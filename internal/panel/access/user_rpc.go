package access

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

const (
	defaultDeviceLimit = 5
	maxBulk            = 5000
	viaWindow          = 7 * 24 * time.Hour
)

var statusProto = map[string]adminv1.UserStatus{
	StatusActive:   adminv1.UserStatus_USER_STATUS_ACTIVE,
	StatusDisabled: adminv1.UserStatus_USER_STATUS_DISABLED,
	StatusExpired:  adminv1.UserStatus_USER_STATUS_EXPIRED,
	StatusLimited:  adminv1.UserStatus_USER_STATUS_LIMITED,
}

var resetToProto = map[string]adminv1.QuotaReset{
	ResetNone:         adminv1.QuotaReset_QUOTA_RESET_NONE,
	ResetDay:          adminv1.QuotaReset_QUOTA_RESET_DAY,
	ResetWeek:         adminv1.QuotaReset_QUOTA_RESET_WEEK,
	ResetMonth:        adminv1.QuotaReset_QUOTA_RESET_MONTH,
	ResetRollingMonth: adminv1.QuotaReset_QUOTA_RESET_ROLLING_MONTH,
}

func resetFromProto(r adminv1.QuotaReset) (string, error) {
	for k, v := range resetToProto {
		if v == r {
			return k, nil
		}
	}
	if r == adminv1.QuotaReset_QUOTA_RESET_UNSPECIFIED {
		return ResetMonth, nil
	}
	return "", invalid("unknown quota reset")
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0).UTC()
}

