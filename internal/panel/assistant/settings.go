//go:build !js

package assistant

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type settingsRow struct {
	enabled       bool
	provider      adminv1.AssistantProvider
	effort        adminv1.AssistantEffort
	audience      adminv1.AssistantAudience
	panelNotesEnc []byte
	panelNotes    string
	retention     uint32
}

type providerRow struct {
	sealed    []byte
	hint      string
	added     int64
	model     string
	budget    float64
	errorCode string
	errorAt   int64
}

func (s *Service) readSettings(ctx context.Context) (settingsRow, error) {
	var row settingsRow
	var enabled int
	err := s.st.R.QueryRowContext(ctx, `SELECT enabled,provider,effort,audience,panel_notes_enc,retention_days FROM assistant_settings WHERE id=1`).Scan(&enabled, &row.provider, &row.effort, &row.audience, &row.panelNotesEnc, &row.retention)
	row.enabled = enabled != 0
	return row, err
}

func (s *Service) readProviderRow(ctx context.Context, p adminv1.AssistantProvider) (providerRow, error) {
	var row providerRow
	err := s.st.R.QueryRowContext(ctx, `SELECT key_enc,key_hint,added_at,model,budget_usd,last_error_code,last_error_at FROM assistant_provider WHERE provider=?`, int32(p)).Scan(&row.sealed, &row.hint, &row.added, &row.model, &row.budget, &row.errorCode, &row.errorAt)
	return row, err
}

func (s *Service) openProviderKey(ctx context.Context, p adminv1.AssistantProvider) ([]byte, error) {
	row, err := s.readProviderRow(ctx, p)
	if err != nil || len(row.sealed) == 0 {
		return nil, err
	}
	plain, err := s.vault.Open(row.sealed, ProviderKeyAAD(p))
	if err != nil {
		return nil, errors.New("assistant provider key is unavailable")
	}
	return plain, nil
}

func (s *Service) setProviderError(ctx context.Context, p adminv1.AssistantProvider, code string) {
	_, err := s.st.W.ExecContext(context.WithoutCancel(ctx), `UPDATE assistant_provider SET last_error_code=?,last_error_at=? WHERE provider=?`, code, s.now().Unix(), int32(p))
	if err != nil {
		s.log.Warn("assistant provider error status update failed", "provider", p.String(), "err", err)
	}
}

func (s *Service) clearProviderError(ctx context.Context, p adminv1.AssistantProvider) {
	_, err := s.st.W.ExecContext(context.WithoutCancel(ctx), `UPDATE assistant_provider SET last_error_code='',last_error_at=0 WHERE provider=?`, int32(p))
	if err != nil {
		s.log.Warn("assistant provider status update failed", "provider", p.String(), "err", err)
	}
}

func (s *Service) models(ctx context.Context, p adminv1.AssistantProvider, key string) ([]modelInfo, error) {
	a := s.providers[p]
	if a == nil {
		return nil, apiError("provider_unreachable")
	}
	callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	return a.Check(callCtx, key)
}

func modelProto(m modelInfo) *adminv1.AssistantModel {
	return &adminv1.AssistantModel{Id: m.id, Name: m.name, ContextTokens: m.context, InputUsdPerMtok: m.inputPrice, OutputUsdPerMtok: m.outputPrice, Recommended: m.recommended}
}

func providerProto(s *Service, ctx context.Context, p adminv1.AssistantProvider) (*adminv1.ProviderStatus, error) {
	row, err := s.readProviderRow(ctx, p)
	if err != nil {
		return nil, err
	}
	spent, err := s.providerSpend(ctx, p, monthKey(s.now()))
	if err != nil {
		return nil, err
	}
	model := row.model
	if model == "" {
		model = defaultModel(p)
	}
	return &adminv1.ProviderStatus{Provider: p, Connected: len(row.sealed) > 0, KeyHint: row.hint, AddedUnix: row.added,
		Choice: &adminv1.ProviderChoice{Provider: p, Model: model, BudgetUsdMonth: row.budget}, SpentUsdMonth: spent, LastErrorCode: row.errorCode, LastErrorUnix: row.errorAt}, nil
}

