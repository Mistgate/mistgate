package fleet

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The limits the panel builds its bridge state to are the agent's: a state over one of them is refused whole by the agent (an
// entry that refuses its BridgeServe stops the bridge of every exit at once), so the panel's patterns, counts and pool sizes are held
// against the agent's own validators here, at the edges.
func TestPanelBridgeLimitsAreTheAgents(t *testing.T) {
	roots := x509.NewCertPool()
	serves := func(entryID, path string, exits []string) bool {
		e := nodebridge.NewEntry()
		defer e.Close()
		return e.SetServe(nodebridge.ServeConfig{EntryNodeID: entryID, Path: path, ExitNodeIDs: exits, Roots: roots}) == nil
	}
	dialsOK := func(targets []string, pool uint32) string {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		d := nodebridge.NewDialer(nil)
		defer d.Close()
		dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: "192.0.2.10", Port: 443, SNI: "msk1.example", Path: "/p", Pool: pool}
		for _, id := range targets {
			dial.Targets = append(dial.Targets, plugin.BridgeTarget{RelayID: id, Network: "tcp", Port: 4443})
		}
		d.Sync(ctx, []nodebridge.DialSpec{{Dial: dial}})
		for _, s := range d.States() {
			return s.Error
		}
		return "no_state"
	}

	for _, id := range []string{"a", "nod_de1", "a.b-c_d", strings.Repeat("n", 128), strings.Repeat("n", 129), "", "has space", "é", "a/b", "a:b"} {
		if got, want := bridgeNodeIDPattern.MatchString(id), serves("nod_msk1", "/p", []string{id}); got != want {
			t.Errorf("node id %q: the panel says %v, the agent %v", id, got, want)
		}
		if got, want := bridgeNodeIDPattern.MatchString(id), serves(id, "/p", []string{"nod_de1"}); got != want {
			t.Errorf("entry id %q: the panel says %v, the agent %v", id, got, want)
		}
	}
	for _, id := range []string{"rly_a", "a", strings.Repeat("r", 64), strings.Repeat("r", 65), "", "bad id", "r/1", "é"} {
		if got, want := bridgeRelayIDPattern.MatchString(id), dialsOK([]string{id}, 2) == ""; got != want {
			t.Errorf("relay id %q: the panel says %v, the agent %v", id, got, want)
		}
	}
	for _, path := range []string{"/a", "/" + strings.Repeat("p", 240), "/" + strings.Repeat("p", 241), "/a/../b", "/a?b", "/a#b", "/a%2f", "a", "", "/", "/.well-known/mistgate/bridge/abc_-.XYZ"} {
		if got, want := bridgePathValid(path), serves("nod_msk1", path, []string{"nod_de1"}); got != want {
			t.Errorf("path %q: the panel says %v, the agent %v", path, got, want)
		}
	}
	// the longest ids and path the panel ever builds go through together, and so do 64 exits, 64 targets and both pool sizes
	exits := make([]string, bridgeMaxExits)
	for i := range exits {
		exits[i] = fmt.Sprintf("nod_%0123d", i)
	}
	if !serves(strings.Repeat("e", 128), "/"+strings.Repeat("p", 240), exits) {
		t.Error("the agent refused 64 exits with the longest ids and the longest path")
	}
	if serves("nod_msk1", "/p", append(exits, "nod_extra")) {
		t.Errorf("the agent took more than the %d exits the panel stops at", bridgeMaxExits)
	}
	targets := make([]string, bridgeMaxTargets)
	for i := range targets {
		targets[i] = fmt.Sprintf("rly_%060d", i)
	}
	if got := dialsOK(targets, store.BridgePoolMax); got != "" {
		t.Errorf("the agent refused %d targets with the longest relay ids at pool %d: %q", bridgeMaxTargets, store.BridgePoolMax, got)
	}
	if got := dialsOK(append(targets, "rly_extra"), 2); got == "" {
		t.Errorf("the agent took more than the %d targets the panel stops at", bridgeMaxTargets)
	}
	if store.BridgePoolMin != nodebridge.MinPoolSize || store.BridgePoolMax != nodebridge.MaxPoolSize {
		t.Errorf("the panel asks for pools %d to %d, the agent takes %d to %d", store.BridgePoolMin, store.BridgePoolMax, nodebridge.MinPoolSize, nodebridge.MaxPoolSize)
	}
	for _, pool := range []uint32{store.BridgePoolMin, store.BridgePoolMax} {
		if got := dialsOK([]string{"rly_a"}, pool); got != "" {
			t.Errorf("pool %d: %q", pool, got)
		}
	}
	// an empty BridgeServe and an empty list of dials are what "stop" is: valid and harmless
	if serves("nod_msk1", "/p", nil) {
		t.Error("the agent took a serve with no exits (the panel never sends one: it sends none at all)")
	}
	// a pin the panel sends is the 64 lower-case hex digits the agent checks
	pin, ok := protocols.NormalizePin(strings.ToUpper(bridgePin))
	if !ok || pin != bridgePin {
		t.Errorf("normalised pin %q %v", pin, ok)
	}
	if got := dialsOK([]string{"rly_a"}, 2); got != "" {
		t.Errorf("a dial with a name: %q", got)
	}
}

