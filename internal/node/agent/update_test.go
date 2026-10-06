package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/update"
	"github.com/mistgate/mistgate/internal/release"
)

const (
	oldBuilt = 1000
	newBuilt = 2000
	binName  = "mistgate-node-linux-amd64"
)

var (
	oldBin = []byte("old agent build")
	newBin = append([]byte("new agent build "), make([]byte, 4500)...)
)

// FetchUpdate is the fake panel's half of the download: the named file of p.bundle from the asked offset, in small chunks.
func (p *fakePanel) FetchUpdate(_ context.Context, req *connect.Request[pb.FetchUpdateRequest], stream *connect.ServerStream[pb.FetchUpdateResponse]) error {
	p.mu.Lock()
	p.fetches = append(p.fetches, req.Msg.Offset)
	data, ok := p.bundle[req.Msg.Name]
	busy := p.fetchBusy > 0
	if busy {
		p.fetchBusy--
	}
	p.mu.Unlock()
	switch {
	case busy:
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many downloads"))
	case !ok || req.Msg.Offset > uint64(len(data)):
		return connect.NewError(connect.CodeNotFound, errors.New("no such file"))
	}
	for off := int(req.Msg.Offset); off < len(data); off += 1000 {
		end := min(off+1000, len(data))
		if err := stream.Send(&pb.FetchUpdateResponse{Data: data[off:end], TotalSize: uint64(len(data))}); err != nil {
			return err
		}
	}
	return nil
}

// updFix is one node's binary on disk plus the owner's key.
type updFix struct {
	t     *testing.T
	exe   string
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	execs chan string
	panel *fakePanel // for the ordering check at exec time
	sent  atomic.Int32
}

func newUpdFix(t *testing.T) *updFix {
	t.Helper()
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	f := &updFix{t: t, exe: filepath.Join(t.TempDir(), "mistgate-node"), pub: pub, priv: priv, execs: make(chan string, 8)}
	if err := os.WriteFile(f.exe, oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// cfg returns the Config mutation of an agent that is build `built` of this binary.
func (f *updFix) cfg(built int64, version string, mut func(*update.Config)) func(*Config) {
	return func(c *Config) {
		c.Built, c.Version = built, version
		uc := update.Config{
			StateDir: c.StateDir, ExePath: f.exe, Version: version, Built: built, Key: f.pub, UnitGen: 2, GOOS: "linux", GOARCH: "amd64",
			Backoff: []time.Duration{time.Millisecond}, RetryWait: time.Millisecond, Args: []string{f.exe, "run"},
			Exec: func(path string, _, _ []string) error {
				if f.panel != nil {
					f.sent.Store(int32(f.panel.cmdsSeen.Load())) // how many CommandResults the panel had when the exec happened
				}
				f.execs <- path
				return nil
			},
		}
		if mut != nil {
			mut(&uc)
		}
		c.Updater = update.New(uc)
	}
}

func (f *updFix) manifest(content []byte, built int64) (man, sig []byte) {
	f.t.Helper()
	sum := sha256.Sum256(content)
	m := &release.Manifest{
		Schema: 1, Version: "0.2.0-new", Built: built, Expires: time.Now().Unix() + 3600,
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: binName, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}},
	}
	b, err := m.Marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	return b, release.Sign(f.priv, b)
}

func (f *updFix) waitExec() string {
	f.t.Helper()
	select {
	case p := <-f.execs:
		return p
	case <-time.After(8 * time.Second):
		f.t.Fatal("the agent never re-executed")
		return ""
	}
}

func (f *updFix) noExec(d time.Duration) {
	f.t.Helper()
	select {
	case p := <-f.execs:
		f.t.Fatalf("unexpected re-exec of %s", p)
	case <-time.After(d):
	}
}

func fast(a *Agent) { a.commitTick, a.commitSettle = 10*time.Millisecond, 30*time.Millisecond }

func (h *harness) nextHello() *pb.Hello {
	h.t.Helper()
	select {
	case x := <-h.panel.hellos:
		return x
	case <-time.After(8 * time.Second):
		h.t.Fatal("no Hello")
		return nil
	}
}

