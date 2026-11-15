//go:build !js

package fleet

import (
	"bytes"
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"strconv"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	mtprotoProto "github.com/mistgate/mistgate/internal/panel/protocols/mtproto"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	reverseEntryAddress = "192.0.2.11"
	reverseExitAddress  = "192.0.2.12"
)

// reverseCheckEnv uses documentation node names and addresses to drive R0 with the repository's fake agents.
func newReverseCheckEnv(t *testing.T, entrySite, exitTCP bool) *checkEnv {
	t.Helper()
	reg, err := protocols.NewRegistry(fakeProto{"fakehy", plugin.ClientHapp}, fakeProto{"fakewg", plugin.ClientAmnezia},
		tcpProto{fakeProto{"vless", plugin.ClientMihomo}}, mtprotoProto.New())
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, reg)
	x := &checkEnv{relayEnv: &relayEnv{env: e, entry: e.enroll("msk1"), exit: e.enroll("de1")}}
	now := time.Now().Unix()
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES
		('prf_hy', 'fakehy', 'hy', '{"port":4443}', 1, ?, ?),
		('prf_vless', 'vless', 'vl', '{"port":8443,"network":"tcp","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}', 1, ?, ?)`,
		now, now, now, now)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at) VALUES
		('inb_exit', 'prf_hy', ?, 1, 'active', ?, ?), ('inb_vless', 'prf_vless', ?, 1, 'active', ?, ?)`,
		x.exit.nodeID, now, now, x.exit.nodeID, now, now)
	if entrySite {
		e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at)
			VALUES ('prf_entry_site', 'vless', 'entry site', '{"port":443,"network":"tcp","tls":"self","name":"msk1.example.invalid","security":"tls"}', 1, ?, ?)`, now, now)
		e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, state, created_at, updated_at)
			VALUES ('inb_entry_site', 'prf_entry_site', ?, 1, 'active', ?, ?)`, x.entry.nodeID, now, now)
	}
	for _, n := range []struct {
		a       *agent
		address string
	}{{x.entry, reverseEntryAddress}, {x.exit, reverseExitAddress}} {
		e.exec(`UPDATE node SET state = 'active', address = ?, provider = 'provider-r0', country_code = 'ZZ' WHERE id = ?`, n.address, n.a.nodeID)
	}
	e.f.settleUDP = func(context.Context) error { return nil }
	e.run()
	entryCaps := []string{plugin.CapRelayForward, capUDPCheck, capTCPCheck, capVLESS, capSpeed}
	exitCaps := []string{capMTProto, capVLESS, capUDPCheck, capSpeed}
	if exitTCP {
		exitCaps = append(exitCaps, capTCPCheck)
	}
	x.ec, _, _ = connectCaps(x.entry, "msk1", entryCaps...)
	x.xc, _, _ = connectCaps(x.exit, "de1", exitCaps...)
	x.waitApplied(x.exit.nodeID)
	if entrySite {
		x.waitApplied(x.entry.nodeID)
	}
	return x
}

