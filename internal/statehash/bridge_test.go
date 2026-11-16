package statehash

import (
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

func bridgeServe() *plugin.BridgeServe {
	return &plugin.BridgeServe{Port: 8443, Path: "/assets/x1", ExitNodeIDs: []string{"nod_nl1", "nod_de1"}}
}

func bridgeDials() []plugin.BridgeDial {
	return []plugin.BridgeDial{
		{EntryNodeID: "nod_msk2", Host: "192.0.2.21", Port: 443, SNI: "msk2.example.com", Path: "/a/b", Pool: 2,
			Targets: []plugin.BridgeTarget{{RelayID: "rly_b", Network: "tcp", Port: 8443}, {RelayID: "rly_a", Network: "tcp", Port: 443}}},
		{EntryNodeID: "nod_msk1", Host: "192.0.2.20", Port: 443, PinSHA256: "aa", Path: "/c/d", PanelVia: true,
			Targets: []plugin.BridgeTarget{{RelayID: "rly_c", Network: "tcp", Port: 8443}}},
	}
}

// A node without a bridge hashes exactly as before the bridge existed: a nil or an empty BridgeServe, no dials or an empty list,
// a plain forward and a forward whose mode is "dnat" all leave every earlier hash alone.
func TestBridgeLinesOnlyWhenPresent(t *testing.T) {
	ins := []Inbound{goldenInbound(), awgInbound()}
	w := plugin.WarpSpec{Enabled: true, PrivateKey: "k", PeerPublicKey: "p", EndpointV4: "203.0.113.10", Ports: []uint16{2408}, AddressV4: "172.16.0.2/32"}
	rl := []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443}}
	if StateBridge([]Inbound{goldenInbound()}, nil, nil, nil, nil) != goldenState ||
		StateBridge([]Inbound{goldenInbound()}, nil, nil, &plugin.BridgeServe{}, []plugin.BridgeDial{}) != goldenState {
		t.Fatal("no bridge must not change the golden state hash")
	}
	if StateBridge(nil, nil, nil, nil, nil) != goldenEmpty || StateBridge(nil, nil, nil, &plugin.BridgeServe{}, nil) != goldenEmpty {
		t.Fatal("empty state")
	}
	if StateBridge(ins, &w, rl, nil, nil) != StateRelays(ins, &w, rl) || StateBridge(ins, &w, rl, &plugin.BridgeServe{}, nil) != StateRelays(ins, &w, rl) {
		t.Fatal("no bridge: StateBridge is StateRelays")
	}
	// The golden relay hash of a plain forward stands.
	const want = "5396698f4c1454918c7061b761e59a4aa1a6d1204b5aeb70e6378c50ea6d9157"
	if StateBridge([]Inbound{goldenInbound()}, nil, rl, nil, nil) != want {
		t.Fatal("a plain forward changed the golden relay hash")
	}
	// "dnat" is the plain forward; the same hash whichever way a panel or an agent spells it.
	dn := []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443, Mode: "dnat"}}
	if StateRelays(ins, &w, dn) != StateRelays(ins, &w, rl) {
		t.Fatal(`a mode of "dnat" must hash like a plain forward`)
	}
	if got, want := Relay(rl[0]), "relay\x1frly_a\x1ftcp\x1f8443\x1f203.0.113.7\x1f443"; got != want {
		t.Fatalf("plain relay line = %q", got)
	}
}

func TestReverseForwardLine(t *testing.T) {
	r := plugin.RelayForward{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443, Mode: "reverse", ExitNodeID: "nod_de1"}
	if got, want := Relay(r), "relay\x1frly_a\x1ftcp\x1f8443\x1f203.0.113.7\x1f443\x1freverse\x1fnod_de1"; got != want {
		t.Fatalf("reverse relay line = %q, want %q", got, want)
	}
	plain := r
	plain.Mode, plain.ExitNodeID = "", ""
	ins := []Inbound{goldenInbound()}
	with, without := StateRelays(ins, nil, []plugin.RelayForward{r}), StateRelays(ins, nil, []plugin.RelayForward{plain})
	if with == without {
		t.Fatal("a reverse forward must change the hash")
	}
	for name, mut := range map[string]func(*plugin.RelayForward){
		"mode": func(f *plugin.RelayForward) { f.Mode = "" },
		"exit": func(f *plugin.RelayForward) { f.ExitNodeID = "nod_nl1" },
	} {
		c := r
		mut(&c)
		if StateRelays(ins, nil, []plugin.RelayForward{c}) == with {
			t.Errorf("%s change does not move the hash", name)
		}
	}
}