func (h *harness) drainHellos() {
	for {
		select {
		case <-h.panel.hellos:
		default:
			return
		}
	}
}

func sendUpdate(p *fakePanel, man, sig []byte) {
	p.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_UpdateAgent{UpdateAgent: &pb.UpdateAgent{RequestId: "upd-1", Manifest: man, Signature: sig}}})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestHelloCarriesUpdateFields(t *testing.T) {
	t.Run("signed build on a writable, guarded install", func(t *testing.T) {
		f := newUpdFix(t)
		h := newHarness(t, harnessOpts{cfg: f.cfg(oldBuilt, "0.1.0-old", nil)})
		hello := h.nextHello()
		if hello.Built != oldBuilt || hello.AgentVersion != "0.1.0-old" || hello.LastUpdate != nil {
			t.Errorf("hello = %v", hello)
		}
		if !contains(hello.Capabilities, "doctor/1") || !contains(hello.Capabilities, "update/1") || !contains(hello.Capabilities, "update-guard/1") {
			t.Errorf("capabilities = %v", hello.Capabilities)
		}
	})
	t.Run("generation 1 unit: the directory is read-only, so no update capability", func(t *testing.T) {
		f := newUpdFix(t)
		h := newHarness(t, harnessOpts{cfg: f.cfg(oldBuilt, "0.1.0-old", func(c *update.Config) {
			c.ExePath = filepath.Join(filepath.Dir(f.exe), "no-such-dir", "mistgate-node")
		})})
		if caps := h.nextHello().Capabilities; contains(caps, "update/1") || contains(caps, "update-guard/1") {
			t.Errorf("capabilities = %v", caps)
		}
	})
	t.Run("unsigned build", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		hello := h.nextHello()
		if contains(hello.Capabilities, "update/1") || hello.Built != 0 {
			t.Errorf("hello = %v", hello)
		}
		// Even so a stray UpdateAgent is answered, not ignored.
		h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_UpdateAgent{UpdateAgent: &pb.UpdateAgent{RequestId: "x"}}})
		if r := h.panel.nextCmd(); r.Ok || r.Error != "unsigned_build" || r.RequestId != "x" {
			t.Errorf("result = %v", r)
		}
		h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RollbackAgent{RollbackAgent: &pb.RollbackAgent{RequestId: "y"}}})
		if r := h.panel.nextCmd(); r.Ok || r.Error != "unsigned_build" || r.RequestId != "y" {
			t.Errorf("result = %v", r)
		}
	})
}

