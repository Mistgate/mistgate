package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/dns"
)

func dnsTools() []toolDef {
	return slices.Concat(
		[]toolDef{
			readTool("dns_presets_list", ProfileReadonly, procs(adminv1connect.DnsServiceListDnsPresetsProcedure),
				"DNS presets with their resolver and split rules, effective user count, default status, provider catalog and client transport support.",
				func(c *call, _ noArgs) (any, error) {
					r, err := c.cl.DNS.ListDnsPresets(c.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
					if err != nil {
						return nil, apiError(err)
					}
					return dnsListView(r.Msg), nil
				}),
			readTool("dns_preset_get", ProfileReadonly, procs(adminv1connect.DnsServiceListDnsPresetsProcedure),
				"One DNS preset by id, including its resolver and split rules, user count and default status.",
				func(c *call, in dnsPresetGetArgs) (any, error) {
					if err := validID("preset_id", in.PresetID); err != nil {
						return nil, err
					}
					r, err := c.cl.DNS.ListDnsPresets(c.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
					if err != nil {
						return nil, apiError(err)
					}
					for _, p := range r.Msg.GetPresets() {
						if p.GetId() == in.PresetID {
							return dnsPresetView(p), nil
						}
					}
					return nil, errors.New("DNS preset not found")
				}),
		},
		change(dnsPresetCreate), change(dnsPresetUpdate), change(dnsPresetDelete),
	)
}

type dnsPresetGetArgs struct {
	PresetID string `json:"preset_id" jsonschema:"DNS preset id from dns_presets_list"`
}

type dnsServerV struct {
	Kind    string `json:"kind,omitempty"`
	Address string `json:"address,omitempty"`
	Variant string `json:"provider_variant,omitempty"`
}
type dnsSplitV struct {
	Suffixes []string     `json:"suffixes,omitempty"`
	Geo      []string     `json:"geo,omitempty"`
	Servers  []dnsServerV `json:"servers"`
}
type dnsPresetV struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Description        string       `json:"description,omitempty"`
	Builtin            bool         `json:"builtin"`
	Servers            []dnsServerV `json:"servers"`
	Split              []dnsSplitV  `json:"split"`
	IPv4Only           bool         `json:"ipv4_only"`
	SplitDirect        bool         `json:"split_direct"`
	PreferredTransport string       `json:"preferred_transport"`
	BlockAds           bool         `json:"block_ads"`
	Category           string       `json:"category,omitempty"`
	Country            string       `json:"country,omitempty"`
	UserCount          uint32       `json:"user_count"`
	IsDefault          bool         `json:"is_default"`
}
type dnsListV struct {
	Presets       []dnsPresetV        `json:"presets"`
	Providers     []dnsProviderV      `json:"providers"`
	ClientSupport []dnsClientSupportV `json:"client_support"`
}
type dnsProviderV struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Variants []dnsProviderVariantV `json:"variants"`
}
type dnsProviderVariantV struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	IPv4     []string `json:"ipv4,omitempty"`
	IPv6     []string `json:"ipv6,omitempty"`
	Plain    bool     `json:"plain"`
	DoTHost  string   `json:"dot_host,omitempty"`
	DoTPort  uint32   `json:"dot_port,omitempty"`
	DoHURL   string   `json:"doh_url,omitempty"`
	Country  string   `json:"country,omitempty"`
}
type dnsClientSupportV struct {
	Client     string   `json:"client"`
	Transports []string `json:"transports"`
	IPv6       bool     `json:"ipv6"`
}

