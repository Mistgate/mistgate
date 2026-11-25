package fleet

import (
	"testing"
	"time"
)

func TestOwnGap(t *testing.T) {
	const ka = 20 * time.Second
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	type frame struct {
		id string
		s  int
	}
	cases := []struct {
		name   string
		frames []frame
		ask    int // seconds of the last contact of the node asking
		hit    bool
	}{
		{"one node silent: no gap", []frame{{"de1", 0}, {"de1", 10}, {"de1", 100}}, 10, false},
		{"two nodes silent: gap", []frame{{"de1", 0}, {"nl1", 5}, {"de1", 75}}, 5, true},
		{"short silence: no gap", []frame{{"de1", 0}, {"nl1", 5}, {"de1", 30}}, 5, false},
		{"contact long before the gap is a blip of the node", []frame{{"de1", 0}, {"nl1", 100}, {"de1", 110}, {"fi1", 112}, {"de1", 200}}, 20, false},
		{"contact after the gap start window is inside", []frame{{"de1", 0}, {"nl1", 5}, {"de1", 75}}, -15, true},
		{"the other node was heard long before the silence", []frame{{"de1", 0}, {"nl1", 100}, {"de1", 200}}, 100, false},
	}
	for _, c := range cases {
		var g ownGap
		for _, f := range c.frames {
			g.heard(f.id, at(f.s), ka)
		}
		_, hit, first := g.gapAt(at(c.ask), ka)
		if hit != c.hit || (hit && !first) {
			t.Errorf("%s: hit=%v first=%v, want hit=%v", c.name, hit, first, c.hit)
		}
	}

	var g ownGap
	g.heard("de1", at(0), ka)
	g.heard("nl1", at(5), ka)
	g.heard("de1", at(75), ka)
	if secs, _, _ := g.gapAt(at(5), ka); secs != 70 {
		t.Errorf("silence = %d s, want 70", secs)
	}
	if _, hit, first := g.gapAt(at(5), ka); !hit || first {
		t.Errorf("the second node asking: hit=%v first=%v, want a hit that is not first", hit, first)
	}
	// a gap older than 15 minutes is dropped, and at most 8 are kept
	g.heard("nl1", at(76), ka)
	g.heard("de1", at(76+16*60), ka)
	if _, hit, _ := g.gapAt(at(5), ka); hit {
		t.Error("an old gap is still remembered")
	}
	var many ownGap
	s := 0
	many.heard("de1", at(s), ka)
	many.heard("nl1", at(s), ka)
	for range 12 {
		s += 50
		many.heard("de1", at(s), ka)
		many.heard("nl1", at(s), ka)
	}
	if n := len(many.spans); n != ownGapMax {
		t.Errorf("%d gaps kept, want %d", n, ownGapMax)
	}
}
