//go:build !js

package app

import (
	"context"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/subhost"
)

// newSubHost wires the subscription host of the panel VPS: it moves the public address the base holds and the fleet's routing,
// and runs its tunnels with the other background jobs. The stored state is applied at once, so the first request already
// sees the right public address.
func newSubHost(k *core) subHostService {
	if k.edge { // the edge edition (Remote set) has its own public address
		return nil
	}
	acmeDir := ""
	if k.c.DataDir != "" {
		acmeDir = filepath.Join(k.c.DataDir, "acme") // where `mistgate serve` keeps the autocert cache
	}
	svc := subhost.New(subhost.Config{Store: k.st, Fleet: k.fl, Addr: k.addr, StepUp: k.authSvc.RequireStepUp,
		LinkPrefix: k.in.LinkPrefix, ACMEDir: acmeDir, Now: k.c.Clock, Log: k.log})
	if err := svc.Load(context.Background()); err != nil {
		k.log.Error("subscription host: read state", "code", "sub_host_state_read_failed", "err", err)
	}
	k.c.BackgroundStarters = append(k.c.BackgroundStarters, BackgroundJob{Name: "subscription-host", Run: svc.Run})
	return svc
}

func subHostAdminHandlers(sub subHostService) []httpserver.AdminHandler {
	if sub == nil {
		return nil
	}
	path, handler := sub.Handler()
	return []httpserver.AdminHandler{{Path: path, Handler: handler}}
}
