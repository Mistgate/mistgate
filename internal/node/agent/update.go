package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
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
	capUpdateLink = "update-link/1"

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
type updSource struct {
	a     *Agent
	token string
}

func (s updSource) Now() time.Time { return s.a.now() }

func (s updSource) Failed() []string { return s.a.failedInbounds() }

// Fetch downloads one bundle file and appends to w from offset. VPS agents use the panel's mTLS stream; link-only agents
// use the authenticated link route. Errors are mapped for the updater: busy is ErrRetryLater, unavailable files are
// ErrPermanent.
func (s updSource) Fetch(ctx context.Context, name string, offset uint64, w io.Writer) (uint64, error) {
	a := s.a
	if a.linkOnly() {
		return s.fetchLink(ctx, name, offset, w)
	}
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

func (s updSource) fetchLink(ctx context.Context, name string, offset uint64, w io.Writer) (uint64, error) {
	if s.token == "" {
		return 0, fmt.Errorf("%w: missing download token", update.ErrPermanent)
	}
	a := s.a
	rawURL, err := linkDownloadURL(a.cfg.LinkURL, a.meta.NodeID, name)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid link download URL: %v", update.ErrPermanent, err)
	}
	keepaliveTimeout := 45 * time.Second
	if st := a.settings.Load(); st != nil {
		keepaliveTimeout = secs(st.KeepaliveTimeoutS, keepaliveTimeout)
	}
	idle := durOr(a.linkPingTimeout, keepaliveTimeout)
	requestCtx, cancel := context.WithCancel(ctx)
	var stallFired atomic.Bool
	stall := time.AfterFunc(idle, func() {
		stallFired.Store(true)
		cancel()
	})
	defer func() {
		stall.Stop()
		cancel()
	}()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: cannot build link download request", update.ErrPermanent)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client, cleanup := a.newLinkHTTPClient()
	if cleanup != nil {
		defer cleanup()
	}
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
	if err != nil {
		if stallFired.Load() && ctx.Err() == nil {
			return 0, fmt.Errorf("link download stalled for %s", idle)
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return 0, fmt.Errorf("link download request failed: %w", err)
	}
	defer resp.Body.Close()
	body := linkStallReader{r: resp.Body, stall: stall, idle: idle}

	var total uint64
	switch resp.StatusCode {
	case http.StatusOK:
		if resp.ContentLength >= 0 {
			total = uint64(resp.ContentLength)
		}
		if offset > 0 {
			if offset > uint64(1<<63-1) {
				return total, errors.New("link download offset is too large")
			}
			if _, err := io.CopyN(io.Discard, body, int64(offset)); err != nil {
				if stallFired.Load() && ctx.Err() == nil {
					return total, fmt.Errorf("link download stalled for %s", idle)
				}
				return total, fmt.Errorf("discard ranged response prefix: %w", err)
			}
		}
	case http.StatusPartialContent:
		total, err = linkContentRange(resp.Header.Get("Content-Range"), offset)
		if err != nil {
			return 0, err
		}
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return 0, fmt.Errorf("%w: link download returned HTTP %d", update.ErrPermanent, resp.StatusCode)
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return 0, fmt.Errorf("%w: link download returned HTTP %d", update.ErrRetryLater, resp.StatusCode)
	default:
		return 0, fmt.Errorf("link download returned HTTP %d", resp.StatusCode)
	}
	if _, err := io.Copy(w, body); err != nil {
		if stallFired.Load() && ctx.Err() == nil {
			return total, fmt.Errorf("link download stalled for %s", idle)
		}
		return total, err
	}
	return total, nil
}

type linkStallReader struct {
	r     io.Reader
	stall *time.Timer
	idle  time.Duration
}

func (r linkStallReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.stall.Reset(r.idle)
	}
	return n, err
}

func linkDownloadURL(linkBase, nodeID, name string) (string, error) {
	if nodeID == "" || name == "" {
		return "", errors.New("node id and file name are required")
	}
	u, err := parseLinkBase(linkBase)
	if err != nil {
		return "", err
	}
	escapedPrefix := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = strings.TrimRight(u.Path, "/") + "/update/" + nodeID + "/" + name
	u.RawPath = escapedPrefix + "/update/" + url.PathEscape(nodeID) + "/" + url.PathEscape(name)
	u.Scheme = "https"
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.Opaque = ""
	return u.String(), nil
}

func linkContentRange(value string, offset uint64) (total uint64, err error) {
	unit, value, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || unit != "bytes" {
		return 0, errors.New("link download returned an invalid Content-Range")
	}
	byteRange, size, ok := strings.Cut(value, "/")
	if !ok {
		return 0, errors.New("link download returned an invalid Content-Range")
	}
	startText, endText, ok := strings.Cut(byteRange, "-")
	if !ok {
		return 0, errors.New("link download returned an invalid Content-Range")
	}
	start, err := strconv.ParseUint(startText, 10, 64)
	if err != nil || start != offset {
		return 0, fmt.Errorf("link download Content-Range starts at %q, want %d", startText, offset)
	}
	end, err := strconv.ParseUint(endText, 10, 64)
	if err != nil || end < start {
		return 0, errors.New("link download returned an invalid Content-Range")
	}
	if size == "*" {
		return 0, nil
	}
	total, err = strconv.ParseUint(size, 10, 64)
	if err != nil || total == 0 || end >= total {
		return 0, errors.New("link download returned an invalid Content-Range")
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

// failedInbounds lists the inbounds the engines report FAILED or whose last apply carried an error (a blocked inbound has
// no engine entry), sorted and unique (the same source the doctor reads; safe from any goroutine).
func (a *Agent) failedInbounds() []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, p := range a.protocols {
		for _, h := range a.engines[p].Health() {
			if h.State == plugin.RunFailed {
				add(h.InboundID)
			}
		}
	}
	if f := a.failedApply.Load(); f != nil {
		for _, id := range *f {
			add(id)
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
	ctx := s.ctx
	if a.linkOnly() && a.runCtx != nil {
		ctx = a.runCtx
	}
	res, fin := a.upd.Apply(ctx, r, updSource{a: a, token: r.GetDownloadToken()})
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
