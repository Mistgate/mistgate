// Package update is the node agent's self-update: verify a release the owner signed, swap the executable, re-exec,
// and undo it if the new build does not come up. The protocol and the state machine are specified in
// agent.proto ("UPDATE"); this package is the filesystem and decision
// half, internal/node/agent is the wiring (commands, Hello, the download client, draining before exec).
//
// Trust: the ed25519 key compiled into this binary (buildinfo.ReleaseKey) is the only thing that decides what
// may be installed. A build without the key never updates itself.
package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/release"
)

// Files in the state dir (mode 0600). update.starts and the guard's update.rolledback[.reason] are written by the unit's
// ExecStartPre script (GuardScript), everything else by this package.
const (
	FilePending        = "update.pending"
	FileStarts         = "update.starts"
	FileRolledBack     = "update.rolledback"
	FileRolledBackWhy  = "update.rolledback.reason"
	FileOutcome        = "update.outcome"
	CapUpdate          = "update/1"
	CapGuard           = "update-guard/1"
	CommitWindow       = 5 * time.Minute
	probeName          = ".mistgate-write-probe"
	defaultTimeout     = 10 * time.Minute
	defaultRetryWait   = 5 * time.Second
	defaultMaxReasonSz = 64
)

// Reasons of a rollback (LastUpdate.reason vocabulary of agent.proto).
const (
	ReasonNotCommitted  = "not_committed"
	ReasonBuiltMismatch = "built_mismatch"
	ReasonCrashLoop     = "crash_loop"
	ReasonApplyFailed   = "apply_failed"
	ReasonManual        = "manual"
	ReasonInterrupted   = "failed: interrupted"
	ReasonExec          = "failed: exec"
	ReasonNoPrevious    = "failed: no_previous"
)

