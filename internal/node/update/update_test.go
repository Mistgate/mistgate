package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/release"
)

const (
	ownBuilt = 1000
	newBuilt = 2000
)

var (
	oldBin = []byte("old binary \x00\x01 v1")
	newBin = append([]byte("new binary v2 "), make([]byte, 5000)...)
)

type env struct {
	t     *testing.T
	dir   string // executable directory
	state string
	exe   string
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	now   time.Time

	mu    sync.Mutex
	execs []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: t.TempDir(), state: t.TempDir(), pub: pub, priv: priv, now: time.Unix(1_500_000, 0)}
	e.exe = filepath.Join(e.dir, "mistgate-node")
	if err := os.WriteFile(e.exe, oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) updater(mut func(*Config)) *Updater {
	cfg := Config{
		StateDir: e.state, ExePath: e.exe, Version: "0.1.0-old", Built: ownBuilt, Key: e.pub, UnitGen: 2,
		Now: func() time.Time { return e.now }, GOOS: "linux", GOARCH: "amd64",
		Backoff: []time.Duration{time.Millisecond, time.Millisecond}, RetryWait: time.Millisecond,
		Args: []string{e.exe, "run"}, Env: []string{"A=b"},
		Exec: func(path string, argv, env []string) error {
			e.mu.Lock()
			e.execs = append(e.execs, path)
			e.mu.Unlock()
			return nil
		},
	}
	if mut != nil {
		mut(&cfg)
	}
	return New(cfg)
}

func (e *env) execCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.execs)
}

// manifest builds and signs a manifest for content (as linux/amd64) with the given built time.
func (e *env) manifest(content []byte, built int64, mut func(*release.Manifest)) (manifest, sig []byte) {
	e.t.Helper()
	sum := sha256.Sum256(content)
	m := &release.Manifest{
		Schema: 1, Version: "0.2.0-new", Built: built, Expires: e.now.Unix() + 3600,
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: "mistgate-node-linux-amd64", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}},
	}
	if mut != nil {
		mut(m)
	}
	b, err := m.Marshal()
	if err != nil {
		e.t.Fatal(err)
	}
	return b, release.Sign(e.priv, b)
}

func (e *env) req(manifest, sig []byte) *pb.UpdateAgent {
	return &pb.UpdateAgent{RequestId: "req-1", Manifest: manifest, Signature: sig}
}

// fakeSrc serves content; dropAfter > 0 makes the first call die after that many bytes.
type fakeSrc struct {
	now       time.Time
	content   []byte
	total     uint64 // 0 = len(content)
	dropAfter int
	fails     int // fail this many calls outright
	retry     int // answer ErrRetryLater this many times first
	failed    []string
	offsets   []uint64
	calls     int
}

func (s *fakeSrc) Now() time.Time   { return s.now }
func (s *fakeSrc) Failed() []string { return s.failed }
func (s *fakeSrc) Fetch(_ context.Context, name string, off uint64, w io.Writer) (uint64, error) {
	s.calls++
	s.offsets = append(s.offsets, off)
	total := s.total
	if total == 0 {
		total = uint64(len(s.content))
	}
	if s.retry > 0 {
		s.retry--
		return 0, ErrRetryLater
	}
	if s.fails > 0 {
		s.fails--
		return 0, errors.New("connection reset")
	}
	data := s.content[min(off, uint64(len(s.content))):]
	if s.dropAfter > 0 && len(data) > s.dropAfter {
		_, _ = w.Write(data[:s.dropAfter])
		s.dropAfter = 0
		return total, errors.New("stream dropped")
	}
	_, err := w.Write(data)
	return total, err
}

