package access

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/auth"
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
	ViaCountry, ViaNodeID                    string // a config through a relay's entry; empty for the direct and the IPv6 one
	IPv6                                     bool   // the config goes to the node's IPv6 entry
	Server                                   string // the public name of the node ("Germany 2"): the user page shows this, never NodeName
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
	if utf8.RuneCountInString(label) > 40 || strings.ContainsFunc(label, store.UnsafeDisplayRune) {
		return "", "", invalid("label must be at most 40 characters without control or format characters")
	}
	return platform, label, nil
}

// CleanClientDeviceInput applies the platform and model rules used by the device page to client API enrolment.
func CleanClientDeviceInput(platform, model string) (string, string, error) {
	return cleanDeviceInput(platform, model, true)
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
func (s *Service) usableInbounds(ctx context.Context, u store.AccessUser, gs store.AccessGroups, profileID string) ([]store.AccessInboundFull, error) {
	full, err := s.st.Access().InboundsFull(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []store.AccessInboundFull
	for _, f := range full {
		if f.Profile.ID == profileID && s.usable(f, gs, u) {
			out = append(out, f)
		}
	}
	return out, nil
}

// checkAgents fails with "agent too old" when every previously connected node lacks awg/1.
func (s *Service) checkAgents(ins []store.AccessInboundFull) error {
	if len(ins) == 0 {
		return nil
	}
	for _, f := range ins {
		if f.Node.LastSeenAt.IsZero() || slices.Contains(f.Node.AgentCaps, agentcap.AWG) {
			return nil
		}
	}
	return failed("agent too old: update the node agent to use AmneziaWG")
}

// deviceScope is everything an operation on one AWG device needs, loaded once.
type deviceScope struct {
	user    store.AccessUser
	groups  store.AccessGroups
	dev     store.AccessAWGDevice
	profile store.AccessProfile
	proto   protocols.Protocol
	merged  json.RawMessage
	ins     []store.AccessInboundFull
	// relays and pathCreds come with the scope (loadDevice) so that rendering the configs through an entry costs no read of
	// its own; relaysKnown is false for a scope that was put together by hand (AddAWGDevice), which reads the relays itself.
	relays      []store.RelayLink
	relaysKnown bool
	pathCreds   map[string]store.AccessCred // by path; renderDeviceConfigs adds what it issues
}

// loadDevice finds a live AWG device and what goes with it. owner != "" restricts it to that user's devices; any
// other device is then "not found". Preconditions that make a config impossible are returned as FailedPrecondition.
func (s *Service) loadDevice(ctx context.Context, owner, deviceID string) (*deviceScope, error) {
	loaded, err := s.st.Access().AWGDeviceScope(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && owner != "" && loaded.Device.UserID != owner) {
		return nil, notFound("device")
	} else if err != nil {
		return nil, s.internal("get device", err)
	}
	sc := &deviceScope{user: loaded.User, groups: loaded.Groups, dev: loaded.Device, profile: loaded.Profile,
		relays: loaded.Relays, relaysKnown: true, pathCreds: map[string]store.AccessCred{}}
	for _, c := range loaded.PathCreds {
		sc.pathCreds[c.Path] = c
	}
	if sc.proto, err = s.awgPlugin(); err != nil {
		return nil, err
	}
	if sc.merged, err = s.mergedSettings(sc.profile); err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	if !sc.groups.Holds(sc.dev.ProfileID) {
		return nil, failed("profile_not_in_group")
	}
	for _, inbound := range loaded.Inbounds {
		if s.usable(inbound, sc.groups, sc.user) {
			sc.ins = append(sc.ins, inbound)
		}
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
	return s.AddAWGDeviceRoute(ctx, by, userID, profileID, platform, label, "")
}

// AddAWGDeviceRoute is AddAWGDevice for a key with a route: "" (all through the VPN) or awg.RouteRUDirect (the Russian services
// around the VPN). Whether the owner offers that route to people is the page's check (subs), not this one: the admin may issue it.
func (s *Service) AddAWGDeviceRoute(ctx context.Context, by, userID, profileID, platform, label, route string) (store.AccessAWGDevice, []DeviceConfig, error) {
	if route != "" && route != awg.RouteRUDirect {
		return store.AccessAWGDevice{}, nil, invalid("route must be empty or %q", awg.RouteRUDirect)
	}
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
	gs, err := a.UserGroups(ctx, u)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get group", err)
	}
	if !gs.Holds(p.ID) {
		return store.AccessAWGDevice{}, nil, failed("profile_not_in_group")
	}
	ins, err := s.usableInbounds(ctx, u, gs, p.ID)
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
	// Refused before the device exists: one with no config would only take a place in the limit.
	if ok, err := s.deviceWayIn(ctx, ins, merged, gs, u); err != nil {
		return store.AccessAWGDevice{}, nil, err
	} else if !ok {
		return store.AccessAWGDevice{}, nil, failed("no_inbound")
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
		ProfileID: p.ID, MaxIdx: maxIdx, Limit: u.DeviceLimit, Route: route,
	}, now, s.awgIssuer(proto, u.ID, devID, p.ID, merged))
	var limit *store.AccessLimitError
	switch {
	case errors.As(err, &limit):
		if eventErr := s.st.RecordDeviceLimitReached(ctx, u.ID, limit.Used, limit.Limit, limit.Mode, true, limit.WasIdle, now); eventErr != nil {
			s.log.Warn("access: device limit event", "code", "device_limit_event_write_failed", "user", u.ID, "err", eventErr)
		}
		return store.AccessAWGDevice{}, nil, failed("device_limit: %d/%d%s", limit.Used, limit.Limit, s.limitDevices(ctx, u.ID))
	case errors.Is(err, store.ErrAccessSubnetFull):
		return store.AccessAWGDevice{}, nil, failed("subnet_full")
	case errors.Is(err, store.ErrNotFound):
		return store.AccessAWGDevice{}, nil, notFound("user or profile")
	case err != nil:
		return store.AccessAWGDevice{}, nil, s.internal("add device", err)
	}
	s.notify.StateChanged()
	params := map[string]any{"user": u.ID, "device": devID, "profile": p.ID}
	if route != "" {
		params["route"] = route
	}
	s.audit(ctx, by, "device_create", params)
	dev, err := a.AWGDevice(ctx, devID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get device", err)
	}
	// Created at the profile's current epoch: the configs below are the current ones.
	cfgs, err := s.renderDeviceConfigs(ctx, &deviceScope{user: u, groups: gs, dev: dev, profile: p, proto: proto, merged: merged, ins: ins}, false, true)
	return dev, cfgs, err
}

// limitDevices says which devices fill a limit, for the message of the device_limit error: ": iPhone 15, Pixel 9, shared"
// (the label of each device, the apps and the keys alike, "shared" for the common place; at most six, then "…"), or "" when
// they cannot be read. It is the detail the admin's text puts in brackets, so it holds names and no words of a language.
func (s *Service) limitDevices(ctx context.Context, userID string) string {
	devs, err := s.st.Access().Devices(ctx, userID, s.now())
	if err != nil || len(devs) == 0 {
		return ""
	}
	const shown = 6
	names := make([]string, 0, shown+1)
	for _, d := range devs {
		if d.IsTelegramKey() {
			continue // it fills no place
		}
		if len(names) == shown {
			names = append(names, "…")
			break
		}
		name := cleanHeader(d.Model, 24)
		switch {
		case d.Implicit && d.Kind == store.DeviceKindLink:
			name = "shared"
		case name == "":
			name = d.Platform
		}
		if name == "" {
			name = "?"
		}
		names = append(names, name)
	}
	return ": " + strings.Join(names, ", ")
}

// DeviceConfigs renders the configs of a device for every usable node and marks the device as having received the
// profile's current epoch (its "needs a new key" badge goes away). owner "" = an admin, else the user the device
// must belong to. The returned device already shows the new epoch. Only the person's own fetch (owner set) counts as
// the device holding the DNS of every server now: an admin who looks at the keys has not delivered them.
func (s *Service) DeviceConfigs(ctx context.Context, by, owner, deviceID string) (store.AccessAWGDevice, []DeviceConfig, error) {
	sc, err := s.loadDevice(ctx, owner, deviceID)
	if err != nil {
		return store.AccessAWGDevice{}, nil, err
	}
	cfgs, err := s.renderDeviceConfigs(ctx, sc, true, owner != "")
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
	sc.pathCreds = map[string]store.AccessCred{} // the rotation revoked the keys of the paths: the next render issues new ones
	s.audit(ctx, by, "device_rotate", map[string]any{"user": sc.user.ID, "device": deviceID, "profile": sc.profile.ID})
	if sc.dev, err = a.AWGDevice(ctx, deviceID); err != nil {
		return store.AccessAWGDevice{}, nil, s.internal("get device", err)
	}
	cfgs, err := s.renderDeviceConfigs(ctx, sc, false, true) // the new credential carries the current epoch
	return sc.dev, cfgs, err
}

// ownDevice finds a live device that a person (owner != "") or the admin (owner "") may remove or rename: an AmneziaVPN key or
// a device of an app that reports a device ID. The shared device is the place of every app that reports none, no one's
// device: FailedPrecondition shared_device. Anyone else's device is "not found". A key comes with its AWG row (nil for a
// link device).
func (s *Service) ownDevice(ctx context.Context, owner, deviceID string) (store.AccessDevice, *store.AccessAWGDevice, error) {
	a := s.st.Access()
	dev, err := a.Device(ctx, deviceID, s.now())
	if errors.Is(err, store.ErrNotFound) || (err == nil && owner != "" && dev.UserID != owner) {
		return store.AccessDevice{}, nil, notFound("device")
	} else if err != nil {
		return store.AccessDevice{}, nil, s.internal("get device", err)
	}
	if dev.Kind == store.DeviceKindLink && dev.Implicit {
		return store.AccessDevice{}, nil, failed("shared_device")
	}
	if dev.Kind != store.DeviceKindKey {
		return dev, nil, nil
	}
	ad, err := a.AWGDevice(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) {
		return store.AccessDevice{}, nil, notFound("device")
	} else if err != nil {
		return store.AccessDevice{}, nil, s.internal("get device", err)
	}
	return dev, &ad, nil
}

// RelabelDevice changes the label of a device (device.model) and returns it (without DNSStale: the page keeps what it
// knows about that). Any device of the person but the shared one: a key or the device of an app (see ownDevice); the
// result of a link device is an AccessAWGDevice without AWG data (AWG false).
func (s *Service) RelabelDevice(ctx context.Context, owner, deviceID, label string) (store.AccessAWGDevice, error) {
	_, label, err := cleanDeviceInput("", label, false)
	if err != nil {
		return store.AccessAWGDevice{}, err
	}
	if label == "" {
		return store.AccessAWGDevice{}, invalid("label must not be empty")
	}
	dev, awgDev, err := s.ownDevice(ctx, owner, deviceID)
	if err != nil {
		return store.AccessAWGDevice{}, err
	}
	if err := s.st.Access().RenameDevice(ctx, deviceID, label); errors.Is(err, store.ErrNotFound) {
		return store.AccessAWGDevice{}, notFound("device")
	} else if err != nil {
		return store.AccessAWGDevice{}, s.internal("rename device", err)
	}
	if awgDev == nil {
		dev.Model = label
		return store.AccessAWGDevice{AccessDevice: dev}, nil
	}
	awgDev.Model = label
	return *awgDev, nil
}

// RevokeOwnDevice revokes a device of the given user (the public page's "remove"): an AmneziaVPN key or the device of an app
// that reports a device ID, not the shared device (see ownDevice). A device of someone else is "not found". Its credentials
// leave the nodes with the next desired state, which ends its sessions. The same app can register again as a new device
// (this is no block) if the user has room for it.
func (s *Service) RevokeOwnDevice(ctx context.Context, by, owner, deviceID string) error {
	dev, awgDev, err := s.ownDevice(ctx, owner, deviceID)
	if err != nil {
		return err
	}
	if _, err := s.st.Access().RevokeDevice(ctx, deviceID, s.now()); errors.Is(err, store.ErrNotFound) {
		return notFound("device")
	} else if err != nil {
		return s.internal("revoke device", err)
	}
	s.notify.StateChanged()
	params := map[string]any{"user": owner, "device": deviceID, "kind": dev.Kind}
	if awgDev != nil {
		params["profile"] = awgDev.ProfileID
	}
	s.audit(ctx, by, "device_revoke", params)
	return nil
}

// viaLabel is what a config through an entry node adds to the server's name: the entry's country, never its node name.
func viaLabel(country string) string {
	if cc := strings.ToUpper(strings.TrimSpace(country)); cc != "" {
		return " · via " + cc
	}
	return " · via entry"
}

// deviceWayIn reports whether a device of these inbounds would get a config: an inbound whose address is shown, or a hidden
// one with an IPv6 entry or a usable relay (the paths renderDeviceConfigs gives).
func (s *Service) deviceWayIn(ctx context.Context, ins []store.AccessInboundFull, merged json.RawMessage, gs store.AccessGroups, u store.AccessUser) (bool, error) {
	if slices.ContainsFunc(ins, func(f store.AccessInboundFull) bool { return f.Inbound.DirectInSubscriptions }) {
		return true, nil
	}
	links, err := s.st.RelayLinks(ctx, store.RelayFilter{})
	if err != nil {
		return false, s.internal("read relays for device configs", err)
	}
	relaysOf := s.subRelays(links, ins, gs, u, plugin.FormatURIList)
	for _, f := range ins {
		if spec, err := s.buildSpec(f, merged); err == nil && len(serverPaths(f.Node, spec, relaysOf[f.Inbound.ID])) > 1 {
			return true, nil
		}
	}
	return false, nil
}

// renderDeviceConfigs renders the .conf and the vpn:// key of the device for each usable inbound of its profile.
// markEpoch records the profile's current epoch on the credential (the user now holds current configs); recordDNS records
// the DNS each config carries (the key now holds it), what "stale DNS" is measured against.
func (s *Service) renderDeviceConfigs(ctx context.Context, sc *deviceScope, markEpoch, recordDNS bool) ([]DeviceConfig, error) {
	pt, err := s.vault.Open(sc.dev.SecretEnc, sc.dev.CredID)
	if err != nil {
		return nil, s.internal("open device key", err)
	}
	nodeDNS := s.newNodeDNS(ctx, sc.user.ID)
	relayLinks := sc.relays
	if !sc.relaysKnown {
		var err error
		if relayLinks, err = s.st.RelayLinks(ctx, store.RelayFilter{}); err != nil {
			return nil, s.internal("read relays for device configs", err)
		}
	}
	relaysOf := s.subRelays(relayLinks, sc.ins, sc.groups, sc.user, plugin.FormatURIList)
	maxIdx, maxIdxErr := awg.MaxPeerIndex(sc.merged)
	version := awgVersion(sc.merged)
	var reqs []protocols.ClientReq
	for _, r := range protocols.MinClientsOf(sc.proto, sc.merged) {
		if r.Client == plugin.ClientAmnezia {
			reqs = append(reqs, r)
		}
	}
	nodes := make([]store.AccessNode, len(sc.ins))
	for i, f := range sc.ins {
		nodes[i] = f.Node
	}
	title, brand, lang := s.keyNaming(ctx)
	public, names, files := keyNames(title, brand, lang, nodes)
	var out []DeviceConfig
	pathAdded, hidden := false, false
	fileUsed := map[string]bool{}
	for _, name := range files {
		fileUsed[name] = true
	}
	held := map[string]string{} // node id -> the resolver pair the config of that node carries
	for i, f := range sc.ins {
		spec, err := s.buildSpec(f, sc.merged)
		if err != nil {
			s.log.Error("access: cannot build inbound for a device config", "inbound", f.Inbound.ID, "err", err)
			continue
		}
		// The DNS of this node (the person's pick, else the node's default, else their own rule): a key holds one resolver
		// pair per server.
		servers, splitLost := nodeDNS.awg(f.Node.ID)
		_, dnsFallback := awg.PickDNS(servers)
		warnings := []string{"amnezia_desktop_mtu"}
		if dnsFallback {
			warnings = append(warnings, "dns_fallback")
		}
		if splitLost {
			warnings = append(warnings, "dns_no_split")
		}
		in := protocols.RenderInput{
			Inbound: inboundView(nodeView(f.Node), spec, ""), Settings: sc.merged,
			UserID: sc.user.ID, UserName: sc.user.Name, DeviceID: sc.dev.ID, Label: sc.dev.Model,
			Secret: string(pt), Peer: pt, InboundPublic: json.RawMessage(f.Inbound.PluginPublicJSON),
			DNS: servers, NodeAddr: f.Node.Address, DisplayName: names[i],
			// Same rule as the subscription: no IPv6 address of the tunnel where the exit gives the client no IPv6.
			NoIPv6Egress: !ipv6Egress(f.Node, egressOf(sc.merged)),
			RUDirect:     sc.dev.Route == awg.RouteRUDirect,
		}
		in.Format = plugin.FormatAWGConf
		conf, ok1 := sc.proto.Render(in)
		in.Format = plugin.FormatAmneziaVPN
		key, ok2 := sc.proto.Render(in)
		if !ok1 || !ok2 {
			s.log.Error("access: cannot render a device config", "inbound", f.Inbound.ID, "device", sc.dev.ID)
			continue
		}
		held[f.Node.ID] = dnsSig(servers)
		// Same rule as the subscription: an inbound whose address is hidden gives only its other paths (IPv6 entry, relays).
		if f.Inbound.DirectInSubscriptions {
			out = append(out, DeviceConfig{
				InboundID: f.Inbound.ID, NodeID: f.Node.ID, NodeName: f.Node.Name, CountryCode: f.Node.CountryCode, Server: public[i],
				ProfileName: f.Profile.Name, AWGVersion: version, Conf: string(conf.Data), VPNKey: string(key.Data),
				ConfFilename: files[i], Stale: sc.dev.Stale(),
				MinClients: reqs, Warnings: warnings,
			})
		} else {
			hidden = true
		}
		if maxIdxErr != nil {
			continue
		}
		for _, p := range serverPaths(f.Node, spec, relaysOf[f.Inbound.ID]) {
			if p.Kind == pathDirect {
				continue
			}
			path := p.Key
			cred, have := sc.pathCreds[path]
			if !have {
				var created bool
				var err error
				cred, created, err = s.st.Access().EnsureKeyPathCred(ctx, sc.dev.ID, sc.profile.ID, path, maxIdx, s.now(),
					s.awgIssuer(sc.proto, sc.user.ID, sc.dev.ID, sc.profile.ID, sc.merged))
				if errors.Is(err, store.ErrAccessSubnetFull) {
					s.log.Warn("access: AWG path key network is full for a static device", "code", "awg_path_network_full", "user", sc.user.ID,
						"device", sc.dev.ID, "profile", sc.profile.ID, "path", path, "err", err)
					continue
				}
				if errors.Is(err, store.ErrNotFound) {
					continue // the device, the entry or the relay ceased to be eligible while its configs were being rendered
				}
				if err != nil {
					s.log.Warn("access: cannot issue an AWG path key for a static device", "code", "awg_path_credential_issue_failed", "user", sc.user.ID,
						"device", sc.dev.ID, "profile", sc.profile.ID, "path", path, "err", err)
					continue
				}
				if sc.pathCreds == nil {
					sc.pathCreds = map[string]store.AccessCred{}
				}
				sc.pathCreds[path] = cred
				pathAdded = pathAdded || created
			}
			pathSecret, err := s.vault.Open(cred.SecretEnc, cred.ID)
			if err != nil {
				s.log.Error("access: cannot open an AWG path key for a static device", "code", "awg_path_credential_open_failed", "device", sc.dev.ID, "path", path, "err", err)
				continue
			}
			// The IPv6 twin is the same server at the entry's address; a twin through a relay is the exit's server at the entry node.
			mark, suffix := " · IPv6", "-ipv6"
			cfg := DeviceConfig{
				InboundID: f.Inbound.ID, NodeID: f.Node.ID, NodeName: f.Node.Name, CountryCode: f.Node.CountryCode,
				IPv6: p.Kind == pathIPv6, ProfileName: f.Profile.Name, AWGVersion: version,
				Stale: sc.dev.Stale(), MinClients: reqs, Warnings: warnings,
			}
			pathInput := p.input(in, f, spec)
			if p.Kind == pathRelay {
				// the entry's country names the config, never its node name
				mark, suffix = viaLabel(p.Relay.ViaNodeCountry), "-via-"+cmp.Or(slug(p.Relay.ViaNodeCountry), "entry")
				pathInput.NodeAddr = p.Relay.ViaNodeAddress
				cfg.ViaCountry, cfg.ViaNodeID = p.Relay.ViaNodeCountry, p.Relay.ViaNodeID
			}
			pathInput.Secret, pathInput.Peer = string(pathSecret), pathSecret
			pathInput.DisplayName = names[i] + mark
			pathInput.Format = plugin.FormatAWGConf
			pathConf, confOK := sc.proto.Render(pathInput)
			pathInput.Format = plugin.FormatAmneziaVPN
			pathKey, keyOK := sc.proto.Render(pathInput)
			if !confOK || !keyOK {
				s.log.Error("access: cannot render an AWG path config", "code", "awg_path_config_render_failed", "inbound", f.Inbound.ID, "device", sc.dev.ID, "path", path)
				continue
			}
			// A second path of the same name gets a number.
			base := strings.TrimSuffix(files[i], ".conf") + suffix
			filename := base + ".conf"
			for k := 2; fileUsed[filename]; k++ {
				filename = base + "-" + strconv.Itoa(k) + ".conf"
			}
			fileUsed[filename] = true
			cfg.Server, cfg.Conf, cfg.VPNKey, cfg.ConfFilename = public[i]+mark, string(pathConf.Data), string(pathKey.Data), filename
			out = append(out, cfg)
		}
	}
	if len(out) == 0 && hidden {
		return nil, failed("no_inbound") // every address is hidden and no IPv6 entry or relay is usable
	}
	if len(out) == 0 {
		return nil, s.internal("render device config", errors.New("no inbound produced a config"))
	}
	if sc.dev.Route == awg.RouteRUDirect {
		held[ruSigKey] = awg.RUDirectVersion // the version of the list this key holds, measured by markDNSStale
	}
	if markEpoch || recordDNS {
		var epoch *int64
		if markEpoch {
			epoch = &sc.dev.CriticalEpoch
		}
		var sig map[string]string
		if recordDNS {
			sig = held
		}
		if err := s.st.Access().RecordDeviceConfig(ctx, sc.dev.CredID, epoch, sig); err != nil {
			return nil, s.internal("record the device config", err)
		}
	}
	if pathAdded {
		s.notify.StateChanged()
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
