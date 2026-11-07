package agent

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tspuprobe"
)

// pathTraceState is the bookkeeping of PathTrace (agent.proto "PATH PROBE"): one walk at a time, and the test seams.
type pathTraceState struct {
	running atomic.Bool
	// supported and walk are nil in production (tspuprobe.Supported and tspuprobe.Walk).
	supported func() bool
	walk      func(ctx context.Context, host string, port uint16, maxTTL int) ([]tspuprobe.WalkHop, bool, error)
}

// runPathTrace handles one PathTrace command: walk to the target with TTL-limited SYNs and answer with a line per hop. No
// router address is read or returned: only whether something answered and how long it took.
func (a *Agent) runPathTrace(s *session, r *pb.PathTrace) {
	reply := func(res *pb.CommandResult) {
		res.RequestId = r.RequestId
		s.send(cmdResult(res))
	}
	if r.Host == "" || r.Port == 0 || r.Port > 65535 || r.MaxTtl > tspuprobe.WalkMaxTTL {
		reply(&pb.CommandResult{Error: "bad_params"})
		return
	}
	supported := tspuprobe.Supported
	if a.pathTrace.supported != nil {
		supported = a.pathTrace.supported
	}
	if !supported() {
		reply(&pb.CommandResult{Error: "unsupported"})
		return
	}
	if a.measuring.Load() || !a.pathTrace.running.CompareAndSwap(false, true) {
		reply(&pb.CommandResult{Error: "busy"})
		return
	}
	defer a.pathTrace.running.Store(false)

	walk := a.pathTrace.walk
	if walk == nil {
		walk = tspuprobe.Walk
	}
	hops, reached, err := walk(s.ctx, r.Host, uint16(r.Port), int(r.MaxTtl))
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		reply(&pb.CommandResult{Error: pathTraceError(err)})
		return
	}
	params := make(map[string]string, len(hops)+2)
	params["family"] = "4"
	params["reached"] = "0"
	if reached {
		params["reached"] = "1"
	}
	for _, h := range hops {
		params["h"+strconv.Itoa(h.TTL)] = h.Kind + ":" + strconv.FormatInt(h.RTT.Round(time.Millisecond).Milliseconds(), 10)
	}
	reply(&pb.CommandResult{Ok: true, Params: params})
}

func pathTraceError(err error) string {
	switch {
	case errors.Is(err, tspuprobe.ErrBadParams):
		return "bad_params"
	case errors.Is(err, tspuprobe.ErrUnsupportedHost):
		return "unsupported_host"
	case errors.Is(err, tspuprobe.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, tspuprobe.ErrNoRoute):
		return "no_route"
	default:
		return "failed: " + shortUDPError(err)
	}
}
