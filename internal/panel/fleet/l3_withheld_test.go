package fleet

import (
	"strings"
	"testing"
	"time"
)

// An awg inbound added while an old agent (no "awg/1") is connected changes nothing in what that agent is sent, so no
// delta goes out and no ApplyResult ever comes back: the inbound has to be marked failed by the reconcile itself, or the
// node page shows it as pending for ever. Found by scripts/e2e-wsl.sh --old-node.
func TestAwgInboundAddedOnAnOldAgentIsFailedWithoutAnApplyResult(t *testing.T) {
	x, a := newL3Env(t)
	x.src.mu.Lock()
	x.src.awgOn = false // the inbound does not exist yet when the agent connects
	x.src.mu.Unlock()
	c, m, _ := connectFull(a, "old")
	if len(m.in) != 1 {
		t.Fatalf("an old agent holds %v", keys(m.in))
	}
	if r := x.inboundRow("inb_awg"); r.State == "failed" {
		t.Fatalf("the inbound is failed before it is part of the desired state: %+v", r)
	}

	x.src.mu.Lock()
	x.src.awgOn = true // the owner adds the inbound
	x.src.mu.Unlock()
	x.f.StateChanged()
	within(t, "the withheld inbound is marked failed with no apply result", func() bool {
		r := x.inboundRow("inb_awg")
		return r.State == "failed" && strings.HasPrefix(r.LastError, "agent_too_old")
	})
	c.quiet(400 * time.Millisecond) // and the old agent is told nothing

	// The same reconcile again does not rewrite the row (the update is conditional).
	updated := func() (v int64) {
		x.st.R.QueryRow(`SELECT updated_at FROM inbound WHERE id = 'inb_awg'`).Scan(&v)
		return
	}
	before := updated()
	x.clock.add(time.Hour)
	x.f.StateChanged()
	time.Sleep(150 * time.Millisecond)
	if after := updated(); after != before {
		t.Errorf("an unchanged withheld inbound was rewritten: %d -> %d", before, after)
	}
}

func TestSessionCoreReconcileDoesNotFailWithheldInbounds(t *testing.T) {
	x, a := newL3Env(t)
	x.src.mu.Lock()
	x.src.awgOn = false
	x.src.mu.Unlock()
	_, _, _ = connectCaps(a, "old")

	row := x.inboundRow("inb_awg")
	if row.State == "failed" {
		t.Fatalf("inbound was failed before it became withheld: %+v", row)
	}

	x.src.mu.Lock()
	x.src.awgOn = true
	x.src.mu.Unlock()
	x.f.mu.Lock()
	s := x.f.sessions[a.nodeID]
	x.f.mu.Unlock()
	if s == nil {
		t.Fatal("connected session was not registered")
	}
	prepared, err := x.f.prepareDesiredState(s.ctx, a.nodeID, s.caps)
	if err != nil {
		t.Fatal(err)
	}

	s.coreMu.Lock()
	state, sidecar := s.coreState, s.coreSidecar
	_, err = s.core.Step(s.ctx, state, sidecar, SessionEvent{Kind: EventDesiredChanged, At: x.clock.now().UTC(),
		Mode: reconcileChange, Prepared: prepared})
	s.coreMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if row = x.inboundRow("inb_awg"); row.State == "failed" {
		t.Fatalf("SessionCore.Step performed the withheld-inbound store write: %+v", row)
	}
}
