package fleet

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The panel's side of WARP reach (agent.proto "WARP REACH"): ask one node to fetch the owner's sites through its WARP tunnel and
// say which show a block page. This file only sends the command and reads the answer; when to ask, which sites and what to keep
// belong to the WARP module (internal/panel/warp).

const (
	capWarpReach = "warp-reach/1"
	// warpReachWait is the agent's own longest run (12 s a site, in parallel, and the trace) and a margin for the answer.
	warpReachWait = 60 * time.Second
)

// errWarpReachAnswer is an answer of the agent that is not in the shape the contract says.
var errWarpReachAnswer = errors.New("warp reach: unreadable answer")

// WarpReach asks the node to check the sites and returns the colo of its WARP exit at that run and one answer per site, in the
// order asked. An error means no usable answer: the node is offline, its agent does not list warp-reach/1 ("agent too old"),
// the link was lost, the 60 s ran out, or the agent refused (busy, too_soon, warp_down, bad_params: the error says which).
func (f *Fleet) WarpReach(ctx context.Context, nodeID string, domains []string) (string, []store.WarpReachItem, error) {
	result, err := f.command(ctx, nodeID, warpReachWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_WarpReach{WarpReach: &agentv1.WarpReach{RequestId: reqID, Domains: domains}}}
	})
	if err != nil {
		return "", nil, err
	}
	return parseWarpReach(result, len(domains))
}

// parseWarpReach reads a CommandResult of WarpReach: params "colo" and "r<i>" = "<verdict>:<status>:<ms>" for each of the n sites.
func parseWarpReach(result *agentv1.CommandResult, n int) (string, []store.WarpReachItem, error) {
	if result == nil {
		return "", nil, errWarpReachAnswer
	}
	if !result.Ok {
		code, _, _ := strings.Cut(result.Error, ":")
		switch code {
		case "busy", "too_soon", "warp_down", "bad_params":
			return "", nil, errors.New("warp reach: " + code)
		}
		return "", nil, errors.New("warp reach: failed")
	}
	items := make([]store.WarpReachItem, n)
	for i := range items {
		verdict, rest, _ := strings.Cut(result.Params["r"+strconv.Itoa(i)], ":")
		status, ms, _ := strings.Cut(rest, ":")
		switch verdict {
		case "ok", "blocked", "unreachable", "error":
		default:
			return "", nil, errWarpReachAnswer
		}
		st, err1 := strconv.ParseUint(status, 10, 16)
		took, err2 := strconv.ParseUint(ms, 10, 32)
		if err1 != nil || err2 != nil {
			return "", nil, errWarpReachAnswer
		}
		items[i] = store.WarpReachItem{Verdict: verdict, HTTPStatus: uint32(st), Millis: uint32(took)}
	}
	return store.Clip(result.Params["colo"], 16), items, nil
}
