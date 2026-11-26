package agent

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/relayfwd"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The relay forwards of the agent (agent.proto "RELAY FORWARDS"): the list in the desired state is resolved, checked
// against what the node uses, handed to the host as one nft table and reported back per forward. Like the rest of the
// reconcile it runs on the worker goroutine.

const (
	// relayResolveEvery is how long a resolved address is trusted; the sweep (every 30 s) re-resolves after it.
	relayResolveEvery = 5 * time.Minute
	// relayRetryEvery is how soon a name that failed to resolve is tried again: a dead resolver must not slow every apply.
	relayRetryEvery = time.Minute
	// relayResolveBudget bounds all the lookups of one sync, so a node that boots without DNS still restores its inbounds.
	relayResolveBudget = 20 * time.Second
)

// relayRuntime is the worker-owned state of the forwards between reconciles.
type relayRuntime struct {
	results       []*pb.RelayResult // of the last sync, by relay id; never mutated once stored
	claims        map[uint16]string // TCP ports of the forwards installed now -> "relay:<id>" (decoy sites give these up)
	listeners     map[string]bridge.RelayListener
	pending       []bridge.RelayListener // reverse listeners waiting for host INPUT rules
	listenerPorts map[uint16]struct{}    // reverse TCP ports that need host INPUT firewall access
	fwdTCP        map[uint16]struct{}    // userspace forward ports (the other family) that need host INPUT access
	fwdUDP        map[uint16]struct{}
	hosts         map[string]relayLookup
	logged        map[string]string // relay id -> the last failure logged, so one is logged once

	// Test seams; nil = the real thing.
	lookup func(ctx context.Context, host string) (netip.Addr, error)
	self   func(netip.Addr) bool
	clock  func() time.Time
}

// relayLookup is what is known about one to_host: the last address that worked, when, and the last failure.
type relayLookup struct {
	ip       netip.Addr
	at       time.Time
	failedAt time.Time
	err      string
}

func (r *relayRuntime) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

// resolve returns the address of host (A record only). A name is looked up again after relayResolveEvery; when that fails
// the last address that worked is kept, and a name that never resolved is retried after relayRetryEvery.
func (a *Agent) resolveRelay(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	rt := &a.relay
	e, now := rt.hosts[host], rt.now()
	if e.ip.IsValid() && now.Sub(e.at) < relayResolveEvery {
		return e.ip, nil
	}
	if !e.failedAt.IsZero() && now.Sub(e.failedAt) < relayRetryEvery {
		if e.ip.IsValid() {
			return e.ip, nil
		}
		return netip.Addr{}, errors.New(e.err)
	}
	lookup := rt.lookup
	if lookup == nil {
		lookup = a.lookupIPv4
	}
	ip, err := lookup(ctx, host)
	if err != nil {
		e.failedAt, e.err = now, shortMsg(err)
		rt.hosts[host] = e
		if e.ip.IsValid() { // keep the last good address
			return e.ip, nil
		}
		return netip.Addr{}, err
	}
	rt.hosts[host] = relayLookup{ip: ip, at: now}
	return ip, nil
}

// lookupIPv4 resolves with the node's resolvers (NodeSettings.dns_resolvers, the system's when none) and gives up when ctx ends.
func (a *Agent) lookupIPv4(ctx context.Context, host string) (netip.Addr, error) {
	type answer struct {
		ip  netip.Addr
		err error
	}
	servers := a.DNS()
	ch := make(chan answer, 1)
	go func() {
		ip, err := egress.LookupIPv4(servers, host)
		ch <- answer{ip, err}
	}()
	select {
	case r := <-ch:
		return r.ip, r.err
	case <-ctx.Done():
		return netip.Addr{}, errors.New("lookup timed out")
	}
}