// TestUpdateOverTheWire is the whole story with the fake panel: UpdateAgent, download, swap, result before the exec, drain,
// "exec", then the new build connects, commits and reports, and the outcome stops being repeated once it was acked.
func TestUpdateOverTheWire(t *testing.T) {
	f := newUpdFix(t)
	h := newHarness(t, harnessOpts{noStart: true, cfg: f.cfg(oldBuilt, "0.1.0-old", nil)})
	f.panel = h.panel
	fast(h.a)
	h.start()
	h.nextHello()
	man, sig := f.manifest(newBin, newBuilt)
	h.panel.mu.Lock()
	h.panel.bundle = map[string][]byte{binName: newBin}
	h.panel.fetchBusy = 1 // the first download attempt finds the panel busy and waits
	h.panel.mu.Unlock()

	sendUpdate(h.panel, man, sig)
	res := h.panel.nextCmd()
	if !res.Ok || res.RequestId != "upd-1" || res.Affected != 1 || res.Params["to_built"] != "2000" {
		t.Fatalf("result = %v", res)
	}
	if got := f.waitExec(); got != f.exe {
		t.Errorf("exec of %s", got)
	}
	if f.sent.Load() != 1 {
		t.Error("the CommandResult must reach the panel before the process is replaced")
	}
	if b, _ := os.ReadFile(f.exe); string(b) != string(newBin) {
		t.Error("executable is not the downloaded build")
	}
	if b, _ := os.ReadFile(f.exe + ".prev"); string(b) != string(oldBin) {
		t.Error(".prev is not the old build")
	}
	h.panel.mu.Lock()
	offsets := append([]uint64(nil), h.panel.fetches...)
	h.panel.mu.Unlock()
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 0 { // busy once, then a full download
		t.Errorf("fetch offsets = %v", offsets)
	}
	h.eng.mu.Lock()
	closed := h.eng.closed
	h.eng.mu.Unlock()
	if !closed {
		t.Error("engines were not drained before the exec")
	}
	h.stop()
	h.drainHellos()

	// --- the new process: same state dir, built 2000
	h.cfgMut = f.cfg(newBuilt, "0.2.0-new", nil)
	h.eng = newFakeEngine("fake")
	h.a = h.newAgent()
	fast(h.a)
	h.start()
	hello := h.nextHello()
	if hello.Built != newBuilt || hello.AgentVersion != "0.2.0-new" || hello.LastUpdate != nil {
		t.Errorf("new build's hello = %v", hello)
	}
	if _, err := os.Stat(filepath.Join(h.dir, update.FilePending)); err != nil {
		t.Fatal("the marker must exist until the commit")
	}
	eventually(t, func() bool { return h.panel.hasEvent("update_committed") }, "update_committed event")
	h.panel.mu.Lock()
	for _, e := range h.panel.events {
		if e.Code == "update_committed" && (e.Params["to_built"] != strconv.FormatInt(newBuilt, 10) || e.Params["from_built"] != strconv.FormatInt(oldBuilt, 10)) {
			t.Errorf("update_committed params = %v, the panel needs the builds to tell this commit from an earlier one", e.Params)
		}
	}
	h.panel.mu.Unlock()
	if _, err := os.Stat(filepath.Join(h.dir, update.FilePending)); err == nil {
		t.Error("marker still there after the commit")
	}
	f.noExec(50 * time.Millisecond)

	// The outcome rides the next Hello until a HelloAck for it arrives, then it is forgotten.
	h.panel.dropConn()
	hello = h.nextHello()
	lu := hello.LastUpdate
	if lu == nil || lu.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_OK || lu.FromVersion != "0.1.0-old" || lu.ToVersion != "0.2.0-new" || lu.ToBuilt != newBuilt || lu.FromBuilt != oldBuilt {
		t.Fatalf("last_update = %v", lu)
	}
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(h.dir, update.FileOutcome)); return err != nil }, "outcome file removed after the ack")
	h.panel.dropConn()
	if hello := h.nextHello(); hello.LastUpdate != nil {
		t.Errorf("an acked outcome was reported again: %v", hello.LastUpdate)
	}
	// .prev stays for a manual rollback.
	if b, _ := os.ReadFile(f.exe + ".prev"); string(b) != string(oldBin) {
		t.Error(".prev must survive the commit")
	}
}

func TestUpdateRefusedOverTheWire(t *testing.T) {
	f := newUpdFix(t)
	h := newHarness(t, harnessOpts{cfg: f.cfg(oldBuilt, "0.1.0-old", nil)})
	h.nextHello()
	man, sig := f.manifest(newBin, newBuilt)
	sig[0] ^= 1
	sendUpdate(h.panel, man, sig)
	if r := h.panel.nextCmd(); r.Ok || r.Error != "bad_signature" || r.RequestId != "upd-1" {
		t.Fatalf("result = %v", r)
	}
	// Missing file on the panel: not_found is permanent, answered as download_failed, binary untouched.
	man, sig = f.manifest(newBin, newBuilt)
	sendUpdate(h.panel, man, sig)
	if r := h.panel.nextCmd(); r.Ok || r.Error != "download_failed" {
		t.Fatalf("result = %v", r)
	}
	f.noExec(50 * time.Millisecond)
	if b, _ := os.ReadFile(f.exe); string(b) != string(oldBin) {
		t.Error("a refused update changed the binary")
	}
	if _, err := os.Stat(f.exe + ".new"); err == nil {
		t.Error(".new left behind")
	}
}

