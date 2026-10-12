package store

import (
	"context"
	"testing"
	"time"
)

// Checks shared by the SQLite test (provision_test.go) and the fake-D1 test (d1_store_test.go): the attempt column and
// the pending lookups must behave the same on both databases.

func newAttemptJob(id string, at time.Time) NodeProvisionJob {
	return NodeProvisionJob{ID: id, NodeID: "node_" + id, Name: "node-" + id, Address: "203.0.113.10", SSHHost: "203.0.113.10", SSHPort: 22,
		HostFingerprint: "SHA256:example", Secret: []byte("sealed"), CreatedBy: "admin", CreatedAt: at, UpdatedAt: at}
}

func jobAttempts(t *testing.T, st *Store, id string) int {
	t.Helper()
	var n int
	if err := st.R.QueryRowContext(context.Background(), `SELECT attempt FROM node_provision_job WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func jobEventCodes(t *testing.T, st *Store, id string) []string {
	t.Helper()
	events, _, err := st.NodeProvisionEvents(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, e := range events {
		codes = append(codes, e.Code)
	}
	return codes
}

// checkProvisionAttemptCap: a claim counts an attempt; the restart that interrupts the MaxProvisionAttempts-th one fails
// the job with job_interrupted and erases its secret, and an owner's retry starts the count again.
func checkProvisionAttemptCap(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Unix(1_800_000_000, 0).UTC()
	if err := st.CreateNodeProvisionJob(ctx, newAttemptJob("cap", t0)); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= MaxProvisionAttempts; n++ {
		if _, ok, err := st.ClaimNodeProvisionJob(ctx, t0.Add(time.Duration(n)*time.Minute)); err != nil || !ok {
			t.Fatalf("claim %d: ok=%v err=%v", n, ok, err)
		}
		if got := jobAttempts(t, st, "cap"); got != n {
			t.Fatalf("attempt after claim %d = %d", n, got)
		}
		if err := st.RequeueNodeProvisionJobs(ctx, t0.Add(time.Duration(n)*time.Minute+time.Second)); err != nil {
			t.Fatal(err)
		}
		state, err := st.NodeProvisionJobState(ctx, "cap")
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{true: "queued", false: "failed"}[n < MaxProvisionAttempts]; state != want {
			t.Fatalf("state after restart %d = %q, want %q", n, state, want)
		}
	}
	job, err := st.NodeProvisionJob(ctx, "cap")
	if err != nil || job.State != "failed" || job.Phase != "failed" || job.ErrorCode != "job_interrupted" || len(job.Secret) != 0 {
		t.Fatalf("interrupted job = %+v, err %v", job, err)
	}
	codes := jobEventCodes(t, st, "cap")
	resumed, interrupted := 0, 0
	for _, c := range codes {
		switch c {
		case "resumed_after_restart":
			resumed++
		case "job_interrupted":
			interrupted++
		}
	}
	if resumed != MaxProvisionAttempts-1 || interrupted != 1 || codes[len(codes)-1] != "job_interrupted" {
		t.Fatalf("events = %v, want %d resumes then one job_interrupted", codes, MaxProvisionAttempts-1)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, t0.Add(time.Hour)); err != nil || ok {
		t.Fatalf("a failed job was claimed again: ok=%v err=%v", ok, err)
	}

	// retry: all attempts again, and the first restart does not fail it
	if err := st.RetryNodeProvisionJob(ctx, "cap", "failed", []byte("sealed-2"), t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := jobAttempts(t, st, "cap"); got != 0 {
		t.Fatalf("attempt after retry = %d, want 0", got)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, t0.Add(3*time.Hour)); err != nil || !ok {
		t.Fatalf("claim after retry: ok=%v err=%v", ok, err)
	}
	if err := st.RequeueNodeProvisionJobs(ctx, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.NodeProvisionJobState(ctx, "cap"); state != "queued" || jobAttempts(t, st, "cap") != 1 {
		t.Fatalf("after a retry the first interruption left state %q, attempt %d", state, jobAttempts(t, st, "cap"))
	}
}

// A cancel_requested job whose runner is gone becomes cancelled, never job_interrupted, and other states are untouched.
func checkProvisionRequeueLeavesOtherStates(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Unix(1_800_000_000, 0).UTC()
	for _, id := range []string{"queued", "cancelling"} {
		if err := st.CreateNodeProvisionJob(ctx, newAttemptJob(id, t0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE node_provision_job SET state = 'cancel_requested', attempt = ? WHERE id = 'cancelling'`, MaxProvisionAttempts); err != nil {
		t.Fatal(err)
	}
	if err := st.RequeueNodeProvisionJobs(ctx, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.NodeProvisionJobState(ctx, "queued"); state != "queued" {
		t.Fatalf("a queued job became %q", state)
	}
	job, err := st.NodeProvisionJob(ctx, "cancelling")
	if err != nil || job.State != "cancelled" || job.ErrorCode != "remote_outcome_unknown" {
		t.Fatalf("cancel_requested job = %+v, err %v", job, err)
	}
}

func checkProvisionPending(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Unix(1_800_000_000, 0).UTC()
	expect := func(step string, pending, queued bool) {
		t.Helper()
		gotPending, err := st.NodeProvisionJobsPending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gotQueued, err := st.NodeProvisionJobsQueued(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if gotPending != pending || gotQueued != queued {
			t.Fatalf("%s: pending %v queued %v, want %v %v", step, gotPending, gotQueued, pending, queued)
		}
	}
	expect("no jobs", false, false)
	if err := st.CreateNodeProvisionJob(ctx, newAttemptJob("pend", t0)); err != nil {
		t.Fatal(err)
	}
	expect("queued", true, true)
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, t0); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	expect("running", true, false)
	if _, _, err := st.RequestCancelNodeProvisionJob(ctx, "pend", t0); err != nil {
		t.Fatal(err)
	}
	expect("cancel requested", true, false)
	if _, err := st.FinishCancelledNodeProvisionJob(ctx, "pend", t0); err != nil {
		t.Fatal(err)
	}
	expect("cancelled", false, false)
}
