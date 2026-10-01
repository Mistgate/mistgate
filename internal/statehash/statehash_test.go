package statehash

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

func goldenInbound() Inbound {
	return Inbound{
		Spec: plugin.InboundSpec{
			ID: "inb_a", Protocol: "hysteria2", ProfileID: "prf_a", Version: 3, Enabled: true,
			Listen:   plugin.Listen{Network: "udp", Port: 443, HopFrom: 20000, HopTo: 30000},
			TLS:      plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "example.com"},
			Egress:   "direct",
			Settings: json.RawMessage(`{"obfs":{"type":"salamander","password":"x"}}`),
		},
		Creds: []plugin.UserCred{
			{CredID: "crd_b", Data: json.RawMessage(`{"auth_sha256":"bb"}`), RateLimitBps: 7, ValidUntil: time.Unix(2000000000, 0)},
			{CredID: "crd_a", Data: json.RawMessage(`{"auth_sha256":"aa"}`)},
		},
	}
}

// Golden values taken from the implementation before L3 (Tunnel / WarpSpec) support was added: they must never
// change, or every deployed node would report state_drift after an upgrade.
const (
	goldenSpec  = "559c5af4f1114c8bf7a76c3b06d00e87e1419aef5e4e99f6c40386f90a2bab39"
	goldenCreds = "82cb732504bffc502b863e2b7d5cfeae16617c75478ab26845796a73430c77dd"
	goldenState = "7e6ad85e22fb7274ab2c92000fbeb325b277673e4e61559894cf4e2ec2f01a0b"
	goldenEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func TestGoldenHysteria2Unchanged(t *testing.T) {
	in := goldenInbound()
	if got := Spec(in.Spec); got != goldenSpec {
		t.Errorf("Spec = %s", got)
	}
	if got := Creds(in.Creds); got != goldenCreds {
		t.Errorf("Creds = %s", got)
	}
	if got := State([]Inbound{in}); got != goldenState {
		t.Errorf("State = %s", got)
	}
	if got := State(nil); got != goldenEmpty {
		t.Errorf("State(nil) = %s", got)
	}
}

func awgInbound() Inbound {
	in := goldenInbound()
	in.Spec.ID, in.Spec.Protocol = "inb_w", "awg"
	in.Spec.Tunnel = plugin.Tunnel{
		AddrV4: netip.MustParsePrefix("10.66.4.1/22"), AddrV6: netip.MustParsePrefix("fd66:66:0:1::1/64"), MTU: 1280,
	}
	return in
}

func TestTunnelChangesSpecHashOnlyWhenSet(t *testing.T) {
	base := goldenInbound().Spec
	if got := Spec(base); got != goldenSpec {
		t.Fatalf("no tunnel: %s", got)
	}
	a := awgInbound().Spec
	h := Spec(a)
	if h == Spec(base) || h == "" {
		t.Fatal("a tunnel must change the hash")
	}
	for name, mut := range map[string]func(*plugin.InboundSpec){
		"v4":  func(s *plugin.InboundSpec) { s.Tunnel.AddrV4 = netip.MustParsePrefix("10.66.8.1/22") },
		"v6":  func(s *plugin.InboundSpec) { s.Tunnel.AddrV6 = netip.Prefix{} },
		"mtu": func(s *plugin.InboundSpec) { s.Tunnel.MTU = 1420 },
	} {
		b := a
		mut(&b)
		if Spec(b) == h {
			t.Errorf("%s change does not move the hash", name)
		}
	}
}

func TestWarpLineOnlyWhenPresent(t *testing.T) {
	ins := []Inbound{goldenInbound(), awgInbound()}
	if StateWarp(ins, nil) != State(ins) {
		t.Fatal("nil WarpSpec must not change the state hash")
	}
	if StateWarp(nil, nil) != goldenEmpty {
		t.Fatal("empty state")
	}
	w := plugin.WarpSpec{
		Enabled: true, PrivateKey: "k", PeerPublicKey: "p", EndpointV4: "203.0.113.10", Ports: []uint16{2408, 500},
		AddressV4: "172.16.0.2/32", MTU: 1280, Reserved: []byte{1, 2, 3}, Backend: "auto",
	}
	with := StateWarp(ins, &w)
	if with == State(ins) {
		t.Fatal("a WarpSpec must change the state hash")
	}
	paused := w
	paused.Enabled = false
	if StateWarp(ins, &paused) == with {
		t.Fatal("pausing must change the state hash")
	}
	res := w
	res.Reserved = nil
	if StateWarp(ins, &res) == with {
		t.Fatal("reserved bytes must count")
	}
	if StateWarp(nil, &w) == goldenEmpty {
		t.Fatal("warp on an empty node differs from an empty node")
	}
}
