package access

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ErrUnknownToken is returned for a subscription token that belongs to nobody.
var ErrUnknownToken = errors.New("access: unknown subscription token")

// SubView is what the public subscription endpoint needs about one token.
type SubView struct {
	UserID           string
	UserName         string
	SubscriptionName string
	Status           string // active | disabled | expired | limited
	// Usage in the current quota period (Up + Down = the user's used bytes), quota and term end; zero = none.
	Up, Down, Total uint64
	Expires         time.Time
	// Lines are the client fragments, one per usable inbound; empty unless Status is active. In the default
	// format they are hysteria2:// and vless:// URIs; for SubOptions.Format = FormatMihomo each is one proxy as an element of a
	// YAML list, rendered by its plugin (the assembler adds groups, dns and rules).
	Lines []string

	// What the public user page and the server names need; all optional (a fake Source may leave them zero).
	// Servers parallels Lines (same order) with the facts behind each line: the subscription handler renders the
	// remark (#fragment) from them. Devices, QuotaReset and the rest describe the user, not the protocols.
	Servers     []SubServer
	QuotaReset  string // none | day | week | month | rolling_month
	NextReset   time.Time
	DeviceLimit int
	// DevicesUsed counts the devices that count against DeviceLimit (live credentials); Devices lists them.
	DevicesUsed int
	Devices     []SubDevice
	// AWGProfiles are the AmneziaWG profiles the user could add a device on right now (in the group, with a usable
	// inbound; empty unless the Amnezia toggle is on).
	AWGProfiles []SubAWGProfile
	// AccessHapp / AccessAmnezia: the user's app toggle AND a usable deployed inbound whose protocol that app
	// can consume (the user page shows only what works).
	AccessHapp, AccessAmnezia bool
	// AppAmnezia is the user's AmneziaVPN toggle alone (AccessAmnezia also needs a live inbound and an active user): a person
	// whose subscription ended still sees the keys they hold and can remove them.
	AppAmnezia bool
	// Nodes are the servers of the person, one per node, in subscription order: the ones a link carries and the ones a key
	// can be made for. Empty unless Status is active.
	Nodes []SubNode
	// DNSLink is the DNS of the apps that take the link (they have one resolver for the whole subscription): the person's pick for
	// them, what applies without it and what the owner offers. DNSPresets are the presets the page names: the ones the nodes
	// and the link apps offer and apply. Both empty unless Status is active.
	DNSLink      dns.LinkDNS
	DNSPresets   []SubDNSPreset
	EffectiveDNS *dns.Preset // the preset of the link apps (Mihomo, Xray JSON, sing-box), already read with the node choices
	// Format is what Lines hold (FormatURIList unless the view was asked for another).
	Format plugin.ClientFormat
	// LinkMode is the mode of the device limit for the apps on the link (store.LinkModeTrack, LinkModeEnforce or LinkModeStrict):
	// the page words the shared place and the full limit by it.
	LinkMode string
	// Refusal says why an app that is entitled to servers gets none: RefusalDeviceLimit, RefusalDeviceBlocked or
	// RefusalNoDeviceID. "" for everyone served, for a person without access (Status says why) and for the page, which never
	// registers a device. Lines and Servers are empty with it; the subscription handler answers with one entry that says why.
	Refusal string
}

// The reasons of SubView.Refusal. They are stable words: the subscription handler words them for the apps (subs.stateNote).
const (
	// RefusalDeviceLimit: the person has no room for one more device (ENFORCE and STRICT) and none of theirs is silent enough to
	// give its place away.
	RefusalDeviceLimit = "device_limit"
	// RefusalDeviceBlocked: the owner blocked the device of the app.
	RefusalDeviceBlocked = "device_blocked"
	// RefusalNoDeviceID: STRICT, and the app reports no device ID (X-HWID).
	RefusalNoDeviceID = "no_device_id"
)

// SubNode is one node as the page's server list shows it.
type SubNode struct {
	ID          string
	Name        string // the panel's node name: never emitted as user-facing text
	CountryCode string
	Location    string
	// Online: the node's agent is connected (a live session, or a network sample not older than 90 seconds).
	Online bool
	// LoadPercent is as in SubServer: nil when the capacity or a fresh sample is missing.
	LoadPercent *int
	Conns       []SubConn
	DNS         *SubNodeDNS // nil: the owner offers no choice on this node
}

// SubConn is one way to use a node: by the link or by a key.
type SubConn struct {
	Way       string // "link" | "key"
	Exit      string // "direct" | "warp"
	ProfileID string // the AmneziaWG profile of a key
	Server    int    // a link: the index into SubView.Servers (unused when MihomoOnly)
	// MihomoOnly: a link that only the Mihomo apps can use (Gecko). The URI list has no server for it, so Server means nothing.
	MihomoOnly bool
}

