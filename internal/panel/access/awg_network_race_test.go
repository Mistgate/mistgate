package access

import (
	"encoding/json"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// concurrently runs the calls at once while the single writer connection is held, so that every call that gets as far
// as its database write queues up behind the others: the check-then-write race of the client networks then happens
// every time instead of once in a while. With the networks serialised the later calls wait for the first one instead,
// which is why the wait for the queue is bounded.
func (e *env) concurrently(calls ...func() error) []error {
	e.t.Helper()
	blocker, err := e.st.W.BeginTx(e.ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer blocker.Rollback()
	waiters := e.st.W.Stats().WaitCount
	start := make(chan struct{})
	res := make(chan error, len(calls))
	for _, c := range calls {
		go func() { <-start; res <- c() }()
	}
	close(start)
	queued := func(n int64) bool { return e.st.W.Stats().WaitCount >= waiters+n }
	for deadline := time.Now().Add(5 * time.Second); !queued(1) && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	for deadline := time.Now().Add(300 * time.Millisecond); !queued(int64(len(calls))) && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if err := blocker.Commit(); err != nil {
		e.t.Fatal(err)
	}
	out := make([]error, 0, len(calls))
	for range calls {
		out = append(out, <-res)
	}
	return out
}

func (e *env) awgNetworksOf(name string) [2]string {
	e.t.Helper()
	ps, err := e.st.Access().Profiles(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, p := range ps {
		if p.Name == name {
			var s struct{ Subnet4, Subnet6 string }
			if err := json.Unmarshal([]byte(p.SettingsJSON), &s); err != nil {
				e.t.Fatal(err)
			}
			return [2]string{s.Subnet4, s.Subnet6}
		}
	}
	e.t.Fatalf("profile %q not found", name)
	return [2]string{}
}

func TestConcurrentAWGProfileCreatesDoNotShareNetworks(t *testing.T) {
	e := newEnv(t)
	create := func(name string) func() error {
		return func() error {
			_, err := e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "awg", Name: name, SettingsJson: "{}"}))
			return err
		}
	}
	for _, err := range e.concurrently(create("race-awg-a"), create("race-awg-b")) {
		if err != nil {
			t.Fatalf("concurrent profile create: %v", err)
		}
	}
	if a, b := e.awgNetworksOf("race-awg-a"), e.awgNetworksOf("race-awg-b"); a == b || a[0] == b[0] || a[1] == b[1] {
		t.Fatalf("concurrent creates persisted shared client networks: %v %v", a, b)
	}
}

func TestConcurrentAWGProfileUpdatesDoNotShareNetworks(t *testing.T) {
	e := newEnv(t)
	p1 := e.awgProfile("race-upd-a", "{}")
	p2 := e.awgProfile("race-upd-b", "{}")
	update := func(id string) func() error {
		return func() error {
			_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{
				ProfileId: id, ExpectedVersion: 1, SettingsJson: new(`{"subnet4":"172.20.0.0/22"}`),
			}))
			return err
		}
	}
	ok := 0
	for _, err := range e.concurrently(update(p1.Id), update(p2.Id)) {
		if err == nil {
			ok++
		}
	}
	if a, b := e.awgNetworksOf("race-upd-a"), e.awgNetworksOf("race-upd-b"); ok != 1 || a[0] == b[0] {
		t.Fatalf("%d of 2 concurrent updates moved to the same network: %v %v", ok, a, b)
	}
}
