package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func TestCapabilitiesAdvertiseWebSocketLink(t *testing.T) {
	a := &Agent{engines: map[string]engine.Engine{}}
	if !slices.Contains(a.capabilities(), "ws-link/1") {
		t.Fatal("Hello.capabilities does not advertise ws-link/1")
	}
}

func TestConfiguredLinkUsesMTLSWhenPanelDoesNotAdvertiseSupport(t *testing.T) {
	h := newHarness(t, harnessOpts{cfg: func(c *Config) { c.LinkURL = "wss://example.com/abcdefghijklmnop" }})
	h.waitConnected()
	select {
	case <-h.panel.connects:
		t.Fatal("agent reconnected instead of staying on the unadvertised mTLS transport")
	case <-time.After(150 * time.Millisecond):
	}
	if h.a.linkAdvertised.Load() {
		t.Fatal("panel did not advertise link support")
	}
	if h.a.cur.Load() == nil || h.panel.connCount() != 1 {
		t.Fatalf("agent did not keep its mTLS session: active=%v Connects=%d", h.a.cur.Load() != nil, h.panel.connCount())
	}
}

func TestEnrollStoresIdentity(t *testing.T) {
	panel := newFakePanel(t)
	dir := filepath.Join(t.TempDir(), "state")
	cfg := EnrollConfig{StateDir: dir, Panel: panel.addr(), SNI: testSNI, CASHA256: panel.fingerprint(), Token: panel.token}
	meta, err := Enroll(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if meta.NodeID != "nod_test" || meta.Panel != panel.addr() || meta.SNI != testSNI {
		t.Fatalf("meta = %+v", meta)
	}
	id, err := loadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := id.cert.Leaf.URIs[0].String(); got != "mistgate://node/nod_test" {
		t.Errorf("URI SAN = %s", got)
	}
	if runtime.GOOS != "windows" { // POSIX modes mean nothing on Windows
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Errorf("state dir mode %v", fi.Mode().Perm())
		}
		for _, f := range []string{fileIdentity, fileCA, fileMeta} {
			if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v %v", f, fi, err)
			}
		}
	}
	// A second enrollment must not silently replace the identity.
	if _, err := Enroll(context.Background(), cfg); err == nil {
		t.Error("re-enroll without --force succeeded")
	}
}

func TestInterruptedEnrollmentReusesPendingKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	first, firstCSR, err := pendingKeyAndCSR(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	second, secondCSR, err := pendingKeyAndCSR(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.D.Cmp(second.D) != 0 {
		t.Fatal("retry generated a different key after an interrupted enrollment")
	}
	for _, csrDER := range [][]byte{firstCSR, secondCSR} {
		csr, err := x509.ParseCertificateRequest(csrDER)
		if err != nil {
			t.Fatal(err)
		}
		if err := csr.CheckSignature(); err != nil {
			t.Fatalf("retry CSR signature: %v", err)
		}
		if !csr.PublicKey.(*ecdsa.PublicKey).Equal(&first.PublicKey) {
			t.Fatal("retry CSR does not use the persisted key")
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dir, filePendingKey)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("pending key permissions = %v, err %v", info, err)
		}
	}
	forced, _, err := pendingKeyAndCSR(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.D.Cmp(forced.D) == 0 {
		t.Fatal("force did not replace the pending key")
	}
}

func TestEnrollRejectsWrongPinAndToken(t *testing.T) {
	panel := newFakePanel(t)
	base := EnrollConfig{Panel: panel.addr(), SNI: testSNI, CASHA256: panel.fingerprint(), Token: panel.token}

	bad := base
	bad.StateDir = filepath.Join(t.TempDir(), "a")
	fp := panel.fingerprint()
	flip := "0"
	if fp[0] == '0' {
		flip = "1"
	}
	bad.CASHA256 = flip + fp[1:] // a CA we do not trust (always differs from the real pin)
	if _, err := Enroll(context.Background(), bad); err == nil {
		t.Fatal("enrolled against a panel whose CA does not match the pin")
	}
	if _, err := os.Stat(bad.StateDir); err == nil {
		t.Error("state dir created by a failed enrollment")
	}
	if panel.tokenUsed {
		t.Error("the token reached a panel that failed the pin check")
	}

	bad = base
	bad.StateDir = filepath.Join(t.TempDir(), "b")
	bad.CASHA256 = "not hex"
	if _, err := Enroll(context.Background(), bad); err == nil {
		t.Fatal("garbage fingerprint accepted")
	}

	bad = base
	bad.StateDir = filepath.Join(t.TempDir(), "c")
	bad.Token = "wrong"
	if _, err := Enroll(context.Background(), bad); err == nil {
		t.Fatal("wrong token accepted")
	}

	// Wrong SNI: the certificate is for another name, so verification fails even with the right pin.
	bad = base
	bad.StateDir = filepath.Join(t.TempDir(), "d")
	bad.SNI = "other.invalid"
	if _, err := Enroll(context.Background(), bad); err == nil {
		t.Fatal("wrong SNI accepted")
	}
}

func TestNewRequiresEnrollment(t *testing.T) {
	_, err := New(Config{StateDir: t.TempDir()}, nil, &fakeHost{})
	if !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("err = %v", err)
	}
}

