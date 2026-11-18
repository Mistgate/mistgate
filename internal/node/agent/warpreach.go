package agent

import (
	"errors"
	"strconv"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/warp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// capWarpReach marks the agent that can fetch sites through its WARP tunnel and say which show a block page (agent.proto
// "WARP REACH"). It is listed with the WARP manager: without WARP there is nothing to ask.
const capWarpReach = "warp-reach/1"

// warpReach answers one WarpReach command: the verdict for every site, and the colo of the exit that gave them. The answer is
// one CommandResult; the work and its limits (8 sites, 64 KiB, one at a time, one per health interval, a guarded dial) are the
// WARP manager's.
func (a *Agent) warpReach(s *session, r *pb.WarpReach) {
	reply := func(res *pb.CommandResult) {
		res.RequestId = r.RequestId
		s.send(cmdResult(res))
	}
	if a.cfg.Warp == nil || len(r.Domains) == 0 || len(r.Domains) > plugin.MaxWarpReachDomains {
		reply(&pb.CommandResult{Error: "bad_params"})
		return
	}
	rep, err := a.cfg.Warp.Reach(s.ctx, r.Domains)
	if s.ctx.Err() != nil {
		return
	}
	switch {
	case err == nil:
		params := make(map[string]string, len(rep.Items)+1)
		params["colo"] = rep.Colo
		for i, it := range rep.Items {
			params["r"+strconv.Itoa(i)] = it.Verdict + ":" + strconv.Itoa(it.Code) + ":" + strconv.FormatUint(uint64(it.Ms), 10)
		}
		reply(&pb.CommandResult{Ok: true, Params: params})
	case errors.Is(err, warp.ErrReachBadName):
		reply(&pb.CommandResult{Error: "bad_params"})
	case errors.Is(err, warp.ErrReachBusy):
		reply(&pb.CommandResult{Error: "busy"})
	case errors.Is(err, warp.ErrReachTooSoon):
		reply(&pb.CommandResult{Error: "too_soon"})
	case errors.Is(err, warp.ErrNotActive):
		reply(&pb.CommandResult{Error: "warp_down"})
	default:
		reply(&pb.CommandResult{Error: "failed: " + err.Error()})
	}
}
