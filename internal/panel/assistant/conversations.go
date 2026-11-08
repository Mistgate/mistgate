//go:build !js

package assistant

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

type messageBody struct {
	Text       string                `json:"text,omitempty"`
	Context    *adminv1.PageContext  `json:"context,omitempty"`
	Tools      []*adminv1.ToolCall   `json:"tools,omitempty"`
	Plans      []*adminv1.PlanCard   `json:"plans,omitempty"`
	Remembered []*adminv1.MemoryNote `json:"remembered,omitempty"`
	End        string                `json:"end,omitempty"`
	Transcript []json.RawMessage     `json:"transcript,omitempty"`
}

type messageRow struct {
	id, role string
	segment  int
	body     []byte
	created  int64
}

func sealJSON(v *vault.Vault, value any, aad string) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return v.Seal(b, aad), nil
}
func openJSON(v *vault.Vault, blob []byte, aad string, out any) error {
	plain, err := v.Open(blob, aad)
	if err != nil {
		return errors.New("assistant stored content is unavailable")
	}
	defer zero(plain)
	if err = json.Unmarshal(plain, out); err != nil {
		return errors.New("assistant stored content is invalid")
	}
	return nil
}
func sealText(v *vault.Vault, text, aad string) []byte { return v.Seal([]byte(text), aad) }
func openText(v *vault.Vault, blob []byte, aad string) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	plain, err := v.Open(blob, aad)
	if err != nil {
		return "", errors.New("assistant stored content is unavailable")
	}
	defer zero(plain)
	return string(plain), nil
}

func (s *Service) createConversation(ctx context.Context, adminID string, now time.Time) (string, error) {
	return s.createTaskConversation(ctx, adminID, "", "", now)
}

// createTaskConversation is createConversation for the runs of a scheduled task: it carries the task's id and, as its title,
// the task's name (a person's conversation gets its title from the model).
func (s *Service) createTaskConversation(ctx context.Context, adminID, taskID, title string, now time.Time) (string, error) {
	id := newConversationID()
	_, err := s.st.W.ExecContext(ctx, `INSERT INTO assistant_conversation(id,admin_id,title_enc,summary_enc,created_at,updated_at,segment,task_id) VALUES(?,?,?,?,?,?,0,?)`, id, adminID, sealText(s.vault, title, ConversationTitleAAD(id)), sealText(s.vault, "", ConversationSummaryAAD(id)), now.Unix(), now.Unix(), taskID)
	return id, err
}

func (s *Service) saveMessage(ctx context.Context, conv, admin, role string, segment int, body messageBody, now time.Time) (string, error) {
	id := newMessageID()
	return s.saveMessageID(ctx, id, conv, admin, role, segment, body, now)
}