func dnsListView(r *adminv1.ListDnsPresetsResponse) *dnsListV {
	v := &dnsListV{Presets: []dnsPresetV{}, Providers: []dnsProviderV{}, ClientSupport: []dnsClientSupportV{}}
	for _, p := range r.GetPresets() {
		v.Presets = append(v.Presets, dnsPresetView(p))
	}
	for _, p := range r.GetProviders() {
		pv := dnsProviderV{ID: clean(p.GetId(), 60), Name: nm(p.GetName()), Variants: []dnsProviderVariantV{}}
		for _, x := range p.GetVariants() {
			name := clean(x.GetNameEn(), 80)
			if name == "" {
				name = clean(x.GetNameRu(), 80)
			}
			pv.Variants = append(pv.Variants, dnsProviderVariantV{ID: clean(x.GetId(), 80), Name: name, Category: enumName("DNS_CATEGORY_", x.GetCategory().String()),
				IPv4: cleanIDs(x.GetIpv4()), IPv6: cleanIDs(x.GetIpv6()), Plain: x.GetPlain(), DoTHost: clean(x.GetDotHost(), 253), DoTPort: x.GetDotPort(), DoHURL: clean(x.GetDohUrl(), 500), Country: clean(x.GetCountry(), 2)})
		}
		v.Providers = append(v.Providers, pv)
	}
	for _, s := range r.GetClientSupport() {
		cv := dnsClientSupportV{Client: enumName("DNS_CLIENT_", s.GetClient().String()), IPv6: s.GetIpv6(), Transports: []string{}}
		for _, t := range s.GetTransports() {
			cv.Transports = append(cv.Transports, dnsTransportWord(t))
		}
		v.ClientSupport = append(v.ClientSupport, cv)
	}
	return v
}

func dnsPresetView(p *adminv1.DnsPreset) dnsPresetV {
	v := dnsPresetV{ID: clean(p.GetId(), 80), Name: nm(p.GetName()), Description: clean(p.GetDescription(), 512), Builtin: p.GetBuiltin(), IPv4Only: p.GetIpv4Only(),
		SplitDirect: p.GetSplitDirect(), PreferredTransport: dnsTransportWord(p.GetPreferredTransport()), BlockAds: p.GetBlockAds(), Category: enumName("DNS_CATEGORY_", p.GetCategory().String()),
		Country: clean(p.GetCountry(), 2), UserCount: p.GetUserCount(), IsDefault: p.GetIsDefault(), Servers: []dnsServerV{}, Split: []dnsSplitV{}}
	for _, s := range p.GetServers() {
		v.Servers = append(v.Servers, dnsServerView(s))
	}
	for _, rule := range p.GetSplit() {
		x := dnsSplitV{Suffixes: cleanIDs(rule.GetSuffixes()), Geo: cleanIDs(rule.GetGeo()), Servers: []dnsServerV{}}
		for _, s := range rule.GetServers() {
			x.Servers = append(x.Servers, dnsServerView(s))
		}
		v.Split = append(v.Split, x)
	}
	return v
}

func dnsServerView(s *adminv1.DnsServer) dnsServerV {
	kind := enumName("DNS_SERVER_KIND_", s.GetKind().String())
	if kind == "unspecified" {
		kind = ""
	}
	return dnsServerV{Kind: kind, Address: clean(s.GetAddress(), 500), Variant: clean(s.GetProviderVariant(), 100)}
}

func dnsTransportWord(t adminv1.DnsTransport) string {
	if t == adminv1.DnsTransport_DNS_TRANSPORT_UNSPECIFIED {
		return "plain"
	}
	return enumName("DNS_TRANSPORT_", t.String())
}

type dnsServerArg struct {
	Kind    string `json:"kind,omitempty" jsonschema:"plain, dot or doh for a custom address; omit when provider_variant is set"`
	Address string `json:"address,omitempty" jsonschema:"resolver IP, host or HTTPS URL for a custom resolver"`
	Variant string `json:"provider_variant,omitempty" jsonschema:"provider catalog variant id, for example cloudflare/family"`
}
type dnsSplitArg struct {
	Suffixes []string       `json:"suffixes,omitempty"`
	Geo      []string       `json:"geo,omitempty" jsonschema:"country geosite codes such as cn"`
	Servers  []dnsServerArg `json:"servers"`
}
type dnsPresetArgs struct {
	ReasonField
	PresetID           string         `json:"preset_id,omitempty" jsonschema:"required for update; omit for create"`
	Name               string         `json:"name"`
	Description        string         `json:"description,omitempty"`
	Servers            []dnsServerArg `json:"servers"`
	Split              []dnsSplitArg  `json:"split,omitempty"`
	IPv4Only           bool           `json:"ipv4_only,omitempty"`
	SplitDirect        bool           `json:"split_direct,omitempty"`
	PreferredTransport string         `json:"preferred_transport,omitempty" jsonschema:"plain, dot or doh; catalog servers use this when the client supports it"`
	BlockAds           *bool          `json:"block_ads,omitempty"`
	BeforeHash         string         `json:"before_hash,omitempty" jsonschema:"-"`
}