var (
	// ErrRetryLater is what a Fetcher returns for "the panel's download slots are busy": waited out, not counted as a failed attempt.
	ErrRetryLater = errors.New("update: download slot busy, retry later")
	// ErrPermanent wraps a download error that retrying cannot fix (name not in the manifest, no bundle).
	ErrPermanent = errors.New("update: permanent download error")
	// ErrNoPrevious means there is no <exe>.prev to go back to.
	ErrNoPrevious = errors.New("no_previous")

	reasonRe = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// Source is what the agent lends to one update: the download client, its corrected clock and the inbounds that
// are FAILED right now (the gate compares against them: a node that already had a broken inbound must not roll back for it).
type Source interface {
	// Fetch appends the named file to w from byte offset, returning the file's total size as the panel reported it.
	Fetch(ctx context.Context, name string, offset uint64, w io.Writer) (total uint64, err error)
	Failed() []string
	Now() time.Time
}

// Finish ends an update or a rollback: it re-executes the new binary. The agent calls it after the CommandResult is on
// the wire and the engines are drained. An error means exec failed; the previous binary is back in place and the agent
// must exit so the supervisor restarts it.
type Finish func() error

// Config describes the running binary and its environment. Zero values are production defaults except Exec, which
// the caller sets to syscall.Exec (nil means "do not exec", for tests).
type Config struct {
	StateDir string
	ExePath  string // default: the running executable, symlinks resolved, taken once at New
	Version  string
	Built    int64
	Key      ed25519.PublicKey // nil = unsigned build: no self-update
	UnitGen  int               // MISTGATE_UNIT_GEN: >= 2 means the unit carries the crash-loop guard
	Exec     func(path string, argv, env []string) error
	Args     []string // default os.Args
	Env      []string // default os.Environ()
	Now      func() time.Time
	GOOS     string // default runtime.GOOS
	GOARCH   string
	Window   time.Duration // commit window, default CommitWindow
	Backoff  []time.Duration
	Timeout  time.Duration // whole download, default 10 min
	Log      *slog.Logger
	// RetryWait is the pause after ErrRetryLater.
	RetryWait time.Duration
}

// Marker is the content of update.pending (and, after a crash-loop restore, of update.rolledback).
type Marker struct {
	FromVersion string   `json:"from_version"`
	FromBuilt   int64    `json:"from_built"`
	ToVersion   string   `json:"to_version"`
	ToBuilt     int64    `json:"to_built"`
	StartedUnix int64    `json:"started_unix"`
	RequestID   string   `json:"request_id,omitempty"`
	PreFailed   []string `json:"pre_failed,omitempty"` // inbounds FAILED before the update (not the new build's fault)
}

// Updater is safe for concurrent use; one update or rollback runs at a time.
type Updater struct {
	cfg      Config
	log      *slog.Logger
	exe      string
	started  time.Time
	deadline time.Time

	busy    atomic.Bool
	pend    atomic.Pointer[Marker]        // set by Startup for a new build inside its commit window
	outcome atomic.Pointer[pb.LastUpdate] // what Hello reports until a HelloAck for it
}

// New fills the defaults. It touches no file.
func New(cfg Config) *Updater {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.GOOS == "" {
		cfg.GOOS = goos
	}
	if cfg.GOARCH == "" {
		cfg.GOARCH = goarch
	}
	if cfg.Window <= 0 {
		cfg.Window = CommitWindow
	}
	if cfg.Backoff == nil {
		cfg.Backoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.RetryWait <= 0 {
		cfg.RetryWait = defaultRetryWait
	}
	if cfg.Args == nil {
		cfg.Args = os.Args
	}
	if cfg.Env == nil {
		cfg.Env = os.Environ()
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	u := &Updater{cfg: cfg, log: cfg.Log, exe: cfg.ExePath, started: cfg.Now()}
	if u.exe == "" {
		// Taken once at start: after the swap /proc/self/exe reads "... (deleted)".
		if p, err := os.Executable(); err == nil {
			if p, err = filepath.EvalSymlinks(p); err == nil {
				u.exe = p
			}
		}
	}
	u.deadline = u.started.Add(cfg.Window)
	return u
}

// Capabilities lists "update/1" only when this process can really update itself: signed build, Linux, and
// a writable executable directory (false under the generation 1 unit, which is what keeps old nodes on "update by
// hand once"); "update-guard/1" when the unit carries the crash-loop guard.
func (u *Updater) Capabilities() []string {
	if u == nil || u.cfg.Key == nil || u.cfg.GOOS != "linux" || u.exe == "" || u.probe() != nil {
		return nil
	}
	caps := []string{CapUpdate}
	if u.cfg.UnitGen >= 2 {
		caps = append(caps, CapGuard)
	}
	return caps
}

// probe proves the executable's directory is writable (create and remove a file next to it).
func (u *Updater) probe() error {
	p := filepath.Join(filepath.Dir(u.exe), probeName)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(p)
}

func (u *Updater) path(name string) string { return filepath.Join(u.cfg.StateDir, name) }
func (u *Updater) prev() string            { return u.exe + ".prev" }
func (u *Updater) newPath() string         { return u.exe + ".new" }

// Outcome is the Hello.last_update to send now (nil = none). AckOutcome forgets it once a HelloAck for that Hello
// arrived; an outcome that was replaced in between stays.
func (u *Updater) Outcome() *pb.LastUpdate {
	if u == nil {
		return nil
	}
	return u.outcome.Load()
}

func (u *Updater) AckOutcome(o *pb.LastUpdate) {
	if u != nil && o != nil && u.outcome.CompareAndSwap(o, nil) {
		_ = os.Remove(u.path(FileOutcome))
	}
}

// Pending reports whether this process is a new build inside its commit window.
func (u *Updater) Pending() bool { return u.pend.Load() != nil }

// Startup reads the marker files before the agent connects. It may decide to roll back at once, in
// which case Finish is set and the caller must drain and run it before doing anything else.
type Startup struct {
	Pending *Marker // the new build, just exec'd: the caller must run Watch
	Finish  Finish  // a rollback that has to be executed now
}

func (u *Updater) Startup() Startup {
	var st Startup
	if m, err := readMarker(u.path(FileRolledBack)); m != nil || err != nil { // the unit guard restored .prev
		if m == nil {
			m = &Marker{}
		}
		reason := ReasonCrashLoop
		if b, err := os.ReadFile(u.path(FileRolledBackWhy)); err == nil && reasonRe.Match([]byte(strings.TrimSpace(string(b)))) {
			reason = strings.TrimSpace(string(b))
		}
		u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK, m, reason)
		for _, f := range []string{FileRolledBack, FileRolledBackWhy, FileStarts} {
			_ = os.Remove(u.path(f))
		}
	}
	m, err := readMarker(u.path(FilePending))
	switch {
	case err != nil: // unreadable marker: drop it, say so, run as we are
		u.log.Error("update marker unreadable, dropped", "err", err)
		_ = os.Remove(u.path(FilePending))
		u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_FAILED, &Marker{}, "failed: bad marker")
	case m == nil:
	case u.cfg.Built == m.ToBuilt: // the new build, just exec'd
		u.pend.Store(m)
		st.Pending = m
		u.log.Info("running a new build that is not committed yet", "from", m.FromVersion, "to", m.ToVersion,
			"window", u.cfg.Window.String())
	case u.cfg.Built == m.FromBuilt: // the swap never happened
		_ = os.Remove(u.path(FilePending))
		_ = os.Remove(u.newPath())
		u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_FAILED, m, ReasonInterrupted)
	default: // the bundle held a binary that is not the release it claims to be
		u.log.Error("binary is not the release the update marker describes", "built", u.cfg.Built, "marker_to", m.ToBuilt)
		fin, err := u.rollbackFor(m, ReasonBuiltMismatch)
		if err != nil {
			u.log.Error("rollback failed", "err", err)
		}
		st.Finish = fin
	}
	if u.outcome.Load() == nil {
		u.loadOutcome()
	}
	return st
}

