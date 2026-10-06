package access

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

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
	// format they are hysteria2:// URIs; for SubOptions.Format = FormatMihomo each is one proxy as an element of a
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
	// DNSLink is the preset that applies to the apps that take the link (Hysteria2 in Mihomo and in Happ have one resolver for the
	// whole subscription): the person's own, the group's, the instance's. DNSPresets are the presets the page names: the ones
	// the nodes offer and apply, and DNSLink when any node offers a choice. Both empty unless Status is active.
	DNSLink    string
	DNSPresets []SubDNSPreset
	// Format is what Lines hold (FormatURIList unless the view was asked for another).
	Format plugin.ClientFormat
}

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
	// LoadPercent is the larger of this node's RX/TX rates as a percentage of its configured symmetric capacity.
	// It is nil when capacity is unknown or the node has no fresh sample. The rates themselves stay out of the view:
	// whatever reaches a user must not tell when the other person on a node streams.
	LoadPercent *int
}

// SubDevice is one device of the user as the public page lists it.
type SubDevice struct {
	ID, Platform, Model string
	App                 string // "happ" | "amnezia" | ""
	LastSeen            time.Time
	Online              bool
	AWG                 *SubAWG // set for an AmneziaWG device
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
	// Format of the lines; "" = FormatURIList. FormatMihomo also gives the implicit device its AWG credentials
	// (one per usable AWG profile, made on the first fetch): a Mihomo client has no device identity, so all of the
	// user's Mihomo clients share them.
	Format plugin.ClientFormat
	// Name is the server name a Mihomo proxy carries; nil = "<node> · <profile>".
	Name func(SubServer) string
	// NoTouch: the view is for the page, not for an app, so it does not mark the implicit device as having fetched the subscription.
	NoTouch bool
	// NoPageData: the view is for an app, so it leaves out what only the user page shows (the DNS of each node, the
	// presets they name, the keys that hold an older DNS) and does not read it.
	NoPageData bool
}

