//go:build !js

package hosttunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
)

// ErrNotRecognised: the peer completed TLS far enough to show a certificate, and it was not the node's. The node answers
// as its site to a hello whose mark it does not accept: clocks too far apart, an older agent, or another tunnel secret.
var ErrNotRecognised = errors.New("hosttunnel: the node did not recognise the panel")

// DialConfig is the panel's side of a tunnel handshake.
type DialConfig struct {
	ServerName  string // the subscription name
	Secret      string // the tunnel secret
	Certificate tls.Certificate
	// VerifyNode checks the node's certificate chain.
	VerifyNode func(rawCerts [][]byte) error
	// Now is the clock of the mark; nil means time.Now.
	Now func() time.Time
}

// Dial runs the tunnel's TLS handshake on raw with Chrome's ClientHello carrying the mark, and returns the connection
// once the node proved itself and accepted HTTP/2 over TLS 1.3. It closes raw on error.
func Dial(ctx context.Context, raw net.Conn, cfg DialConfig) (net.Conn, error) {
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	foreign := false
	uc := utls.UClient(raw, &utls.Config{
		ServerName: cfg.ServerName, MinVersion: utls.VersionTLS13,
		InsecureSkipVerify: true, // the node's chain and identity are checked by VerifyNode
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if err := cfg.VerifyNode(rawCerts); err != nil {
				foreign = true
				return err
			}
			return nil
		},
		Certificates: []utls.Certificate{{Certificate: cfg.Certificate.Certificate, PrivateKey: cfg.Certificate.PrivateKey}},
	}, utls.HelloChrome_Auto)
	if err := uc.BuildHandshakeState(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("build client hello: %w", err)
	}
	hello := uc.HandshakeState.Hello
	// Chrome's hello already carries a random 32-byte session id: the mark replaces it in place, so the bytes it
	// authenticates are the bytes sent.
	if err := SealMark(cfg.Secret, hello.Raw, now()); err != nil {
		_ = raw.Close()
		return nil, err
	}
	hello.SessionId = append(hello.SessionId[:0], hello.Raw[helloSID:helloSIDEnd]...)
	if err := uc.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		if foreign {
			return nil, fmt.Errorf("%w: %w", ErrNotRecognised, err)
		}
		return nil, err
	}
	if st := uc.ConnectionState(); st.Version != utls.VersionTLS13 || st.NegotiatedProtocol != "h2" {
		_ = uc.Close()
		return nil, errors.New("hosttunnel: the node did not accept the tunnel protocol")
	}
	return uc, nil
}
