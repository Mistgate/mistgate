//go:build !js

package ownprobe

import (
	"context"
	"time"
)

// SilentAfter is how long an active probe may keep silent and still be asked: the Own probes card says "no contact" past it
// (web/src/lib/ownprobe.ts offlineAfterSec). A probe that has been silent longer than its interval cannot take a job, so it is
// not asked, and the card says so.
const SilentAfter = time.Hour

// NowPlan is the owner's probes' part of "Check now" of a node (health.CountryNowSource): every active probe that has been heard
// from lately has its next job made due now, so that it takes the next one at its next sync (the probes ask every 12 to 18
// minutes; the job covers the fleet's nodes, this one among them). Nothing is sent to a probe: it comes for the job itself. The
// source reads "queued", and the result reaches the verdict when the probes report. With no such probe it says why.
func (s *Service) NowPlan(ctx context.Context, _ string) (run func(context.Context) (string, string), skip string) {
	settings, err := s.settings(ctx)
	if err != nil {
		s.cfg.Log.Warn("own probes: read settings", "err", err)
		return nil, "error"
	}
	if !settings.Enabled {
		return nil, "off"
	}
	probes, err := s.cfg.Store.OwnProbes(ctx)
	if err != nil {
		s.cfg.Log.Warn("own probes: read probes", "err", err)
		return nil, "error"
	}
	now := s.cfg.Now()
	var ids []string
	active := false
	for _, p := range probes {
		if p.State != "active" {
			continue
		}
		active = true
		if now.Sub(p.LastSeen) <= SilentAfter {
			ids = append(ids, p.ID)
		}
	}
	switch {
	case len(ids) == 0 && !active:
		return nil, "no_probe"
	case len(ids) == 0:
		return nil, "probe_silent"
	}
	return func(ctx context.Context) (string, string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		asked := 0
		for _, id := range ids {
			if err := s.cfg.Store.DueOwnProbe(ctx, id, s.cfg.Now()); err == nil {
				asked++
			}
		}
		if asked == 0 {
			return "failed", "error"
		}
		return "queued", ""
	}, ""
}
