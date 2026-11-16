// Package sitehttptest checks that an HTTPS site of the node speaks HTTP/2 like every other one. A scanner sees the first
// SETTINGS and WINDOW_UPDATE frames a server sends, so all sites of a node (Hysteria2, VLESS, TrustTunnel, the bridge's)
// must send the same: Check opens a connection to a running site and compares them with the ones of sitehttp itself.
package sitehttptest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/node/sitehttp"
)

// Frames are the first SETTINGS and WINDOW_UPDATE frames a server sends on an HTTP/2 connection.
type Frames struct {
	Settings []http2.Setting
	Windows  []uint32
}

// Handshake runs TLS with ALPN h2 and the HTTP/2 preface over conn (any server name: the check does not verify the
// certificate) and returns the frames the server sent first. It closes conn.
func Handshake(t testing.TB, conn net.Conn) Frames {
	t.Helper()
	defer conn.Close()
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", NextProtos: []string{"h2"}})
	_ = tlsConn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake with the site: %v", err)
	}
	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != "h2" {
		t.Fatalf("the site negotiated %q, want h2", got)
	}
	if _, err := io.WriteString(tlsConn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	framer := http2.NewFramer(tlsConn, tlsConn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	var got Frames
	for len(got.Settings) == 0 || len(got.Windows) == 0 {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("reading the first frames of the site: %v", err)
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if f.IsAck() {
				continue
			}
			_ = f.ForeachSetting(func(s http2.Setting) error { got.Settings = append(got.Settings, s); return nil })
		case *http2.WindowUpdateFrame:
			if f.StreamID == 0 {
				got.Windows = append(got.Windows, f.Increment)
			}
		}
	}
	return got
}

// Certificate is a throwaway self-signed certificate for a site under test.
func Certificate(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"example.com"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Reference returns the frames of a bare server configured by sitehttp.ConfigureSiteHTTP2: what every site must send.
func Reference(t testing.TB) Frames {
	t.Helper()
	server := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		// every site of the node bounds the header block like this, and it shows in SETTINGS_MAX_HEADER_LIST_SIZE
		MaxHeaderBytes: 16 << 10,
		TLSConfig:      &tls.Config{Certificates: []tls.Certificate{Certificate(t)}, NextProtos: []string{"h2", "http/1.1"}},
	}
	if err := sitehttp.ConfigureSiteHTTP2(server); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(tls.NewListener(ln, server.TLSConfig))
	defer server.Close()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return Handshake(t, conn)
}

// Check fails the test when the site behind conn sends other first frames than sitehttp's own.
func Check(t testing.TB, site string, conn net.Conn) {
	t.Helper()
	want := Reference(t)
	got := Handshake(t, conn)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the %s site's first HTTP/2 frames differ from the shared ones:\n got  %+v\n want %+v", site, got, want)
	}
	if len(got.Windows) != 1 || got.Windows[0] != 16<<20-65535 {
		t.Fatalf("the %s site raised the connection window by %v, want one raise to 16 MiB", site, got.Windows)
	}
}
