package access

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// capTCPShare is what an agent lists when it can share a TCP port between services by SNI (agent.proto "SHARED TCP PORT").
const capTCPShare = "tcp-share/1"

// shareProtocols are the protocols whose TCP inbounds can stand on one port, told apart by the name in the first ClientHello
// (plugin.TCPSharePlan). A TCP inbound of any other protocol keeps the port to itself.
var shareProtocols = []string{"vless", "trusttunnel", "mtproto"}

// portSubject says who asks for the port: the profile (named in a refusal) and what the node's agent lists (nil: the node has not
// said yet, so it is not refused for being old: the deployment withholds what an old agent cannot take).
type portSubject struct {
	Profile string
	Agent   []string
	Address string // the node's address, for the name a refusal suggests
}

// isSharePeer says other is an inbound of the node that spec may share its TCP port with: both listen on the same TCP port, with no
// hop range, and are of protocols that take part. A relay's forward or a bridge has no inbound spec and is never one.
func isSharePeer(spec plugin.InboundSpec, other nodeListen) bool {
	return spec.ID != "" && other.spec.ID != "" && spec.ID != other.spec.ID &&
		spec.Listen.Network == "tcp" && other.l.Network == "tcp" && spec.Listen.Port != 0 && spec.Listen.Port == other.l.Port &&
		spec.Listen.HopFrom == 0 && other.l.HopFrom == 0 &&
		slices.Contains(shareProtocols, spec.Protocol) && slices.Contains(shareProtocols, other.protocol)
}

func sharePeers(spec plugin.InboundSpec, others []nodeListen) []nodeListen {
	var out []nodeListen
	for _, o := range others {
		if isSharePeer(spec, o) {
			out = append(out, o)
		}
	}
	return out
}

// shareRefusal is the refusal of a TCP port that spec would share with other services of the node and cannot: the agent is too
// old, a name is asked for twice, a service has no name, or none of them serves the node's own site. nil when spec has no peer, or
// the port can be shared. Disabled neighbours count: switching one on later must not break the port. Only what spec brings is
// refused: a node that is in that state already (an address changed under it) is the node page's to show.
func shareRefusal(spec plugin.InboundSpec, node string, others []nodeListen, who portSubject, free func() uint16) error {
	peers := sharePeers(spec, others)
	if len(peers) == 0 {
		return nil
	}
	port := strconv.Itoa(int(spec.Listen.Port))
	if len(who.Agent) > 0 && !slices.Contains(who.Agent, capTCPShare) {
		return coded(connect.CodeFailedPrecondition, "share_agent_old", "port", port, "node", node, "profile", peers[0].profile)
	}
	names := map[string]string{spec.ID: who.Profile}
	var before, after []plugin.InboundSpec
	for _, p := range peers {
		names[p.spec.ID] = p.profile
		before = append(before, p.spec)
	}
	after = append(slices.Clone(before), spec)
	known := map[plugin.TCPShareProblem]bool{}
	for _, p := range plugin.TCPShareProblems(before, spec.Listen.Port) {
		known[p] = true
	}
	for _, p := range plugin.TCPShareProblems(after, spec.Listen.Port) {
		if known[p] {
			continue
		}
		// the other party of the refusal is the one that is not spec
		other := p.ID
		if p.ID == spec.ID {
			other = p.By
		}
		kv := []string{"port", port, "node", node, "profile", names[other]}
		if port := free(); port != 0 {
			kv = append(kv, "free", strconv.Itoa(int(port)))
		}
		switch p.Code {
		case plugin.SNITaken:
			kv = append(kv, "name", p.Name)
			// The admin can put the profile beside the others under a name of its own when the name it answers to is its to
			// set (the node's own site: the domain of the inbound; not the target of a REALITY that shows another site).
			if r, ok := plugin.TCPRouteOf(spec); ok && r.Site {
				kv = append(kv, "share", "1")
				if name := suggestShareName(who.Address, after); name != "" {
					kv = append(kv, "suggest", name)
				}
			}
			return coded(connect.CodeAlreadyExists, plugin.SNITaken, kv...)
		case plugin.ShareUnsupported:
			// the profile named is the service that cannot be told apart, which is the one asked for or a neighbour
			kv[5] = names[p.ID]
			return coded(connect.CodeFailedPrecondition, plugin.ShareUnsupported, append(kv, "protocol", protocolOf(p.ID, spec, peers))...)
		case plugin.ShareNeedsSite:
			kv[5] = peers[0].profile
			return coded(connect.CodeFailedPrecondition, plugin.ShareNeedsSite, kv...)
		}
	}
	return nil
}

// shareWords are the first labels the panel suggests for a service that stands beside another on a port: neutral words of a site
// with a content network, so that the names a visitor can find (certificate logs, a scan of names) say nothing about a VPN.
var shareWords = []string{"cdn", "static", "media", "assets", "files", "img", "edge", "app"}

// suggestShareName is "<word>.<address>" for the first word no service of the port answers to, "" when the node is known by an IP
// address (a name of its own needs a domain).
func suggestShareName(address string, specs []plugin.InboundSpec) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(address), "."))
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil || !strings.Contains(host, ".") {
		return ""
	}
	taken := map[string]bool{}
	for _, s := range specs {
		if r, ok := plugin.TCPRouteOf(s); ok {
			for _, n := range r.Names {
				taken[n] = true
			}
		}
	}
	for _, w := range shareWords {
		if name := w + "." + host; !taken[name] {
			return name
		}
	}
	return ""
}

func protocolOf(id string, spec plugin.InboundSpec, peers []nodeListen) string {
	if id == spec.ID {
		return spec.Protocol
	}
	for _, p := range peers {
		if p.spec.ID == id {
			return p.protocol
		}
	}
	return ""
}

// shareWarnings are what the admin should know about the TCP port spec stands on before it is added or switched on: that it
// shares the port (and who restarts), and that a stranger who asks for the name of a service that shows another site sees that
// site, not the node's own. Only the enabled neighbours take part: a disabled one runs nothing.
func shareWarnings(node string, spec plugin.InboundSpec, profile string, others []nodeListen) []*adminv1.StatusReason {
	var peers []nodeListen
	for _, p := range sharePeers(spec, others) {
		if p.enabled {
			peers = append(peers, p)
		}
	}
	if len(peers) == 0 {
		return nil
	}
	specs := []plugin.InboundSpec{spec}
	names := map[string]string{spec.ID: profile}
	for _, p := range peers {
		specs = append(specs, p.spec)
		names[p.spec.ID] = p.profile
	}
	share := plugin.TCPSharePlan(specs)[spec.Listen.Port]
	if !share.Front() {
		return nil
	}
	port := strconv.Itoa(int(spec.Listen.Port))
	var ids []string
	for _, r := range share.Routes {
		ids = append(ids, names[r.ID])
	}
	params := map[string]string{"node": node, "port": port, "profiles": strings.Join(ids, ", "), "default": names[share.Default]}
	// The services that stood alone on the port are started again behind the front, so what they carry reconnects; when two
	// or more already shared it only the new one starts (the others are not touched).
	if len(peers) == 1 {
		params["restarts"] = peers[0].profile
	}
	out := []*adminv1.StatusReason{{Code: "tcp_port_shared", Params: params}}
	for _, r := range share.Routes {
		if !r.Site {
			out = append(out, &adminv1.StatusReason{Code: "share_two_faces", Params: map[string]string{
				"node": node, "port": port, "profile": names[r.ID], "default": names[share.Default]}})
		}
	}
	return out
}
