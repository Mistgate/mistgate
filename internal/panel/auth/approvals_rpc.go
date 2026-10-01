package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ApprovalService: the owner's inbox for the dangerous changes an MCP agent planned.
// Approving needs a fresh step-up, rejecting does not. Like the token service it is owner-only, signed-in only,
// and a token cannot reach it.

const maxApprovalIDLen = 64

type approvalRPC struct{ s *Service }

// ApprovalHandler returns the Connect path prefix and the handler of ApprovalService. Mount it behind
// RequireSession.
func (s *Service) ApprovalHandler() (string, http.Handler) {
	return adminv1connect.NewApprovalServiceHandler(&approvalRPC{s}, connect.WithReadMaxBytes(64<<10))
}

func toProtoApprovalState(status string) adminv1.ApprovalState {
	switch status {
	case store.PlanAwaiting:
		return adminv1.ApprovalState_APPROVAL_STATE_AWAITING
	case store.PlanApproved:
		return adminv1.ApprovalState_APPROVAL_STATE_APPROVED
	case store.PlanRejected:
		return adminv1.ApprovalState_APPROVAL_STATE_REJECTED
	case store.PlanExpired:
		return adminv1.ApprovalState_APPROVAL_STATE_EXPIRED
	case store.PlanApplying:
		return adminv1.ApprovalState_APPROVAL_STATE_APPLYING
	case store.PlanApplied:
		return adminv1.ApprovalState_APPROVAL_STATE_APPLIED
	case store.PlanFailed:
		return adminv1.ApprovalState_APPROVAL_STATE_FAILED
	case store.PlanCancelled:
		return adminv1.ApprovalState_APPROVAL_STATE_CANCELLED
	}
	return adminv1.ApprovalState_APPROVAL_STATE_UNSPECIFIED
}

// toProtoApproval builds the owner's view of a plan. The facts and danger codes were written by the panel when the
// plan was made; a stored value that does not parse is shown as no facts rather than failing the page. The agent's
// reason stays marked by the SPA as an untrusted quote (the Approval message documents it).
func toProtoApproval(p store.MCPPlan) *adminv1.Approval {
	var facts []struct {
		Key             string            `json:"key"`
		Value           string            `json:"value"`
		Untrusted       bool              `json:"untrusted"`
		Code            string            `json:"code"`
		Params          map[string]string `json:"params"`
		UntrustedParams []string          `json:"untrusted_params"`
	}
	_ = json.Unmarshal([]byte(p.FactsJSON), &facts)
	var danger []string
	_ = json.Unmarshal([]byte(p.Danger), &danger)
	a := &adminv1.Approval{
		Id: p.ID, TokenId: p.TokenID, TokenName: p.TokenName, TokenProfile: toProtoProfile(p.TokenProfile), Tool: p.Tool,
		Danger: danger, Reason: store.Clip(p.Reason, 300), State: toProtoApprovalState(p.Status),
		CreatedUnix: p.CreatedAt.Unix(), ExpiresUnix: p.ExpiresAt.Unix(),
		DecidedByName: p.DecidedByName, DecidedUnix: unixOrZero(p.DecidedAt), AppliedUnix: unixOrZero(p.AppliedAt),
		Result: p.Result, Error: p.Error, OutcomeCode: p.OutcomeCode,
	}
	if p.OutcomeCode != "" {
		_ = json.Unmarshal([]byte(p.OutcomeParams), &a.OutcomeParams)
	}
	if p.Status == store.PlanApplying {
		a.AppliedUnix = 0 // while applying the column holds the start, and the proto says "when it finished"
	}
	for _, f := range facts {
		a.Facts = append(a.Facts, &adminv1.ApprovalFact{Key: f.Key, Value: f.Value, Untrusted: f.Untrusted, Code: f.Code, Params: f.Params, UntrustedParams: f.UntrustedParams})
	}
	return a
}

// ListApprovals returns the approvals waiting for the owner first, then the recent history.
func (r *approvalRPC) ListApprovals(ctx context.Context, req *connect.Request[adminv1.ListApprovalsRequest]) (*connect.Response[adminv1.ListApprovalsResponse], error) {
	s := r.s
	if _, err := ownerSession(ctx); err != nil {
		return nil, err
	}
	now := s.now()
	rows, awaiting, err := s.st.ListApprovals(ctx, now, req.Msg.AwaitingOnly, int(min(req.Msg.HistoryLimit, 200)))
	if err != nil {
		s.log.Error("list approvals", "err", err)
		return nil, errInternal(err)
	}
	resp := &adminv1.ListApprovalsResponse{Awaiting: uint32(awaiting), NowUnix: now.Unix()}
	for _, p := range rows {
		resp.Approvals = append(resp.Approvals, toProtoApproval(p))
	}
	return connect.NewResponse(resp), nil
}

// decide is Approve and Reject: the compare-and-swap, the errors, the audit row.
func (r *approvalRPC) decide(ctx context.Context, admin store.Admin, req connect.AnyRequest, id string, approve bool) (*adminv1.Approval, error) {
	s := r.s
	if id == "" || len(id) > maxApprovalIDLen {
		return nil, invalid("which approval?")
	}
	p, err := s.st.DecideMCPPlan(ctx, id, admin.ID, approve, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such approval"))
	case errors.Is(err, store.ErrPlanState):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this approval is not waiting any more (it was decided, it expired, or its token was revoked)"))
	case err != nil:
		s.log.Error("decide approval", "err", err)
		return nil, errInternal(err)
	}
	action := "approval_reject"
	if approve {
		action = "approval_approve"
	}
	s.audit(ctx, admin.ID, action, "ok", s.clientIP(req), map[string]any{"plan_id": p.ID, "tool": p.Tool, "token_id": p.TokenID})
	return toProtoApproval(p), nil
}

// Approve is the owner agreeing to exactly what the approval lists. Needs a step-up.
func (r *approvalRPC) Approve(ctx context.Context, req *connect.Request[adminv1.ApproveRequest]) (*connect.Response[adminv1.ApproveResponse], error) {
	admin, err := ownerSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	a, err := r.decide(ctx, admin, req, req.Msg.Id, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ApproveResponse{Approval: a}), nil
}

// Reject is the owner refusing. No step-up: refusing is always safe.
func (r *approvalRPC) Reject(ctx context.Context, req *connect.Request[adminv1.RejectRequest]) (*connect.Response[adminv1.RejectResponse], error) {
	admin, err := ownerSession(ctx)
	if err != nil {
		return nil, err
	}
	a, err := r.decide(ctx, admin, req, req.Msg.Id, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RejectResponse{Approval: a}), nil
}
