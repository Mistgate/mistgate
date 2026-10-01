package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"connectrpc.com/connect"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/node/update"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Self-update wiring (agent.proto "UPDATE"). The decisions and the filesystem work are
// internal/node/update; this file lends it the download client, the clock and the failed-inbound list, answers
// UpdateAgent/RollbackAgent, drains the agent before a re-exec, and runs the commit watch of a new build.

const (
	// A new build commits only after it has been connected this long with its state applied: engines need a moment
	// before a failing inbound shows up in their health.
	commitSettle = 10 * time.Second
	commitTick   = 2 * time.Second
	// Time the stream gets to finish after our half-close, before we exec regardless.
	execDrain = 2 * time.Second
)

// errExec is why Run returns when a re-exec failed: the process exits and the supervisor starts the restored binary.
var errExec = errors.New("re-exec failed")

// updSource is the update package's view of the agent.
type updSource struct{ a *Agent }

func (s updSource) Now() time.Time { return s.a.now() }

func (s updSource) Failed() []string { return s.a.failedInbounds() }

// Fetch downloads one bundle file from the panel with its own mTLS client, appending to w from offset. Errors are
// mapped for the updater: a busy panel is ErrRetryLater, an unknown name or a missing bundle is ErrPermanent.
func (s updSource) Fetch(ctx context.Context, name string, offset uint64, w io.Writer) (uint64, error) {
	a := s.a
	st := a.settings.Load()
	client := newHTTPClient(a.mtlsConfig(), dialTimeout(st), &http.HTTP2Config{
		SendPingTimeout: secs(st.KeepaliveIntervalS, 20*time.Second),
		PingTimeout:     secs(st.KeepaliveTimeoutS, 45*time.Second),
	})
	defer client.CloseIdleConnections()
	cl := agentv1connect.NewAgentServiceClient(client, "https://"+a.meta.Panel, connect.WithReadMaxBytes(1<<20))
	stream, err := cl.FetchUpdate(ctx, connect.NewRequest(&pb.FetchUpdateRequest{Name: name, Offset: offset}))
	if err != nil {
		return 0, mapFetchErr(err)
	}
	defer stream.Close()
	var total uint64
	for stream.Receive() {
		m := stream.Msg()
		total = m.TotalSize
		if len(m.Data) > 0 {
			if _, err := w.Write(m.Data); err != nil {
				return total, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return total, mapFetchErr(err)
	}
	return total, nil
}

func mapFetchErr(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted:
		return fmt.Errorf("%w: %v", update.ErrRetryLater, err)
	case connect.CodeNotFound, connect.CodeFailedPrecondition, connect.CodePermissionDenied, connect.CodeUnimplemented:
		return fmt.Errorf("%w: %v", update.ErrPermanent, err)
	}
	return err
}

// failedInbounds lists the inbounds the engines report FAILED, sorted (the same source the doctor reads; safe from any goroutine).
func (a *Agent) failedInbounds() []string {
	var out []string
	for _, p := range a.protocols {
		for _, h := range a.engines[p].Health() {
			if h.State == plugin.RunFailed {
				out = append(out, h.InboundID)
			}
		}
	}
	sort.Strings(out)
	return out
}

// updateStatus is what the commit watch asks every tick: is this stream up and settled with the desired state applied,
// and which inbounds are FAILED that were not before the update. The panel sends a desired state only when the node
// lacks it, so "applied" means: nothing is in flight and the last one on this connection was not refused.
func (a *Agent) updateStatus(preFailed []string) update.Status {
	var st update.Status
	at := a.connAt.Load()
	st.Settled = at != 0 && time.Since(time.Unix(0, at)) >= a.commitSettle && a.dsIn.Load() == a.dsDone.Load() && a.dsOK.Load()
	was := map[string]bool{}
	for _, id := range preFailed {
		was[id] = true
	}
	for _, id := range a.failedInbounds() {
		if !was[id] {
			st.NewFailed = append(st.NewFailed, id)
		}
	}
	return st
}

// startUpdate runs once at the start of Run, before anything touches the engines: it acts on the update marker files (Updater.Startup).
// It returns true when the process is being replaced (nothing more should run).
func (a *Agent) startUpdate(ctx context.Context) (stop bool) {
	if a.cfg.Updater == nil {
		return false
	}
	st := a.upd.Startup()
	if o := a.upd.Outcome(); o != nil && o.Outcome == pb.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK {
		a.event(pb.Severity_SEVERITY_WARNING, "update_rolled_back", "", map[string]string{
			"from_version": o.FromVersion, "to_version": o.ToVersion, "reason": o.Reason,
		})
	}
	if st.Finish != nil {
		a.execVia(nil, st.Finish)
		return ctx.Err() != nil
	}
	if st.Pending != nil {
		a.upMarker = st.Pending
		go a.commitWatch(ctx, st.Pending)
	}
	return false
}

// commitWatch is the 5-minute window of a new build.
func (a *Agent) commitWatch(ctx context.Context, m *update.Marker) {
	o, fin, err := a.upd.Watch(ctx, a.commitTick, func() update.Status { return a.updateStatus(m.PreFailed) })
	switch {
	case err != nil:
		if ctx.Err() == nil {
			a.log.Error("update watch", "err", err)
		}
	case o != nil:
		a.event(pb.Severity_SEVERITY_INFO, "update_committed", "", map[string]string{
			"from_version": o.FromVersion, "from_built": strconv.FormatInt(o.FromBuilt, 10),
			"to_version": o.ToVersion, "to_built": strconv.FormatInt(o.ToBuilt, 10),
		})
	case fin != nil:
		a.execVia(a.cur.Load(), fin)
	}
}

// applyUpdate answers one UpdateAgent. Everything but the exec itself happens here; the CommandResult is on the wire
// before the engines are drained and the process is replaced.
func (a *Agent) applyUpdate(s *session, r *pb.UpdateAgent) {
	if a.upd == nil {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unsigned_build"}))
		return
	}
	res, fin := a.upd.Apply(s.ctx, r, updSource{a})
	a.finishCommand(s, res, fin)
}

func (a *Agent) rollbackUpdate(s *session, r *pb.RollbackAgent) {
	if a.upd == nil {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unsigned_build"}))
		return
	}
	res, fin := a.upd.Rollback(s.ctx, r)
	a.finishCommand(s, res, fin)
}

func (a *Agent) finishCommand(s *session, res *pb.CommandResult, fin update.Finish) {
	if fin == nil {
		s.send(cmdResult(res))
		return
	}
	s.sendSync(cmdResult(res), 3*time.Second)
	a.execVia(s, fin)
}

// execVia drains the agent and runs fin (the re-exec). The stream gets a half-close first so the last frames (the
// CommandResult) reach the panel, then the engines close their sockets. If fin returns the exec failed: the updater has put
// the previous binary back, so the process exits and the supervisor starts it.
func (a *Agent) execVia(s *session, fin update.Finish) {
	a.exiting.Store(true)
	if s != nil {
		s.drain(execDrain)
	}
	a.closeEngines()
	if err := fin(); err != nil {
		a.log.Error("re-exec failed, exiting", "err", err)
		a.cancel(fmt.Errorf("%w: %v", errExec, err))
	}
}
