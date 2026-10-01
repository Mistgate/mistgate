package cfapi

// The uTLS ClientHello and the dial helper below are derived from wgcf v2.3.0 (cloudflare/api.go, commit ace873c),
// https://github.com/ViRb3/wgcf, which is distributed under the MIT License:
//
//	MIT License
//
//	Copyright (c) 2020 ViRb3
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy
//	of this software and associated documentation files (the "Software"), to deal
//	in the Software without restriction, including without limitation the rights
//	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//	copies of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all
//	copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//	SOFTWARE.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

const handshakeTimeout = 10 * time.Second

var (
	specsMu  sync.RWMutex
	tlsSpecs = map[string]func() *utls.ClientHelloSpec{"wgcf_v2.3.0": wgcfHelloV230}
)

// RegisterTLSSpec adds a named ClientHello (for the day Cloudflare changes what it accepts and the owner ships a new
// one without waiting for a release). An existing name is replaced.
func RegisterTLSSpec(id string, spec func() *utls.ClientHelloSpec) {
	specsMu.Lock()
	defer specsMu.Unlock()
	tlsSpecs[id] = spec
}

// TLSSpecIDs lists the known ClientHello names.
func TLSSpecIDs() []string {
	specsMu.RLock()
	defer specsMu.RUnlock()
	out := make([]string, 0, len(tlsSpecs))
	for id := range tlsSpecs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func lookupSpec(id string) (func() *utls.ClientHelloSpec, bool) {
	specsMu.RLock()
	defer specsMu.RUnlock()
	f, ok := tlsSpecs[id]
	return f, ok
}

// newTransport returns an HTTP/1.1 transport whose TLS handshake is the named uTLS ClientHello. A proxy would make
// net/http bypass DialTLSContext for the target handshake, so there is none.
func newTransport(specID string, roots *x509.CertPool) (*http.Transport, error) {
	build, ok := lookupSpec(specID)
	if !ok {
		return nil, fmt.Errorf("cfapi: unknown tls spec %q", specID)
	}
	cache := utls.NewLRUClientSessionCache(8)
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: false,
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			raw, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				raw.Close()
				return nil, fmt.Errorf("split TLS address: %w", err)
			}
			hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
			defer cancel()
			c, err := handshake(hctx, raw, host, roots, cache, build())
			if err != nil {
				raw.Close()
				return nil, err
			}
			return c, nil
		},
		TLSHandshakeTimeout:   handshakeTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
	}, nil
}

func handshake(ctx context.Context, raw net.Conn, serverName string, roots *x509.CertPool, cache utls.ClientSessionCache, spec *utls.ClientHelloSpec) (*utls.UConn, error) {
	cfg := &utls.Config{
		ServerName:         serverName,
		RootCAs:            roots,
		ClientSessionCache: cache,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
		NextProtos:         []string{"http/1.1"},
	}
	u := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := u.ApplyPreset(spec); err != nil {
		return nil, fmt.Errorf("apply WARP TLS fingerprint: %w", err)
	}
	// Conscrypt leaves the session ID empty on a fresh TLS 1.2 connection. uTLS normally generates one for every
	// custom ClientHello, so clear it before the session lookup and restore it only when a cached ticket is used.
	u.HandshakeState.Hello.SessionId = nil
	if err := u.BuildHandshakeState(); err != nil {
		return nil, fmt.Errorf("build WARP TLS ClientHello: %w", err)
	}
	if ticketPresent(u) {
		sid := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, sid); err != nil {
			return nil, fmt.Errorf("generate TLS session ID: %w", err)
		}
		u.HandshakeState.Hello.SessionId = sid
		if err := u.MarshalClientHello(); err != nil {
			return nil, fmt.Errorf("marshal resumed WARP TLS ClientHello: %w", err)
		}
	}
	if err := u.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("WARP TLS handshake: %w", err)
	}
	return u, nil
}

func ticketPresent(c *utls.UConn) bool {
	for _, e := range c.Extensions {
		if t, ok := e.(*utls.SessionTicketExtension); ok {
			return len(t.Ticket) != 0
		}
	}
	return false
}

// wgcfHelloV230 is the Android app's ClientHello as wgcf v2.3.0 builds it: TLS 1.2 only, two ECDHE AES-256-GCM
// suites, ALPN http/1.1 only (no h2), the extension order of Conscrypt.
func wgcfHelloV230() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS12,
		CipherSuites: []uint16{
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
		CompressionMethods: []uint8{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateNever},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}},
			&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
			&utls.SessionTicketExtension{},
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
				utls.PKCS1WithSHA1,
			}},
			&utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
		},
	}
}
