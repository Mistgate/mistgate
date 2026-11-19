//go:build !js

package countryprobe

import (
	"context"
	"errors"
)

// NowPlan is the TSPU probe's part of "Check now" of a node (health.CountryNowSource): the probe node tests that one node out of
// turn. It says at once why it will not (the reason words of health.CountryNowSource) or returns the run, which keeps the run lock
// from now until it ends. The run stores the node's rows like a scheduled one, but leaves the status of the newest run and the
// rotation of the batches alone. A run is one batch of one node: about a minute; its status is the reason when it did not end "ok".
func (s *Service) NowPlan(ctx context.Context, nodeID string) (run func(context.Context) (string, string), skip string) {
	if s.cfg.Edge || censorSimManualOnly {
		return nil, "off"
	}
	gen := s.generation()
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("countryprobe: read settings", "err", err)
		return nil, "error"
	}
	if !st.enabled || st.node == "" || s.unavailable(ctx) != "" {
		return nil, "off"
	}
	nodes, err := s.cfg.Nodes(ctx)
	if err != nil {
		s.log.Warn("countryprobe: read nodes", "err", err)
		return nil, "error"
	}
	probe, fit := probeNode(nodes, st.node)
	if !fit {
		return nil, "no_probe"
	}
	if nodeID == probe.ID {
		return nil, "probe_node" // the probe node does not test itself
	}
	var target Node
	for _, n := range nodes {
		if n.ID == nodeID {
			target = n
		}
	}
	switch solo := []Node{probe, target}; {
	case target.ID == "" || !target.Online:
		return nil, "node_offline"
	case len(orderPorts(target.Ports)) == 0:
		return nil, "no_ports"
	case len(targetsOf(probe, solo)) == 0:
		return nil, "no_address" // it shares an address with the probe node
	}
	if !s.runMu.TryLock() {
		return nil, "busy"
	}
	return func(ctx context.Context) (string, string) {
		defer s.runMu.Unlock()
		rctx, cancel := context.WithTimeout(ctx, runBudget)
		defer cancel()
		res, err := s.runNow(rctx, gen, st, probe, []Node{probe, target}, true)
		switch {
		case errors.Is(err, errSwitchedOff):
			return "failed", "off"
		case err != nil:
			s.log.Warn("countryprobe: run for one node failed", "err", err)
			return "failed", "error"
		case res.run.Status != StatusOK:
			return "failed", res.run.Status
		}
		s.changed(ctx)
		return "done", ""
	}, ""
}
