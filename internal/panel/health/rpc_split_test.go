package health

import (
	"slices"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// rounds makes n OK rounds of one inbound with the given connect and cold time.
func rounds(node, protocol string, n int, connect, latency uint32) protoRounds {
	r := protoRounds{nodeID: node, protocol: protocol}
	for range n {
		r.rounds = append(r.rounds, okRound{connect: connect, latency: latency})
	}
	return r
}

// lossy is rounds with the packets the panel's Hysteria2 client sent and lost in them.
func lossy(r protoRounds, sent, lost uint64) protoRounds {
	r.udpSent, r.udpLost = sent, lost
	return r
}

// back is rounds with what the node sent to the check and lost, the way back.
func back(r protoRounds, sent, lost uint64) protoRounds {
	r.nodeSent, r.nodeLost = sent, lost
	return r
}

func TestUDPLossNotice(t *testing.T) {
	const fast, slow = 150, 150 + udpSlowOverTCPMS // cold times; VLESS of the node runs at fast
	for _, tc := range []struct {
		name     string
		in       []protoRounds
		nodes    []string // nil = no notice
		share    uint32
		lost     uint32
		nodeLost uint32
	}{
		{"UDP a second slower than the node's VLESS", []protoRounds{
			rounds("de1", "hysteria2", 5, 900, slow), rounds("de1", "hysteria2", 5, 90, fast), rounds("de1", "awg", 3, 90, slow),
			rounds("de1", "awg", 7, 90, fast), rounds("nl1", "hysteria2", 10, 90, fast), rounds("de1", "vless", 10, 90, fast),
			rounds("nl1", "vless", 10, 90, fast),
		}, []string{"de1"}, 40, 0, 0},
		{"jitter below 600 ms is not a lost packet", []protoRounds{
			rounds("de1", "hysteria2", 10, 90, slow-1), rounds("de1", "vless", 10, 90, fast),
		}, nil, 0, 0, 0},
		{"each node against its own VLESS: a far node is slow for both", []protoRounds{
			rounds("fi1", "hysteria2", 10, 400, 1500), rounds("fi1", "vless", 10, 400, 1200),
			rounds("de1", "hysteria2", 10, 90, fast), rounds("de1", "vless", 10, 90, fast),
		}, nil, 0, 0, 0},
		{"the lost packets of the listed nodes' Hysteria2 rounds, both ways", []protoRounds{
			back(lossy(rounds("de1", "hysteria2", 4, 1100, slow+500), 1000, 31), 4000, 220),
			back(lossy(rounds("nl1", "hysteria2", 4, 90, fast), 1000, 0), 4000, 400),
			rounds("de1", "vless", 4, 90, fast), rounds("nl1", "vless", 4, 90, fast),
		}, []string{"de1"}, 100, 31, 55},
		{"TCP is slow too: the panel's own network", []protoRounds{
			rounds("de1", "hysteria2", 10, 2500, 3000), rounds("de1", "vless", 9, 90, fast), rounds("de1", "vless", 1, 2100, 2200),
		}, nil, 0, 0, 0},
		{"a node without VLESS alone is not trusted", []protoRounds{
			rounds("nl1", "hysteria2", 10, 2500, 3000), rounds("de1", "hysteria2", 10, 90, fast), rounds("de1", "vless", 10, 90, fast),
		}, nil, 0, 0, 0},
		{"two nodes without VLESS are", []protoRounds{
			rounds("nl1", "hysteria2", 10, 2500, 3000), rounds("fr1", "awg", 5, 5100, 5300), rounds("fr1", "awg", 5, 90, fast),
		}, []string{"nl1", "fr1"}, 75, 0, 0},
		{"exactly the threshold", []protoRounds{
			rounds("de1", "hysteria2", 2, 90, slow), rounds("de1", "hysteria2", 8, 90, fast), rounds("de1", "vless", 10, 90, fast),
		}, []string{"de1"}, 20, 0, 0},
		{"too few rounds to call it a trend", []protoRounds{
			rounds("de1", "hysteria2", 2, 900, slow), rounds("de1", "vless", 10, 90, fast),
		}, nil, 0, 0, 0},
		{"nothing at all", nil, nil, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := udpLossNotice(tc.in)
			if tc.nodes == nil {
				if got != nil {
					t.Fatalf("notice %+v, want none", got)
				}
				return
			}
			if got == nil || !slices.Equal(got.NodeIds, tc.nodes) || got.SharePercent != tc.share || got.LostPermille != tc.lost ||
				got.NodeLostPermille != tc.nodeLost {
				t.Fatalf("notice %+v, want nodes %v share %d lost %d node lost %d", got, tc.nodes, tc.share, tc.lost, tc.nodeLost)
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
	put := func(in string, ago time.Duration, st adminv1.CheckStatus, connect, response uint32, udp ...uint32) {
		t.Helper()
		r := Result{Status: st, At: now.Add(-ago), LatencyMS: connect + response, ConnectMS: connect, ResponseMS: response,
			ExitIP: "203.0.113.9", ExitCountry: "DE"}
		if len(udp) == 2 {
			r.UDPSent, r.UDPLost = udp[0], udp[1]
		}
		e.s.record(e.ctx, in, r)
	}
	// de1 hysteria2: three of four OK rounds waited for a retry, its QUIC client lost 12 of 400 packets
	put(hy2, 50*time.Minute, cOK, 2600, 80, 100, 5)
	put(hy2, 40*time.Minute, cOK, 2700, 0, 100, 4) // the second request did not reuse the connection
	put(hy2, 30*time.Minute, cOK, 2900, 120, 100, 3)
	put(hy2, 20*time.Minute, cOK, 90, 60, 100, 0)
	put(hy2, 10*time.Minute, cFail, 9000, 9000, 50, 50) // failed rounds are no samples
	put(hy2, 3*time.Hour, cOK, 1, 1, 50, 50)            // too old
	// nl1 has no slow rounds
	for i := range 4 {
		put(awgLike, time.Duration(10+10*i)*time.Minute, cOK, 80, 40)
	}
	// the panel's TCP probe is fine
	for i := range 4 {
		put(tcp, time.Duration(10+10*i)*time.Minute, cOK, 60, 30)
	}
	// the way back: what de1 counted for the check's key, this hour and the one before; one from three hours ago is out
	for _, h := range []struct {
		ago        time.Duration
		sent, lost int
	}{{0, 300, 12}, {time.Hour, 200, 8}, {3 * time.Hour, 900, 900}} {
		e.exec(`INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, user_id, probe_pkts_sent, probe_pkts_lost) VALUES (?, ?, ?, '', '', ?, ?)`,
			"de1", now.Add(-h.ago).Unix()/3600*3600, hy2, h.sent, h.lost)
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
	if c := cells[hy2]; c.ConnectHourMs != 2700 || c.ResponseHourMs != 80 || c.UdpSentHour != 400 || c.UdpLostHour != 12 ||
		c.NodeSentHour != 500 || c.NodeLostHour != 20 {
		t.Fatalf("hy2 connect %d response %d udp %d/%d node %d/%d", c.ConnectHourMs, c.ResponseHourMs, c.UdpLostHour, c.UdpSentHour,
			c.NodeLostHour, c.NodeSentHour)
	}
	if c := cells[tcp]; c.ConnectHourMs != 60 || c.ResponseHourMs != 30 {
		t.Fatalf("tcp connect %d response %d", c.ConnectHourMs, c.ResponseHourMs)
	}
	if u := resp.Msg.UdpLoss; u == nil || !slices.Equal(u.NodeIds, []string{"de1"}) || u.SharePercent != 75 || u.LostPermille != 30 ||
		u.NodeLostPermille != 40 {
		t.Fatalf("udp_loss %+v", u)
	}
	if m := resultMsg(Result{ConnectMS: 5, ResponseMS: 6, UDPSent: 7, UDPLost: 1}); m.ConnectMs != 5 || m.ResponseMs != 6 || m.UdpSent != 7 || m.UdpLost != 1 {
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