// A new build that never gets to commit (here: the panel never answers) rolls itself back when the window closes, and the
// old build, started again, reports it in Hello and as an event.
func TestNewBuildThatDoesNotCommitRollsBack(t *testing.T) {
	f := newUpdFix(t)
	if err := os.WriteFile(f.exe, newBin, 0o755); err != nil { // the new build is what sits on disk and runs
		t.Fatal(err)
	}
	if err := os.WriteFile(f.exe+".prev", oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now() // the window starts when the updater is made, inside newHarness
	h := newHarness(t, harnessOpts{noStart: true, cfg: f.cfg(newBuilt, "0.2.0-new", func(c *update.Config) { c.Window = 250 * time.Millisecond })})
	marker := `{"from_version":"0.1.0-old","from_built":1000,"to_version":"0.2.0-new","to_built":2000,"started_unix":1}`
	if err := os.WriteFile(filepath.Join(h.dir, update.FilePending), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	h.panel.blackhole.Store(true) // connects, never gets a HelloAck
	fast(h.a)
	h.start()
	f.waitExec()
	if time.Since(start) < 200*time.Millisecond {
		t.Error("rolled back before the window closed")
	}
	if b, _ := os.ReadFile(f.exe); string(b) != string(oldBin) {
		t.Error("previous build not restored")
	}
	if _, err := os.Stat(filepath.Join(h.dir, update.FilePending)); err == nil {
		t.Error("marker left")
	}
	h.stop()

	// --- the old build comes back
	h.panel.blackhole.Store(false)
	h.cfgMut = f.cfg(oldBuilt, "0.1.0-old", nil)
	h.a = h.newAgent()
	fast(h.a)
	h.start()
	hello := h.nextHello()
	lu := hello.LastUpdate
	if lu == nil || lu.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || lu.Reason != "not_committed" || lu.ToVersion != "0.2.0-new" || lu.FromVersion != "0.1.0-old" {
		t.Fatalf("last_update = %v", lu)
	}
	eventually(t, func() bool { return h.panel.hasEvent("update_rolled_back") }, "update_rolled_back event")
	h.panel.mu.Lock()
	defer h.panel.mu.Unlock()
	for _, e := range h.panel.events {
		if e.Code == "update_rolled_back" && (e.Params["reason"] != "not_committed" || e.Params["to_version"] != "0.2.0-new") {
			t.Errorf("event = %v", e)
		}
	}
}

// Commit needs the stream up, the desired state applied and no inbound FAILED that was not FAILED before the update.
func TestCommitWaitsForAppliedStateWithoutNewFailures(t *testing.T) {
	run := func(t *testing.T, preFailed string, wantCommit bool) {
		f := newUpdFix(t)
		_ = os.WriteFile(f.exe, newBin, 0o755)
		_ = os.WriteFile(f.exe+".prev", oldBin, 0o755)
		h := newHarness(t, harnessOpts{noStart: true, cfg: f.cfg(newBuilt, "0.2.0-new", func(c *update.Config) { c.Window = 1500 * time.Millisecond })})
		marker := `{"from_version":"0.1.0-old","from_built":1000,"to_version":"0.2.0-new","to_built":2000,"started_unix":1` + preFailed + `}`
		_ = os.WriteFile(filepath.Join(h.dir, update.FilePending), []byte(marker), 0o600)
		h.eng.unhealthy = map[string]bool{"inb_1": true}
		h.a.commitTick, h.a.commitSettle = 10*time.Millisecond, 200*time.Millisecond // the state push must land first
		h.start()
		h.nextHello()
		h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("c1"))))
		h.panel.nextApply()
		if wantCommit {
			eventually(t, func() bool { return h.panel.hasEvent("update_committed") }, "commit despite the FAILED inbound that predates the update")
			f.noExec(100 * time.Millisecond)
			return
		}
		f.waitExec() // apply_failed
		if b, _ := os.ReadFile(f.exe); string(b) != string(oldBin) {
			t.Error("not rolled back")
		}
		h.stop()
		h.cfgMut = f.cfg(oldBuilt, "0.1.0-old", nil)
		h.eng = newFakeEngine("fake")
		h.a = h.newAgent()
		h.drainHellos()
		h.start()
		if lu := h.nextHello().LastUpdate; lu == nil || lu.Reason != "apply_failed" || lu.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK {
			t.Errorf("last_update = %v", lu)
		}
	}
	t.Run("a new FAILED inbound rolls the update back", func(t *testing.T) { run(t, "", false) })
	t.Run("a FAILED inbound from before the update is not its fault", func(t *testing.T) {
		run(t, `,"pre_failed":["inb_1"]`, true)
	})
}

