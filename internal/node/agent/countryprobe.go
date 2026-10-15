package agent

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tspuprobe"
)

// capCountryProbe: this host can run CountryProbe (agent.proto "COUNTRY PROBE"). It needs no capability and no unit change.
const capCountryProbe = "country-probe/1"

// countryProbeState is the agent's bookkeeping for CountryProbe: one run at a time, and the test seams.
type countryProbeState struct {
	running atomic.Bool
	// supported and run are nil in production (tspuprobe.Supported and tspuprobe.Run).
	supported func() bool
	run       func(context.Context, tspuprobe.Request) (tspuprobe.Result, error)
}

func (c *countryProbeState) canProbe() bool {
	if c.supported != nil {
		return c.supported()
	}
	return tspuprobe.Supported()
}

// countryProbe handles one CountryProbe command: place the DPI box on the path from this node, then say for every target
// host:port whether its SYNs get past it. The answer is one CommandResult (agent.proto "COUNTRY PROBE").
func (a *Agent) countryProbe(s *session, r *pb.CountryProbe) {
	reply := func(res *pb.CommandResult) {
		res.RequestId = r.RequestId
		s.send(cmdResult(res))
	}
	req, err := countryRequest(r)
	if err == nil {
		err = tspuprobe.Validate(req)
	}
	if err != nil {
		reply(&pb.CommandResult{Error: countryProbeError(err)})
		return
	}
	if !a.country.canProbe() {
		reply(&pb.CommandResult{Error: "unsupported"})
		return
	}
	if a.measuring.Load() || !a.country.running.CompareAndSwap(false, true) {
		reply(&pb.CommandResult{Error: "busy"})
		return
	}
	defer a.country.running.Store(false)

	run := a.country.run
	if run == nil {
		run = tspuprobe.Run
	}
	res, err := run(s.ctx, req)
	if s.ctx.Err() != nil {
		return
	}
	switch {
	case err == nil:
		params := make(map[string]string, len(res.Verdicts)+2)
		params["dpi_hop"] = strconv.Itoa(res.DPIHop)
		params["control"] = "ok"
		for key, verdict := range res.Verdicts {
			params["t"+key] = verdict
		}
		reply(&pb.CommandResult{Ok: true, Params: params})
	case errors.Is(err, tspuprobe.ErrControlFailed):
		reply(&pb.CommandResult{Error: "control_failed", Params: map[string]string{"dpi_hop": strconv.Itoa(res.DPIHop), "control": "fail"}})
	default:
		out := &pb.CommandResult{Error: countryProbeError(err)}
		var noHop *tspuprobe.NoDPIHopError
		if errors.As(err, &noHop) {
			out.Params = map[string]string{"reason": noHop.Reason}
		}
		reply(out)
	}
}

func countryRequest(r *pb.CountryProbe) (tspuprobe.Request, error) {
	req := tspuprobe.Request{Control: r.ControlTarget, BlockedRef: r.BlockedRef, HopLimit: int(r.HopLimit), MaxTTL: int(r.MaxTtl)}
	if r.HopLimit > tspuprobe.MaxHopLimit || r.MaxTtl > tspuprobe.MaxMaxTTL || len(r.Targets) > tspuprobe.MaxTargets {
		return req, tspuprobe.ErrBadParams
	}
	for _, t := range r.Targets {
		if t == nil || len(t.Ports) > tspuprobe.MaxPorts {
			return req, tspuprobe.ErrBadParams
		}
		target := tspuprobe.Target{Host: t.Host}
		for _, port := range t.Ports {
			if port == 0 || port > 65535 {
				return req, tspuprobe.ErrBadParams
			}
			target.Ports = append(target.Ports, uint16(port))
		}
		req.Targets = append(req.Targets, target)
	}
	return req, nil
}

func countryProbeError(err error) string {
	switch {
	case errors.Is(err, tspuprobe.ErrBadParams):
		return "bad_params"
	case errors.Is(err, tspuprobe.ErrUnsupportedHost):
		return "unsupported_host"
	case errors.Is(err, tspuprobe.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, tspuprobe.ErrNoDPIHop):
		return "no_dpi_hop"
	case errors.Is(err, tspuprobe.ErrControlFailed):
		return "control_failed"
	case errors.Is(err, tspuprobe.ErrDeadline):
		return "failed: deadline"
	default:
		return "failed: " + shortUDPError(err)
	}
}
