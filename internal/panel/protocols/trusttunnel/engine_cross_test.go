//go:build !js

package trusttunnel_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"golang.org/x/net/http2"

	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	nodett "github.com/mistgate/mistgate/internal/node/trusttunnel"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/trusttunnel"
	"github.com/mistgate/mistgate/internal/plugin"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

// The panel plugin and the node engine only meet through bytes: the inbound spec (Settings {"h3":bool}, SideListens), the
// credential's NodeData and the capabilities. Golden files check each side alone; this is the test that what the plugin issues is
// what the engine takes: the real plugin builds the inbound and two credentials, the real engine is given exactly those, and a
// client holding the secret the plugin returned (what the person's app gets) tunnels through it, over h2 and, with h3 on, h3.

type crossCert struct{ cert *tls.Certificate }

func (c crossCert) Acquire(context.Context, string, plugin.TLS) (nodeengine.Cert, error) {
	return nodeengine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c.cert, nil },
		Info: func() nodeengine.CertInfo {
			return nodeengine.CertInfo{PinSHA256: "pin", NotAfter: c.cert.Leaf.NotAfter}
		},
	}, nil
}
func (crossCert) Release(string) {}

func crossCertificate(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"de1.example.com"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// crossEgress sends every tunnel to one local echo server.
type crossEgress struct{ addr string }

func (e crossEgress) TCP(string) (net.Conn, error) { return net.Dial("tcp", e.addr) }
func (crossEgress) UDP(string) (nodeengine.EgressUDP, error) {
	return nil, io.ErrClosedPipe
}
func (crossEgress) CheckUDP(string) error { return nil }

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func connectReq(authority, auth string) (*http.Request, *io.PipeWriter) {
	reader, writer := io.Pipe()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Scheme: "https", Host: authority}, Host: authority, Header: http.Header{}, Body: reader, ContentLength: -1}
	if auth != "" {
		req.Header.Set("Proxy-Authorization", auth)
	}
	return req, writer
}

func tunnelEcho(t *testing.T, rt func(*http.Request) (*http.Response, error), secret string, wantOK bool) {
	t.Helper()
	user, pass, ok := strings.Cut(secret, ":")
	if !ok {
		t.Fatalf("the plugin's secret %q is not user:password", secret)
	}
	auth, err := wire.BuildProxyAuthorization(user, pass)
	if err != nil {
		t.Fatal(err)
	}
	req, w := connectReq("example.org:80", auth)
	resp, err := rt(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !wantOK {
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("a wrong secret got a tunnel")
		}
		_ = w.Close()
		return
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT with the secret the plugin issued: status %d", resp.StatusCode)
	}
	if _, err := io.WriteString(w, "ping through the plugin's credential"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != "ping through the plugin's credential" {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

func TestEngineAcceptsWhatThePluginIssues(t *testing.T) {
	for _, h3 := range []bool{false, true} {
		name := map[bool]string{false: "h2", true: "h2+h3"}[h3]
		t.Run(name, func(t *testing.T) {
			p := trusttunnel.New()
			settings, _ := json.Marshal(map[string]any{"port": 443, "h3": h3, "sni": "de1.example.com", "tls_mode": "acme_domain", "egress": "direct"})
			if errs := p.Validate(settings); len(errs) != 0 {
				t.Fatalf("validate: %+v", errs)
			}
			port := freePort(t)
			spec, err := p.BuildInbound(protocols.InboundInput{
				Profile: protocols.ProfileView{ID: "profile-1", Settings: settings}, Node: protocols.NodeView{Address: "203.0.113.10"},
				SpecVersion: 1, Enabled: true, PortOverride: port,
			})
			if err != nil {
				t.Fatal(err)
			}
			spec.ID = "inbound-1"
			if got := plugin.SideListens(spec); h3 != (len(got) == 1 && got[0] == plugin.Listen{Network: "udp", Port: uint16(port)}) {
				t.Fatalf("SideListens = %v for h3=%v", got, h3)
			}
			var creds []plugin.UserCred
			secrets := map[string]string{}
			for _, id := range []string{"alice-dev", "bob-dev"} {
				issued, err := p.IssueCredential(protocols.IssueInput{UserID: id, DeviceID: id})
				if err != nil {
					t.Fatal(err)
				}
				creds = append(creds, plugin.UserCred{CredID: id, UserID: id, DeviceID: id, Data: issued.NodeData})
				secrets[id] = issued.Secret
			}

			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer echo.Close()
			go func() {
				for {
					c, err := echo.Accept()
					if err != nil {
						return
					}
					go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
				}
			}()
			nodett.BindIP = net.ParseIP("127.0.0.1")
			env := nodeengine.Env{
				Certs:      crossCert{crossCertificate(t)},
				Egress:     func(string) (nodeengine.Egress, error) { return crossEgress{echo.Addr().String()}, nil },
				Masquerade: func(string) http.Handler { return http.NotFoundHandler() },
			}
			eng, err := nodett.New(env)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close(context.Background())
			report, err := eng.Apply(context.Background(), spec, creds)
			if err != nil {
				t.Fatalf("the engine refused the plugin's inbound: %v", err)
			}
			if report.CredCount != 2 || len(report.Warnings) != 0 {
				t.Fatalf("the engine took %d credentials with warnings %v, want 2 and none", report.CredCount, report.Warnings)
			}
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))

			tr := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "de1.example.com", NextProtos: []string{"h2"}}, DialTLSContext: func(ctx context.Context, _, _ string, c *tls.Config) (net.Conn, error) {
				return tls.Dial("tcp", addr, c)
			}}
			defer tr.CloseIdleConnections()
			tunnelEcho(t, tr.RoundTrip, secrets["alice-dev"], true)
			tunnelEcho(t, tr.RoundTrip, secrets["bob-dev"], true)
			tunnelEcho(t, tr.RoundTrip, "alice-dev:"+secrets["bob-dev"], false)

			if h3 {
				tr3 := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "de1.example.com"},
					Dial: func(ctx context.Context, _ string, c *tls.Config, q *quic.Config) (*quic.Conn, error) {
						return quic.DialAddr(ctx, addr, c, q)
					}}
				defer tr3.Close()
				tunnelEcho(t, tr3.RoundTrip, secrets["alice-dev"], true)
			} else if c, err := net.ListenPacket("udp", addr); err != nil {
				t.Fatalf("h3 is off but the UDP port is taken: %v", err)
			} else {
				_ = c.Close()
			}

			up, down := uint64(0), uint64(0)
			got, err := eng.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range got.Traffic {
				up += tr.Up
				down += tr.Down
			}
			if up == 0 || up != down {
				t.Fatalf("the engine counted up/down %d/%d for the echoed payloads", up, down)
			}
		})
	}
}
