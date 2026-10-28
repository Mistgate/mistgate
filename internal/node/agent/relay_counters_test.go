package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/doctor"
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
			{"counter":{"family":"inet","table":"mistgate_relay","name":"udp_9443","packets":9,"bytes":1234}},
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
	if got[1].ID != "relay" || got[1].Status != doctor.OK || got[1].Measured != now ||
		got[1].Params["relay"] != "rly_b" || got[1].Params["network"] != "udp" || got[1].Params["port"] != "9443" ||
		got[1].Params["bytes"] != "1234" || got[1].Params["packets"] != "9" {
		t.Errorf("UDP relay result = %+v", got[1])
	}
}