// Watch runs in the new build until the update commits or the window ends. Every tick it asks status; the
// update commits after two consecutive polls that are Settled (connected, desired state applied) without a new FAILED
// inbound. At the deadline it rolls back: apply_failed when it was settled but an inbound is FAILED, else not_committed.
// It returns the committed outcome, or the Finish of a rollback (the caller drains and runs it), or ctx.Err().
func (u *Updater) Watch(ctx context.Context, tick time.Duration, status func() Status) (*pb.LastUpdate, Finish, error) {
	if u.pend.Load() == nil {
		return nil, nil, nil
	}
	ok := 0
	for {
		t := time.NewTimer(tick)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, nil, ctx.Err()
		case <-t.C:
		}
		s := status()
		if s.Settled && len(s.NewFailed) == 0 {
			if ok++; ok >= 2 {
				o, err := u.Commit()
				return o, nil, err
			}
		} else {
			ok = 0
		}
		if !u.cfg.Now().Before(u.deadline) {
			reason := ReasonNotCommitted
			if s.Settled && len(s.NewFailed) > 0 {
				reason = ReasonApplyFailed
			}
			fin, err := u.RollbackNow(reason)
			return nil, fin, err
		}
	}
}

// Status is what the agent tells Watch about itself.
type Status struct {
	Settled   bool     // connected to the panel, desired state applied, quiet for a while
	NewFailed []string // inbounds FAILED now that were not FAILED before the update
}

// Commit makes the update permanent: outcome ok, marker and start counter gone. The previous binary stays as .prev
// (the manual rollback target).
func (u *Updater) Commit() (*pb.LastUpdate, error) {
	m := u.pend.Load()
	if m == nil {
		return nil, nil
	}
	u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_OK, m, "")
	_ = os.Remove(u.path(FilePending))
	_ = os.Remove(u.path(FileStarts))
	u.pend.Store(nil)
	u.log.Info("update committed", "from", m.FromVersion, "to", m.ToVersion)
	return u.outcome.Load(), nil
}

// RollbackNow is the self-rollback of a new build that did not commit: .prev over the executable, outcome
// rolled_back with the reason, marker removed. It does not exec; the returned Finish does.
func (u *Updater) RollbackNow(reason string) (Finish, error) {
	m := u.pend.Load()
	if m == nil {
		return nil, errors.New("update: no update in progress")
	}
	fin, err := u.rollbackFor(m, reason)
	if err == nil {
		u.pend.Store(nil)
		u.busy.Store(true) // the process is about to be replaced
	}
	return fin, err
}

