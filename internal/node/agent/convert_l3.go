package agent

import (
	"fmt"
	"net/netip"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/warp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Wire conversion of the L3 additions (agent.proto "AWG AND WARP"): the tunnel of an awg inbound, the node-level
// WarpSpec, and the health messages that go the other way.

// tunnelFromPB parses InboundSpec.tunnel. The addresses are interface addresses ("10.66.4.1/22"), not masked
// prefixes: the engine derives the client subnet from them.
func tunnelFromPB(t *pb.Tunnel) (plugin.Tunnel, error) {
	var out plugin.Tunnel
	if t.AddrV4 != "" {
		p, err := netip.ParsePrefix(t.AddrV4)
		if err != nil {
			return out, fmt.Errorf("tunnel addr_v4: %v", err)
		}
		out.AddrV4 = p
	}
	if t.AddrV6 != "" {
		p, err := netip.ParsePrefix(t.AddrV6)
		if err != nil {
			return out, fmt.Errorf("tunnel addr_v6: %v", err)
		}
		out.AddrV6 = p
	}
	if t.Mtu > 65535 {
		return out, fmt.Errorf("tunnel mtu %d", t.Mtu)
	}
	out.MTU = uint16(t.Mtu)
	return out, nil
}

func tunnelToPB(t plugin.Tunnel) *pb.Tunnel {
	out := &pb.Tunnel{Mtu: uint32(t.MTU)}
	if t.AddrV4.IsValid() {
		out.AddrV4 = t.AddrV4.String()
	}
	if t.AddrV6.IsValid() {
		out.AddrV6 = t.AddrV6.String()
	}
	return out
}

// warpFromPB validates the bounds the manager would otherwise have to guess (numbers that do not fit, reserved bytes
// of the wrong length). Everything else (keys, addresses, the endpoint literal) is the manager's to parse: a spec it
// refuses is reported by it, it does not make the whole desired state REJECTED.
func warpFromPB(w *pb.WarpSpec) (*plugin.WarpSpec, error) {
	if w.Mtu > 65535 {
		return nil, rejectf("warp: mtu %d", w.Mtu)
	}
	if n := len(w.Reserved); n != 0 && n != 3 {
		return nil, rejectf("warp: reserved must be 0 or 3 bytes, not %d", n)
	}
	out := &plugin.WarpSpec{
		Enabled: w.Enabled, PrivateKey: w.PrivateKey, PeerPublicKey: w.PeerPublicKey,
		EndpointV4: w.EndpointV4, EndpointV6: w.EndpointV6, AddressV4: w.AddressV4, AddressV6: w.AddressV6,
		MTU: uint16(w.Mtu), Reserved: append([]byte(nil), w.Reserved...), Backend: w.Backend,
	}
	for _, p := range w.Ports {
		if p == 0 || p > 65535 {
			return nil, rejectf("warp: port %d", p)
		}
		out.Ports = append(out.Ports, uint16(p))
	}
	return out, nil
}

func warpToPB(w *plugin.WarpSpec) *pb.WarpSpec {
	if w == nil {
		return nil
	}
	out := &pb.WarpSpec{
		Enabled: w.Enabled, PrivateKey: w.PrivateKey, PeerPublicKey: w.PeerPublicKey,
		EndpointV4: w.EndpointV4, EndpointV6: w.EndpointV6, AddressV4: w.AddressV4, AddressV6: w.AddressV6,
		Mtu: uint32(w.MTU), Reserved: append([]byte(nil), w.Reserved...), Backend: w.Backend,
	}
	for _, p := range w.Ports {
		out.Ports = append(out.Ports, uint32(p))
	}
	return out
}

func warpStateToPB(s warp.State) pb.WarpState {
	switch s {
	case warp.StateUnavailable:
		return pb.WarpState_WARP_STATE_UNAVAILABLE
	case warp.StateStarting:
		return pb.WarpState_WARP_STATE_STARTING
	case warp.StateUp:
		return pb.WarpState_WARP_STATE_UP
	case warp.StateDown:
		return pb.WarpState_WARP_STATE_DOWN
	case warp.StateDisabled:
		return pb.WarpState_WARP_STATE_DISABLED
	}
	return pb.WarpState_WARP_STATE_UNSPECIFIED
}

// warpHealthToPB never carries a key or an address of the account: Health has none. off is the panel clock minus the local
// clock, like every other timestamp the agent reports.
func warpHealthToPB(h warp.Health, off time.Duration) *pb.WarpHealth {
	out := &pb.WarpHealth{
		State: warpStateToPB(h.State), Backend: h.Backend, Endpoint: h.Endpoint, WarpFlag: h.WarpFlag, Colo: h.Colo,
		ProbeCloudflareOk: h.ProbeCloudflareOK, ProbeOtherOk: h.ProbeOtherOK, ConsecutiveFailures: h.Failures,
		RxBytes: h.RxBytes, TxBytes: h.TxBytes, LastError: h.LastError,
	}
	if !h.LastHandshake.IsZero() {
		out.LastHandshakeUnix = h.LastHandshake.Add(off).Unix()
	}
	if !h.CheckedAt.IsZero() {
		out.CheckedUnix = h.CheckedAt.Add(off).Unix()
	}
	out.ProbeCloudflare, out.ProbeOther = probeToPB(h.ProbeCloudflare, off), probeToPB(h.ProbeOther, off)
	return out
}

func probeToPB(p *warp.ProbeResult, off time.Duration) *pb.WarpProbeResult {
	if p == nil {
		return nil
	}
	return &pb.WarpProbeResult{Ok: p.OK, LatencyMs: uint32(p.Latency.Milliseconds()), AtUnix: p.At.Add(off).Unix()}
}
