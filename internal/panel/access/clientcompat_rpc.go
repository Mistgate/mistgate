package access

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ClientCompatService: the client catalogue (clientcompat.go) and the list of devices that receive REALITY, judged by it.

// ClientCompatHandler returns the Connect path and handler of ClientCompatService.
func (s *Service) ClientCompatHandler() (string, http.Handler) {
	return adminv1connect.NewClientCompatServiceHandler(s, handlerOpts...)
}

const (
	outdatedPageDefault = 50
	outdatedPageMax     = 200
)

// compatInternal logs a failure with its code and answers the caller with a bare internal error.
func (s *Service) compatInternal(code string, err error) error {
	s.log.Error("client catalogue", "code", code, "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// clientTable is the table in force. Changes the panel cannot read leave the built-in table (a warning says so): the list is
// knowledge, it never has to refuse because a stored row is bad.
func (s *Service) clientTable(ctx context.Context) (ClientTable, error) {
	raw, err := s.st.Setting(ctx, store.SettingClientCompat)
	if errors.Is(err, store.ErrNotFound) {
		return NewClientTable(nil), nil
	}
	if err != nil {
		return ClientTable{}, s.compatInternal("client_compat_read_failed", err)
	}
	rows, err := ParseClientOverrides(raw)
	if err != nil {
		s.log.Warn("client catalogue: the stored changes are unreadable, using the built-in table", "code", "client_compat_unreadable", "err", err)
		return NewClientTable(nil), nil
	}
	return NewClientTable(rows), nil
}

func ruleProto(r ClientRule) *adminv1.ClientCompatRule {
	return &adminv1.ClientCompatRule{App: r.App, Platform: r.Platform, Core: r.Core, Unsupported: r.Unsupported, MinChrome: r.MinChrome, MinOther: r.MinOther, Title: r.Title}
}

func rulesProto(rows []ClientRule) []*adminv1.ClientCompatRule {
	out := make([]*adminv1.ClientCompatRule, len(rows))
	for i, r := range rows {
		out[i] = ruleProto(r)
	}
	return out
}

func (s *Service) compatTables(ctx context.Context) (rules, defaults []*adminv1.ClientCompatRule, err error) {
	t, err := s.clientTable(ctx)
	if err != nil {
		return nil, nil, err
	}
	def := NewClientTable(nil)
	return rulesProto(t.Rules()), rulesProto(def.Rules()), nil
}

func (s *Service) GetClientCompat(ctx context.Context, _ *connect.Request[adminv1.GetClientCompatRequest]) (*connect.Response[adminv1.GetClientCompatResponse], error) {
	rules, defaults, err := s.compatTables(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetClientCompatResponse{Rules: rules, Defaults: defaults}), nil
}

// compatOwner: the owner at the panel, never a token or an MCP session (the policy says the same; this is the second lock).
func compatOwner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, connect.NewError(connect.CodePermissionDenied, errors.New("owner access required"))
	}
	return a, nil
}

func (s *Service) UpdateClientCompat(ctx context.Context, req *connect.Request[adminv1.UpdateClientCompatRequest]) (*connect.Response[adminv1.UpdateClientCompatResponse], error) {
	admin, err := compatOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.cfg.StepUp == nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	want := make([]ClientRule, len(req.Msg.Rules))
	for i, r := range req.Msg.Rules {
		want[i] = ClientRule{App: r.GetApp(), Platform: r.GetPlatform(), Core: r.GetCore(), Unsupported: r.GetUnsupported(), MinChrome: r.GetMinChrome(), MinOther: r.GetMinOther()}
	}
	overrides, err := ClientOverridesOf(want)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	value, err := MarshalClientOverrides(overrides)
	if err != nil {
		return nil, s.compatInternal("client_compat_encode_failed", err)
	}
	if value == "" {
		err = s.st.DeleteSettings(ctx, store.SettingClientCompat)
	} else {
		err = s.st.SetSettings(ctx, map[string]string{store.SettingClientCompat: value})
	}
	if err != nil {
		return nil, s.compatInternal("client_compat_save_failed", err)
	}
	changed := make([]string, 0, len(overrides))
	for _, r := range overrides[:min(len(overrides), 20)] {
		changed = append(changed, strings.TrimSuffix(r.App+"/"+r.Platform, "/"))
	}
	s.audit(ctx, admin.ID, "client_compat.update", map[string]any{"changed": len(overrides), "rows": changed})
	rules, defaults, err := s.compatTables(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateClientCompatResponse{Rules: rules, Defaults: defaults}), nil
}

