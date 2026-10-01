package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// AWG devices of the users. One device = one WireGuard peer of ONE profile: a key
// pair and a tunnel address from the profile's network, valid on every inbound of the profile that the user may
// use. The methods below are the one implementation behind DeviceService (admin) and the self-service endpoints
// of the public page; `by` names who acts for the audit row: an admin id, or "user:<id>" for the page.

// DeviceConfig is the config of a device on one node.
type DeviceConfig struct {
	InboundID, NodeID, NodeName, CountryCode string
	ProfileName, AWGVersion                  string // "3.1" | "2.0"
	Conf                                     string // WireGuard-style .conf (AmneziaVPN and AmneziaWG import it)
	VPNKey                                   string // the vpn:// key for AmneziaVPN
	ConfFilename                             string // already safe, never derived from raw user input
	// Stale: the device held an older config of this profile that this one replaces.
	Stale      bool
	MinClients []protocols.ClientReq
	// Warnings are stable codes the UI localises: amnezia_desktop_mtu, dns_fallback, dns_no_split.
	Warnings []string
}

var devicePlatforms = []string{"ios", "android", "windows", "macos", "linux", "other"}

// cleanDeviceInput validates the platform and the label of a new or renamed device. An empty label stays empty.
func cleanDeviceInput(platform, label string, needPlatform bool) (string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if needPlatform {
		if platform == "" {
			platform = "other"
		}
		if !slices.Contains(devicePlatforms, platform) {
			return "", "", invalid("platform must be one of %s", strings.Join(devicePlatforms, ", "))
		}
	}
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > 40 || strings.ContainsFunc(label, func(r rune) bool { return r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 }) {
		return "", "", invalid("label must be at most 40 characters without control characters")
	}
	return platform, label, nil
}

// actor names the caller of an admin RPC for the audit log.
func actor(ctx context.Context) string {
	if a, ok := auth.AdminFrom(ctx); ok {
		return a.ID
	}
	return "anonymous"
}

// audit writes one row. params must not hold a key or a config.
func (s *Service) audit(ctx context.Context, by, action string, params map[string]any) {
	b, _ := json.Marshal(params)
	e := store.AuditEntry{Actor: by, Action: action, Params: string(b), Result: "ok"} // Source: the caller's (token: api or mcp), else panel
	if ip := auth.ClientIPFrom(ctx); ip.IsValid() {
		e.IP = ip.String()
	}
	if err := s.st.Audit(ctx, s.now(), e); err != nil {
		s.log.Error("access: audit write failed", "action", action, "err", err)
	}
}

// awgPlugin returns the per-device plugin (awg) or an error when this panel has none.
func (s *Service) awgPlugin() (protocols.Protocol, error) {
	p, ok := s.reg.Get(awg.ID)
	if !ok {
		return nil, failed("AmneziaWG is not available on this panel")
	}
	return p, nil
}

// awgVersion reads the protocol version out of profile settings ("" when unreadable).
func awgVersion(settings []byte) string {
	var v struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(settings, &v)
	return v.Version
}

// awgIssuer makes the store callback that issues the credential of a peer index: keys and addresses from the
// plugin, the secret sealed by the vault (AAD = the new credential's id).
func (s *Service) awgIssuer(proto protocols.Protocol, userID, deviceID, profileID string, merged json.RawMessage) store.AWGIssue {
	return func(idx int) (store.AccessCred, string, error) {
		iss, err := proto.IssueCredential(protocols.IssueInput{
			UserID: userID, DeviceID: deviceID, ProfileID: profileID, Settings: merged, PeerIndex: idx,
		})
		if err != nil {
			return store.AccessCred{}, "", err
		}
		var nd awg.NodeData
		if err := json.Unmarshal(iss.NodeData, &nd); err != nil || nd.PublicKey == "" {
			return store.AccessCred{}, "", fmt.Errorf("the plugin issued node data without a public key")
		}
		id := store.NewID("crd_")
		return store.AccessCred{
			ID: id, Protocol: proto.ID(), SecretEnc: s.vault.Seal([]byte(iss.Secret), id), DataJSON: string(iss.NodeData),
		}, nd.PublicKey, nil
	}
}

