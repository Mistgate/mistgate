package mtproto

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/mtproto/mtprototest"
	"github.com/mistgate/mistgate/internal/node/engine"
	panelhealth "github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

const healthWebDomain = "tg.example.com"

func TestPanelHealthProbeAgainstWebInbound(t *testing.T) {
	t.Run("valid key reaches fake DC and validates resPQ", func(t *testing.T) {
		cert := testCert(t, healthWebDomain)
		f := newHealthWebFixture(t, &fakeCerts{acme: cert, self: cert}, respondWebHealthPQ)
		tunnel, err := panelhealth.DialMTProto(context.Background(), healthWebTarget(f, key(1)), healthWebOptions(cert))
		if err != nil {
			t.Fatal(err)
		}
		defer tunnel.Close()
		result := tunnel.(interface{ ProbeResult() panelhealth.Result }).ProbeResult()
		if result.Status != adminv1.CheckStatus_CHECK_STATUS_OK || result.ConnectMS == 0 || result.ResponseMS == 0 {
			t.Fatalf("Web health result: %+v", result)
		}
	})

	t.Run("wrong key is auth", func(t *testing.T) {
		cert := testCert(t, healthWebDomain)
		f := newHealthWebFixture(t, &fakeCerts{acme: cert, self: cert}, respondWebHealthPQ)
		assertWebHealthCode(t, healthWebTarget(f, key(9)), healthWebOptions(cert), "auth")
	})

	t.Run("certificate name mismatch is tls", func(t *testing.T) {
		cert := testCert(t, "other.example.com")
		f := newHealthWebFixture(t, &fakeCerts{acme: cert, self: cert}, respondWebHealthPQ)
		assertWebHealthCode(t, healthWebTarget(f, key(1)), healthWebOptions(cert), "tls")
	})

	t.Run("unissued certificate has a specific tls detail", func(t *testing.T) {
		cert := testCert(t, healthWebDomain)
		f := newHealthWebFixture(t, &fakeCerts{acme: cert, self: cert}, respondWebHealthPQ)
		_, err := panelhealth.DialMTProto(context.Background(), healthWebTarget(f, key(1)), panelhealth.DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		})
		assertWebHealthError(t, err, "tls")
		var probeErr *panelhealth.ProbeError
		if !errors.As(err, &probeErr) || !strings.Contains(probeErr.Detail, "not been issued yet") {
			t.Fatalf("certificate detail = %v", err)
		}
	})

	t.Run("fake DC refusal is exit_unreachable", func(t *testing.T) {
		cert := testCert(t, healthWebDomain)
		f := newHealthWebFixture(t, &fakeCerts{acme: cert, self: cert}, func(c *mtprototest.DCConn) {
			_, _ = readWebHealthRequest(c)
		})
		assertWebHealthCode(t, healthWebTarget(f, key(1)), healthWebOptions(cert), "exit_unreachable")
	})

	t.Run("silent fake DC is timeout", func(t *testing.T) {
		cert := testCert(t, healthWebDomain)
		release := make(chan struct{})
		defer close(release)
		f := newHealthWebFixture(t, &fakeCerts{acme: cert, self: cert}, func(c *mtprototest.DCConn) {
			if _, err := readWebHealthRequest(c); err == nil {
				<-release
			}
		})
		assertWebHealthCode(t, healthWebTarget(f, key(1)), panelhealth.DialOptions{
			HandshakeTimeout: 2 * time.Second, ProbeTimeout: 300 * time.Millisecond, ProbeTLS: healthWebTLS(cert),
		}, "timeout")
	})
}