// ---- the list ----

// CompatDevice is a device that receives REALITY and what the table says about it.
type CompatDevice struct {
	store.ClientCompatDevice
	UserName    string
	Verdict     ClientVerdict
	Fingerprint string // the one that gave the verdict
}

// realityFingerprint is the fingerprint of a VLESS REALITY profile ("" for anything else, a profile the panel cannot read too).
func realityFingerprint(f store.AccessInboundFull) string {
	if f.Profile.Protocol != vless.ID {
		return ""
	}
	var st vless.Settings
	if json.Unmarshal([]byte(f.Profile.SettingsJSON), &st) != nil || st.Security != "reality" {
		return ""
	}
	if st.Reality.Fingerprint == "" {
		return "chrome"
	}
	return st.Reality.Fingerprint
}

// CompatDevices lists the devices that receive a VLESS REALITY server now (userID restricts it to one person), each judged by the
// table against every fingerprint its person's REALITY profiles use; the worst verdict stands. The fingerprints in use are
// returned too, sorted. A device is on the list when it is a live, not blocked link device with a VLESS credential and its
// person is active and can use a live inbound of a REALITY profile (the subscription's own rule, usable).
func (s *Service) CompatDevices(ctx context.Context, userID string) ([]CompatDevice, []string, error) {
	table, err := s.clientTable(ctx)
	if err != nil {
		return nil, nil, err
	}
	a := s.st.Access()
	devs, err := a.ClientCompatDevices(ctx, userID)
	if err != nil {
		return nil, nil, s.compatInternal("client_compat_devices_failed", err)
	}
	if len(devs) == 0 {
		return nil, nil, nil
	}
	var ids []string
	for _, d := range devs {
		if len(ids) == 0 || ids[len(ids)-1] != d.UserID { // the store orders by user
			ids = append(ids, d.UserID)
		}
	}
	users, err := a.UsersByIDs(ctx, ids)
	if err != nil {
		return nil, nil, s.compatInternal("client_compat_users_failed", err)
	}
	groups, err := a.Groups(ctx)
	if err != nil {
		return nil, nil, s.compatInternal("client_compat_groups_failed", err)
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, nil, s.compatInternal("client_compat_inbounds_failed", err)
	}
	var reality []store.AccessInboundFull
	fpOf := map[string]string{}
	for _, f := range full {
		if fp := realityFingerprint(f); fp != "" && liveInbound(f) {
			reality = append(reality, f)
			fpOf[f.Inbound.ID] = fp
		}
	}
	groupOf := map[string]store.AccessGroup{}
	for _, g := range groups {
		groupOf[g.ID] = g
	}
	now := s.now()
	type person struct {
		name string
		fps  []string
	}
	people := map[string]person{}
	var inUse []string
	for _, u := range users {
		if ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, u.UsedBytes, now) != StatusActive {
			continue
		}
		g := groupOf[u.GroupID]
		var fps []string
		for _, f := range reality {
			if s.usable(f, g, u) && !slices.Contains(fps, fpOf[f.Inbound.ID]) {
				fps = append(fps, fpOf[f.Inbound.ID])
			}
		}
		if len(fps) == 0 {
			continue
		}
		slices.Sort(fps)
		people[u.ID] = person{name: u.Name, fps: fps}
		for _, fp := range fps {
			if !slices.Contains(inUse, fp) {
				inUse = append(inUse, fp)
			}
		}
	}
	slices.Sort(inUse)
	var out []CompatDevice
	for _, d := range devs {
		p, ok := people[d.UserID]
		if !ok {
			continue
		}
		best := CompatDevice{ClientCompatDevice: d, UserName: p.name}
		for i, fp := range p.fps {
			v := table.Verdict(d.Client, d.Platform, fp)
			if i == 0 || clientSeverity(v.State) > clientSeverity(best.Verdict.State) {
				best.Verdict, best.Fingerprint = v, fp
			}
		}
		out = append(out, best)
	}
	return out, inUse, nil
}

