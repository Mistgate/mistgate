package health

import (
	"context"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// TSPUState is what the country check needs to know of the TSPU probe (internal/panel/countryprobe): whether its results count
// (it is on, has a probe node, and covers the country), the probe node whose rows to read, and how old a row may be to still
// say something about the node now. Run is the newest run's status, for GetChecks (nil: none).
type TSPUState struct {
	On      bool
	Vantage string
	MaxAge  time.Duration
	Run     *adminv1.TspuProbeRun
}

func (s *Service) tspuState(ctx context.Context) TSPUState {
	if s.cfg.TSPU == nil {
		return TSPUState{}
	}
	return s.cfg.TSPU(ctx)
}

// ProbeNode is an active node as the TSPU probe needs it: whether it is connected and what its agent can do (to choose the probe
// node), and the address and TCP ports the probe tests (the same ports Globalping uses).
type ProbeNode struct {
	ID, Name, Address, CountryCode string
	Caps                           []string
	Online                         bool
	Ports                          []uint16
}

// ProbeNodes lists the active nodes for the TSPU probe.
func (s *Service) ProbeNodes(ctx context.Context) ([]ProbeNode, error) {
	sn, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		return nil, err
	}
	live := indexLiveRows(liveRows)
	var out []ProbeNode
	for _, n := range sn.nodes {
		if n.State != "active" {
			continue
		}
		l := live[n.ID]
		out = append(out, ProbeNode{ID: n.ID, Name: n.Name, Address: n.Address, CountryCode: n.CountryCode, Caps: l.AgentCaps, Online: l.Connected,
			Ports: countryPorts(sn.byNode[n.ID])})
	}
	return out, nil
}
