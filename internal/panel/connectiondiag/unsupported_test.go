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
		if got := UnsupportedCounters(protocol); !slices.Equal(got, want) {
			t.Errorf("%s unsupported = %v, want %v", protocol, got, want)
		}
	}
	if got := UnsupportedCounters("VLESS"); !slices.Equal(got, UnsupportedCounters("vless")) {
		t.Errorf("protocol name case changed its unsupported counters: %v", got)
	}
	if got := UnsupportedCountersForNode("hysteria2", []string{"links/1", "links/2"}); len(got) != 0 {
		t.Errorf("Hysteria2 links/2 unsupported = %v, want none", got)
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