func TestCheckRelayReverseReachRunsAfterFailedLegAndStoresResult(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	resultCh := x.start("rly_t")
	leg := waitTCPProbe(x.ec)
	if leg.Host != reverseExitAddress || len(leg.Targets) != 1 || leg.Targets[0].Port != 8443 || leg.Targets[0].Sni != "www.example.org" {
		t.Fatalf("leg probe = %+v", leg)
	}
	replyPortCommand(x.ec, leg.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t8443": "timeout"}})

	reverse := waitTCPProbe(x.xc)
	if reverse.Host != reverseEntryAddress || len(reverse.Targets) != 1 || reverse.Targets[0].Port != 443 || reverse.Targets[0].Sni != "msk1.example.invalid" {
		t.Fatalf("reverse reach probe = %+v", reverse)
	}
	replyPortCommand(x.xc, reverse.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t443": "ours"}})

	got := result(t, resultCh)
	if got.Leg.Status != "fail" || got.ReverseReach.Status != "ok" || got.ReverseReach.Code != "" || got.ReverseReach.Sender != x.exit.nodeID ||
		got.ReverseReach.Verdict != "ours" || got.ReverseReach.At == 0 || got.Stage != store.RelayStageLeg || got.OK {
		t.Fatalf("check = %+v", got)
	}
	if got.ReverseSpeed.Status != "" {
		t.Fatalf("the speed proof ran without a request: %+v", got.ReverseSpeed)
	}
	stored, err := x.st.RelayLink(x.ctx, "rly_t")
	if err != nil {
		t.Fatal(err)
	}
	check, found := stored.Check()
	if !found || check.ReverseReach != got.ReverseReach {
		t.Fatalf("stored reverse reach = %+v, found %v", check.ReverseReach, found)
	}
	idle(t, 100*time.Millisecond, x.ec, x.xc)
}

func TestCheckRelayReverseReachDoesNotRunAfterPassingLegUnlessAsked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stages []string
		want   bool
	}{
		{name: "default"},
		{name: "explicit", stages: []string{store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newReverseCheckEnv(t, true, true)
			x.tcpRelay(false)
			resultCh := x.start("rly_t", tc.stages...)
			leg := waitTCPProbe(x.ec)
			if leg.Host != reverseExitAddress {
				t.Fatalf("leg host = %q", leg.Host)
			}
			replyPortCommand(x.ec, leg.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t8443": "ours"}})
			if tc.want {
				reverse := waitTCPProbe(x.xc)
				if reverse.Host != reverseEntryAddress || len(reverse.Targets) != 1 || reverse.Targets[0].Port != 443 {
					t.Fatalf("explicit reverse probe = %+v", reverse)
				}
				replyPortCommand(x.xc, reverse.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t443": "ours"}})
			}
			got := result(t, resultCh)
			if (got.ReverseReach.Status == "ok") != tc.want {
				t.Fatalf("reverse reach = %+v, want ran %v", got.ReverseReach, tc.want)
			}
			idle(t, 75*time.Millisecond, x.ec, x.xc)
		})
	}
}

func TestCheckRelayReverseReachReportsMissingSiteAndUnsupportedAgent(t *testing.T) {
	t.Run("entry has no site", func(t *testing.T) {
		x := newReverseCheckEnv(t, false, true)
		x.tcpRelay(false)
		resultCh := x.start("rly_t")
		tcpRun(x.ec, "timeout", 8443)
		got := result(t, resultCh)
		if got.ReverseReach.Status != "skipped" || got.ReverseReach.Code != "entry_site_unavailable" || got.ReverseReach.Sender != "" {
			t.Fatalf("missing site stage = %+v", got.ReverseReach)
		}
		idle(t, 75*time.Millisecond, x.xc)
	})
	t.Run("entry site port refuses the connection", func(t *testing.T) {
		x := newReverseCheckEnv(t, true, true)
		x.tcpRelay(false)
		resultCh := x.start("rly_t")
		tcpRun(x.ec, "timeout", 8443)
		reverse := waitTCPProbe(x.xc)
		replyPortCommand(x.xc, reverse.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t443": "refused"}})
		got := result(t, resultCh)
		if got.ReverseReach.Status != "skipped" || got.ReverseReach.Code != "entry_site_unavailable" || got.ReverseReach.Sender != x.exit.nodeID {
			t.Fatalf("refused entry site stage = %+v", got.ReverseReach)
		}
	})
	t.Run("exit agent lacks tcpcheck", func(t *testing.T) {
		x := newReverseCheckEnv(t, true, false)
		x.tcpRelay(false)
		resultCh := x.start("rly_t")
		tcpRun(x.ec, "timeout", 8443)
		got := result(t, resultCh)
		if got.ReverseReach.Status != "skipped" || got.ReverseReach.Code != "unsupported" || got.ReverseReach.Sender != x.exit.nodeID {
			t.Fatalf("unsupported stage = %+v", got.ReverseReach)
		}
		idle(t, 75*time.Millisecond, x.xc)
	})
}

func TestRelayChecksSharingNodesDoNotOverlapMeasurements(t *testing.T) {
	x := newReverseCheckEnv(t, false, true)
	x.tcpRelay(false)
	x.relay("rly_u", 8443, false)
	first := x.start("rly_t")
	probe := waitTCPProbe(x.ec)
	second := x.start("rly_u")
	firstLock := x.entry.nodeID
	if x.exit.nodeID < firstLock {
		firstLock = x.exit.nodeID
	}
	waitPortCheckWaiter(t, x.f, firstLock)
	idle(t, 150*time.Millisecond, x.ec, x.xc)
	replyPortCommand(x.ec, probe.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"family": "4", "t8443": "ours"}})
	if got := result(t, first); got.Leg.Status != "ok" {
		t.Fatalf("first leg = %+v", got.Leg)
	}
	udpRun(x.xc, x.ec, 4443, udpCheckSendCount)
	if got := result(t, second); got.Leg.Status != "ok" {
		t.Fatalf("second leg = %+v", got.Leg)
	}
}

func TestCheckRelayReverseSpeedRequiresRequestAndUsesBoundedSpeedReservation(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	resultCh := x.start("rly_t", store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach, store.RelayStageReverseSpeed)
	tcpRun(x.ec, "ours", 8443)
	tcpRun(x.xc, "ours", 443)

	serve := waitSpeed(x.ec, 1)
	if serve.Count < speedMinMB || serve.Count > speedMaxMB || serve.Count != relayReverseSpeedMB || !serve.Df {
		t.Fatalf("reverse speed serve = %+v", serve)
	}
	replyPortCommand(x.ec, serve.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"port": "40201"}})
	run := waitSpeed(x.xc, 2)
	if run.Host != reverseEntryAddress || len(run.Ports) != 1 || run.Ports[0] != 40201 || run.Count < speedMinMB || run.Count > speedMaxMB || !run.Df {
		t.Fatalf("reverse speed run = %+v", run)
	}
	replyPortCommand(x.xc, run.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"mbit_s": "87.3", "bytes": strconv.FormatUint(uint64(relayReverseSpeedMB)<<20, 10), "connect_ms": "13", "first_byte_ms": "35", "duration_ms": "725"}})
	stop := waitSpeed(x.ec, 3)
	replyPortCommand(x.ec, stop.RequestId, &agentv1.CommandResult{Ok: true})

	got := result(t, resultCh)
	stage := got.ReverseSpeed
	if stage.Status != "ok" || stage.Sender != x.exit.nodeID || stage.MB < speedMinMB || stage.MB > speedMaxMB || stage.MB != relayReverseSpeedMB ||
		stage.Bytes != uint64(relayReverseSpeedMB)<<20 || stage.MbitS != 87.3 || stage.ConnectMS != 13 || stage.FirstByteMS != 35 || stage.DurationMS != 725 {
		t.Fatalf("reverse speed stage = %+v", stage)
	}
	rows, err := x.st.PathSpeeds(x.ctx, time.Unix(0, 0), x.entry.nodeID, 0)
	if err != nil || len(rows) != 1 || rows[0].FromID != x.exit.nodeID || rows[0].Direction != "up" {
		t.Fatalf("reverse speed history = %+v, %v", rows, err)
	}
}

