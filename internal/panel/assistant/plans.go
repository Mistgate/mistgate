//go:build !js

package assistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type assistantPlanRow struct {
	id, conversation, admin, token, tool, state string
	confirm, card                               []byte
	inbox                                       int
	created, expires, updated                   int64
}

func (s *Service) assistantPlan(ctx context.Context, adminID, conversationID, planID string) (assistantPlanRow, error) {
	var p assistantPlanRow
	err := s.st.R.QueryRowContext(ctx, `SELECT id,conversation_id,admin_id,token_id,tool,confirm_enc,card_enc,inbox_only,state,created_at,expires_at,updated_at FROM assistant_plan WHERE id=? AND conversation_id=? AND admin_id=?`, planID, conversationID, adminID).
		Scan(&p.id, &p.conversation, &p.admin, &p.token, &p.tool, &p.confirm, &p.card, &p.inbox, &p.state, &p.created, &p.expires, &p.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, codeConnect("plan_not_pending")
	}
	return p, err
}

func (s *Service) savePlanCard(ctx context.Context, id string, card *adminv1.PlanCard, state string) error {
	blob, err := sealJSON(s.vault, card, PlanCardAAD(id))
	if err != nil {
		return err
	}
	_, err = s.st.W.ExecContext(ctx, `UPDATE assistant_plan SET card_enc=?,state=?,updated_at=? WHERE id=?`, blob, state, s.now().Unix(), id)
	return err
}

func profileForRole(role string) string {
	switch role {
	case store.RoleReadonly:
		return store.ProfileReadonly
	case store.RoleHelper:
		return store.ProfileOperator
	default:
		return store.ProfileAdmin
	}
}

func (s *Service) assistantSession(ctx context.Context, admin store.Admin, tokenID string) (mcp.AssistantSession, error) {
	s.mu.RLock()
	runtime := s.tools
	s.mu.RUnlock()
	if runtime == nil {
		return nil, errors.New("assistant panel tools are unavailable")
	}
	profile := profileForRole(admin.Role)
	if tokenID != "" && tokenID == store.ScheduledTokenID(admin.ID) {
		// a plan a scheduled task prepared: its apply runs with the identity that made it, the operator profile
		return runtime.OpenAssistant(ctx, mcp.AssistantIdentity{TokenID: tokenID, Profile: store.ProfileOperator, AdminID: admin.ID, AdminName: admin.DisplayName, Scheduled: true})
	}
	if tokenID == "" {
		token, err := s.st.EnsureAssistantToken(ctx, admin.ID, profile, s.now())
		if err != nil {
			return nil, err
		}
		tokenID = token.ID
	}
	return runtime.OpenAssistant(ctx, mcp.AssistantIdentity{TokenID: tokenID, Profile: profile, AdminID: admin.ID, AdminName: admin.DisplayName})
}

func (s *Service) ApplyPlan(ctx context.Context, req *connect.Request[adminv1.ApplyPlanRequest]) (*connect.Response[adminv1.ApplyPlanResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireAccess(ctx, admin); err != nil {
		return nil, err
	}
	if req.Msg.ConversationId == "" || req.Msg.PlanId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conversation and plan are required"))
	}
	p, err := s.assistantPlan(ctx, admin.ID, req.Msg.ConversationId, req.Msg.PlanId)
	if err != nil {
		return nil, err
	}
	card, err := s.currentPlanCard(ctx, p.id)
	if err != nil {
		return nil, err
	}
	if card.State == adminv1.PlanState_PLAN_STATE_APPLIED || card.State == adminv1.PlanState_PLAN_STATE_FAILED || card.State == adminv1.PlanState_PLAN_STATE_REJECTED || card.State == adminv1.PlanState_PLAN_STATE_EXPIRED || card.State == adminv1.PlanState_PLAN_STATE_DISMISSED {
		return nil, codeConnect("plan_not_pending")
	}
	if p.inbox != 0 || card.InboxOnly {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this plan must be confirmed in the approval inbox"))
	}
	mcpPlan, err := s.st.GetMCPPlan(ctx, p.id)
	if err != nil {
		return nil, codeConnect("plan_not_pending")
	}
	if mcpPlan.NeedsApproval {
		if admin.Role != store.RoleOwner {
			card.State = adminv1.PlanState_PLAN_STATE_WAITING_OWNER
			if err = s.savePlanCard(ctx, p.id, card, "waiting_owner"); err != nil {
				return nil, err
			}
			return connect.NewResponse(&adminv1.ApplyPlanResponse{Plan: card}), nil
		}
		if s.auth == nil {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("step-up is unavailable"))
		}
		if err = s.auth.RequireStepUp(ctx); err != nil {
			return nil, err
		}
		if mcpPlan.Status == store.PlanAwaiting {
			if _, err = s.auth.ApproveAssistantMCPPlan(ctx, p.id, admin); err != nil {
				return nil, codeConnect("plan_not_pending")
			}
		} else if mcpPlan.Status != store.PlanApproved && mcpPlan.Status != store.PlanApplying {
			return nil, codeConnect("plan_not_pending")
		}
	} else if mcpPlan.Status != store.PlanPlanned && mcpPlan.Status != store.PlanApplying {
		return nil, codeConnect("plan_not_pending")
	}
	plain, err := s.vault.Open(p.confirm, PlanConfirmAAD(p.id))
	if err != nil {
		return nil, errors.New("assistant plan confirmation is unavailable")
	}
	defer zero(plain)
	session, err := s.assistantSession(ctx, admin, p.token)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	args, _ := json.Marshal(map[string]string{"confirm_token": string(plain)})
	result, err := session.CallTool(ctx, p.tool+"_apply", args)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return s.syncApplyCardFromMCP(ctx, p.id, card, result.Text)
	}
	var applied struct {
		Status string `json:"status"`
		Result string `json:"result"`
	}
	if json.Unmarshal([]byte(result.Text), &applied) != nil || applied.Status == "" {
		return s.syncApplyCardFromMCP(ctx, p.id, card, "plan apply returned an invalid result")
	}
	if applied.Status == mcp.StatusApplied || applied.Status == "applied" {
		card.State = adminv1.PlanState_PLAN_STATE_APPLIED
		card.Result = cleanContextText(applied.Result, 300)
		card.Error = ""
		if err = s.savePlanCard(ctx, p.id, card, "applied"); err != nil {
			return nil, err
		}
	} else {
		return s.syncApplyCardFromMCP(ctx, p.id, card, applied.Result)
	}
	return connect.NewResponse(&adminv1.ApplyPlanResponse{Plan: card}), nil
}

