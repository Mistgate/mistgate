package hysteria2

import (
	"context"
	"errors"
	"sync/atomic"

	quic "github.com/apernet/quic-go"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/nets"
)

// Why handshakes fail (agent.proto LinkCounters, links/3). quic-go never returns a connection whose handshake failed: the
// listener only hands out completed ones. The patched core passes quic.Transport.ConnContext through
// (third_party/hysteria-core/MISTGATE.md), which quic-go calls for every connection it creates and whose context it ends
// with the close error, also when the handshake did not complete. One callback per connection, nothing per packet; only
// the resolved ASN is retained from ClientInfo.RemoteAddr.

type handshakeKey struct{}

// handshake follows one QUIC connection from its first packet to its end.
type handshake struct {
	accepted atomic.Bool // the handshake completed (ConnEventLogger.Accepted)
	asn      uint32
}

// connContext is QUICConfig.ConnContext of the inbound's server.
func (in *inbound) connContext(ctx context.Context, client *quic.ClientInfo) (context.Context, error) {
	var asn uint32
	if client != nil {
		asn = nets.LookupAddr(in.e.env.Nets, client.RemoteAddr)
	}
	hs := &handshake{asn: asn}
	if in.e.env.Nets != nil {
		in.e.env.Nets.Add(in.spec.ID, asn, nets.Attempt, "", "")
	}
	context.AfterFunc(ctx, func() { in.handshakeEnded(hs, context.Cause(ctx)) })
	return context.WithValue(ctx, handshakeKey{}, hs), nil
}

// markAccepted tells the connection's tracker that its handshake completed: its end is then an ordinary close, counted by
// ConnDisconnect, not a failed handshake.
func markAccepted(conn *quic.Conn) {
	if conn == nil {
		return
	}
	if hs, ok := conn.Context().Value(handshakeKey{}).(*handshake); ok {
		hs.accepted.Store(true)
	}
}

// handshakeEnded counts why a connection whose handshake never completed ended. A connection that closed without a timeout
// or an alert (an application close, a stateless reset, a transport error) is not counted: hellos - accepted keeps it.
func (in *inbound) handshakeEnded(hs *handshake, cause error) {
	if hs.accepted.Load() || cause == nil {
		return
	}
	switch class, certificate := handshakeFailure(cause); class {
	case hsTimeout:
		in.hsUnfinished.Add(1)
		if in.e.env.Nets != nil {
			in.e.env.Nets.Add(in.spec.ID, hs.asn, nets.Unfinished, "", "")
		}
	case hsAlertSent:
		in.tlsAlertsSent.Add(1)
	case hsAlertRecv:
		in.tlsAlertsRecv.Add(1)
		if certificate {
			in.tlsCertRefused.Add(1)
		}
	}
}

type hsFailure int

const (
	hsOther hsFailure = iota
	hsTimeout
	hsAlertSent
	hsAlertRecv
)

// handshakeFailure classifies the close error of a handshake: a timeout (the node's answer got no reply in time), or a TLS
// alert (a CRYPTO_ERROR, 0x100 + the alert) sent by the node or by the client; certificate reports an alert that refuses
// the certificate.
func handshakeFailure(err error) (class hsFailure, certificate bool) {
	var hsTimeoutErr *quic.HandshakeTimeoutError
	var idle *quic.IdleTimeoutError
	if errors.As(err, &hsTimeoutErr) || errors.As(err, &idle) {
		return hsTimeout, false
	}
	var te *quic.TransportError
	if !errors.As(err, &te) || !te.ErrorCode.IsCryptoError() {
		return hsOther, false
	}
	if !te.Remote {
		return hsAlertSent, false
	}
	return hsAlertRecv, engine.CertificateAlert(uint8(te.ErrorCode - 0x100))
}
