// Package clientapi owns the panel setting that advertises desktop client API discovery on subscription answers.
package clientapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Config configures Service. Store and StepUp are required.
type Config struct {
	Store      *store.Store
	StepUp     func(context.Context) error
	Invalidate func()
	Log        *slog.Logger
	Now        func() time.Time
}

// Service implements the owner switch for desktop client API discovery.
type Service struct{ cfg Config }

func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Invalidate == nil {
		cfg.Invalidate = func() {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}
}

// Handler is the admin ClientApiService.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewClientApiServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(4 << 10)}, opts...)...)
}

type rpc struct{ s *Service }

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

var errInternal = fail(connect.CodeInternal, "internal error")

func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func (s *Service) current(ctx context.Context) (*adminv1.ClientApiSettings, error) {
	v, err := s.cfg.Store.Setting(ctx, store.SettingClientAPI)
	if errors.Is(err, store.ErrNotFound) {
		return &adminv1.ClientApiSettings{}, nil
	}
	if err != nil {
		s.cfg.Log.Error("client API setting read failed", "code", "client_api_setting_read_failed", "err", err)
		return nil, errInternal
	}
	return &adminv1.ClientApiSettings{Enabled: v == "1"}, nil
}

func (r rpc) GetClientApi(ctx context.Context, _ *connect.Request[adminv1.GetClientApiRequest]) (*connect.Response[adminv1.GetClientApiResponse], error) {
	settings, err := r.s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetClientApiResponse{Settings: settings}), nil
}

func (r rpc) UpdateClientApi(ctx context.Context, req *connect.Request[adminv1.UpdateClientApiRequest]) (*connect.Response[adminv1.UpdateClientApiResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if enabled := req.Msg.Enabled; enabled != nil {
		value := "0"
		if *enabled {
			value = "1"
		}
		if err := s.cfg.Store.SetSettings(ctx, map[string]string{store.SettingClientAPI: value}); err != nil {
			s.cfg.Log.Error("client API setting save failed", "code", "client_api_setting_save_failed", "err", err)
			return nil, errInternal
		}
		s.cfg.Invalidate()
		s.audit(ctx, admin, *enabled)
	}
	settings, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateClientApiResponse{Settings: settings}), nil
}

func (s *Service) audit(ctx context.Context, admin store.Admin, enabled bool) {
	params := `{"enabled":` + strconv.FormatBool(enabled) + `}`
	if err := s.cfg.Store.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: admin.ID, Action: "client_api_update", Params: params, Result: "ok"}); err != nil {
		s.cfg.Log.Warn("client API setting audit failed", "code", "client_api_audit_failed", "err", err)
	}
}
