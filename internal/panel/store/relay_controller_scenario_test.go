package store

import (
	"fmt"
	"testing"
	"time"
)

// A minute-by-minute model of what the panel does around the controller (the client-eye round, the diagnosis a failed round
// starts, the direct leg probe every 30 minutes in reverse, the bridge report, the 8 MB proof it runs itself), against a world
// that says what works when. The table in relay_controller_test.go tests single decisions; these tests are about chatter and
// about time: how often the way changes while the world keeps changing its mind.
type relaySim struct {
	t        *testing.T
	min      int // minutes since the start
	in       RelayModeInput
	legOK    func(min int) bool // the direct leg's packets pass
	flowOK   func(min int) bool // an 8 MB flow of the direct way passes (false: freezes after 16 KB)
	bridge   func(min int) bool // the bridge holds and carries people
	reach    bool               // the exit reaches the entry's site
	switches []string
	proofs   []int // minutes the 8 MB flow was run
	diagAt   int
}

func newRelaySim(t *testing.T, active string) *relaySim {
	s := &relaySim{t: t, diagAt: -1000, reach: true}
	s.in = RelayModeInput{Mode: RelayModeAuto, ActiveMode: active, Network: "tcp", EntrySupported: true, ExitSupported: true,
		Now: controllerNow, ModeChangedAt: controllerNow.Add(-24 * time.Hour)}
	return s
}

func (s *relaySim) now() time.Time { return controllerNow.Add(time.Duration(s.min) * time.Minute) }

func (s *relaySim) at() int64 { return s.now().Unix() }

