package dns

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
)

// Happ routing profile: what a preset can and cannot become.
//
// Source: Happ developer documentation, https://www.happ.su/main/dev-docs/routing (JSON fields, deep links)
// and https://www.happ.su/main/ru/dev-docs/routing.md (DNS section), read 2026-09-30. A subscription carries
// the profile in the HTTP header `routing: happ://routing/onadd/<base64 of the JSON>` (`onadd` = add and
// activate; `add` activates only after the geo files are downloaded; `off` switches routing off).
//
// What the format has for DNS is exactly two resolvers and nothing else:
//
//   - Remote DNS (RemoteDNSType/Domain/IP): resolves the domains that go through the tunnel, i.e. everything
//     that is not routed direct. Type is "DoH" (needs Domain, an https URL; IP is a bootstrap address) or
//     "DoU" (plain UDP; Domain empty, IP is the server). There is no DoT.
//   - Domestic DNS (DomesticDNSType/Domain/IP): resolves the domains that go direct (DirectSites / DirectIp
//     rules), same types.
//   - DnsHosts: a hosts file ({"name": "ip"}); used here to pin the address of a DoH host name.
//
// Consequences for a preset:
//
//  1. One resolver per role. Happ takes a single Remote and a single Domestic resolver, so of the endpoints
//     Resolve(ClientHapp, ...) returns for a preset's main servers only the first is used; the rest (the
//     fallbacks) are lost. What Happ cannot express never reaches that list: DoT (Resolve drops it, or steps a
//     catalog server down from DoT to plain), a plain server with a port other than 53, a DoH host name whose IP
//     we do not know (the catalog knows its own hosts; an IP-literal host is fine).
//  2. A split by domain suffix is only possible by making those domains go direct. The Domestic resolver is
//     tied to direct traffic: there is no field "send .ru queries to 77.88.8.8 but keep the traffic proxied".
//     So the first split rule becomes Domestic DNS plus DirectSites ("domain:<suffix>", Xray syntax, suffix
//     match at a label boundary), which also means a device connects to those sites without the VPN. Later
//     rules with a different resolver are dropped (only one Domestic resolver exists). This is the preset's
//     SplitDirect switch: only a preset with it on gets the mapping; with it off the split is ignored for
//     Happ and all queries use the main resolver (everything stays inside the tunnel).
//  3. Without a split the Domestic resolver is set to the same server as the Remote one, so that any domain
//     a user's own Happ rules send direct still uses the preset (an ad-blocking preset stays ad-blocking).
//  4. IPv4Only has no counterpart in the profile and is ignored.
//  5. The profile is a whole-device setting applied by Happ when the subscription is refreshed; it does not
//     carry the preset name. Name is constant so that a changed preset overwrites the profile instead of
//     adding another one.
//
// UNVERIFIED (not stated in the docs, taken from Xray conventions and common profiles): the "domain:" prefix
// in DirectSites, that GlobalProxy "true" means "everything through the tunnel except the rules", and that
// Geoipurl/Geositeurl may be omitted. Nothing here uses geosite:/geoip: entries, so no geo files are needed.
// Standard padded base64 is used, as in Happ's own examples. The code was not run against a real Happ client.

// HappProfileName is the Name of the generated routing profile.
const HappProfileName = "DNS"

// happProfile is the routing profile JSON, fields in the order of the docs. Strings "true"/"false" as in the docs.
type happProfile struct {
	Name            string            `json:"Name"`
	GlobalProxy     string            `json:"GlobalProxy"`
	RemoteDNSType   string            `json:"RemoteDNSType"`
	RemoteDNSDomain string            `json:"RemoteDNSDomain"`
	RemoteDNSIP     string            `json:"RemoteDNSIP"`
	DomesticDNSType string            `json:"DomesticDNSType"`
	DomesticDNSDom  string            `json:"DomesticDNSDomain"`
	DomesticDNSIP   string            `json:"DomesticDNSIP"`
	LastUpdated     string            `json:"LastUpdated"`
	DnsHosts        map[string]string `json:"DnsHosts"`
	DirectSites     []string          `json:"DirectSites"`
	DirectIp        []string          `json:"DirectIp"`
	ProxySites      []string          `json:"ProxySites"`
	ProxyIp         []string          `json:"ProxyIp"`
	BlockSites      []string          `json:"BlockSites"`
	BlockIp         []string          `json:"BlockIp"`
	DomainStrategy  string            `json:"DomainStrategy"`
	FakeDNS         string            `json:"FakeDNS"`
}

