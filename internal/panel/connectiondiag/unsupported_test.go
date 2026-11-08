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
