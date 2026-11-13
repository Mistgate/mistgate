package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

func newKmodL3(t *testing.T, tune func(*Agent, *awgprep.KmodController)) *l3 {
	t.Helper()
	x := &l3{ord: &order{}}
	x.awg = newFakeAwg(x.ord)
	x.warp = newFakeWarp(x.ord)
	x.harness = newHarness(t, harnessOpts{
		extra: map[string]engine.Factory{awg.Protocol: x.awg.factory(), "hysteria2": newFakeEngine("hysteria2").factory()},
		wrapHost: func(f *fakeHost) hostctl.Host {
			x.tun = &tunHost{fakeHost: f, ord: x.ord, counters: map[uint16]uint64{}}
			return x.tun
		},
		cfg: func(c *Config) {
			status := filepath.Join(c.StateDir, awgprep.KmodStatusFileName)
			c.AwgKmod = awgprep.NewKmodController(status, nil, nil)
		},
		tune: func(a *Agent) {
			a.kmodWatch = 5 * time.Millisecond
			tune(a, a.cfg.AwgKmod)
		},
	})
	return x
}

func setKmodStatus(t *testing.T, ctl *awgprep.KmodController, st awgprep.KmodStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if err := awgprep.WriteKmodStatus(ctl.StatusPath, st); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func awgApplyCount(e *fakeAwg, id string) int {
	e.fakeEngine.mu.Lock()
	defer e.fakeEngine.mu.Unlock()
	return e.fakeEngine.applies[id]
}

func TestKmodCompletionReappliesOnlyForBootOrRepairWhenKernelWorks(t *testing.T) {
	for _, tc := range []struct {
		name, mode, finalState string
		moves                  bool
		wantApply              int
		wantProbe              int
	}{
		{name: "boot moves to kernel", mode: string(awgprep.KmodBoot), finalState: awgprep.StateDone, moves: true, wantApply: 2, wantProbe: 1},
		{name: "repair moves to kernel", mode: string(awgprep.KmodRepair), finalState: awgprep.StateDone, moves: true, wantApply: 2, wantProbe: 1},
		{name: "failed boot moves when kernel works", mode: string(awgprep.KmodBoot), finalState: awgprep.StateFailed, moves: true, wantApply: 2, wantProbe: 1},
		{name: "boot keeps userspace if kernel is unavailable", mode: string(awgprep.KmodBoot), finalState: awgprep.StateDone, wantApply: 1, wantProbe: 1},
		{name: "failed repair keeps userspace if kernel is unavailable", mode: string(awgprep.KmodRepair), finalState: awgprep.StateFailed, wantApply: 1, wantProbe: 1},
		{name: "apt transaction does not reapply", mode: string(awgprep.KmodAfterApt), finalState: awgprep.StateDone, moves: true, wantApply: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().Unix()
			x := newKmodL3(t, func(_ *Agent, ctl *awgprep.KmodController) {
				setKmodStatus(t, ctl, awgprep.KmodStatus{Mode: awgprep.KmodMode(tc.mode), State: awgprep.StateRunning, Started: now, Updated: now})
			})
			x.waitConnected()
			mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))))
			x.awg.hmu.Lock()
			x.awg.reprobe = tc.moves
			if tc.moves {
				x.awg.status = awg.BackendStatus{Mode: "kernel", Name: "kernel", Available: true}
			} else {
				x.awg.status = awg.BackendStatus{Mode: "kernel", Name: "userspace", Available: true, Fallback: true}
			}
			x.awg.hmu.Unlock()
			setKmodStatus(t, x.a.cfg.AwgKmod, awgprep.KmodStatus{Mode: awgprep.KmodMode(tc.mode), State: tc.finalState, Started: now, Updated: now + 1})
			if tc.wantProbe != 0 {
				eventually(t, func() bool {
					x.awg.hmu.Lock()
					defer x.awg.hmu.Unlock()
					return x.awg.reprobes == tc.wantProbe
				}, "module re-probe")
			} else {
				time.Sleep(30 * time.Millisecond)
			}
			eventually(t, func() bool { return awgApplyCount(x.awg, "inb_a") >= tc.wantApply }, "inbound reconciliation")
			if got := awgApplyCount(x.awg, "inb_a"); got != tc.wantApply {
				t.Fatalf("AWG apply count = %d, want %d", got, tc.wantApply)
			}
			x.awg.hmu.Lock()
			gotProbe, status := x.awg.reprobes, x.awg.status
			x.awg.hmu.Unlock()
			if gotProbe != tc.wantProbe || (tc.moves && status.Name != "kernel") {
				t.Fatalf("re-probes=%d backend=%+v", gotProbe, status)
			}
		})
	}
}
