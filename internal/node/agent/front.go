package agent

import (
	"cmp"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/trusttunnel"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The TCP ports services share by SNI. Where plugin.TCPSharePlan says a port has two services or more, the agent binds it
// itself (internal/node/front) and every service of the port accepts from its route of the front (engine.Env.SharedListener);
// anywhere else the engines listen as they always did. reconcile moves an inbound between the two by removing it and applying
// it again, in this order, on every transition (the port is the one thing that cannot be held twice):
//   - alone -> shared: the engines that listened alone are removed (they free the port), the front binds it, then the engines
//     apply and accept from their routes.
//   - shared -> alone: the engines behind the front are removed, the front closes (it frees the port), then the engine applies
//     and binds the port itself.
//   - shared -> shared (a service comes, goes, or changes): only that service is removed or applied; the front gets the new
//     routes first and keeps the queues of the others, so their connections and engines are not touched.
//   - the port is taken by another process: the front cannot bind, every service of the port fails with the bind error, and
//     the next sweep binds again; once the port is free the front comes up and the failed services apply behind it.

// BindIP is nil in production (all interfaces). Tests bind the fronts to loopback.
var BindIP net.IP

// placeOff is the place of an inbound that must not run: it asked for a name another service of its port holds.
const placeOff = 1 << 16

// sharedPort is a TCP port several services share.
type sharedPort struct {
	f   *front.Front // nil while the port cannot be bound
	def string       // the inbound that gets what no route claims
	err string       // why f is nil
}

// planShares says where each inbound of next runs for this reconcile: a.place is the shared port it sits behind, placeOff, or
// absent for alone; a.noRun says why an inbound must not run at all. It returns the ports that get a front.
func (a *Agent) planShares(next *model) map[uint16]plugin.TCPPortShare {
	var specs []plugin.InboundSpec
	for _, id := range next.ids() {
		if in := next.inbounds[id]; in.parseErr == "" && in.applyErr == "" && in.spec.Enabled { // as tcpPortClaims
			specs = append(specs, in.spec)
		}
	}
	fronts := map[uint16]plugin.TCPPortShare{}
	place, noRun := map[string]uint32{}, map[string]string{}
	for port, p := range plugin.TCPSharePlan(specs) {
		if p.Front() {
			fronts[port] = p
			for _, r := range p.Routes {
				place[r.ID] = uint32(port)
			}
		}
		for id, s := range p.Shadowed {
			place[id], noRun[id] = placeOff, fmt.Sprintf("sni_shadowed: the name %s is served by inbound %s", s.Name, s.By)
			if a.noRun[id] != noRun[id] {
				a.log.Warn("inbound has no name of its own on a shared port", "code", "sni_shadowed", "inbound", id, "port", port, "name", s.Name, "holder", s.By)
			}
		}
	}
	a.place, a.noRun = place, noRun
	return fronts
}

// syncFronts closes the fronts of the ports that are not shared any more and opens or updates the others. It runs after the
// engines that change place are removed and before the others apply. A port that cannot be bound fails its services.
func (a *Agent) syncFronts(fronts map[uint16]plugin.TCPPortShare) {
	a.frontMu.Lock()
	defer a.frontMu.Unlock()
	if a.shared == nil {
		a.shared = map[uint16]*sharedPort{}
	}
	for port, sp := range a.shared {
		if _, ok := fronts[port]; ok {
			continue
		}
		if sp.f != nil {
			_ = sp.f.Close()
			a.log.Info("shared port closed", "port", port)
		}
		delete(a.shared, port)
	}
	for port, p := range fronts {
		sp := a.shared[port]
		if sp == nil {
			sp = &sharedPort{}
			a.shared[port] = sp
		}
		sp.def = p.Default
		if sp.f == nil {
			a.openFront(port, sp, len(p.Routes))
		}
		if sp.f == nil {
			for _, r := range p.Routes {
				a.noRun[r.ID] = sp.err
			}
			continue
		}
		routes := make([]front.Route, len(p.Routes))
		for i, r := range p.Routes {
			routes[i] = frontRoute(r)
		}
		sp.f.SetRoutes(routes, p.Default)
	}
}

// frontRoute is a route with the TCP keepalive its service sets on the sockets of a listener it binds alone. The front's own
// listener sets Go's default, which MTProto and the TLS front of VLESS keep.
func frontRoute(r plugin.TCPRoute) front.Route {
	out := front.Route{ID: r.ID, Names: r.Names}
	switch {
	case r.Protocol == "trusttunnel":
		ka := trusttunnel.KeepAlive
		out.KeepAlive = &ka
	case r.Reality: // Xray listens with no keepalive (transport/internet/system_listener.go)
		out.KeepAlive = &net.KeepAliveConfig{Enable: false}
	}
	return out
}

func (a *Agent) openFront(port uint16, sp *sharedPort, routes int) {
	host := ""
	if BindIP != nil {
		host = BindIP.String()
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		if sp.err != err.Error() {
			a.log.Warn("shared port cannot be bound", "code", "front_bind_failed", "port", port, "err", err)
		}
		sp.err = err.Error()
		return
	}
	sp.err = ""
	sp.f = front.New(ln, a.log.With("source", "front", "port", port))
	a.log.Info("shared port opened", "port", port, "routes", routes)
}

// sharedListener is engine.Env.SharedListener: the route of an inbound on a shared port, nil on any other port.
func (a *Agent) sharedListener(id string, port uint16) net.Listener {
	a.frontMu.Lock()
	defer a.frontMu.Unlock()
	if sp := a.shared[port]; sp != nil && sp.f != nil {
		return sp.f.Listener(id)
	}
	return nil
}

func (a *Agent) closeFronts() {
	a.frontMu.Lock()
	defer a.frontMu.Unlock()
	for port, sp := range a.shared {
		if sp.f != nil {
			_ = sp.f.Close()
		}
		delete(a.shared, port)
	}
}

// sharedPortStats is StatsBatch.shared_ports: what each front counted since the agent started.
func (a *Agent) sharedPortStats() []*pb.SharedPort {
	a.frontMu.Lock()
	defer a.frontMu.Unlock()
	var out []*pb.SharedPort
	for port, sp := range a.shared {
		row := &pb.SharedPort{Port: uint32(port), DefaultInboundId: sp.def, BindError: sp.err}
		if sp.f != nil {
			s := sp.f.Stats()
			row.Accepted, row.Timeouts, row.Evicted, row.Refused, row.NoRoute, row.Panics = s.Accepted, s.Timeouts, s.Evicted, s.Refused, s.NoRoute, s.Panics
			row.DefaultNoSni, row.DefaultUnknownName, row.DefaultNotTls, row.DefaultBadHello, row.DefaultHelloTooLarge =
				s.NoSNI, s.UnknownName, s.NotTLS, s.BadHello, s.HelloTooLarge
			for id, n := range s.Delivered {
				row.Routes = append(row.Routes, &pb.SharedRoute{InboundId: id, Delivered: n})
			}
			slices.SortFunc(row.Routes, func(x, y *pb.SharedRoute) int { return strings.Compare(x.InboundId, y.InboundId) })
			for _, u := range s.Unknown[:min(len(s.Unknown), 5)] {
				row.UnknownNames = append(row.UnknownNames, &pb.SharedUnknownName{Name: u.Name, Count: u.Count})
			}
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(x, y *pb.SharedPort) int { return cmp.Compare(x.Port, y.Port) })
	return out
}