func TestCheckRelayReverseSpeedSkipsWhenEitherNodeIsBusy(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	release, err := x.f.startSpeed(x.ctx, x.exit.nodeID, x.entry.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	resultCh := x.start("rly_t", store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach, store.RelayStageReverseSpeed)
	tcpRun(x.ec, "ours", 8443)
	tcpRun(x.xc, "ours", 443)
	got := result(t, resultCh)
	if got.ReverseSpeed.Status != "skipped" || got.ReverseSpeed.Code != "busy" {
		t.Fatalf("busy speed stage = %+v", got.ReverseSpeed)
	}
	idle(t, 75*time.Millisecond, x.ec, x.xc)
}

// The proof the controller asks for before a reverse relay goes back to DNAT: the ENTRY connects to the exit's node and takes 8 MB
// (the exit sends: that is the way people's downloads go), and it runs as the panel's own, not the owner's.
func TestCheckRelayDirectSpeedIsTheEntryConnectingToTheExit(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	resultCh := x.start("rly_t", store.RelayStageDirectSpeed)
	serve := waitSpeed(x.xc, 1)
	if serve.Count != relayDirectSpeedMB || serve.Df {
		t.Fatalf("direct speed serve = %+v (the exit serves 8 MB, and the sender is the exit: not the entry's direction up)", serve)
	}
	replyPortCommand(x.xc, serve.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"port": "40202"}})
	run := waitSpeed(x.ec, 2)
	if run.Host != reverseExitAddress || len(run.Ports) != 1 || run.Ports[0] != 40202 || run.Count != relayDirectSpeedMB || run.Df {
		t.Fatalf("direct speed run = %+v (the entry connects to the exit's address, never the other way)", run)
	}
	replyPortCommand(x.ec, run.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"mbit_s": "64.5", "bytes": strconv.FormatUint(uint64(relayDirectSpeedMB)<<20, 10), "connect_ms": "21", "first_byte_ms": "48", "duration_ms": "1040"}})
	stop := waitSpeed(x.xc, 3)
	replyPortCommand(x.xc, stop.RequestId, &agentv1.CommandResult{Ok: true})

	got := result(t, resultCh)
	stage := got.DirectSpeed
	if stage.Status != "ok" || stage.Sender != x.entry.nodeID || stage.MB != relayDirectSpeedMB || stage.Bytes != uint64(relayDirectSpeedMB)<<20 || stage.MbitS != 64.5 {
		t.Fatalf("direct speed stage = %+v", stage)
	}
	if got.Leg.Status != "" || got.ReverseSpeed.Status != "" {
		t.Fatalf("the proof ran other stages: leg %+v reverse speed %+v", got.Leg, got.ReverseSpeed)
	}
	rows, err := x.st.PathSpeeds(x.ctx, time.Unix(0, 0), x.entry.nodeID, 0)
	if err != nil || len(rows) != 1 || rows[0].FromID != x.entry.nodeID || rows[0].Direction != "down" {
		t.Fatalf("direct speed history = %+v, %v", rows, err)
	}
	// it is kept with the check, and a later run of other stages does not lose it
	again := x.start("rly_t", store.RelayStageLeg)
	tcpRun(x.ec, "ours", 8443)
	if kept := result(t, again).DirectSpeed; kept.Status != "ok" || kept.At != stage.At {
		t.Fatalf("a later check lost the proof: %+v", kept)
	}
}