// relayGuard is what the node uses: the sshd ports, the listeners and port-hop ranges of all its inbounds, its own addresses.
func (a *Agent) relayGuard(next *model) hostctl.ForwardGuard {
	g := hostctl.ForwardGuard{SSHPorts: a.host.SSHPorts(), IsSelf: a.relay.self}
	if g.IsSelf == nil {
		g.IsSelf = hostctl.IsOwnAddr
	}
	for _, id := range next.ids() {
		l := next.inbounds[id].spec.Listen
		g.Ports = append(g.Ports, hostctl.NetPort{Network: l.Network, Port: l.Port})
		if l.HopFrom != 0 {
			g.Hops = append(g.Hops, hostctl.Hop{InboundID: id, Network: l.Network, From: l.HopFrom, To: l.HopTo, Port: l.Port})
		}
	}
	if next.bridgeServe != nil && next.bridgeServe.Port != 0 {
		g.Ports = append(g.Ports, hostctl.NetPort{Network: "tcp", Port: next.bridgeServe.Port})
	}
	return g
}

// reverseRelayTCPClaims releases shared HTTPS masquerade ports before reverse listeners bind them.
func (a *Agent) reverseRelayTCPClaims(next *model) map[uint16]string {
	guard := a.relayGuard(next)
	claims := make(map[uint16]string)
	var accepted []hostctl.Forward
	for _, relay := range next.relays {
		if relay.Mode != "reverse" || relay.Network != "tcp" {
			continue
		}
		if err := hostctl.ValidateForwardPort(relay.ID, relay.Network, relay.Port, accepted, guard); err != nil {
			continue
		}
		accepted = append(accepted, hostctl.Forward{ID: relay.ID, Network: relay.Network, Port: relay.Port, ToPort: 1})
		claims[relay.Port] = "relay:" + relay.ID
	}
	return claims
}

