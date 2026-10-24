package connectiondiag

import (
	"slices"
	"testing"
)

func TestUnsupportedCounters(t *testing.T) {
	for protocol, want := range map[string][]string{
		"awg":       {"rejects", "drops", "hellos", "handshakes"},
		"vless":     {"connects", "rejects", "drops", "stalls", "unknown_keys", "hellos", "handshakes"},
		"hysteria2": {},
	} {
		if got := UnsupportedCounters(protocol); !slices.Equal(got, want) {
			t.Errorf("%s unsupported = %v, want %v", protocol, got, want)
		}
	}
	if got := UnsupportedCounters("VLESS"); !slices.Equal(got, UnsupportedCounters("vless")) {
		t.Errorf("protocol name case changed its unsupported counters: %v", got)
	}
}