// SubServer is one Lines entry with the facts the remark (server name) is built from.
type SubServer struct {
	URI         string // the same string as the matching Lines entry (a proxy for the Mihomo format)
	Protocol    string // plugin id
	ProfileID   string
	NodeID      string // internal grouping key; never emitted as user-facing text
	Node        string // node name
	CountryCode string // node country, ISO 3166-1 alpha-2 or ""
	Location    string
	Profile     string // profile name
	Exit        string // "direct" | "warp"
	// Made is the inbound's rank in creation order. Servers are listed by country and protocol, but a repeated name is
	// numbered in creation order, so a server added later never takes the name of an older one (a Mihomo select group
	// remembers the chosen server by name).
	Made int
	// LoadPercent is the larger of this node's RX/TX rates as a percentage of its configured symmetric capacity.
	// It is nil when capacity is unknown or the node has no fresh sample. The rates themselves stay out of the view:
	// whatever reaches a user must not tell when the other person on a node streams.
	LoadPercent *int
}

// SubDevice is one device of the user as the public page lists it.
type SubDevice struct {
	ID, Platform, Model string
	// Kind is "link" (an app that reports a device ID), "shared" (the common place of the apps that report none) or "key" (an
	// AmneziaVPN key). The person can remove and rename the first and the last.
	Kind string
	// OSVersion and Client are what the app last said about itself (X-Ver-OS, the first token of its User-Agent); "" if nothing.
	OSVersion, Client string
	App               string // "happ" | "amnezia" | ""
	LastSeen          time.Time
	Online            bool
	AWG               *SubAWG // set for an AmneziaWG device
}

// SubAWG is what the public page shows about an AmneziaWG device. The keys are not part of it: the page fetches
// them on demand (DeviceConfigs).
type SubAWG struct {
	ProfileID, ProfileName string
	Version                string // "3.1" | "2.0"
	Address                string // "10.66.4.5, fd66:66:0:1::5"
	Stale                  bool   // the profile changed in a way that breaks the config the device holds
	// DNSStale lists the nodes where the DNS that applies to the person is not the one this device's key holds (see markDNSStale).
	DNSStale      []string
	LastHandshake time.Time
	MinClients    []protocols.ClientReq
}

// SubAWGProfile is an AmneziaWG profile a device can be added on. The page names it by what a person can tell apart:
// the countries of its nodes, its exit and its version, never by the profile's own name.
type SubAWGProfile struct {
	ID, Name, Version string
	Egress            string   // "direct" | "warp"
	Countries         []string // country codes of its usable nodes, in subscription order, without repeats ("" left out)
}

// SubOptions says how a subscription view is rendered.
type SubOptions struct {
	// Format of the lines; "" = FormatURIList. FormatMihomo also gives the request's device its AWG credentials (one per
	// usable AWG profile, made on the first fetch).
	Format plugin.ClientFormat
	// Device is what the request says about its device (its HWID above all). With a HWID the app gets a device of its own, with
	// credentials of its own; without one, or when the user has no room for another device, the shared device of the user.
	Device DeviceHint
	// Name is the server name a Mihomo proxy carries; nil = "<node> · <profile>".
	Name func(SubServer) string
	// NoTouch: the view is for the page, not for an app, so it writes nothing: no device, no credentials, no mark that the
	// subscription was fetched. A server the person has no credentials for yet is shown with a throwaway secret.
	NoTouch bool
	// NoPageData: the view is for an app, so it leaves out what only the user page shows (the DNS of each node, the
	// presets they name, the keys that hold an older DNS) and does not read it.
	NoPageData bool
	// IncludeEffectiveDNS lets the writer of a profile (Mihomo, Xray JSON, sing-box) reuse the effective preset read with node DNS.
	IncludeEffectiveDNS bool
}

// Subscription resolves a token. The lookup is by sha256(token), so the comparison never sees the token
// itself. For an active user it makes sure the request's device holds its credentials (the device is created on the first
// fetch of an app, e.g. after a revoke) and renders one line per usable inbound.
func (s *Service) Subscription(ctx context.Context, token string) (SubView, error) {
	return s.SubscriptionWith(ctx, token, SubOptions{})
}

// CheckSubscriptionToken checks whether a cached subscription token still exists, without building its view.
func (s *Service) CheckSubscriptionToken(ctx context.Context, token string) error {
	found, err := s.st.Access().HasUserWithTokenHash(ctx, hashToken(token))
	if err != nil {
		return err
	}
	if !found {
		return ErrUnknownToken
	}
	return nil
}

// SubscriptionWith is Subscription with a client format (see SubOptions).
func (s *Service) SubscriptionWith(ctx context.Context, token string, opt SubOptions) (SubView, error) {
	u, err := s.st.Access().UserByTokenHash(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return SubView{}, ErrUnknownToken
	} else if err != nil {
		return SubView{}, err
	}
	return s.subView(ctx, u, !opt.NoTouch, opt)
}