// syncRelays makes the host forward exactly what next lists and can be forwarded safely, and records the outcome per
// forward in a.relay.results. It runs after the tunnels and before the engines, on every reconcile; the host does nothing
// for a set it already runs. Anything that cannot be installed is a failed forward, never a half-installed one: a refused
// forward, one whose name does not resolve, a FORWARD chain that drops, a kernel that refuses.
func (a *Agent) syncRelays(ctx context.Context, next *model) {
	rt := &a.relay
	rh, ok := a.host.(hostctl.RelayHost)
	hostSupported := ok && rh.RelaySupported()

	// Names that no forward uses any more are forgotten.
	if rt.hosts == nil {
		rt.hosts = map[string]relayLookup{}
	}
	for host := range rt.hosts {
		if !relaysUse(next.relays, host) {
			delete(rt.hosts, host)
		}
	}
	lctx, cancel := context.WithTimeout(ctx, relayResolveBudget)
	defer cancel()

	guard := a.relayGuard(next)
	var accepted []hostctl.Forward
	var acceptedPorts []hostctl.Forward
	var userspace []relayfwd.Forward
	reverse := make(map[string]bridge.RelayListener)
	results := make(map[string]*pb.RelayResult, len(next.relays))
	for _, f := range next.relays { // sorted by id: on a clash the earlier id wins
		r := &pb.RelayResult{RelayId: f.ID}
		results[f.ID] = r
		if f.Mode == "reverse" {
			if f.Network != "tcp" || f.ExitNodeID == "" {
				r.Error = "bad_reverse_config"
				continue
			}
			if err := hostctl.ValidateForwardPort(f.ID, f.Network, f.Port, acceptedPorts, guard); err != nil {
				r.Error = "refused: " + strings.TrimPrefix(err.Error(), "forward "+f.ID+": ")
				continue
			}
			acceptedPorts = append(acceptedPorts, hostctl.Forward{ID: f.ID, Network: f.Network, Port: f.Port, ToPort: 1})
			if a.bridgeServeError != "" || !a.bridgeServesExit(next.bridgeServe, f.ExitNodeID) {
				r.Error = "bridge_unavailable"
				continue
			}
			reverse[f.ID] = bridge.RelayListener{RelayID: f.ID, ExitNodeID: f.ExitNodeID, Port: f.Port}
			continue // Reverse forwards never resolve or dial the panel's to_host/to_port.
		}
		if f.ToFamily == "6" {
			// The exit is on the other address family: the kernel cannot rewrite a packet across families, so the agent carries
			// this port in userspace. The panel sends a literal it has proved; no lookup happens here.
			fw, err := userspaceForward(f, guard)
			if err != nil {
				r.Error = err.Error()
				continue
			}
			if err := hostctl.ValidateForwardPort(f.ID, f.Network, f.Port, acceptedPorts, guard); err != nil {
				r.Error = "refused: " + strings.TrimPrefix(err.Error(), "forward "+f.ID+": ")
				continue
			}
			acceptedPorts = append(acceptedPorts, hostctl.Forward{ID: f.ID, Network: f.Network, Port: f.Port, ToPort: 1})
			userspace = append(userspace, fw)
			continue
		}
		if !hostSupported {
			r.Error = "unsupported_host"
			continue
		}
		ip, err := a.resolveRelay(lctx, f.ToHost)
		if err != nil {
			r.Error = "resolve: " + shortMsg(err)
			continue
		}
		r.ToIp = ip.String()
		fw := hostctl.Forward{ID: f.ID, Network: f.Network, Port: f.Port, ToIP: ip, ToPort: f.ToPort}
		if err := hostctl.ValidateForward(fw, acceptedPorts, guard); err != nil {
			r.Error = "refused: " + strings.TrimPrefix(err.Error(), "forward "+f.ID+": ")
			continue
		}
		acceptedPorts = append(acceptedPorts, fw)
		accepted = append(accepted, fw)
	}

	// Keep unchanged reverse listeners while the host table is updated. A reverse-to-DNAT switch installs
	// DNAT first; a DNAT-to-reverse switch starts its listener later, after INPUT rules are synchronized.
	keep := make(map[string]bridge.RelayListener)
	for id, old := range rt.listeners {
		if want, ok := reverse[id]; ok && want == old {
			keep[id] = old
		}
	}
	// The host gets the forwards that passed; when it cannot install them, all of those fail together.
	failure := ""
	if hostSupported {
		if err := rh.SetRelays(ctx, accepted); err != nil {
			failure = shortMsg(err)
			if errors.Is(err, hostctl.ErrForwardDrop) {
				failure = "forward_drop"
				a.log.Warn("relay forwards not installed: a FORWARD chain drops forwarded packets", "code", "relay_forward_drop", "detail", shortMsg(err))
			}
		}
	}
	listenerErrors := map[string]error{}
	activeListeners := keep
	if a.bridgeEntry != nil && (failure == "" || !hostSupported) {
		keepList := make([]bridge.RelayListener, 0, len(keep))
		for _, l := range keep {
			keepList = append(keepList, l)
		}
		a.bridgeEntry.SyncListeners(keepList)
	}
	rt.claims, rt.listenerPorts, rt.pending, rt.fwdTCP, rt.fwdUDP = nil, nil, nil, nil, nil
	rt.listeners = activeListeners
	for _, fw := range accepted {
		r := results[fw.ID]
		if failure != "" {
			r.Error = failure
			continue
		}
		r.Ok = true
		if fw.Network == "tcp" {
			if rt.claims == nil {
				rt.claims = map[uint16]string{}
			}
			rt.claims[fw.Port] = "relay:" + fw.ID
		}
	}
	// The userspace forwards are made exactly the list that passed; one that fails does not take the kernel ones with it.
	fwdErrs := map[string]error{}
	if a.relayFwd != nil {
		fwdErrs = a.relayFwd.Sync(userspace)
	} else {
		for _, u := range userspace {
			fwdErrs[u.ID] = errors.New("unsupported_host")
		}
	}
	for _, u := range userspace {
		r := results[u.ID]
		if err := fwdErrs[u.ID]; err != nil {
			r.Error = err.Error()
			continue
		}
		r.Ok, r.ToIp = true, u.To.Addr().String()
		if u.Network == "tcp" {
			if rt.claims == nil {
				rt.claims = map[uint16]string{}
			}
			rt.claims[u.Port] = "relay:" + u.ID
			if rt.fwdTCP == nil {
				rt.fwdTCP = map[uint16]struct{}{}
			}
			rt.fwdTCP[u.Port] = struct{}{}
			continue
		}
		if rt.fwdUDP == nil {
			rt.fwdUDP = map[uint16]struct{}{}
		}
		rt.fwdUDP[u.Port] = struct{}{}
	}
	for id, l := range activeListeners {
		if r := results[id]; r != nil {
			r.Ok = true
		}
		if rt.claims == nil {
			rt.claims = map[uint16]string{}
		}
		rt.claims[l.Port] = "relay:" + id
		if rt.listenerPorts == nil {
			rt.listenerPorts = map[uint16]struct{}{}
		}
		rt.listenerPorts[l.Port] = struct{}{}
	}
	for id, l := range reverse {
		if _, alreadyRunning := keep[id]; alreadyRunning {
			continue
		}
		if failure != "" && hostSupported {
			listenerErrors[id] = errors.New("relay_table")
			continue
		}
		if a.bridgeEntry == nil {
			listenerErrors[id] = errors.New("listener_failed")
			continue
		}
		rt.pending = append(rt.pending, l)
		if rt.listenerPorts == nil {
			rt.listenerPorts = map[uint16]struct{}{}
		}
		rt.listenerPorts[l.Port] = struct{}{}
		if rt.claims == nil {
			rt.claims = map[uint16]string{}
		}
		rt.claims[l.Port] = "relay:" + id
	}
	for id := range reverse {
		if r := results[id]; r != nil && !r.Ok && r.Error == "" {
			if err := listenerErrors[id]; err != nil {
				r.Error = err.Error()
			}
		}
	}

	out := make([]*pb.RelayResult, 0, len(results))
	for _, f := range next.relays {
		out = append(out, results[f.ID])
	}
	rt.results = out
	a.logRelayFailures(out)
}