func TestBridgeLinesCountAndSort(t *testing.T) {
	ins := []Inbound{goldenInbound()}
	rl := []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443}}
	base := StateRelays(ins, nil, rl)
	both := StateBridge(ins, nil, rl, bridgeServe(), bridgeDials())
	onlyServe := StateBridge(ins, nil, rl, bridgeServe(), nil)
	onlyDials := StateBridge(ins, nil, rl, nil, bridgeDials())
	if both == base || onlyServe == base || onlyDials == base || both == onlyServe || both == onlyDials || onlyServe == onlyDials {
		t.Fatal("serve and dials must each change the hash")
	}
	// Order does not matter: the exits of the serve, the dials, the targets of a dial.
	serve := bridgeServe()
	serve.ExitNodeIDs = []string{"nod_de1", "nod_nl1"}
	dials := bridgeDials()
	dials[0], dials[1] = dials[1], dials[0]
	dials[1].Targets[0], dials[1].Targets[1] = dials[1].Targets[1], dials[1].Targets[0]
	if StateBridge(ins, nil, rl, serve, dials) != both {
		t.Fatal("the order of exits, dials and targets must not matter")
	}
	// Every field counts.
	for name, mut := range map[string]func(*plugin.BridgeServe){
		"port":     func(s *plugin.BridgeServe) { s.Port = 443 },
		"path":     func(s *plugin.BridgeServe) { s.Path = "/assets/x2" },
		"exits":    func(s *plugin.BridgeServe) { s.ExitNodeIDs = s.ExitNodeIDs[:1] },
		"panelvia": func(s *plugin.BridgeServe) { s.PanelVia = true },
	} {
		s := *bridgeServe()
		s.ExitNodeIDs = append([]string(nil), s.ExitNodeIDs...)
		mut(&s)
		if StateBridge(ins, nil, rl, &s, bridgeDials()) == both {
			t.Errorf("serve %s does not move the hash", name)
		}
	}
	for name, mut := range map[string]func(*plugin.BridgeDial){
		"entry":    func(d *plugin.BridgeDial) { d.EntryNodeID = "nod_msk9" },
		"host":     func(d *plugin.BridgeDial) { d.Host = "192.0.2.99" },
		"port":     func(d *plugin.BridgeDial) { d.Port = 8443 },
		"sni":      func(d *plugin.BridgeDial) { d.SNI = "other.example.com" },
		"pin":      func(d *plugin.BridgeDial) { d.PinSHA256 = "bb" },
		"path":     func(d *plugin.BridgeDial) { d.Path = "/z" },
		"pool":     func(d *plugin.BridgeDial) { d.Pool = 4 },
		"panelvia": func(d *plugin.BridgeDial) { d.PanelVia = !d.PanelVia },
		"target":   func(d *plugin.BridgeDial) { d.Targets = d.Targets[:1] },
		"network": func(d *plugin.BridgeDial) {
			d.Targets = []plugin.BridgeTarget{{RelayID: "rly_b", Network: "udp", Port: 8443}, d.Targets[1]}
		},
	} {
		d := bridgeDials()
		d[0].Targets = append([]plugin.BridgeTarget(nil), d[0].Targets...)
		mut(&d[0])
		if StateBridge(ins, nil, rl, bridgeServe(), d) == both {
			t.Errorf("dial %s does not move the hash", name)
		}
	}
	// The documented lines.
	if got, want := BridgeServe(*bridgeServe()), "bridge_serve\x1f8443\x1f/assets/x1\x1fnod_de1,nod_nl1\x1f0"; got != want {
		t.Errorf("BridgeServe = %q, want %q", got, want)
	}
	if got, want := BridgeDial(bridgeDials()[0]), "bridge_dial\x1fnod_msk2\x1f192.0.2.21\x1f443\x1fmsk2.example.com\x1f\x1f/a/b\x1f2\x1f0\x1frly_a:tcp:443,rly_b:tcp:8443"; got != want {
		t.Errorf("BridgeDial = %q, want %q", got, want)
	}
	// On an empty node a bridge differs from nothing, and the bridge lines follow the relay lines.
	if StateBridge(nil, nil, nil, bridgeServe(), nil) == goldenEmpty {
		t.Fatal("a bridge on an empty node differs from an empty node")
	}
}

// The hash of one golden inbound, one forward and a bridge, fixed here so the panel and the agent cannot drift apart on the lines.
func TestGoldenBridgeState(t *testing.T) {
	r := plugin.RelayForward{ID: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443, Mode: "reverse", ExitNodeID: "nod_de1"}
	const want = "1380dbb01dbe5b459c1c34818fb95578878cd9079c57daf4284d8304f6a8062e"
	if got := StateBridge([]Inbound{goldenInbound()}, nil, []plugin.RelayForward{r}, bridgeServe(), bridgeDials()); got != want {
		t.Errorf("StateBridge = %s", got)
	}
}
