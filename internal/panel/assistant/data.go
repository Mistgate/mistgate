//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func ProviderKeyAAD(p adminv1.AssistantProvider) string {
	return "assistant-provider:" + providerSlug(p)
}
func ConversationTitleAAD(id string) string   { return "assistant-conversation-title:" + id }
func ConversationSummaryAAD(id string) string { return "assistant-conversation-summary:" + id }
func MessageAAD(id string) string             { return "assistant-message:" + id }
func PlanConfirmAAD(id string) string         { return "assistant-plan-confirm:" + id }
func PlanCardAAD(id string) string            { return "assistant-plan-card:" + id }
func MemoryAAD(id string) string              { return "assistant-memory:" + id }
func PanelNotesAAD() string                   { return "assistant-settings:panel-notes" }

func providerSlug(p adminv1.AssistantProvider) string {
	switch p {
	case adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC:
		return "anthropic"
	case adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK:
		return "deepseek"
	case adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI:
		return "zai"
	case adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD:
		return "ollama_cloud"
	default:
		return "unknown"
	}
}

func (s *Service) providerSpend(ctx context.Context, p adminv1.AssistantProvider, month string) (float64, error) {
	var amount float64
	err := s.st.R.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT SUM(usd) FROM assistant_usage WHERE month=? AND provider=?),0) +
		COALESCE((SELECT SUM(estimate_usd) FROM assistant_budget_reservation WHERE month=? AND provider=? AND state='reserved'),0)`,
		month, int32(p), month, int32(p)).Scan(&amount)
	return amount, err
}

type callReservation struct {
	id, month    string
	estimatedIn  int64
	estimatedUSD float64
}

func maxOutputTokens(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func estimateRequestInputTokens(req modelRequest) int64 {
	size := len(req.System) + len(req.Stable) + 64
	for _, raw := range req.History {
		size += len(raw)
	}
	for _, tool := range req.Tools {
		size += len(tool.Name) + len(tool.Description) + len(tool.InputSchema) + 48
	}
	// One token per UTF-8 byte is a hard upper bound for the text sent to the provider's tokenizer.
	return max(1, int64(size))
}

func (s *Service) reserveProviderCall(ctx context.Context, adminID string, p adminv1.AssistantProvider, model string, budget float64, req modelRequest) (callReservation, error) {
	maxOutput := maxOutputTokens(req.MaxOutputTokens, defaultMaxOutputTokens)
	inTokens := estimateRequestInputTokens(req)
	estimate := estimatePrice(p, model, inTokens, maxOutput)
	if math.IsNaN(estimate) || math.IsInf(estimate, 0) || estimate < 0 {
		return callReservation{}, apiError("budget_reached")
	}
	month := monthKey(s.now())
	reservationID := store.NewID("asres_")
	accepted, err := s.st.ReserveAssistantBudget(ctx, reservationID, month, adminID, int32(p), model, estimate, budget, s.now())
	if err != nil {
		return callReservation{}, err
	}
	if !accepted {
		return callReservation{}, apiError("budget_reached")
	}
	return callReservation{id: reservationID, month: month, estimatedIn: inTokens, estimatedUSD: estimate}, nil
}

func (s *Service) reconcileProviderCall(ctx context.Context, reservation callReservation, adminID string, p adminv1.AssistantProvider, model string, input, output int64, reported, turn bool) float64 {
	amount := reservation.estimatedUSD
	if reported {
		input, output = max(input, 0), max(output, 0)
		amount = estimatePrice(p, model, input, output)
	} else {
		input, output = 0, 0
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		amount = reservation.estimatedUSD
	}
	if err := s.st.ReconcileAssistantBudget(context.WithoutCancel(ctx), reservation.id, reservation.month, adminID, int32(p), input, output, amount, turn, s.now()); err != nil {
		s.log.Warn("assistant usage reconciliation failed", "provider", p.String(), "err", err)
	}
	return amount
}

type providerState struct {
	key    []byte
	model  string
	budget float64
}

func (s *Service) readProvider(ctx context.Context, p adminv1.AssistantProvider) (providerState, error) {
	row, err := s.readProviderRow(ctx, p)
	if err != nil {
		return providerState{}, err
	}
	var key []byte
	if len(row.sealed) > 0 {
		key, err = s.vault.Open(row.sealed, ProviderKeyAAD(p))
		if err != nil {
			return providerState{}, errors.New("assistant provider key is unavailable")
		}
	}
	return providerState{key: key, model: row.model, budget: row.budget}, nil
}

func (s *Service) validateAdminAssistant(ctx context.Context, adminID string) error {
	if adminID == "" {
		return errors.New("assistant admin is missing")
	}
	if s.st == nil || s.vault == nil {
		return errors.New("assistant storage is unavailable")
	}
	return s.ensureRows(ctx)
}

func (s *Service) assistantTurnAllowed(ctx context.Context, adminID, role string) (bool, error) {
	current, err := s.st.Admin(ctx, adminID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.Role != role {
		return false, nil
	}
	var enabled int
	var audience adminv1.AssistantAudience
	if err = s.st.R.QueryRowContext(ctx, `SELECT enabled,audience FROM assistant_settings WHERE id=1`).Scan(&enabled, &audience); err != nil {
		return false, err
	}
	return enabled != 0 && audienceAllows(audience, current.Role), nil
}

func estimatePrice(p adminv1.AssistantProvider, model string, input, output int64) float64 {
	price := priceFor(p, model)
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	return (float64(input)*price.input + float64(output)*price.output) / 1_000_000
}

func cleanContextText(s string, limit int) string {
	s = mcp.ScrubText(s)
	var out strings.Builder
	n := 0
	for _, r := range s {
		if n >= limit {
			break
		}
		if r == '\u0000' || r == '\u202e' || r == '\u202d' || r == '\u200e' || r == '\u200f' {
			continue
		}
		out.WriteRune(r)
		n++
	}
	return strings.TrimSpace(out.String())
}

var assistantCredentialLabel = regexp.MustCompile(`(?i)(^|[^[:alnum:]_])((?:password|passwd|pwd|pass|secret|token|api[\s_-]*key|private[\s_-]*key|key|license(?:[\s_-]*key)?|licence(?:[\s_-]*key)?|пароль|токен|ключ|лицензия))(\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)

