package health

import (
	"context"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	nodehy2 "github.com/mistgate/mistgate/internal/node/hysteria2"
)

func waitFor(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// restart replaces the engine by a fresh one that applies the same state. The certificate is new (self-signed),
// so the panel learns the new pin like it would from the next ApplyResult of the agent.
func (r *engineRig) restart(inbound string) {
	r.t.Helper()
	r.e.Close(r.ctx)
	direct := egress.New(func() []string { return nil }, egress.AllowPrivate())
	e, err := nodehy2.New(engine.Env{
		Certs:  certs.New(r.t.TempDir()),
		Egress: func(string) (engine.Egress, error) { return direct, nil },
		DNS:    func() []string { return nil },
		Now:    time.Now,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { e.Close(context.Background()) })
	r.e = e
	rep, err := e.Apply(r.ctx, r.spec, r.creds)
	if err != nil {
		r.t.Fatal(err)
	}
	r.exec(`UPDATE inbound SET cert_pin_sha256 = ? WHERE id = ?`, rep.Cert.PinSHA256, inbound)
	r.s.invalidateSnapshot()
}

// The whole module running (scheduler, workers, evaluator) against a real engine on loopback: rounds are
// stored, a dead engine opens NO_TRAFFIC after two failed rounds and turns the node status, and a restarted
// engine resolves it again.
func TestRunEndToEndAgainstARealEngine(t *testing.T) {
	r := newEngineRig(t)
	r.s.cfg.Now = time.Now
	r.s.now = time.Now
	r.s.startedAt = time.Now().Add(-time.Hour)
	r.s.cfg.Interval, r.s.cfg.Tick, r.s.cfg.FailedEvery = 300*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond
	r.s.cfg.HandshakeTimeout, r.s.cfg.RetryDelay = 600*time.Millisecond, 50*time.Millisecond
	id := r.deploy("salamander", true)

	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() { r.s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	samples := func() int {
		rows, _ := r.st.RecentSamples(r.ctx, id, 50)
		return len(rows)
	}
	waitFor(t, "two stored rounds", 10*time.Second, func() bool { return samples() >= 2 })
	rows, _ := r.st.RecentSamples(r.ctx, id, 1)
	if rows[0].Status != int(cOK) || rows[0].ExitCountry != "DE" {
		t.Fatalf("round: %+v", rows[0])
	}
	if a := r.active(); len(a) != 0 {
		t.Fatalf("alerts while healthy: %v", a)
	}

	r.e.Close(r.ctx) // the engine dies, the agent (here: the fake fleet) stays connected
	waitFor(t, "NO_TRAFFIC", 20*time.Second, func() bool { _, ok := r.active()["no_traffic/nod_l/"]; return ok })
	if nt, failed, total, _ := r.s.NodeHealth("nod_l"); !nt || failed != 1 || total != 1 {
		t.Fatalf("node health: %v %d %d", nt, failed, total)
	}

	r.restart(id)
	waitFor(t, "recovery", 20*time.Second, func() bool { return len(r.active()) == 0 })
	if nt, _, _, _ := r.s.NodeHealth("nod_l"); nt {
		t.Fatal("node still NO_TRAFFIC after recovery")
	}
	var cleared bool
	for _, h := range r.history() {
		cleared = cleared || (h.Kind == kNoTraffic && h.Resolution == "cleared")
	}
	if !cleared {
		t.Fatalf("history: %+v", r.history())
	}
}
