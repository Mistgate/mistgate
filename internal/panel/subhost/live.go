//go:build !js

package subhost

import (
	"context"

	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
)

// addLive adds what the panel sees of its hosts now to h.
func (s *Service) addLive(ctx context.Context, st state, h *subhostv1.SubscriptionHost) {}