func (s *Service) GetAssistantSettings(ctx context.Context, _ *connect.Request[adminv1.GetAssistantSettingsRequest]) (*connect.Response[adminv1.GetAssistantSettingsResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if admin.Role != store.RoleOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	settings, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	settings.panelNotes, err = openText(s.vault, settings.panelNotesEnc, PanelNotesAAD())
	if err != nil {
		return nil, err
	}
	resp := &adminv1.GetAssistantSettingsResponse{Settings: settingsProto(settings)}
	for _, spec := range providerSpecs {
		status, e := providerProto(s, ctx, spec.provider)
		if e != nil {
			return nil, e
		}
		resp.Providers = append(resp.Providers, status)
	}
	rows, e := s.st.R.QueryContext(ctx, `SELECT u.admin_id,COALESCE(a.display_name,''),SUM(u.usd),SUM(u.turns) FROM assistant_usage u LEFT JOIN admin a ON a.id=u.admin_id WHERE u.month=? GROUP BY u.admin_id,a.display_name ORDER BY SUM(u.usd) DESC,u.admin_id`, monthKey(s.now()))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var usage adminv1.AssistantUsage
		var turns int64
		if e = rows.Scan(&usage.AdminId, &usage.AdminName, &usage.Usd, &turns); e != nil {
			return nil, e
		}
		usage.Turns = uint32(max(turns, 0))
		resp.Usage = append(resp.Usage, &usage)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return connect.NewResponse(resp), nil
}

func settingsProto(row settingsRow) *adminv1.AssistantSettings {
	return &adminv1.AssistantSettings{Enabled: row.enabled, Provider: row.provider, Effort: row.effort, Audience: row.audience, PanelNotes: row.panelNotes, RetentionDays: row.retention}
}

func (s *Service) UpdateAssistantSettings(ctx context.Context, req *connect.Request[adminv1.UpdateAssistantSettingsRequest]) (*connect.Response[adminv1.UpdateAssistantSettingsResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if admin.Role != store.RoleOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	if req.Msg.Settings == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("settings are required"))
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	in := req.Msg.Settings
	if in.RetentionDays != 0 && (in.RetentionDays < 7 || in.RetentionDays > 365) || utf8.RuneCountInString(in.PanelNotes) > 4000 || !validEffort(in.Effort) || !validAudience(in.Audience) || !validProvider(in.Provider) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("assistant settings are invalid"))
	}
	if in.Enabled && in.Provider == adminv1.AssistantProvider_ASSISTANT_PROVIDER_UNSPECIFIED {
		return nil, codeConnect("no_provider")
	}
	choices := make([]store.AssistantProviderChoice, 0, len(req.Msg.Choices))
	seenProviders := make(map[adminv1.AssistantProvider]struct{}, len(req.Msg.Choices))
	for _, choice := range req.Msg.Choices {
		if choice == nil || !validProvider(choice.Provider) || math.IsNaN(choice.BudgetUsdMonth) || math.IsInf(choice.BudgetUsdMonth, 0) || choice.BudgetUsdMonth < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider choice is invalid"))
		}
		if _, exists := seenProviders[choice.Provider]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider choice is duplicated"))
		}
		seenProviders[choice.Provider] = struct{}{}
		key, e := s.openProviderKey(ctx, choice.Provider)
		if e != nil || len(key) == 0 {
			return nil, codeConnect("no_provider")
		}
		models, e := s.models(ctx, choice.Provider, string(key))
		zero(key)
		if e != nil {
			code := providerCode(e)
			s.setProviderError(ctx, choice.Provider, code)
			return nil, codeConnect(code)
		}
		if choice.Model != "" {
			found := false
			for _, m := range models {
				if m.id == choice.Model {
					found = true
					break
				}
			}
			if !found {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider model is not available"))
			}
		}
		model := choice.Model
		if model == "" {
			model = chooseDefaultModel(choice.Provider, models)
		}
		choices = append(choices, store.AssistantProviderChoice{Provider: int32(choice.Provider), Model: model, Budget: choice.BudgetUsdMonth})
	}
	if in.Enabled {
		row, e := s.readProviderRow(ctx, in.Provider)
		if e != nil || len(row.sealed) == 0 {
			return nil, codeConnect("no_provider")
		}
	}
	sealedNotes := sealText(s.vault, in.PanelNotes, PanelNotesAAD())
	if err = s.st.SaveAssistantSettings(ctx, choices, in.Enabled, int32(in.Provider), int32(in.Effort), int32(in.Audience), sealedNotes, in.RetentionDays, s.now()); err != nil {
		return nil, err
	}
	s.audit(ctx, admin, "assistant_settings_update", "ok", map[string]any{"enabled": in.Enabled, "provider": in.Provider.String(), "audience": in.Audience.String()})
	row := settingsRow{enabled: in.Enabled, provider: in.Provider, effort: in.Effort, audience: in.Audience, panelNotes: in.PanelNotes, panelNotesEnc: sealedNotes, retention: in.RetentionDays}
	resp := &adminv1.UpdateAssistantSettingsResponse{Settings: settingsProto(row)}
	for _, spec := range providerSpecs {
		status, e := providerProto(s, ctx, spec.provider)
		if e != nil {
			return nil, e
		}
		resp.Providers = append(resp.Providers, status)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) SetProviderKey(ctx context.Context, req *connect.Request[adminv1.SetProviderKeyRequest]) (*connect.Response[adminv1.SetProviderKeyResponse], error) {
	defer func() { req.Msg.ApiKey = "" }()
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if admin.Role != store.RoleOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	if s.auth == nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("step-up is unavailable"))
	}
	if err = s.auth.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	provider, key := req.Msg.Provider, []byte(strings.TrimSpace(req.Msg.ApiKey))
	defer zero(key)
	if !validProvider(provider) || len(key) < 8 || len(key) > 4096 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider key is invalid"))
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	models, err := s.models(ctx, provider, string(key))
	if err != nil {
		code := providerCode(err)
		s.setProviderError(ctx, provider, code)
		s.audit(ctx, admin, "assistant_provider_key_set", "fail", map[string]any{"provider": provider.String()})
		return nil, codeConnect(code)
	}
	sealed := s.vault.Seal(key, ProviderKeyAAD(provider))
	hint := string(key)
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	}
	row, err := s.readProviderRow(ctx, provider)
	if err != nil {
		return nil, err
	}
	chosen := row.model
	validChosen := false
	for _, candidate := range models {
		if candidate.id == chosen {
			validChosen = true
			break
		}
	}
	if !validChosen {
		chosen = chooseDefaultModel(provider, models)
	}
	if _, err = s.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=?,key_hint=?,added_at=?,model=?,last_error_code='',last_error_at=0 WHERE provider=?`, sealed, hint, s.now().Unix(), chosen, int32(provider)); err != nil {
		return nil, err
	}
	s.audit(ctx, admin, "assistant_provider_key_set", "ok", map[string]any{"provider": provider.String()})
	status, err := providerProto(s, ctx, provider)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.SetProviderKeyResponse{Status: status}
	for _, m := range models {
		resp.Models = append(resp.Models, modelProto(m))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) RemoveProviderKey(ctx context.Context, req *connect.Request[adminv1.RemoveProviderKeyRequest]) (*connect.Response[adminv1.RemoveProviderKeyResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if admin.Role != store.RoleOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	if s.auth == nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("step-up is unavailable"))
	}
	if err = s.auth.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	if !validProvider(req.Msg.Provider) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider is invalid"))
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	if _, err = s.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=X'',key_hint='',added_at=0,last_error_code='',last_error_at=0 WHERE provider=?`, int32(req.Msg.Provider)); err != nil {
		return nil, err
	}
	s.audit(ctx, admin, "assistant_provider_key_remove", "ok", map[string]any{"provider": req.Msg.Provider.String()})
	status, err := providerProto(s, ctx, req.Msg.Provider)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RemoveProviderKeyResponse{Status: status}), nil
}