func (s *Service) saveMessageID(ctx context.Context, id, conv, admin, role string, segment int, body messageBody, now time.Time) (string, error) {
	sealed, err := sealJSON(s.vault, body, MessageAAD(id))
	if err != nil {
		return "", err
	}
	result, err := s.st.W.ExecContext(ctx, `INSERT INTO assistant_message(id,conversation_id,admin_id,role,segment,body_enc,created_at)
		SELECT ?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM assistant_conversation WHERE id=? AND admin_id=?)`, id, conv, admin, role, segment, sealed, now.Unix(), conv, admin)
	if err != nil {
		return "", err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if inserted == 0 {
		return "", store.ErrAssistantConversationGone
	}
	_, err = s.st.W.ExecContext(ctx, `UPDATE assistant_conversation SET updated_at=? WHERE id=? AND admin_id=?`, now.Unix(), conv, admin)
	return id, err
}

func (s *Service) conversation(ctx context.Context, adminID, id string) (*adminv1.ConversationSummary, int, error) {
	summary := &adminv1.ConversationSummary{}
	var titleEnc, summaryEnc []byte
	var segment int
	err := s.st.R.QueryRowContext(ctx, `SELECT id,title_enc,summary_enc,created_at,updated_at,segment,task_id FROM assistant_conversation WHERE id=? AND admin_id=?`, id, adminID).Scan(&summary.Id, &titleEnc, &summaryEnc, &summary.CreatedUnix, &summary.UpdatedUnix, &segment, &summary.TaskId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, connect.NewError(connect.CodeNotFound, errors.New("conversation not found"))
	}
	if err != nil {
		return nil, 0, err
	}
	summary.Title, err = openText(s.vault, titleEnc, ConversationTitleAAD(id))
	if err != nil {
		return nil, 0, err
	}
	var count uint32
	err = s.st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_message WHERE conversation_id=? AND admin_id=?`, id, adminID).Scan(&count)
	if err != nil {
		return nil, 0, err
	}
	summary.Messages = count
	return summary, segment, nil
}

func (s *Service) ListConversations(ctx context.Context, req *connect.Request[adminv1.ListConversationsRequest]) (*connect.Response[adminv1.ListConversationsResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	limit := int(req.Msg.Limit)
	if limit == 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	if limit < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conversation limit is invalid"))
	}
	offset := 0
	if req.Msg.PageToken != "" {
		raw, e := base64.RawURLEncoding.DecodeString(req.Msg.PageToken)
		if e != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page token is invalid"))
		}
		offset, e = strconv.Atoi(string(raw))
		if e != nil || offset < 0 || offset > 10000 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page token is invalid"))
		}
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT id,title_enc,summary_enc,created_at,updated_at,(SELECT count(*) FROM assistant_message m WHERE m.conversation_id=c.id AND m.admin_id=c.admin_id),task_id FROM assistant_conversation c WHERE admin_id=? ORDER BY updated_at DESC,id DESC LIMIT 5000`, admin.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	words := searchWords(req.Msg.Query)
	var matches []*adminv1.ConversationSummary
	for rows.Next() {
		var id string
		var titleEnc, summaryEnc []byte
		var item adminv1.ConversationSummary
		if err = rows.Scan(&id, &titleEnc, &summaryEnc, &item.CreatedUnix, &item.UpdatedUnix, &item.Messages, &item.TaskId); err != nil {
			return nil, err
		}
		item.Id = id
		item.Title, err = openText(s.vault, titleEnc, ConversationTitleAAD(id))
		if err != nil {
			return nil, err
		}
		summary, e := openText(s.vault, summaryEnc, ConversationSummaryAAD(id))
		if e != nil {
			return nil, e
		}
		if allWords(words, item.Title+" "+summary) {
			matches = append(matches, &item)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	resp := &adminv1.ListConversationsResponse{}
	if offset > len(matches) {
		offset = len(matches)
	}
	end := min(offset+limit, len(matches))
	resp.Conversations = matches[offset:end]
	if end < len(matches) {
		resp.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetConversation(ctx context.Context, req *connect.Request[adminv1.GetConversationRequest]) (*connect.Response[adminv1.GetConversationResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conversation id is required"))
	}
	summary, _, err := s.conversation(ctx, admin.ID, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT id,role,segment,body_enc,created_at FROM assistant_message WHERE conversation_id=? AND admin_id=? ORDER BY rowid`, req.Msg.Id, admin.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &adminv1.GetConversationResponse{Conversation: summary}
	for rows.Next() {
		var row messageRow
		if err = rows.Scan(&row.id, &row.role, &row.segment, &row.body, &row.created); err != nil {
			return nil, err
		}
		var body messageBody
		if err = openJSON(s.vault, row.body, MessageAAD(row.id), &body); err != nil {
			return nil, err
		}
		role := adminv1.ChatRole_CHAT_ROLE_USER
		if row.role == "assistant" || row.role == "summary" {
			role = adminv1.ChatRole_CHAT_ROLE_ASSISTANT
		}
		if row.role == "summary" && body.Text != "" {
			body.Text = "Earlier conversation summary: " + body.Text
		}
		for i, plan := range body.Plans {
			if plan != nil {
				if fresh, e := s.currentPlanCard(ctx, plan.PlanId); e == nil {
					body.Plans[i] = fresh
				}
			}
		}
		resp.Messages = append(resp.Messages, &adminv1.ChatMessage{Id: row.id, Role: role, Text: body.Text, CreatedUnix: row.created, Context: body.Context, Tools: body.Tools, Plans: body.Plans, Remembered: body.Remembered, End: body.End})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var n int
	err = s.st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_lock WHERE conversation_id=? AND admin_id=? AND expires_at>?`, req.Msg.Id, admin.ID, s.now().Unix()).Scan(&n)
	if err != nil {
		return nil, err
	}
	resp.Running = n > 0
	return connect.NewResponse(resp), nil
}

func (s *Service) currentPlanCard(ctx context.Context, id string) (*adminv1.PlanCard, error) {
	var state string
	var blob []byte
	var expires int64
	err := s.st.R.QueryRowContext(ctx, `SELECT state,card_enc,expires_at FROM assistant_plan WHERE id=?`, id).Scan(&state, &blob, &expires)
	if err != nil {
		return nil, err
	}
	var card adminv1.PlanCard
	if err = openJSON(s.vault, blob, PlanCardAAD(id), &card); err != nil {
		return nil, err
	}
	if s.now().Unix() >= expires && card.State != adminv1.PlanState_PLAN_STATE_DISMISSED {
		card.State = adminv1.PlanState_PLAN_STATE_EXPIRED
	}
	plan, err := s.st.GetMCPPlan(ctx, id)
	if err == nil {
		switch {
		case card.State == adminv1.PlanState_PLAN_STATE_DISMISSED:
		case plan.Status == store.PlanPlanned:
			if card.State == adminv1.PlanState_PLAN_STATE_WAITING_OWNER {
				card.State = adminv1.PlanState_PLAN_STATE_PROPOSED
			}
		case plan.Status == store.PlanAwaiting:
			if state == "waiting_owner" {
				card.State = adminv1.PlanState_PLAN_STATE_WAITING_OWNER
			}
		case plan.Status == store.PlanApproved:
			card.State = adminv1.PlanState_PLAN_STATE_WAITING_OWNER
		case plan.Status == store.PlanApplied:
			card.State = adminv1.PlanState_PLAN_STATE_APPLIED
			card.Result = plan.Result
		case plan.Status == store.PlanFailed:
			card.State = adminv1.PlanState_PLAN_STATE_FAILED
			card.Error = plan.Error
			card.OutcomeCode = plan.OutcomeCode
		case plan.Status == store.PlanRejected:
			card.State = adminv1.PlanState_PLAN_STATE_REJECTED
		case plan.Status == store.PlanExpired:
			card.State = adminv1.PlanState_PLAN_STATE_EXPIRED
		case plan.Status == store.PlanCancelled:
			card.State = adminv1.PlanState_PLAN_STATE_FAILED
			card.Error = "plan was cancelled"
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	sealed, e := sealJSON(s.vault, &card, PlanCardAAD(id))
	if e == nil {
		_, _ = s.st.W.ExecContext(ctx, `UPDATE assistant_plan SET card_enc=?,state=?,updated_at=? WHERE id=?`, sealed, planStateString(card.State), s.now().Unix(), id)
	}
	return &card, nil
}

func planStateString(s adminv1.PlanState) string {
	switch s {
	case adminv1.PlanState_PLAN_STATE_WAITING_OWNER:
		return "waiting_owner"
	case adminv1.PlanState_PLAN_STATE_APPLIED:
		return "applied"
	case adminv1.PlanState_PLAN_STATE_FAILED:
		return "failed"
	case adminv1.PlanState_PLAN_STATE_REJECTED:
		return "rejected"
	case adminv1.PlanState_PLAN_STATE_DISMISSED:
		return "dismissed"
	case adminv1.PlanState_PLAN_STATE_EXPIRED:
		return "expired"
	default:
		return "proposed"
	}
}

func (s *Service) DeleteConversation(ctx context.Context, req *connect.Request[adminv1.DeleteConversationRequest]) (*connect.Response[adminv1.DeleteConversationResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conversation id is required"))
	}
	if _, _, err = s.conversation(ctx, admin.ID, req.Msg.Id); err != nil {
		return nil, err
	}
	s.cancelAssistantTurns(admin.ID, req.Msg.Id, false)
	deleted, err := s.st.DeleteAssistantConversation(ctx, admin.ID, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("conversation not found"))
	}
	return connect.NewResponse(&adminv1.DeleteConversationResponse{}), nil
}

func (s *Service) DeleteAllConversations(ctx context.Context, req *connect.Request[adminv1.DeleteAllConversationsRequest]) (*connect.Response[adminv1.DeleteAllConversationsResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.AllAdmins {
		if admin.Role != store.RoleOwner {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
		}
		if s.auth == nil {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("step-up is unavailable"))
		}
		if err = s.auth.RequireStepUp(ctx); err != nil {
			return nil, err
		}
	}
	if req.Msg.AllAdmins {
		s.cancelAssistantTurns("", "", true)
	} else {
		s.cancelAssistantTurns(admin.ID, "", false)
	}
	deleted, err := s.st.DeleteAssistantConversations(ctx, admin.ID, req.Msg.AllAdmins)
	if err != nil {
		return nil, err
	}
	if req.Msg.AllAdmins {
		s.audit(ctx, admin, "assistant_conversations_delete_all", "ok", map[string]any{"all_admins": true})
	}
	return connect.NewResponse(&adminv1.DeleteAllConversationsResponse{Deleted: uint32(max(deleted, 0))}), nil
}

func (s *Service) cancelAssistantTurns(adminID, conversationID string, all bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, active := range s.running {
		if all || conversationID != "" && id == conversationID || conversationID == "" && active.adminID == adminID {
			active.cancel()
		}
	}
}

func searchWords(query string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(strings.TrimSpace(query))) {
		if len(w) > 50 {
			w = w[:50]
		}
		out = append(out, w)
	}
	return out
}
func allWords(words []string, haystack string) bool {
	h := strings.ToLower(haystack)
	for _, w := range words {
		if !strings.Contains(h, w) {
			return false
		}
	}
	return true
}
func (s *Service) Retention(ctx context.Context, now time.Time) error {
	return s.st.AssistantRetention(ctx, now)
}

func assistantPageContext(input *adminv1.PageContext) *adminv1.PageContext {
	if input == nil {
		return nil
	}
	route := ""
	if strings.HasPrefix(input.Route, "/") && !strings.HasPrefix(input.Route, "//") && !strings.ContainsAny(input.Route, "?#\\\r\n\t \"'") {
		route = cleanContextText(input.Route, 300)
	}
	return &adminv1.PageContext{Kind: cleanContextText(input.Kind, 40), Id: cleanContextText(input.Id, 100), Label: cleanContextText(input.Label, 120), Route: route}
}