func TestHelloAndHelloAck(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var hello *pb.Hello
	select {
	case hello = <-h.panel.hellos:
	case <-time.After(8 * time.Second):
		t.Fatal("no Hello")
	}
	if hello.NodeId != "nod_test" || hello.ApiVersion != 1 || len(hello.InstanceId) != 32 || hello.AgentVersion == "" {
		t.Errorf("hello = %v", hello)
	}
	// seq 1 is the agent_started event of this start, still waiting for its Ack
	if hello.NextSeq != 2 || hello.FirstUnackedSeq != 1 || hello.AppliedRevision != 0 || hello.AppliedStateHash != "" {
		t.Errorf("fresh agent hello = %v", hello)
	}
	if len(hello.Engines) != 1 || hello.Engines[0].Protocol != "fake" || hello.Engines[0].Version != "fake v1" {
		t.Errorf("engines = %v", hello.Engines)
	}
	f := hello.Facts
	if f.Hostname != "de1" || f.CpuCount != 4 || f.RamTotalBytes != 1<<30 || f.Virt != "kvm" || !f.HasIpv6 || f.BootUnix != 1700000000 || f.Kernel != "6.1.0" {
		t.Errorf("facts = %v", f)
	}
	// The agent adopts the node settings of the HelloAck.
	eventually(t, func() bool { return len(h.a.DNS()) == 1 && h.a.DNS()[0] == "192.0.2.53" }, "dns resolvers from HelloAck")
	h.host.mu.Lock()
	defer h.host.mu.Unlock()
	if h.host.baselines != 1 {
		t.Errorf("baseline applied %d times", h.host.baselines)
	}
}

// Reliable messages: seq strictly increasing, resent in order after a reconnect, counted exactly once
// even when the panel re-receives them (it deduplicates on (instance, seq)).
func TestStatsSeqDedupAcrossReconnect(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"))))
	h.panel.nextApply()
	h.eng.setEmit(true)

	eventually(t, func() bool { return h.panel.committedCount() >= 5 }, "first batches committed")
	// The panel stops acking (acks lost) while it keeps committing; the agent must keep the batches.
	h.panel.dropAcks.Store(true)
	before := h.panel.committedCount()
	eventually(t, func() bool { return h.panel.committedCount() >= before+8 }, "more batches committed without acks")
	// The panel "forgets" what it committed (reports acked_seq 0 at the next Hello) and the stream breaks.
	h.panel.ackedOverride.Store(0)
	h.panel.dropConn()
	h.waitConnected()
	h.panel.dropAcks.Store(false)
	eventually(t, func() bool {
		h.panel.mu.Lock()
		defer h.panel.mu.Unlock()
		return h.panel.dups >= 8
	}, "unacked batches resent (and recognised as duplicates)")
	h.panel.ackedOverride.Store(-1)

	// Stop producing, let everything drain, then compare what the engine handed out with what was counted.
	h.eng.setEmit(false)
	up, down := h.eng.emitted()
	eventually(t, func() bool {
		h.panel.mu.Lock()
		defer h.panel.mu.Unlock()
		return h.panel.up["crd_a"] >= up
	}, "all batches counted")
	time.Sleep(100 * time.Millisecond) // anything still in flight would show up as an over-count
	h.panel.mu.Lock()
	defer h.panel.mu.Unlock()
	if h.panel.up["crd_a"] != up || h.panel.down["crd_a"] != down || up == 0 {
		t.Fatalf("panel counted up=%d down=%d, engine emitted up=%d down=%d", h.panel.up["crd_a"], h.panel.down["crd_a"], up, down)
	}
	// seq: committed values strictly increase; arrival order within each stream is ascending.
	for i := 1; i < len(h.panel.committed); i++ {
		if h.panel.committed[i] <= h.panel.committed[i-1] {
			t.Fatalf("committed seqs not increasing: %v", h.panel.committed)
		}
	}
	for ci, c := range h.panel.conns {
		for i := 1; i < len(c.seqs); i++ {
			if c.seqs[i] <= c.seqs[i-1] {
				t.Fatalf("stream %d resent out of order: %v", ci, c.seqs)
			}
		}
	}
	if len(h.panel.conns) < 2 {
		t.Fatal("no reconnect happened")
	}
	// The second Hello told the panel where the queue starts.
	second := h.panel.conns[1].hello
	if second.InstanceId != h.panel.conns[0].hello.InstanceId || second.FirstUnackedSeq == 0 || second.NextSeq <= second.FirstUnackedSeq {
		t.Errorf("second hello: %v", second)
	}
}

