// Package countryreach holds the one setting that every source of the country check shares: the country to look from (Globalping,
// the block lists and the TSPU probe all read it, which is why it is not a setting of any of them), and the history of what the
// sources said, which the Reachability page draws.
package countryreach

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	settingCountry = "country_reachability.country"
	// DefaultCountry is where the check looks from until the owner says otherwise.
	DefaultCountry = "RU"
)

var countryRe = regexp.MustCompile(`^[A-Za-z]{2}$`)

// HistoryDays is the longest history GetReachabilityHistory returns: the sources keep their results for 7 days.
const HistoryDays = 7

// History reads what the sources that are on said of each node since a time, in one address family (4 or 6;
// health.Service.CountryHistory, which knows which sources are on; this service is built before it).
type History func(ctx context.Context, since time.Time, family int) (sources []string, nodes []*adminv1.NodeReachabilityHistory, err error)

// Service is the country setting and its admin API.
type Service struct {
	st      *store.Store
	log     *slog.Logger
	now     func() time.Time
	history History
}

// New builds the service. A nil history answers GetReachabilityHistory with no sources.
func New(st *store.Store, log *slog.Logger, now func() time.Time, history History) *Service {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, log: log, now: now, history: history}
}

// Country is the country to look from, upper case; the default when nothing valid is stored or the store cannot be read.
func (s *Service) Country(ctx context.Context) string {
	v, err := s.st.Setting(ctx, settingCountry)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("countryreach: read the country", "err", err)
		}
		return DefaultCountry
	}
	if v = strings.ToUpper(v); countryRe.MatchString(v) {
		return v
	}
	return DefaultCountry
}

// Handler is the admin CountryReachabilityService.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewCountryReachabilityServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(4 << 10)}, opts...)...)
}

type rpc struct{ s *Service }

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

// owner is the signed-in owner; a token (API or MCP) is refused again here, whatever the policy table says.
func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func (r rpc) GetCountryReachability(ctx context.Context, _ *connect.Request[adminv1.GetCountryReachabilityRequest]) (*connect.Response[adminv1.GetCountryReachabilityResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetCountryReachabilityResponse{Settings: &adminv1.CountryReachability{Country: r.s.Country(ctx)}}), nil
}

func (r rpc) UpdateCountryReachability(ctx context.Context, req *connect.Request[adminv1.UpdateCountryReachabilityRequest]) (*connect.Response[adminv1.UpdateCountryReachabilityResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	c := strings.ToUpper(strings.TrimSpace(req.Msg.Country))
	if !countryRe.MatchString(c) {
		return nil, fail(connect.CodeInvalidArgument, "country: two letters, ISO 3166-1 alpha-2")
	}
	s := r.s
	if err := s.st.SetSettings(ctx, map[string]string{settingCountry: c}); err != nil {
		s.log.Error("countryreach: save the country", "err", err)
		return nil, fail(connect.CodeInternal, "internal error")
	}
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: "country_reachability_update", Params: `{"country":"` + c + `"}`, Result: "ok"}); err != nil {
		s.log.Warn("countryreach: audit", "err", err)
	}
	return connect.NewResponse(&adminv1.UpdateCountryReachabilityResponse{Settings: &adminv1.CountryReachability{Country: c}}), nil
}

// GetReachabilityHistory is a read like HealthService.GetChecks (policy.go): every role and API tokens, no owner check here.
func (r rpc) GetReachabilityHistory(ctx context.Context, req *connect.Request[adminv1.GetReachabilityHistoryRequest]) (*connect.Response[adminv1.GetReachabilityHistoryResponse], error) {
	s := r.s
	days := req.Msg.Days
	if days == 0 || days > HistoryDays {
		days = HistoryDays
	}
	family := int(req.Msg.Family)
	switch family {
	case 0:
		family = 4
	case 4, 6:
	default:
		return nil, fail(connect.CodeInvalidArgument, "family: 4 or 6")
	}
	now := s.now()
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	out := &adminv1.GetReachabilityHistoryResponse{Country: s.Country(ctx), SinceUnix: since.Unix(), NowUnix: now.Unix(), Family: uint32(family)}
	if s.history != nil {
		var err error
		if out.Sources, out.Nodes, err = s.history(ctx, since, family); err != nil {
			s.log.Error("countryreach: read the history", "err", err)
			return nil, fail(connect.CodeInternal, "internal error")
		}
	}
	return connect.NewResponse(out), nil
}