// PreviewSubscription is Subscription for the admin's preview of the user page: it looks the user up by id
// (ErrUnknownToken when there is none), writes nothing, and also returns the
// subscription link ("" when the panel has no public address configured).
func (s *Service) PreviewSubscription(ctx context.Context, userID string) (SubView, string, error) {
	u, err := s.st.Access().User(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return SubView{}, "", ErrUnknownToken
	} else if err != nil {
		return SubView{}, "", err
	}
	v, err := s.subView(ctx, u, false, SubOptions{})
	if err != nil {
		return SubView{}, "", err
	}
	pt, err := s.vault.Open(u.SubTokenEnc, u.ID)
	if err != nil {
		return SubView{}, "", err
	}
	link, _ := s.subscriptionURL(string(pt)) // not configured: the page then has no link, which is what it would get
	return v, link, nil
}

// credKey names a credential in the secrets map: a per-device protocol holds one credential per profile.
func credKey(protocol, profileID string) string {
	if profileID == "" {
		return protocol
	}
	return protocol + "/" + profileID
}

func subscriptionProtocolRank(protocol string) int {
	switch protocol {
	case "hysteria2":
		return 0
	case "vless":
		return 1
	case "awg":
		return 2
	default:
		return 3
	}
}

func compareSubscriptionInbounds(a, b store.AccessInboundFull) int {
	if a.Node.CountryCode == "" && b.Node.CountryCode != "" {
		return 1
	}
	if a.Node.CountryCode != "" && b.Node.CountryCode == "" {
		return -1
	}
	if c := strings.Compare(a.Node.CountryCode, b.Node.CountryCode); c != 0 {
		return c
	}
	if c := strings.Compare(a.Node.Location, b.Node.Location); c != 0 {
		return c
	}
	if c := strings.Compare(a.Node.Name, b.Node.Name); c != 0 {
		return c
	}
	if c := strings.Compare(a.Node.ID, b.Node.ID); c != 0 {
		return c
	}
	if ar, br := subscriptionProtocolRank(a.Profile.Protocol), subscriptionProtocolRank(b.Profile.Protocol); ar != br {
		return ar - br
	} else if ar == 3 {
		if c := strings.Compare(a.Profile.Protocol, b.Profile.Protocol); c != 0 {
			return c
		}
	}
	if c := strings.Compare(egressOf(json.RawMessage(a.Profile.SettingsJSON)), egressOf(json.RawMessage(b.Profile.SettingsJSON))); c != 0 {
		return c
	}
	if c := strings.Compare(a.Profile.Name, b.Profile.Name); c != 0 {
		return c
	}
	if c := a.Inbound.CreatedAt.Compare(b.Inbound.CreatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.Inbound.ID, b.Inbound.ID)
}