func dnsKindWord(k string) (dns.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "plain":
		return dns.KindPlain, nil
	case "dot":
		return dns.KindDoT, nil
	case "doh":
		return dns.KindDoH, nil
	}
	return "", errors.New("DNS server kind must be plain, dot or doh")
}

func dnsInputServers(in []dnsServerArg) ([]dns.Server, error) {
	if len(in) > dns.MaxServers {
		return nil, fmt.Errorf("at most %d main DNS servers", dns.MaxServers)
	}
	out := make([]dns.Server, 0, len(in))
	for i, s := range in {
		var k dns.Kind
		var err error
		if s.Variant == "" {
			k, err = dnsKindWord(s.Kind)
			if err != nil {
				return nil, fmt.Errorf("servers[%d]: %w", i, err)
			}
		} else if s.Kind != "" || s.Address != "" {
			return nil, fmt.Errorf("servers[%d]: provider_variant cannot be combined with kind or address", i)
		}
		out = append(out, dns.Server{Kind: k, Address: s.Address, Variant: s.Variant})
	}
	return out, nil
}

func dnsInputSplit(in []dnsSplitArg) ([]dns.SplitRule, error) {
	if len(in) > dns.MaxSplitRules {
		return nil, fmt.Errorf("at most %d split rules", dns.MaxSplitRules)
	}
	out := make([]dns.SplitRule, 0, len(in))
	for i, r := range in {
		servers, err := dnsInputServers(r.Servers)
		if err != nil {
			return nil, fmt.Errorf("split[%d]: %w", i, err)
		}
		out = append(out, dns.SplitRule{Suffixes: slices.Clone(r.Suffixes), Geo: slices.Clone(r.Geo), Servers: servers})
	}
	return out, nil
}

func dnsCleanArgs(a dnsPresetArgs) (*adminv1.DnsPreset, error) {
	if err := rejectSecretShaped("name", a.Name); err != nil {
		return nil, err
	}
	if err := rejectSecretShaped("description", a.Description); err != nil {
		return nil, err
	}
	for i, s := range a.Servers {
		if err := rejectSecretShaped(fmt.Sprintf("servers[%d].address", i), s.Address); err != nil {
			return nil, err
		}
		if err := rejectSecretShaped(fmt.Sprintf("servers[%d].provider_variant", i), s.Variant); err != nil {
			return nil, err
		}
	}
	for i, rule := range a.Split {
		for _, text := range append(slices.Clone(rule.Suffixes), rule.Geo...) {
			if err := rejectSecretShaped(fmt.Sprintf("split[%d]", i), text); err != nil {
				return nil, err
			}
		}
		for j, server := range rule.Servers {
			if err := rejectSecretShaped(fmt.Sprintf("split[%d].servers[%d].address", i, j), server.Address); err != nil {
				return nil, err
			}
		}
	}
	servers, err := dnsInputServers(a.Servers)
	if err != nil {
		return nil, err
	}
	split, err := dnsInputSplit(a.Split)
	if err != nil {
		return nil, err
	}
	name, description, servers, split, err := dns.Clean(a.Name, a.Description, servers, split)
	if err != nil {
		return nil, err
	}
	transport, err := dns.NormalizeTransport(dns.Kind(strings.ToLower(strings.TrimSpace(a.PreferredTransport))))
	if err != nil {
		return nil, errors.New("preferred_transport must be plain, dot or doh")
	}
	p := &adminv1.DnsPreset{Name: name, Description: description, Ipv4Only: a.IPv4Only, SplitDirect: a.SplitDirect, Servers: []*adminv1.DnsServer{}, Split: []*adminv1.DnsSplitRule{}}
	for _, s := range servers {
		p.Servers = append(p.Servers, dnsProtoServer(s))
	}
	for _, r := range split {
		x := &adminv1.DnsSplitRule{Suffixes: r.Suffixes, Geo: r.Geo, Servers: []*adminv1.DnsServer{}}
		for _, s := range r.Servers {
			x.Servers = append(x.Servers, dnsProtoServer(s))
		}
		p.Split = append(p.Split, x)
	}
	p.PreferredTransport = map[dns.Kind]adminv1.DnsTransport{dns.KindPlain: adminv1.DnsTransport_DNS_TRANSPORT_PLAIN, dns.KindDoT: adminv1.DnsTransport_DNS_TRANSPORT_DOT, dns.KindDoH: adminv1.DnsTransport_DNS_TRANSPORT_DOH}[transport]
	if a.BlockAds != nil {
		p.BlockAds = *a.BlockAds
	}
	a.Name, a.Description = name, description
	return p, nil
}

