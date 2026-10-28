package agent

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/hostctl"
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
	results []*pb.RelayResult // of the last sync, by relay id; never mutated once stored
	claims  map[uint16]string // TCP ports of the forwards installed now -> "relay:<id>" (decoy sites give these up)
	hosts   map[string]relayLookup
	logged  map[string]string // relay id -> the last failure logged, so one is logged once

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
	return g
}

// syncRelays makes the host forward exactly what next lists and can be forwarded safely, and records the outcome per
// forward in a.relay.results. It runs after the tunnels and before the engines, on every reconcile; the host does nothing
// for a set it already runs. Anything that cannot be installed is a failed forward, never a half-installed one: a refused
// forward, one whose name does not resolve, a FORWARD chain that drops, a kernel that refuses.
func (a *Agent) syncRelays(ctx context.Context, next *model) {
	rt := &a.relay
	rh, ok := a.host.(hostctl.RelayHost)
	if !ok || !rh.RelaySupported() {
		rt.results, rt.claims = nil, nil
		for _, f := range next.relays {
			rt.results = append(rt.results, &pb.RelayResult{RelayId: f.ID, Error: "unsupported_host"})
		}
		if len(next.relays) > 0 {
			a.logRelayFailures(rt.results)
		}
		return
	}

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
	results := make(map[string]*pb.RelayResult, len(next.relays))
	for _, f := range next.relays { // sorted by id: on a clash the earlier id wins
		r := &pb.RelayResult{RelayId: f.ID}
		results[f.ID] = r
		ip, err := a.resolveRelay(lctx, f.ToHost)
		if err != nil {
			r.Error = "resolve: " + shortMsg(err)
			continue
		}
		r.ToIp = ip.String()
		fw := hostctl.Forward{ID: f.ID, Network: f.Network, Port: f.Port, ToIP: ip, ToPort: f.ToPort}
		if err := hostctl.ValidateForward(fw, accepted, guard); err != nil {
			r.Error = "refused: " + strings.TrimPrefix(err.Error(), "forward "+f.ID+": ")
			continue
		}
		accepted = append(accepted, fw)
	}

	// The host gets the forwards that passed; when it cannot install them, all of those fail together.
	failure := ""
	if err := rh.SetRelays(ctx, accepted); err != nil {
		failure = shortMsg(err)
		if errors.Is(err, hostctl.ErrForwardDrop) {
			failure = "forward_drop"
			a.log.Warn("relay forwards not installed: a FORWARD chain drops forwarded packets", "detail", shortMsg(err))
		}
	}
	rt.claims = nil
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

	out := make([]*pb.RelayResult, 0, len(results))
	for _, f := range next.relays {
		out = append(out, results[f.ID])
	}
	rt.results = out
	a.logRelayFailures(out)
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
			a.log.Warn("relay forward failed", "relay", r.RelayId, "error", r.Error)
		}
	}
	for id := range rt.logged {
		if !seen[id] {
			delete(rt.logged, id)
		}
	}
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

// relaysFromPB turns the wire list into the model's: sorted by id. A list that cannot be represented (an empty or repeated
// id, a port beyond 65535) rejects the whole message, like a malformed inbound; whether a forward may be installed is
// decided later, per forward.
func relaysFromPB(l *pb.RelayForwards) ([]plugin.RelayForward, error) {
	out := make([]plugin.RelayForward, 0, len(l.Items))
	seen := map[string]bool{}
	for _, f := range l.Items {
		switch {
		case f == nil || f.RelayId == "":
			return nil, rejectf("relay without id")
		case seen[f.RelayId]:
			return nil, rejectf("relay %s listed twice", f.RelayId)
		case f.Port > 65535 || f.ToPort > 65535:
			return nil, rejectf("relay %s: port out of range", f.RelayId)
		}
		seen[f.RelayId] = true
		out = append(out, plugin.RelayForward{ID: f.RelayId, Network: f.Network, Port: uint16(f.Port), ToHost: f.ToHost, ToPort: uint16(f.ToPort)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// relaysToPB is the persisted form: nil when there are no forwards, so a state without any is what it was before them.
func relaysToPB(rs []plugin.RelayForward) *pb.RelayForwards {
	if len(rs) == 0 {
		return nil
	}
	l := &pb.RelayForwards{Items: make([]*pb.RelayForward, len(rs))}
	for i, f := range rs {
		l.Items[i] = &pb.RelayForward{RelayId: f.ID, Network: f.Network, Port: uint32(f.Port), ToHost: f.ToHost, ToPort: uint32(f.ToPort)}
	}
	return l
}