// usableInbounds are the inbounds of a profile that one user can connect to now (the subscription half of the
// effective-access rule, see usable).
func (s *Service) usableInbounds(ctx context.Context, u store.AccessUser, g store.AccessGroup, profileID string) ([]store.AccessInboundFull, error) {
	full, err := s.st.Access().InboundsFull(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []store.AccessInboundFull
	for _, f := range full {
		if f.Profile.ID == profileID && s.usable(f, g, u) {
			out = append(out, f)
		}
	}
	return out, nil
}

// checkAgents fails with "agent too old" when every node of the inbounds announced that it cannot run awg.
func (s *Service) checkAgents(ins []store.AccessInboundFull) error {
	if s.caps == nil || len(ins) == 0 {
		return nil
	}
	for _, f := range ins {
		if known, has := s.caps.AgentCapability(f.Node.ID, "awg/1"); !known || has {
			return nil
		}
	}
	return failed("agent too old: update the node agent to use AmneziaWG")
}

// deviceScope is everything an operation on one AWG device needs, loaded once.
type deviceScope struct {
	user    store.AccessUser
	group   store.AccessGroup
	dev     store.AccessAWGDevice
	profile store.AccessProfile
	proto   protocols.Protocol
	merged  json.RawMessage
	ins     []store.AccessInboundFull
}

// loadDevice finds a live AWG device and what goes with it. owner != "" restricts it to that user's devices; any
// other device is then "not found". Preconditions that make a config impossible are returned as FailedPrecondition.
func (s *Service) loadDevice(ctx context.Context, owner, deviceID string) (*deviceScope, error) {
	a := s.st.Access()
	dev, err := a.AWGDevice(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && owner != "" && dev.UserID != owner) {
		return nil, notFound("device")
	} else if err != nil {
		return nil, s.internal("get device", err)
	}
	sc := &deviceScope{dev: dev}
	if sc.user, err = a.User(ctx, dev.UserID); err != nil {
		return nil, s.internal("get user", err)
	}
	if sc.group, err = a.Group(ctx, sc.user.GroupID); err != nil {
		return nil, s.internal("get group", err)
	}
	if sc.profile, err = a.Profile(ctx, dev.ProfileID); err != nil {
		return nil, s.internal("get profile", err)
	}
	if sc.proto, err = s.awgPlugin(); err != nil {
		return nil, err
	}
	if sc.merged, err = s.mergedSettings(sc.profile); err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	if !slices.Contains(sc.group.ProfileIDs, dev.ProfileID) {
		return nil, failed("profile_not_in_group")
	}
	if sc.ins, err = s.usableInbounds(ctx, sc.user, sc.group, dev.ProfileID); err != nil {
		return nil, s.internal("inbounds", err)
	}
	if len(sc.ins) == 0 {
		return nil, failed("no_inbound")
	}
	return sc, nil
}

// AddAWGDevice creates an AWG device of a user on a profile and returns it with its configs. Preconditions, each
// a FailedPrecondition whose message starts with a stable code: user_disabled, app_disabled, profile_not_in_group,
// no_inbound, "agent too old", device_limit ("device_limit: 5/5"), subnet_full.
func (s *Service) AddAWGDevice(ctx context.Context, by, userID, profileID, platform, label string) (store.AccessAWGDevice, []DeviceConfig, error) {
	platform, label, err := cleanDeviceInput(platform, label, true)
	if err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	if profileID == "" {
		return store.AccessAWGDevice{}, nil, invalid("profile is required")
	}
	a := s.st.Access()
	u, err := a.User(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return store.AccessAWGDevice{}, nil, notFound("user")
	} else if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get user", err)
	}
	if u.Disabled {
		return store.AccessAWGDevice{}, nil, failed("user_disabled")
	}
	if !u.AppAmnezia {
		return store.AccessAWGDevice{}, nil, failed("app_disabled")
	}
	proto, err := s.awgPlugin()
	if err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	p, err := a.Profile(ctx, profileID)
	if errors.Is(err, store.ErrNotFound) {
		return store.AccessAWGDevice{}, nil, notFound("profile")
	} else if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get profile", err)
	}
	if p.Protocol != proto.ID() {
		return store.AccessAWGDevice{}, nil, invalid("profile %q is not an AmneziaWG profile", p.Name)
	}
	g, err := a.Group(ctx, u.GroupID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get group", err)
	}
	if !slices.Contains(g.ProfileIDs, p.ID) {
		return store.AccessAWGDevice{}, nil, failed("profile_not_in_group")
	}
	ins, err := s.usableInbounds(ctx, u, g, p.ID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("inbounds", err)
	}
	if len(ins) == 0 {
		return store.AccessAWGDevice{}, nil, failed("no_inbound")
	}
	if err := s.checkAgents(ins); err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	merged, err := s.mergedSettings(p)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("open profile secrets", err)
	}
	maxIdx, err := awg.MaxPeerIndex(merged)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("profile network", err)
	}
	if label == "" {
		existing, err := a.AWGDevices(ctx, u.ID)
		if err != nil {
			return store.AccessAWGDevice{}, nil, s.internal("devices", err)
		}
		n := 1
		for _, d := range existing {
			if d.Platform == platform {
				n++
			}
		}
		label = fmt.Sprintf("%s %d", platform, n)
	}
	devID := store.NewID("dev_")
	now := s.now()
	_, err = a.AddAWGDevice(ctx, store.AWGDeviceAdd{
		Device:    store.AccessDevice{ID: devID, UserID: u.ID, Platform: platform, Model: label},
		ProfileID: p.ID, MaxIdx: maxIdx, Limit: u.DeviceLimit,
	}, now, s.awgIssuer(proto, u.ID, devID, p.ID, merged))
	var limit *store.AccessLimitError
	switch {
	case errors.As(err, &limit):
		return store.AccessAWGDevice{}, nil, failed("device_limit: %d/%d", limit.Used, limit.Limit)
	case errors.Is(err, store.ErrAccessSubnetFull):
		return store.AccessAWGDevice{}, nil, failed("subnet_full")
	case errors.Is(err, store.ErrNotFound):
		return store.AccessAWGDevice{}, nil, notFound("user or profile")
	case err != nil:
		return store.AccessAWGDevice{}, nil, s.internal("add device", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, by, "device_create", map[string]any{"user": u.ID, "device": devID, "profile": p.ID})
	dev, err := a.AWGDevice(ctx, devID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get device", err)
	}
	// Created at the profile's current epoch: the configs below are the current ones.
	cfgs, err := s.renderDeviceConfigs(ctx, &deviceScope{user: u, group: g, dev: dev, profile: p, proto: proto, merged: merged, ins: ins}, false)
	return dev, cfgs, err
}

