//go:build !js

package health

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"

	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
)

// dialAWG is the AmneziaWG client of the checker: amneziawg-go inside the panel process on a netstack TUN (a userspace
// TCP/IP stack: no interface, no root, nothing on the host's routing table), configured from the same clientConfig a real
// device's .conf is written from (awg.ClientDevice) with the probe peer's own key pair and address. One device per
// round, closed with the tunnel: like a client that connects, fetches and disconnects, so every round also tests the
// handshake and what the node does with a new session.
//
// A node never answers a peer it does not know or a packet with the wrong obfuscation, so a missing handshake cannot be
// told apart from a blocked UDP port: both are "timeout" (hysteria2 can tell a refused credential, AWG cannot).
func dialAWG(ctx context.Context, t Target, o DialOptions) (Tunnel, error) {
	ip, err := nodeIP(ctx, t.Node.Address)
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot resolve the node address"}
	}
	// The probe is a device like any other of the profile: the same id-driven signature (when the profile has per-device
	// ones) comes out of the same code. DNS is nil: the pair a config of this profile carries without a user's preset.
	ct, err := awg.ClientDevice(protocols.RenderInput{
		Settings: t.Settings,
		Inbound:  protocols.InboundView{ID: t.Inbound.ID, Port: t.Spec.Listen.Port},
		DeviceID: t.CredID, Secret: t.Secret, Peer: json.RawMessage(t.Secret),
		InboundPublic: json.RawMessage(t.Inbound.PluginPublic), NodeAddr: ip.String(),
	})
	if err != nil || len(ct.Addrs) == 0 {
		return nil, errClientUnsupported
	}
	// The stack gets the IPv4 address only. A name then resolves to A records and dials over IPv4: a node
	// without IPv6 egress (most of them) would otherwise show as slow or failed on the IPv6 attempt that comes first.
	local := []netip.Addr{}
	for _, a := range ct.Addrs {
		if a.Is4() {
			local = append(local, a)
		}
	}
	if len(local) == 0 {
		return nil, errClientUnsupported
	}
	tdev, tnet, err := netstack.CreateNetTUN(local, ct.DNS, ct.MTU)
	if err != nil {
		return nil, &ProbeError{Code: skipClientMissing, Detail: "cannot create the client stack: " + err.Error(), Skip: true}
	}
	gate := newGatedTun(tdev)
	dev := device.NewDevice(gate, awgBind(), device.NewLogger(device.LogLevelSilent, ""))
	err = dev.IpcSet(ct.IPC)
	gate.release()
	if err != nil {
		dev.Close()
		return nil, &ProbeError{Code: skipClientMissing, Detail: "the client config was refused: " + err.Error(), Skip: true}
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, &ProbeError{Code: skipClientMissing, Detail: "cannot open the client socket: " + err.Error(), Skip: true}
	}
	if p := dev.LookupPeer(device.NoisePublicKey(ct.Server)); p != nil {
		_ = p.SendHandshakeInitiation(false) // the first packet of a client; a keepalive in the profile does the same
	}

	// Wait for the handshake here, so that its absence is named as such and not as a failed first request.
	timer := time.NewTimer(o.HandshakeTimeout)
	defer timer.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for !handshaken(dev, ct.Server) {
		select {
		case <-tick.C:
		case <-timer.C:
			dev.Close()
			return nil, &ProbeError{Code: "timeout", Detail: "no handshake answer after " + strconv.Itoa(int(o.HandshakeTimeout.Seconds())) + "s"}
		case <-ctx.Done():
			dev.Close()
			return nil, &ProbeError{Code: "timeout", Detail: "no handshake answer within the round budget"}
		}
	}
	return awgTunnel{dev, tnet}, nil
}

// handshaken reports whether the peer has completed a handshake with this device.
func handshaken(dev *device.Device, server [32]byte) bool {
	text, err := dev.IpcGet()
	if err != nil {
		return false
	}
	peers, err := awguapi.ParseStats(text)
	if err != nil {
		return false
	}
	for _, p := range peers {
		if p.PublicKey == server && !p.LastHS.IsZero() {
			return true
		}
	}
	return false
}

// gatedTun works around a quirk of amneziawg-go (v3.1.20260828): the reader of the TUN takes the transport padding (S4) once
// per loop, before it blocks in Read, and NewDevice starts it before the config is applied, so the first packet read from the
// TUN leaves without the padding, the peer cannot tell what it is ("unknown type") and drops it. For the checker that is
// the SYN of the first connection: a retransmission, one to two seconds on every round's latency. The first Read waits
// until the config is in and returns no packets, so the reader goes round again and takes the real padding.
type gatedTun struct {
	tun.Device
	once  sync.Once
	ready chan struct{} // closed by release: the config is applied
	done  chan struct{} // closed by Close
}

func newGatedTun(d tun.Device) *gatedTun {
	return &gatedTun{Device: d, ready: make(chan struct{}), done: make(chan struct{})}
}

func (g *gatedTun) release() { close(g.ready) }

func (g *gatedTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	first := false
	g.once.Do(func() { first = true })
	if first {
		select {
		case <-g.ready:
			return 0, nil
		case <-g.done:
			return 0, os.ErrClosed
		}
	}
	return g.Device.Read(bufs, sizes, offset)
}

func (g *gatedTun) Close() error {
	select {
	case <-g.done:
	default:
		close(g.done)
	}
	return g.Device.Close()
}

// awgBind makes the socket side of a client device. Production: the system's UDP sockets on a random port; tests swap in
// an in-memory bind.
var awgBind = func() conn.Bind { return conn.NewDefaultBind() }

type awgTunnel struct {
	dev  *device.Device
	tnet *netstack.Net
}

func (a awgTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return a.tnet.DialContext(ctx, network, addr)
}

func (a awgTunnel) Close() error {
	a.dev.Close() // closes the TUN and the socket with it
	return nil
}
