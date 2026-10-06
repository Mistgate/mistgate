package access

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"

	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// DNS per node for the person (migration 00044, the user page's "DNS for each server"): the rule is in the dns module
// (NodeChoices); this file carries it into what a client is handed and into the page's data.
//
// Where it reaches: the AmneziaWG keys (.conf, vpn://, QR) and the AmneziaWG proxies of a Mihomo profile carry a resolver
// per server, so they get the DNS of their node. Hysteria2 in Mihomo and in Happ have one resolver for the whole
// subscription: they keep the preset that applies to the person without a node (dns.Service.Effective).

// nodeDNS resolves a person's DNS preset per node, for one request.
type nodeDNS struct {
	s       *Service
	ctx     context.Context
	userID  string
	choices dns.NodeChoices
	cache   map[string]dns.Preset
	user    *dns.Preset
}

func (s *Service) newNodeDNS(ctx context.Context, userID string) *nodeDNS {
	c, err := s.dns.NodeChoices(ctx, userID)
	if err != nil {
		// A subscription is never worth failing over this: without the choices every node is on the user's own rule.
		s.log.Warn("access: cannot read the node DNS choices", "err", err)
	}
	return &nodeDNS{s: s, ctx: ctx, userID: userID, choices: c, cache: map[string]dns.Preset{}}
}

// withoutNode is the preset that applies to the person where no node decides: their own, the group's, the instance's.
func (n *nodeDNS) withoutNode() (dns.Preset, bool) {
	if n.user == nil {
		p, _, err := n.s.dns.Effective(n.ctx, n.userID)
		if err != nil {
			n.s.log.Warn("access: cannot read the user's dns preset", "err", err)
			return dns.Preset{}, false
		}
		n.user = &p
	}
	return *n.user, true
}

// on is the preset that applies to the person on a node: the pick the node still offers, else the node's default, else
// withoutNode.
func (n *nodeDNS) on(nodeID string) (dns.Preset, bool) {
	id := n.choices.Effective(nodeID)
	if id == "" {
		return n.withoutNode()
	}
	if p, ok := n.cache[id]; ok {
		return p, true
	}
	p, err := n.s.dns.PresetByID(n.ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			n.s.log.Warn("access: cannot read a dns preset", "err", err)
		}
		return n.withoutNode()
	}
	n.cache[id] = p
	return p, true
}

// awg is what an AWG config of the node carries: the plain IPv4 resolvers of the preset that applies there, and whether the
// preset's split-direct rules are lost (a .conf carries one pair of addresses, no split).
func (n *nodeDNS) awg(nodeID string) (servers []string, splitLost bool) {
	pre, ok := n.on(nodeID)
	if !ok {
		return nil, false
	}
	return awgServers(pre)
}

// awgDNS is what an AWG config of a user carries on a node (see nodeDNS.awg).
func (s *Service) awgDNS(ctx context.Context, userID, nodeID string) (servers []string, splitLost bool) {
	return s.newNodeDNS(ctx, userID).awg(nodeID)
}

// dnsSig is the resolver pair an AWG config carries for these servers (what awg.PickDNS puts in the .conf). A key holds the
// pair it was issued with, per node (device_credential.dns_sig); it is stale on a node when the pair that applies now is another.
func dnsSig(servers []string) string {
	pair, _ := awg.PickDNS(servers)
	return pair[0] + "," + pair[1]
}

// markDNSStale sets DNSStale of AWG devices of the person u: the nodes where the DNS that applies now is not the one the key
// was issued with. Whatever changed it counts (the person's pick or its removal, the owner's offer or default, an edited
// or deleted preset). A key with nothing recorded, or on a node the person no longer uses, is not stale.
func (s *Service) markDNSStale(u store.AccessUser, g store.AccessGroup, full []store.AccessInboundFull, n *nodeDNS, devs []store.AccessAWGDevice) {
	now := map[string]string{} // node id -> the pair that applies to the person now ("" = unknown)
	for i := range devs {
		d := &devs[i]
		d.DNSStale = nil
		for _, f := range full {
			held, ok := d.DNSSig[f.Node.ID]
			if !ok || f.Profile.ID != d.ProfileID || !s.usable(f, g, u) || slices.Contains(d.DNSStale, f.Node.ID) {
				continue
			}
			cur, seen := now[f.Node.ID]
			if !seen {
				if pre, ok := n.on(f.Node.ID); ok {
					servers, _ := awgServers(pre)
					cur = dnsSig(servers)
				}
				now[f.Node.ID] = cur
			}
			if cur != "" && cur != held {
				d.DNSStale = append(d.DNSStale, f.Node.ID)
			}
		}
	}
}