// A limit reached, or a busy node, is "skipped busy" and sends the agents nothing; the daily limit of speed tests holds for the
// panel's own proofs, so the controller cannot use up what the owner has.
func TestCheckRelayDirectSpeedRespectsTheDailyLimit(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	for i := 0; i < speedNodeDayMax; i++ {
		if err := x.st.AddPathSpeedStart(x.ctx, "system", x.entry.nodeID, x.exit.nodeID, time.Now().Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got := result(t, x.start("rly_t", store.RelayStageDirectSpeed))
	if got.DirectSpeed.Status != "skipped" || got.DirectSpeed.Code != "busy" {
		t.Fatalf("direct speed over the daily limit = %+v", got.DirectSpeed)
	}
	idle(t, 75*time.Millisecond, x.ec, x.xc)
}

// A reverse_reach probe says which port it was made for; a later check that does not repeat it keeps it only while the entry's
// endpoint is still that port (the first version probed 443 and wrote no port).
func TestCheckRelayDropsAReverseProbeOfAnEndpointTheEntryNoLongerHas(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	now := time.Now().Unix()
	if err := x.st.SetRelayCheck(x.ctx, "rly_t", store.RelayCheck{At: now, Network: "tcp", Port: 7443,
		ReverseReach: store.RelayStage{Status: "ok", At: now}}); err != nil { // no port: 443
		t.Fatal(err)
	}
	run := func() store.RelayCheck {
		resultCh := x.start("rly_t", store.RelayStageLeg)
		tcpRun(x.ec, "ours", 8443)
		return result(t, resultCh)
	}
	if got := run(); got.ReverseReach.Status != "ok" {
		t.Fatalf("the probe of 443 was dropped while the entry's endpoint is 443: %+v", got.ReverseReach)
	}
	if err := x.st.SetNodeBridgePort(x.ctx, x.entry.nodeID, 8443); err != nil {
		t.Fatal(err)
	}
	if got := run(); got.ReverseReach.Status != "" {
		t.Fatalf("the probe of 443 survived the endpoint moving to 8443: %+v", got.ReverseReach)
	}
}

func TestAcquireRelayPortChecksSortsAndDeduplicatesNodeIDs(t *testing.T) {
	f := &Fleet{}
	release, err := f.acquireRelayPortChecks(context.Background(), "de1", "de1", "msk1")
	if err != nil {
		t.Fatal(err)
	}
	f.portCheckMu.Lock()
	ids := make([]string, 0, len(f.portCheckLocks))
	for id, lock := range f.portCheckLocks {
		if len(lock.gate) != 1 {
			t.Errorf("node %s has %d gate holders, want one", id, len(lock.gate))
		}
		ids = append(ids, id)
	}
	f.portCheckMu.Unlock()
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"de1", "msk1"}) {
		t.Fatalf("locked node IDs = %v", ids)
	}
	release()
}

