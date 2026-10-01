//go:build linux

package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// runGuard runs the unit's crash-loop guard the way systemd does: sh -c '<script>' mistgate-guard <bin> <state-dir>.
func runGuard(t *testing.T, sh, bin, state string) {
	t.Helper()
	out, err := exec.Command(sh, "-c", GuardScript, "mistgate-guard", bin, state).CombinedOutput()
	if err != nil { // it must exit 0 whatever happens
		t.Fatalf("guard exited with %v: %s", err, out)
	}
}

func guardShells(t *testing.T) []string {
	var shells []string
	for _, s := range []string{"sh", "dash", "bash"} { // /bin/sh is dash on Debian and Ubuntu, bash elsewhere
		if p, err := exec.LookPath(s); err == nil {
			shells = append(shells, p)
		}
	}
	if len(shells) == 0 {
		t.Skip("no POSIX shell")
	}
	return shells
}

// TestGuardRestoresPreviousOnTheThirdStart plays three systemd starts of a freshly updated build that keeps crashing.
func TestGuardRestoresPreviousOnTheThirdStart(t *testing.T) {
	for _, sh := range guardShells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			dir, state := t.TempDir(), t.TempDir()
			bin := filepath.Join(dir, "mistgate-node")
			mustWrite(t, bin, "NEW build that crashes")
			mustWrite(t, bin+".prev", "OLD build")
			marker := `{"from_version":"0.1.0-old","from_built":1000,"to_version":"0.2.0-new","to_built":2000,"started_unix":1}`
			mustWrite(t, filepath.Join(state, FilePending), marker)

			runGuard(t, sh, bin, state) // start 1
			if got := readStr(t, filepath.Join(state, FileStarts)); got != "1" {
				t.Fatalf("after start 1 counter = %q", got)
			}
			runGuard(t, sh, bin, state) // start 2
			if got := readStr(t, filepath.Join(state, FileStarts)); got != "2" {
				t.Fatalf("after start 2 counter = %q", got)
			}
			if readStr(t, bin) != "NEW build that crashes" || exists(filepath.Join(state, FileRolledBack)) {
				t.Fatal("restored too early")
			}
			runGuard(t, sh, bin, state) // start 3: restore
			if got := readStr(t, bin); got != "OLD build" {
				t.Fatalf("binary after start 3 = %q", got)
			}
			if exists(bin+".prev") || exists(filepath.Join(state, FilePending)) || exists(filepath.Join(state, FileStarts)) {
				t.Error(".prev, marker or counter left behind")
			}
			if readStr(t, filepath.Join(state, FileRolledBack)) != marker || strings.TrimSpace(readStr(t, filepath.Join(state, FileRolledBackWhy))) != "crash_loop" {
				t.Error("rolledback files wrong")
			}

			// The restored build reads it and reports the rollback.
			u := New(Config{StateDir: state, ExePath: bin, Version: "0.1.0-old", Built: 1000, GOOS: "linux", Now: time.Now})
			if st := u.Startup(); st.Pending != nil || st.Finish != nil {
				t.Fatalf("startup = %+v", st)
			}
			o := u.Outcome()
			if o == nil || o.Outcome != pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK || o.Reason != "crash_loop" || o.ToVersion != "0.2.0-new" || o.FromBuilt != 1000 {
				t.Errorf("outcome = %v", o)
			}
			if exists(filepath.Join(state, FileRolledBack)) {
				t.Error("rolledback not consumed")
			}
		})
	}
}

func TestGuardEdgeCases(t *testing.T) {
	sh := guardShells(t)[0]
	t.Run("no marker: nothing happens and the counter is cleared", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		bin := filepath.Join(dir, "mistgate-node")
		mustWrite(t, bin, "CURRENT")
		mustWrite(t, bin+".prev", "OLD")
		mustWrite(t, filepath.Join(state, FileStarts), "2")
		for i := 0; i < 5; i++ {
			runGuard(t, sh, bin, state)
		}
		if readStr(t, bin) != "CURRENT" || exists(filepath.Join(state, FileStarts)) || exists(filepath.Join(state, FileRolledBack)) {
			t.Error("guard acted without a marker")
		}
	})
	t.Run("third start without .prev leaves everything as it is", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		bin := filepath.Join(dir, "mistgate-node")
		mustWrite(t, bin, "NEW")
		mustWrite(t, filepath.Join(state, FilePending), "{}")
		for i := 0; i < 4; i++ {
			runGuard(t, sh, bin, state)
		}
		if readStr(t, bin) != "NEW" || !exists(filepath.Join(state, FilePending)) || exists(filepath.Join(state, FileRolledBack)) {
			t.Error("guard acted without a previous build")
		}
	})
	t.Run("a garbage counter starts over", func(t *testing.T) {
		dir, state := t.TempDir(), t.TempDir()
		bin := filepath.Join(dir, "mistgate-node")
		mustWrite(t, bin, "NEW")
		mustWrite(t, filepath.Join(state, FilePending), "{}")
		mustWrite(t, filepath.Join(state, FileStarts), "lots; rm -rf /")
		runGuard(t, sh, bin, state)
		if got := readStr(t, filepath.Join(state, FileStarts)); got != "1" {
			t.Errorf("counter = %q", got)
		}
	})
	t.Run("an unwritable state dir still exits 0", func(t *testing.T) {
		runGuard(t, sh, "/nonexistent/bin", "/nonexistent/state")
	})
	t.Run("a path with shell metacharacters is data, not code", func(t *testing.T) {
		dir := t.TempDir()
		state := filepath.Join(dir, "st ate;touch pwned")
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(state, FilePending), "{}")
		runGuard(t, sh, filepath.Join(dir, "bin $(touch pwned2)"), state)
		if exists(filepath.Join(dir, "pwned")) || exists("pwned") || exists(filepath.Join(dir, "pwned2")) {
			t.Error("guard executed a path as code")
		}
		if readStr(t, filepath.Join(state, FileStarts)) != "1" {
			t.Error("counter not written in the odd directory")
		}
	})
}

// TestUpdateThenCrashLoopEndToEnd is the whole chain without systemd: Apply swaps the binary and writes the marker,
// the "new build" is started by the supervisor three times, the guard restores the previous build, which reports it.
func TestUpdateThenCrashLoopEndToEnd(t *testing.T) {
	sh := guardShells(t)[0]
	e := newEnv(t)
	u := e.updater(nil)
	man, sig := e.manifest(newBin, newBuilt, nil)
	res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
	if !res.Ok {
		t.Fatal(res)
	}
	_ = fin() // the re-exec does not count as a supervisor start
	runGuard(t, sh, e.exe, e.state)
	runGuard(t, sh, e.exe, e.state)
	if readStr(t, e.exe) != string(newBin) {
		t.Fatal("restored before the third start")
	}
	runGuard(t, sh, e.exe, e.state)
	if readStr(t, e.exe) != string(oldBin) {
		t.Fatal("previous build not restored after the third start")
	}
	old := e.updater(nil)
	old.Startup()
	if o := old.Outcome(); o == nil || o.Reason != "crash_loop" || o.ToBuilt != newBuilt || o.FromBuilt != ownBuilt {
		t.Errorf("outcome = %v", o)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readStr(t *testing.T, p string) string {
	t.Helper()
	return strings.TrimRight(string(readFile(t, p)), "\n")
}
