package warp

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

func TestParseSpec(t *testing.T) {
	ok, err := parseSpec(spec())
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.cands) != 3 || ok.cands[1] != netip.MustParseAddrPort("198.51.100.7:500") || ok.mtu != 1280 || ok.backend != "auto" || ok.hasV6() {
		t.Fatalf("%+v", ok)
	}
	// defaults: ports, mtu, host address without a prefix
	d := spec()
	d.Ports, d.MTU, d.AddressV4 = nil, 0, "172.16.0.2"
	p, err := parseSpec(d)
	if err != nil || len(p.cands) != 4 || p.cands[3].Port() != 4500 || p.mtu != 1280 || p.addrV4.Bits() != 32 {
		t.Fatalf("%+v %v", p, err)
	}

	bad := map[string]func(*plugin.WarpSpec){
		"private key":       func(s *plugin.WarpSpec) { s.PrivateKey = "not a key" },
		"peer key":          func(s *plugin.WarpSpec) { s.PeerPublicKey = key1[:20] },
		"hostname endpoint": func(s *plugin.WarpSpec) { s.EndpointV4 = "engage.example.com" },
		"v6 in v4 slot":     func(s *plugin.WarpSpec) { s.EndpointV4 = "2001:db8::1" },
		"v4 in v6 slot":     func(s *plugin.WarpSpec) { s.EndpointV6 = "198.51.100.1" },
		"no endpoint":       func(s *plugin.WarpSpec) { s.EndpointV4 = "" },
		"unspecified ep":    func(s *plugin.WarpSpec) { s.EndpointV4 = "0.0.0.0" },
		"port 0":            func(s *plugin.WarpSpec) { s.Ports = []uint16{0} },
		"no address":        func(s *plugin.WarpSpec) { s.AddressV4 = "" },
		"v6 address in v4":  func(s *plugin.WarpSpec) { s.AddressV4 = "2001:db8::2/128" },
		"bad v6":            func(s *plugin.WarpSpec) { s.AddressV6 = "172.16.0.3/32" },
		"small mtu":         func(s *plugin.WarpSpec) { s.MTU = 100 },
		"v6 needs 1280":     func(s *plugin.WarpSpec) { s.AddressV6, s.MTU = "2001:db8::2/128", 1200 },
		"reserved length":   func(s *plugin.WarpSpec) { s.Reserved = []byte{1, 2} },
		"backend":           func(s *plugin.WarpSpec) { s.Backend = "masque" },
	}
	for name, mut := range bad {
		s := spec()
		mut(s)
		_, err := parseSpec(s)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), key1) || strings.Contains(err.Error(), key2) {
			t.Errorf("%s: error leaks a key: %v", name, err)
		}
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := (Settings{}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Settings{
		"long iface":  {Iface: "mgwarp-very-long-name"},
		"bad iface":   {Iface: "mg warp"},
		"main table":  {Table: 254},
		"same prefs":  {OifPref: 100, SubnetPref: 100},
		"pref 0 edge": {OifPref: 40000},
		"nft name":    {NftTable: "a b"},
	} {
		if s.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRenderNft(t *testing.T) {
	got := renderNft("mistgate_warp", "mgwarp", nil, netip.AddrPort{})
	want := `add table inet mistgate_warp
delete table inet mistgate_warp
table inet mistgate_warp {
	chain post {
		type nat hook postrouting priority srcnat; policy accept;
		oifname "mgwarp" masquerade
	}
	chain clamp {
		type filter hook forward priority mangle; policy accept;
		oifname "mgwarp" tcp flags syn tcp option maxseg size set rt mtu
	}
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	rsv := renderNft("mistgate_warp", "mgwarp", []byte{0x0a, 0x0b, 0x0c}, netip.MustParseAddrPort("198.51.100.7:2408"))
	if !strings.Contains(rsv, "chain rsv {") || !strings.Contains(rsv, "ip daddr 198.51.100.7 udp dport 2408 @th,64,8 >= 1 @th,64,8 <= 4 @th,72,24 set 0x0a0b0c") {
		t.Fatalf("%s", rsv)
	}
	v6 := renderNft("t", "mgwarp", []byte{1, 2, 3}, netip.MustParseAddrPort("[2001:db8::1]:500"))
	if !strings.Contains(v6, "ip6 daddr 2001:db8::1 udp dport 500") || !strings.Contains(v6, "set 0x010203") {
		t.Fatalf("%s", v6)
	}
	if strings.Contains(renderNft("t", "mgwarp", []byte{1, 2}, netip.MustParseAddrPort("198.51.100.7:1")), "rsv") {
		t.Fatal("a reserved value of the wrong length must not produce a rule")
	}
}

func TestParseTrace(t *testing.T) {
	w, c := parseTrace("fl=1\nip=198.51.100.9\ncolo=FRA\nwarp=plus\nloc=DE\n")
	if w != "plus" || c != "FRA" {
		t.Fatalf("%q %q", w, c)
	}
	if w, _ := parseTrace("garbage"); w != "" {
		t.Fatal(w)
	}
}
