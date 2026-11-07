//go:build !js

package app

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/ops"
)

// addOps mounts the OpsService (log ring and level, retention, export, metrics settings) behind the session, and the /metrics
// endpoint on the admin surface. The metrics read the raw fleet, health and relay services: its own bearer token guards them.
func (k *core) addOps(cfg *httpserver.Config) {
	var oc OpsConfig
	if k.c.Ops != nil {
		oc = *k.c.Ops
	}
	agentSNIPrefix, _, _ := strings.Cut(k.in.AgentSNI, ".")
	_, fleetH := k.fl.FleetHandler()
	_, healthH := k.hl.Handler()
	_, relayH := k.acc.RelayHandler()
	svc := ops.New(ops.Config{
		Store: k.st, Ring: oc.Ring, Level: oc.Level, DefaultLevel: oc.DefaultLevel, Format: oc.Format,
		Secrets: []string{k.in.AgentSNI, agentSNIPrefix, k.in.AdminPrefix, k.in.SubPrefix, k.in.LinkPrefix},
		StepUp:  k.authSvc.RequireStepUp,
		Fleet:   fleetH, Health: healthH, Relay: relayH,
		Debug: k.fl,
		Now:   k.c.Clock, Log: k.log,
	})
	path, handler := svc.Handler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
	cfg.Metrics = svc.MetricsHandler()
}