func newHealthWebFixture(t *testing.T, certs *fakeCerts, serve func(*mtprototest.DCConn)) *fixture {
	t.Helper()
	oldBind, oldDC := BindIP, dcAddress
	BindIP = net.IPv4(127, 0, 0, 1)
	dc := mtprototest.NewDC(t, serve)
	dcAddress = func(dcID int16) (string, bool) { return dc.Addr, dcID == 2 }
	t.Cleanup(func() { BindIP, dcAddress = oldBind, oldDC })

	port := freePort(t)
	spec := plugin.InboundSpec{
		ID: "health_web", Protocol: Protocol, Enabled: true,
		Listen:   plugin.Listen{Network: "tcp", Port: port},
		Settings: json.RawMessage(`{"domains":["tg.example.com"],"mode":"web"}`),
	}
	eng, err := New(engine.Env{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Certs: certs})
	if err != nil {
		t.Fatal(err)
	}
	e := eng.(*Engine)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	if _, err := e.Apply(context.Background(), spec, []plugin.UserCred{credential("probe", key(1))}); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, e: e, spec: spec, dc: dc, addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}
}

func healthWebTarget(f *fixture, probeKey [16]byte) panelhealth.Target {
	return panelhealth.Target{
		Node:    store.NodeRow{Address: "127.0.0.1"},
		Inbound: store.FleetInboundRow{ID: f.spec.ID, Protocol: Protocol},
		Spec:    f.spec, Settings: f.spec.Settings, CredID: "probe", Secret: hex.EncodeToString(probeKey[:]),
	}
}

func healthWebTLS(cert *tls.Certificate) *tls.Config {
	roots := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		panic(err)
	}
	roots.AddCert(leaf)
	return &tls.Config{RootCAs: roots}
}

func healthWebOptions(cert *tls.Certificate) panelhealth.DialOptions {
	return panelhealth.DialOptions{HandshakeTimeout: time.Second, ProbeTimeout: time.Second, ProbeTLS: healthWebTLS(cert)}
}

func assertWebHealthCode(t *testing.T, target panelhealth.Target, options panelhealth.DialOptions, want string) {
	t.Helper()
	_, err := panelhealth.DialMTProto(context.Background(), target, options)
	assertWebHealthError(t, err, want)
}

func assertWebHealthError(t *testing.T, err error, want string) {
	t.Helper()
	var probeErr *panelhealth.ProbeError
	if !errors.As(err, &probeErr) || probeErr.Code != want {
		t.Fatalf("Web health error = %v, want code %q", err, want)
	}
}

func readWebHealthRequest(c *mtprototest.DCConn) ([]byte, error) {
	var first [1]byte
	if _, err := io.ReadFull(c, first[:]); err != nil {
		return nil, err
	}
	words := int(first[0])
	if first[0] == 0x7f {
		var length [3]byte
		if _, err := io.ReadFull(c, length[:]); err != nil {
			return nil, err
		}
		words = int(length[0]) | int(length[1])<<8 | int(length[2])<<16
	}
	if words <= 0 || words > 64<<10/4 {
		return nil, errors.New("invalid health request length")
	}
	msg := make([]byte, words*4)
	if _, err := io.ReadFull(c, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func respondWebHealthPQ(c *mtprototest.DCConn) {
	request, err := readWebHealthRequest(c)
	if err != nil || len(request) < 40 {
		return
	}
	response := make([]byte, 76)
	binary.LittleEndian.PutUint64(response[8:16], uint64(time.Now().Unix())<<32)
	binary.LittleEndian.PutUint32(response[16:20], uint32(len(response)-20))
	binary.LittleEndian.PutUint32(response[20:24], 0x05162463)
	copy(response[24:40], request[24:40])
	copy(response[40:56], []byte("server-nonce-123"))
	response[56] = 3
	copy(response[57:60], []byte{1, 2, 3})
	binary.LittleEndian.PutUint32(response[60:64], 0x1cb5c415)
	binary.LittleEndian.PutUint32(response[64:68], 1)
	binary.LittleEndian.PutUint64(response[68:76], 0x1122334455667788)
	words := len(response) / 4
	frame := append([]byte{byte(words)}, response...)
	for len(frame) > 0 {
		n, err := c.Write(frame)
		if err != nil || n == 0 {
			return
		}
		frame = frame[n:]
	}
	_, _ = c.Read(make([]byte, 1))
}
