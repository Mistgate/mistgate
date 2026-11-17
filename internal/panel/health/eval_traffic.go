//go:build !js

package health

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The alerts that say a node is silent or cut off from a country carry how much the node moved just before: the traffic of the
// last 24 hours and of the last 7 days in bytes (up and down, all protocols), as it was when the alert opened. Someone who
// reads them later sees whether the node was busy when it went dark, which is what tells a block after a volume from a
// block of an idle address.
const (
	paramTraffic24h = "traffic_24h_bytes"
	paramTraffic7d  = "traffic_7d_bytes"
)

// withTrafficVolume puts the two numbers on the conditions of those alerts. An active alert keeps the numbers it opened with
// (they do not move as the node's quiet hours pass); a condition without one gets them now. A read that fails leaves the
// alert without them rather than without the alert.
func (s *Service) withTrafficVolume(ctx context.Context, now time.Time, d *derived, active []store.HealthAlert) {
	for k, c := range d.conds {
		if (k.kind != kNodeDown && k.kind != kCountryBlocked) || k.node == "" {
			continue
		}
		if c.params == nil {
			c.params = map[string]string{}
			d.conds[k] = c
		}
		var had map[string]string
		for _, a := range active {
			if a.Kind == k.kind && a.NodeID == k.node && a.Subject == k.subject {
				had = a.Params
			}
		}
		if had[paramTraffic24h] != "" {
			c.params[paramTraffic24h], c.params[paramTraffic7d] = had[paramTraffic24h], had[paramTraffic7d]
		} else if err := s.st.AddTrafficParams(ctx, k.node, now, c.params); err != nil {
			s.log.Warn("health: node traffic for an alert", "node", k.node, "err", err)
		}
	}
}