// relayWithBridgeCandidate gives the entry a separate bridge site (port 8443) that has reported nothing yet, and an auto relay,
// switched on and direct, whose leg has failed while the exit reaches the site: the controller may move it any second, so the
// exit has to dial, and the entry has to serve.
func (x *relayEnv) relayWithBridgeCandidate(id string, active string) {
	x.t.Helper()
	x.exec(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1 vless-tls/1' WHERE id = ?`, x.entry.nodeID)
	x.exec(`UPDATE node SET agent_caps = 'relay-reverse/1 vless-tls/1' WHERE id = ?`, x.exit.nodeID)
	x.givePath(8443)
	x.reverseRelay(id, relayInbound, 7443, active)
	if err := x.st.SetRelayBridgeSide(x.ctx, id, "entry", nil); err != nil { // the site has not reported yet
		x.t.Fatal(err)
	}
	now := x.f.now().Unix()
	if err := x.st.SetRelayCheck(x.ctx, id, store.RelayCheck{At: now, Network: "tcp", Port: 7443, Leg: store.RelayStage{Status: "fail", Code: "timeout", At: now},
		ReverseReach: store.RelayStage{Status: "ok", At: now, Port: 8443}}); err != nil {
		x.t.Fatal(err)
	}
}

func (x *relayEnv) desiredOf(nodeID string) (*plugin.BridgeServe, []plugin.BridgeDial) {
	x.t.Helper()
	n, err := x.st.Node(x.ctx, nodeID)
	if err != nil {
		x.t.Fatal(err)
	}
	serve, dials, err := x.f.bridgeDesired(x.ctx, n, n.AgentCaps)
	if err != nil {
		x.t.Fatal(err)
	}
	return serve, dials
}

// The first pin, and a pin after the entry's certificate was renewed: the entry serves its site as soon as a relay may use the
// bridge, before anything is switched, and reports the pin; only then does the exit dial, with that pin, and never without a way to
// verify the entry. A new pin goes to the exit in the next state, and a site that stops reporting one takes the dial away.
func TestBridgeFirstPinAndPinRenewal(t *testing.T) {
	x := newRelayEnv(t)
	x.relayWithBridgeCandidate("rly_a", store.RelayModeDNAT)

	serve, _ := x.desiredOf(x.entry.nodeID)
	if serve == nil || serve.Port != 8443 || serve.Path != bridgePath || len(serve.ExitNodeIDs) != 1 || serve.ExitNodeIDs[0] != x.exit.nodeID {
		t.Fatalf("the entry does not serve its site before the first dial: %+v", serve)
	}
	if _, dials := x.desiredOf(x.exit.nodeID); len(dials) != 0 {
		t.Fatalf("the exit dials a site whose certificate nobody has reported: %+v", dials)
	}

	now := x.f.now()
	report := func(pin string, at time.Time) {
		x.f.recordBridgeReports(x.ctx, x.entry.nodeID, []*agentv1.BridgeState{{PeerNodeId: x.exit.nodeID, SiteCertPinSha256: pin}}, at)
	}
	report(bridgePin, now)
	_, dials := x.desiredOf(x.exit.nodeID)
	if len(dials) != 1 || dials[0].PinSHA256 != bridgePin || dials[0].Port != 8443 || dials[0].Path != bridgePath || dials[0].Pool != 2 {
		t.Fatalf("the exit's dial after the first pin: %+v", dials)
	}
	ds := &agentv1.DesiredState{BridgeDials: bridgeDialsProto(dials), BridgeServe: bridgeServeProto(serve)}
	assertAgentBridgeValid(t, ds, x.pool)

	// the certificate is renewed: the entry's next report says the new pin (a changed pin is written at once, whatever the clock)
	renewed := strings.Repeat("cd", 32)
	report(renewed, x.f.now())
	if _, dials = x.desiredOf(x.exit.nodeID); len(dials) != 1 || dials[0].PinSHA256 != renewed {
		t.Fatalf("the exit was not given the renewed pin: %+v", dials)
	}

	// the site stops reporting a pin (it is down): no dial rather than one that trusts anything
	report("", x.f.now())
	if _, dials = x.desiredOf(x.exit.nodeID); len(dials) != 0 {
		t.Fatalf("a dial without a pin or a name: %+v", dials)
	}
}

// A relay that is the owner's dnat never raises a site; one that is switched off still does (it may be switched on and checked).
func TestBridgeSiteIsRaisedForRelaysThatMayUseIt(t *testing.T) {
	x := newRelayEnv(t)
	x.relayWithBridgeCandidate("rly_a", store.RelayModeDNAT)
	off := false
	x.setRelay("rly_a", store.RelayPatch{Enabled: &off})
	if serve, dials := x.desiredOf(x.entry.nodeID); serve == nil || len(dials) != 0 {
		t.Fatalf("a relay that is off must still have its site up for its check: %+v", serve)
	}
	dnat := store.RelayModeDNAT
	x.setRelay("rly_a", store.RelayPatch{Mode: &dnat})
	if serve, _ := x.desiredOf(x.entry.nodeID); serve != nil {
		t.Fatalf("the owner's dnat raised a bridge site: %+v", serve)
	}
}

// The panel writes what an agent says of its bridges when it changes, and otherwise every 20 seconds; a node that has never
// reported a bridge costs no database work, however many stats batches it sends.
func TestBridgeReportsAreWrittenOnChangeOrEveryTwentySeconds(t *testing.T) {
	x := newRelayEnv(t)
	x.relayWithBridgeCandidate("rly_a", store.RelayModeReverse)
	now := x.f.now()
	rows := func(conns uint32, clients uint32) []*agentv1.BridgeState {
		return []*agentv1.BridgeState{{PeerNodeId: x.exit.nodeID, Conns: conns, SinceUnix: now.Unix() - 60, ClientsNow: clients, SiteCertPinSha256: bridgePin}}
	}
	conns := func() uint32 {
		l, err := x.st.RelayLink(x.ctx, "rly_a")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := l.Bridge()
		if b.Entry == nil {
			return 0
		}
		return b.Entry.Conns
	}
	mark := func() { // a sentinel nobody but a write of the report changes
		if err := x.st.SetRelayBridgeSide(x.ctx, "rly_a", "entry", &store.RelayBridgeSideCheck{Connected: true, Conns: 99, AtUnix: now.Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, rows(2, 10), now)
	if conns() != 2 {
		t.Fatalf("the first report was not written: %d", conns())
	}
	mark()
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, rows(2, 10), now.Add(5*time.Second))
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, rows(2, 14), now.Add(6*time.Second)) // four more people: not news
	if conns() != 99 {
		t.Fatalf("a report that says the same was written again: %d", conns())
	}
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, rows(2, 10), now.Add(21*time.Second))
	if conns() != 2 {
		t.Fatalf("the report was not refreshed after 20 seconds: %d", conns())
	}
	mark()
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, rows(1, 10), now.Add(22*time.Second)) // a connection went: news
	if conns() != 1 {
		t.Fatalf("a changed report was not written at once: %d", conns())
	}
	mark()
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, rows(1, 30), now.Add(23*time.Second)) // ten more people: news
	if conns() != 1 {
		t.Fatalf("people up by a step of ten was not written at once: %d", conns())
	}

	// a node that never reported a bridge: nothing is read or written for it
	mark()
	x.f.recordBridgeReports(x.ctx, x.exit.nodeID, nil, now.Add(24*time.Second))
	x.f.recordBridgeReports(x.ctx, x.exit.nodeID, nil, now.Add(48*time.Second))
	if conns() != 99 {
		t.Fatalf("an empty snapshot of a node that never reported touched the relay: %d", conns())
	}
	x.f.bridgeSeenMu.Lock()
	_, kept := x.f.bridgeSeen[x.exit.nodeID]
	x.f.bridgeSeenMu.Unlock()
	if kept {
		t.Fatal("a node that never reported a bridge is kept in the table of those that do")
	}
}