func redactUserMessage(s string) string {
	s = mcp.ScrubText(s)
	return assistantCredentialLabel.ReplaceAllString(s, `${1}${2}${3}[removed: looks like a password]`)
}

func safeToolArgs(raw []byte) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "[invalid JSON input]"
	}
	var scrub func(any) any
	scrub = func(input any) any {
		switch v := input.(type) {
		case map[string]any:
			for key, value := range v {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
				if strings.Contains(normalized, "password") || strings.Contains(normalized, "apikey") || strings.Contains(normalized, "privatekey") || strings.Contains(normalized, "presharedkey") || strings.Contains(normalized, "secret") || strings.HasSuffix(normalized, "token") {
					v[key] = "[redacted]"
				} else {
					v[key] = scrub(value)
				}
			}
			return v
		case []any:
			for i := range v {
				v[i] = scrub(v[i])
			}
			return v
		default:
			return input
		}
	}
	b, err := json.Marshal(scrub(value))
	if err != nil {
		return "[invalid JSON input]"
	}
	return mcp.ScrubJSON(string(b))
}

func validateAssistantText(s string, max int) bool {
	return utf8.RuneCountInString(s) > 0 && utf8.RuneCountInString(s) <= max
}

func newConversationID() string  { return store.NewID("cnv_") }
func newMessageID() string       { return store.NewID("msg_") }
func newAssistantPlanID() string { return store.NewID("apl_") }
func newMemoryID() string        { return store.NewID("mem_") }
