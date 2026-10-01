package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/agent"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hysteria2"
	"github.com/mistgate/mistgate/internal/node/warp"
)

// agentHooks are the things the engines and the WARP manager ask of the agent, which does not exist yet when they are
// built (agent.New builds the engines). newAgent fills them in right after New; until then they answer "nothing".
type agentHooks struct {
	dns      func() []string
	awgMode  func() string
	warpEmit func(warp.Event)
}

// wire is the one place that knows which engines and host-side services this build contains. The agent core
// (internal/node/agent) imports none of them. dns is the agent's NodeSettings resolver list.
func wire(stateDir string, log *slog.Logger, hooks *agentHooks) (agent.Config, map[string]engine.Factory, error) {
	dns := func() []string {
		if hooks.dns == nil {
			return nil
		}
		return hooks.dns()
	}
	direct := egress.New(dns)
	cs := certs.New(filepath.Join(stateDir, "certs"))
	cs.Log = log.With("source", "certs")

	// The WARP manager owns the mgwarp device, its routing table and rules; the agent feeds it the WarpSpec of the
	// desired state and hands its egress to the engines (egress "warp"). Without a WarpSpec it does nothing to the host.
	wm, err := warp.New(warp.Options{
		Log: log.With("source", "warp"), DNS: dns,
		Emit: func(ev warp.Event) {
			if hooks.warpEmit != nil {
				hooks.warpEmit(ev)
			}
		},
	})
	if err != nil {
		return agent.Config{}, nil, fmt.Errorf("warp: %w", err)
	}
	return agent.Config{
			StateDir: stateDir,
			Certs:    cs,
			Warp:     wm,
			Egress: func(name string) (engine.Egress, error) {
				switch name {
				case "", "direct":
					return direct, nil
				case "warp":
					// The agent does not start an inbound with egress warp on a node without WARP; this is the second
					// line: an engine that asks anyway gets an error, never the direct egress.
					if !wm.Configured() {
						return nil, errors.New("egress warp: not configured on this node")
					}
					return wm.Egress(), nil
				}
				return nil, fmt.Errorf("egress %q is not available on this node", name)
			},
			Masquerade: func(string) http.Handler { return decoy.Handler() },
		}, map[string]engine.Factory{
			"hysteria2": hysteria2.Factory,
			awg.Protocol: func(env engine.Env) (engine.Engine, error) {
				mode := func() string {
					if hooks.awgMode == nil {
						return ""
					}
					return hooks.awgMode()
				}
				return newAwgEngine(env, mode), nil
			},
		}, nil
}