// startReverseRelays runs after INPUT firewall rules have been synchronized.
func (a *Agent) startReverseRelays(firewallReady bool) {
	rt := &a.relay
	if len(rt.pending) == 0 {
		return
	}
	if !firewallReady || a.bridgeEntry == nil {
		for _, listener := range rt.pending {
			for _, result := range rt.results {
				if result.RelayId == listener.RelayID {
					result.Ok = false
					result.Error = "relay_table"
				}
			}
		}
		a.logRelayFailures(rt.results)
		return
	}
	listeners := make([]bridge.RelayListener, 0, len(rt.listeners)+len(rt.pending))
	for _, listener := range rt.listeners {
		listeners = append(listeners, listener)
	}
	listeners = append(listeners, rt.pending...)
	errs := a.bridgeEntry.SyncListeners(listeners)
	for _, listener := range rt.pending {
		if err := errs[listener.RelayID]; err != nil {
			for _, result := range rt.results {
				if result.RelayId == listener.RelayID {
					result.Ok = false
					result.Error = "listener_failed"
				}
			}
			delete(rt.listeners, listener.RelayID)
			delete(rt.listenerPorts, listener.Port)
			delete(rt.claims, listener.Port)
			continue
		}
		rt.listeners[listener.RelayID] = listener
		for _, result := range rt.results {
			if result.RelayId == listener.RelayID {
				result.Ok = true
				result.Error = ""
				break
			}
		}
	}
	rt.pending = nil
	a.logRelayFailures(rt.results)
}

// logRelayFailures logs each distinct failure of a forward once (the sweep repeats the sync every 30 s).
func (a *Agent) logRelayFailures(results []*pb.RelayResult) {
	rt := &a.relay
	if rt.logged == nil {
		rt.logged = map[string]string{}
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.RelayId] = true
		switch {
		case r.Error == "":
			delete(rt.logged, r.RelayId)
		case rt.logged[r.RelayId] != r.Error:
			rt.logged[r.RelayId] = r.Error
			a.log.Warn("relay forward failed", "code", "relay_forward_failed", "relay", r.RelayId, "error", r.Error)
		}
	}
	for id := range rt.logged {
		if !seen[id] {
			delete(rt.logged, id)
		}
	}
}

