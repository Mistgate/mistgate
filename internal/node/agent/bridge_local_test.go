package agent

import (
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A bridge target is local when any enabled inbound of the port runs, not only the first one by id: on a port a node's
// inbounds share, the first may be the one that failed.
func TestBridgeTargetIsLocalOnAnyRunningInboundOfThePort(t *testing.T) {
	tcp := func(port uint16) plugin.InboundSpec {
		return plugin.InboundSpec{Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: port}}
	}
	m := &model{inbounds: map[string]*inbound{
		"a": {spec: tcp(443)}, "b": {spec: tcp(443)}, "c": {spec: tcp(8443)}, "d": {spec: tcp(9443)},
	}}
	m.inbounds["d"].spec.Enabled = false
	running := &pb.InboundResult{State: pb.InboundRunState_INBOUND_RUN_STATE_RUNNING}
	failed := &pb.InboundResult{State: pb.InboundRunState_INBOUND_RUN_STATE_FAILED, Error: "address already in use"}
	results := map[string]*pb.InboundResult{"a": failed, "b": running, "c": failed, "d": running}
	for name, tc := range map[string]struct {
		target plugin.BridgeTarget
		want   bool
	}{
		"the first inbound of the port failed, the second runs": {plugin.BridgeTarget{Network: "tcp", Port: 443}, true},
		"the only inbound of the port failed":                   {plugin.BridgeTarget{Network: "tcp", Port: 8443}, false},
		"a disabled inbound does not count":                     {plugin.BridgeTarget{Network: "tcp", Port: 9443}, false},
		"no inbound on the port":                                {plugin.BridgeTarget{Network: "tcp", Port: 1}, false},
		"port 0":                                                {plugin.BridgeTarget{Network: "tcp"}, false},
		"udp":                                                   {plugin.BridgeTarget{Network: "udp", Port: 443}, false},
	} {
		if got := bridgeTargetIsLocal(m, results, tc.target); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	m.inbounds["b"].applyErr = "x"
	if bridgeTargetIsLocal(m, results, plugin.BridgeTarget{Network: "tcp", Port: 443}) {
		t.Error("an inbound with an apply error counted")
	}
}