func okOrFail(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

// tick is one minute: a client-eye round, what follows it, and one run of the controller (twice when it asks for the proof).
func (s *relaySim) tick() {
	in := &s.in
	in.Now = s.now()
	// the client-eye round through whichever way stands now
	ok := s.flowOK(s.min)
	if in.ActiveMode == RelayModeReverse {
		ok = s.bridge(s.min)
	}
	if ok {
		in.MonitorFailStreak, in.MonitorFailAt, in.MonitorStage = 0, 0, ""
	} else {
		in.MonitorFailStreak++
		in.MonitorFailAt, in.MonitorStage = s.at(), "path"
		if in.ActiveMode == RelayModeDNAT && s.legOK(s.min) {
			in.MonitorStage = "freeze"
		}
		if s.min-s.diagAt >= 30 { // the diagnosis a failed round starts, at most every 30 minutes
			s.diagAt = s.min
			in.Leg = s.legStage()
			if s.reach {
				in.ReverseReach = stageAt("ok", s.now())
			}
		}
	}
	if in.ActiveMode == RelayModeReverse && (in.Leg.At == 0 || s.at()-in.Leg.At >= int64(30*time.Minute/time.Second)) {
		in.Leg = s.legStage() // the leg probe of a reverse relay, every 30 minutes
	}
	// the bridge is held while reverse, and is warmed when the way is wrong (leg failed or a flow froze) or just after a return
	wanted := in.ActiveMode == RelayModeReverse ||
		(in.Leg.Status == "fail" && freshStage(in.Leg, in.Now, relayModeCheckFresh)) || (in.MonitorFailStreak > 0 && in.MonitorStage == "freeze") ||
		(in.ModeReason == "direct_back" && in.Now.Sub(in.ModeChangedAt) < relayModeHold)
	if wanted {
		in.Bridge = stageAt(okOrFail(s.bridge(s.min)), s.now())
	}
	s.decide()
}

func (s *relaySim) legStage() RelayStage { return stageAt(okOrFail(s.legOK(s.min)), s.now()) }

func (s *relaySim) decide() {
	in := &s.in
	for round := 0; round < 2; round++ {
		d := EvaluateRelayMode(*in)
		in.DirectStreak, in.DirectStreakAt = d.DirectStreak, d.DirectStreakAt
		if d.ActiveMode != in.ActiveMode {
			s.switches = append(s.switches, fmt.Sprintf("%d:%s(%s)", s.min, d.ActiveMode, d.ModeReason))
			in.ActiveMode, in.ModeChangedAt = d.ActiveMode, s.now()
			// the monitor of the old way says nothing about the new one
			in.MonitorFailStreak, in.MonitorFailAt, in.MonitorStage = 0, 0, ""
		}
		in.ModeReason = d.ModeReason
		if !d.NeedDirectSpeed {
			return
		}
		// the daily limit: a node takes 6 tests in 24 hours, one that cannot start is skipped
		recent := 0
		for _, m := range s.proofs {
			if s.min-m < 24*60 {
				recent++
			}
		}
		if recent >= 6 {
			in.DirectSpeed = RelayStage{Status: "skipped", Code: "busy", At: s.at()}
			continue
		}
		s.proofs = append(s.proofs, s.min)
		in.DirectSpeed = RelayStage{Status: okOrFail(s.flowOK(s.min)), At: s.at(), MB: 8}
	}
}

func (s *relaySim) run(minutes int) {
	for i := 0; i < minutes; i++ {
		s.tick()
		s.min++
	}
}

func always(v bool) func(int) bool { return func(int) bool { return v } }

func TestRelayModeOverTime(t *testing.T) {
	t.Run("a dead direct way goes to the bridge in minutes and stays there", func(t *testing.T) {
		s := newRelaySim(t, RelayModeDNAT)
		s.legOK, s.flowOK, s.bridge = always(false), always(false), always(true)
		s.run(4)
		if s.in.ActiveMode != RelayModeReverse {
			t.Fatalf("still direct after 4 minutes: %v", s.switches)
		}
		s.run(12 * 60)
		if len(s.switches) != 1 || s.in.ActiveMode != RelayModeReverse || len(s.proofs) != 0 {
			t.Fatalf("switches=%v proofs=%v", s.switches, s.proofs)
		}
	})
	t.Run("a blinking direct leg never takes the relay back", func(t *testing.T) {
		s := newRelaySim(t, RelayModeReverse)
		s.legOK = func(m int) bool { return m/20%2 == 0 } // 20 minutes up, 20 down
		s.flowOK, s.bridge = always(true), always(true)
		s.run(24 * 60)
		if len(s.switches) != 0 || len(s.proofs) != 0 {
			t.Fatalf("a blinking leg changed the way: switches=%v proofs=%v", s.switches, s.proofs)
		}
	})
	t.Run("a healed direct way returns after the hold, three samples and one proof", func(t *testing.T) {
		s := newRelaySim(t, RelayModeReverse)
		s.in.ModeChangedAt = s.now()
		s.legOK, s.flowOK, s.bridge = always(true), always(true), always(true)
		s.run(4 * 60)
		if len(s.switches) != 1 || s.in.ActiveMode != RelayModeDNAT || s.in.ModeReason != "direct_back" || len(s.proofs) != 1 {
			t.Fatalf("switches=%v proofs=%v", s.switches, s.proofs)
		}
		var at int
		_, _ = fmt.Sscanf(s.switches[0], "%d:", &at)
		if at < 60 || at > 100 {
			t.Fatalf("returned at minute %d, want after an hour (three samples 30 minutes apart) and soon after", at)
		}
	})
	t.Run("packets pass but the flow freezes: the proof keeps the relay on the bridge, inside the daily limit", func(t *testing.T) {
		s := newRelaySim(t, RelayModeReverse)
		s.legOK, s.flowOK, s.bridge = always(true), always(false), always(true)
		s.run(48 * 60)
		if len(s.switches) != 0 {
			t.Fatalf("a frozen direct way was taken back: %v", s.switches)
		}
		if len(s.proofs) == 0 || len(s.proofs) > 10 {
			t.Fatalf("proofs run in 48 hours = %v (a failed proof is not repeated for 6 hours, the limit is 6 a day)", s.proofs)
		}
		for i := 1; i < len(s.proofs); i++ {
			if s.proofs[i]-s.proofs[i-1] < 6*60 {
				t.Fatalf("proofs %v closer than the 6 hours after a failure", s.proofs)
			}
		}
	})
	t.Run("a freeze in direct mode goes to the bridge, and a live leg does not take it back", func(t *testing.T) {
		s := newRelaySim(t, RelayModeDNAT)
		s.legOK, s.flowOK, s.bridge = always(true), always(false), always(true)
		s.run(24 * 60)
		if len(s.switches) != 1 || s.in.ActiveMode != RelayModeReverse {
			t.Fatalf("switches=%v", s.switches)
		}
	})
	t.Run("both ways dead: nothing to go to, no chatter", func(t *testing.T) {
		s := newRelaySim(t, RelayModeDNAT)
		s.legOK, s.flowOK, s.bridge = always(false), always(false), always(false)
		s.run(6 * 60)
		if len(s.switches) != 0 {
			t.Fatalf("switches=%v", s.switches)
		}
	})
	t.Run("a bridge that drops for a minute now and then, with the leg alive, stays", func(t *testing.T) {
		s := newRelaySim(t, RelayModeReverse)
		s.legOK, s.flowOK = always(true), always(false)
		s.bridge = func(m int) bool { return m%7 != 0 }
		s.run(12 * 60)
		if len(s.switches) != 0 {
			t.Fatalf("switches=%v", s.switches)
		}
	})
	t.Run("a bridge dead for good with a live leg goes back to direct once and does not ping-pong", func(t *testing.T) {
		s := newRelaySim(t, RelayModeReverse)
		s.legOK, s.bridge, s.flowOK = always(true), always(false), always(true)
		s.run(6 * 60)
		if len(s.switches) != 1 || s.in.ModeReason != "bridge_failed" {
			t.Fatalf("switches=%v reason=%q", s.switches, s.in.ModeReason)
		}
	})
	t.Run("after the return the direct way freezes again: the bridge is back at once and stays", func(t *testing.T) {
		s := newRelaySim(t, RelayModeReverse)
		s.in.ModeChangedAt = s.now()
		s.legOK, s.bridge = always(true), always(true)
		s.flowOK = func(m int) bool { return m < 150 } // fine until minute 150, frozen after
		s.run(8 * 60)
		if len(s.switches) < 2 || s.in.ActiveMode != RelayModeReverse {
			t.Fatalf("switches=%v active=%s", s.switches, s.in.ActiveMode)
		}
		if len(s.switches) > 4 {
			t.Fatalf("chatter: %v", s.switches)
		}
	})
}
