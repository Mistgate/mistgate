//go:build !js

package subhost

import (
	"context"
	"strconv"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// Readiness: the panel moves its subscriptions and agents to the subdomain only once the host can carry them. Until then the
// host has its role and the panel dials it, but the address stays what it was (design/adr/0018-panel-behind-nat.md). A host
// that later stops being ready does not move them back: the agents keep their fallback to the previous address.

// The conditions readiness waits for (SubscriptionHost.not_ready).
const (
	notReadyOffline   = "host_offline" // the host's agent is not connected
	notReadyHostError = "host_error"   // the host reports no role yet, or a role that does not run
	notReadySiteCert  = "site_cert"    // the host's site on 443 has no publicly trusted certificate yet
	notReadyBridge    = "bridge"       // the panel holds no connection to the host
	notReadyPanelCert = "panel_cert"   // the panel has no certificate for the subdomain yet
)

// tunnelDownAfter is how long the panel's connection to the host may stay down before the owner hears of it.
const tunnelDownAfter = 30 * time.Second

// notReady lists what keeps the host of st from being ready; empty = ready.
func (s *Service) notReady(ctx context.Context, st state) []string {
	if st.State == stateOff || st.Node == "" {
		return nil
	}
	var out []string
	if !s.cfg.Fleet.HasSession(st.Node) {
		out = append(out, notReadyOffline)
	}
	if hs := s.cfg.Fleet.SubHostStatus(st.Node); hs == nil || hs.Error != "" {
		out = append(out, notReadyHostError)
	}
	if _, _, _, public, _ := s.cfg.Fleet.SubHostTarget(ctx, st.Node, st.SiteName, st.BootstrapPin); !public {
		out = append(out, notReadySiteCert)
	}
	if !s.bridgeUp(st.Node) {
		out = append(out, notReadyBridge)
	}
	if !s.panelCert(ctx, st.Domain) {
		out = append(out, notReadyPanelCert)
	}
	return out
}

func (s *Service) bridgeUp(nodeID string) bool {
	if s.upSeam != nil {
		return s.upSeam(nodeID)
	}
	for _, d := range s.tun.dialer.States() {
		if d.PeerNodeID == nodeID {
			return d.Conns > 0
		}
	}
	return false
}

// panelCert says the panel has the subdomain's certificate: a visitor was handed it, or the ACME cache holds it.
func (s *Service) panelCert(ctx context.Context, domain string) bool {
	if s.cfg.Certificate != nil {
		return true
	}
	s.tun.mu.Lock()
	seen := s.tun.certSeen
	s.tun.mu.Unlock()
	if seen {
		return true
	}
	if s.tun.acme.Cache == nil || domain == "" {
		return false
	}
	_, err := s.tun.acme.Cache.Get(ctx, domain)
	return err == nil
}

// checkReady moves the panel to the subdomain once the host is ready, and tells the owner when the panel's connection to the
// host goes down and comes back.
func (s *Service) checkReady(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.current()
	if st.State == stateOff {
		s.downSince, s.downTold = time.Time{}, false
		return
	}
	now := s.cfg.Now()
	switch up := s.bridgeUp(st.Node); {
	case up && s.downTold:
		s.downSince, s.downTold = time.Time{}, false
		s.event(ctx, 1, "sub_host_tunnel_up", st.Node, nil)
	case up:
		s.downSince = time.Time{}
	case s.downSince.IsZero():
		s.downSince = now
	case !s.downTold && now.Sub(s.downSince) >= tunnelDownAfter:
		s.downTold = true
		s.event(ctx, 2, "sub_host_tunnel_down", st.Node, nil)
	}
	if st.ReadyAt != "" || (st.State != stateOn && st.State != stateMoving) || len(s.notReady(ctx, st)) > 0 {
		return
	}
	stored, err := loadState(ctx, s.cfg.Store)
	if err != nil || stored.Node != st.Node || stored.ReadyAt != "" {
		return
	}
	stored.ReadyAt, stored.PublicURL = strconv.FormatInt(now.Unix(), 10), "https://"+stored.Domain
	if err := saveState(ctx, s.cfg.Store, stored); err != nil {
		s.cfg.Log.Error("subscription host: save state", "code", "sub_host_internal", "err", err)
		return
	}
	s.applyRouting(ctx, stored)
	s.cfg.Log.Info("subscription host ready: subscriptions and agents move to the subdomain", "domain", stored.Domain, "node", stored.Node)
	s.event(ctx, 1, "sub_host_ready", stored.Node, map[string]string{"domain": stored.Domain})
}

func (s *Service) event(ctx context.Context, sev int, code, nodeID string, params map[string]string) {
	if err := s.cfg.Store.InsertEvent(ctx, store.EventRow{Time: s.cfg.Now(), Severity: sev, Code: code, Source: "panel", NodeID: nodeID, Params: params}); err != nil {
		s.cfg.Log.Warn("subscription host: write event", "code", "sub_host_event_failed", "event", code, "err", err)
	}
}
