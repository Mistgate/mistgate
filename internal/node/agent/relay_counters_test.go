package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/relayfwd"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestRelayCounterResultsReadNamedNFTRelayCounters(t *testing.T) {
	forwards := []plugin.RelayForward{
		{ID: "rly_b", Network: "udp", Port: 9443},
		{ID: "rly_a", Network: "tcp", Port: 8443},
	}
	wantArgs := []string{"-j", "list", "counters", "table", "inet", "mistgate_relay"}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	got := relayCounterResults(context.Background(), func(_ context.Context, command string, args ...string) ([]byte, error) {
		if command != "nft" || !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("runner received %q %v", command, args)
		}
		return []byte(`{"nftables":[
			{"counter":{"family":"inet","table":"mistgate_relay","name":"tcp_8443","packets":42,"bytes":65536}},
			{"counter":{"family":"inet","table":"mistgate_relay","name":"tcp_8443_back","packets":21,"bytes":32768}},
			{"counter":{"family":"inet","table":"mistgate_relay","name":"udp_9443","packets":9,"bytes":1234}},
			{"counter":{"family":"inet","table":"mistgate_relay","name":"udp_9443_back","packets":8,"bytes":4321}},
			{"counter":{"family":"inet","table":"other","name":"tcp_9999","packets":1,"bytes":1}}
		]}`), nil
	}, forwards, now)
	if len(got) != 2 {
		t.Fatalf("counter results = %+v", got)
	}
	if got[0].ID != "relay" || got[0].Status != doctor.OK || got[0].Measured != now ||
		got[0].Params["relay"] != "rly_a" || got[0].Params["network"] != "tcp" || got[0].Params["port"] != "8443" ||
		got[0].Params["bytes"] != "65536" || got[0].Params["packets"] != "42" {
		t.Errorf("TCP relay result = %+v", got[0])
	}
	if got[0].Params["bytes_back"] != "32768" || got[0].Params["packets_back"] != "21" {
		t.Errorf("TCP relay result = %+v", got[0])
	}
	if got[1].ID != "relay" || got[1].Status != doctor.OK || got[1].Measured != now ||
		got[1].Params["relay"] != "rly_b" || got[1].Params["network"] != "udp" || got[1].Params["port"] != "9443" ||
		got[1].Params["bytes"] != "1234" || got[1].Params["packets"] != "9" ||
		got[1].Params["bytes_back"] != "4321" || got[1].Params["packets_back"] != "8" {
		t.Errorf("UDP relay result = %+v", got[1])
	}
}

func TestReverseRelayCounterResultsUseListenerBothDirections(t *testing.T) {
	forwards := []plugin.RelayForward{{ID: "rly_a", Network: "tcp", Port: 8443, Mode: "reverse"}}
	got := reverseRelayCounterResults(forwards, map[string]bridge.RelayCounters{"rly_a": {Bytes: 100, BytesBack: 75}}, time.Unix(1, 0))
	if len(got) != 1 || got[0].Params["bytes"] != "100" || got[0].Params["bytes_back"] != "75" {
		t.Fatalf("reverse counters = %+v", got)
	}
	if nft := relayCounterResults(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("reverse forwards must not read DNAT counters")
		return nil, nil
	}, forwards, time.Now()); len(nft) != 0 {
		t.Fatalf("nft results for reverse relay: %+v", nft)
	}
}

// The counters of a forward carried in userspace come from the forwarder, not from nft, and only a "6" forward is counted that
// way: a kernel forward of the same list is left to relayCounterResults.
func TestForwardRelayCounterResultsUseTheUserspaceForwarder(t *testing.T) {
	forwards := []plugin.RelayForward{
		{ID: "rly_v6", Network: "udp", Port: 9443, ToFamily: "6"},
		{ID: "rly_v4", Network: "tcp", Port: 8443},
	}
	now := time.Unix(1, 0)
	got := forwardRelayCounterResults(forwards, map[string]relayfwd.Counters{
		"rly_v6": {Bytes: 100, BytesBack: 75, Packets: 3, PacketsBack: 2},
	}, now)
	if len(got) != 1 || got[0].ID != "relay" || got[0].Measured != now || got[0].Params["relay"] != "rly_v6" ||
		got[0].Params["network"] != "udp" || got[0].Params["port"] != "9443" || got[0].Params["bytes"] != "100" ||
		got[0].Params["bytes_back"] != "75" || got[0].Params["packets"] != "3" || got[0].Params["packets_back"] != "2" {
		t.Fatalf("userspace counters = %+v", got)
	}
	if nft := relayCounterResults(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("a userspace forward must not read DNAT counters")
		return nil, nil
	}, forwards[:1], time.Now()); len(nft) != 0 {
		t.Fatalf("nft results for a userspace forward: %+v", nft)
	}
}
