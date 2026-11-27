package plugin

import (
	"reflect"
	"strings"
	"testing"
)

func subHostSpecs() (vlessTLS, realitySelf, realityOther, trust, mtDecoy, mtOther, hy2, socks InboundSpec) {
	tcp := Listen{Network: "tcp", Port: 443}
	vlessTLS = InboundSpec{ID: "inb_b", Protocol: "vless", Listen: tcp, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "de1.example.com"}, Settings: []byte(`{"security":"tls"}`)}
	realitySelf = InboundSpec{ID: "inb_c", Protocol: "vless", Listen: tcp, Settings: []byte(`{"security":"reality","reality":{"target":"self","server_names":["self.example.com"]}}`)}
	realityOther = InboundSpec{ID: "inb_d", Protocol: "vless", Listen: tcp, Settings: []byte(`{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`)}
	trust = InboundSpec{ID: "inb_e", Protocol: "trusttunnel", Listen: tcp, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "tt.example.com"}, Settings: []byte(`{}`)}
	mtDecoy = InboundSpec{ID: "inb_f", Protocol: "mtproto", Listen: tcp, Settings: []byte(`{"domains":["mt.example.com"]}`)}
	mtOther = InboundSpec{ID: "inb_g", Protocol: "mtproto", Listen: tcp, Settings: []byte(`{"domains":["mt2.example.com"],"fallback":"site"}`)}
	hy2 = InboundSpec{ID: "inb_a", Protocol: "hysteria2", Listen: Listen{Network: "udp", Port: 443}, TLS: TLS{Mode: TLSAcmeDomain, ServerName: "hy.example.com"}, Settings: []byte(`{}`)}
	socks = InboundSpec{ID: "inb_h", Protocol: "socks", Listen: tcp}
	return
}

func TestSubHostRoutes(t *testing.T) {
	vlessTLS, realitySelf, realityOther, trust, mtDecoy, mtOther, hy2, socks := subHostSpecs()
	role := SubHostRole{Names: []string{"Sub.Example.com.", "sub.example.com"}, Path: "/p", SiteName: "de1-site.example.com"}
	subRoute := TCPExtraRoute{Port: 443, Route: TCPRoute{ID: SubHostRouteID, Protocol: "sub-host", Names: []string{"sub.example.com"}}}
	siteRoute := TCPExtraRoute{Port: 443, Route: TCPRoute{ID: SubHostSiteRouteID, Protocol: "sub-host-site", Names: []string{"de1-site.example.com"}, Site: true}}
	fallbackRoute := siteRoute
	fallbackRoute.Route.Names = nil
	inboundSite := SubHostRole{Names: role.Names, Path: role.Path}
	sameName := role
	sameName.SiteName = "de1.example.com"
	for _, tc := range []struct {
		name   string
		role   SubHostRole
		specs  []InboundSpec
		routes []TCPExtraRoute
		unfit  string
	}{
		{"nothing on 443: the agent's own site", role, nil, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"VLESS TLS keeps the explicit site", role, []InboundSpec{vlessTLS}, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"REALITY self keeps the explicit site", role, []InboundSpec{realitySelf}, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"TrustTunnel keeps the explicit site", role, []InboundSpec{trust}, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"the MTProto decoy keeps the explicit site", role, []InboundSpec{mtDecoy}, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"matching inbound site leaves an unnamed fallback", sameName, []InboundSpec{vlessTLS}, []TCPExtraRoute{subRoute, fallbackRoute}, ""},
		{"without a site name the inbound provides the site", inboundSite, []InboundSpec{vlessTLS}, []TCPExtraRoute{subRoute}, ""},
		{"MTProto with another site: the agent's site is the default", role, []InboundSpec{mtOther}, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"a Hysteria2 site gives 443 up to a TCP inbound", role, []InboundSpec{hy2, vlessTLS}, []TCPExtraRoute{subRoute, siteRoute}, ""},
		{"REALITY with someone else's site", role, []InboundSpec{vlessTLS, realityOther}, nil, SubHostRealityForeign},
		{"a Hysteria2 site holds TCP 443", role, []InboundSpec{hy2}, nil, SubHostHysteria2Site},
		{"an inbound that cannot share", role, []InboundSpec{socks}, nil, SubHostPortUnshareable},
		{"an inbound asks for the subscription name", SubHostRole{Names: []string{"de1.example.com"}}, []InboundSpec{vlessTLS}, nil, SubHostNameTaken},
		{"an inbound asks for the site name", SubHostRole{Names: []string{"sub.example.com"}, SiteName: "mt2.example.com"}, []InboundSpec{mtOther}, nil, SubHostNameTaken},
		{"no site and no site name", SubHostRole{Names: []string{"sub.example.com"}}, nil, nil, SubHostSiteNameRequired},
		{"the site name is a subscription name", SubHostRole{Names: []string{"sub.example.com"}, SiteName: "sub.example.com"}, nil, nil, SubHostSiteNameConflict},
		{"no name a hello can carry", SubHostRole{Names: []string{"203.0.113.7", "*.example.com"}, SiteName: "x.example.com"}, nil, nil, SubHostNamesInvalid},
	} {
		routes, unfit := SubHostRoutes(tc.role, tc.specs)
		if unfit != tc.unfit || !reflect.DeepEqual(routes, tc.routes) {
			t.Errorf("%s: routes %+v, unfit %q; want %+v, %q", tc.name, routes, unfit, tc.routes, tc.unfit)
		}
	}
	for _, id := range []string{SubHostRouteID, SubHostSiteRouteID} {
		if !strings.HasPrefix(id, "~") { // inbound ids are "inb_" and base32 (store.NewID): they never hold "~"
			t.Errorf("route id %q could be an inbound's", id)
		}
	}
}