// subView builds the view of one user; touch is an app's fetch: it makes the request's device and its credentials and
// records the fetch on the device. The page and the preview (touch false) write nothing.
func (s *Service) subView(ctx context.Context, u store.AccessUser, touch bool, opt SubOptions) (SubView, error) {
	a := s.st.Access()
	now := s.now()
	// Not the stored status: expiry and quota are exact at this instant even before the sweep ran.
	ps, reset := AdvancePeriod(u.QuotaReset, u.PeriodStart, now)
	if reset {
		u.UsedBytes, u.PeriodStart = 0, ps
	}
	v := SubView{
		UserID: u.ID, UserName: u.Name, SubscriptionName: u.SubscriptionName, Status: ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, u.UsedBytes, now),
		Total: u.QuotaBytes, Expires: u.ExpiresAt, QuotaReset: u.QuotaReset, NextReset: NextReset(u.QuotaReset, u.PeriodStart),
		DeviceLimit: u.DeviceLimit, AppAmnezia: u.AppAmnezia,
	}
	var hash []byte // the request's device: its HWID, hashed with the user; nil = the shared device
	if opt.Device.HWID != "" {
		hash = store.HWIDHash(u.ID, opt.Device.HWID)
	}
	data, err := a.SubscriptionData(ctx, u.ID, u.GroupID, u.PeriodStart, v.Status == StatusActive, now, hash)
	if err != nil {
		return SubView{}, err
	}
	made := make(map[string]int, len(data.Inbounds)) // the store returns creation order: SubServer.Made
	for i, f := range data.Inbounds {
		made[f.Inbound.ID] = i
	}
	slices.SortStableFunc(data.Inbounds, compareSubscriptionInbounds)
	liveByNode := make(map[string]store.NodeLiveRow, len(data.LiveNodes))
	for _, live := range data.LiveNodes {
		liveByNode[live.NodeID] = live
	}
	v.Up = min(data.Up, u.UsedBytes) // used_bytes is authoritative; the buckets only give the split
	v.Down = u.UsedBytes - v.Up
	devs, awgDevs := data.Devices, data.AWG
	awgByID := map[string]store.AccessAWGDevice{}
	for _, d := range awgDevs {
		awgByID[d.ID] = d
	}
	for _, d := range devs {
		sd := SubDevice{ID: d.ID, Platform: d.Platform, Model: d.Model, Kind: subDeviceKind(d), OSVersion: d.OSVersion, Client: d.Client,
			LastSeen: d.LastSeenAt, Online: d.Kind == store.DeviceKindLink && len(data.OnlineDevices[d.ID]) > 0}
		for _, pid := range d.Protocols {
			if p, ok := s.reg.Get(pid); ok {
				if happ, amnezia, _ := clientApps(p); happ {
					sd.App = "happ"
				} else if amnezia {
					sd.App = "amnezia"
				}
				break
			}
		}
		if ad, ok := awgByID[d.ID]; ok {
			sd.LastSeen = ad.LastSeenAt
			sd.AWG = &SubAWG{
				ProfileID: ad.ProfileID, ProfileName: ad.ProfileName, Version: awgVersion([]byte(ad.ProfileSettingsJSON)),
				Address: deviceAddress(ad.DataJSON), Stale: ad.Stale(), LastHandshake: ad.LastSeenAt,
				MinClients: s.awgMinClients(ad.ProfileSettingsJSON),
			}
		}
		v.Devices = append(v.Devices, sd)
	}
	v.DevicesUsed, v.LinkMode = len(devs), data.LinkMode
	if v.Status != StatusActive {
		// The app keeps fetching after access ended: the touch is what lets health notice that the person is trying.
		if touch {
			dev, m := data.Device, opt.Device.meta()
			if dev.ID == "" && hash != nil {
				// An app that reports a HWID no device is known by (and none is made for a person without access): the shared
				// device stands for it, without taking the app's details.
				dev, m = sharedDevice(data.Devices), deviceMeta{}
			}
			if dev.ID != "" && now.Sub(dev.LastSeenAt) >= deviceTouchEvery {
				s.touchDevice(dev.ID, now, m)
			}
		}
		return v, nil
	}

	rd := requestDevice{dev: data.Device, creds: data.DeviceCreds, hash: hash}
	g, full := data.Group, data.Inbounds
	if touch {
		if rd, v.Refusal, err = s.ensureLinkCreds(ctx, u, opt.Device, rd, data); err != nil {
			return SubView{}, err
		}
		if v.Refusal == "" && opt.Format == plugin.FormatMihomo {
			rd, v.Refusal = s.ensureMihomoAWG(ctx, u, g, full, opt.Device, rd, data)
		}
		if v.Refusal != "" {
			if rd.added {
				s.notify.StateChanged()
			}
			return v, nil // a refused app gets no server, and the request wrote nothing for it
		}
		if rd.added {
			s.notify.StateChanged()
		}
		if rd.dev.ID != "" && now.Sub(rd.dev.LastSeenAt) >= deviceTouchEvery {
			m := opt.Device.meta()
			if rd.hash == nil && opt.Device.HWID != "" {
				m.OSVersion = "" // the app took the shared place: its OS is not the place's
			}
			s.touchDevice(rd.dev.ID, now, m)
		}
	}
	secrets := map[string]string{}
	for _, c := range rd.creds {
		pt, err := s.vault.Open(c.SecretEnc, c.ID)
		if err != nil {
			return SubView{}, err
		}
		secrets[credKey(c.Protocol, c.ProfileID)] = string(pt)
	}
	if !touch {
		// The page lists the person's servers, which it renders from a secret: a person with no device yet gets throwaway ones,
		// issued for nobody and never stored (this view is not an app's, see cachedView.app).
		if err := s.placeholderSecrets(u, rd.dev.Protocols, secrets); err != nil {
			return SubView{}, err
		}
	}

	format := opt.Format
	if format == "" {
		format = plugin.FormatURIList
	}
	v.Format = format
	var networkNodeIDs []string
	capacityMbps := map[string]int{}
	for _, f := range full {
		if !s.usable(f, g, u) {
			continue
		}
		if _, exists := s.reg.Get(f.Profile.Protocol); exists {
			networkNodeIDs = append(networkNodeIDs, f.Node.ID)
			capacityMbps[f.Node.ID] = f.Node.BandwidthMbps
		}
	}
	networkUsage := CurrentNetworkUtilization(networkNodeIDs, capacityMbps, liveByNode, now)
	var nd *nodeDNS // read on first use: only the page and the AmneziaWG proxies of a Mihomo profile need it
	dnsOf := func() *nodeDNS {
		if nd == nil {
			nd = s.newNodeDNS(ctx, u.ID)
		}
		return nd
	}
	awgAt := map[string]int{}  // profile id -> its index in v.AWGProfiles
	nodeAt := map[string]int{} // node id -> its index in v.Nodes
	nodeOf := func(f store.AccessInboundFull) *SubNode {
		i, ok := nodeAt[f.Node.ID]
		if !ok {
			i = len(v.Nodes)
			nodeAt[f.Node.ID] = i
			n := SubNode{ID: f.Node.ID, Name: f.Node.Name, CountryCode: f.Node.CountryCode, Location: f.Node.Location}
			if usage, ok := networkUsage[f.Node.ID]; ok {
				n.LoadPercent = usage.LoadPercent
			}
			v.Nodes = append(v.Nodes, n)
		}
		return &v.Nodes[i]
	}
	for _, f := range full {
		if !s.usable(f, g, u) {
			continue
		}
		proto, _ := s.reg.Get(f.Profile.Protocol)
		happ, amnezia, _ := clientApps(proto)
		v.AccessHapp = v.AccessHapp || happ && u.AppHapp
		v.AccessAmnezia = v.AccessAmnezia || amnezia && u.AppAmnezia
		merged, err := s.mergedSettings(f.Profile)
		if err != nil {
			return SubView{}, err
		}
		if protocols.IsPerDevice(proto) {
			i, ok := awgAt[f.Profile.ID]
			if !ok {
				i = len(v.AWGProfiles)
				awgAt[f.Profile.ID] = i
				v.AWGProfiles = append(v.AWGProfiles, SubAWGProfile{ID: f.Profile.ID, Name: f.Profile.Name, Version: awgVersion(merged), Egress: egressOf(merged)})
			}
			if cc := strings.ToUpper(f.Node.CountryCode); cc != "" && !slices.Contains(v.AWGProfiles[i].Countries, cc) {
				v.AWGProfiles[i].Countries = append(v.AWGProfiles[i].Countries, cc)
			}
			n := nodeOf(f)
			n.Conns = append(n.Conns, SubConn{Way: "key", Exit: egressOf(merged), ProfileID: f.Profile.ID})
		}
		key := f.Profile.Protocol
		if protocols.IsPerDevice(proto) {
			key = credKey(key, f.Profile.ID)
		}
		secret := secrets[key]
		if secret == "" {
			continue
		}
		spec, err := s.buildSpec(f, merged)
		if err != nil {
			s.log.Error("access: cannot build inbound for subscription", "inbound", f.Inbound.ID, "err", err)
			continue
		}
		if _, pinOK := protocols.NormalizePin(f.Inbound.CertPinSHA256); spec.TLS.Mode == plugin.TLSSelfSigned && !pinOK {
			continue // the node has not reported its certificate yet: a client could not verify it
		}
		srv := SubServer{NodeID: f.Node.ID, Node: f.Node.Name, CountryCode: f.Node.CountryCode, Location: f.Node.Location,
			Profile: f.Profile.Name, Protocol: f.Profile.Protocol, ProfileID: f.Profile.ID, Exit: egressOf(merged), Made: made[f.Inbound.ID]}
		if usage, ok := networkUsage[f.Node.ID]; ok {
			srv.LoadPercent = usage.LoadPercent
		}
		in := protocols.RenderInput{
			Format: format, Settings: merged, Inbound: inboundView(nodeView(f.Node), spec, f.Inbound.CertPinSHA256),
			UserID: u.ID, UserName: u.Name, DeviceID: rd.dev.ID, Secret: secret,
			InboundPublic: json.RawMessage(f.Inbound.PluginPublicJSON),
		}
		if protocols.IsPerDevice(proto) {
			// An AmneziaWG proxy carries a resolver of its own: the DNS of its node. (Hysteria2 has none; the URI list has no AWG.)
			var dnsServers []string
			if format == plugin.FormatMihomo {
				dnsServers, _ = dnsOf().awg(f.Node.ID)
			}
			in.Peer, in.DNS, in.NodeAddr = json.RawMessage(secret), dnsServers, f.Node.Address
		}
		if opt.Name != nil {
			in.DisplayName = opt.Name(srv)
		}
		frag, ok := proto.Render(in)
		if ok {
			srv.URI = string(frag.Data)
			v.Lines = append(v.Lines, srv.URI)
			v.Servers = append(v.Servers, srv)
			if !protocols.IsPerDevice(proto) { // the page lists a link by its Hysteria2 servers; the keys are counted above
				n := nodeOf(f)
				n.Conns = append(n.Conns, SubConn{Way: "link", Exit: srv.Exit, Server: len(v.Servers) - 1})
			}
		} else if format == plugin.FormatURIList && !protocols.IsPerDevice(proto) {
			// The URI list leaves out what only the Mihomo apps can use (Gecko), but it is still the person's server: the page lists it.
			in.Format = plugin.FormatMihomo
			if _, ok := proto.Render(in); ok {
				n := nodeOf(f)
				n.Conns = append(n.Conns, SubConn{Way: "link", Exit: srv.Exit, MihomoOnly: true})
			}
		}
	}
	for i := range v.Nodes {
		v.Nodes[i].Online = liveByNode[v.Nodes[i].ID].Connected
	}
	if opt.NoPageData {
		if format != plugin.FormatURIList && opt.IncludeEffectiveDNS && nd != nil {
			if preset, ok := nd.linkApps(); ok {
				v.EffectiveDNS = &preset
			}
		}
		return v, nil
	}
	n := dnsOf()
	s.markDNSStale(u, g, full, n, awgDevs)
	for _, ad := range awgDevs {
		if i := slices.IndexFunc(v.Devices, func(d SubDevice) bool { return d.ID == ad.ID }); i >= 0 && v.Devices[i].AWG != nil {
			v.Devices[i].AWG.DNSStale = ad.DNSStale
		}
	}
	s.nodeDNSData(ctx, &v, n, awgDevs)
	return v, nil
}