func awgServers(pre dns.Preset) (servers []string, splitLost bool) {
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

// ---- the page's data ----

// SubNodeDNS is what the page says about the DNS of one server; nil on a server that offers no choice.
type SubNodeDNS struct {
	Choice    string   // what the person picked ("" = nothing, or a pick the node no longer offers)
	Effective string   // the preset id that applies on this server
	Options   []string // what the owner offers, in order
	Default   string   // what applies without a pick: the node's default, else the person's usual DNS (rule 3)
	// KeysToRefresh are the AmneziaWG devices of the person with a key on this server that holds a DNS other than the one
	// that applies now.
	KeysToRefresh []string
}

// SubDNSPreset is a preset the page names: the stored name and description, translated by the page (dns.Preset.NameIn).
type SubDNSPreset = dns.Preset

// nodeDNSData fills the DNS of the page's servers and the presets they name. awgs are the AWG devices of the person.
func (s *Service) nodeDNSData(ctx context.Context, v *SubView, n *nodeDNS, awgs []store.AccessAWGDevice) {
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for i := range v.Nodes {
		node := &v.Nodes[i]
		opts := n.choices.Options(node.ID)
		if len(opts) == 0 {
			continue
		}
		eff, ok := n.on(node.ID)
		d := &SubNodeDNS{Choice: n.choices.Pick(node.ID), Options: opts, Default: n.choices.Default(node.ID), KeysToRefresh: []string{}}
		if ok {
			d.Effective = eff.ID
		}
		if d.Default == "" {
			if p, ok := n.withoutNode(); ok {
				d.Default = p.ID
			}
		}
		for _, dev := range awgs {
			if slices.Contains(dev.DNSStale, node.ID) {
				d.KeysToRefresh = append(d.KeysToRefresh, dev.ID)
			}
		}
		node.DNS = d
		for _, id := range opts {
			add(id)
		}
		add(d.Effective)
		add(d.Default)
	}
	if link, ok := n.withoutNode(); ok {
		v.DNSLink = link.ID
		if len(ids) > 0 { // the note over the list names it
			add(link.ID)
		}
	}
	for _, id := range ids {
		if p, ok := n.cache[id]; ok {
			v.DNSPresets = append(v.DNSPresets, p)
		} else if n.user != nil && n.user.ID == id {
			v.DNSPresets = append(v.DNSPresets, *n.user)
		} else if p, err := s.dns.PresetByID(ctx, id); err == nil {
			v.DNSPresets = append(v.DNSPresets, p)
		}
	}
}

// ---- the person's pick ----

// PageDNSChoice is what the page's POST <link>/dns changes: the person's pick on one node. Nothing here reaches the
// nodes (DNS is carried by the client); the next subscription fetch and the next fetch of a key carry it.
type PageDNSChoice struct {
	NodeID   string
	PresetID string // "" = back to the node's default
}

// Error codes of SetPageDNS, the messages of its FailedPrecondition/NotFound errors (the page's codes).
const (
	DNSNotFound   = "not_found"
	DNSNotAllowed = "not_allowed"
)

// SetPageDNS records the pick of a user on a node. NotFound: the node is not one of the person's servers. FailedPrecondition
// "not_allowed": the node does not offer that preset. The audit row names the person and the node the owner gave names to,
// and the preset; never the link and never an address.
func (s *Service) SetPageDNS(ctx context.Context, userID string, c PageDNSChoice) error {
	a := s.st.Access()
	u, err := a.User(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("user")
	} else if err != nil {
		return s.internal("get user", err)
	}
	if ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, u.UsedBytes, s.now()) != StatusActive {
		return failed("user_inactive") // a user without access has no servers
	}
	g, err := a.Group(ctx, u.GroupID)
	if err != nil {
		return s.internal("get group", err)
	}
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return s.internal("inbounds", err)
	}
	var node *store.AccessNode
	for _, f := range full {
		if f.Node.ID == c.NodeID && s.usable(f, g, u) {
			node = &f.Node
			break
		}
	}
	if node == nil {
		return notFound("server")
	}
	if c.PresetID != "" {
		offered := false
		offers, err := s.st.DNS().NodeOptions(ctx)
		if err != nil {
			return s.internal("node options", err)
		}
		for _, o := range offers[c.NodeID] {
			offered = offered || o.PresetID == c.PresetID
		}
		if !offered {
			return failed(DNSNotAllowed)
		}
	}
	err = s.st.DNS().SetUserNodeChoice(ctx, u.ID, c.NodeID, c.PresetID, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return failed(DNSNotAllowed) // the preset was deleted between the check and the write
	case err != nil:
		return s.internal("set dns choice", err)
	}
	params := map[string]any{"user": u.Name, "node": node.Name, "preset": c.PresetID}
	if row, err := s.st.DNS().Get(ctx, c.PresetID); err == nil {
		params["preset_name"] = row.Name
	}
	s.auditNoAddress(ctx, "user:"+u.ID, "page_dns_choice", params)
	return nil
}

// auditNoAddress writes an audit row of the public page without the client address (the page's rows never carry one).
func (s *Service) auditNoAddress(ctx context.Context, by, action string, params map[string]any) {
	b, _ := json.Marshal(params)
	e := store.AuditEntry{Actor: by, Action: action, Params: string(b), Result: "ok"}
	if err := s.st.Audit(ctx, s.now(), e); err != nil {
		s.log.Error("access: audit write failed", "action", action, "err", err)
	}
}