// happResolver is one Happ DNS role.
type happResolver struct{ typ, domain, ip string }

// happPick takes the first endpoint of a client-ready list (Resolve(ClientHapp, ...)). hosts receives the DnsHosts
// pin of a DoH host name.
func happPick(eps []Endpoint, hosts map[string]string) (happResolver, bool) {
	if len(eps) == 0 {
		return happResolver{}, false
	}
	e := eps[0]
	if e.Kind == KindDoH {
		u, _ := url.Parse(e.Address)
		host := u.Hostname()
		if _, err := netip.ParseAddr(host); err != nil {
			hosts[host] = e.IPs[0]
		}
		return happResolver{"DoH", e.Address, e.IPs[0]}, true
	}
	addr := e.Address
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		addr = ap.Addr().String()
	}
	return happResolver{"DoU", "", addr}, true
}

// happBuild maps a preset to a profile and lists what was lost.
func happBuild(p Preset) (happProfile, []string, error) {
	hosts := map[string]string{}
	eps, notes := p.EndpointsFor(ClientHapp)
	remote, ok := happPick(eps, hosts)
	if !ok {
		return happProfile{}, nil, fmt.Errorf("dns: Happ cannot express any main server of preset %q (it takes plain IP and DoH only)", p.Name)
	}
	if n := len(eps) - 1; n > 0 {
		notes = append(notes, fmt.Sprintf("%d fallback resolver(s) dropped: Happ takes one resolver", n))
	}
	domestic := remote
	var direct []string
	if len(p.Split) > 0 && !p.SplitDirect {
		notes = append(notes, "split ignored (SplitDirect off)")
	}
	if p.SplitDirect {
		used := false
		for i, r := range p.Split {
			seps, _ := p.SplitEndpointsFor(ClientHapp, i)
			dr, dok := happPick(seps, hosts)
			switch {
			case !dok:
				notes = append(notes, fmt.Sprintf("split rule %d dropped: no server Happ can express", i+1))
			case used && dr != domestic:
				notes = append(notes, fmt.Sprintf("split rule %d dropped: Happ has one Domestic resolver", i+1))
			default:
				used, domestic = true, dr
				for _, sfx := range r.Suffixes {
					e := "domain:" + suffixBody(sfx)
					if !slices.Contains(direct, e) {
						direct = append(direct, e)
					}
				}
			}
		}
		if used {
			notes = append(notes, "split domains go direct (outside the tunnel): Happ ties the Domestic resolver to direct traffic")
		}
	}
	if p.IPv4Only {
		notes = append(notes, "ipv4_only ignored: Happ has no such setting")
	}
	if direct == nil {
		direct = []string{}
	}
	return happProfile{
		Name: HappProfileName, GlobalProxy: "true",
		RemoteDNSType: remote.typ, RemoteDNSDomain: remote.domain, RemoteDNSIP: remote.ip,
		DomesticDNSType: domestic.typ, DomesticDNSDom: domestic.domain, DomesticDNSIP: domestic.ip,
		DnsHosts: hosts, DirectSites: direct, DirectIp: []string{}, ProxySites: []string{}, ProxyIp: []string{},
		BlockSites: []string{}, BlockIp: []string{}, DomainStrategy: "IPIfNonMatch", FakeDNS: "false",
	}, notes, nil
}

// happRender returns the value of the `routing` subscription header for a preset, and what of the
// preset Happ could not take.
func happRender(p Preset) (header string, notes []string, err error) {
	prof, notes, err := happBuild(p)
	if err != nil {
		return "", nil, err
	}
	b, err := json.Marshal(prof)
	if err != nil {
		return "", nil, err
	}
	return "happ://routing/onadd/" + base64.StdEncoding.EncodeToString(b), notes, nil
}

// HappRouting returns the value of the `routing` header for a preset; its split becomes direct domains only
// when the preset's SplitDirect is on. An error means Happ cannot express any main server of the preset: the
// caller leaves the header out.
func HappRouting(p Preset) (string, error) {
	h, _, err := happRender(p)
	return h, err
}

// HappRoutingNotes lists, in English, which parts of the preset the Happ profile drops or changes.
func HappRoutingNotes(p Preset) []string {
	_, notes, _ := happRender(p)
	return notes
}
