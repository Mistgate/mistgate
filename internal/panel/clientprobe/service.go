package clientprobe

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// maxGroups is the most operators and regions shown per node.
const maxGroups = 8

// Config configures Service. Store and StepUp are required.
type Config struct {
	Store *store.Store
	// StepUp is auth.Service.RequireStepUp: changing the switch asks for it.
	StepUp func(context.Context) error
	// Invalidate drops the cached subscription settings of this process (subsettings.Cache.Invalidate), which carry the switch,
	// so that its subscription answers follow at once; nil = within the cache's TTL.
	Invalidate func()
	Log        *slog.Logger
	Now        func() time.Time
}

// Service is the owner's switch of the kl!ck reports and what the stored reports say.
type Service struct{ cfg Config }

// New builds the service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Invalidate == nil {
		cfg.Invalidate = func() {}
	}
	return &Service{cfg: cfg}
}

// Handler is the admin ClientProbeService.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewClientProbeServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(4 << 10)}, opts...)...)
}

type rpc struct{ s *Service }

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

var errInternal = fail(connect.CodeInternal, "internal error")

// owner is the signed-in owner; a token (API or MCP) is refused again here, whatever the policy table says.
func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func (s *Service) enabled(ctx context.Context) (bool, error) {
	v, err := s.cfg.Store.Setting(ctx, store.SettingClientProbe)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return v == "1", err
}

// current is the switch with the number of stored reports; an off source shows none.
func (s *Service) current(ctx context.Context) (*adminv1.ClientProbeSettings, error) {
	on, err := s.enabled(ctx)
	if err != nil {
		s.cfg.Log.Error("clientprobe: read the switch", "err", err)
		return nil, errInternal
	}
	m := &adminv1.ClientProbeSettings{Enabled: on}
	if !on {
		return m, nil
	}
	n, last, err := s.cfg.Store.ClientProbeTotals(ctx, s.cfg.Now().Add(-Keep))
	if err != nil {
		s.cfg.Log.Error("clientprobe: read the reports", "err", err)
		return nil, errInternal
	}
	m.Reports = uint32(n)
	if !last.IsZero() {
		m.LastUnix = last.Unix()
	}
	return m, nil
}

// GetClientProbe is a read like HealthService.GetChecks (policy.go): every role and API tokens, no owner check here.
func (r rpc) GetClientProbe(ctx context.Context, _ *connect.Request[adminv1.GetClientProbeRequest]) (*connect.Response[adminv1.GetClientProbeResponse], error) {
	s := r.s
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	now := s.cfg.Now()
	out := &adminv1.GetClientProbeResponse{Settings: cur, NowUnix: now.Unix()}
	if cur.Enabled {
		counts, err := s.cfg.Store.ClientProbeCounts(ctx, now.Add(-Keep))
		if err != nil {
			s.cfg.Log.Error("clientprobe: read the counts", "err", err)
			return nil, errInternal
		}
		out.Nodes = Nodes(counts)
	}
	return connect.NewResponse(out), nil
}

func (r rpc) UpdateClientProbe(ctx context.Context, req *connect.Request[adminv1.UpdateClientProbeRequest]) (*connect.Response[adminv1.UpdateClientProbeResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if e := req.Msg.Enabled; e != nil {
		v := "0"
		if *e {
			v = "1"
		}
		if err := s.cfg.Store.SetSettings(ctx, map[string]string{store.SettingClientProbe: v}); err != nil {
			s.cfg.Log.Error("clientprobe: save the switch", "err", err)
			return nil, errInternal
		}
		s.cfg.Invalidate()
		s.audit(ctx, admin, "client_probe_update", `{"enabled":`+strconv.FormatBool(*e)+`}`)
	}
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateClientProbeResponse{Settings: cur}), nil
}

func (r rpc) DisconnectClientProbe(ctx context.Context, _ *connect.Request[adminv1.DisconnectClientProbeRequest]) (*connect.Response[adminv1.DisconnectClientProbeResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	// off first: a report that comes in between is refused, and none is left behind
	if err := s.cfg.Store.SetSettings(ctx, map[string]string{store.SettingClientProbe: "0"}); err != nil {
		s.cfg.Log.Error("clientprobe: disconnect", "err", err)
		return nil, errInternal
	}
	s.cfg.Invalidate()
	if err := s.cfg.Store.DeleteClientProbeReports(ctx); err != nil {
		s.cfg.Log.Error("clientprobe: forget the reports", "err", err)
		return nil, errInternal
	}
	s.audit(ctx, admin, "client_probe_disconnect", "")
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisconnectClientProbeResponse{Settings: cur}), nil
}

