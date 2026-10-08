//go:build !js

package health

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	panelhy2 "github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

// dialHysteria2 is the Hysteria2 client of the checker: the hysteria core client, pointed at the node's address
// and the inbound's own port (not the hop range, so a blocked hop range is not seen), with the profile's
// obfuscation and the certificate rules of a real client (a self-signed inbound is pinned to the certificate
// the node reported, any other is verified against the server name).
func dialHysteria2(ctx context.Context, t Target, o DialOptions) (Tunnel, error) {
	var set panelhy2.Settings
	if err := json.Unmarshal(t.Settings, &set); err != nil {
		return nil, errClientUnsupported
	}
	switch set.Obfs.Type {
	case "none", "salamander", "gecko":
	default:
		return nil, errClientUnsupported
	}

	ip, err := udpcheck.Resolve(ctx, t.Node.Address)
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot resolve the node address"}
	}
	tlsCfg := client.TLSConfig{ServerName: t.Spec.TLS.ServerName}
	if t.Spec.TLS.Mode == plugin.TLSSelfSigned {
		pin, ok := protocols.NormalizePin(t.Inbound.CertPin)
		if !ok {
			return nil, &ProbeError{Code: skipNotActive, Detail: "certificate pin not reported yet", Skip: true}
		}
		tlsCfg.InsecureSkipVerify = true // the pin replaces the chain check, like pinSHA256 in a subscription
		tlsCfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return fmt.Errorf("certificate missing")
			}
			sum := sha256.Sum256(raw[0])
			if hex.EncodeToString(sum[:]) != pin {
				return fmt.Errorf("certificate pin mismatch")
			}
			return nil
		}
	}
	cfg := &client.Config{
		ServerAddr: &net.UDPAddr{IP: ip.AsSlice(), Port: int(t.Spec.Listen.Port)},
		Auth:       t.Secret,
		TLSConfig:  tlsCfg,
		QUICConfig: client.QUICConfig{MaxIdleTimeout: 10 * time.Second},
	}
	if set.Obfs.Type != "none" {
		cfg.ConnFactory = obfsFactory{set.Obfs.Type, []byte(set.Obfs.Password)}
	} else if probeBindIP != nil {
		cfg.ConnFactory = plainFactory{}
	}

	// NewClient takes no context and waits for the QUIC default handshake timeout; the round must not wait longer
	// than its own budget, so the attempt runs on the side and is closed when it finishes late.
	type outcome struct {
		c   client.Client
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		c, _, err := client.NewClient(cfg)
		done <- outcome{c, err}
	}()
	timer := time.NewTimer(o.HandshakeTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		return hy2Tunnel{r.c}, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	go func() {
		if r := <-done; r.c != nil {
			r.c.Close()
		}
	}()
	return nil, &ProbeError{Code: "timeout", Detail: "handshake timeout after " + strconv.Itoa(int(o.HandshakeTimeout.Seconds())) + "s"}
}

type hy2Tunnel struct{ c client.Client }

func (h hy2Tunnel) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	return h.c.TCP(addr)
}
func (h hy2Tunnel) Close() error { return h.c.Close() }

// obfsFactory gives the client a UDP socket wrapped in the profile's obfuscation.
type obfsFactory struct {
	typ string
	pw  []byte
}

func (f obfsFactory) New(net.Addr) (net.PacketConn, error) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: probeBindIP})
	if err != nil {
		return nil, err
	}
	var wrapped net.PacketConn
	if f.typ == "gecko" {
		wrapped, err = obfs.WrapPacketConnGecko(pc, obfs.GeckoOptions{Password: f.pw})
	} else {
		wrapped, err = obfs.WrapPacketConnSalamander(pc, f.pw)
	}
	if err != nil {
		pc.Close()
		return nil, err
	}
	return wrapped, nil
}

// probeBindIP is the local address of the client socket; nil, the production value, lets the system choose (all
// interfaces). Tests set it to loopback so that Windows does not ask for a firewall rule for the test binary.
var probeBindIP net.IP

// plainFactory is the client socket of a profile without obfuscation, used only when probeBindIP is set (otherwise the
// core client makes its own).
type plainFactory struct{}

func (plainFactory) New(net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", &net.UDPAddr{IP: probeBindIP})
}
