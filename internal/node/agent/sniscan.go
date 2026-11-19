package agent

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/sniscan"
)

// capSniScan: the agent answers SniScan (agent.proto "NEIGHBOR SITES").
const capSniScan = "sni-scan/1"

// sniScanState is the bookkeeping of SniScan: one scan at a time, and the test seam.
type sniScanState struct {
	running atomic.Bool
	// scan is nil in production (sniscan.New().Scan).
	scan func(ctx context.Context, addr netip.Addr) (*sniscan.Result, error)
}

// runSniScan handles one SniScan command: scan the /24 of the node's own address within the limits of internal/node/sniscan
// and answer with counts and the candidates. The scan lives in the session's context: a stream that ends ends the scan, with
// no answer and nothing left on the node.
func (a *Agent) runSniScan(s *session, r *pb.SniScan) {
	reply := func(res *pb.CommandResult) {
		res.RequestId = r.RequestId
		s.send(cmdResult(res))
	}
	addr, err := netip.ParseAddr(r.Address)
	if err != nil || !addr.Unmap().Is4() {
		reply(&pb.CommandResult{Error: "bad_params"})
		return
	}
	if a.measuring.Load() || !a.sniScan.running.CompareAndSwap(false, true) {
		reply(&pb.CommandResult{Error: "busy"})
		return
	}
	defer a.sniScan.running.Store(false)

	scan := a.sniScan.scan
	if scan == nil {
		scan = sniscan.New().Scan
	}
	res, err := scan(s.ctx, addr)
	if s.ctx.Err() != nil {
		return
	}
	switch {
	case errors.Is(err, sniscan.ErrNotMyAddress), errors.Is(err, sniscan.ErrNoRoots), errors.Is(err, sniscan.ErrBadAddress):
		reply(&pb.CommandResult{Error: err.Error()})
	case err != nil:
		reply(&pb.CommandResult{Error: "failed: " + shortUDPError(err)})
	default:
		a.log.Info("neighbor scan done", "summary", res.String()) // counts only: no name or address of a neighbour in the log
		reply(&pb.CommandResult{Ok: true, Params: res.Params()})
	}
}
