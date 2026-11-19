//go:build !js

package health

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
	"github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The targets of the own probes. A file of its own, built for the VPS only: the Cloudflare edition has no probes, and its Workers
// have a size limit (scripts/check-edge-sizes.sh).

// probeTurn is how long one service of a shared port is the node's TLS target.
const probeTurn = 15 * time.Minute

// ProbeTarget is one public IPv4 measurement made by an owner-managed probe. It intentionally has no node name.
type ProbeTarget struct {
	NodeID string
	IP     string
	Port   uint16
	Kind   string // tcp, tls or fetch
	SNI    string
	Path   string
}

// ProbeTargets builds the panel-owned targets for the own-probe protocol. Only connected, active nodes with public IPv4
// addresses are included, and no node contributes more than two targets. Fleet-wide rotation is applied by ownprobe.
func (s *Service) ProbeTargets(ctx context.Context) ([]ProbeTarget, error) {
	nodes, err := s.countryNodes(ctx)
	if err != nil {
		return nil, err
	}
	sn, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var out []ProbeTarget
	for _, n := range nodes {
		ip, ok := s.countryAddress(ctx, n.node)
		if !ok {
			continue
		}
		if !countryreach.ProbeIPv4(ip) {
			continue
		}
		nodeTargets := make([]ProbeTarget, 0, 2)
		if len(n.ports) > 0 {
			nodeTargets = append(nodeTargets, ProbeTarget{NodeID: n.node.ID, IP: ip, Port: n.ports[0], Kind: "tcp"})
		}
		var tlsTargets []ProbeTarget
		for _, t := range sn.byNode[n.node.ID] {
			if !t.in.Enabled || t.in.State != "active" || t.err != nil ||
				(t.in.Protocol != hysteria2.ID && t.spec.Listen.Network != "tcp") {
				continue
			}
			switch t.in.Protocol {
			case vless.ID:
				var cfg vless.Settings
				if json.Unmarshal(t.settings, &cfg) != nil {
					continue
				}
				sni := probeName(t.spec)
				if !countryreach.ProbeSNI(sni) {
					continue
				}
				path := "/"
				switch cfg.Transport {
				case "xhttp":
					if cfg.XHTTP.Path != "" {
						path = cfg.XHTTP.Path
					}
				case "ws":
					if cfg.WS != nil && cfg.WS.Path != "" {
						path = cfg.WS.Path
					}
				}
				if !countryreach.ProbePath(path) {
					continue
				}
				kind := "tls"
				// the spec, not the profile: a node can show a site of its own, and the node's own site is what a fetch would not get
				if _, _, other := plugin.RealityTarget(t.spec); other {
					kind = "fetch"
				}
				tlsTargets = append(tlsTargets, ProbeTarget{NodeID: n.node.ID, IP: ip, Port: t.spec.Listen.Port, Kind: kind, SNI: sni, Path: path})
			case hysteria2.ID:
				sni := probeName(t.spec)
				if !countryreach.ProbeSNI(sni) {
					continue
				}
				if port := hy2DecoyPort(t); port > 0 {
					tlsTargets = append(tlsTargets, ProbeTarget{NodeID: n.node.ID, IP: ip, Port: port, Kind: "tls", SNI: sni, Path: "/"})
				}
			}
		}
		slices.SortStableFunc(tlsTargets, func(a, b ProbeTarget) int { return cmp.Compare(a.Port, b.Port) })
		if len(tlsTargets) > 0 && len(nodeTargets) < 2 {
			// Services that share the lowest port by SNI are told apart by the name the probe sends; a node has room for one TLS
			// target, so each takes its turn (ponytail: the turn follows the clock, a quarter of an hour each; a probe that
			// compares a service with itself over time sees the same name for that long).
			same := 1
			for same < len(tlsTargets) && tlsTargets[same].Port == tlsTargets[0].Port {
				same++
			}
			nodeTargets = append(nodeTargets, tlsTargets[int(s.now().Unix()/int64(probeTurn.Seconds()))%same])
		}
		out = append(out, nodeTargets...)
	}
	slices.SortStableFunc(out, func(a, b ProbeTarget) int {
		if c := strings.Compare(a.NodeID, b.NodeID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Port, b.Port); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return out, nil
}

// probeName is the name the inbound answers to: the one in its node-side spec, which carries the node's own "Domain (SNI)"
// (the inbound's override) where the profile's settings only carry the default. A REALITY inbound with another site has none
// in its TLS block and takes the first of its server names.
func probeName(spec plugin.InboundSpec) string {
	if spec.TLS.ServerName != "" {
		return spec.TLS.ServerName
	}
	var settings struct {
		Reality struct {
			ServerNames []string `json:"server_names"`
		} `json:"reality"`
	}
	if json.Unmarshal(spec.Settings, &settings) == nil && len(settings.Reality.ServerNames) > 0 {
		return settings.Reality.ServerNames[0]
	}
	return ""
}