func firstUnacked(a *Agent) (first, next uint64) {
	next, first = a.out.hello()
	return
}

func TestHonestHelloAckDropsAckedMessages(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"))))
	h.panel.nextApply()
	h.eng.setEmit(true)
	eventually(t, func() bool { return h.panel.committedCount() >= 6 }, "batches")
	h.panel.dropAcks.Store(true) // commits continue, acks are lost
	eventually(t, func() bool { first, _ := firstUnacked(h.a); return first != 0 }, "unacked queue")
	h.panel.dropConn()
	h.waitConnected()
	h.panel.dropAcks.Store(false)
	h.eng.setEmit(false)
	eventually(t, func() bool { first, _ := firstUnacked(h.a); return first == 0 }, "outbox drained")
	h.panel.mu.Lock()
	defer h.panel.mu.Unlock()
	if h.panel.dups != 0 {
		t.Errorf("agent resent %d messages the panel had already committed", h.panel.dups)
	}
	up, _ := h.eng.emitted()
	if h.panel.up["crd_a"] != up {
		t.Errorf("counted %d, emitted %d", h.panel.up["crd_a"], up)
	}
}

func TestStatsBatchContent(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"))))
	h.panel.nextApply()
	h.eng.setEmit(true)
	eventually(t, func() bool {
		h.panel.mu.Lock()
		defer h.panel.mu.Unlock()
		return len(h.panel.sessions) == 1
	}, "a batch with a session")
	h.panel.mu.Lock()
	defer h.panel.mu.Unlock()
	s := h.panel.sessions[0]
	if s.CredId != "crd_a" || s.InboundId != "inb_1" || s.RemoteIp != "203.0.113.7" || s.ConnectedAtUnix < 1700000000 {
		t.Errorf("session = %v", s)
	}
}

// independent expectation: built from plugin types by hand, not through the agent's own merge code.
func wantInbound(creds ...plugin.UserCred) statehash.Inbound {
	return statehash.Inbound{
		Spec: plugin.InboundSpec{
			ID: "inb_1", Protocol: "fake", ProfileID: "prf_1", Version: 1, Enabled: true,
			Listen: plugin.Listen{Network: "udp", Port: 443},
			TLS:    plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "example.com"}, Egress: "direct",
			Settings: json.RawMessage(`{"obfs":"x"}`),
		},
		Creds: creds,
	}
}

func wantCred(id string) plugin.UserCred {
	return plugin.UserCred{CredID: id, UserID: "usr_" + id, DeviceID: "dev_" + id, Data: json.RawMessage(`{"auth_sha256":"` + id + `"}`)}
}