func (s *Service) ListProviderModels(ctx context.Context, req *connect.Request[adminv1.ListProviderModelsRequest]) (*connect.Response[adminv1.ListProviderModelsResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if admin.Role != store.RoleOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	if !validProvider(req.Msg.Provider) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider is invalid"))
	}
	key, err := s.openProviderKey(ctx, req.Msg.Provider)
	if err != nil || len(key) == 0 {
		return nil, codeConnect("no_provider")
	}
	defer zero(key)
	models, err := s.models(ctx, req.Msg.Provider, string(key))
	if err != nil {
		code := providerCode(err)
		s.setProviderError(ctx, req.Msg.Provider, code)
		return nil, codeConnect(code)
	}
	s.clearProviderError(ctx, req.Msg.Provider)
	resp := &adminv1.ListProviderModelsResponse{}
	for _, m := range models {
		resp.Models = append(resp.Models, modelProto(m))
	}
	return connect.NewResponse(resp), nil
}

func chooseDefaultModel(provider adminv1.AssistantProvider, models []modelInfo) string {
	want := defaultModel(provider)
	for _, m := range models {
		if m.id == want {
			return want
		}
	}
	for _, m := range models {
		if m.recommended {
			return m.id
		}
	}
	if len(models) > 0 {
		return models[0].id
	}
	return want
}

func validProvider(p adminv1.AssistantProvider) bool { _, ok := specFor(p); return ok }
func validEffort(e adminv1.AssistantEffort) bool {
	return e == adminv1.AssistantEffort_ASSISTANT_EFFORT_QUICK || e == adminv1.AssistantEffort_ASSISTANT_EFFORT_NORMAL || e == adminv1.AssistantEffort_ASSISTANT_EFFORT_THOROUGH
}
func validAudience(a adminv1.AssistantAudience) bool {
	return a == adminv1.AssistantAudience_ASSISTANT_AUDIENCE_OWNER || a == adminv1.AssistantAudience_ASSISTANT_AUDIENCE_ADMINS || a == adminv1.AssistantAudience_ASSISTANT_AUDIENCE_EVERYONE
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
func monthKey(t time.Time) string { return t.UTC().Format("2006-01") }
