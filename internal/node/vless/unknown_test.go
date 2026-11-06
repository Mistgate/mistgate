package vless

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	xtlsreality "github.com/xtls/reality"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol"
	xuuid "github.com/xtls/xray-core/common/uuid"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vless/encoding"

	"github.com/mistgate/mistgate/internal/plugin"
)

func TestUnknownVLESSUUIDsCountOncePerInbound(t *testing.T) {
	f, certs := newTLSRuntimeFixture(t)
	certs.setIssued()
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	target.StartTLS()
	t.Cleanup(target.Close)
	f.targetAddr = strings.TrimPrefix(target.URL, "https://")
	for alpn := range 3 {
		xtlsreality.GlobalPostHandshakeRecordsLens.Store(fmt.Sprintf("%s localhost %d", f.targetAddr, alpn), []int{})
	}
	reality := f.spec("reality-tcp", "tcp")
	tls := f.tlsSpec("tls-xhttp", "xhttp", "auto")
	credReality := testCredential("reality-user", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	credTLS := testCredential("tls-user", "66ad4540-b58c-4ad2-9926-ea63445a9b58")
	if _, err := f.engine.Apply(context.Background(), reality, []plugin.UserCred{credReality}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Apply(context.Background(), tls, []plugin.UserCred{credTLS}); err != nil {
		t.Fatal(err)
	}

	unknownReality := newXrayClient(t, reality.Listen.Port, "tcp", "stale-reality", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	unknownTLS := newXrayTLSClient(t, tls.Listen.Port, "xhttp", "auto", "h2", "stale-tls", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	echo := startEchoServer(t)
	targetAddr := echo.Addr().String()
	for range 2 {
		conn, _ := unknownReality.tryDial(targetAddr)
		if conn != nil {
			_, _ = conn.Write([]byte("unknown"))
			_ = conn.Close()
		}
	}
	conn, _ := unknownTLS.tryDial(targetAddr)
	if conn != nil {
		_, _ = conn.Write([]byte("unknown"))
		_ = conn.Close()
	}
	waitUnknownKeyLinks(t, f.engine, map[string]uint32{"reality-tcp": 2, "tls-xhttp": 1})

	knownReality := newXrayClient(t, reality.Listen.Port, "tcp", credReality.CredID, stringID(credReality))
	connReality := knownReality.dial(t, echo.Addr().String())
	if _, err := exchange(connReality, []byte("known REALITY UUID")); err != nil {
		t.Fatal(err)
	}
	_ = connReality.Close()

	knownTLS := newXrayTLSClient(t, tls.Listen.Port, "xhttp", "auto", "h2", credTLS.CredID, stringID(credTLS))
	connTLS := knownTLS.dial(t, echo.Addr().String())
	if _, err := exchange(connTLS, []byte("known TLS UUID")); err != nil {
		t.Fatal(err)
	}
	_ = connTLS.Close()

	collected, err := f.engine.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Links) != 0 {
		t.Fatalf("known UUIDs added unknown-key counters: %+v", collected.Links)
	}
}

func TestUnknownUUIDValidatorIgnoresKnownUsersAndNonVLESSGarbage(t *testing.T) {
	base := new(xrayvless.MemoryValidator)
	knownID, err := xuuid.ParseString("66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Add(&protocol.MemoryUser{
		Email:   "known",
		Account: &xrayvless.MemoryAccount{ID: protocol.NewID(knownID)},
	}); err != nil {
		t.Fatal(err)
	}
	unknownID, err := xuuid.ParseString("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	var counted uint32
	validator := &countingValidator{Validator: base, onUnknown: func() { counted++ }}

	if err := decodeVLESSRequest(t, validator, testVLESSRequest(0, unknownID)); err == nil || !strings.Contains(err.Error(), "invalid request user id") {
		t.Fatalf("unknown UUID decode error = %v, want invalid request user id", err)
	}
	if counted != 1 {
		t.Fatalf("unknown UUID count = %d, want one rejected connection", counted)
	}
	if err := decodeVLESSRequest(t, validator, testVLESSRequest(0, knownID)); err != nil {
		t.Fatalf("known UUID request failed to decode: %v", err)
	}
	if err := decodeVLESSRequest(t, validator, []byte("GET /not-vless HTTP/1.1\r\n\r\n")); err == nil || !strings.Contains(err.Error(), "invalid request version") {
		t.Fatalf("garbage decode error = %v, want invalid request version", err)
	}
	if counted != 1 {
		t.Fatalf("known request or non-VLESS garbage changed count to %d, want one", counted)
	}
}

func TestUnknownVLESSWarningIsRateLimitedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	tracker := newUnknownKeyTracker("profile-a", logger)
	start := time.Unix(1_800_000_000, 0)
	tracker.record(start)
	tracker.record(start.Add(time.Minute))
	tracker.record(start.Add(9*time.Minute + 59*time.Second))
	if got := strings.Count(output.String(), "level=WARN"); got != 1 {
		t.Fatalf("warning count before ten minutes = %d, want 1: %s", got, output.String())
	}
	tracker.record(start.Add(10 * time.Minute))
	got := output.String()
	if strings.Count(got, "level=WARN") != 2 || !strings.Contains(got, "3 connections with an unknown key on profile-a in the last 10 min") {
		t.Fatalf("rate-limited warning did not report its ten-minute count: %s", got)
	}
	for _, secret := range []string{"aaaaaaaa-aaaa-4aaa-8bbb-cccccccccccc", "203.0.113.10", "2001:db8::10"} {
		if strings.Contains(got, secret) {
			t.Errorf("warning exposed %q: %s", secret, got)
		}
	}
}

func decodeVLESSRequest(t *testing.T, validator xrayvless.Validator, data []byte) error {
	t.Helper()
	first := buf.New()
	defer first.Release()
	_, _, _, _, err := encoding.DecodeRequestHeader(false, first, bytes.NewReader(data), validator)
	return err
}

func testVLESSRequest(version byte, id xuuid.UUID) []byte {
	request := append([]byte{version}, id[:]...)
	return append(request, 0, byte(protocol.RequestCommandTCP), 0, 80, 1, 127, 0, 0, 1)
}

func waitUnknownKeyLinks(t *testing.T, e *eng, want map[string]uint32) {
	t.Helper()
	got := make(map[string]uint32, len(want))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		collected, err := e.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, stat := range collected.Links {
			got[stat.InboundID] += stat.UnknownKeys
		}
		complete := len(got) == len(want)
		for inboundID, count := range want {
			if got[inboundID] != count {
				complete = false
			}
		}
		if complete {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("unknown-key links = %v, want %v", got, want)
}

func TestUnknownKeyStatsSurviveInboundRemoval(t *testing.T) {
	engine := &eng{inbounds: make(map[string]*inbound), unknownKeys: make(map[string]*unknownKeyTracker)}
	tracker := engine.unknownKeyTracker("removed-profile")
	tracker.record(time.Now())
	if err := engine.Remove(context.Background(), "removed-profile"); err != nil {
		t.Fatal(err)
	}
	collected, err := engine.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Links) != 1 || collected.Links[0] != (plugin.LinkStat{InboundID: "removed-profile", UnknownKeys: 1}) {
		t.Fatalf("removed inbound links = %+v, want its pending unknown-key count", collected.Links)
	}
}

// A handler of another shape is refused without being touched: start() then serves the inbound without the count.
func TestInstrumentRefusesAnotherShape(t *testing.T) {
	other := &struct{ validator any }{}
	for _, v := range []any{nil, other, struct{}{}} {
		if err := instrumentVLESSValidator(v, func() {}); err == nil {
			t.Errorf("instrumentVLESSValidator(%T) = nil, want an error", v)
		}
	}
	if other.validator != nil {
		t.Error("the foreign struct was modified")
	}
}