func (u *Updater) rollbackFor(m *Marker, reason string) (Finish, error) {
	if _, err := os.Stat(u.prev()); err != nil { // nothing to go back to: stay, and say the update is unverified
		_ = os.Remove(u.path(FilePending))
		u.pend.Store(nil)
		u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_FAILED, m, ReasonNoPrevious)
		return nil, ErrNoPrevious
	}
	u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK, m, reason)
	// The markers go only after the restore worked: they are what the unit's crash-loop guard counts, so when the
	// rename fails the guard still gets its chance to restore .prev on a later start.
	if err := os.Rename(u.prev(), u.exe); err != nil {
		u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_FAILED, m, "failed: rollback not writable")
		return nil, fmt.Errorf("restore previous binary: %w", err)
	}
	_ = os.Remove(u.path(FilePending))
	_ = os.Remove(u.path(FileStarts))
	syncDir(filepath.Dir(u.exe))
	u.log.Warn("rolling back to the previous build", "reason", reason, "from", m.FromVersion, "to", m.ToVersion)
	return u.execFinish(nil), nil
}

// execFinish replaces this process. after runs when exec returned (it failed).
func (u *Updater) execFinish(after func()) Finish {
	return func() error {
		if u.cfg.Exec == nil {
			return nil
		}
		err := u.cfg.Exec(u.exe, u.cfg.Args, u.cfg.Env)
		if err == nil { // only a test stub returns nil; the real exec never returns on success
			return nil
		}
		if after != nil {
			after()
		}
		return fmt.Errorf("re-exec %s: %w", u.exe, err)
	}
}

// setOutcome records (memory and disk) what Hello.last_update will say. m supplies from/to; for a manual rollback
// the restored build does not know itself yet, so from is left empty and filled at its next start.
func (u *Updater) setOutcome(o pb.UpdateOutcome, m *Marker, reason string) {
	lu := &pb.LastUpdate{
		Outcome: o, FromVersion: m.FromVersion, FromBuilt: m.FromBuilt, ToVersion: m.ToVersion, ToBuilt: m.ToBuilt,
		Reason: reason, AtUnix: u.cfg.Now().Unix(),
	}
	u.outcome.Store(lu)
	if b, err := protojson.Marshal(lu); err == nil {
		if err := writeAtomic(u.path(FileOutcome), b, 0o600); err != nil {
			u.log.Error("cannot write update outcome", "err", err)
		}
	}
}

func (u *Updater) loadOutcome() {
	b, err := os.ReadFile(u.path(FileOutcome))
	if err != nil {
		return
	}
	lu := &pb.LastUpdate{}
	if protojson.Unmarshal(b, lu) != nil || lu.Outcome == pb.UpdateOutcome_UPDATE_OUTCOME_UNSPECIFIED {
		_ = os.Remove(u.path(FileOutcome))
		return
	}
	if lu.Outcome == pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK && lu.FromVersion == "" { // manual rollback: we are the restored build
		lu.FromVersion, lu.FromBuilt = u.cfg.Version, u.cfg.Built
		if b, err := protojson.Marshal(lu); err == nil {
			_ = writeAtomic(u.path(FileOutcome), b, 0o600)
		}
	}
	u.outcome.Store(lu)
}

// ---------------------------------------------------------------------------------------------------
// UpdateAgent and RollbackAgent

