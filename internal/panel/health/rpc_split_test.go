package health

import (
	"slices"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// rounds makes n OK rounds of one inbound, slow of them at or above the UDP retry threshold.
func rounds(node, protocol string, n, slow int) protoRounds {
	r := protoRounds{nodeID: node, protocol: protocol}
	for i := range n {
		ms := uint32(90)
		if i < slow {
			ms = udpRetryMS + 500
		}
		r.connect = append(r.connect, ms)
	}
	return r
}

func TestUDPLossNotice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    []protoRounds
		nodes []string // nil = no notice
		share uint32
	}{
		{"only UDP is slow", []protoRounds{
			rounds("de1", "hysteria2", 10, 5), rounds("de1", "awg", 10, 3), rounds("nl1", "hysteria2", 10, 0), rounds("de1", "vless", 10, 0),
		}, []string{"de1"}, 40},
		{"two nodes, shares add up over the flagged ones", []protoRounds{
			rounds("de1", "hysteria2", 10, 5), rounds("nl1", "awg", 20, 4), rounds("fr1", "hysteria2", 10, 1), rounds("de1", "vless", 10, 0),
		}, []string{"de1", "nl1"}, 30},
		{"TCP is slow too: the panel's own network", []protoRounds{
			rounds("de1", "hysteria2", 10, 6), rounds("de1", "vless", 10, 1),
		}, nil, 0},
		{"TCP a little slow but under 5 percent", []protoRounds{
			rounds("de1", "hysteria2", 10, 6), rounds("de1", "vless", 40, 1),
		}, []string{"de1"}, 60},
		{"no TCP rounds, one node is not enough", []protoRounds{
			rounds("de1", "hysteria2", 10, 6), rounds("nl1", "awg", 10, 0),
		}, nil, 0},
		{"no TCP rounds, two nodes are", []protoRounds{
			rounds("de1", "hysteria2", 10, 6), rounds("nl1", "awg", 10, 2),
		}, []string{"de1", "nl1"}, 40},
		{"below the threshold", []protoRounds{
			rounds("de1", "hysteria2", 10, 1), rounds("nl1", "awg", 20, 3), rounds("de1", "vless", 10, 0),
		}, nil, 0},
		{"exactly the threshold", []protoRounds{
			rounds("de1", "hysteria2", 10, 2), rounds("de1", "vless", 10, 0),
		}, []string{"de1"}, 20},
		{"too few rounds to call it a trend", []protoRounds{
			rounds("de1", "hysteria2", 2, 2), rounds("de1", "vless", 10, 0),
		}, nil, 0},
		{"nothing at all", nil, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := udpLossNotice(tc.in)
			if tc.nodes == nil {
				if got != nil {
					t.Fatalf("notice %+v, want none", got)
				}
				return
			}
			if got == nil || !slices.Equal(got.NodeIds, tc.nodes) || got.SharePercent != tc.share {
				t.Fatalf("notice %+v, want nodes %v share %d", got, tc.nodes, tc.share)
			}
		})
	}
}

// GetChecks: the hourly medians of connect and response time leave out rounds without them (0), failed rounds and
// rounds older than an hour; the UDP-loss notice names the node.
func TestGetChecksSplitMediansAndUDPLoss(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "hostera", true)
	hy2 := e.inbound("de1", 443)
	awgLike := e.inbound("nl1", 443)
	tcp := e.vlessInbound("de1", 8443)
	now := e.clock.Now()
	put := func(in string, ago time.Duration, st adminv1.CheckStatus, connect, response uint32) {
		t.Helper()
		e.s.record(e.ctx, in, Result{Status: st, At: now.Add(-ago), LatencyMS: connect + response, ConnectMS: connect, ResponseMS: response,
			ExitIP: "203.0.113.9", ExitCountry: "DE"})
	}
	// de1 hysteria2: three of four OK rounds waited for a retry
	put(hy2, 50*time.Minute, cOK, 2600, 80)
	put(hy2, 40*time.Minute, cOK, 2700, 0) // the second request did not reuse the connection
	put(hy2, 30*time.Minute, cOK, 2900, 120)
	put(hy2, 20*time.Minute, cOK, 90, 60)
	put(hy2, 10*time.Minute, cFail, 9000, 9000) // failed rounds are no samples
	put(hy2, 3*time.Hour, cOK, 1, 1)            // too old
	// nl1 has no slow rounds
	for i := range 4 {
		put(awgLike, time.Duration(10+10*i)*time.Minute, cOK, 80, 40)
	}
	// the panel's TCP probe is fine
	for i := range 4 {
		put(tcp, time.Duration(10+10*i)*time.Minute, cOK, 60, 30)
	}

	resp, err := rpc{e.s}.GetChecks(e.ctx, req(&adminv1.GetChecksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	cells := map[string]*adminv1.CheckCell{}
	for _, row := range resp.Msg.Rows {
		for _, c := range row.Cells {
			if c.Deployed {
				cells[c.InboundId] = c
			}
		}
	}
	// medians are the upper middle of the sorted values: connect {90 2600 2700 2900} -> 2700, response {60 80 120} -> 80
	if c := cells[hy2]; c.ConnectHourMs != 2700 || c.ResponseHourMs != 80 {
		t.Fatalf("hy2 connect %d response %d", c.ConnectHourMs, c.ResponseHourMs)
	}
	if c := cells[tcp]; c.ConnectHourMs != 60 || c.ResponseHourMs != 30 {
		t.Fatalf("tcp connect %d response %d", c.ConnectHourMs, c.ResponseHourMs)
	}
	if u := resp.Msg.UdpLoss; u == nil || !slices.Equal(u.NodeIds, []string{"de1"}) || u.SharePercent != 75 {
		t.Fatalf("udp_loss %+v", u)
	}
	if m := resultMsg(Result{ConnectMS: 5, ResponseMS: 6}); m.ConnectMs != 5 || m.ResponseMs != 6 {
		t.Fatalf("result message %+v", m)
	}

	// the slow rounds age out: no notice
	e.clock.Advance(time.Hour)
	resp, err = rpc{e.s}.GetChecks(e.ctx, req(&adminv1.GetChecksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.UdpLoss != nil {
		t.Fatalf("udp_loss %+v after the slow rounds aged out", resp.Msg.UdpLoss)
	}
}