// userProtos converts users to their API form with the derived columns (devices, via, online).
func (s *Service) userProtos(ctx context.Context, users []store.AccessUser) ([]*adminv1.User, error) {
	if len(users) == 0 {
		return nil, nil
	}
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	a := s.st.Access()
	devs, err := a.DeviceCounts(ctx, ids)
	if err != nil {
		return nil, s.internal("device counts", err)
	}
	viaProtos, err := a.UsersProtocolsSince(ctx, ids, s.now().Add(-viaWindow))
	if err != nil {
		return nil, s.internal("via", err)
	}
	nodes, err := a.Nodes(ctx)
	if err != nil {
		return nil, s.internal("nodes", err)
	}
	nodeName := map[string]string{}
	for _, n := range nodes {
		nodeName[n.ID] = n.Name
	}
	online := s.online.OnlineUsers()
	dnsLookup, err := s.st.DNS().Lookup(ctx)
	if err != nil {
		return nil, s.internal("dns presets", err)
	}
	// what each user's page offers (access_happ / access_amnezia): the groups and the inbounds, read once
	gs, err := a.Groups(ctx)
	if err != nil {
		return nil, s.internal("groups", err)
	}
	groups := map[string]store.AccessGroup{}
	for _, g := range gs {
		groups[g.ID] = g
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	out := make([]*adminv1.User, len(users))
	for i, u := range users {
		var via []adminv1.App
		for _, pid := range viaProtos[u.ID] {
			if p, ok := s.reg.Get(pid); ok {
				_, _, apps := clientApps(p)
				for _, app := range apps {
					if !slices.Contains(via, app) {
						via = append(via, app)
					}
				}
			}
		}
		slices.Sort(via)
		cur, isOnline := online[u.ID]
		var next int64
		if !NextReset(u.QuotaReset, u.PeriodStart).IsZero() {
			next = NextReset(u.QuotaReset, u.PeriodStart).Unix()
		}
		out[i] = &adminv1.User{
			Id: u.ID, Name: u.Name, SubscriptionName: u.SubscriptionName, GroupId: u.GroupID, GroupName: u.GroupName, Status: statusProto[u.Status],
			Apps: &adminv1.AppToggles{Happ: u.AppHapp, Amnezia: u.AppAmnezia}, Via: via,
			DevicesUsed: uint32(devs[u.ID]), DeviceLimit: uint32(u.DeviceLimit),
			UsedBytes: u.UsedBytes, QuotaBytes: u.QuotaBytes, QuotaReset: resetToProto[u.QuotaReset], NextResetUnix: next,
			ExpiresUnix: unixOrZero(u.ExpiresAt), LastSeenUnix: unixOrZero(u.LastSeenAt), Online: isOnline,
			SpeedLimitBps: u.SpeedLimitBps, Nodes: &adminv1.NodeSelection{All: u.AllNodes, NodeIds: u.NodeIDs},
			CreatedUnix: u.CreatedAt.Unix(), DnsPresetId: u.DNSPresetID,
		}
		effID, effName, src := dnsLookup.Resolve(u.DNSPresetID, u.GroupDNSPresetID)
		out[i].EffectiveDnsPresetId, out[i].EffectiveDnsPresetName, out[i].DnsSource = effID, effName, dns.Source(src).Proto()
		out[i].GroupColor = groups[u.GroupID].Color
			out[i].AccessHapp, out[i].AccessAmnezia = s.accessOf(u, groups[u.GroupID], full)
		if isOnline {
			out[i].CurrentNodeId, out[i].CurrentNodeName = cur, nodeName[cur]
		}
	}
	return out, nil
}

// resolveNodes validates a node selection (nil = all nodes).
func (s *Service) resolveNodes(ctx context.Context, sel *adminv1.NodeSelection) (all bool, ids []string, err error) {
	if sel == nil || sel.All {
		return true, nil, nil
	}
	ids = slices.Compact(slices.Sorted(slices.Values(sel.NodeIds)))
	if len(ids) == 0 {
		return false, nil, invalid("select at least one node or all nodes")
	}
	have, err := s.st.Access().ExistingNodeIDs(ctx, ids)
	if err != nil {
		return false, nil, s.internal("nodes", err)
	}
	for _, id := range ids {
		if !have[id] {
			return false, nil, notFound("node " + id)
		}
	}
	return false, ids, nil
}

func (s *Service) subscriptionURL(token string) (string, error) {
	base := strings.TrimRight(s.cfg.SubscriptionBaseURL, "/")
	if base == "" {
		return "", coded(connect.CodeFailedPrecondition, "sub_address_missing")
	}
	return base + "/" + token, nil
}

// newCreds issues a credential for every protocol the apps allow and that is not in have. A per-device protocol
// (AWG) is skipped: its credentials are issued on request, bound to one profile (AddAWGDevice, and lazily for the
// implicit device of a Mihomo subscription).
func (s *Service) newCreds(userID, deviceID string, happ, amnezia bool, have map[string]bool) ([]store.AccessCred, error) {
	var out []store.AccessCred
	now := s.now()
	for _, p := range s.reg.List() {
		if have[p.ID()] || protocols.IsPerDevice(p) || !protocols.AllowedForApps(p, appsOf(happ, amnezia)...) {
			continue
		}
		iss, err := p.IssueCredential(protocols.IssueInput{UserID: userID, DeviceID: deviceID})
		if err != nil {
			return nil, err
		}
		id := store.NewID("crd_")
		out = append(out, store.AccessCred{
			ID: id, DeviceID: deviceID, UserID: userID, Protocol: p.ID(),
			SecretEnc: s.vault.Seal([]byte(iss.Secret), id), DataJSON: string(iss.NodeData), CreatedAt: now,
		})
	}
	return out, nil
}

// ensureCreds makes sure the user's implicit device exists and holds a credential for every protocol the
// user's apps allow (one implicit device, no HWID). It reports whether anything was added.
// The device is created only when it gets a credential: a user of the Amnezia app alone has none until an AWG
// credential is issued, and an empty device would take a slot of the device limit.
// Runs on create, on app-toggle change and on subscription fetch, not on registering a new plugin
// (a fetch covers that lazily).
func (s *Service) ensureCreds(ctx context.Context, u store.AccessUser) (bool, error) {
	a := s.st.Access()
	for range 2 { // a concurrent fetch may create the device first: retry once
		dev, err := a.ImplicitDevice(ctx, u.ID)
		if errors.Is(err, store.ErrNotFound) {
			dev = store.AccessDevice{ID: store.NewID("dev_"), UserID: u.ID, Implicit: true, CreatedAt: s.now()}
			creds, err := s.newCreds(u.ID, dev.ID, u.AppHapp, u.AppAmnezia, nil)
			if err != nil || len(creds) == 0 {
				return false, err
			}
			switch err := a.AddDevice(ctx, dev, creds); {
			case errors.Is(err, store.ErrAccessExists):
				continue
			case err != nil:
				return false, err
			}
			return true, nil
		} else if err != nil {
			return false, err
		}
		have := map[string]bool{}
		for _, p := range dev.Protocols {
			have[p] = true
		}
		creds, err := s.newCreds(u.ID, dev.ID, u.AppHapp, u.AppAmnezia, have)
		if err != nil || len(creds) == 0 {
			return false, err
		}
		switch err := a.AddCreds(ctx, creds); {
		case errors.Is(err, store.ErrAccessExists):
			continue
		case err != nil:
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *Service) CreateUser(ctx context.Context, req *connect.Request[adminv1.CreateUserRequest]) (*connect.Response[adminv1.CreateUserResponse], error) {
	m := req.Msg
	name, err := cleanName("user", m.Name)
	if err != nil {
		return nil, err
	}
	if m.GroupId == "" {
		return nil, invalid("group is required")
	}
	reset, err := resetFromProto(m.QuotaReset)
	if err != nil {
		return nil, err
	}
	limit := int(m.DeviceLimit)
	if limit == 0 {
		limit = defaultDeviceLimit
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("device limit must be 1-100")
	}
	if m.TermDays > 36500 {
		return nil, invalid("term is too long")
	}
	happ, amnezia := true, true
	if m.Apps != nil {
		happ, amnezia = m.Apps.Happ, m.Apps.Amnezia
	}
	if !happ && !amnezia {
		return nil, invalid("at least one app must be on")
	}
	all, nodeIDs, err := s.resolveNodes(ctx, m.Nodes)
	if err != nil {
		return nil, err
	}
	if err := s.checkDNSPreset(ctx, m.DnsPresetId); err != nil {
		return nil, err
	}
	now := s.now()
	var expires time.Time
	if m.TermDays > 0 {
		expires = now.AddDate(0, 0, int(m.TermDays))
	}
	token, hash := newToken()
	url, err := s.subscriptionURL(token) // before anything is stored: no link, no user
	if err != nil {
		return nil, err
	}
	id := store.NewID("usr_")
	u := store.AccessUser{
		ID: id, Name: name, GroupID: m.GroupId, Status: ComputeStatus(false, expires, m.QuotaBytes, 0, now),
		AppHapp: happ, AppAmnezia: amnezia, AllNodes: all, NodeIDs: nodeIDs,
		QuotaBytes: m.QuotaBytes, QuotaReset: reset, PeriodStart: PeriodStart(reset, now), ExpiresAt: expires,
		DeviceLimit: limit, SpeedLimitBps: m.SpeedLimitBps, DNSPresetID: m.DnsPresetId,
		SubTokenHash: hash, SubTokenEnc: s.vault.Seal([]byte(token), id), CreatedAt: now,
	}
	dev := store.AccessDevice{ID: store.NewID("dev_"), UserID: id, Implicit: true, CreatedAt: now}
	creds, err := s.newCreds(id, dev.ID, happ, amnezia, nil)
	if err != nil {
		return nil, s.internal("issue credentials", err)
	}
	switch err := s.st.Access().CreateUser(ctx, u, dev, creds); {
	case errors.Is(err, store.ErrAccessExists):
		return nil, coded(connect.CodeAlreadyExists, "name_taken")
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("group or node")
	case err != nil:
		return nil, s.internal("create user", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, actor(ctx), "user_create", map[string]any{"user": id, "name": name})
	created, err := s.loadUsers(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.CreateUserResponse{User: created[0]}
	// The link and the page password are the user's credentials: an API token (and MCP behind it) never gets them, the
	// owner copies them in the admin panel (GetSubscriptionLink, closed to tokens).
	if auth.PrincipalFrom(ctx).Token {
		return connect.NewResponse(resp), nil
	}
	if resp.PagePassword, err = s.pagePassword(ctx, token); err != nil {
		return nil, err
	}
	resp.SubscriptionUrl = url
	return connect.NewResponse(resp), nil
}

func (s *Service) loadUsers(ctx context.Context, ids ...string) ([]*adminv1.User, error) {
	us, err := s.st.Access().UsersByIDs(ctx, ids)
	if err != nil {
		return nil, s.internal("load users", err)
	}
	return s.userProtos(ctx, us)
}

func (s *Service) ListUsers(ctx context.Context, req *connect.Request[adminv1.ListUsersRequest]) (*connect.Response[adminv1.ListUsersResponse], error) {
	m := req.Msg
	q := store.AccessUserQuery{Query: strings.TrimSpace(m.Query), GroupID: m.GroupId, Limit: int(m.PageSize), Now: s.now()}
	if q.Limit == 0 {
		q.Limit = 50
	}
	q.Limit = min(q.Limit, 200)
	switch m.Filter {
	case adminv1.UserFilter_USER_FILTER_UNSPECIFIED:
	case adminv1.UserFilter_USER_FILTER_ONLINE:
		q.Filter = "online"
	case adminv1.UserFilter_USER_FILTER_EXPIRING:
		q.Filter = "expiring"
	case adminv1.UserFilter_USER_FILTER_OVER_QUOTA:
		q.Filter = "over_quota"
	default:
		return nil, invalid("unknown filter")
	}
	for id := range s.online.OnlineUsers() {
		q.OnlineIDs = append(q.OnlineIDs, id)
	}
	if m.PageToken != "" {
		b, err := base64.RawURLEncoding.DecodeString(m.PageToken)
		if err != nil {
			return nil, invalid("bad page token")
		}
		q.After = string(b)
	}
	users, more, counts, err := s.st.Access().ListUsers(ctx, q)
	if err != nil {
		return nil, s.internal("list users", err)
	}
	resp := &adminv1.ListUsersResponse{Counts: &adminv1.UserCounts{
		All: uint32(counts.All), Online: uint32(counts.Online), Expiring: uint32(counts.Expiring), OverQuota: uint32(counts.OverQuota),
	}}
	if resp.Users, err = s.userProtos(ctx, users); err != nil {
		return nil, err
	}
	if more {
		resp.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(users[len(users)-1].Name))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetUser(ctx context.Context, req *connect.Request[adminv1.GetUserRequest]) (*connect.Response[adminv1.GetUserResponse], error) {
	a := s.st.Access()
	u, err := a.User(ctx, req.Msg.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("user")
	} else if err != nil {
		return nil, s.internal("get user", err)
	}
	protos, err := s.userProtos(ctx, []store.AccessUser{u})
	if err != nil {
		return nil, err
	}
	resp := &adminv1.GetUserResponse{User: protos[0]}
	now := s.now()

	devs, err := a.Devices(ctx, u.ID)
	if err != nil {
		return nil, s.internal("devices", err)
	}
	awgDevs, err := a.AWGDevices(ctx, u.ID)
	if err != nil {
		return nil, s.internal("awg devices", err)
	}
	awgByID := map[string]store.AccessAWGDevice{}
	for _, d := range awgDevs {
		awgByID[d.ID] = d
	}
	for _, d := range devs {
		if ad, ok := awgByID[d.ID]; ok {
			resp.Devices = append(resp.Devices, s.deviceProto(ad))
			continue
		}
		resp.Devices = append(resp.Devices, &adminv1.Device{
			Id: d.ID, Platform: d.Platform, Model: d.Model, FirstSeenUnix: d.FirstSeenAt.Unix(), LastSeenUnix: d.LastSeenAt.Unix(),
			Online: protos[0].Online, Protocols: d.Protocols,
		})
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -13)
	daily, err := a.DailyTraffic(ctx, u.ID, from)
	if err != nil {
		return nil, s.internal("daily traffic", err)
	}
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		resp.DailyTraffic = append(resp.DailyTraffic, &adminv1.DayTraffic{DayUnix: d.Unix(), Bytes: daily[d.Unix()]})
	}
	nt, err := a.NodeTraffic(ctx, u.ID, u.PeriodStart)
	if err != nil {
		return nil, s.internal("node traffic", err)
	}
	for _, t := range nt {
		resp.NodeTraffic = append(resp.NodeTraffic, &adminv1.NodeTraffic{NodeId: t.NodeID, NodeName: t.NodeName, Protocol: t.Protocol, Bytes: t.Bytes})
	}

	g, err := a.Group(ctx, u.GroupID)
	if err != nil {
		return nil, s.internal("group", err)
	}
	inGroup := map[string]bool{}
	for _, id := range g.ProfileIDs {
		inGroup[id] = true
	}
	profs, err := a.Profiles(ctx)
	if err != nil {
		return nil, s.internal("profiles", err)
	}
	for _, p := range profs {
		if inGroup[p.ID] {
			resp.Profiles = append(resp.Profiles, &adminv1.ProfileRef{Id: p.ID, Name: p.Name, Protocol: p.Protocol})
		}
	}

	nodes, err := a.Nodes(ctx)
	if err != nil {
		return nil, s.internal("nodes", err)
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	for _, n := range nodes {
		na := &adminv1.NodeAccess{
			NodeId: n.ID, NodeName: n.Name, CountryCode: n.CountryCode, Location: n.Location, Provider: n.Provider,
			Selected: u.AllNodes || slices.Contains(u.NodeIDs, n.ID),
		}
		for _, f := range full {
			if f.Node.ID != n.ID || !f.Inbound.Enabled {
				continue
			}
			if !slices.Contains(na.NodeProtocols, f.Profile.Protocol) {
				na.NodeProtocols = append(na.NodeProtocols, f.Profile.Protocol)
			}
			if inGroup[f.Profile.ID] && s.allowed(f.Profile.Protocol, u.AppHapp, u.AppAmnezia) && !slices.Contains(na.Protocols, f.Profile.Protocol) {
				na.Protocols = append(na.Protocols, f.Profile.Protocol)
			}
		}
		resp.NodeAccess = append(resp.NodeAccess, na)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) UpdateUser(ctx context.Context, req *connect.Request[adminv1.UpdateUserRequest]) (*connect.Response[adminv1.UpdateUserResponse], error) {
	m := req.Msg
	a := s.st.Access()
	old, err := a.User(ctx, m.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("user")
	} else if err != nil {
		return nil, s.internal("get user", err)
	}
	u := old
	u.NodeIDs = slices.Clone(old.NodeIDs)
	now := s.now()
	setNodes := false
	if m.Name != nil {
		if u.Name, err = cleanName("user", *m.Name); err != nil {
			return nil, err
		}
	}
	if m.SubscriptionName != nil {
		if u.SubscriptionName, err = cleanSubscriptionName(*m.SubscriptionName); err != nil {
			return nil, err
		}
	}
	if m.GroupId != nil {
		if *m.GroupId == "" {
			return nil, invalid("group is required")
		}
		u.GroupID = *m.GroupId
	}
	if m.QuotaBytes != nil {
		u.QuotaBytes = *m.QuotaBytes
	}
	if m.QuotaReset != nil {
		if *m.QuotaReset == adminv1.QuotaReset_QUOTA_RESET_UNSPECIFIED {
			return nil, invalid("unknown quota reset")
		}
		if u.QuotaReset, err = resetFromProto(*m.QuotaReset); err != nil {
			return nil, err
		}
		if u.QuotaReset != old.QuotaReset {
			u.PeriodStart = PeriodStart(u.QuotaReset, now)
		}
	}
	if m.ExpiresUnix != nil {
		u.ExpiresAt = timeOrZero(*m.ExpiresUnix)
	}
	if m.DeviceLimit != nil {
		if *m.DeviceLimit < 1 || *m.DeviceLimit > 100 {
			return nil, invalid("device limit must be 1-100")
		}
		u.DeviceLimit = int(*m.DeviceLimit)
	}
	if m.Apps != nil {
		u.AppHapp, u.AppAmnezia = m.Apps.Happ, m.Apps.Amnezia
		if !u.AppHapp && !u.AppAmnezia {
			return nil, invalid("at least one app must be on")
		}
	}
	if m.Nodes != nil {
		if u.AllNodes, u.NodeIDs, err = s.resolveNodes(ctx, m.Nodes); err != nil {
			return nil, err
		}
		setNodes = true
	}
	if m.SpeedLimitBps != nil {
		u.SpeedLimitBps = *m.SpeedLimitBps
	}
	if m.DnsPresetId != nil {
		if err := s.checkDNSPreset(ctx, *m.DnsPresetId); err != nil {
			return nil, err
		}
		u.DNSPresetID = *m.DnsPresetId // DNS is client-side (the subscription): no state push for the nodes
	}
	u.Status = ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, u.UsedBytes, now)
	var impact *adminv1.AccessImpact
	if u.GroupID != old.GroupID {
		if impact, err = s.groupChangeImpact(ctx, old, u.GroupID); err != nil {
			return nil, err
		}
	}
	if m.DryRun {
		users, err := s.loadUsers(ctx, old.ID)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&adminv1.UpdateUserResponse{User: users[0], Impact: impact}), nil
	}
	switch err := a.UpdateUser(ctx, u, setNodes); {
	case errors.Is(err, store.ErrAccessExists):
		return nil, coded(connect.CodeAlreadyExists, "name_taken")
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("user, group or node")
	case err != nil:
		return nil, s.internal("update user", err)
	}
	created, err := s.ensureCreds(ctx, u) // a newly enabled app may need credentials
	if err != nil {
		return nil, s.internal("issue credentials", err)
	}
	if created || u.Status != old.Status || u.GroupID != old.GroupID || u.AppHapp != old.AppHapp || u.AppAmnezia != old.AppAmnezia ||
		u.AllNodes != old.AllNodes || !slices.Equal(u.NodeIDs, old.NodeIDs) || u.SpeedLimitBps != old.SpeedLimitBps ||
		!u.ExpiresAt.Equal(old.ExpiresAt) {
		s.notify.StateChanged()
	}
	s.audit(ctx, actor(ctx), "user_update", map[string]any{"user": u.ID, "name": u.Name, "fields": changedUserFields(m)})
	users, err := s.loadUsers(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateUserResponse{User: users[0], Impact: impact}), nil
}

// groupChangeImpact is what moving the user to another group does to their profiles and AmneziaVPN keys.
func (s *Service) groupChangeImpact(ctx context.Context, u store.AccessUser, to string) (*adminv1.AccessImpact, error) {
	a := s.st.Access()
	from, err := a.Group(ctx, u.GroupID)
	if err != nil {
		return nil, s.internal("group", err)
	}
	next, err := a.Group(ctx, to)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("group")
	} else if err != nil {
		return nil, s.internal("group", err)
	}
	awg, err := a.AWGDevicesPerProfile(ctx, "", u.ID)
	if err != nil {
		return nil, s.internal("awg devices", err)
	}
	return s.impactOf(ctx, from.ProfileIDs, next.ProfileIDs, 1, awg)
}

func checkIDs(ids []string) error {
	if len(ids) == 0 {
		return invalid("no users given")
	}
	if len(ids) > maxBulk {
		return invalid("too many users at once (max %d)", maxBulk)
	}
	return nil
}

func (s *Service) SetUsersEnabled(ctx context.Context, req *connect.Request[adminv1.SetUsersEnabledRequest]) (*connect.Response[adminv1.SetUsersEnabledResponse], error) {
	if err := checkIDs(req.Msg.UserIds); err != nil {
		return nil, err
	}
	if err := s.st.Access().SetUsersDisabled(ctx, req.Msg.UserIds, !req.Msg.Enabled); err != nil {
		return nil, s.internal("set disabled", err)
	}
	if err := s.Recompute(ctx, req.Msg.UserIds); err != nil {
		return nil, s.internal("recompute", err)
	}
	users, err := s.loadUsers(ctx, req.Msg.UserIds...)
	if err != nil {
		return nil, err
	}
	action := map[bool]string{true: "users_enable", false: "users_disable"}[req.Msg.Enabled]
	s.audit(ctx, actor(ctx), action, usersParams(users))
	return connect.NewResponse(&adminv1.SetUsersEnabledResponse{Users: users}), nil
}

// ExtendUsers moves the term end to max(now, current) + days. Users without a term end are left alone:
// extending "forever" would silently put an end date on them.
func (s *Service) ExtendUsers(ctx context.Context, req *connect.Request[adminv1.ExtendUsersRequest]) (*connect.Response[adminv1.ExtendUsersResponse], error) {
	m := req.Msg
	if err := checkIDs(m.UserIds); err != nil {
		return nil, err
	}
	if m.Days < 1 || m.Days > 36500 {
		return nil, invalid("days must be 1-36500")
	}
	a := s.st.Access()
	us, err := a.UsersByIDs(ctx, m.UserIds)
	if err != nil {
		return nil, s.internal("load users", err)
	}
	now := s.now()
	for _, u := range us {
		if u.ExpiresAt.IsZero() {
			continue
		}
		if err := a.SetUserExpiry(ctx, u.ID, maxTime(now, u.ExpiresAt).AddDate(0, 0, int(m.Days))); err != nil {
			return nil, s.internal("extend", err)
		}
	}
	if err := s.Recompute(ctx, m.UserIds); err != nil {
		return nil, s.internal("recompute", err)
	}
	users, err := s.loadUsers(ctx, m.UserIds...)
	if err != nil {
		return nil, err
	}
	p := usersParams(users)
	p["days"] = m.Days
	s.audit(ctx, actor(ctx), "users_extend", p)
	return connect.NewResponse(&adminv1.ExtendUsersResponse{Users: users}), nil
}

// ResetUserTraffic zeroes used_bytes of the current period. The period start (so the next reset) and the traffic
// history stay; the status is recomputed, which lifts LIMITED, and the nodes hear about it through Recompute-style
// notification when a status changed.
func (s *Service) ResetUserTraffic(ctx context.Context, req *connect.Request[adminv1.ResetUserTrafficRequest]) (*connect.Response[adminv1.ResetUserTrafficResponse], error) {
	m := req.Msg
	if err := checkIDs(m.UserIds); err != nil {
		return nil, err
	}
	a := s.st.Access()
	us, err := a.UsersByIDs(ctx, m.UserIds)
	if err != nil {
		return nil, s.internal("load users", err)
	}
	if len(us) == 0 {
		return nil, notFound("user")
	}
	now := s.now()
	changed := false
	ids := make([]string, 0, len(us))
	for _, u := range us {
		status := ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, 0, now)
		if err := a.ResetUserPeriod(ctx, u.ID, u.PeriodStart, status); err != nil {
			return nil, s.internal("reset traffic", err)
		}
		changed = changed || status != u.Status
		ids = append(ids, u.ID)
		s.audit(ctx, actor(ctx), "user_reset_traffic", map[string]any{"user": u.ID, "was_bytes": u.UsedBytes})
	}
	if changed {
		s.notify.StateChanged()
	}
	users, err := s.loadUsers(ctx, ids...)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ResetUserTrafficResponse{Users: users}), nil
}

// usersParams are the audit params of a bulk change: how many users and the first names.
func usersParams(users []*adminv1.User) map[string]any {
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	return map[string]any{"count": len(users), "names": nameList(names)}
}

// nameList is "Marina, Oleg, Lena" for the audit log: the first five names, then "…".
func nameList(names []string) string {
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + ", …"
	}
	return strings.Join(names, ", ")
}

// changedUserFields names what an UpdateUser request sets, for the audit row.
func changedUserFields(m *adminv1.UpdateUserRequest) string {
	var f []string
	for _, x := range []struct {
		set  bool
		name string
	}{
		{m.Name != nil, "name"}, {m.GroupId != nil, "group"}, {m.QuotaBytes != nil, "quota"}, {m.QuotaReset != nil, "quota_reset"},
		{m.ExpiresUnix != nil, "expires"}, {m.DeviceLimit != nil, "device_limit"}, {m.Apps != nil, "apps"}, {m.Nodes != nil, "nodes"},
		{m.SpeedLimitBps != nil, "speed_limit"}, {m.DnsPresetId != nil, "dns"}, {m.SubscriptionName != nil, "subscription_name"},
	} {
		if x.set {
			f = append(f, x.name)
		}
	}
	return strings.Join(f, ",")
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (s *Service) RevokeDevice(ctx context.Context, req *connect.Request[adminv1.RevokeDeviceRequest]) (*connect.Response[adminv1.RevokeDeviceResponse], error) {
	userID, err := s.st.Access().RevokeDevice(ctx, req.Msg.DeviceId, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("device")
	} else if err != nil {
		return nil, s.internal("revoke device", err)
	}
	s.notify.StateChanged()
	users, err := s.loadUsers(ctx, userID)
	if err != nil || len(users) == 0 {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RevokeDeviceResponse{User: users[0]}), nil
}

func (s *Service) DeleteUsers(ctx context.Context, req *connect.Request[adminv1.DeleteUsersRequest]) (*connect.Response[adminv1.DeleteUsersResponse], error) {
	if err := checkIDs(req.Msg.UserIds); err != nil {
		return nil, err
	}
	gone, err := s.st.Access().UsersByIDs(ctx, req.Msg.UserIds) // their names, for the audit row
	if err != nil {
		return nil, s.internal("load users", err)
	}
	n, err := s.st.Access().DeleteUsers(ctx, req.Msg.UserIds)
	if err != nil {
		return nil, s.internal("delete users", err)
	}
	if n > 0 {
		s.notify.StateChanged()
		names := make([]string, len(gone))
		for i, u := range gone {
			names[i] = u.Name
		}
		s.audit(ctx, actor(ctx), "user_delete", map[string]any{"count": n, "names": nameList(names)})
	}
	return connect.NewResponse(&adminv1.DeleteUsersResponse{}), nil
}

func (s *Service) GetSubscriptionLink(ctx context.Context, req *connect.Request[adminv1.GetSubscriptionLinkRequest]) (*connect.Response[adminv1.GetSubscriptionLinkResponse], error) {
	a := s.st.Access()
	u, err := a.User(ctx, req.Msg.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("user")
	} else if err != nil {
		return nil, s.internal("get user", err)
	}
	var token string
	if req.Msg.Rotate {
		tok, hash := newToken()
		if err := a.SetSubToken(ctx, u.ID, hash, s.vault.Seal([]byte(tok), u.ID)); err != nil {
			return nil, s.internal("rotate token", err)
		}
		token = tok
	} else {
		pt, err := s.vault.Open(u.SubTokenEnc, u.ID)
		if err != nil {
			return nil, s.internal("open subscription token", err)
		}
		token = string(pt)
	}
	url, err := s.subscriptionURL(token)
	if err != nil {
		return nil, err
	}
	pw, err := s.pagePassword(ctx, token)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetSubscriptionLinkResponse{Url: url, PagePassword: pw}), nil
}

// pagePassword is the password of the page of a token, "" when the instance does not ask for one. It is computed
// (package pagepass), so a rotated link has a new one.
func (s *Service) pagePassword(ctx context.Context, token string) (string, error) {
	set, err := subsettings.Load(ctx, s.st)
	if err != nil {
		return "", s.internal("load subscription settings", err)
	}
	if !subsettings.PagePassword(set) {
		return "", nil
	}
	return pagepass.Password(s.vault.Derive(pagepass.KeyLabel), token), nil
}