// Subscription resolves a token. The lookup is by sha256(token), so the comparison never sees the token
// itself. For an active user it makes sure the implicit device holds its credentials (created on first
// fetch if missing, e.g. after a revoke) and renders one line per usable inbound.
func (s *Service) Subscription(ctx context.Context, token string) (SubView, error) {
	return s.SubscriptionWith(ctx, token, SubOptions{})
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
// (ErrUnknownToken when there is none), does not record a fetch on the device, and also returns the
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

// subView builds the view of one user; touch records the fetch on the implicit device (real fetches only).
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
	up, _, err := a.UserUsage(ctx, u.ID, u.PeriodStart)
	if err != nil {
		return SubView{}, err
	}
	v.Up = min(up, u.UsedBytes) // used_bytes is authoritative; the buckets only give the split
	v.Down = u.UsedBytes - v.Up
	devs, err := a.Devices(ctx, u.ID)
	if err != nil {
		return SubView{}, err
	}
	awgDevs, err := a.AWGDevices(ctx, u.ID)
	if err != nil {
		return SubView{}, err
	}
	awgByID := map[string]store.AccessAWGDevice{}
	for _, d := range awgDevs {
		awgByID[d.ID] = d
	}
	_, userOnline := s.online.OnlineUsers()[u.ID]
	for _, d := range devs {
		sd := SubDevice{ID: d.ID, Platform: d.Platform, Model: d.Model, LastSeen: d.LastSeenAt, Online: d.Implicit && userOnline}
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
	v.DevicesUsed = len(devs)
	if v.Status != StatusActive {
		return v, nil
	}

	created, err := s.ensureCreds(ctx, u)
	if err != nil {
		return SubView{}, err
	}
	g, err := a.Group(ctx, u.GroupID)
	if err != nil {
		return SubView{}, err
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return SubView{}, err
	}
	if opt.Format == plugin.FormatMihomo {
		if added := s.ensureMihomoAWG(ctx, u, g, full); added {
			created = true
		}
	}
	if created {
		s.notify.StateChanged()
	}
	dev, err := a.ImplicitDevice(ctx, u.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return SubView{}, err
	} // no implicit device: a user of the Amnezia app alone who has fetched nothing that needs one
	var creds []store.AccessCred
	if dev.ID != "" {
		if touch && now.Sub(dev.LastSeenAt) >= deviceTouchEvery {
			s.touchDevice(dev.ID, now)
		}
		if creds, err = a.DeviceCreds(ctx, dev.ID); err != nil {
			return SubView{}, err
		}
	}
	secrets := map[string]string{}
	for _, c := range creds {
		pt, err := s.vault.Open(c.SecretEnc, c.ID)
		if err != nil {
			return SubView{}, err
		}
		secrets[credKey(c.Protocol, c.ProfileID)] = string(pt)
	}

	format := opt.Format
	if format == "" {
		format = plugin.FormatURIList
	}
	v.Format = format
	var networkNodeIDs []string
	var networkSource NetworkUsageSource
	capacityMbps := map[string]int{}
	if source, ok := s.online.(NetworkUsageSource); ok {
		networkSource = source
		for _, f := range full {
			if !s.usable(f, g, u) {
				continue
			}
			if _, exists := s.reg.Get(f.Profile.Protocol); exists {
				networkNodeIDs = append(networkNodeIDs, f.Node.ID)
				capacityMbps[f.Node.ID] = f.Node.BandwidthMbps
			}
		}
	}
	networkUsage := CurrentNetworkUtilization(networkNodeIDs, capacityMbps, networkSource, now)
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
			Profile: f.Profile.Name, Protocol: f.Profile.Protocol, ProfileID: f.Profile.ID, Exit: egressOf(merged)}
		if usage, ok := networkUsage[f.Node.ID]; ok {
			srv.LoadPercent = usage.LoadPercent
		}
		in := protocols.RenderInput{
			Format: format, Settings: merged, Inbound: inboundView(nodeView(f.Node), spec, f.Inbound.CertPinSHA256),
			UserID: u.ID, UserName: u.Name, DeviceID: dev.ID, Secret: secret,
		}
		if protocols.IsPerDevice(proto) {
			// An AmneziaWG proxy carries a resolver of its own: the DNS of its node. (Hysteria2 has none; the URI list has no AWG.)
			var dnsServers []string
			if format == plugin.FormatMihomo {
				dnsServers, _ = dnsOf().awg(f.Node.ID)
			}
			in.Peer, in.InboundPublic, in.DNS, in.NodeAddr = json.RawMessage(secret), json.RawMessage(f.Inbound.PluginPublicJSON), dnsServers, f.Node.Address
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
		v.Nodes[i].Online = s.nodeOnline(v.Nodes[i].ID, networkUsage)
	}
	if opt.NoPageData {
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

// AgentSessionSource is an optional interface of the online source of New (the fleet module implements it): whether the
// agent of a node holds a live session with the panel.
type AgentSessionSource interface {
	AgentConnected(nodeID string) bool
}

// nodeOnline: the agent of the node answers (a live session, or a network sample that is not older than 90 seconds).
func (s *Service) nodeOnline(nodeID string, usage map[string]NodeNetworkUtilization) bool {
	if _, ok := usage[nodeID]; ok {
		return true
	}
	src, ok := s.online.(AgentSessionSource)
	return ok && src.AgentConnected(nodeID)
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

// ensureMihomoAWG gives the implicit device an AWG credential for every AWG profile the user can use, for the
// Mihomo format (see SubOptions). It reports whether any was added. A failure (an exhausted network, say) is
// logged and leaves that user without AWG proxies: the subscription itself must not fail for it.
func (s *Service) ensureMihomoAWG(ctx context.Context, u store.AccessUser, g store.AccessGroup, full []store.AccessInboundFull) bool {
	proto, ok := s.reg.Get(awg.ID)
	if !ok {
		return false
	}
	a := s.st.Access()
	dev := store.AccessDevice{ID: store.NewID("dev_"), UserID: u.ID, Implicit: true}
	if d, err := a.ImplicitDevice(ctx, u.ID); err == nil {
		dev.ID = d.ID
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
		want = append(want, store.AWGImplicitWant{ProfileID: f.Profile.ID, MaxIdx: maxIdx, Issue: s.awgIssuer(proto, u.ID, dev.ID, f.Profile.ID, merged)})
	}
	if len(want) == 0 {
		return false
	}
	n, err := a.EnsureImplicitAWGCreds(ctx, u.ID, dev, s.now(), want)
	if err != nil {
		s.log.Warn("access: cannot issue AWG credentials for a Mihomo subscription", "user", u.ID, "err", err)
		return false
	}
	return n > 0
}

// deviceTouchEvery is how stale a device's last_seen_at must be before a subscription fetch refreshes it.
const deviceTouchEvery = time.Hour

// touchDevice records a fetch on the device, off the request path and at most once per device at a time. The
// fetch is read-only by design: it is open to anyone holding a link, and a write per request would queue on the
// single writer connection that stats ingestion and the admin share. Only a stale last_seen_at (decided from the
// row the fetch already read) gets here, so a device is written about once an hour; a busy writer delays only
// this goroutine, never the response.
func (s *Service) touchDevice(id string, now time.Time) {
	if _, busy := s.touching.LoadOrStore(id, struct{}{}); busy {
		return
	}
	go func() {
		defer s.touching.Delete(id)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.st.Access().TouchDevice(ctx, id, now, now.Add(-deviceTouchEvery)); err != nil {
			s.log.Warn("access: cannot record subscription fetch", "err", err)
		}
	}()
}

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
	return f.Inbound.Enabled && (f.Inbound.State == "pending" || f.Inbound.State == "active") && f.Node.State == "active"
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