func (e *env) src(content []byte) *fakeSrc { return &fakeSrc{now: e.now, content: content} }

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestApplySwapsAndMarks(t *testing.T) {
	e := newEnv(t)
	u := e.updater(nil)
	man, sig := e.manifest(newBin, newBuilt, nil)
	src := e.src(newBin)
	src.failed = []string{"inb_a"}

	res, fin := u.Apply(context.Background(), e.req(man, sig), src)
	if !res.Ok || res.Error != "" || res.Affected != 1 || res.RequestId != "req-1" {
		t.Fatalf("result = %v", res)
	}
	if fin == nil {
		t.Fatal("no Finish")
	}
	if res.Params["from_version"] != "0.1.0-old" || res.Params["to_version"] != "0.2.0-new" || res.Params["to_built"] != "2000" || res.Params["from_built"] != "1000" {
		t.Errorf("params = %v", res.Params)
	}
	if string(readFile(t, e.exe)) != string(newBin) {
		t.Error("executable is not the new build")
	}
	if string(readFile(t, e.exe+".prev")) != string(oldBin) {
		t.Error(".prev is not the old build")
	}
	if exists(e.exe + ".new") {
		t.Error(".new left behind")
	}
	if fi, _ := os.Stat(e.exe); runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("new binary not executable: %v", fi.Mode())
	}
	mk, err := readMarker(filepath.Join(e.state, FilePending))
	if err != nil || mk == nil {
		t.Fatalf("marker: %v %v", mk, err)
	}
	want := Marker{FromVersion: "0.1.0-old", FromBuilt: ownBuilt, ToVersion: "0.2.0-new", ToBuilt: newBuilt, StartedUnix: e.now.Unix(), RequestID: "req-1", PreFailed: []string{"inb_a"}}
	if !reflect.DeepEqual(*mk, want) {
		t.Errorf("marker = %+v, want %+v", *mk, want)
	}
	if e.execCount() != 0 {
		t.Error("exec ran before Finish")
	}
	if err := fin(); err != nil {
		t.Fatal(err)
	}
	if e.execCount() != 1 || e.execs[0] != e.exe {
		t.Errorf("execs = %v", e.execs)
	}
	// One update at a time: the process is about to be replaced.
	res2, _ := u.Apply(context.Background(), e.req(man, sig), src)
	if res2.Error != "busy" {
		t.Errorf("second apply = %v", res2)
	}
}

func TestApplyRefusals(t *testing.T) {
	otherPub, otherPriv, _ := release.GenerateKey()
	_ = otherPub
	for _, tc := range []struct {
		name string
		want string
		run  func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc)
	}{
		{"unsigned build", "unsigned_build", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			return e.updater(func(c *Config) { c.Key = nil }), e.req(m, s), e.src(newBin)
		}},
		{"bad signature", "bad_signature", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			s[3] ^= 0xff
			return e.updater(nil), e.req(m, s), e.src(newBin)
		}},
		{"tampered manifest", "bad_signature", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			m = append(m[:len(m)-2:len(m)-2], []byte(" \n")...)
			return e.updater(nil), e.req(m, s), e.src(newBin)
		}},
		{"signed by another key", "bad_signature", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, _ := e.manifest(newBin, newBuilt, nil)
			return e.updater(nil), e.req(m, release.Sign(otherPriv, m)), e.src(newBin)
		}},
		{"empty signature", "bad_signature", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, _ := e.manifest(newBin, newBuilt, nil)
			return e.updater(nil), e.req(m, nil), e.src(newBin)
		}},
		{"signed garbage", "bad_manifest", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m := []byte(`{"schema":1,"surprise":true}`)
			return e.updater(nil), e.req(m, release.Sign(e.priv, m)), e.src(newBin)
		}},
		{"newer schema", "bad_manifest", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m := []byte(`{"schema":2}`)
			return e.updater(nil), e.req(m, release.Sign(e.priv, m)), e.src(newBin)
		}},
		{"expired", "expired", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, func(m *release.Manifest) { m.Expires = e.now.Unix() - 1 })
			return e.updater(nil), e.req(m, s), e.src(newBin)
		}},
		{"older than running", "downgrade", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, ownBuilt-1, nil)
			return e.updater(nil), e.req(m, s), e.src(newBin)
		}},
		{"no file for this platform", "no_file_for_platform", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			return e.updater(func(c *Config) { c.GOARCH = "arm64" }), e.req(m, s), e.src(newBin)
		}},
		{"hash mismatch", "hash_mismatch", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			evil := append([]byte(nil), newBin...)
			evil[0] ^= 0xff
			return e.updater(nil), e.req(m, s), e.src(evil)
		}},
		{"panel serves another size", "size_mismatch", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			return e.updater(nil), e.req(m, s), e.src(newBin[:len(newBin)-7])
		}},
		{"panel streams past the size", "size_mismatch", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			src := e.src(append(append([]byte(nil), newBin...), 1, 2, 3))
			src.total = uint64(len(newBin)) // claims the right size, sends more
			return e.updater(nil), e.req(m, s), src
		}},
		{"download keeps failing", "download_failed", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			src := e.src(newBin)
			src.fails = 99
			return e.updater(nil), e.req(m, s), src
		}},
		{"executable directory not writable", "not_writable", func(e *env) (*Updater, *pb.UpdateAgent, *fakeSrc) {
			m, s := e.manifest(newBin, newBuilt, nil)
			return e.updater(func(c *Config) { c.ExePath = filepath.Join(e.dir, "missing", "mistgate-node") }), e.req(m, s), e.src(newBin)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			u, req, src := tc.run(e)
			res, fin := u.Apply(context.Background(), req, src)
			if res.Ok || res.Error != tc.want || fin != nil || res.RequestId != "req-1" {
				t.Fatalf("result = %v, want error %q", res, tc.want)
			}
			if string(readFile(t, e.exe)) != string(oldBin) {
				t.Error("installed binary was touched")
			}
			for _, f := range []string{e.exe + ".new", e.exe + ".prev", filepath.Join(e.state, FilePending)} {
				if exists(f) {
					t.Errorf("%s left behind", f)
				}
			}
			if e.execCount() != 0 {
				t.Error("exec ran")
			}
			// A refusal leaves the updater free for the next attempt.
			man, sig := e.manifest(newBin, newBuilt, nil)
			good := e.updater(nil)
			if r, _ := good.Apply(context.Background(), e.req(man, sig), e.src(newBin)); !r.Ok {
				t.Errorf("a clean retry failed: %v", r)
			}
		})
	}
}

