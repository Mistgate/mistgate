package fleet

import (
	"crypto/tls"
	"time"
)

// HostTunnel is what the subscription host node is told: the SNI the panel dials it with and the host names it forwards
// to the panel.
type HostTunnel struct {
	SNI   string
	Names []string
}

// PanelRouting is how the panel is reached at the moment: the link address agents with panel-link/1 follow, the
// subscription host tunnels by node id, whether new nodes enrol link-only, and which node is the subscription host.
type PanelRouting struct {
	LinkURL  string
	Tunnels  map[string]HostTunnel
	LinkOnly bool
	IsHost   func(nodeID string) bool
}

// SetPanelRouting replaces the routing. The caller calls StateChanged so connected agents get their new settings.
func (f *Fleet) SetPanelRouting(r PanelRouting) { f.routing.Store(&r) }

// panelRouting is the current routing (zero value when none was set).
func (f *Fleet) panelRouting() PanelRouting {
	if r := f.routing.Load(); r != nil {
		return *r
	}
	return PanelRouting{}
}

// SessionVia is the Host a node's live link session dialled; "" for an mTLS session or no session in this process.
func (f *Fleet) SessionVia(nodeID string) string {
	if s := f.session(nodeID); s != nil {
		return s.via
	}
	return ""
}

// IsSubscriptionHost says whether the node carries the subscription subdomain now (or is about to, during a move).
func (f *Fleet) IsSubscriptionHost(nodeID string) bool {
	r := f.panelRouting()
	return r.IsHost != nil && r.IsHost(nodeID)
}

// tunnelCert is a cached client certificate of the panel for one tunnel SNI.
type tunnelCert struct {
	cert    tls.Certificate
	renewAt time.Time
}