// Apply executes one UpdateAgent. The CommandResult always goes back to the panel; a non-nil Finish means the
// binary was swapped and the caller must send the result, drain the engines and call it.
func (u *Updater) Apply(ctx context.Context, req *pb.UpdateAgent, src Source) (*pb.CommandResult, Finish) {
	res := &pb.CommandResult{RequestId: req.RequestId}
	fail := func(err error) (*pb.CommandResult, Finish) {
		res.Error = codeOf(err)
		res.Detail = err.Error()
		u.log.Warn("update refused", "code", res.Error, "err", err)
		return res, nil
	}
	if u.cfg.Key == nil {
		return fail(step("unsigned_build", errors.New("unsigned build: update by hand")))
	}
	m, err := release.Verify(u.cfg.Key, req.Manifest, req.Signature)
	if err != nil {
		return fail(err)
	}
	switch err := m.Check(src.Now(), u.cfg.Built); {
	case errors.Is(err, release.ErrUpToDate):
		res.Ok, res.Detail, res.Params = true, "already current", map[string]string{"noop": "1"}
		return res, nil
	case err != nil:
		return fail(err)
	}
	file, err := m.FileFor(u.cfg.GOOS, u.cfg.GOARCH)
	if err != nil {
		return fail(err)
	}
	if u.pend.Load() != nil || !u.busy.CompareAndSwap(false, true) { // also busy while a new build is uncommitted
		return fail(step("busy", errors.New("another update or rollback is in progress")))
	}
	swapped := false
	defer func() {
		if !swapped {
			u.busy.Store(false)
		}
	}()
	if err := u.probe(); err != nil {
		return fail(step("not_writable", err))
	}

	if err := u.download(ctx, src, file); err != nil {
		_ = os.Remove(u.newPath())
		return fail(err)
	}
	if err := u.verifyNew(file); err != nil {
		_ = os.Remove(u.newPath())
		return fail(err)
	}
	if err := copyFile(u.exe, u.prev(), 0o755); err != nil {
		_ = os.Remove(u.newPath())
		return fail(step("not_writable", err))
	}
	marker := &Marker{
		FromVersion: u.cfg.Version, FromBuilt: u.cfg.Built, ToVersion: m.Version, ToBuilt: m.Built,
		StartedUnix: src.Now().Unix(), RequestID: req.RequestId, PreFailed: src.Failed(),
	}
	b, _ := jsonMarshal(marker)
	if err := writeAtomic(u.path(FilePending), b, 0o600); err != nil {
		_ = os.Remove(u.newPath())
		return fail(step("not_writable", err))
	}
	if err := os.Rename(u.newPath(), u.exe); err != nil {
		_ = os.Remove(u.path(FilePending))
		_ = os.Remove(u.newPath())
		return fail(step("not_writable", err))
	}
	syncDir(filepath.Dir(u.exe))
	swapped = true
	u.log.Info("binary replaced, re-executing", "from", marker.FromVersion, "to", marker.ToVersion)
	res.Ok, res.Affected = true, 1
	res.Params = map[string]string{
		"from_version": marker.FromVersion, "from_built": fmt.Sprint(marker.FromBuilt),
		"to_version": marker.ToVersion, "to_built": fmt.Sprint(marker.ToBuilt),
	}
	return res, u.execFinish(func() { // exec failed: put the old binary back, forget the marker, let the supervisor restart us
		if err := os.Rename(u.prev(), u.exe); err != nil {
			u.log.Error("cannot restore the previous binary after a failed exec; the marker stays for the next start", "err", err)
		} else {
			_ = os.Remove(u.path(FilePending))
		}
		u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_FAILED, marker, ReasonExec)
	})
}

// Rollback executes one RollbackAgent: .prev over the executable (consumed: a second call answers no_previous).
func (u *Updater) Rollback(_ context.Context, req *pb.RollbackAgent) (*pb.CommandResult, Finish) {
	res := &pb.CommandResult{RequestId: req.RequestId}
	fail := func(err error) (*pb.CommandResult, Finish) {
		res.Error = codeOf(err)
		res.Detail = err.Error()
		return res, nil
	}
	if u.cfg.Key == nil {
		return fail(step("unsigned_build", errors.New("unsigned build: update by hand")))
	}
	if !u.busy.CompareAndSwap(false, true) {
		return fail(step("busy", errors.New("another update or rollback is in progress")))
	}
	done := false
	defer func() {
		if !done {
			u.busy.Store(false)
		}
	}()
	if _, err := os.Stat(u.prev()); err != nil {
		return fail(step("no_previous", errors.New("there is no previous build on this node")))
	}
	if err := u.probe(); err != nil {
		return fail(step("not_writable", err))
	}
	// to = the build we leave; from (the restored build) is filled in by the old binary at its start.
	left := &Marker{ToVersion: u.cfg.Version, ToBuilt: u.cfg.Built}
	u.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK, left, ReasonManual)
	if err := os.Rename(u.prev(), u.exe); err != nil { // markers stay: the guard may still need them
		u.outcome.Store(nil)
		_ = os.Remove(u.path(FileOutcome))
		return fail(step("not_writable", err))
	}
	_ = os.Remove(u.path(FilePending))
	_ = os.Remove(u.path(FileStarts))
	syncDir(filepath.Dir(u.exe))
	done = true
	u.pend.Store(nil)
	u.log.Warn("manual rollback: previous binary restored, re-executing", "leaving", u.cfg.Version)
	res.Ok, res.Affected = true, 1
	res.Params = map[string]string{"from_version": u.cfg.Version, "from_built": fmt.Sprint(u.cfg.Built)}
	return res, u.execFinish(nil)
}

