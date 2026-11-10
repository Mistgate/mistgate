//go:build !js

package app

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/ops"
	"github.com/mistgate/mistgate/internal/panel/rpcstat"
	"github.com/mistgate/mistgate/internal/panel/update"
)

type networkSettingsNotifier interface {
	SetNetworkMode(uint32) bool
	StateChanged()
}

func networkSettingsChanged(fl networkSettingsNotifier) func(uint32) {
	return func(mode uint32) {
		if fl.SetNetworkMode(mode) {
			fl.StateChanged()
		}
	}
}

func networkTableForOps(upd *update.Service) ops.NetworkTable {
	if upd == nil {
		return nil
	}
	if table := upd.NetworksTable(); table != nil {
		return table
	}
	return nil
}

// addOps mounts the OpsService (log ring and level, retention, export, metrics settings, the panel's resources and the fleet's
// load) behind the session, and the /metrics endpoint on the admin surface. The metrics read the raw fleet, health and relay
// services: its own bearer token guards them. The HTTP middleware counts the admin API's calls into the window the service reads.
func (k *core) addOps(cfg *httpserver.Config) {
	var oc OpsConfig
	if k.c.Ops != nil {
		oc = *k.c.Ops
	}
	agentSNIPrefix, _, _ := strings.Cut(k.in.AgentSNI, ".")
	_, fleetH := k.fl.FleetHandler()
	_, healthH := k.hl.Handler()
	_, relayH := k.acc.RelayHandler()
	calls := rpcstat.New(nil)
	cfg.RPCStats = calls
	svc := ops.New(ops.Config{
		Store: k.st, Ring: oc.Ring, Level: oc.Level, DefaultLevel: oc.DefaultLevel, Format: oc.Format,
		Secrets: []string{k.in.AgentSNI, agentSNIPrefix, k.in.AdminPrefix, k.in.SubPrefix, k.in.LinkPrefix},
		StepUp:  k.authSvc.RequireStepUp,
		Fleet:   fleetH, Health: healthH, Relay: relayH,
		Debug: k.fl,
		RPC:   calls, Capacity: k.fl.CapacityNodes,
		NetworkTable: func() ops.NetworkTable { return networkTableForOps(k.upd) },
		NetworkBundle: func() (string, string) {
			if k.upd == nil {
				return "", ""
			}
			return k.upd.NetworkTableFile()
		},
		NetworkSettingsChanged: networkSettingsChanged(k.fl),
		Now:                    k.c.Clock, Log: k.log,
	})
	path, handler := svc.Handler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
	cfg.Metrics = svc.MetricsHandler()
}
