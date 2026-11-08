//go:build !js

package assistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// Config supplies the signed-in AssistantService dependencies. Endpoint overrides and clients exist only for tests;
// production uses the fixed HTTPS URLs in provider_prices.go.
type Config struct {
	Store         *store.Store
	Vault         *vault.Vault
	Auth          *auth.Service
	Now           func() time.Time
	Log           *slog.Logger
	ProviderBases map[adminv1.AssistantProvider]string
	HTTPClients   map[adminv1.AssistantProvider]*http.Client
	Providers     map[adminv1.AssistantProvider]adapter
}

type Service struct {
	st        *store.Store
	vault     *vault.Vault
	auth      *auth.Service
	now       func() time.Time
	log       *slog.Logger
	providers map[adminv1.AssistantProvider]adapter
	mu        sync.RWMutex
	tools     mcp.AssistantRuntime
	running   map[string]activeAssistantTurn
	// retryWaits are the waits between the attempts of a scheduled run's or a follow-up's model call (nil: 5 s, then 30 s).
	retryWaits []time.Duration
	// notify sends a follow-up's problem to Telegram (SetFollowUpNotifier); nil: nothing is sent
	notify FollowUpNotifier
}

type activeAssistantTurn struct {
	adminID string
	cancel  context.CancelFunc
	stopped *atomic.Bool
}

// New creates the AssistantService and its one guarded HTTP client per provider.
func New(c Config) *Service {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	s := &Service{st: c.Store, vault: c.Vault, auth: c.Auth, now: c.Now, log: c.Log, providers: make(map[adminv1.AssistantProvider]adapter), running: make(map[string]activeAssistantTurn)}
	for provider, injected := range c.Providers {
		s.providers[provider] = injected
	}
	for _, spec := range providerSpecs {
		if s.providers[spec.provider] != nil {
			continue
		}
		base := spec.base
		if override := c.ProviderBases[spec.provider]; override != "" {
			base = override
		}
		transport, err := newProviderHTTP(base, c.HTTPClients[spec.provider])
		if err != nil {
			c.Log.Error("assistant provider configuration", "provider", spec.provider.String(), "err", err)
			continue
		}
		if spec.provider == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC {
			s.providers[spec.provider] = &anthropicAdapter{http: transport}
		} else {
			s.providers[spec.provider] = &openAIAdapter{kind: spec.provider, http: transport, models: spec.curated, defaultModel: spec.defaultModel}
		}
	}
	if s.st != nil && s.vault != nil {
		if err := s.ensureRows(context.Background()); err != nil {
			s.log.Warn("assistant storage initialization failed", "err", err)
		}
	}
	return s
}

// SetToolRuntime installs the MCP server once the authenticated admin API has been assembled.
func (s *Service) SetToolRuntime(runtime mcp.AssistantRuntime) {
	s.mu.Lock()
	s.tools = runtime
	s.mu.Unlock()
}

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewAssistantServiceHandler(s, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}