// requestDevice is the device a subscription request is served from, with its live credentials. hash is the one it is looked
// up by (nil: the shared device). added: credentials were issued for it in this request, which the nodes must hear about.
type requestDevice struct {
	dev   store.AccessDevice
	creds []store.AccessCred
	hash  []byte
	added bool
}

// subDeviceKind is SubDevice.Kind of a device.
func subDeviceKind(d store.AccessDevice) string {
	switch {
	case d.Kind == store.DeviceKindKey:
		return "key"
	case d.Implicit:
		return "shared"
	}
	return "link"
}

// sharedDevice is the shared device among the user's devices, or none.
func sharedDevice(devs []store.AccessDevice) store.AccessDevice {
	for _, d := range devs {
		if d.Implicit && d.Kind == store.DeviceKindLink {
			return d
		}
	}
	return store.AccessDevice{}
}

// ensureLinkCreds makes the request's device hold a credential for every protocol the user's apps allow. A device that
// has them all costs nothing (this is the path of every fetch of a known device). Otherwise the snapshot may already refuse the
// request (admit: reads only), and one store call fills the device in, takes the shared device over for the first HWID of the
// user, creates the device, or, in ENFORCE and STRICT, creates it in the place of a device that has been silent for 30 days.
// In TRACK a user with twice the limit in live devices gets no device for one more HWID: that app takes the shared place, as
// an app without a HWID does. refusal is a Refusal* reason, "" when the request is served.
func (s *Service) ensureLinkCreds(ctx context.Context, u store.AccessUser, hint DeviceHint, rd requestDevice, data store.SubscriptionData) (requestDevice, string, error) {
	have := make(map[string]bool, len(rd.dev.Protocols))
	for _, protocol := range rd.dev.Protocols {
		have[protocol] = true
	}
	if len(s.missingProtocols(u.AppHapp, u.AppAmnezia, have)) == 0 {
		return rd, "", nil // a user of the Amnezia app alone has none to give, and no device is made for nothing
	}
	if refusal := s.admit(u, rd, data); refusal != "" {
		return rd, refusal, nil
	}
	m := hint.meta()
	template := store.AccessDevice{ID: store.NewID("dev_"), UserID: u.ID, HWIDHash: rd.hash, Implicit: rd.hash == nil,
		Platform: m.Platform, Model: m.Model, OSVersion: m.OSVersion, Client: m.Client}
	res, err := s.st.Access().EnsureLinkDevice(ctx, template, u.DeviceLimit, s.now(), func(deviceID string, have map[string]bool) ([]store.AccessCred, error) {
		return s.newCreds(u.ID, deviceID, u.AppHapp, u.AppAmnezia, have)
	})
	if err != nil {
		if refusal, fallback := s.refusalOf(u, err); refusal != "" {
			return rd, refusal, nil
		} else if fallback {
			hint.HWID = ""
			return s.ensureLinkCreds(ctx, u, hint, requestDevice{}, data)
		}
		return rd, "", err
	}
	if res.Device.ID == "" {
		return rd, "", nil
	}
	s.placed(u, data, res)
	return requestDevice{dev: res.Device, creds: res.Creds, hash: rd.hash, added: len(res.Added) > 0}, "", nil
}

