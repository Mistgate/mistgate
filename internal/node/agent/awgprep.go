package agent

import (
	"context"
	"strconv"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awgprep"
)

// The automatic preparation of the AmneziaWG kernel module (agent.proto "AWG AND WARP", PREPARE THE KERNEL MODULE). The
// agent only decides and follows: internal/node/awgprep starts the real work as its own systemd unit, outside this
// process's sandbox, and the agent keeps the current backend (userspace) the whole time. Switching to the module is the
// panel's job once it hears "done"; the agent re-runs the two doctor checks that describe the result.

// capAwgPrepare is listed in Hello.capabilities: the panel sends PrepareAwgKernel only to agents that have it.
const capAwgPrepare = "awg-prepare/1"

const (
	// prepareWatchEvery is how often the status file of a run is looked at.
	prepareWatchEvery = 5 * time.Second
	// The doctor re-runs after the end of a run: at once (the module and the headers), and again once the panel has
	// switched the backend and the engine was rebuilt on it.
	prepareRecheckLater = 25 * time.Second
)

// prepareAwgKernel answers a PrepareAwgKernel with one CommandResult. It is always ok = true: what is the case is in
// params.state (ready, needs_prepare, started, running, unsupported), so the panel never has to guess from an error text.
func (a *Agent) prepareAwgKernel(s *session, r *pb.PrepareAwgKernel) {
	ctl := a.cfg.AwgPrepare
	if ctl == nil { // the panel sends it to agents that listed the capability only; an unwired build lists none
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unsupported_host"}))
		return
	}
	var ans awgprep.Answer
	if r.DryRun {
		ans = ctl.Check(s.ctx)
	} else {
		ans = ctl.Start(s.ctx)
	}
	a.log.Info("awg kernel module", "dry_run", r.DryRun, "state", ans.State, "code", ans.Code, "kernel", ans.Kernel)
	params := map[string]string{"state": ans.State}
	for k, v := range map[string]string{"code": ans.Code, "reason": ans.Reason, "kernel": ans.Kernel} {
		if v != "" {
			params[k] = v
		}
	}
	if ans.Since > 0 {
		params["since_unix"] = strconv.FormatInt(ans.Since, 10)
	}
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Detail: ans.Reason, Params: params}))
	if ans.State == awgprep.AnswerStarted {
		a.event(pb.Severity_SEVERITY_INFO, "awg_kernel_prepare_started", "", map[string]string{"kernel": ans.Kernel})
	}
}

// prepareLoop follows the runs for the agent's whole life, across reconnects and across an agent restart (the job is a
// separate unit and keeps going): the end of every run becomes one event, whenever the panel is reachable again.
func (a *Agent) prepareLoop(ctx context.Context) {
	every := a.prepWatch
	if every == 0 {
		every = prepareWatchEvery
	}
	a.cfg.AwgPrepare.Watch(ctx, every, func(st awgprep.Status) { a.prepareEnded(ctx, st) })
}

// prepareEnded raises the event of a finished run and has the doctor look again.
func (a *Agent) prepareEnded(ctx context.Context, st awgprep.Status) {
	params := map[string]string{"kernel": st.Kernel, "minutes": strconv.FormatInt(max((st.Finished-st.Started+59)/60, 1), 10)}
	if st.State == awgprep.StateDone {
		a.log.Info("awg kernel module is ready", "kernel", st.Kernel)
		a.event(pb.Severity_SEVERITY_INFO, "awg_kernel_prepare_done", "", params)
	} else {
		params["code"], params["reason"] = st.Code, st.Reason
		a.log.Warn("awg kernel module was not prepared", "code", st.Code, "reason", st.Reason)
		a.event(pb.Severity_SEVERITY_ERROR, "awg_kernel_prepare_failed", "", params)
	}
	go func() {
		a.recheck(ctx, awgprepChecks)
		sleepCtx(ctx, prepareRecheckLater)
		if ctx.Err() == nil {
			a.recheck(ctx, awgprepChecks)
		}
	}()
}

// awgprepChecks are the doctor checks that describe the module and the backend ("kernel_headers", "awg_backend").
var awgprepChecks = []string{"kernel_headers", "awg_backend"}

// recheck runs some doctor checks and sends the partial report on the current stream (if there is one).
func (a *Agent) recheck(ctx context.Context, ids []string) {
	for try := 0; try < recheckTries; try++ {
		rep, err := a.doc.Run(ctx, ids)
		if err == nil {
			if s := a.cur.Load(); s != nil {
				s.send(reportMsg("", rep, ""))
			}
			return
		}
		sleepCtx(ctx, time.Second) // a periodic run is in progress
		if ctx.Err() != nil {
			return
		}
	}
}
