//go:build !js

package app

import (
	"context"

	"github.com/mistgate/mistgate/internal/panel/cheburcheck"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/ownprobe"
)

// countryNowSources are the sources "Check now" of a node asks for a round of the country check out of turn besides Globalping
// (health.Config.CountryNow): the TSPU probe, cheburcheck on both of its sources and the owner's own probes. bsbord is not among
// them: it is paid and runs on its schedule only. The services are read when the request comes (the own probes are built after
// the health module).
func countryNowSources(k *core) []health.CountryNowSource {
	cheb := func(source string) func(context.Context, string) (health.CountryNowRun, string) {
		return func(ctx context.Context, nodeID string) (health.CountryNowRun, string) {
			return k.cc.NowPlan(ctx, nodeID, source)
		}
	}
	return []health.CountryNowSource{
		{Source: countryreach.SourceTSPU, Plan: func(ctx context.Context, nodeID string) (health.CountryNowRun, string) {
			return k.cp.NowPlan(ctx, nodeID)
		}},
		{Source: countryreach.SourceCheburcheck, Plan: cheb(cheburcheck.SourcePublic)},
		{Source: countryreach.SourceCheburcheckOwn, Plan: cheb(cheburcheck.SourceOwn)},
		{Source: countryreach.SourceProbes, Plan: func(ctx context.Context, nodeID string) (health.CountryNowRun, string) {
			if own, ok := k.own.(*ownprobe.Service); ok {
				return own.NowPlan(ctx, nodeID)
			}
			return nil, "off"
		}},
	}
}