// admit is the refusal the snapshot of a fetch already holds for a request whose device does not exist yet. The store makes
// the same decision again, with guards, when it is called; this one only spares the call (and its reads) to an app that is
// refused on every refresh.
func (s *Service) admit(u store.AccessUser, rd requestDevice, data store.SubscriptionData) string {
	err := data.LinkRefusal(u.DeviceLimit, rd.hash != nil, s.now())
	if err == nil {
		return ""
	}
	refusal, _ := s.refusalOf(u, err)
	return refusal
}

// refusalOf maps what the store said about a new device to a Refusal* reason. fallback: TRACK has no room for this HWID, and
// the app takes the shared place instead. A limit that was reached makes the device_limit_reached event, once an hour.
func (s *Service) refusalOf(u store.AccessUser, err error) (refusal string, fallback bool) {
	var limit *store.AccessLimitError
	switch {
	case errors.Is(err, store.ErrAccessBlocked):
		return RefusalDeviceBlocked, false
	case errors.Is(err, store.ErrNoSharedDevice):
		return RefusalNoDeviceID, false
	case errors.As(err, &limit):
		// TRACK refuses nobody: the app takes the shared place, and the event is a sighting. ENFORCE and STRICT turn it away.
		refused := limit.Mode != store.LinkModeTrack
		s.limitReached(u.ID, limit.Used, u.DeviceLimit, limit.Mode, refused)
		if !refused {
			return "", true
		}
		return RefusalDeviceLimit, false
	}
	return "", false
}