func TestApplyEqualBuiltIsANoop(t *testing.T) {
	e := newEnv(t)
	u := e.updater(nil)
	man, sig := e.manifest(newBin, ownBuilt, nil)
	res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
	if !res.Ok || res.Affected != 0 || res.Params["noop"] != "1" || res.Detail != "already current" || fin != nil {
		t.Fatalf("result = %v", res)
	}
	if exists(e.exe+".prev") || exists(filepath.Join(e.state, FilePending)) {
		t.Error("a no-op touched the disk")
	}
}

func TestDownloadResumesAndRetries(t *testing.T) {
	t.Run("truncated stream resumes from the bytes on disk", func(t *testing.T) {
		e := newEnv(t)
		u := e.updater(nil)
		man, sig := e.manifest(newBin, newBuilt, nil)
		src := e.src(newBin)
		src.dropAfter = 1234
		res, fin := u.Apply(context.Background(), e.req(man, sig), src)
		if !res.Ok || fin == nil {
			t.Fatalf("result = %v", res)
		}
		if !reflect.DeepEqual(src.offsets, []uint64{0, 1234}) {
			t.Errorf("offsets = %v", src.offsets)
		}
		if string(readFile(t, e.exe)) != string(newBin) {
			t.Error("resumed download is corrupt")
		}
	})
	t.Run("a busy panel is waited out without spending an attempt", func(t *testing.T) {
		e := newEnv(t)
		man, sig := e.manifest(newBin, newBuilt, nil)
		u := e.updater(func(c *Config) { c.Backoff = []time.Duration{time.Millisecond} })
		src := e.src(newBin)
		src.retry = 5
		res, _ := u.Apply(context.Background(), e.req(man, sig), src)
		if !res.Ok || src.calls != 6 {
			t.Fatalf("result = %v, calls = %d", res, src.calls)
		}
	})
	t.Run("attempts are one try plus one per backoff", func(t *testing.T) {
		e := newEnv(t)
		man, sig := e.manifest(newBin, newBuilt, nil)
		u := e.updater(nil) // two backoff entries
		src := e.src(newBin)
		src.fails = 99
		res, _ := u.Apply(context.Background(), e.req(man, sig), src)
		if res.Error != "download_failed" || src.calls != 3 {
			t.Fatalf("result = %v, calls = %d", res, src.calls)
		}
	})
	t.Run("a permanent error is not retried", func(t *testing.T) {
		e := newEnv(t)
		man, sig := e.manifest(newBin, newBuilt, nil)
		u := e.updater(nil)
		res, _ := u.Apply(context.Background(), e.req(man, sig), &permSrc{fakeSrc: e.src(newBin)})
		if res.Error != "download_failed" {
			t.Fatalf("result = %v", res)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		e := newEnv(t)
		man, sig := e.manifest(newBin, newBuilt, nil)
		u := e.updater(nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		src := e.src(newBin)
		src.fails = 99
		res, _ := u.Apply(ctx, e.req(man, sig), src)
		if res.Error != "download_failed" || exists(e.exe+".new") {
			t.Fatalf("result = %v", res)
		}
	})
}

type permSrc struct {
	*fakeSrc
	n int
}

func (p *permSrc) Fetch(context.Context, string, uint64, io.Writer) (uint64, error) {
	p.n++
	if p.n > 1 {
		panic("retried a permanent error")
	}
	return 0, ErrPermanent
}

func TestExecFailureRestoresThePreviousBinary(t *testing.T) {
	e := newEnv(t)
	u := e.updater(func(c *Config) {
		c.Exec = func(string, []string, []string) error { return errors.New("exec format error") }
	})
	man, sig := e.manifest(newBin, newBuilt, nil)
	res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
	if !res.Ok || fin == nil {
		t.Fatal(res)
	}
	if err := fin(); err == nil {
		t.Fatal("exec failure was swallowed")
	}
	if string(readFile(t, e.exe)) != string(oldBin) || exists(filepath.Join(e.state, FilePending)) {
		t.Error("old binary / marker not restored after a failed exec")
	}
	o := u.Outcome()
	if o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_FAILED || o.Reason != ReasonExec || o.ToBuilt != newBuilt {
		t.Errorf("outcome = %v", o)
	}
}

func TestStartupMarkerTable(t *testing.T) {
	marker := Marker{FromVersion: "0.1.0-old", FromBuilt: ownBuilt, ToVersion: "0.2.0-new", ToBuilt: newBuilt, StartedUnix: 1}
	put := func(e *env, m Marker) {
		b, _ := jsonMarshal(m)
		if err := os.WriteFile(filepath.Join(e.state, FilePending), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("new build just exec'd: commit window", func(t *testing.T) {
		e := newEnv(t)
		put(e, marker)
		u := e.updater(func(c *Config) { c.Built, c.Version = newBuilt, "0.2.0-new" })
		st := u.Startup()
		if st.Pending == nil || st.Finish != nil || !u.Pending() || u.Outcome() != nil {
			t.Fatalf("startup = %+v outcome %v", st, u.Outcome())
		}
		if !exists(filepath.Join(e.state, FilePending)) {
			t.Error("marker must stay until the commit")
		}
	})
	t.Run("old build: the swap never happened", func(t *testing.T) {
		e := newEnv(t)
		put(e, marker)
		_ = os.WriteFile(e.exe+".new", []byte("half"), 0o755)
		u := e.updater(nil)
		st := u.Startup()
		o := u.Outcome()
		if st.Pending != nil || st.Finish != nil || o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_FAILED || o.Reason != ReasonInterrupted {
			t.Fatalf("startup = %+v outcome %v", st, o)
		}
		if exists(filepath.Join(e.state, FilePending)) || exists(e.exe+".new") {
			t.Error("marker or .new left")
		}
	})
	t.Run("wrong file in the bundle: roll back at once", func(t *testing.T) {
		e := newEnv(t)
		put(e, marker)
		_ = os.WriteFile(e.exe+".prev", oldBin, 0o755)
		_ = os.WriteFile(e.exe, []byte("mislabelled"), 0o755)
		u := e.updater(func(c *Config) { c.Built = 1500 }) // neither from nor to
		st := u.Startup()
		o := u.Outcome()
		if st.Finish == nil || o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || o.Reason != ReasonBuiltMismatch || o.FromBuilt != ownBuilt || o.ToBuilt != newBuilt {
			t.Fatalf("startup = %+v outcome %v", st, o)
		}
		if string(readFile(t, e.exe)) != string(oldBin) || exists(e.exe+".prev") || exists(filepath.Join(e.state, FilePending)) {
			t.Error("old binary not restored")
		}
		if err := st.Finish(); err != nil || e.execCount() != 1 {
			t.Errorf("finish: %v, execs %d", err, e.execCount())
		}
	})
	t.Run("wrong file and no previous build: stay and say so", func(t *testing.T) {
		e := newEnv(t)
		put(e, marker)
		u := e.updater(func(c *Config) { c.Built = 1500 })
		st := u.Startup()
		o := u.Outcome()
		if st.Finish != nil || o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_FAILED || o.Reason != ReasonNoPrevious {
			t.Fatalf("startup = %+v outcome %v", st, o)
		}
		if exists(filepath.Join(e.state, FilePending)) {
			t.Error("marker would loop forever")
		}
	})
	t.Run("the unit guard restored .prev after a crash loop", func(t *testing.T) {
		e := newEnv(t)
		b, _ := jsonMarshal(marker)
		_ = os.WriteFile(filepath.Join(e.state, FileRolledBack), b, 0o600)
		_ = os.WriteFile(filepath.Join(e.state, FileRolledBackWhy), []byte("crash_loop\n"), 0o600)
		_ = os.WriteFile(filepath.Join(e.state, FileStarts), []byte("3"), 0o600)
		u := e.updater(nil)
		st := u.Startup()
		o := u.Outcome()
		if st.Finish != nil || st.Pending != nil || o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || o.Reason != "crash_loop" || o.ToVersion != "0.2.0-new" {
			t.Fatalf("startup = %+v outcome %v", st, o)
		}
		for _, f := range []string{FileRolledBack, FileRolledBackWhy, FileStarts} {
			if exists(filepath.Join(e.state, f)) {
				t.Errorf("%s left", f)
			}
		}
		if !exists(filepath.Join(e.state, FileOutcome)) {
			t.Error("outcome not persisted for the Hello")
		}
	})
	t.Run("an odd reason file is not trusted", func(t *testing.T) {
		e := newEnv(t)
		b, _ := jsonMarshal(marker)
		_ = os.WriteFile(filepath.Join(e.state, FileRolledBack), b, 0o600)
		_ = os.WriteFile(filepath.Join(e.state, FileRolledBackWhy), []byte("x; rm -rf /"), 0o600)
		u := e.updater(nil)
		u.Startup()
		if o := u.Outcome(); o == nil || o.Reason != ReasonCrashLoop {
			t.Errorf("outcome = %v", o)
		}
	})
	t.Run("an unreadable marker is dropped", func(t *testing.T) {
		e := newEnv(t)
		_ = os.WriteFile(filepath.Join(e.state, FilePending), []byte("{not json"), 0o600)
		u := e.updater(nil)
		st := u.Startup()
		if st.Pending != nil || st.Finish != nil || exists(filepath.Join(e.state, FilePending)) || u.Outcome() == nil {
			t.Errorf("startup = %+v outcome %v", st, u.Outcome())
		}
	})
	t.Run("an outcome on disk is reported until acked", func(t *testing.T) {
		e := newEnv(t)
		u1 := e.updater(nil)
		u1.setOutcome(pb.UpdateOutcome_UPDATE_OUTCOME_OK, &marker, "")
		u2 := e.updater(nil) // the next process
		u2.Startup()
		o := u2.Outcome()
		if o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_OK || o.ToVersion != "0.2.0-new" {
			t.Fatalf("outcome = %v", o)
		}
		u2.AckOutcome(&pb.LastUpdate{}) // a different outcome than the one sent: stays
		if u2.Outcome() == nil || !exists(filepath.Join(e.state, FileOutcome)) {
			t.Fatal("acked an outcome that was not the one sent")
		}
		u2.AckOutcome(o)
		if u2.Outcome() != nil || exists(filepath.Join(e.state, FileOutcome)) {
			t.Error("outcome not forgotten after the ack")
		}
	})
}

func TestWatchCommitAndSelfRollback(t *testing.T) {
	marker := Marker{FromVersion: "0.1.0-old", FromBuilt: ownBuilt, ToVersion: "0.2.0-new", ToBuilt: newBuilt, PreFailed: []string{"inb_old"}}
	start := func(t *testing.T) (*env, *Updater, *time.Time) {
		e := newEnv(t)
		b, _ := jsonMarshal(marker)
		_ = os.WriteFile(filepath.Join(e.state, FilePending), b, 0o600)
		_ = os.WriteFile(filepath.Join(e.state, FileStarts), []byte("1"), 0o600)
		_ = os.WriteFile(e.exe+".prev", oldBin, 0o755)
		_ = os.WriteFile(e.exe, newBin, 0o755)
		clock := e.now
		u := e.updater(func(c *Config) {
			c.Built, c.Version = newBuilt, "0.2.0-new"
			c.Now = func() time.Time { return clock }
		})
		if st := u.Startup(); st.Pending == nil {
			t.Fatal("not in the commit window")
		}
		return e, u, &clock
	}
	tick := time.Millisecond

	t.Run("commits after two settled polls", func(t *testing.T) {
		e, u, _ := start(t)
		polls := 0
		o, fin, err := u.Watch(context.Background(), tick, func() Status {
			polls++
			return Status{Settled: true}
		})
		if err != nil || fin != nil || o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_OK || polls != 2 {
			t.Fatalf("o=%v fin=%v err=%v polls=%d", o, fin != nil, err, polls)
		}
		for _, f := range []string{FilePending, FileStarts} {
			if exists(filepath.Join(e.state, f)) {
				t.Errorf("%s left after the commit", f)
			}
		}
		if string(readFile(t, e.exe+".prev")) != string(oldBin) {
			t.Error(".prev must stay for a manual rollback")
		}
		if u.Pending() || e.execCount() != 0 {
			t.Error("commit must not exec")
		}
	})
	t.Run("a failure that predates the update does not matter; a new one resets the count", func(t *testing.T) {
		_, u, _ := start(t)
		seq := []Status{{Settled: true}, {Settled: true, NewFailed: []string{"inb_x"}}, {Settled: true}, {Settled: true}}
		i := 0
		o, _, err := u.Watch(context.Background(), tick, func() Status { s := seq[i]; i++; return s })
		if err != nil || o == nil || i != 4 {
			t.Fatalf("o=%v err=%v polls=%d", o, err, i)
		}
	})
	t.Run("five minutes without a commit: rolled back, not_committed", func(t *testing.T) {
		e, u, clock := start(t)
		o, fin, err := u.Watch(context.Background(), tick, func() Status {
			*clock = clock.Add(2 * time.Minute) // a fake clock: 3 polls pass the 5-minute window
			return Status{}
		})
		if err != nil || o != nil || fin == nil {
			t.Fatalf("o=%v fin=%v err=%v", o, fin != nil, err)
		}
		out := u.Outcome()
		if out.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || out.Reason != ReasonNotCommitted || out.FromBuilt != ownBuilt || out.ToBuilt != newBuilt {
			t.Errorf("outcome = %v", out)
		}
		if string(readFile(t, e.exe)) != string(oldBin) || exists(e.exe+".prev") || exists(filepath.Join(e.state, FilePending)) || exists(filepath.Join(e.state, FileStarts)) {
			t.Error("old binary not restored / marker left")
		}
		if err := fin(); err != nil || e.execCount() != 1 {
			t.Errorf("finish %v execs %d", err, e.execCount())
		}
	})
	t.Run("settled but a new inbound is FAILED at the deadline: apply_failed", func(t *testing.T) {
		_, u, clock := start(t)
		_, fin, err := u.Watch(context.Background(), tick, func() Status {
			*clock = clock.Add(3 * time.Minute)
			return Status{Settled: true, NewFailed: []string{"inb_new"}}
		})
		if err != nil || fin == nil || u.Outcome().Reason != ReasonApplyFailed {
			t.Fatalf("fin=%v err=%v outcome=%v", fin != nil, err, u.Outcome())
		}
	})
	t.Run("no previous build to go back to", func(t *testing.T) {
		e, u, clock := start(t)
		_ = os.Remove(e.exe + ".prev")
		_, fin, err := u.Watch(context.Background(), tick, func() Status { *clock = clock.Add(10 * time.Minute); return Status{} })
		if !errors.Is(err, ErrNoPrevious) || fin != nil || u.Outcome().Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_FAILED {
			t.Fatalf("fin=%v err=%v outcome=%v", fin != nil, err, u.Outcome())
		}
	})
	t.Run("context ends the watch", func(t *testing.T) {
		_, u, _ := start(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := u.Watch(ctx, tick, func() Status { return Status{} }); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a process that is not a new build has nothing to watch", func(t *testing.T) {
		e := newEnv(t)
		u := e.updater(nil)
		u.Startup()
		if o, fin, err := u.Watch(context.Background(), tick, func() Status { return Status{} }); o != nil || fin != nil || err != nil {
			t.Errorf("%v %v %v", o, fin != nil, err)
		}
	})
}

func TestNoUpdateWhileANewBuildIsUncommitted(t *testing.T) {
	e := newEnv(t)
	b, _ := jsonMarshal(Marker{FromVersion: "a", FromBuilt: ownBuilt, ToVersion: "b", ToBuilt: newBuilt})
	_ = os.WriteFile(filepath.Join(e.state, FilePending), b, 0o600)
	u := e.updater(func(c *Config) { c.Built = newBuilt })
	u.Startup()
	man, sig := e.manifest(newBin, newBuilt+10, nil)
	res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
	if res.Error != "busy" || fin != nil {
		t.Fatalf("result = %v", res)
	}
}

func TestRollbackAgent(t *testing.T) {
	t.Run("restores .prev once", func(t *testing.T) {
		e := newEnv(t)
		_ = os.WriteFile(e.exe+".prev", []byte("the previous build"), 0o755)
		_ = os.WriteFile(filepath.Join(e.state, FileStarts), []byte("2"), 0o600)
		u := e.updater(nil)
		res, fin := u.Rollback(context.Background(), &pb.RollbackAgent{RequestId: "rb-1"})
		if !res.Ok || res.RequestId != "rb-1" || fin == nil {
			t.Fatalf("result = %v", res)
		}
		if string(readFile(t, e.exe)) != "the previous build" || exists(e.exe+".prev") {
			t.Error("previous build not restored / not consumed")
		}
		o := u.Outcome()
		if o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || o.Reason != ReasonManual || o.ToVersion != "0.1.0-old" {
			t.Errorf("outcome = %v", o)
		}
		if err := fin(); err != nil || e.execCount() != 1 {
			t.Errorf("finish %v", err)
		}
		// The restored build fills in who it is when it reads the outcome.
		restored := New(Config{StateDir: e.state, ExePath: e.exe, Version: "0.0.9-restored", Built: 900, Key: e.pub, Now: func() time.Time { return e.now }, GOOS: "linux"})
		restored.Startup()
		if o := restored.Outcome(); o == nil || o.FromVersion != "0.0.9-restored" || o.FromBuilt != 900 || o.ToVersion != "0.1.0-old" {
			t.Errorf("restored build's outcome = %v", o)
		}
	})
	t.Run("no previous build", func(t *testing.T) {
		e := newEnv(t)
		u := e.updater(nil)
		for i := 0; i < 2; i++ {
			res, fin := u.Rollback(context.Background(), &pb.RollbackAgent{RequestId: "rb"})
			if res.Ok || res.Error != "no_previous" || fin != nil {
				t.Fatalf("call %d: %v", i, res)
			}
		}
		if string(readFile(t, e.exe)) != string(oldBin) || u.Outcome() != nil {
			t.Error("a refused rollback changed something")
		}
	})
	t.Run("unsigned build", func(t *testing.T) {
		e := newEnv(t)
		_ = os.WriteFile(e.exe+".prev", []byte("x"), 0o755)
		u := e.updater(func(c *Config) { c.Key = nil })
		if res, _ := u.Rollback(context.Background(), &pb.RollbackAgent{}); res.Error != "unsigned_build" {
			t.Errorf("result = %v", res)
		}
	})
	t.Run("an update after a rollback has a fresh .prev; two calls in a row are busy", func(t *testing.T) {
		e := newEnv(t)
		_ = os.WriteFile(e.exe+".prev", []byte("x"), 0o755)
		u := e.updater(nil)
		if res, _ := u.Rollback(context.Background(), &pb.RollbackAgent{}); !res.Ok {
			t.Fatal(res)
		}
		_ = os.WriteFile(e.exe+".prev", []byte("y"), 0o755)
		if res, _ := u.Rollback(context.Background(), &pb.RollbackAgent{}); res.Error != "busy" {
			t.Errorf("result = %v", res)
		}
	})
}

func TestCapabilities(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		want []string
	}{
		{"signed, writable, guarded unit", nil, []string{"update/1", "update-guard/1"}},
		{"generation 1 unit: writable dir in a foreground run", func(c *Config) { c.UnitGen = 1 }, []string{"update/1"}},
		{"unsigned build", func(c *Config) { c.Key = nil }, nil},
		{"not linux", func(c *Config) { c.GOOS = "darwin" }, nil},
		{"executable directory not writable", func(c *Config) { c.ExePath = filepath.Join(e.dir, "nope", "x") }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e.updater(tc.mut).Capabilities()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("caps = %v, want %v", got, tc.want)
			}
		})
	}
	if ents, _ := os.ReadDir(e.dir); len(ents) != 1 {
		t.Errorf("the write probe left files behind: %v", ents)
	}
	var nilU *Updater
	if nilU.Capabilities() != nil || nilU.Outcome() != nil {
		t.Error("a nil updater must be inert")
	}
}

// A failed restore of .prev must keep update.pending and update.starts: they are what the unit's crash-loop guard
// counts, so a new build that cannot be rolled back by us is still rolled back by the guard on a later start. (The
// executable is made a non-empty directory: renaming a file over it fails on every OS.)
func TestFailedRestoreKeepsTheGuardMarkers(t *testing.T) {
	marker := Marker{FromVersion: "0.1.0-old", FromBuilt: ownBuilt, ToVersion: "0.2.0-new", ToBuilt: newBuilt}
	keeps := func(t *testing.T, e *env) {
		t.Helper()
		for _, f := range []string{FilePending, FileStarts} {
			if !exists(filepath.Join(e.state, f)) {
				t.Errorf("%s removed although the restore failed: the guard would never recover this node", f)
			}
		}
		if string(readFile(t, e.exe+".prev")) != string(oldBin) {
			t.Error(".prev gone after a failed restore")
		}
	}
	blockExe := func(e *env) {
		t.Helper()
		if err := os.Remove(e.exe); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(e.exe, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	armed := func(t *testing.T) (*env, *Updater) {
		e := newEnv(t)
		b, _ := jsonMarshal(marker)
		_ = os.WriteFile(filepath.Join(e.state, FilePending), b, 0o600)
		_ = os.WriteFile(filepath.Join(e.state, FileStarts), []byte("1"), 0o600)
		_ = os.WriteFile(e.exe+".prev", oldBin, 0o755)
		_ = os.WriteFile(e.exe, newBin, 0o755)
		u := e.updater(func(c *Config) { c.Built, c.Version = newBuilt, "0.2.0-new" })
		if st := u.Startup(); st.Pending == nil {
			t.Fatal("not in the commit window")
		}
		blockExe(e)
		return e, u
	}
	t.Run("self-rollback", func(t *testing.T) {
		e, u := armed(t)
		if _, err := u.RollbackNow(ReasonNotCommitted); err == nil {
			t.Fatal("a failed rename was reported as success")
		}
		keeps(t, e)
	})
	t.Run("manual rollback of an uncommitted build", func(t *testing.T) {
		e, u := armed(t)
		if res, fin := u.Rollback(context.Background(), &pb.RollbackAgent{RequestId: "rb"}); res.Ok || fin != nil {
			t.Fatalf("a failed rename was reported as success: %v", res)
		}
		keeps(t, e)
	})
	t.Run("exec failure after the swap", func(t *testing.T) {
		e := newEnv(t)
		u := e.updater(func(c *Config) {
			c.Exec = func(string, []string, []string) error {
				blockExe(e) // the restore below now fails
				return errors.New("exec format error")
			}
		})
		man, sig := e.manifest(newBin, newBuilt, nil)
		res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
		if !res.Ok || fin == nil {
			t.Fatal(res)
		}
		_ = os.WriteFile(filepath.Join(e.state, FileStarts), []byte("1"), 0o600)
		if err := fin(); err == nil {
			t.Fatal("exec failure was swallowed")
		}
		keeps(t, e)
	})
}
