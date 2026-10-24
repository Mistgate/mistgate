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