var compatStates = []string{ClientOK, ClientBelow, ClientUnsupported, ClientUnknown}

func compatStateProto(state string) adminv1.ClientCompatState {
	switch state {
	case ClientOK:
		return adminv1.ClientCompatState_CLIENT_COMPAT_STATE_OK
	case ClientBelow:
		return adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM
	case ClientUnsupported:
		return adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNSUPPORTED
	case ClientUnknown:
		return adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNKNOWN
	}
	return adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNSPECIFIED
}

func (s *Service) ListOutdatedDevices(ctx context.Context, req *connect.Request[adminv1.ListOutdatedDevicesRequest]) (*connect.Response[adminv1.ListOutdatedDevicesResponse], error) {
	m := req.Msg
	var want string // "" = every state but ok
	switch m.State {
	case adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNSPECIFIED:
	case adminv1.ClientCompatState_CLIENT_COMPAT_STATE_OK:
		want = ClientOK
	case adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM:
		want = ClientBelow
	case adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNSUPPORTED:
		want = ClientUnsupported
	case adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNKNOWN:
		want = ClientUnknown
	default:
		return nil, invalid("unknown state")
	}
	// ponytail: every REALITY device is loaded and judged per call (a few thousand rows of a few words); a cache or SQL paging
	// when a panel with tens of thousands of devices makes the screen slow.
	all, fps, err := s.CompatDevices(ctx, "")
	if err != nil {
		return nil, err
	}
	devices, people := map[string]uint32{}, map[string]map[string]bool{}
	for _, d := range all {
		devices[d.Verdict.State]++
		if people[d.Verdict.State] == nil {
			people[d.Verdict.State] = map[string]bool{}
		}
		people[d.Verdict.State][d.UserID] = true
	}
	resp := &adminv1.ListOutdatedDevicesResponse{Fingerprints: fps}
	for _, st := range compatStates {
		resp.Counts = append(resp.Counts, &adminv1.ClientCompatCount{State: compatStateProto(st), Devices: devices[st], People: uint32(len(people[st]))})
	}
	var rows []CompatDevice
	for _, d := range all {
		if (want == "" && d.Verdict.State != ClientOK || want != "" && d.Verdict.State == want) && (m.UserId == "" || d.UserID == m.UserId) {
			rows = append(rows, d)
		}
	}
	slices.SortStableFunc(rows, func(a, b CompatDevice) int {
		if c := clientSeverity(b.Verdict.State) - clientSeverity(a.Verdict.State); c != 0 {
			return c
		}
		if c := b.LastSeenAt.Compare(a.LastSeenAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.UserName, b.UserName); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	resp.Total = uint32(len(rows))
	size := int(m.PageSize)
	if size <= 0 {
		size = outdatedPageDefault
	}
	size = min(size, outdatedPageMax)
	off := min(int(min(m.Offset, 1<<20)), len(rows))
	for _, d := range rows[off:min(off+size, len(rows))] {
		var last int64
		if !d.LastSeenAt.IsZero() {
			last = d.LastSeenAt.Unix()
		}
		resp.Devices = append(resp.Devices, &adminv1.OutdatedDevice{
			DeviceId: d.ID, UserId: d.UserID, UserName: d.UserName, Client: d.Client, Platform: d.Platform, Model: d.Model, OsVersion: d.OSVersion,
			Shared: d.Shared, LastSeenUnix: last, State: compatStateProto(d.Verdict.State), Reason: d.Verdict.Reason,
			NeedVersion: d.Verdict.Need, Fingerprint: d.Fingerprint, AppTitle: d.Verdict.Title,
		})
	}
	return connect.NewResponse(resp), nil
}
