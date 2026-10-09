package vless

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	xlog "github.com/xtls/xray-core/common/log"

	"github.com/mistgate/mistgate/internal/node/engine"
)

func TestProtocolVersionAndCapabilities(t *testing.T) {
	instance, err := New(engine.Env{})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close(context.Background())
	if got := instance.Protocol(); got != "vless" {
		t.Fatalf("Protocol() = %q", got)
	}
	if got := instance.Version(); got != "xray-core v26.3.27" {
		t.Fatalf("Version() = %q", got)
	}
	if got := instance.Capabilities(); !got.RateLimitPerCred || !got.HardExpiry {
		t.Fatalf("Capabilities() = %+v", got)
	}
}

func TestXrayLogHandlerDropsAccessAndBelowWarning(t *testing.T) {
	var output bytes.Buffer
	installXrayLogging(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	handler := xrayLogHandler{}
	handler.Handle(&xlog.AccessMessage{From: "client", To: "203.0.113.10:443", Status: xlog.AccessAccepted, Email: "alice"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Info, Content: "info destination=203.0.113.10:443 email=alice"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Debug, Content: "debug"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "warning"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Error, Content: "error"})

	got := output.String()
	if strings.Contains(got, "203.0.113.10") || strings.Contains(got, "alice") {
		t.Fatalf("sensitive access data reached slog: %s", got)
	}
	if strings.Contains(got, "info") || strings.Contains(got, "debug") || !strings.Contains(got, "warning") || !strings.Contains(got, "error") {
		t.Fatalf("unexpected Xray severity filtering: %s", got)
	}
	if strings.Count(got, "source=xray") != 2 {
		t.Fatalf("warning and error records must carry source=xray: %s", got)
	}
}

func TestXrayLogHandlerDropsSessionsAndNoiseAndRedactsAddresses(t *testing.T) {
	var output bytes.Buffer
	installXrayLogging(slog.New(slog.NewTextHandler(&output, nil)))
	handler := xrayLogHandler{}
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "[1799322037] account 66ad4540-b58c-4ad2-9926-ea63445a9b57 is rejected from 203.0.113.10"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "account 66ad4540-b58c-4ad2-9926-ea63445a9b57 failed from 203.0.113.10 via [2001:db8::10]:443"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "core: Xray 1.8.25 started"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Error, Content: "failed to serve HTTP for XHTTP > accept tcp 127.0.0.1:443: use of closed network connection"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "safe warning"})

	got := output.String()
	for _, sensitive := range []string{"1799322037", "66ad4540-b58c-4ad2-9926-ea63445a9b57", "203.0.113.10", "2001:db8::10", "127.0.0.1", "core: Xray", "failed to serve HTTP for XHTTP"} {
		if strings.Contains(got, sensitive) {
			t.Errorf("Xray log retained %q: %s", sensitive, got)
		}
	}
	if !strings.Contains(got, "account [redacted]") || !strings.Contains(got, "safe warning") {
		t.Fatalf("safe warning content was not forwarded after redaction: %s", got)
	}
}