func (s *Service) ensureRows(ctx context.Context) error {
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO assistant_settings (id) VALUES (1) ON CONFLICT(id) DO NOTHING`); err != nil {
		return err
	}
	var legacyNotes string
	var notesEnc []byte
	if err := s.st.R.QueryRowContext(ctx, `SELECT panel_notes,panel_notes_enc FROM assistant_settings WHERE id=1`).Scan(&legacyNotes, &notesEnc); err != nil {
		return err
	}
	if legacyNotes != "" && len(notesEnc) == 0 {
		sealed := sealText(s.vault, legacyNotes, PanelNotesAAD())
		if _, err := s.st.W.ExecContext(ctx, `UPDATE assistant_settings SET panel_notes='',panel_notes_enc=? WHERE id=1 AND panel_notes_enc=X'' AND panel_notes=?`, sealed, legacyNotes); err != nil {
			return err
		}
	}
	for _, spec := range providerSpecs {
		if _, err := s.st.W.ExecContext(ctx, `INSERT INTO assistant_provider (provider) VALUES (?) ON CONFLICT(provider) DO NOTHING`, int32(spec.provider)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, admin store.Admin, action, result string, params map[string]any) {
	if s.st == nil {
		return
	}
	b, _ := json.Marshal(params)
	if len(params) == 0 {
		b = []byte(`{}`)
	}
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: string(b), Result: result, Source: store.AuditPanel}); err != nil {
		s.log.Warn("assistant audit write failed", "action", action, "err", err)
	}
}

func adminFrom(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok {
		return store.Admin{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in required"))
	}
	return a, nil
}

func (s *Service) access(ctx context.Context, admin store.Admin) (*adminv1.GetAssistantAccessResponse, error) {
	if err := s.ensureRows(ctx); err != nil {
		return nil, err
	}
	settings, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.GetAssistantAccessResponse{Provider: settings.provider, ReadOnly: admin.Role == store.RoleReadonly}
	if !settings.enabled {
		resp.Reason = "assistant_off"
		return resp, nil
	}
	if !audienceAllows(settings.audience, admin.Role) {
		resp.Reason = "not_allowed"
		return resp, nil
	}
	resp.Effort = settings.effort
	if resp.Choices, err = s.chatChoices(ctx, settings.provider); err != nil {
		return nil, err
	}
	provider := settings.provider
	if provider == adminv1.AssistantProvider_ASSISTANT_PROVIDER_UNSPECIFIED {
		resp.Reason = "no_provider"
		return resp, nil
	}
	state, err := s.readProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	defer zero(state.key)
	if len(state.key) == 0 {
		resp.Reason = "no_provider"
		return resp, nil
	}
	resp.Model = state.model
	if resp.Model == "" {
		resp.Model = defaultModel(provider)
	}
	if spent, err := s.providerSpend(ctx, provider, monthKey(s.now())); err != nil {
		return nil, err
	} else if state.budget == 0 || spent >= state.budget {
		resp.Reason = "budget_reached"
		return resp, nil
	}
	resp.Available = true
	return resp, nil
}

// chatChoices are the connected providers for the chat's picker, the panel's own first, then in the settings' order.
func (s *Service) chatChoices(ctx context.Context, first adminv1.AssistantProvider) ([]*adminv1.ChatProviderChoice, error) {
	var out []*adminv1.ChatProviderChoice
	for _, spec := range providerSpecs {
		row, err := s.readProviderRow(ctx, spec.provider)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(row.sealed) == 0 {
			continue
		}
		model := row.model
		if model == "" {
			model = defaultModel(spec.provider)
		}
		spent, err := s.providerSpend(ctx, spec.provider, monthKey(s.now()))
		if err != nil {
			return nil, err
		}
		p := listedPrice(spec, model)
		choice := &adminv1.ChatProviderChoice{Provider: spec.provider, Model: model, InputUsdPerMtok: p.input, OutputUsdPerMtok: p.output, BudgetReached: row.budget <= 0 || spent >= row.budget}
		if spec.provider == first {
			out = append([]*adminv1.ChatProviderChoice{choice}, out...)
		} else {
			out = append(out, choice)
		}
	}
	return out, nil
}

// listedPrice is a model's own price for the picker, zero when the model is not in the curated list (budgeting uses
// priceFor, which assumes the dearest).
func listedPrice(spec providerSpec, model string) price {
	for _, known := range spec.curated {
		if known.id == model {
			return price{known.inputPrice, known.outputPrice}
		}
	}
	return price{}
}

func audienceAllows(a adminv1.AssistantAudience, role string) bool {
	switch a {
	case adminv1.AssistantAudience_ASSISTANT_AUDIENCE_EVERYONE:
		return role == store.RoleOwner || role == store.RoleHelper || role == store.RoleReadonly
	case adminv1.AssistantAudience_ASSISTANT_AUDIENCE_ADMINS:
		return role == store.RoleOwner || role == store.RoleHelper
	case adminv1.AssistantAudience_ASSISTANT_AUDIENCE_OWNER:
		return role == store.RoleOwner
	default:
		return false
	}
}

func (s *Service) GetAssistantAccess(ctx context.Context, _ *connect.Request[adminv1.GetAssistantAccessRequest]) (*connect.Response[adminv1.GetAssistantAccessResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := s.access(ctx, admin)
	if err != nil {
		return nil, err
	}
	// the chat button's dot and its next look: the conversations a follow-up answered, the admin's next follow-up
	unread, next, err := s.st.UnreadAssistantConversations(ctx, admin.ID)
	if err != nil {
		return nil, err
	}
	resp.Unread = uint32(max(unread, 0))
	if !next.IsZero() {
		resp.NextFollowUpUnix = next.Unix()
	}
	resp.TimezoneOffsetMinutes = int32(s.panelOffset(ctx))
	return connect.NewResponse(resp), nil
}

var _ adminv1connect.AssistantServiceHandler = (*Service)(nil)
