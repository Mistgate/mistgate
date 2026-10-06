//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
)

// A live database (00018) with a node: 00019 adds one column with a default; the old row stays readable after later
// migrations, the column rejects what is not JSON, rolling back drops it, and migrations apply again.
func TestAwgPrepareMigrationUpDownUp(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 18); err != nil {
		t.Fatalf("up to 18: %v", err)
	}
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at, awg_backend) VALUES ('nod_a', 'na', 'a.example.com', 'active', 1, 'userspace')`)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up to latest: %v", err)
	}
	n, err := s.Node(ctx, "nod_a")
	if err != nil || n.AwgPrepareJSON != "" || n.AwgBackend != "userspace" || n.AwgPrepare() != (AwgPrepareRow{}) || n.TorrentBlockerEnabled {
		t.Fatalf("an existing node after migration = %+v, %v", n, err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE node SET awg_prepare_json = 'not json' WHERE id = 'nod_a'`); err == nil {
		t.Fatal("a non-JSON state was accepted")
	}
	if err := s.SetAwgPrepare(ctx, "nod_a", AwgPrepareRow{State: AwgPrepareFailed, Since: 9, Code: "timeout", Reason: "slow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 18); err != nil {
		t.Fatalf("down to 18: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM node WHERE id = 'nod_a' AND awg_backend = 'userspace'`) != 1 {
		t.Fatal("down touched the node")
	}
	if _, err := s.W.ExecContext(ctx, `SELECT awg_prepare_json FROM node`); err == nil {
		t.Fatal("the column is left after down")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n, _ := s.Node(ctx, "nod_a"); n.AwgPrepareJSON != "" {
		t.Fatalf("not empty after up again: %q", n.AwgPrepareJSON)
	}
}

func TestAwgPrepareStateMachine(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_a', 'na', 'a.example.com', 'active', 1)`)
	get := func() (AwgPrepareRow, string) {
		n, err := s.Node(ctx, "nod_a")
		if err != nil {
			t.Fatal(err)
		}
		return n.AwgPrepare(), n.AwgBackend
	}
	if r, b := get(); r != (AwgPrepareRow{}) || b != "auto" {
		t.Fatalf("fresh node = %+v %q", r, b)
	}

	// The admin asked: running, wanted. The node says "started" later: it keeps the first start time.
	if err := s.SetAwgPrepare(ctx, "nod_a", AwgPrepareRow{State: AwgPrepareRunning, Since: 100, Want: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AwgPrepareStarted(ctx, "nod_a", 105); err != nil {
		t.Fatal(err)
	}
	if r, _ := get(); r.State != AwgPrepareRunning || r.Since != 100 || !r.Want {
		t.Fatalf("after started = %+v", r)
	}

	// A failure changes nothing but the record, and ends the wish: the backend is never "kernel" for a failed build.
	if sw, err := s.AwgPrepareFinish(ctx, "nod_a", false, 160, "step_failed", "step 2 of 5 failed"); err != nil || sw {
		t.Fatalf("failed: switched %v, %v", sw, err)
	}
	if r, b := get(); r.State != AwgPrepareFailed || r.Code != "step_failed" || r.Reason == "" || r.Want || b != "auto" {
		t.Fatalf("after failure = %+v %q", r, b)
	}

	// A retry, then success: awg_backend becomes "kernel" in the same step, once.
	if err := s.AwgPrepareStarted(ctx, "nod_a", 200); err != nil { // a node-reported start of a run the admin did not ask for: no wish
		t.Fatal(err)
	}
	if r, _ := get(); r.State != AwgPrepareRunning || r.Since != 200 || r.Want || r.Code != "" {
		t.Fatalf("restarted = %+v", r)
	}
	if sw, _ := s.AwgPrepareFinish(ctx, "nod_a", true, 300, "", ""); sw {
		t.Fatal("switched a node nobody asked to switch")
	}
	if r, b := get(); r.State != AwgPrepareDone || b != "auto" {
		t.Fatalf("done without a wish = %+v %q", r, b)
	}
	_ = s.SetAwgPrepare(ctx, "nod_a", AwgPrepareRow{State: AwgPrepareRunning, Since: 400, Want: true})
	if sw, err := s.AwgPrepareFinish(ctx, "nod_a", true, 500, "", ""); err != nil || !sw {
		t.Fatalf("done with a wish: switched %v, %v", sw, err)
	}
	if r, b := get(); r.State != AwgPrepareDone || r.Since != 500 || r.Want || b != "kernel" {
		t.Fatalf("after success = %+v %q", r, b)
	}
	// the same "done" twice (an event replayed) switches nothing more
	if sw, _ := s.AwgPrepareFinish(ctx, "nod_a", true, 501, "", ""); sw {
		t.Fatal("a replayed done switched again")
	}

	// A backend chosen by hand while a build runs ends the wish: the finished build does not override the admin.
	_ = s.SetAwgPrepare(ctx, "nod_a", AwgPrepareRow{State: AwgPrepareRunning, Since: 600, Want: true})
	us := "userspace"
	if _, err := s.UpdateNode(ctx, "nod_a", NodePatch{AwgBackend: &us}); err != nil {
		t.Fatal(err)
	}
	if r, b := get(); r.State != AwgPrepareRunning || r.Want || b != "userspace" {
		t.Fatalf("after the admin chose userspace = %+v %q", r, b)
	}
	if sw, _ := s.AwgPrepareFinish(ctx, "nod_a", true, 700, "", ""); sw {
		t.Fatal("the finished build overrode the admin's choice")
	}
	if _, b := get(); b != "userspace" {
		t.Fatalf("backend = %q", b)
	}
	// a node row that is not there
	if err := s.AwgPrepareStarted(ctx, "nod_zz", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown node: %v", err)
	}
	// editing something else leaves the wish alone
	_ = s.SetAwgPrepare(ctx, "nod_a", AwgPrepareRow{State: AwgPrepareRunning, Since: 800, Want: true})
	note := "hello"
	if _, err := s.UpdateNode(ctx, "nod_a", NodePatch{Notes: &note}); err != nil {
		t.Fatal(err)
	}
	if r, _ := get(); !r.Want {
		t.Fatal("editing the notes dropped the wish")
	}
}