// The entry's count of people raises the pool to four at 80 % of what two connections carry (even for a quiet forward), keeps it
// there while people stay, and lets go 30 minutes after the load has gone; the figure of the exit's relay shows the same.
func TestBridgeOccupancyChoosesThePool(t *testing.T) {
	x := newRelayEnv(t)
	x.relayWithBridgeCandidate("rly_a", store.RelayModeReverse)
	base := x.f.now().Add(-2 * time.Hour) // the story is told in the past, so that every report is older than the real clock
	report := func(clients uint32, at time.Time) {
		x.f.recordBridgeReports(x.ctx, x.entry.nodeID, []*agentv1.BridgeState{{PeerNodeId: x.exit.nodeID, Conns: 2, SinceUnix: base.Unix() - 60,
			ClientsNow: clients, SiteCertPinSha256: bridgePin}}, at)
	}
	pool := func() uint32 {
		l, err := x.st.RelayLink(x.ctx, "rly_a")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := l.Bridge()
		return max(b.Pool, 2)
	}
	for _, step := range []struct {
		clients uint32
		after   time.Duration
		want    uint32
		why     string
	}{
		{100, 0, 2, "100 people are well inside two connections"},
		{194, 30 * time.Second, 4, "80 % of what two connections carry (242) asks for four"},
		{150, 10 * time.Minute, 4, "people above 40 % of two connections' room keep four"},
		{20, 11 * time.Minute, 4, "the load went a minute ago: 30 minutes of calm first"},
		{20, 42 * time.Minute, 2, "30 minutes after the load, two again"},
	} {
		report(step.clients, base.Add(step.after))
		if got := pool(); got != step.want {
			t.Fatalf("%d people at +%s: pool %d, want %d (%s)", step.clients, step.after, got, step.want, step.why)
		}
	}
	// and what the exit is told is that pool, right now
	x.f.recordBridgeReports(x.ctx, x.entry.nodeID, []*agentv1.BridgeState{{PeerNodeId: x.exit.nodeID, Conns: 2, SinceUnix: base.Unix() - 60,
		ClientsNow: 200, SiteCertPinSha256: bridgePin}}, x.f.now())
	_, dials := x.desiredOf(x.exit.nodeID)
	if len(dials) != 1 || dials[0].Pool != 4 || len(dials[0].Targets) != 1 {
		t.Fatalf("the exit's dial with 200 people on the bridge: %+v", dials)
	}
	assertAgentBridgeValid(t, &agentv1.DesiredState{BridgeDials: bridgeDialsProto(dials)}, x.pool)
}