// placed records what a device that was made in this request means for the limit: in TRACK, a person who now has more
// devices than the limit is an event for the owner to see. A place that a silent device gave away is no such case.
func (s *Service) placed(u store.AccessUser, data store.SubscriptionData, res store.LinkDeviceResult) {
	if res.Born && u.DeviceLimit > 0 && res.Replaced == "" && len(data.Devices) >= u.DeviceLimit {
		s.limitReached(u.ID, len(data.Devices)+1, u.DeviceLimit, data.LinkMode, false)
	}
}

// limitReached writes the device_limit_reached event off the request (the store keeps one an hour per user; the same hour is
// remembered here too, so that a refused app that keeps asking does not even queue the read). On Cloudflare the work runs
// after the response (AfterResponse).
func (s *Service) limitReached(userID string, used, limit int, mode string, refused bool) {
	now := s.now()
	key := "lim:" + userID + ":" + strconv.FormatBool(refused)
	if at, seen := s.limited.LoadOrStore(key, now); seen {
		if now.Sub(at.(time.Time)) < time.Hour {
			return
		}
		s.limited.Store(key, now)
	}
	s.afterResponse(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.st.RecordDeviceLimitReached(ctx, userID, used, limit, mode, refused, now); err != nil {
			s.log.Warn("access: device limit event", "user", userID, "err", err)
		}
	})
}

// placeholderSecrets fills secrets with a throwaway one for every protocol the user's apps allow and that has none: what the
// user page needs to list a server for a person whose device does not exist yet. Per-device protocols have none.
func (s *Service) placeholderSecrets(u store.AccessUser, protocolsHeld []string, secrets map[string]string) error {
	have := make(map[string]bool, len(protocolsHeld))
	for _, protocol := range protocolsHeld {
		have[protocol] = true
	}
	for _, p := range s.missingProtocols(u.AppHapp, u.AppAmnezia, have) {
		iss, err := p.IssueCredential(protocols.IssueInput{UserID: u.ID})
		if err != nil {
			return err
		}
		secrets[p.ID()] = iss.Secret
	}
	return nil
}

// awgMinClients are the minimum client versions of an AWG profile, from its settings without secrets (the
// requirements depend only on the version).
func (s *Service) awgMinClients(settings string) []protocols.ClientReq {
	p, ok := s.reg.Get(awg.ID)
	if !ok {
		return nil
	}
	return protocols.MinClientsOf(p, json.RawMessage(settings))
}