func TestDesiredStateFullDeltaAndHash(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()

	// 1. full state
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"), cred("crd_b"))))
	r := h.panel.nextApply()
	if r.Revision != 1 || r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || len(r.Inbounds) != 1 {
		t.Fatalf("full: %v", r)
	}
	if want := statehash.State([]statehash.Inbound{wantInbound(wantCred("crd_a"), wantCred("crd_b"))}); r.StateHash != want {
		t.Fatalf("hash %s, want %s", r.StateHash, want)
	}
	ir := r.Inbounds[0]
	if ir.InboundId != "inb_1" || ir.CredCount != 2 || ir.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING || ir.Restarted ||
		ir.CertPinSha256 != "aainb_1" || ir.CertNotAfterUnix != 2000000000 || ir.SpecHash != statehash.Spec(wantInbound().Spec) {
		t.Fatalf("inbound result: %v", ir)
	}

	// 2. delta: add one credential, remove another; the engine gets the WHOLE merged set, no restart
	h.panel.push(&pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{
		{InboundId: "inb_1", Creds: []*pb.Credential{cred("crd_c")}, RemovedCredIds: []string{"crd_a"}},
	}})
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Revision != 2 || r.Inbounds[0].Restarted || r.Inbounds[0].CredCount != 2 {
		t.Fatalf("delta: %v", r)
	}
	if want := statehash.State([]statehash.Inbound{wantInbound(wantCred("crd_b"), wantCred("crd_c"))}); r.StateHash != want {
		t.Fatalf("hash after delta %s, want %s", r.StateHash, want)
	}
	if got := h.eng.credIDs("inb_1"); len(got) != 2 || got[0] != "crd_b" || got[1] != "crd_c" {
		t.Fatalf("engine creds %v", got)
	}

	// 3. delta on the wrong base: nothing changes, BASE_MISMATCH, hash of what actually runs
	h.panel.push(&pb.DesiredState{Revision: 9, BaseRevision: 8, Inbounds: []*pb.InboundState{
		{InboundId: "inb_1", Creds: []*pb.Credential{cred("crd_z")}},
	}})
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_BASE_MISMATCH || r.Revision != 9 || r.Error == "" {
		t.Fatalf("mismatch: %v", r)
	}
	if want := statehash.State([]statehash.Inbound{wantInbound(wantCred("crd_b"), wantCred("crd_c"))}); r.StateHash != want {
		t.Fatalf("mismatch must report the running state")
	}
	if got := h.eng.credIDs("inb_1"); len(got) != 2 {
		t.Fatalf("engine changed by a mismatched delta: %v", got)
	}

	// 4. the panel answers with a full state, which the agent applies
	h.panel.push(fullState(10, inb("inb_1", 0, 0, cred("crd_b"), cred("crd_c"), cred("crd_z"))))
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Revision != 10 || r.Inbounds[0].CredCount != 3 {
		t.Fatalf("resent full: %v", r)
	}
	if want := statehash.State([]statehash.Inbound{wantInbound(wantCred("crd_b"), wantCred("crd_c"), wantCred("crd_z"))}); r.StateHash != want {
		t.Fatalf("hash after resend %s, want %s", r.StateHash, want)
	}

	// 5. a spec change restarts (and only that inbound); users-only changes did not
	applies := h.eng.applyCount("inb_1")
	changed := inb("inb_1", 0, 0)
	changed.Spec.SpecVersion = 2
	changed.CredsReplace = false
	h.panel.push(&pb.DesiredState{Revision: 11, BaseRevision: 10, Inbounds: []*pb.InboundState{changed}})
	r = h.panel.nextApply()
	if !r.Inbounds[0].Restarted || r.Inbounds[0].CredCount != 3 || h.eng.applyCount("inb_1") != applies+1 {
		t.Fatalf("spec change: %v", r)
	}

	// 6. a stale delta (revision below the applied one) is ignored silently; re-delivery of the applied revision is idempotent
	h.panel.push(&pb.DesiredState{Revision: 5, BaseRevision: 4})
	h.panel.push(&pb.DesiredState{Revision: 11, BaseRevision: 10, Inbounds: []*pb.InboundState{changed}})
	r = h.panel.nextApply()
	if r.Revision != 11 || r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("redelivery: %v", r)
	}
	select {
	case extra := <-h.panel.applies:
		t.Fatalf("stale delta answered: %v", extra)
	case <-time.After(150 * time.Millisecond):
	}

	// 7. rejected: unknown protocol. Nothing changes.
	bad := inb("inb_2", 0, 0, cred("crd_q"))
	bad.Spec.Protocol = "nosuch"
	h.panel.push(&pb.DesiredState{Revision: 12, BaseRevision: 11, Inbounds: []*pb.InboundState{bad}})
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_REJECTED || r.Error == "" || h.eng.has("inb_2") {
		t.Fatalf("reject: %v", r)
	}

	// 8. a full state without inb_1 removes it
	h.panel.push(fullState(13))
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || len(r.Inbounds) != 0 || h.eng.has("inb_1") {
		t.Fatalf("removal: %v", r)
	}
	if r.StateHash != statehash.State(nil) {
		t.Fatalf("empty state hash")
	}
	if !h.panel.hasEvent("engine_started") {
		eventually(t, func() bool { return h.panel.hasEvent("engine_started") }, "engine_started event")
	}
}

