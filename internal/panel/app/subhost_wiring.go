//go:build !js

package app

import (
	"context"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/subhost"
)

// newSubHost wires the subscription host of the panel VPS: it moves the public address the base holds and the fleet's settings,
// and runs with the other background jobs. The stored state is applied at once, so the first request already sees the right
// public address.
func newSubHost(k *core) subHostService {
	if k.edge {
		return nil
	}
	acmeDir := ""
	if k.c.DataDir != "" {
		acmeDir = filepath.Join(k.c.DataDir, "acme") // where `mistgate serve` keeps the autocert cache
	}
	svc := subhost.New(subhost.Config{Store: k.st, Fleet: k.fl, Addr: k.addr, StepUp: k.authSvc.RequireStepUp,
		LinkPrefix: k.in.LinkPrefix, ACMEDir: acmeDir, Now: k.c.Clock, Log: k.log})
	k.acc.SetSubHostRoles(svc.RoleOf) // an inbound change that would make a host unfit is refused
	if err := svc.Load(context.Background()); err != nil {
		k.log.Error("subscription host: read state", "code", "sub_host_state_read_failed", "err", err)
	}
	k.c.BackgroundStarters = append(k.c.BackgroundStarters, BackgroundJob{Name: "subscription-host", Run: svc.Run})
	return svc
}

// addSubHost mounts the SubscriptionHostService behind the session, and gives the public server the subdomain's connections.
func (k *core) addSubHost(cfg *httpserver.Config) {
	if k.sub == nil {
		return
	}
	path, handler := k.sub.Handler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
	cfg.PublicTunnel, cfg.PublicTunnelTLS = k.sub.PublicListener(), k.sub.PublicTLS() // the subdomain's connections, from the hosts
}
