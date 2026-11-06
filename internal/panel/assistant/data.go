package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
	err := s.st.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(usd),0) FROM assistant_usage WHERE month=? AND provider=?`, month, int32(p)).Scan(&amount)
	return amount, err
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

func (s *Service) recordUsage(ctx context.Context, adminID string, p adminv1.AssistantProvider, model string, input, output int64, turn bool) float64 {
	amount := estimatePrice(p, model, input, output)
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		amount = 0
	}
	turns := 0
	if turn {
		turns = 1
	}
	_, err := s.st.W.ExecContext(context.WithoutCancel(ctx), `INSERT INTO assistant_usage(month,admin_id,provider,input_tokens,output_tokens,usd,turns) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(month,admin_id,provider) DO UPDATE SET input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,usd=usd+excluded.usd,turns=turns+excluded.turns`, monthKey(s.now()), adminID, int32(p), input, output, amount, turns)
	if err != nil {
		s.log.Warn("assistant usage write failed", "provider", p.String(), "err", err)
	}
	return amount
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