func TestPartialApplyKeepsOtherInbounds(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.eng.mu.Lock()
	h.eng.applyErr["inb_bad"] = errBoom
	h.eng.mu.Unlock()
	h.panel.push(fullState(1, inb("inb_good", 0, 0, cred("crd_a")), inb("inb_bad", 0, 0, cred("crd_b"))))
	r := h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || len(r.Inbounds) != 2 {
		t.Fatalf("partial: %v", r)
	}
	for _, ir := range r.Inbounds {
		switch ir.InboundId {
		case "inb_good":
			if ir.Error != "" || ir.State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
				t.Errorf("good: %v", ir)
			}
		case "inb_bad":
			if ir.Error != errBoom.Error() || ir.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED {
				t.Errorf("bad: %v", ir)
			}
		}
	}
	eventually(t, func() bool { return h.panel.hasEvent("engine_failed") }, "engine_failed event")

	// The failed inbound is retried by the next apply, and becomes healthy.
	h.eng.mu.Lock()
	delete(h.eng.applyErr, "inb_bad")
	h.eng.mu.Unlock()
	h.panel.push(&pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{{InboundId: "inb_good", Creds: []*pb.Credential{cred("crd_c")}}}})
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || !h.eng.has("inb_bad") {
		t.Fatalf("retry: %v", r)
	}
}

func TestUnchangedInboundsAreNotReapplied(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a")), inb("inb_2", 0, 0, cred("crd_b"))))
	h.panel.nextApply()
	h.panel.push(&pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{{InboundId: "inb_1", Creds: []*pb.Credential{cred("crd_c")}}}})
	h.panel.nextApply()
	if h.eng.applyCount("inb_1") != 2 || h.eng.applyCount("inb_2") != 1 {
		t.Fatalf("applies inb_1=%d inb_2=%d", h.eng.applyCount("inb_1"), h.eng.applyCount("inb_2"))
	}
}

func TestPortHops(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 20000, 29999, cred("crd_a")), inb("inb_2", 0, 0, cred("crd_b"))))
	h.panel.nextApply()
	calls := h.host.hops()
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != (hostctl.Hop{InboundID: "inb_1", Network: "udp", From: 20000, To: 29999, Port: 443}) {
		t.Fatalf("hop calls: %+v", calls)
	}
	// A users-only delta leaves the firewall alone.
	h.panel.push(&pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{{InboundId: "inb_2", Creds: []*pb.Credential{cred("crd_c")}}}})
	h.panel.nextApply()
	if got := h.host.hops(); len(got) != 1 {
		t.Fatalf("unchanged hops touched the firewall again: %+v", got)
	}
	// Removing the inbound removes the redirect.
	h.panel.push(&pb.DesiredState{Revision: 3, BaseRevision: 2, RemovedInboundIds: []string{"inb_1"}})
	h.panel.nextApply()
	calls = h.host.hops()
	if len(calls) != 2 || len(calls[1]) != 0 {
		t.Fatalf("hop calls after removal: %+v", calls)
	}
}

func TestPortHopFailureIsReported(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.host.hopErr = errors.New("nft is not installed")
	h.panel.push(fullState(1, inb("inb_1", 20000, 29999, cred("crd_a"))))
	r := h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || r.Inbounds[0].Error == "" {
		t.Fatalf("hop failure: %v", r)
	}
	eventually(t, func() bool { return h.panel.hasEvent("hop_failed") }, "hop_failed event")
}

