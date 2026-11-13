package agent

import (
	"context"
	"strconv"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"google.golang.org/protobuf/proto"
)

// OS jobs (agent.proto "OS UPDATES"). The agent only decides and follows: internal/node/osupdate starts the real work as its
// own systemd unit, outside this process's sandbox, and follows it through a status file that survives an agent restart and a
// reboot of the host. Nothing of the command line comes from the wire except enumerated values.

const osUpdateWatchEvery = 5 * time.Second

// osUpdateChecks are the doctor checks that describe the result of a job.
var osUpdateChecks = []string{"security_updates", "kernel_headers", "awg_backend", "resolver"}

// osJobActive: an OS job is going. Other host work (an agent update, the module build, the unit refresh) does not start
// next to it; the controller asks the same questions the other way round before it starts a job.
func (a *Agent) osJobActive() bool { return a.cfg.OSUpdate != nil && a.cfg.OSUpdate.Active() }

// awgKernelInUse: AmneziaWG inbounds run on the kernel module right now, so a reboot into a kernel without the module would
// take them down.
func (a *Agent) awgKernelInUse() bool {
	bs, ok := a.awgStatuser()
	if !ok {
		return false
	}
	m := a.snapshotModel()
	has := false
	for _, id := range m.ids() {
		if m.inbounds[id].spec.Protocol == awg.Protocol {
			has = true
			break
		}
	}
	return has && bs.BackendStatus().Name == "kernel"
}

// osSettled is the controller's question after a reboot: are the engines restored, and how many inbounds are down.
func (a *Agent) osSettled() (bool, int) {
	failed := len(a.failedInbounds())
	return a.warm && failed == 0, failed
}

// updateOs answers an UpdateOs with one CommandResult. It is always ok = true: what is the case is in params.state (started,
// running, unsupported), so the panel never has to guess from an error text. The values are checked here again; the panel is
// not trusted to have done it.
func (a *Agent) updateOs(s *session, r *pb.UpdateOs) {
	ctl := a.cfg.OSUpdate
	if ctl == nil || !a.canOSUpdate() { // the panel sends it to agents that listed the capability only
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unsupported_host"}))
		return
	}
	ans := a.startOsJob(s.ctx, r)
	a.log.Info("os update request", "action", r.Action, "mode", r.Mode, "reboot", r.Reboot, "state", ans.State, "code", ans.Code)
	params := map[string]string{"state": ans.State}
	for k, v := range map[string]string{"code": ans.Code, "reason": ans.Reason, "job_id": ans.JobID, "result": ans.Result} {
		if v != "" {
			params[k] = v
		}
	}
	if ans.Since > 0 {
		params["since_unix"] = strconv.FormatInt(ans.Since, 10)
	}
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Detail: ans.Reason, Params: params}))
	if ans.State == osupdate.AnswerStarted {
		a.event(pb.Severity_SEVERITY_INFO, "os_update_started", "", map[string]string{"job_id": ans.JobID, "action": r.Action, "mode": r.Mode})
		a.wakeOsJob()
	}
}

func (a *Agent) startOsJob(ctx context.Context, r *pb.UpdateOs) osupdate.Answer {
	bad := func(reason string) osupdate.Answer {
		return osupdate.Answer{State: osupdate.AnswerUnsupported, Code: osupdate.CodeBadRequest, Reason: reason}
	}
	// ParseSpec reads "if-required" as well (the command line spells it so); the wire has one spelling.
	if strings.Contains(r.Reboot, "-") {
		return bad("unknown reboot value")
	}
	spec, ref := osupdate.ParseSpec(r.Action, r.Mode, r.Reboot, r.JobId)
	if ref != nil {
		return osupdate.Answer{State: osupdate.AnswerUnsupported, Code: ref.Code, Reason: ref.Reason}
	}
	if spec.JobID != "" && !strings.HasPrefix(spec.JobID, "osj_") {
		return bad("the job id is not valid")
	}
	spec.AwgRequired = a.awgKernelInUse()
	return a.cfg.OSUpdate.Start(ctx, spec)
}

func (a *Agent) wakeOsJob() {
	select {
	case a.osJobWake <- struct{}{}:
	default:
	}
}

