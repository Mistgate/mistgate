//go:build !js

package warp

// The ClientHello below and the way it is applied are derived from wgcf v2.3.0 (cloudflare/api.go,
// warpClientHelloSpec), https://github.com/ViRb3/wgcf, which is distributed under the MIT License:
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
//
// Why: in July to September 2026 the registration endpoint answered HTTP 429 to a Go ClientHello on the very
// first request (wgcf issues 613 and 626); imitating the Android app's hello (TLS 1.2 only, two AES-256-GCM
// suites, http/1.1 only, empty session id on a fresh connection) is what fixed it.

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
	"time"

	utls "github.com/refraction-networking/utls"
)

// tlsSpecWgcf230 is the ClientHello of wgcf v2.3.0.
const tlsSpecWgcf230 = "wgcf_v2.3.0"

// tlsSpecs are the ClientHellos the registration client can imitate, by the id stored in Params.TLSSpecID. A new
// Cloudflare fingerprint check means a new entry here (code) and a changed id (data).
var tlsSpecs = map[string]func() *utls.ClientHelloSpec{
	tlsSpecWgcf230: wgcf230ClientHelloSpec,
}

func tlsSpecIDs() []string {
	ids := make([]string, 0, len(tlsSpecs))
	for id := range tlsSpecs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

const tlsHandshakeTimeout = 10 * time.Second

// newTransport builds an HTTP/1.1 transport whose TLS ClientHello is the given spec. roots nil = the system roots
// (tests pass a pool with the fake API's certificate). It never goes through a proxy: a CONNECT proxy would make
// net/http skip DialTLSContext and send its own hello.
func newTransport(specID string, roots *x509.CertPool) (*http.Transport, error) {
	spec, ok := tlsSpecs[specID]
	if !ok {
		return nil, fmt.Errorf("unknown tls spec %q", specID)
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	cache := utls.NewLRUClientSessionCache(16)
	return &http.Transport{
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			raw, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				raw.Close()
				return nil, err
			}
			hctx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
			defer cancel()
			conn, err := handshake(hctx, raw, host, roots, cache, spec())
			if err != nil {
				raw.Close()
				return nil, err
			}
			return conn, nil
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
	}, nil
}

func handshake(ctx context.Context, raw net.Conn, serverName string, roots *x509.CertPool, cache utls.ClientSessionCache, spec *utls.ClientHelloSpec) (*utls.UConn, error) {
	conn := utls.UClient(raw, &utls.Config{
		ServerName:         serverName,
		RootCAs:            roots,
		ClientSessionCache: cache,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
		NextProtos:         []string{"http/1.1"},
	}, utls.HelloCustom)
	if err := conn.ApplyPreset(spec); err != nil {
		return nil, fmt.Errorf("apply tls fingerprint: %w", err)
	}
	// Conscrypt leaves the session id empty on a fresh TLS 1.2 connection; uTLS fills one for every custom hello,
	// so clear it before the session lookup and restore it only when a cached ticket is used.
	conn.HandshakeState.Hello.SessionId = nil
	if err := conn.BuildHandshakeState(); err != nil {
		return nil, fmt.Errorf("build client hello: %w", err)
	}
	if ticketPresent(conn) {
		id := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, id); err != nil {
			return nil, err
		}
		conn.HandshakeState.Hello.SessionId = id
		if err := conn.MarshalClientHello(); err != nil {
			return nil, fmt.Errorf("marshal resumed client hello: %w", err)
		}
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return conn, nil
}

func ticketPresent(conn *utls.UConn) bool {
	for _, e := range conn.Extensions {
		if t, ok := e.(*utls.SessionTicketExtension); ok {
			return len(t.Ticket) != 0
		}
	}
	return false
}

func wgcf230ClientHelloSpec() *utls.ClientHelloSpec {
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