func TestKickAndRestartCommands(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"))))
	h.panel.nextApply()

	h.eng.kickRet = 2
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Kick{Kick: &pb.Kick{RequestId: "req_1", CredIds: []string{"crd_a", "crd_b"}, Reason: "test"}}})
	r := h.panel.nextCmd()
	if r.RequestId != "req_1" || !r.Ok || r.Affected != 2 || r.Error != "" {
		t.Fatalf("kick: %v", r)
	}
	if len(h.eng.kicked) != 1 || len(h.eng.kicked[0]) != 2 || h.eng.kicked[0][0] != "crd_a" {
		t.Fatalf("engine kicked %v", h.eng.kicked)
	}

	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RestartInbound{RestartInbound: &pb.RestartInbound{RequestId: "req_2", InboundId: "inb_1"}}})
	r = h.panel.nextCmd()
	if r.RequestId != "req_2" || !r.Ok || r.Affected != 1 {
		t.Fatalf("restart: %v", r)
	}
	if len(h.eng.removed) != 1 || h.eng.applyCount("inb_1") != 2 || !h.eng.has("inb_1") {
		t.Fatalf("restart did not remove+apply: removed=%v applies=%d", h.eng.removed, h.eng.applyCount("inb_1"))
	}
	eventually(t, func() bool { return h.panel.hasEvent("engine_restarted") }, "engine_restarted event")

	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RestartInbound{RestartInbound: &pb.RestartInbound{RequestId: "req_3", InboundId: "inb_nope"}}})
	if r = h.panel.nextCmd(); r.RequestId != "req_3" || r.Ok || r.Error == "" {
		t.Fatalf("restart unknown: %v", r)
	}

	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Ping{Ping: &pb.Ping{Nonce: 7}}}) // answered with Pong, must not break anything
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RestartInbound{RestartInbound: &pb.RestartInbound{RequestId: "req_4"}}})
	if r = h.panel.nextCmd(); r.RequestId != "req_4" || !r.Ok || r.Affected != 1 {
		t.Fatalf("restart all: %v", r)
	}
}

func TestPersistedStateRestoresWithoutPanel(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(7, inb("inb_1", 20000, 29999, cred("crd_a"), cred("crd_b"))))
	want := h.panel.nextApply()
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if !h.eng.closed {
		t.Error("engines not closed on a normal stop")
	}
	if h.host.cleaned {
		t.Error("a normal stop must leave the host baseline in place")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(h.dir, fileState)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("state file: %v %v", fi, err)
		}
	}

	// A new agent process on the same state dir, with the panel gone: engines must come back by themselves.
	h.panel.srv.Close()
	eng2 := newFakeEngine("fake")
	host2 := &fakeHost{}
	a2, err := New(Config{StateDir: h.dir}, map[string]engine.Factory{"fake": eng2.factory()}, host2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	a2.backoffMin, a2.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	go func() { done <- a2.Run(ctx) }()
	// restore sets the model after reconcile: wait for the whole restore, not just the engine (flaked under load)
	eventually(t, func() bool { return len(eng2.credIDs("inb_1")) == 2 && a2.snapshotModel().revision == 7 }, "state restored from disk")
	if calls := host2.hops(); len(calls) == 0 || len(calls[len(calls)-1]) != 1 {
		t.Errorf("hop redirect not restored: %+v", calls)
	}
	hello := a2.hello(ctx).GetHello()
	if hello.AppliedRevision != 7 || hello.AppliedStateHash != want.StateHash {
		t.Errorf("hello after restart: rev=%d hash=%s want %s", hello.AppliedRevision, hello.AppliedStateHash, want.StateHash)
	}
	cancel()
	<-done
}

func TestCredentialTermEndsByItself(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	short := cred("crd_short")
	short.ValidUntilUnix = time.Now().Add(2500 * time.Millisecond).Unix() // valid for at least 1.5 s
	ds := fullState(1, inb("inb_1", 0, 0, cred("crd_long"), short))
	h.panel.push(ds)
	r := h.panel.nextApply()
	if r.Inbounds[0].CredCount != 2 {
		t.Fatalf("both credentials are valid at first: %v", r)
	}
	// The agent drops the expired credential without any message from the panel.
	eventually(t, func() bool { return len(h.eng.credIDs("inb_1")) == 1 }, "expired credential dropped")
	if got := h.eng.credIDs("inb_1"); got[0] != "crd_long" {
		t.Fatalf("kept %v", got)
	}
	eventually(t, func() bool { return h.panel.hasEvent("credential_expired") }, "credential_expired event")
	// Not reported as drift: the state hash still equals the panel's (which lists the expired credential).
	hello := h.a.hello(context.Background()).GetHello()
	if hello.AppliedStateHash != expectedHash(t, ds) {
		t.Errorf("withheld expired credential shows up as drift")
	}
}

