package mcp

import (
	"encoding/json"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// node_connections and fleet_connections carry the TrustTunnel refusal reasons (agent.proto LinkCounters tunnel_*) of an inbound and of
// its hours, under the names the agent protocol uses; a zero reason is left out.
func TestNodeConnectionsCarryTunnelRefusals(t *testing.T) {
	in := &adminv1.InboundConnections{InboundId: "inb_tt", Protocol: "trusttunnel", Connects: 3,
		TunnelBlocked: 1, TunnelNoIpv4: 2, TunnelConnRefused: 3, TunnelConnUnreachable: 4, TunnelConnDns: 5, TunnelConnOther: 6, TunnelTimeout: 7,
		TunnelOtherDeadlines: 8, TunnelOtherAuthority: 9, TunnelOtherIcmp: 10, TunnelOtherDial: 11, TunnelStreamClosed: 12,
		TunnelStreamCredGone: 13, TunnelStreamConnLimit: 14,
		Hours: []*adminv1.LinkHour{{HourStartUnix: 3600, TunnelNoIpv4: 2, TunnelStreamConnLimit: 14}}}
	raw, err := json.Marshal(inboundConnectionsView(in))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"tunnel_blocked", "tunnel_no_ipv4", "tunnel_conn_refused", "tunnel_conn_unreachable", "tunnel_conn_dns",
		"tunnel_conn_other", "tunnel_timeout", "tunnel_other_deadlines", "tunnel_other_authority", "tunnel_other_icmp", "tunnel_other_dial",
		"tunnel_stream_closed", "tunnel_stream_cred_gone", "tunnel_stream_conn_limit"} {
		if got[name] != float64(i+1) {
			t.Errorf("%s = %v, want %d (%s)", name, got[name], i+1, raw)
		}
	}
	hours := got["hours"].([]any)[0].(map[string]any)
	if hours["tunnel_no_ipv4"] != float64(2) || hours["tunnel_stream_conn_limit"] != float64(14) || hours["tunnel_blocked"] != nil {
		t.Errorf("hour = %v", hours)
	}
	quiet, _ := json.Marshal(inboundConnectionsView(&adminv1.InboundConnections{InboundId: "inb_h", Protocol: "hysteria2", Connects: 1}))
	if string(quiet) != "" && json.Valid(quiet) {
		var m map[string]any
		_ = json.Unmarshal(quiet, &m)
		for k := range m {
			if len(k) > 7 && k[:7] == "tunnel_" {
				t.Errorf("a Hysteria2 inbound carries %s", k)
			}
		}
	}
}