func dnsProtoServer(s dns.Server) *adminv1.DnsServer {
	kind := map[dns.Kind]adminv1.DnsServerKind{dns.KindPlain: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, dns.KindDoH: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, dns.KindDoT: adminv1.DnsServerKind_DNS_SERVER_KIND_DOT}[s.Kind]
	return &adminv1.DnsServer{Kind: kind, Address: s.Address, ProviderVariant: s.Variant}
}

func dnsServerArgs(in []*adminv1.DnsServer) []dnsServerArg {
	out := make([]dnsServerArg, 0, len(in))
	for _, s := range in {
		k := enumName("DNS_SERVER_KIND_", s.GetKind().String())
		if k == "unspecified" {
			k = ""
		}
		out = append(out, dnsServerArg{Kind: k, Address: s.GetAddress(), Variant: s.GetProviderVariant()})
	}
	return out
}
func dnsSplitArgs(in []*adminv1.DnsSplitRule) []dnsSplitArg {
	out := make([]dnsSplitArg, 0, len(in))
	for _, r := range in {
		out = append(out, dnsSplitArg{Suffixes: slices.Clone(r.GetSuffixes()), Geo: slices.Clone(r.GetGeo()), Servers: dnsServerArgs(r.GetServers())})
	}
	return out
}

func dnsTransportValue(word string) (adminv1.DnsTransport, error) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "", "plain":
		return adminv1.DnsTransport_DNS_TRANSPORT_PLAIN, nil
	case "dot":
		return adminv1.DnsTransport_DNS_TRANSPORT_DOT, nil
	case "doh":
		return adminv1.DnsTransport_DNS_TRANSPORT_DOH, nil
	}
	return 0, errors.New("preferred_transport must be plain, dot or doh")
}

func dnsPresetHash(p *adminv1.DnsPreset) string {
	return hashText(p.GetId() + "\x00" + p.GetName() + "\x00" + p.GetDescription() + "\x00" + fmt.Sprint(p.GetIpv4Only(), p.GetSplitDirect(), p.GetPreferredTransport(), p.GetBlockAds()) + "\x00" + fmt.Sprint(p.GetServers(), p.GetSplit()))
}

