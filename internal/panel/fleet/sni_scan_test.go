//go:build !js

package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/prototext"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

// The panel side of the neighbour scan against a fake agent: the command and its gate, the strict reading of an answer from a node
// that may say anything, the filter of the panel's own addresses and names, the pause of 30 minutes, one scan at a time, the audit
// row without names. The scanner itself is internal/node/sniscan.

type sniResult struct {
	resp *probev1.NodeSniScanResponse
	err  error
}

func startSniScan(x *l3Env, nodeID string, run bool) <-chan sniResult {
	out := make(chan sniResult, 1)
	go func() {
		r, err := (probeService{x.f}).NodeSniScan(x.ctx, connect.NewRequest(&probev1.NodeSniScanRequest{NodeId: nodeID, Run: run}))
		if err != nil {
			out <- sniResult{err: err}
			return
		}
		out <- sniResult{resp: r.Msg}
	}()
	return out
}

func awaitSni(t *testing.T, ch <-chan sniResult) *probev1.NodeSniScanResponse {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.resp
	case <-time.After(5 * time.Second):
		t.Fatal("the scan did not finish")
		return nil
	}
}

// sniNode is a node at 203.0.113.10 with an agent that lists sni-scan/1.
func sniNode(t *testing.T) (*l3Env, *agent, *conn) {
	t.Helper()
	x, target := newL3Env(t)
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, target.nodeID)
	c, _, _ := connectCaps(target, "sni-target", capSniScan)
	return x, target, c
}

// ageSni makes the last scan of every node d older, and forgets the probe limiter's windows: the test's time passes without the
// clock moving, which would end the agents' sessions (no message for 90 s).
func ageSni(x *l3Env, d time.Duration) {
	x.exec(`UPDATE node_sni_scan SET at_unix = at_unix - ?`, int64(d/time.Second))
	p := &x.f.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, t := range p.sniStart {
		p.sniStart[id] = t.Add(-d)
	}
	p.node, p.caller = map[string][]time.Time{}, map[string][]time.Time{}
}

func waitSniScan(c *conn) *agentv1.SniScan {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetSniScan() != nil }).GetSniScan()
}

// sniAnswer is an agent's ok answer with the given candidates (anything JSON can carry) and counters.
func sniAnswer(cands ...map[string]any) *agentv1.CommandResult {
	js, _ := json.Marshal(cands)
	return &agentv1.CommandResult{Ok: true, Params: map[string]string{"addresses": "253", "open": "41", "tls13": "19", "mlkem": "7", "valid": "4",
		"partial": "0", "candidates": string(js)}}
}

func cand(ip, name, dns string) map[string]any {
	return map[string]any{"ip": ip, "name": name, "dns": dns, "h2": true, "single_site": true, "http": 200, "ms": 12, "days": 60}
}

func scanOnce(t *testing.T, x *l3Env, c *conn, nodeID string, answer *agentv1.CommandResult) *probev1.NodeSniScanResponse {
	t.Helper()
	res := startSniScan(x, nodeID, true)
	cmd := waitSniScan(c)
	replyPortCommand(c, cmd.RequestId, answer)
	return awaitSni(t, res)
}

