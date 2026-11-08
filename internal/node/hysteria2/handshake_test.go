package hysteria2

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	quic "github.com/apernet/quic-go"

	"github.com/mistgate/mistgate/internal/plugin"
)

// inboundTotal adds up the inbound row's links/3 counters (and the hellos they are read against) until ready holds.
func inboundTotal(t *testing.T, r *rig, within time.Duration, ready func(plugin.LinkStat) bool) plugin.LinkStat {
	t.Helper()
	var total plugin.LinkStat
	deadline := time.Now().Add(within)
	for {
		for _, stat := range r.collect().Links {
			if stat.CredID != "" {
				continue
			}
			total.Hellos += stat.Hellos
			total.Accepted += stat.Accepted
			total.HsUnfinished += stat.HsUnfinished
			total.TLSAlertsSent += stat.TLSAlertsSent
			total.TLSAlertsRecv += stat.TLSAlertsRecv
			total.TLSCertRefused += stat.TLSCertRefused
		}
		if ready(total) {
			return total
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbound handshake counters = %+v, still not there", total)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// rawQUIC dials the inbound with a bare QUIC client and the given TLS config; the handshake is expected to fail.
func rawQUIC(t *testing.T, r *rig, conf *tls.Config) error {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	tr := &quic.Transport{Conn: pc}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tr.Dial(ctx, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.udpPort}, conf, &quic.Config{})
	if err == nil {
		_ = conn.CloseWithError(0, "")
	}
	return err
}

// A client that does not trust the node's certificate ends the handshake with a certificate alert: the node counts it as
// an alert received and a certificate refused. The handshake never completed, so it is no accepted connection.
func TestHandshakeCertificateRefusedByClient(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("pin"))
	err := rawQUIC(t, r, &tls.Config{ServerName: serverName, NextProtos: []string{"h3"}}) // verifies the self-signed test certificate
	var te *quic.TransportError
	if !errors.As(err, &te) || !te.ErrorCode.IsCryptoError() {
		t.Fatalf("dial with certificate verification = %v; want the client's own TLS alert", err)
	}
	got := inboundTotal(t, r, 8*time.Second, func(s plugin.LinkStat) bool { return s.TLSAlertsRecv > 0 })
	if got.TLSAlertsRecv != 1 || got.TLSCertRefused != 1 || got.TLSAlertsSent != 0 || got.HsUnfinished != 0 || got.Accepted != 0 {
		t.Fatalf("handshake counters = %+v; want one received certificate alert and nothing else", got)
	}
}

// A client that does not speak Hysteria's ALPN is refused by the node with no_application_protocol: an alert sent.
func TestHandshakeAlertSentForWrongALPN(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("alpn"))
	err := rawQUIC(t, r, &tls.Config{ServerName: serverName, NextProtos: []string{"not-h3"}, InsecureSkipVerify: true})
	var te *quic.TransportError
	if !errors.As(err, &te) || !te.Remote || !te.ErrorCode.IsCryptoError() {
		t.Fatalf("dial with a foreign ALPN = %v; want the node's TLS alert", err)
	}
	got := inboundTotal(t, r, 8*time.Second, func(s plugin.LinkStat) bool { return s.TLSAlertsSent > 0 })
	if got.TLSAlertsSent != 1 || got.TLSAlertsRecv != 0 || got.TLSCertRefused != 0 || got.HsUnfinished != 0 {
		t.Fatalf("handshake counters = %+v; want one alert sent", got)
	}
}

// The node's answer never reaches the client (the path drops everything from the node after the first packet in): the
// handshake times out on the node and counts as unfinished, not as an alert.
func TestHandshakeUnfinishedWhenTheAnswerIsLost(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("lost"))
	proxy := newTestUDPProxy(t, r.udpPort)
	proxy.dropDown.Store(true)
	done := make(chan error, 1)
	go func() {
		c, _, err := client.NewClient(&client.Config{
			ConnFactory: loopFactory{}, ServerAddr: proxy.addr(), Auth: token("lost"),
			TLSConfig: client.TLSConfig{ServerName: serverName, InsecureSkipVerify: true},
		})
		if err == nil {
			_ = c.Close()
		}
		done <- err
	}()
	got := inboundTotal(t, r, 20*time.Second, func(s plugin.LinkStat) bool { return s.HsUnfinished > 0 })
	if got.HsUnfinished != 1 || got.TLSAlertsSent != 0 || got.TLSAlertsRecv != 0 || got.Accepted != 0 || got.Hellos == 0 {
		t.Fatalf("handshake counters = %+v; want one unfinished handshake after a hello", got)
	}
	if err := <-done; err == nil {
		t.Fatal("the client connected although nothing of the node reached it")
	}
}

// A connection that completes its handshake is never counted as a failure, however it ends.
func TestHandshakeCompletedIsNoFailure(t *testing.T) {
	r := newRig(t, "")
	r.apply(cred("ok"))
	c := r.mustDial("ok")
	if err := echoOnce(c, r.echo, payload(64)); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	got := inboundTotal(t, r, 8*time.Second, func(s plugin.LinkStat) bool { return s.Accepted > 0 })
	time.Sleep(200 * time.Millisecond)
	for _, stat := range r.collect().Links {
		got.HsUnfinished += stat.HsUnfinished
		got.TLSAlertsSent += stat.TLSAlertsSent
		got.TLSAlertsRecv += stat.TLSAlertsRecv
	}
	if got.HsUnfinished|got.TLSAlertsSent|got.TLSAlertsRecv != 0 {
		t.Fatalf("a completed handshake counted as failed: %+v", got)
	}
}

func TestHandshakeFailureClasses(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		class hsFailure
		cert  bool
	}{
		{"handshake timeout", &quic.HandshakeTimeoutError{}, hsTimeout, false},
		{"idle timeout", &quic.IdleTimeoutError{}, hsTimeout, false},
		{"alert sent", &quic.TransportError{ErrorCode: 0x100 + 120}, hsAlertSent, false},
		{"bad certificate from the client", &quic.TransportError{ErrorCode: 0x100 + 42, Remote: true}, hsAlertRecv, true},
		{"unknown ca from the client", &quic.TransportError{ErrorCode: 0x100 + 48, Remote: true}, hsAlertRecv, true},
		{"handshake failure from the client", &quic.TransportError{ErrorCode: 0x100 + 40, Remote: true}, hsAlertRecv, false},
		{"protocol violation", &quic.TransportError{ErrorCode: quic.ProtocolViolation, Remote: true}, hsOther, false},
		{"application close", &quic.ApplicationError{Remote: true}, hsOther, false},
		{"stateless reset", &quic.StatelessResetError{}, hsOther, false},
	} {
		class, cert := handshakeFailure(c.err)
		if class != c.class || cert != c.cert {
			t.Errorf("%s: class %d, certificate %v; want %d, %v", c.name, class, cert, c.class, c.cert)
		}
	}
}