// audit records who did what; params is a JSON object.
func (s *Service) audit(ctx context.Context, admin store.Admin, action, params string) {
	if err := s.cfg.Store.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: params, Result: "ok"}); err != nil {
		s.cfg.Log.Warn("clientprobe: audit", "action", action, "err", err)
	}
}

// Nodes folds the counts by node and network into the answer: per node the totals, the operators and the regions, most
// reports first (then the most freezes and blocks), at most maxGroups of each. The nodes are in the order of the counts.
func Nodes(counts []store.ClientProbeCount) []*adminv1.ClientProbeNode {
	var out []*adminv1.ClientProbeNode
	byNode := map[string]*adminv1.ClientProbeNode{}
	ops, regions := map[string]map[string]*adminv1.ClientProbeTally{}, map[string]map[string]*adminv1.ClientProbeTally{}
	for _, c := range counts {
		n := byNode[c.NodeID]
		if n == nil {
			n = &adminv1.ClientProbeNode{NodeId: c.NodeID, Tally: &adminv1.ClientProbeTally{}}
			byNode[c.NodeID] = n
			out = append(out, n)
			ops[c.NodeID], regions[c.NodeID] = map[string]*adminv1.ClientProbeTally{}, map[string]*adminv1.ClientProbeTally{}
		}
		add(n.Tally, c)
		add(tallyOf(ops[c.NodeID], operatorName(c)), c)
		add(tallyOf(regions[c.NodeID], regionName(c)), c)
	}
	for _, n := range out {
		n.Operators, n.Regions = groups(ops[n.NodeId]), groups(regions[n.NodeId])
	}
	return out
}

func tallyOf(m map[string]*adminv1.ClientProbeTally, name string) *adminv1.ClientProbeTally {
	t := m[name]
	if t == nil {
		t = &adminv1.ClientProbeTally{}
		m[name] = t
	}
	return t
}

func add(t *adminv1.ClientProbeTally, c store.ClientProbeCount) {
	t.Reports += uint32(c.Reports)
	t.Ok += uint32(c.OK)
	t.Freeze += uint32(c.Freeze)
	t.TcpBlocked += uint32(c.TCPBlocked)
	t.HandshakeFailed += uint32(c.Handshake)
	t.NoAnswer += uint32(c.NoAnswer)
	t.LastUnix = max(t.LastUnix, c.Last.Unix())
}

// operatorName is the operator as the app named it, else its AS number, else "" (unknown).
func operatorName(c store.ClientProbeCount) string {
	switch {
	case c.Operator != "":
		return c.Operator
	case c.ASN != 0:
		return "AS" + strconv.FormatUint(uint64(c.ASN), 10)
	}
	return ""
}

// regionName is "RU · Moscow", the country alone, the region alone, or "" (unknown).
func regionName(c store.ClientProbeCount) string {
	switch {
	case c.Country != "" && c.Region != "":
		return c.Country + " · " + c.Region
	case c.Country != "":
		return c.Country
	}
	return c.Region
}

func groups(m map[string]*adminv1.ClientProbeTally) []*adminv1.ClientProbeGroup {
	out := make([]*adminv1.ClientProbeGroup, 0, len(m))
	for name, t := range m {
		out = append(out, &adminv1.ClientProbeGroup{Name: name, Tally: t})
	}
	slices.SortFunc(out, func(a, b *adminv1.ClientProbeGroup) int {
		return cmp.Or(
			cmp.Compare(b.Tally.Reports, a.Tally.Reports),
			cmp.Compare(b.Tally.Freeze+b.Tally.TcpBlocked, a.Tally.Freeze+a.Tally.TcpBlocked),
			strings.Compare(a.Name, b.Name))
	})
	if len(out) > maxGroups {
		out = out[:maxGroups]
	}
	return out
}
