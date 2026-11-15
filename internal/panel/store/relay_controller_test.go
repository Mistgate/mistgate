package store

import (
	"testing"
	"time"
)

var controllerNow = time.Unix(1_800_000_000, 0)

func stageAt(status string, at time.Time) RelayStage {
	return RelayStage{Status: status, At: at.Unix()}
}

func TestEvaluateRelayMode(t *testing.T) {
	now := controllerNow
	stage := stageAt
	base := RelayModeInput{Mode: RelayModeAuto, ActiveMode: RelayModeDNAT, Network: "tcp", EntrySupported: true, ExitSupported: true,
		Now: now, Leg: stage("fail", now), ReverseReach: stage("ok", now), Bridge: stage("ok", now), MonitorFailAt: now.Unix(), MonitorFailStreak: 2}
	reverse := func(x RelayModeInput) RelayModeInput {
		x.ActiveMode, x.ModeReason, x.ModeChangedAt = RelayModeReverse, "leg_failed", now.Add(-time.Hour)
		x.Leg, x.MonitorFailAt, x.MonitorFailStreak = stage("ok", now), 0, 0
		return x
	}
	speed := func(status string, mb uint32, at time.Time) RelayStage { s := stage(status, at); s.MB = mb; return s }
	tests := []struct {
		name   string
		input  RelayModeInput
		active string
		reason string
		streak uint32
		notify bool
		need   bool
	}{
		{name: "direct leg needs two failed client rounds", input: func() RelayModeInput { x := base; x.MonitorFailStreak = 1; return x }(), active: RelayModeDNAT},
		{name: "auto moves after two failed rounds and fresh reverse stages", input: base, active: RelayModeReverse, reason: "leg_failed", notify: true},
		{name: "a stalled flow (freeze) with a live leg moves too", input: func() RelayModeInput {
			x := base
			x.Leg, x.MonitorStage = stage("ok", now), "freeze"
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", notify: true},
		{name: "a failed round with a live leg and no freeze does not", input: func() RelayModeInput { x := base; x.Leg = stage("ok", now); return x }(), active: RelayModeDNAT},
		{name: "a leg failure that is not fresh does not", input: func() RelayModeInput { x := base; x.Leg = stage("fail", now.Add(-16*time.Minute)); return x }(), active: RelayModeDNAT},
		{name: "a leg from the future does not", input: func() RelayModeInput { x := base; x.Leg = stage("fail", now.Add(time.Minute)); return x }(), active: RelayModeDNAT},
		{name: "a monitor failure from the future does not", input: func() RelayModeInput { x := base; x.MonitorFailAt = now.Add(time.Minute).Unix(); return x }(), active: RelayModeDNAT},
		{name: "stale reverse reach cannot switch", input: func() RelayModeInput {
			x := base
			x.ReverseReach = stage("ok", now.Add(-15*time.Minute-time.Second))
			return x
		}(), active: RelayModeDNAT},
		{name: "reverse reach from the future cannot switch", input: func() RelayModeInput { x := base; x.ReverseReach = stage("ok", now.Add(time.Second)); return x }(), active: RelayModeDNAT},
		{name: "a failed reverse reach cannot switch", input: func() RelayModeInput { x := base; x.ReverseReach = stage("fail", now); return x }(), active: RelayModeDNAT},
		{name: "a bridge report older than 90 seconds cannot switch", input: func() RelayModeInput {
			x := base
			x.Bridge = stage("ok", now.Add(-91*time.Second))
			return x
		}(), active: RelayModeDNAT},
		{name: "a bridge that is still warming cannot switch", input: func() RelayModeInput { x := base; x.Bridge = stage("skipped", now); return x }(), active: RelayModeDNAT},
		{name: "an agent that cannot do reverse keeps DNAT as it was", input: func() RelayModeInput { x := base; x.ExitSupported = false; return x }(), active: RelayModeDNAT},
		{name: "UDP stays direct", input: func() RelayModeInput { x := base; x.Network = "udp"; return x }(), active: RelayModeDNAT, reason: "udp"},
		{name: "capability loss immediately leaves reverse", input: func() RelayModeInput { x := base; x.ActiveMode = RelayModeReverse; x.ExitSupported = false; return x }(), active: RelayModeDNAT, reason: "reverse_unsupported", notify: true},
		{name: "owner reverse without a capability is refused on the spot", input: func() RelayModeInput { x := base; x.Mode, x.EntrySupported = RelayModeReverse, false; return x }(), active: RelayModeDNAT, reason: "reverse_unsupported"},
		{name: "owner dnat overrides everything", input: func() RelayModeInput {
			x := reverse(base)
			x.Mode, x.DirectStreak = RelayModeDNAT, 3
			return x
		}(), active: RelayModeDNAT, reason: "owner", notify: true},
		{name: "owner reverse takes effect once the bridge is up", input: func() RelayModeInput {
			x := base
			x.Mode, x.MonitorFailStreak = RelayModeReverse, 0
			return x
		}(), active: RelayModeReverse, reason: "owner", notify: true},
		{name: "owner reverse waits for the bridge", input: func() RelayModeInput {
			x := base
			x.Mode, x.Bridge = RelayModeReverse, stage("fail", now)
			return x
		}(), active: RelayModeDNAT},

		{name: "the bridge fails while the leg is alive: back to direct after two failed rounds", input: func() RelayModeInput {
			x := reverse(base)
			x.Bridge, x.MonitorFailAt, x.MonitorFailStreak = stage("fail", now), now.Unix(), 2
			return x
		}(), active: RelayModeDNAT, reason: "bridge_failed", notify: true},
		{name: "one failed round with a failed bridge is a reconnect, not a verdict", input: func() RelayModeInput {
			x := reverse(base)
			x.Bridge, x.MonitorFailAt, x.MonitorFailStreak = stage("fail", now), now.Unix(), 1
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 1},
		{name: "a failed bridge with healthy rounds stays", input: func() RelayModeInput {
			x := reverse(base)
			x.Bridge = stage("fail", now)
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 1},
		{name: "the path fails through the bridge while the leg is alive", input: func() RelayModeInput {
			x := reverse(base)
			x.Path, x.MonitorFailAt, x.MonitorFailStreak = stage("fail", now), now.Unix(), 2
			return x
		}(), active: RelayModeDNAT, reason: "bridge_failed", notify: true},
		{name: "both ways down: reverse stays (nothing better to go to)", input: func() RelayModeInput {
			x := reverse(base)
			x.Leg, x.Bridge, x.MonitorFailAt, x.MonitorFailStreak = stage("fail", now), stage("fail", now), now.Unix(), 5
			return x
		}(), active: RelayModeReverse, reason: "leg_failed"},
		{name: "a leg sample counts toward the return", input: func() RelayModeInput { return reverse(base) }(), active: RelayModeReverse, reason: "leg_failed", streak: 1},
		{name: "three spaced samples and held: ask for the proof of the direct way", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 2, now.Add(-30*time.Minute).Unix()
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 3, need: true},
		{name: "recovery waits for the 30 minute hold", input: func() RelayModeInput {
			x := reverse(base)
			x.ModeChangedAt = now.Add(-29 * time.Minute)
			x.DirectStreak, x.DirectStreakAt = 2, now.Add(-30*time.Minute).Unix()
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 3},
		{name: "the proof of the direct way (8 MB) returns to direct", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 2, now.Add(-30*time.Minute).Unix()
			x.DirectSpeed = speed("ok", 8, now)
			return x
		}(), active: RelayModeDNAT, reason: "direct_back", notify: true},
		{name: "a proof of another size is no proof", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 3, now.Add(-30*time.Minute).Unix()
			x.DirectSpeed = speed("ok", 16, now)
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 3, need: true},
		{name: "a stale proof is asked for again", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 3, now.Add(-30*time.Minute).Unix()
			x.DirectSpeed = speed("ok", 8, now.Add(-16*time.Minute))
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 3, need: true},
		{name: "a failed proof (frozen flow) restarts the count", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 3, now.Add(-30*time.Minute).Unix()
			x.DirectSpeed = speed("fail", 8, now)
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 0},
		{name: "a proof that could not start is asked again after 30 minutes only", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 3, now.Add(-time.Hour).Unix()
			x.DirectSpeed = speed("skipped", 0, now.Add(-29*time.Minute))
			x.Leg = stage("ok", now.Add(-time.Hour))
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 3},
		{name: "a proof from the future is not a proof and is asked for", input: func() RelayModeInput {
			x := reverse(base)
			x.DirectStreak, x.DirectStreakAt = 3, now.Add(-time.Hour).Unix()
			x.DirectSpeed = speed("ok", 8, now.Add(time.Minute))
			x.Leg = stage("ok", now.Add(-time.Hour))
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 3, need: true},
		{name: "a failed leg sample restarts the count", input: func() RelayModeInput {
			x := reverse(base)
			x.Leg, x.DirectStreak, x.DirectStreakAt = stage("fail", now), 2, now.Add(-time.Hour).Unix()
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", streak: 0},
		{name: "after the return a failed round brings reverse back at once", input: func() RelayModeInput {
			x := base
			x.ModeReason, x.ModeChangedAt, x.MonitorFailStreak, x.MonitorFailAt = "direct_back", now.Add(-10*time.Minute), 1, now.Unix()
			x.Leg = stage("ok", now)
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", notify: true},
		{name: "a day after the return one failed round is not enough", input: func() RelayModeInput {
			x := base
			x.ModeReason, x.ModeChangedAt, x.MonitorFailStreak, x.MonitorFailAt = "direct_back", now.Add(-24*time.Hour), 1, now.Unix()
			x.Leg = stage("ok", now)
			return x
		}(), active: RelayModeDNAT, reason: "direct_back"},
		{name: "after a bridge failure a freeze alone waits out the hold", input: func() RelayModeInput {
			x := base
			x.ModeReason, x.ModeChangedAt, x.MonitorStage = "bridge_failed", now.Add(-10*time.Minute), "freeze"
			x.Leg = stage("ok", now)
			return x
		}(), active: RelayModeDNAT, reason: "bridge_failed"},
		{name: "after a bridge failure a failed leg does not wait", input: func() RelayModeInput {
			x := base
			x.ModeReason, x.ModeChangedAt = "bridge_failed", now.Add(-10*time.Minute)
			return x
		}(), active: RelayModeReverse, reason: "leg_failed", notify: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateRelayMode(tt.input)
			if got.ActiveMode != tt.active || got.ModeReason != tt.reason && !(tt.reason == "" && got.ModeReason == tt.input.ModeReason) ||
				got.DirectStreak != tt.streak || got.Notify != tt.notify || got.NeedDirectSpeed != tt.need {
				t.Fatalf("decision = %+v, want active=%q reason=%q streak=%d notify=%v need=%v", got, tt.active, tt.reason, tt.streak, tt.notify, tt.need)
			}
		})
	}
}

func TestEvaluateRelayModeCountsDirectChecksAtThirtyMinuteIntervals(t *testing.T) {
	now := controllerNow
	for _, tc := range []struct {
		name   string
		age    time.Duration
		streak uint32
	}{
		{name: "too close", age: 29 * time.Minute, streak: 1},
		{name: "thirty minutes", age: 30 * time.Minute, streak: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateRelayMode(RelayModeInput{Mode: RelayModeAuto, ActiveMode: RelayModeReverse, Network: "tcp",
				EntrySupported: true, ExitSupported: true, Now: now,
				Leg: RelayStage{Status: "ok", At: now.Unix()}, DirectStreak: 1, DirectStreakAt: now.Add(-tc.age).Unix()})
			if got.DirectStreak != tc.streak {
				t.Fatalf("direct streak = %d, want %d", got.DirectStreak, tc.streak)
			}
		})
	}
}