// userspaceForward is the userspace side of a forward whose exit is on the other address family. to_host is a literal the panel
// proved: a name would resolve to whichever family the resolver likes, which is exactly what must not happen here.
func userspaceForward(f plugin.RelayForward, guard hostctl.ForwardGuard) (relayfwd.Forward, error) {
	ip, err := netip.ParseAddr(f.ToHost)
	if err != nil {
		return relayfwd.Forward{}, errors.New("refused: not an address")
	}
	ip = ip.Unmap()
	if !hostctl.PublicIPv6(ip) {
		return relayfwd.Forward{}, errors.New("refused: not a public IPv6 address")
	}
	if guard.IsSelf != nil && guard.IsSelf(ip) {
		return relayfwd.Forward{}, errors.New("refused: an address of this node")
	}
	if f.ToPort == 0 {
		return relayfwd.Forward{}, errors.New("refused: no target port")
	}
	return relayfwd.Forward{ID: f.ID, Network: f.Network, Port: f.Port, To: netip.AddrPortFrom(ip, f.ToPort)}, nil
}

func relaysUse(relays []plugin.RelayForward, host string) bool {
	for _, f := range relays {
		if f.ToHost == host {
			return true
		}
	}
	return false
}

// withRelayClaims adds the TCP ports of the forwards installed now to the claims of the inbounds (apply.go tcpPortClaims),
// under the id "relay:<relay id>". An inbound's port never clashes with a forward's: the forward would have been refused.
func withRelayClaims(claims, relay map[uint16]string) map[uint16]string {
	for port, id := range relay {
		if _, taken := claims[port]; !taken {
			claims[port] = id
		}
	}
	return claims
}

// relaysFromPB turns the wire list into the model's sorted list and skips entries that cannot be represented.
func relaysFromPB(l *pb.RelayForwards) ([]plugin.RelayForward, []error) {
	out := make([]plugin.RelayForward, 0, len(l.Items))
	seen := map[string]bool{}
	var rejected []error
	for _, f := range l.Items {
		if f == nil || f.RelayId == "" {
			rejected = append(rejected, rejectf("relay without id"))
			continue
		}
		if seen[f.RelayId] {
			rejected = append(rejected, rejectf("relay %s listed twice", f.RelayId))
			continue
		}
		if f.Port > 65535 || f.ToPort > 65535 {
			rejected = append(rejected, rejectf("relay %s: port out of range", f.RelayId))
			continue
		}
		mode := f.Mode
		if mode == "dnat" {
			mode = ""
		}
		family := f.ToFamily
		if family == "4" {
			family = ""
		}
		if (mode != "" && mode != "reverse") || (mode == "reverse" && (f.Network != "tcp" || f.ExitNodeId == "" || !bridgeNodeIDPattern.MatchString(f.ExitNodeId))) ||
			(mode == "" && f.ExitNodeId != "") || (family != "" && family != "6") || (family == "6" && (mode == "reverse" || f.Network == "")) {
			rejected = append(rejected, rejectf("relay %s: bad mode", f.RelayId))
			continue
		}
		seen[f.RelayId] = true
		out = append(out, plugin.RelayForward{ID: f.RelayId, Network: f.Network, Port: uint16(f.Port), ToHost: f.ToHost, ToPort: uint16(f.ToPort),
			ToFamily: family, Mode: mode, ExitNodeID: f.ExitNodeId})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, rejected
}

// relaysToPB is the persisted form: nil when there are no forwards, so a state without any is what it was before them.
func relaysToPB(rs []plugin.RelayForward) *pb.RelayForwards {
	if len(rs) == 0 {
		return nil
	}
	l := &pb.RelayForwards{Items: make([]*pb.RelayForward, len(rs))}
	for i, f := range rs {
		mode := f.Mode
		if mode == "dnat" {
			mode = ""
		}
		l.Items[i] = &pb.RelayForward{RelayId: f.ID, Network: f.Network, Port: uint32(f.Port), ToHost: f.ToHost, ToPort: uint32(f.ToPort),
			ToFamily: f.ToFamily, Mode: mode, ExitNodeId: f.ExitNodeID}
	}
	return l
}
