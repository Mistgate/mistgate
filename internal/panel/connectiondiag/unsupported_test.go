package connectiondiag

import (
	"slices"
	"testing"
)

func TestUnsupportedCounters(t *testing.T) {
	for protocol, want := range map[string][]string{
		"awg": {"rejects", "drops", "hellos", "handshakes", "client_closes", "freezes", "quiet", "accepted", "hellos_foreign",
			"rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"},
		"vless": {"connects", "rejects", "drops", "stalls", "unknown_keys", "hellos", "handshakes", "keys_stalled", "keys_tried",
			"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"},
		"hysteria2": {"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"},
	} {
		want = append(want, Link3Fields...) // an agent without links/3
		if got := UnsupportedCounters(protocol); !slices.Equal(got, want) {
			t.Errorf("%s unsupported = %v, want %v", protocol, got, want)
		}
	}
	if got := UnsupportedCounters("VLESS"); !slices.Equal(got, UnsupportedCounters("vless")) {
		t.Errorf("protocol name case changed its unsupported counters: %v", got)
	}
	if got := UnsupportedCountersForNode("hysteria2", []string{"links/1", "links/2"}); !slices.Equal(got, Link3Fields) {
		t.Errorf("Hysteria2 links/2 unsupported = %v, want only the links/3 counters", got)
	}
}

func TestVLESSUnknownKeysCapability(t *testing.T) {
	legacy := UnsupportedCountersForNode("vless", nil)
	if !slices.Contains(legacy, "unknown_keys") {
		t.Fatalf("legacy VLESS unsupported = %v, want unknown_keys", legacy)
	}

	current := UnsupportedCountersForNode("VLESS", []string{VLESSUnknownKeysCapability})
	if slices.Contains(current, "unknown_keys") {
		t.Fatalf("VLESS agent with %s still marks unknown_keys unsupported: %v", VLESSUnknownKeysCapability, current)
	}
	if len(current) != len(legacy)-1 {
		t.Fatalf("VLESS unsupported with capability = %v, want only unknown_keys removed from %v", current, legacy)
	}
	if !slices.Contains(UnsupportedCountersForNode("hysteria2", []string{VLESSUnknownKeysCapability}), "client_closes") {
		t.Fatal("VLESS capability changed Hysteria2's unsupported counters")
	}
}

func TestTrustTunnelCounters(t *testing.T) {
	legacy := UnsupportedCountersForNode("trusttunnel", nil)
	if !slices.Contains(legacy, "auth_failed") || slices.Contains(legacy, "unknown_keys") {
		t.Fatalf("legacy TrustTunnel unsupported = %v, want auth_failed unsupported and unknown_keys supported", legacy)
	}
	current := UnsupportedCountersForNode("trusttunnel", []string{TrustTunnelCapability})
	if slices.Contains(current, "auth_failed") || slices.Contains(current, "unknown_keys") {
		t.Fatalf("TrustTunnel with %s unsupported = %v", TrustTunnelCapability, current)
	}
	if len(current) != len(legacy)-1 {
		t.Fatalf("TrustTunnel unsupported with capability = %v, want only auth_failed removed from %v", current, legacy)
	}
	for _, f := range []string{"rejects", "drops", "stalls", "hellos", "handshakes", "hs_unfinished", "tcp_stalls"} {
		if !slices.Contains(current, f) {
			t.Errorf("TrustTunnel does not report %s, yet it is not listed as unsupported: %v", f, current)
		}
	}
	for _, f := range []string{"connects", "unknown_keys", "auth_failed"} {
		if slices.Contains(current, f) {
			t.Errorf("TrustTunnel reports %s, yet it is listed as unsupported: %v", f, current)
		}
	}
	if !slices.Contains(UnsupportedCountersForNode("trusttunnel", []string{Links3Capability}), "auth_failed") {
		t.Fatal("links/3 alone incorrectly made TrustTunnel auth_failed supported")
	}
}

// trusttunnel-tcp-stalls/1: the TCP stalls are the inbound's own, on the agents that count them; no other protocol gains them.
func TestTrustTunnelTCPStallCounters(t *testing.T) {
	without := UnsupportedCountersForNode("trusttunnel", []string{TrustTunnelCapability, Links3Capability})
	with := UnsupportedCountersForNode("trusttunnel", []string{TrustTunnelCapability, Links3Capability, TrustTunnelTCPStallsCapability})
	for _, f := range []string{"tcp_stalls", "tcp_stalls_16k"} {
		if !slices.Contains(without, f) || slices.Contains(with, f) {
			t.Errorf("%s: unsupported without the capability %v, with it %v", f, slices.Contains(without, f), slices.Contains(with, f))
		}
	}
	if len(with) != len(without)-2 {
		t.Fatalf("the stall capability changed other counters: %v vs %v", with, without)
	}
	// never on a key's row: a stall is told apart per connection, and a connection is not a key's
	keys := UnsupportedKeyCounters("trusttunnel", []string{TrustTunnelCapability, Links3Capability, TrustTunnelTCPStallsCapability}, Variant{})
	if !slices.Contains(keys, "tcp_stalls") {
		t.Fatalf("a TrustTunnel key row claims TCP stalls: %v", keys)
	}
	for _, protocol := range []string{"hysteria2", "awg"} {
		if !slices.Contains(UnsupportedCountersForNode(protocol, []string{Links3Capability, TrustTunnelTCPStallsCapability}), "tcp_stalls") {
			t.Errorf("%s gained tcp_stalls from the TrustTunnel capability", protocol)
		}
	}
}

