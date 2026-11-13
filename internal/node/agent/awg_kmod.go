package agent

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/awgprep"
)

const kmodWatchEvery = time.Second

func (a *Agent) startAwgKmod(ctx context.Context, m *model) {
	ctl := a.cfg.AwgKmod
	if ctl == nil || m == nil || m.settings != nil && m.settings.GetAwgBackend() == "userspace" {
		return
	}
	enabled := false
	for _, id := range m.ids() {
		in := m.inbounds[id]
		if in.spec.Protocol == awg.Protocol && in.spec.Enabled {
			enabled = true
			break
		}
	}
	if !enabled || !a.awgKmodAttempted.CompareAndSwap(false, true) {
		return
	}
	ctl.StartBoot(ctx)
}

func (a *Agent) awgKmodLoop(ctx context.Context) {
	every := a.kmodWatch
	if every <= 0 {
		every = kmodWatchEvery
	}
	var seenMode awgprep.KmodMode
	var seenStarted int64
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		st, ok := a.cfg.AwgKmod.Current()
		if ok {
			bootOrRepair := st.Mode == awgprep.KmodBoot || st.Mode == awgprep.KmodRepair
			finished := st.State == awgprep.StateDone || st.State == awgprep.StateFailed
			if bootOrRepair && st.Started != 0 && finished &&
				(st.Mode != seenMode || st.Started != seenStarted) {
				seenMode, seenStarted = st.Mode, st.Started
				select {
				case a.jobs <- job{kmodReapply: true}:
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) reapplyAwgAfterKmod(ctx context.Context) {
	if r, ok := a.engines[awg.Protocol].(AwgModuleReprober); ok {
		moved, err := r.ReprobeKernel(ctx)
		if err != nil {
			a.log.Warn("AmneziaWG engine re-probe after module sync failed", "err", err)
		}
		if !moved {
			return
		}
	} else {
		return
	}
	m := a.snapshotModel()
	for _, id := range m.ids() {
		if m.inbounds[id].spec.Protocol == awg.Protocol {
			delete(a.held, id)
		}
	}
	a.reconcile(ctx, m, nil)
}