// ---------------------------------------------------------------------------------------------------
// download and verify

// download streams the file into <exe>.new (0700 while it is being written), resuming from the bytes already on disk
// after a dropped stream: one try plus one retry per Backoff entry, each failure counted; ErrRetryLater waits without
// counting; the whole thing is bounded by Config.Timeout.
func (u *Updater) download(ctx context.Context, src Source, f release.File) error {
	ctx, cancel := context.WithTimeout(ctx, u.cfg.Timeout)
	defer cancel()
	fh, err := os.OpenFile(u.newPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return step("not_writable", err)
	}
	defer fh.Close()
	w := &capWriter{w: fh, left: f.Size}
	failed := 0
	for w.n < f.Size {
		total, err := src.Fetch(ctx, f.Name, uint64(w.n), w)
		if err == nil && total != 0 && total != uint64(f.Size) {
			return step("size_mismatch", fmt.Errorf("panel serves %d bytes, manifest says %d", total, f.Size))
		}
		switch {
		case err == nil && w.n == f.Size:
		case errors.Is(err, errOverflow):
			return step("size_mismatch", err)
		case ctx.Err() != nil:
			return step("download_failed", ctx.Err())
		case errors.Is(err, ErrPermanent):
			return step("download_failed", err)
		case errors.Is(err, ErrRetryLater):
			if !sleep(ctx, u.cfg.RetryWait) {
				return step("download_failed", ctx.Err())
			}
		default:
			if err == nil {
				err = io.ErrUnexpectedEOF // the stream ended early
			}
			if failed >= len(u.cfg.Backoff) {
				return step("download_failed", err)
			}
			u.log.Warn("download interrupted, retrying", "have", w.n, "of", f.Size, "err", err)
			if !sleep(ctx, u.cfg.Backoff[failed]) {
				return step("download_failed", ctx.Err())
			}
			failed++
		}
	}
	if err := fh.Sync(); err != nil {
		return step("not_writable", err)
	}
	return nil
}

// verifyNew checks size and SHA-256 of <exe>.new against the signed manifest, then makes it executable and durable.
func (u *Updater) verifyNew(f release.File) error {
	r, err := os.Open(u.newPath())
	if err != nil {
		return step("not_writable", err)
	}
	err = release.VerifyFile(f, r)
	r.Close()
	if err != nil {
		if c := release.Code(err); c != "" {
			return step(c, err)
		}
		return step("download_failed", err)
	}
	if err := os.Chmod(u.newPath(), 0o755); err != nil {
		return step("not_writable", err)
	}
	syncDir(filepath.Dir(u.exe))
	return nil
}

var errOverflow = errors.New("update: more bytes than the manifest lists")

// capWriter refuses to write past the size the signed manifest promised (a bad panel must not fill the disk).
type capWriter struct {
	w    io.Writer
	left int64
	n    int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left-c.n {
		return 0, errOverflow
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---------------------------------------------------------------------------------------------------
// errors

// stepErr carries the agent.proto error code of a failed step.
type stepErr struct {
	code string
	err  error
}

func (e *stepErr) Error() string { return e.code + ": " + e.err.Error() }
func (e *stepErr) Unwrap() error { return e.err }

func step(code string, err error) error { return &stepErr{code, err} }

// codeOf maps an error to the CommandResult.error vocabulary: a step's own code, a release-package code, or
// "failed: <short message>".
func codeOf(err error) string {
	var se *stepErr
	if errors.As(err, &se) {
		return se.code
	}
	if c := release.Code(err); c != "" {
		return c
	}
	msg := err.Error()
	if len(msg) > defaultMaxReasonSz {
		msg = msg[:defaultMaxReasonSz]
	}
	return "failed: " + msg
}