func dnsMutationFacts(name, description string, p *adminv1.DnsPreset) []Fact {
	return []Fact{{Key: "name", Value: nm(name), Untrusted: true}, {Key: "description", Value: clean(description, 512), Untrusted: true},
		codedFact("resolvers", fmt.Sprintf("%d main resolvers and %d split rules", len(p.GetServers()), len(p.GetSplit())), "dns_shape", "servers", strconv.Itoa(len(p.GetServers())), "split", strconv.Itoa(len(p.GetSplit()))),
		codedFact("ipv4_only", strconv.FormatBool(p.GetIpv4Only()), "boolean", "value", strconv.FormatBool(p.GetIpv4Only())),
		codedFact("split_direct", strconv.FormatBool(p.GetSplitDirect()), "boolean", "value", strconv.FormatBool(p.GetSplitDirect())),
		codedFact("preferred_transport", dnsTransportWord(p.GetPreferredTransport()), "transport", "value", dnsTransportWord(p.GetPreferredTransport())),
		codedFact("block_ads", strconv.FormatBool(p.GetBlockAds()), "boolean", "value", strconv.FormatBool(p.GetBlockAds()))}
}

var dnsPresetCreate = changeSpec[dnsPresetArgs]{
	name: "dns_preset_create", min: ProfileAdmin, danger: true, procs: procs(adminv1connect.DnsServiceListDnsPresetsProcedure, adminv1connect.DnsServiceCreateDnsPresetProcedure),
	desc: "Create a custom DNS preset with provider or custom resolvers and optional split rules. This can affect the DNS available to users and waits for owner approval.",
	plan: func(c *call, a dnsPresetArgs) (*planned, error) {
		p, err := dnsCleanArgs(a)
		if err != nil {
			return nil, err
		}
		r, err := c.cl.DNS.ListDnsPresets(c.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		for _, old := range r.Msg.GetPresets() {
			if strings.EqualFold(old.GetName(), p.GetName()) {
				return nil, errors.New("a DNS preset with this name already exists")
			}
		}
		a.Name, a.Description = p.GetName(), p.GetDescription()
		return &planned{Summary: "Create a DNS preset that can be assigned to users and groups. The owner must approve this change.", Facts: dnsMutationFacts(a.Name, a.Description, p), Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a dnsPresetArgs, _ Plan) (done, error) {
		p, err := dnsCleanArgs(a)
		if err != nil {
			return done{}, failure("plan_unreadable", "the stored DNS preset is unreadable: make a new plan")
		}
		r, err := c.cl.DNS.CreateDnsPreset(c.ctx, connect.NewRequest(&adminv1.CreateDnsPresetRequest{Name: p.GetName(), Description: p.GetDescription(), Servers: p.GetServers(), Split: p.GetSplit(),
			Ipv4Only: p.GetIpv4Only(), SplitDirect: p.GetSplitDirect(), PreferredTransport: p.GetPreferredTransport(), BlockAds: p.GetBlockAds()}))
		if err != nil {
			return done{}, apiError(err)
		}
		return doneWith("dns_preset_created", "DNS preset created.", "preset_id", clean(r.Msg.GetPreset().GetId(), 80)), nil
	},
}

var dnsPresetUpdate = changeSpec[dnsPresetArgs]{
	name: "dns_preset_update", min: ProfileAdmin, danger: true, procs: procs(adminv1connect.DnsServiceListDnsPresetsProcedure, adminv1connect.DnsServiceUpdateDnsPresetProcedure),
	desc: "Replace the editable fields of a DNS preset. Built-in presets can be edited but cannot be deleted. The change can affect users assigned to the preset and waits for owner approval.",
	plan: func(c *call, a dnsPresetArgs) (*planned, error) {
		if err := validID("preset_id", a.PresetID); err != nil {
			return nil, err
		}
		old, err := c.dnsPreset(a.PresetID)
		if err != nil {
			return nil, err
		}
		p, err := dnsCleanArgs(a)
		if err != nil {
			return nil, err
		}
		if a.BlockAds == nil {
			p.BlockAds = old.GetBlockAds()
		}
		if dnsPresetHash(old) == dnsPresetHash(&adminv1.DnsPreset{Id: old.GetId(), Name: p.GetName(), Description: p.GetDescription(), Ipv4Only: p.GetIpv4Only(), SplitDirect: p.GetSplitDirect(), PreferredTransport: p.GetPreferredTransport(), BlockAds: p.GetBlockAds(), Servers: p.GetServers(), Split: p.GetSplit()}) {
			return nil, errors.New("nothing to change: this DNS preset already looks like this")
		}
		a.BeforeHash, a.Name, a.Description = dnsPresetHash(old), p.GetName(), p.GetDescription()
		return &planned{Summary: "Update a DNS preset and the settings users receive from it. The owner must approve this change.", Facts: dnsMutationFacts(a.Name, a.Description, p), Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a dnsPresetArgs, _ Plan) (done, error) {
		old, err := c.dnsPreset(a.PresetID)
		if err != nil {
			return done{}, err
		}
		if dnsPresetHash(old) != a.BeforeHash {
			return done{}, failure("changed_since_plan", "the DNS preset changed after the plan; make a new plan")
		}
		p, err := dnsCleanArgs(a)
		if err != nil {
			return done{}, failure("plan_unreadable", "the stored DNS preset is unreadable: make a new plan")
		}
		if a.BlockAds == nil {
			p.BlockAds = old.GetBlockAds()
		}
		if _, err := c.cl.DNS.UpdateDnsPreset(c.ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: a.PresetID, Name: p.GetName(), Description: p.GetDescription(), Servers: p.GetServers(), Split: p.GetSplit(),
			Ipv4Only: p.GetIpv4Only(), SplitDirect: p.GetSplitDirect(), PreferredTransport: p.GetPreferredTransport(), BlockAds: &p.BlockAds})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("dns_preset_updated", "DNS preset updated."), nil
	},
}

type dnsPresetDeleteArgs struct {
	ReasonField
	PresetID   string `json:"preset_id" jsonschema:"custom DNS preset id; built-in or default presets are refused"`
	BeforeHash string `json:"before_hash,omitempty" jsonschema:"-"`
}

var dnsPresetDelete = changeSpec[dnsPresetDeleteArgs]{
	name: "dns_preset_delete", min: ProfileAdmin, danger: true, procs: procs(adminv1connect.DnsServiceListDnsPresetsProcedure, adminv1connect.DnsServiceDeleteDnsPresetProcedure),
	desc: "Delete a custom DNS preset. Built-in and instance-default presets cannot be deleted; assigned users and groups fall back to the instance default. Always waits for owner approval.",
	plan: func(c *call, a dnsPresetDeleteArgs) (*planned, error) {
		p, err := c.dnsPreset(a.PresetID)
		if err != nil {
			return nil, err
		}
		if p.GetBuiltin() {
			return nil, errors.New("built-in DNS presets cannot be deleted")
		}
		if p.GetIsDefault() {
			return nil, errors.New("the instance-default DNS preset cannot be deleted")
		}
		a.BeforeHash = dnsPresetHash(p)
		return &planned{Summary: "Delete a custom DNS preset. Assigned users and groups will fall back to the instance default; the owner must approve this change.", Facts: []Fact{
			{Key: "preset", Value: nm(p.GetName()), Untrusted: true}, codedFact("users", plural(int(p.GetUserCount()), "user will fall back", "users will fall back"), "users", "n", strconv.FormatUint(uint64(p.GetUserCount()), 10)),
		}, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a dnsPresetDeleteArgs, _ Plan) (done, error) {
		p, err := c.dnsPreset(a.PresetID)
		if err != nil {
			return done{}, err
		}
		if dnsPresetHash(p) != a.BeforeHash || p.GetBuiltin() || p.GetIsDefault() {
			return done{}, failure("changed_since_plan", "the DNS preset changed after the plan; make a new plan")
		}
		if _, err := c.cl.DNS.DeleteDnsPreset(c.ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: a.PresetID})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("dns_preset_deleted", "DNS preset deleted."), nil
	},
}

func (c *call) dnsPreset(id string) (*adminv1.DnsPreset, error) {
	if err := validID("preset_id", id); err != nil {
		return nil, err
	}
	r, err := c.cl.DNS.ListDnsPresets(c.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	for _, p := range r.Msg.GetPresets() {
		if p.GetId() == id {
			return p, nil
		}
	}
	return nil, errors.New("DNS preset not found")
}
