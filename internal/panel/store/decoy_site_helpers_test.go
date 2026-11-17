//go:build !js

package store

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/decoy"
)

// assignOwn puts a bundle on a node through the library: the site that already holds it, or a new ready one named after the node.
func assignOwn(ctx context.Context, s *Store, nodeID string, b *decoy.Bundle) (NodeRow, error) {
	sites, err := s.DecoySites(ctx)
	if err != nil {
		return NodeRow{}, err
	}
	id := ""
	for _, site := range sites {
		if site.BundleHash == b.Hash() {
			id = site.ID
		}
	}
	if id == "" {
		site, err := s.CreateDecoySite(ctx, time.Now(), NewDecoySite{Name: "site " + nodeID, State: DecoyStateReady, Bundle: b})
		if err != nil {
			return NodeRow{}, err
		}
		id = site.ID
	}
	rows, err := s.AssignDecoySite(ctx, id, []string{nodeID})
	if err != nil {
		return NodeRow{}, err
	}
	return rows[0], nil
}