func TestNodeSniScanSendsTheNodesOwnAddressAndKeepsOnlyWhatIsSafe(t *testing.T) {
	x, target, c := sniNode(t)
	// the panel's own: another node at an address of the same /24, a name of the panel
	peer := x.enroll("peer")
	x.exec(`UPDATE node SET address = '203.0.113.77' WHERE id = ?`, peer.nodeID)
	x.f.cfg.PublicURL = "https://panel.example.com/"
	x.f.cfg.PanelAddr = "198.51.100.9:8443"

	var cands []map[string]any
	cands = append(cands,
		cand("203.0.113.50", "shop.example.com", "here"),
		cand("203.0.113.51", "blog.example.net", "subnet"),
		cand("203.0.113.52", "far.example.org", "elsewhere"),
		cand("203.0.113.77", "peer.example.com", "here"),          // an address of the panel's own node
		cand("203.0.113.60", "panel.example.com", "here"),         // the panel's name
		cand("203.0.113.61", "PANEL.EXAMPLE.COM", "here"),         // not a plain name: dropped before the filter
		cand("198.51.100.20", "other.example.com", "here"),        // not the scanned /24
		cand("203.0.113.62", "*.example.com", "here"),             // a mask
		cand("203.0.113.63", "a b.example.com", "here"),           // a blank
		cand("203.0.113.64", "203.0.113.64", "here"),              // an address
		cand("203.0.113.65", "single", "here"),                    // one label
		cand("203.0.113.66", strings.Repeat("a", 64)+".com", "x"), // a label of 64
		cand("203.0.113.67", "ok.example.com", "somewhere"),       // a verdict the agent does not give
		cand("203.0.113.68", "\x00.example.com", "here"),          // control byte
		cand("not an address", "addr.example.com", "here"),        // no address
		cand("2001:db8::1", "v6.example.com", "here"),             // not IPv4
		cand("203.0.113.50", "shop.example.com", "here"),          // a repeat
		cand("203.0.113.53", "a.example.com", "here"),             // the second name of an address
		cand("203.0.113.53", "b.example.com", "here"),             // the second name of an address
		cand("203.0.113.53", "c.example.com", "here"),             // a third name of one address: more than the agent looks at
		map[string]any{"ip": "203.0.113.54", "name": "extra.example.com", "dns": "here", "h2": false, "http": 99999, "ms": -4, "days": 99999999,
			"cookie": "secret", "nested": map[string]any{"a": 1}}, // extra fields are dropped, numbers held in range
	)
	for i := 0; i < 30; i++ {
		cands = append(cands, cand(fmt.Sprintf("203.0.113.%d", 100+i), fmt.Sprintf("n%d.example.com", i), "subnet"))
	}
	res := startSniScan(x, target.nodeID, true)
	cmd := waitSniScan(c)
	if cmd.Address != "203.0.113.10" {
		t.Errorf("SniScan address = %q, want the node's own address", cmd.Address)
	}
	answer := sniAnswer(cands...)
	answer.Params["addresses"], answer.Params["open"], answer.Params["tls13"], answer.Params["mlkem"], answer.Params["valid"] = "9999", "5000", "300", "40", "30"
	replyPortCommand(c, cmd.RequestId, answer)
	got := awaitSni(t, res)

	if got.Error != "" || got.NodeName != "nodea" || got.Range != "203.0.113.0/24" || got.AtUnix == 0 {
		t.Fatalf("response = %+v", got)
	}
	// counters held in their bounds and in order: addresses <= 254, open <= addresses, tls13 <= open, mlkem <= tls13, valid <= mlkem
	if got.Addresses != 254 || got.Open != 254 || got.Tls13 != 254 || got.Mlkem != 40 || got.Valid != 30 {
		t.Errorf("counters = %d %d %d %d %d", got.Addresses, got.Open, got.Tls13, got.Mlkem, got.Valid)
	}
	// the first 20 valid ones are read, the 2 of the panel's own among them are left out
	if len(got.Candidates) != sniMaxList-2 {
		t.Errorf("%d candidates, want %d", len(got.Candidates), sniMaxList-2)
	}
	names := map[string]*probev1.SniCandidate{}
	for _, cd := range got.Candidates {
		names[cd.Name] = cd
		if !strings.HasPrefix(cd.Ip, "203.0.113.") {
			t.Errorf("candidate %q at %q is outside the scanned /24", cd.Name, cd.Ip)
		}
	}
	for _, want := range []string{"shop.example.com", "blog.example.net", "far.example.org", "a.example.com", "b.example.com", "extra.example.com"} {
		if names[want] == nil {
			t.Errorf("candidate %q was dropped", want)
		}
	}
	for _, bad := range []string{"peer.example.com", "panel.example.com", "PANEL.EXAMPLE.COM", "other.example.com", "*.example.com", "a b.example.com", "203.0.113.64",
		"single", "ok.example.com", "addr.example.com", "v6.example.com", "c.example.com"} {
		if names[bad] != nil {
			t.Errorf("candidate %q came through", bad)
		}
	}
	if got.OwnLeftOut != 2 {
		t.Errorf("own_left_out = %d, want 2 (the node's address and the panel's name)", got.OwnLeftOut)
	}
	if e := names["extra.example.com"]; e != nil && (e.Http != 0 || e.Ms != 0 || e.Days != 36500) {
		t.Errorf("numbers out of range were kept: %+v", e)
	}
	if s := prototext.Format(got); strings.Contains(s, "secret") || strings.Contains(s, "cookie") {
		t.Errorf("a field the panel does not know came through: %s", s)
	}

	// what is stored is what was shown; the audit row has a count and no name
	row, ok, err := x.st.NodeSniScan(x.ctx, target.nodeID)
	if err != nil || !ok || row.At.Unix() != got.AtUnix {
		t.Fatalf("stored scan = %+v, %v, %v", row, ok, err)
	}
	if strings.Contains(row.ResultJSON, "peer.example.com") || strings.Contains(row.ResultJSON, "panel.example.com") {
		t.Errorf("the stored scan holds the panel's own: %s", row.ResultJSON)
	}
	var params string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT params FROM audit WHERE action = 'node.sni_scan'`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(params, "example") || addressRe.MatchString(params) || !strings.Contains(params, `"found":"18"`) || !strings.Contains(params, `"node":"nodea"`) {
		t.Errorf("audit row = %s: want the node's name, a count and no candidate", params)
	}
}

func TestNodeSniScanAnswerReadsLikeTheStoredOne(t *testing.T) {
	x, target, c := sniNode(t)
	// nothing was ever scanned: an empty answer, no command
	empty := awaitSni(t, startSniScan(x, target.nodeID, false))
	if empty.Error != "" || empty.AtUnix != 0 || len(empty.Candidates) != 0 {
		t.Fatalf("never scanned: %+v", empty)
	}
	scanned := scanOnce(t, x, c, target.nodeID, sniAnswer(cand("203.0.113.50", "shop.example.com", "here")))
	read := awaitSni(t, startSniScan(x, target.nodeID, false))
	if read.AtUnix != scanned.AtUnix || read.Range != scanned.Range || len(read.Candidates) != 1 || read.Candidates[0].Name != "shop.example.com" ||
		read.Candidates[0].Dns != "here" || !read.Candidates[0].H2 || read.Candidates[0].Http != 200 || read.Candidates[0].Days != 60 {
		t.Errorf("stored read = %+v, scan = %+v", read, scanned)
	}
	noCommand(t, c, 100*time.Millisecond)
}

func TestNodeSniScanPausesThirtyMinutes(t *testing.T) {
	x, target, c := sniNode(t)
	first := scanOnce(t, x, c, target.nodeID, sniAnswer(cand("203.0.113.50", "shop.example.com", "here")))
	if first.Error != "" {
		t.Fatalf("first scan: %+v", first)
	}
	ageSni(x, 10*time.Minute)
	soon := awaitSni(t, startSniScan(x, target.nodeID, true))
	if soon.Error != "too_soon" || soon.RetryAfterS != 20*60 {
		t.Fatalf("after 10 minutes: error %q, retry_after_s %d; want too_soon, 1200", soon.Error, soon.RetryAfterS)
	}
	if len(soon.Candidates) != 1 || soon.AtUnix != first.AtUnix-600 { // 600 s: the aging of the test
		t.Errorf("a refused scan shows the last result: %+v", soon)
	}
	noCommand(t, c, 100*time.Millisecond)
	ageSni(x, 20*time.Minute-time.Second)
	if again := awaitSni(t, startSniScan(x, target.nodeID, true)); again.Error != "too_soon" || again.RetryAfterS != 1 {
		t.Fatalf("a second before the end: %q, %d", again.Error, again.RetryAfterS)
	}
	ageSni(x, time.Second)
	later := scanOnce(t, x, c, target.nodeID, sniAnswer(cand("203.0.113.51", "blog.example.net", "subnet")))
	if later.Error != "" || len(later.Candidates) != 1 || later.Candidates[0].Name != "blog.example.net" || later.AtUnix != first.AtUnix {
		t.Errorf("scan after the pause: %+v", later)
	}

	// the pause holds across a restart: it is read from the database, not from memory
	x.f.probes.sniStart = nil
	ageSni(x, time.Minute)
	if r := awaitSni(t, startSniScan(x, target.nodeID, true)); r.Error != "too_soon" {
		t.Errorf("after a restart: %q", r.Error)
	}
}

func TestNodeSniScanFailedScanKeepsTheLastResultAndTheNextWaits(t *testing.T) {
	x, target, c := sniNode(t)
	scanOnce(t, x, c, target.nodeID, sniAnswer(cand("203.0.113.50", "shop.example.com", "here")))
	ageSni(x, time.Hour)
	for _, test := range []struct{ node, want string }{
		{"not_my_address", "not_my_address"}, {"no_roots", "no_roots"}, {"busy", "busy"}, {"bad_params", "failed"}, {"failed: dial tcp 192.0.2.1: boom", "failed"},
	} {
		got := scanOnce(t, x, c, target.nodeID, &agentv1.CommandResult{Error: test.node})
		if got.Error != test.want {
			t.Errorf("agent error %q read as %q, want %q", test.node, got.Error, test.want)
		}
		if len(got.Candidates) != 1 || got.Candidates[0].Name != "shop.example.com" || got.AtUnix == 0 {
			t.Errorf("after the error %q the last result is gone: %+v", test.node, got)
		}
		// a scan that reached the agent starts the pause, whatever it ended in
		if wait := awaitSni(t, startSniScan(x, target.nodeID, true)); wait.Error != "too_soon" {
			t.Errorf("after the error %q the next scan was not held back: %q", test.node, wait.Error)
		}
		ageSni(x, 31*time.Minute)
	}
	// a hostile answer that is not an answer at all
	garbage := &agentv1.CommandResult{Ok: true, Params: map[string]string{"candidates": "{not json"}}
	if got := scanOnce(t, x, c, target.nodeID, garbage); got.Error != "failed" {
		t.Errorf("garbage answer: %+v", got)
	}
	ageSni(x, 31*time.Minute)
	if got := scanOnce(t, x, c, target.nodeID, &agentv1.CommandResult{Ok: true, Params: map[string]string{"candidates": strings.Repeat("[", sniMaxJSON+1)}}); got.Error != "failed" {
		t.Errorf("oversized answer: %+v", got)
	}
}

func TestNodeSniScanOneAtATime(t *testing.T) {
	x, target, c := sniNode(t)
	running := startSniScan(x, target.nodeID, true)
	cmd := waitSniScan(c)
	second := awaitSni(t, startSniScan(x, target.nodeID, true))
	if second.Error != "busy" {
		t.Fatalf("a scan while one runs: %q, want busy", second.Error)
	}
	noCommand(t, c, 100*time.Millisecond) // the second was sent nothing
	replyPortCommand(c, cmd.RequestId, sniAnswer(cand("203.0.113.50", "shop.example.com", "here")))
	if first := awaitSni(t, running); first.Error != "" || len(first.Candidates) != 1 {
		t.Fatalf("the first scan: %+v", first)
	}
	// and the slot is given back: the next call is held by the pause, not by busy
	if again := awaitSni(t, startSniScan(x, target.nodeID, true)); again.Error != "too_soon" {
		t.Errorf("after the scan: %q", again.Error)
	}
}

func TestNodeSniScanOldAgentAndOfflineNodeAreSentNothing(t *testing.T) {
	x, target := newL3Env(t)
	x.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = ?`, target.nodeID)
	// not connected
	if got := awaitSni(t, startSniScan(x, target.nodeID, true)); got.Error != "node_offline" {
		t.Errorf("offline: %q", got.Error)
	}
	// connected, without the capability
	old, oldConn := addPortCheckPeer(x, "old", "provider-b", "DE", capProbe)
	x.exec(`UPDATE node SET address = '203.0.113.20' WHERE id = ?`, old.nodeID)
	if got := awaitSni(t, startSniScan(x, old.nodeID, true)); got.Error != "agent_too_old" || len(got.Candidates) != 0 {
		t.Errorf("old agent: %+v", got)
	}
	noCommand(t, oldConn, 100*time.Millisecond)
	// neither refusal used the pause: the node scans as soon as it can
	c, _, _ := connectCaps(target, "sni-target", capSniScan)
	if got := scanOnce(t, x, c, target.nodeID, sniAnswer()); got.Error != "" || len(got.Candidates) != 0 || got.Range != "203.0.113.0/24" {
		t.Errorf("a node that was offline: %+v", got)
	}
	// an unknown node
	if _, err := (probeService{x.f}).NodeSniScan(x.ctx, connect.NewRequest(&probev1.NodeSniScanRequest{NodeId: "nod_missing", Run: true})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown node: %v", err)
	}
}