// trusttunnel-refusals/1: the refusal reasons are TrustTunnel's, on the agents that count them; nobody else lists them.
func TestTrustTunnelRefusalCounters(t *testing.T) {
	without := UnsupportedCountersForNode("trusttunnel", []string{TrustTunnelCapability, Links3Capability})
	with := UnsupportedCountersForNode("trusttunnel", []string{TrustTunnelCapability, TrustTunnelRefusalsCapability})
	for _, f := range TunnelRefusalFields {
		if !slices.Contains(without, f) || slices.Contains(with, f) {
			t.Errorf("%s: unsupported without the capability %v, with it %v", f, slices.Contains(without, f), slices.Contains(with, f))
		}
	}
	if len(TunnelRefusalFields) != 14 || len(with) != len(without)-14 {
		t.Fatalf("the refusal capability changed other counters: %v vs %v", with, without)
	}
	for _, protocol := range []string{"hysteria2", "vless", "awg"} {
		all := UnsupportedCountersForNode(protocol, []string{TrustTunnelRefusalsCapability})
		for _, f := range TunnelRefusalFields {
			if slices.Contains(all, f) {
				t.Errorf("%s lists %s, which only TrustTunnel has", protocol, f)
			}
		}
	}
}

// links/3: each engine's reasons only where it sees them; an old agent, an unknown backend or an unknown transport is "not known".
func TestLinks3Unsupported(t *testing.T) {
	caps := []string{"links/1", "links/2", Links3Capability, VLESSUnknownKeysCapability}
	supported := func(protocol string, v Variant) []string {
		var out []string
		all := UnsupportedCountersFor(protocol, caps, v)
		for _, f := range append([]string{"connects", "hellos", "hellos_foreign"}, Link3Fields...) {
			if !slices.Contains(all, f) {
				out = append(out, f)
			}
		}
		return out
	}
	for _, c := range []struct {
		name, protocol string
		v              Variant
		want           []string
	}{
		{"hysteria2", "hysteria2", Variant{}, []string{"connects", "hellos", "hellos_foreign", "hs_unfinished", "tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused"}},
		{"awg userspace", "awg", Variant{AwgBackend: "userspace"}, []string{"connects", "initiations", "responses", "responses_unmatched"}},
		{"awg kernel", "awg", Variant{AwgBackend: "kernel"}, []string{"connects"}},
		{"awg without a report", "awg", Variant{}, []string{"connects"}},
		{"vless reality tcp", "vless", VLESSVariant(`{"security":"reality","transport":"tcp"}`),
			[]string{"connects", "hellos", "hellos_foreign", "hs_unfinished", "reality_fallback", "auth_failed", "tcp_stalls", "tcp_stalls_16k"}},
		{"vless reality xhttp", "vless", VLESSVariant(`{"security":"reality","transport":"xhttp"}`),
			[]string{"hellos", "hellos_foreign", "hs_unfinished", "reality_fallback", "auth_failed"}},
		{"vless tls direct", "vless", VLESSVariant(`{"security":"tls","transport":"ws"}`),
			[]string{"hellos", "hellos_foreign", "hs_unfinished", "tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused", "tcp_stalls", "tcp_stalls_16k"}},
		{"vless tls behind a CDN", "vless", VLESSVariant(`{"security":"tls","transport":"xhttp","cdn":{"host":"cdn.example.com"}}`),
			[]string{"hellos", "hellos_foreign", "hs_unfinished", "tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused"}},
		{"vless unreadable settings", "vless", VLESSVariant(`not json`), []string{"hellos", "hellos_foreign"}},
	} {
		if got := supported(c.protocol, c.v); !slices.Equal(got, c.want) {
			t.Errorf("%s: supported %v, want %v", c.name, got, c.want)
		}
	}
	// an agent without links/3 has none of them, whatever the variant
	old := UnsupportedCountersFor("vless", []string{"links/1", "links/2"}, VLESSVariant(`{"security":"reality","transport":"tcp"}`))
	for _, f := range append([]string{"connects", "hellos"}, Link3Fields...) {
		if !slices.Contains(old, f) {
			t.Errorf("an agent without links/3 supports %s", f)
		}
	}
	// a key's row: the stall is on the key only over REALITY TCP; AWG handshakes only on userspace
	for _, c := range []struct {
		protocol string
		v        Variant
		known    []string
	}{
		{"vless", Variant{Security: "reality", Transport: "tcp"}, []string{"tcp_stalls", "tcp_stalls_16k"}},
		{"vless", Variant{Security: "tls", Transport: "ws"}, nil},
		{"awg", Variant{AwgBackend: "userspace"}, []string{"initiations", "responses", "responses_unmatched"}},
		{"awg", Variant{AwgBackend: "kernel"}, nil},
		{"hysteria2", Variant{}, nil},
	} {
		un := UnsupportedKeyCounters(c.protocol, caps, c.v)
		for _, f := range KeyLink3Fields {
			if slices.Contains(c.known, f) == slices.Contains(un, f) {
				t.Errorf("%s %+v key row: %s known %v, unsupported %v", c.protocol, c.v, f, slices.Contains(c.known, f), un)
			}
		}
	}
}
