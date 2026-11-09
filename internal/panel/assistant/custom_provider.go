//go:build !js

package assistant

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (s *Service) setCustomProvider(ctx context.Context, admin store.Admin, req *adminv1.SetProviderKeyRequest) (*connect.Response[adminv1.SetProviderKeyResponse], error) {
	name, endpoint, model := strings.TrimSpace(req.CustomName), strings.TrimSpace(req.CustomUrl), strings.TrimSpace(req.CustomModel)
	key := []byte(strings.TrimSpace(req.ApiKey))
	defer zero(key)
	if name == "" || utf8.RuneCountInString(name) > 100 || model == "" || len(model) > 256 || len(endpoint) > 2048 || len(key) > 4096 || (len(key) == 0 && !req.AllowLocal) || math.IsNaN(req.InputUsdPerMtok) || math.IsInf(req.InputUsdPerMtok, 0) || req.InputUsdPerMtok < 0 || math.IsNaN(req.OutputUsdPerMtok) || math.IsInf(req.OutputUsdPerMtok, 0) || req.OutputUsdPerMtok < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("custom provider configuration is invalid"))
	}
	h, err := customProviderHTTP(endpoint, req.AllowLocal)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("custom provider address is invalid"))
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	a := &openAIAdapter{kind: req.Provider, http: h, defaultModel: model}
	models, err := a.Check(ctx, string(key))
	if err != nil {
		return nil, codeConnect(providerCode(err))
	}
	hint := string(key)
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	} else if len(hint) > 0 {
		hint = "****"
	}
	// An encrypted space marks a connected provider without credentials. The
	// HTTP layer trims it and omits the authorization header.
	if len(key) == 0 {
		key = []byte(" ")
	}
	sealed := s.vault.Seal(key, ProviderKeyAAD(req.Provider))
	_, err = s.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=?,key_hint=?,added_at=?,model=?,custom_name=?,custom_url=?,allow_local=?,input_usd_per_mtok=?,output_usd_per_mtok=?,last_error_code='',last_error_at=0 WHERE provider=?`, sealed, hint, s.now().Unix(), model, name, endpoint, boolInt(req.AllowLocal), req.InputUsdPerMtok, req.OutputUsdPerMtok, int32(req.Provider))
	if err != nil {
		return nil, err
	}
	s.audit(ctx, admin, "assistant_provider_key_set", "ok", map[string]any{"provider": req.Provider.String()})
	status, err := providerProto(s, ctx, req.Provider)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.SetProviderKeyResponse{Status: status}
	for _, m := range models {
		resp.Models = append(resp.Models, modelProto(m))
	}
	return connect.NewResponse(resp), nil
}
