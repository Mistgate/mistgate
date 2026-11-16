//go:build !js

package app

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/mistgate/mistgate/internal/panel/countryprobe"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/ownprobe"
)

// newOwnProbes wires the probes of the panel VPS. The Cloudflare edition has none: its Workers have no room for them and no
// place to keep a probe's state between two requests (ownprobe_wiring_js.go).
func newOwnProbes(k *core) ownProbeService {
	hl, upd := k.hl, k.upd
	return ownprobe.New(ownprobe.Config{Store: k.st, Limiter: k.limiter, StepUp: k.authSvc.RequireStepUp,
		PublicURL: k.in.PublicURL, ProbePrefix: k.in.ProbePrefix, Now: k.c.Clock, Country: k.cr.Country,
		Targets: func(ctx context.Context) ([]ownprobe.Target, error) {
			targets, err := hl.ProbeTargets(ctx)
			out := make([]ownprobe.Target, len(targets))
			for i, target := range targets {
				out[i] = ownprobe.Target{NodeID: target.NodeID, IP: target.IP, Port: target.Port, Kind: target.Kind, SNI: target.SNI, Path: target.Path}
			}
			return out, err
		},
		ASN: func(ip netip.Addr) uint32 {
			if table := upd.NetworksTable(); table != nil {
				return table.Lookup(ip)
			}
			return 0
		},
		Operator: health.NetworkOperator,
		References: func(country string) (ownprobe.References, bool) {
			refs, ok := countryprobe.ReferencesFor(country)
			if !ok || len(refs.Blocked) == 0 {
				return ownprobe.References{}, false
			}
			return ownprobe.References{ControlIP: refs.Control, ControlSNI: refs.ControlSNI, BlockedIP: refs.Blocked[0]}, true
		},
		OnChange: func(ctx context.Context) { hl.ReconcileCountry(ctx) }, Log: k.log})
}

func ownProbeAdminHandlers(own ownProbeService) []httpserver.AdminHandler {
	if own == nil {
		return nil
	}
	path, handler := own.Handler()
	return []httpserver.AdminHandler{{Path: path, Handler: handler}}
}

// ownProbeStates is the "probes" source of the country reachability when the owner switched it on.
func ownProbeStates(ctx context.Context, own ownProbeService, log *slog.Logger) []health.ExternalState {
	if own == nil {
		return nil
	}
	on, maxAge, err := own.SourceState(ctx)
	if err != nil {
		log.Warn("health: read own probe settings", "code", "own_probe_settings_failed", "err", err)
		return nil
	}
	if !on {
		return nil
	}
	return []health.ExternalState{{Source: countryreach.SourceProbes, On: true, MaxAge: maxAge}}
}
