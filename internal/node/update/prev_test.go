package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// A .prev that is not the file Apply recorded is not installed, and a .prev with no record is not either.
func TestRollbackRefusesAlteredPrev(t *testing.T) {
	e := newEnv(t)
	u := e.updater(nil)
	man, sig := e.manifest(newBin, newBuilt, nil)
	res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
	if !res.Ok || fin == nil {
		t.Fatalf("apply: %v", res)
	}
	// the "new" process after the exec: same files, Built of the new build
	nu := New(Config{StateDir: e.state, ExePath: e.exe, Version: "0.2.0-new", Built: newBuilt, Key: e.pub, UnitGen: 2,
		Now: func() time.Time { return e.now }, GOOS: "linux", GOARCH: "amd64", Args: []string{e.exe, "run"}, Env: []string{"A=b"},
		Exec: func(string, []string, []string) error { return nil }})
	nu.Startup()
	if _, err := nu.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.exe+".prev", []byte("NOT A RELEASE"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, fin2 := nu.Rollback(context.Background(), &pb.RollbackAgent{RequestId: "rb"})
	if r.Ok || fin2 != nil || r.Error != "no_previous" {
		t.Fatalf("rollback of an altered .prev: %v", r)
	}
	if string(readFile(t, e.exe)) != string(newBin) {
		t.Fatal("the executable changed")
	}
	// the record is gone: nothing vouches for the file
	_ = os.Remove(filepath.Join(e.state, FilePrev))
	if r, _ := nu.Rollback(context.Background(), &pb.RollbackAgent{RequestId: "rb2"}); r.Ok || r.Error != "no_previous" {
		t.Fatalf("rollback without a record: %v", r)
	}
}

// An unmodified .prev is restored and its record is consumed with it.
func TestRollbackAfterApplyRestoresRecordedPrev(t *testing.T) {
	e := newEnv(t)
	u := e.updater(nil)
	man, sig := e.manifest(newBin, newBuilt, nil)
	if res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin)); !res.Ok || fin == nil {
		t.Fatalf("apply: %v", res)
	}
	nu := New(Config{StateDir: e.state, ExePath: e.exe, Version: "0.2.0-new", Built: newBuilt, Key: e.pub, UnitGen: 2,
		Now: func() time.Time { return e.now }, GOOS: "linux", GOARCH: "amd64", Args: []string{e.exe, "run"}, Env: []string{"A=b"},
		Exec: func(string, []string, []string) error { return nil }})
	nu.Startup()
	if _, err := nu.Commit(); err != nil {
		t.Fatal(err)
	}
	if r, fin := nu.Rollback(context.Background(), &pb.RollbackAgent{RequestId: "rb"}); !r.Ok || fin == nil {
		t.Fatalf("rollback: %v", r)
	}
	if string(readFile(t, e.exe)) != string(oldBin) || exists(filepath.Join(e.state, FilePrev)) {
		t.Fatal("the recorded build was not restored or its record stayed")
	}
}

// An update that is refused after the copy was made leaves the older rollback target (and its record) as they were.
func TestFailedUpdateKeepsOlderRollbackTarget(t *testing.T) {
	e := newEnv(t)
	writePrev(t, e, "the build before this one")
	recBefore := readFile(t, filepath.Join(e.state, FilePrev))
	if err := os.Mkdir(filepath.Join(e.state, FilePending+".tmp"), 0o700); err != nil { // makes writeAtomic(update.pending) fail
		t.Fatal(err)
	}
	u := e.updater(nil)
	man, sig := e.manifest(newBin, newBuilt, nil)
	res, fin := u.Apply(context.Background(), e.req(man, sig), e.src(newBin))
	if res.Ok || fin != nil || res.Error != "not_writable" {
		t.Fatalf("expected a refused update, got %v", res)
	}
	if string(readFile(t, e.exe)) != string(oldBin) {
		t.Fatal("exe changed although the update failed")
	}
	if got := string(readFile(t, e.exe+".prev")); got != "the build before this one" {
		t.Fatalf(".prev = %q", got)
	}
	if string(readFile(t, filepath.Join(e.state, FilePrev))) != string(recBefore) {
		t.Fatal("the record changed")
	}
	if exists(e.exe+".prev.new") || exists(e.exe+".prev.old") {
		t.Fatal("staging files left behind")
	}
}