// ensureMihomoAWG gives the request's device an AWG credential for every AWG profile the user can use, for the Mihomo
// format (see SubOptions). added reports whether any was made. A full profile network is logged and only that profile's
// proxy is skipped; the subscription itself must not fail for it, nor for the ceiling of devices in TRACK (the shared device
// then). The device may be born here (a person with only the AmneziaVPN access has no link credential to be born with): it
// is refused like in ensureLinkCreds, and refusal says so.
func (s *Service) ensureMihomoAWG(ctx context.Context, u store.AccessUser, g store.AccessGroup, full []store.AccessInboundFull, hint DeviceHint, rd requestDevice, data store.SubscriptionData) (requestDevice, string) {
	proto, ok := s.reg.Get(awg.ID)
	if !ok {
		return rd, ""
	}
	m := hint.meta()
	template := store.AccessDevice{ID: rd.dev.ID, UserID: u.ID, HWIDHash: rd.hash, Implicit: rd.hash == nil,
		Platform: m.Platform, Model: m.Model, OSVersion: m.OSVersion, Client: m.Client}
	if template.ID == "" {
		template.ID = store.NewID("dev_")
	}
	var want []store.AWGImplicitWant
	seen := map[string]bool{}
	for _, f := range full {
		if f.Profile.Protocol != awg.ID || seen[f.Profile.ID] || !s.usable(f, g, u) {
			continue
		}
		seen[f.Profile.ID] = true
		merged, err := s.mergedSettings(f.Profile)
		if err != nil {
			s.log.Error("access: cannot open profile secrets", "profile", f.Profile.ID, "err", err)
			continue
		}
		maxIdx, err := awg.MaxPeerIndex(merged)
		if err != nil {
			s.log.Error("access: bad AWG network", "profile", f.Profile.ID, "err", err)
			continue
		}
		want = append(want, store.AWGImplicitWant{
			ProfileID: f.Profile.ID,
			MaxIdx:    maxIdx,
			Issue:     s.awgIssuer(proto, u.ID, template.ID, f.Profile.ID, merged),
		})
	}
	if len(want) == 0 {
		return rd, ""
	}
	if rd.dev.ID == "" {
		if refusal := s.admit(u, rd, data); refusal != "" {
			return rd, refusal
		}
	}
	result, err := s.st.Access().EnsureLinkAWGCreds(ctx, template, u.DeviceLimit, s.now(), want)
	if err != nil {
		if refusal, fallback := s.refusalOf(u, err); refusal != "" {
			return rd, refusal
		} else if fallback {
			// No device of its own for this HWID: the shared device holds the proxies, as for an app without a HWID.
			hint.HWID = ""
			return s.ensureMihomoAWG(ctx, u, g, full, hint, requestDevice{added: rd.added}, data)
		}
		s.log.Warn("access: cannot issue AWG credentials for a Mihomo subscription", "user", u.ID, "err", err)
		return rd, ""
	}
	for _, profileID := range result.FullProfiles {
		s.log.Warn("access: AWG profile network is full for a Mihomo subscription", "user", u.ID, "profile", profileID, "err", store.ErrAccessSubnetFull)
	}
	if result.Device.ID == "" {
		return rd, ""
	}
	s.placed(u, data, result)
	return requestDevice{dev: result.Device, creds: result.Creds, hash: rd.hash, added: rd.added || len(result.Added) > 0}, ""
}

// deviceTouchEvery is how stale a device's last_seen_at must be before a subscription fetch refreshes it.
const deviceTouchEvery = time.Hour

// touchDevice records a fetch on the device, off the request path and at most once per device at a time. The
// fetch is read-only by design: it is open to anyone holding a link, and a write per request would queue on the
// single writer connection that stats ingestion and the admin share. Only a stale last_seen_at (decided from the
// row the fetch already read) gets here, so a device is written about once an hour; a busy writer delays only
// this goroutine, never the response.
func (s *Service) touchDevice(id string, now time.Time, m deviceMeta) {
	if _, busy := s.touching.LoadOrStore(id, struct{}{}); busy {
		return
	}
	hook := s.touchHookForTest
	if hook != nil {
		hook(true)
	}
	s.afterResponse(func() {
		defer func() {
			s.touching.Delete(id)
			if hook != nil {
				hook(false)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.st.Access().TouchDevice(ctx, id, now, now.Add(-deviceTouchEvery), m.OSVersion, m.Client); err != nil {
			s.log.Warn("access: cannot record subscription fetch", "err", err)
		}
	})
}

// SetTouchHookForTest observes when an asynchronous subscription touch starts and finishes.
func (s *Service) SetTouchHookForTest(hook func(started bool)) { s.touchHookForTest = hook }

// usable is the subscription half of the effective-access rule: the user's group holds the profile, the
// node is selected and live, the inbound is on and not failed, and an enabled app consumes the protocol.
func (s *Service) usable(f store.AccessInboundFull, g store.AccessGroup, u store.AccessUser) bool {
	return liveInbound(f) &&
		slices.Contains(g.ProfileIDs, f.Profile.ID) &&
		(u.AllNodes || slices.Contains(u.NodeIDs, f.Node.ID)) &&
		s.allowed(f.Profile.Protocol, u.AppHapp, u.AppAmnezia)
}

// liveInbound: the inbound is on and not failed, on an enrolled node (the part of usable that is not about the user).
func liveInbound(f store.AccessInboundFull) bool {
	return store.InboundLive(f.Inbound.Enabled, f.Inbound.State, f.Node.State)
}

// accessOf is what the user's page offers by app (User.access_happ / access_amnezia): SubView's AccessHapp and
// AccessAmnezia, without the status.
func (s *Service) accessOf(u store.AccessUser, g store.AccessGroup, full []store.AccessInboundFull) (happ, amnezia bool) {
	for _, f := range full {
		if !s.usable(f, g, u) {
			continue
		}
		if p, ok := s.reg.Get(f.Profile.Protocol); ok {
			h, a, _ := clientApps(p)
			happ = happ || h && u.AppHapp
			amnezia = amnezia || a && u.AppAmnezia
		}
	}
	return happ, amnezia
}