// osUpdateLoop follows the jobs for the agent's whole life, across reconnects and across an agent restart (the job is a
// separate unit and keeps going): the end of every job becomes one event, whenever the panel is reachable again.
func (a *Agent) osUpdateLoop(ctx context.Context) {
	every := a.osJobWatch
	if every == 0 {
		every = osUpdateWatchEvery
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	var sent, rebootEvent string
	for {
		a.cfg.OSUpdate.Poll(func(st osupdate.Status) { a.osUpdateEnded(ctx, st) })
		if st, ok := a.cfg.OSUpdate.Current(); ok {
			// Back != 0: the host came back and the engines are settling; the reboot is over, its event was sent before it.
			if st.Phase == osupdate.PhaseRebooting && st.State == osupdate.StateRunning && st.Back == 0 && rebootEvent != st.JobID && st.KernelTo != "" {
				rebootEvent = st.JobID
				a.event(pb.Severity_SEVERITY_INFO, "os_reboot_started", "", map[string]string{"job_id": st.JobID, "kernel_from": st.KernelFrom, "kernel_to": st.KernelTo})
			}
		}
		if fp := a.osJobFingerprint(); fp != sent {
			sent = fp
			a.sendOsStatus()
		}
		select {
		case <-ctx.Done():
			return
		case <-a.osJobWake:
		case <-ticker.C:
		}
	}
}

// osJobFingerprint changes when the job part of the snapshot does (not with every heartbeat).
func (a *Agent) osJobFingerprint() string {
	st, ok := a.cfg.OSUpdate.Current()
	if !ok {
		return ""
	}
	return strings.Join([]string{st.JobID, st.State, st.Phase, st.Code, st.Reboot, strconv.FormatUint(uint64(st.Upgraded), 10)}, "|")
}

// osUpdateEnded raises the event of a finished job, reads the host again and has the doctor look again.
func (a *Agent) osUpdateEnded(ctx context.Context, st osupdate.Status) {
	kernel := st.KernelNow
	if kernel == "" {
		kernel = st.KernelTo
	}
	params := map[string]string{"job_id": st.JobID}
	if st.State == osupdate.StateDone {
		params["upgraded"], params["reboot"], params["kernel"] = strconv.FormatUint(uint64(st.Upgraded), 10), st.Reboot, kernel
		if st.HeldReason != "" {
			params["held_reason"] = st.HeldReason
		}
		a.log.Info("os update job finished", "job", st.JobID, "action", st.Action, "upgraded", st.Upgraded, "reboot", st.Reboot)
		a.event(pb.Severity_SEVERITY_INFO, "os_update_done", "", params)
	} else {
		params["code"], params["reason"], params["package"] = st.Code, st.Reason, st.Package
		if st.Reboot != "" {
			params["reboot"] = st.Reboot
		}
		if len(st.Reasons) > 0 {
			params["reasons"] = strings.Join(st.Reasons, ",")
		}
		if kernel != "" {
			params["kernel"] = kernel
		}
		a.log.Warn("os update job failed", "code", "os_update_failed", "job_code", st.Code, "job", st.JobID, "action", st.Action)
		a.event(pb.Severity_SEVERITY_ERROR, "os_update_failed", "", params)
	}
	// The package state changed: read it again now instead of at the next five-minute look, then tell the panel.
	if a.settings.Load().GetOsStatus() && a.osUpdateMonitor != nil {
		if _, _, _, err := a.osUpdateMonitor.Poll(ctx, true); err != nil {
			a.log.Warn("OS status read failed", "code", "os_status_read_failed", "err", err)
		}
	}
	a.sendOsStatus()
	go func() {
		a.recheck(ctx, osUpdateChecks)
		sleepCtx(ctx, prepareRecheckLater)
		if ctx.Err() == nil {
			a.recheck(ctx, osUpdateChecks)
		}
	}()
}

// withOsJob is the snapshot with the job the controller follows (or none).
func (a *Agent) withOsJob(status *pb.OsStatus) *pb.OsStatus {
	if status == nil || a.cfg.OSUpdate == nil {
		return status
	}
	out := proto.Clone(status).(*pb.OsStatus)
	out.Job = nil
	if st, ok := a.cfg.OSUpdate.Current(); ok {
		out.Job = st.Proto()
	}
	return out
}

// sendOsStatus sends the current snapshot, with the job, on the current stream (if there is one and the panel asked).
func (a *Agent) sendOsStatus() {
	if !a.settings.Load().GetOsStatus() || a.osUpdateMonitor == nil {
		return
	}
	status := a.osUpdateMonitor.Snapshot()
	if status == nil {
		return
	}
	if s := a.cur.Load(); s != nil {
		s.send(osStatusMessage(a.withOsJob(status)))
	}
}