func TestRollbackAgentOverTheWire(t *testing.T) {
	f := newUpdFix(t)
	h := newHarness(t, harnessOpts{noStart: true, cfg: f.cfg(newBuilt, "0.2.0-new", nil)})
	f.panel = h.panel
	h.start()
	h.nextHello()
	rb := &pb.ConnectResponse{Message: &pb.ConnectResponse_RollbackAgent{RollbackAgent: &pb.RollbackAgent{RequestId: "rb-1"}}}

	h.panel.send(rb) // nothing to go back to
	if r := h.panel.nextCmd(); r.Ok || r.Error != "no_previous" || r.RequestId != "rb-1" {
		t.Fatalf("result = %v", r)
	}
	f.noExec(50 * time.Millisecond)

	if err := os.WriteFile(f.exe+".prev", oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	h.panel.send(rb)
	if r := h.panel.nextCmd(); !r.Ok || r.RequestId != "rb-1" {
		t.Fatalf("result = %v", r)
	}
	f.waitExec()
	// two results so far: the refusal above and this one
	if f.sent.Load() != 2 {
		t.Error("the CommandResult must reach the panel before the process is replaced")
	}
	if b, _ := os.ReadFile(f.exe); string(b) != string(oldBin) {
		t.Error("previous build not restored")
	}
	h.stop()
	h.drainHellos()

	// The restored build reports who rolled what back.
	h.cfgMut = f.cfg(oldBuilt, "0.1.0-old", nil)
	h.eng = newFakeEngine("fake")
	h.a = h.newAgent()
	h.start()
	lu := h.nextHello().LastUpdate
	if lu == nil || lu.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || lu.Reason != "manual" || lu.FromVersion != "0.1.0-old" || lu.ToVersion != "0.2.0-new" {
		t.Fatalf("last_update = %v", lu)
	}
}

// A failed exec (for instance a binary for another architecture) must not leave a half-updated node: the old binary is back
// and Run ends with an error so the supervisor restarts the node.
func TestFailedExecEndsTheAgentWithTheOldBinaryRestored(t *testing.T) {
	f := newUpdFix(t)
	h := newHarness(t, harnessOpts{noStart: true, cfg: f.cfg(oldBuilt, "0.1.0-old", func(c *update.Config) {
		c.Exec = func(string, []string, []string) error { return errors.New("exec format error") }
	})})
	h.start()
	h.nextHello()
	man, sig := f.manifest(newBin, newBuilt)
	h.panel.mu.Lock()
	h.panel.bundle = map[string][]byte{binName: newBin}
	h.panel.mu.Unlock()
	sendUpdate(h.panel, man, sig)
	if r := h.panel.nextCmd(); !r.Ok {
		t.Fatalf("result = %v", r)
	}
	select {
	case err := <-h.done:
		if err == nil || !errors.Is(err, errExec) {
			t.Fatalf("Run returned %v", err)
		}
		h.cancel = nil
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not end after a failed exec")
	}
	if b, _ := os.ReadFile(f.exe); string(b) != string(oldBin) {
		t.Error("old binary not restored")
	}
	if _, err := os.Stat(filepath.Join(h.dir, update.FilePending)); err == nil {
		t.Error("marker left")
	}
	// The failure is either still on disk for the next Hello or, when the agent managed to reconnect before it ended,
	// already delivered (and acked) in one.
	delivered := false
	for more := true; more; {
		select {
		case hello := <-h.panel.hellos:
			delivered = delivered || (hello.LastUpdate != nil && hello.LastUpdate.Outcome == pb.UpdateOutcome_UPDATE_OUTCOME_FAILED)
		default:
			more = false
		}
	}
	if b, err := os.ReadFile(filepath.Join(h.dir, update.FileOutcome)); (err != nil || len(b) == 0) && !delivered {
		t.Error("the failure must be recorded for the next Hello")
	}
}