// DeviceConfigs renders the configs of a device for every usable node and marks the device as having received the
// profile's current epoch (its "needs a new key" badge goes away). owner "" = an admin, else the user the device
// must belong to. The returned device already shows the new epoch.
func (s *Service) DeviceConfigs(ctx context.Context, by, owner, deviceID string) (store.AccessAWGDevice, []DeviceConfig, error) {
	sc, err := s.loadDevice(ctx, owner, deviceID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	cfgs, err := s.renderDeviceConfigs(ctx, sc, true)
	if err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	s.audit(ctx, by, "device_configs", map[string]any{"user": sc.user.ID, "device": deviceID, "profile": sc.profile.ID})
	sc.dev.ConfigEpoch = max(sc.dev.ConfigEpoch, sc.dev.CriticalEpoch)
	return sc.dev, cfgs, nil
}

// RotateDevice gives the device a new key pair and PSK on the same tunnel address. The old key stops working with
// the next desired state.
func (s *Service) RotateDevice(ctx context.Context, by, owner, deviceID string) (store.AccessAWGDevice, []DeviceConfig, error) {
	sc, err := s.loadDevice(ctx, owner, deviceID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	a := s.st.Access()
	_, err = a.RotateAWGDevice(ctx, deviceID, s.now(), s.awgIssuer(sc.proto, sc.user.ID, deviceID, sc.profile.ID, sc.merged))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.AccessAWGDevice{}, nil, notFound("device")
	case err != nil:
		return store.AccessAWGDevice{}, nil, s.internal("rotate device", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, by, "device_rotate", map[string]any{"user": sc.user.ID, "device": deviceID, "profile": sc.profile.ID})
	if sc.dev, err = a.AWGDevice(ctx, deviceID); err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get device", err)
	}
	cfgs, err := s.renderDeviceConfigs(ctx, sc, false) // the new credential carries the current epoch
	return sc.dev, cfgs, err
}

// RelabelDevice changes the label of a device (device.model) and returns it.
func (s *Service) RelabelDevice(ctx context.Context, owner, deviceID, label string) (store.AccessAWGDevice, error) {
	_, label, err := cleanDeviceInput("", label, false)
	if err != nil {
		return store.AccessAWGDevice{}, err
	}
	if label == "" {
		return store.AccessAWGDevice{}, invalid("label must not be empty")
	}
	a := s.st.Access()
	dev, err := a.AWGDevice(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && owner != "" && dev.UserID != owner) {
		return store.AccessAWGDevice{}, notFound("device")
	} else if err != nil {
		return store.AccessAWGDevice{}, s.internal("get device", err)
	}
	if err := a.RenameDevice(ctx, deviceID, label); errors.Is(err, store.ErrNotFound) {
		return store.AccessAWGDevice{}, notFound("device")
	} else if err != nil {
		return store.AccessAWGDevice{}, s.internal("rename device", err)
	}
	dev.Model = label
	return dev, nil
}

// RevokeOwnDevice revokes an AWG device of the given user (the public page's "remove"); a device of someone else
// is "not found". The peer leaves the nodes with the next desired state, which ends its sessions.
func (s *Service) RevokeOwnDevice(ctx context.Context, by, owner, deviceID string) error {
	a := s.st.Access()
	dev, err := a.AWGDevice(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && dev.UserID != owner) {
		return notFound("device")
	} else if err != nil {
		return s.internal("get device", err)
	}
	if _, err := a.RevokeDevice(ctx, deviceID, s.now()); errors.Is(err, store.ErrNotFound) {
		return notFound("device")
	} else if err != nil {
		return s.internal("revoke device", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, by, "device_revoke", map[string]any{"user": owner, "device": deviceID, "profile": dev.ProfileID})
	return nil
}

// awgDNS returns the plain IPv4 resolvers of the user's effective DNS preset that an AWG client can carry, and
// whether the preset's split-direct rules are lost (a .conf carries one pair of addresses, no split).
func (s *Service) awgDNS(ctx context.Context, userID string) (servers []string, splitLost bool) {
	pre, _, err := s.dns.Effective(ctx, userID)
	if err != nil {
		s.log.Warn("access: cannot read the user's dns preset", "err", err)
		return nil, false
	}
	eps, _ := pre.EndpointsFor(dns.ClientAmneziaWG)
	for _, e := range eps {
		if e.Kind != dns.KindPlain {
			continue
		}
		if a, err := netip.ParseAddr(e.Address); err == nil {
			servers = append(servers, a.String())
		} else if ap, err := netip.ParseAddrPort(e.Address); err == nil {
			servers = append(servers, ap.Addr().String())
		}
	}
	return servers, pre.SplitDirect && len(pre.Split) > 0
}

// renderDeviceConfigs renders the .conf and the vpn:// key of the device for each usable inbound of its profile.
// markReceived records the profile's current epoch on the credential (the user now holds current configs).
func (s *Service) renderDeviceConfigs(ctx context.Context, sc *deviceScope, markReceived bool) ([]DeviceConfig, error) {
	pt, err := s.vault.Open(sc.dev.SecretEnc, sc.dev.CredID)
	if err != nil {
		return nil, s.internal("open device key", err)
	}
	servers, splitLost := s.awgDNS(ctx, sc.user.ID)
	_, dnsFallback := awg.PickDNS(servers)
	version := awgVersion(sc.merged)
	var reqs []protocols.ClientReq
	for _, r := range protocols.MinClientsOf(sc.proto, sc.merged) {
		if r.Client == plugin.ClientAmnezia {
			reqs = append(reqs, r)
		}
	}
	warnings := []string{"amnezia_desktop_mtu"}
	if dnsFallback {
		warnings = append(warnings, "dns_fallback")
	}
	if splitLost {
		warnings = append(warnings, "dns_no_split")
	}
	nodes := make([]store.AccessNode, len(sc.ins))
	for i, f := range sc.ins {
		nodes[i] = f.Node
	}
	title, brand, lang := s.keyNaming(ctx)
	names, files := keyNames(title, brand, lang, nodes)
	var out []DeviceConfig
	for i, f := range sc.ins {
		spec, err := s.buildSpec(f, sc.merged)
		if err != nil {
			s.log.Error("access: cannot build inbound for a device config", "inbound", f.Inbound.ID, "err", err)
			continue
		}
		in := protocols.RenderInput{
			Inbound: inboundView(nodeView(f.Node), spec, ""), Settings: sc.merged,
			UserID: sc.user.ID, UserName: sc.user.Name, DeviceID: sc.dev.ID, Label: sc.dev.Model,
			Secret: string(pt), Peer: pt, InboundPublic: json.RawMessage(f.Inbound.PluginPublicJSON),
			DNS: servers, NodeAddr: f.Node.Address, DisplayName: names[i],
		}
		in.Format = plugin.FormatAWGConf
		conf, ok1 := sc.proto.Render(in)
		in.Format = plugin.FormatAmneziaVPN
		key, ok2 := sc.proto.Render(in)
		if !ok1 || !ok2 {
			s.log.Error("access: cannot render a device config", "inbound", f.Inbound.ID, "device", sc.dev.ID)
			continue
		}
		out = append(out, DeviceConfig{
			InboundID: f.Inbound.ID, NodeID: f.Node.ID, NodeName: f.Node.Name, CountryCode: f.Node.CountryCode,
			ProfileName: f.Profile.Name, AWGVersion: version, Conf: string(conf.Data), VPNKey: string(key.Data),
			ConfFilename: files[i], Stale: sc.dev.Stale(),
			MinClients: reqs, Warnings: warnings,
		})
	}
	if len(out) == 0 {
		return nil, s.internal("render device config", errors.New("no inbound produced a config"))
	}
	if markReceived {
		if err := s.st.Access().SetConfigEpoch(ctx, sc.dev.CredID, sc.dev.CriticalEpoch); err != nil {
			return nil, s.internal("record the received config", err)
		}
	}
	return out, nil
}

// deviceAddress lists the tunnel addresses of a device without masks: "10.66.4.5, fd66:66:0:1::5".
func deviceAddress(dataJSON string) string {
	var nd awg.NodeData
	if json.Unmarshal([]byte(dataJSON), &nd) != nil {
		return ""
	}
	var out []string
	for _, a := range nd.AllowedIPs {
		host, _, _ := strings.Cut(a, "/")
		out = append(out, host)
	}
	return strings.Join(out, ", ")
}