func TestRetire(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 20000, 29999, cred("crd_a"))))
	h.panel.nextApply()

	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Retire{Retire: &pb.Retire{RequestId: "req_r"}}})
	r := h.panel.nextCmd()
	if r.RequestId != "req_r" || !r.Ok {
		t.Fatalf("retire result: %v", r)
	}
	select {
	case err := <-h.done:
		if !errors.Is(err, ErrRetired) {
			t.Fatalf("Run returned %v", err)
		}
		h.cancel = nil
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not return after Retire")
	}
	if !h.eng.closed || !h.host.cleaned {
		t.Errorf("engines closed=%v host cleaned=%v", h.eng.closed, h.host.cleaned)
	}
	if _, err := os.Stat(filepath.Join(h.dir, fileIdentity)); err == nil {
		t.Error("key material survived Retire")
	}
	if es, _ := os.ReadDir(h.dir); len(es) != 0 {
		t.Errorf("state dir not emptied: %v", es)
	}
}

func TestWipeStateRefusesForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "precious"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := wipeState(dir); err == nil {
		t.Fatal("wiped a directory that is not an agent state dir")
	}
	if _, err := os.Stat(filepath.Join(dir, "precious")); err != nil {
		t.Fatal("foreign file deleted")
	}
}

func TestCertificateRenewal(t *testing.T) {
	// The panel issues a certificate with 5 days left, so the agent renews immediately.
	h := newHarness(t, harnessOpts{certValidity: 5 * 24 * time.Hour})
	h.waitConnected()
	eventually(t, func() bool {
		h.panel.mu.Lock()
		defer h.panel.mu.Unlock()
		return h.panel.renewCalls >= 1
	}, "Renew call")
	eventually(t, func() bool { return time.Until(h.a.id.Load().notAfter()) > 25*24*time.Hour }, "new certificate in use")
	id, err := loadIdentity(h.dir)
	if err != nil || time.Until(id.notAfter()) < 25*24*time.Hour {
		t.Fatalf("renewed identity not persisted: %v", err)
	}
	eventually(t, func() bool { return h.panel.hasEvent("cert_renewed") }, "cert_renewed event")
	// The new certificate works for the next connection.
	before := h.panel.connCount()
	h.panel.dropConn()
	eventually(t, func() bool { return h.panel.connCount() > before }, "reconnect with the renewed certificate")
	h.panel.mu.Lock()
	renews := h.panel.renewCalls
	h.panel.mu.Unlock()
	if renews != 1 {
		t.Errorf("renewed %d times; a fresh certificate must not renew again", renews)
	}
}

func TestClockSkewEvent(t *testing.T) {
	panel := newFakePanel(t)
	panel.serverSkew.Store(120)
	h := &harness{t: t, panel: panel, eng: newFakeEngine("fake"), host: &fakeHost{}, dir: t.TempDir() + "/state"}
	if _, err := Enroll(context.Background(), EnrollConfig{StateDir: h.dir, Panel: panel.addr(), SNI: testSNI, CASHA256: panel.fingerprint(), Token: panel.token}); err != nil {
		t.Fatal(err)
	}
	h.a = h.newAgent()
	h.start()
	eventually(t, func() bool { return panel.hasEvent("clock_skew") }, "clock_skew event")
	if off := h.a.offset.Load(); off < 118 || off > 122 {
		t.Errorf("offset = %d", off)
	}
}

func TestNoHelloAckIsAnError(t *testing.T) {
	// A server that accepts the stream and never answers must not hold the agent: the hello timeout fires.
	h := newHarness(t, harnessOpts{noStart: true})
	h.a.helloTimeout = 50 * time.Millisecond
	h.panel.blackhole.Store(true)
	h.start()
	time.Sleep(300 * time.Millisecond) // several attempts, none hangs
	if h.panel.connCount() != 0 {
		t.Fatal("the stream was accepted")
	}
	h.panel.blackhole.Store(false)
	h.waitConnected() // and the loop is still alive: it connects as soon as the panel answers
}
