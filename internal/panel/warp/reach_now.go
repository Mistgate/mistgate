//go:build !js

package warp

import (
	"context"
	"slices"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// "Check now" of the sites card: GetWarpRequest.check_reach_now asks the node to check the sites out of turn. Nothing new is mounted for
// it (a field of the call that the card already makes, the answer a field of its response), and none of it is in the Cloudflare
// edition, which runs no check (reach_js.go). The check runs in the background: the answer of the call says whether the node was asked,
// and the card reads the result, and whether a check is still on, with its usual reads.

// reachByHand is the least time between two checks asked by hand for one node.
const reachByHand = 2 * time.Minute

// reachRunBound is how long a check asked by hand may take: the fleet waits 60 s for the node's answer, and a margin.
const reachRunBound = 75 * time.Second

// claimReach marks the node's check as on; false when one is on already.
func (s *Service) claimReach(nodeID string) bool {
	s.reachMu.Lock()
	defer s.reachMu.Unlock()
	if s.reachBusy[nodeID] {
		return false
	}
	if s.reachBusy == nil {
		s.reachBusy = map[string]bool{}
	}
	s.reachBusy[nodeID] = true
	return true
}

// releaseReach ends the node's check and keeps how it ended ("" = the node was not asked: nothing to keep).
func (s *Service) releaseReach(nodeID, how string) {
	s.reachMu.Lock()
	defer s.reachMu.Unlock()
	delete(s.reachBusy, nodeID)
	if how == "" {
		return
	}
	if s.reachLast == nil {
		s.reachLast = map[string]reachEnd{}
	}
	s.reachLast[nodeID] = reachEnd{how: how, at: s.now()}
}

// reachCheck is GetWarpResponse.reach_check: whether a check of the node is on, how the newest ended and, when the call asked for one
// (ask), what the request did. nil while there is nothing to say.
func (s *Service) reachCheck(ctx context.Context, n store.NodeRow, a store.WarpAccountRow, ask bool) *adminv1.WarpReachCheck {
	v := &adminv1.WarpReachCheck{}
	if ask {
		var retry time.Duration
		v.Asked, retry = s.askReach(ctx, n, a)
		v.RetryAfterS = uint32((retry + time.Second - 1) / time.Second)
	}
	s.reachMu.Lock()
	v.Running = s.reachBusy[n.ID]
	if e, ok := s.reachLast[n.ID]; ok {
		v.Last, v.LastUnix = e.how, e.at.Unix()
	}
	s.reachMu.Unlock()
	if !v.Running && v.Asked == "" && v.Last == "" {
		return nil
	}
	return v
}

// askReach starts a check of the node's sites and says what it did: "started", "running", "too_soon" (with the time left),
// "agent_too_old", "node_offline", "warp_down" or "disabled". The check runs in the background.
func (s *Service) askReach(ctx context.Context, n store.NodeRow, a store.WarpAccountRow) (string, time.Duration) {
	domains, _ := s.reachDomains(ctx)
	switch {
	case len(domains) == 0:
		return "disabled", 0
	case !s.supportsReach(n):
		return "agent_too_old", 0
	case n.State != "active":
		return "node_offline", 0
	}
	if live, err := s.liveNode(ctx, n.ID); err != nil || !live.Connected {
		return "node_offline", 0
	}
	if !a.Enabled || ViewState(&a, true, s.now()) != adminv1.WarpState_WARP_STATE_UP {
		return "warp_down", 0
	}
	if _, ok := s.fl.(reachFleet); !ok {
		return "disabled", 0
	}
	s.reachMu.Lock()
	if s.reachBusy[n.ID] {
		s.reachMu.Unlock()
		return "running", 0
	}
	if t, ok := s.reachAsk[n.ID]; ok && s.now().Before(t.Add(reachByHand)) {
		wait := t.Add(reachByHand).Sub(s.now())
		s.reachMu.Unlock()
		return "too_soon", wait
	}
	if s.reachBusy == nil {
		s.reachBusy = map[string]bool{}
	}
	if s.reachAsk == nil {
		s.reachAsk = map[string]time.Time{}
	}
	s.reachBusy[n.ID], s.reachAsk[n.ID] = true, s.now()
	s.reachMu.Unlock()
	s.reachWG.Add(1)
	go func() {
		defer s.reachWG.Done()
		// it outlives the request that asked: the answer is stored whether or not the page is still open
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reachRunBound)
		defer cancel()
		s.runReach(rctx, a, slices.Clone(domains))
	}()
	return "started", 0
}