func TestNodeSniScanNeedsAPublicIPv4OfTheNode(t *testing.T) {
	x, target, c := sniNode(t)
	for _, address := range []string{"10.0.0.5", "192.168.1.9", "node.example.com", "2001:db8::10"} {
		x.exec(`UPDATE node SET address = ? WHERE id = ?`, address, target.nodeID)
		ageSni(x, time.Hour)
		want := "not_my_address"
		if address == "node.example.com" || address == "2001:db8::10" {
			want = "no_ipv4"
		}
		if got := awaitSni(t, startSniScan(x, target.nodeID, true)); got.Error != want {
			t.Errorf("address %q: %q, want %q", address, got.Error, want)
		}
		noCommand(t, c, 50*time.Millisecond)
	}
}

// parseSniScan is the wall between a node's words and the panel: whatever the bytes are, what it returns holds the bounds.
func FuzzParseSniScan(f *testing.F) {
	f.Add(`[{"ip":"203.0.113.5","name":"a.example.com","dns":"here","h2":true,"http":200,"ms":3,"days":9}]`, "10", "5")
	f.Add(`[{"ip":"203.0.113.5","name":"A.example.com","dns":"x"},{"ip":1},null,"x"]`, "-1", "x")
	f.Add(`{}`, "99999999999999999999", "")
	prefix := netip.MustParsePrefix("203.0.113.0/24")
	f.Fuzz(func(t *testing.T, js, n, partial string) {
		res, ok := parseSniScan(map[string]string{"candidates": js, "addresses": n, "open": n, "tls13": n, "mlkem": n, "valid": n, "partial": partial}, prefix)
		if !ok {
			return
		}
		if len(res.Candidates) > sniMaxList || res.Addresses > sniMaxCount || res.Open > res.Addresses || res.TLS13 > res.Open || res.MLKEM > res.TLS13 || res.Valid > res.MLKEM {
			t.Fatalf("bounds broken: %+v", res)
		}
		for _, cd := range res.Candidates {
			ip, err := netip.ParseAddr(cd.IP)
			if err != nil || !prefix.Contains(ip) || !validSniHost(cd.Name) || cd.HTTP > 599 || cd.Ms > 60_000 {
				t.Fatalf("candidate broke the rules: %+v", cd)
			}
		}
	})
}

// The panel's own names: nodes' addresses and names, the names of their inbounds, REALITY targets, the panel's address.
func TestOwnSniNamesCollectsNodesInboundsAndThePanel(t *testing.T) {
	x, _, _ := portSpecRows(t)
	x.f.cfg.PublicURL = "https://panel.example.com/admin/"
	x.f.cfg.PanelAddr = "panel2.example.com:8443"
	own, err := x.f.ownSniNames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"de1.example.com", "www.example.org", "panel.example.com", "panel2.example.com"} {
		if !own.names[name] {
			t.Errorf("own names miss %q: %v", name, own.names)
		}
	}
	r := &sniScanJSON{Candidates: []sniCandidateJSON{{IP: "203.0.113.5", Name: "www.example.org"}, {IP: "203.0.113.6", Name: "shop.example.com"}}}
	own.filter(r)
	if len(r.Candidates) != 1 || r.Candidates[0].Name != "shop.example.com" || r.OwnLeftOut != 1 {
		t.Errorf("filter = %+v", r)
	}
}
