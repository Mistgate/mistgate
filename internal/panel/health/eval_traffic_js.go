//go:build js

package health

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The traffic numbers on the node_down and country_blocked alerts are the VPS edition's (eval_traffic.go): the Workers' alerts
// carry none.
func (s *Service) withTrafficVolume(context.Context, time.Time, *derived, []store.HealthAlert) {}