// The host's routes make a front of a port with one inbound. When configured, the agent's site is the default.
func TestTCPSharePlanWithExtraRoutes(t *testing.T) {
	vlessTLS, _, _, _, _, mtOther, _, socks := subHostSpecs()
	role := SubHostRole{Names: []string{"sub.example.com"}, SiteName: "de1-site.example.com"}

	extra, _ := SubHostRoutes(role, []InboundSpec{vlessTLS})
	p := TCPSharePlan([]InboundSpec{vlessTLS}, extra...)[443]
	if !p.Front() || p.Default != SubHostSiteRouteID || len(p.Routes) != 3 || p.Routes[0].ID != SubHostRouteID || len(p.Shadowed) != 0 {
		t.Fatalf("one inbound and the host: %+v", p)
	}

	extra, _ = SubHostRoutes(role, nil)
	p = TCPSharePlan(nil, extra...)[443]
	if !p.Front() || p.Default != SubHostSiteRouteID {
		t.Fatalf("the host alone: %+v", p)
	}

	extra, _ = SubHostRoutes(role, []InboundSpec{mtOther})
	p = TCPSharePlan([]InboundSpec{mtOther}, extra...)[443]
	if !p.Front() || p.Default != SubHostSiteRouteID {
		t.Fatalf("MTProto with another site: %+v", p)
	}

	// an inbound that takes the subscription name (were the panel to let it) is shadowed, the host keeps the name
	taker := vlessTLS
	taker.TLS.ServerName = "sub.example.com"
	extra = []TCPExtraRoute{{Port: 443, Route: TCPRoute{ID: SubHostRouteID, Protocol: "sub-host", Names: []string{"sub.example.com"}}}}
	p = TCPSharePlan([]InboundSpec{taker, mtOther}, extra...)[443]
	if p.Shadowed["inb_b"] != (TCPShadow{Name: "sub.example.com", By: SubHostRouteID}) {
		t.Fatalf("shadowed %+v", p.Shadowed)
	}

	if _, ok := TCPSharePlan([]InboundSpec{socks}, extra...)[443]; ok {
		t.Fatal("a port with an inbound that cannot share is never planned")
	}
	if got, want := TCPSharePlan([]InboundSpec{vlessTLS}), map[uint16]TCPPortShare{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("without extras nothing changes: %+v", got)
	}
}