// syncApplyCardFromMCP persists terminal MCP outcomes only. An active plan, including one already applying in
// another request, remains pending in the assistant card until MCP reports a terminal state.
func (s *Service) syncApplyCardFromMCP(ctx context.Context, planID string, card *adminv1.PlanCard, message string) (*connect.Response[adminv1.ApplyPlanResponse], error) {
	current, err := s.st.GetMCPPlan(ctx, planID)
	if err != nil {
		return nil, codeConnect("plan_not_pending")
	}
	state, stored := "", false
	switch current.Status {
	case store.PlanApplied:
		card.State = adminv1.PlanState_PLAN_STATE_APPLIED
		card.Result = cleanContextText(current.Result, 300)
		card.Error = ""
		state, stored = "applied", true
	case store.PlanFailed:
		card.State = adminv1.PlanState_PLAN_STATE_FAILED
		card.Error = cleanContextText(message, 300)
		if card.Error == "" {
			card.Error = cleanContextText(current.Error, 300)
		}
		if card.Error == "" {
			card.Error = "plan apply failed"
		}
		state, stored = "failed", true
	case store.PlanRejected:
		card.State = adminv1.PlanState_PLAN_STATE_REJECTED
		card.Error = cleanContextText(message, 300)
		state, stored = "rejected", true
	case store.PlanExpired:
		card.State = adminv1.PlanState_PLAN_STATE_EXPIRED
		card.Error = cleanContextText(message, 300)
		state, stored = "expired", true
	case store.PlanCancelled:
		card.State = adminv1.PlanState_PLAN_STATE_DISMISSED
		card.Error = cleanContextText(message, 300)
		state, stored = "dismissed", true
	default:
		return connect.NewResponse(&adminv1.ApplyPlanResponse{Plan: card}), nil
	}
	if stored {
		if err = s.savePlanCard(ctx, planID, card, state); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&adminv1.ApplyPlanResponse{Plan: card}), nil
}

func (s *Service) DismissPlan(ctx context.Context, req *connect.Request[adminv1.DismissPlanRequest]) (*connect.Response[adminv1.DismissPlanResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.ConversationId == "" || req.Msg.PlanId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conversation and plan are required"))
	}
	p, err := s.assistantPlan(ctx, admin.ID, req.Msg.ConversationId, req.Msg.PlanId)
	if err != nil {
		return nil, err
	}
	card, err := s.currentPlanCard(ctx, p.id)
	if err != nil {
		return nil, err
	}
	if card.State != adminv1.PlanState_PLAN_STATE_PROPOSED && card.State != adminv1.PlanState_PLAN_STATE_WAITING_OWNER {
		return nil, codeConnect("plan_not_pending")
	}
	cancelled, err := s.st.CancelAssistantMCPPlan(ctx, p.id, p.token, s.now())
	if err != nil {
		return nil, err
	}
	if !cancelled {
		return nil, codeConnect("plan_not_pending")
	}
	card.State = adminv1.PlanState_PLAN_STATE_DISMISSED
	card.Error = ""
	if err = s.savePlanCard(ctx, p.id, card, "dismissed"); err != nil {
		return nil, err
	}
	s.audit(ctx, admin, "assistant_plan_dismiss", "ok", map[string]any{"plan_id": p.id, "tool": p.tool})
	return connect.NewResponse(&adminv1.DismissPlanResponse{Plan: card}), nil
}

func (s *Service) StopTurn(ctx context.Context, req *connect.Request[adminv1.StopTurnRequest]) (*connect.Response[adminv1.StopTurnResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.ConversationId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conversation id is required"))
	}
	if _, _, err = s.conversation(ctx, admin.ID, req.Msg.ConversationId); err != nil {
		return nil, err
	}
	requested, err := s.st.RequestAssistantStop(ctx, req.Msg.ConversationId, admin.ID, s.now())
	if err != nil {
		return nil, err
	}
	if requested {
		s.mu.Lock()
		if active, ok := s.running[req.Msg.ConversationId]; ok {
			active.stopped.Store(true)
			active.cancel()
		}
		s.mu.Unlock()
	}
	return connect.NewResponse(&adminv1.StopTurnResponse{}), nil
}