func TestAcquireRelayPortChecksReleasesWhatItHoldsWhenTheContextEnds(t *testing.T) {
	f := &Fleet{}
	hold, err := f.acquireRelayPortChecks(context.Background(), "msk1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.acquireRelayPortChecks(ctx, "msk1", "de1"); done <- err }() // de1 first, then it waits for msk1
	waitPortCheckWaiter(t, f, "msk1")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting for a held node ended with %v, want the context's error", err)
	}
	f.portCheckMu.Lock()
	held := len(f.portCheckLocks["de1"].gate)
	f.portCheckMu.Unlock()
	if held != 0 {
		t.Fatalf("the node taken before the cancelled wait is still held (%d)", held)
	}
	hold()
	release, err := f.acquireRelayPortChecks(context.Background(), "msk1", "de1")
	if err != nil {
		t.Fatalf("both nodes are free again: %v", err)
	}
	release()
}

func TestCheckRelayReleasesTheNodesWhenItPanics(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	clock := x.f.now
	x.f.now = func() time.Time { // the first thing CheckRelay reads after it holds both nodes; the agents' goroutines read it too
		if bytes.Contains(debug.Stack(), []byte("(*Fleet).CheckRelay")) {
			panic("boom")
		}
		return clock()
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("CheckRelay did not panic")
			}
		}()
		_, _ = x.f.CheckRelay(x.ctx, "rly_t")
	}()
	x.f.portCheckMu.Lock()
	defer x.f.portCheckMu.Unlock()
	for _, id := range []string{x.entry.nodeID, x.exit.nodeID} {
		if lock := x.f.portCheckLocks[id]; lock == nil || len(lock.gate) != 0 {
			t.Errorf("node %s is still held after the panic", id)
		}
	}
}

func TestCheckRelayReverseReachSaysWhyTheExitDoesNotReachTheEntry(t *testing.T) {
	for _, tc := range []struct{ verdict, code string }{{"timeout", "timeout"}, {"other", "other"}} {
		t.Run(tc.verdict, func(t *testing.T) {
			x := newReverseCheckEnv(t, true, true)
			x.tcpRelay(false)
			resultCh := x.start("rly_t")
			tcpRun(x.ec, "timeout", 8443)
			tcpRun(x.xc, tc.verdict, 443)
			got := result(t, resultCh).ReverseReach
			if got.Status != "fail" || got.Code != tc.code || got.Verdict != tc.verdict || got.Sender != x.exit.nodeID {
				t.Fatalf("reverse reach = %+v", got)
			}
		})
	}
}

func TestCheckRelayKeepsTheReverseStagesOfAnEarlierRun(t *testing.T) {
	x := newReverseCheckEnv(t, true, true)
	x.tcpRelay(false)
	first := x.start("rly_t")
	tcpRun(x.ec, "timeout", 8443)
	tcpRun(x.xc, "ours", 443)
	was := result(t, first).ReverseReach
	if was.Status != "ok" {
		t.Fatalf("first run: %+v", was)
	}
	second := x.start("rly_t") // the leg passes now: the stage does not run, and what it found stays with its time
	tcpRun(x.ec, "ours", 8443)
	got := result(t, second)
	if got.Leg.Status != "ok" || got.ReverseReach != was {
		t.Fatalf("second run: leg %+v, reverse reach %+v, want %+v", got.Leg, got.ReverseReach, was)
	}
	idle(t, 75*time.Millisecond, x.xc)
	stored, err := x.st.RelayLink(x.ctx, "rly_t")
	if err != nil {
		t.Fatal(err)
	}
	if check, ok := stored.Check(); !ok || check.ReverseReach != was {
		t.Fatalf("stored = %+v", check.ReverseReach)
	}
}
