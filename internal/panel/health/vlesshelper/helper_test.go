//go:build !js

package vlesshelper

import (
	"bytes"
	"strings"
	"testing"

	xraylog "github.com/xtls/xray-core/common/log"
)

func TestRejectedVLESSUUIDIsRedactedFromHelperLogs(t *testing.T) {
	uuid := "35b78532-7b51-4ea4-9c02-4e8f3a51d103"
	var output bytes.Buffer
	logger := &stderrLogger{writer: &output, secrets: []string{uuid}}
	logger.Handle(&xraylog.AccessMessage{Status: xraylog.AccessRejected, Reason: "invalid request user id: " + uuid})
	if got := output.String(); strings.Contains(got, uuid) || strings.Count(got, "\n") != 1 || !strings.Contains(got, "invalid request user id") {
		t.Fatalf("helper rejection log was not safe and single-line: %q", got)
	}
}

func TestHelperLogsForwardOnlyFailuresAndTheTunnelMarker(t *testing.T) {
	var output bytes.Buffer
	logger := &stderrLogger{writer: &output}
	for _, m := range []struct {
		severity xraylog.Severity
		text     string
	}{
		{xraylog.Severity_Debug, "XMUX: creating xmuxClient"},
		{xraylog.Severity_Info, "app/dispatcher: default route for tcp:a:1"},
		{xraylog.Severity_Info, "transport/internet/splithttp: failed to GET > x509: certificate signed by unknown authority"},
		{xraylog.Severity_Info, "proxy/vless/outbound: tunneling request to tcp:a:1 via b:2"},
		{xraylog.Severity_Error, "websocket: failed to dial"},
	} {
		logger.Handle(&xraylog.GeneralMessage{Severity: m.severity, Content: m.text})
	}
	got := output.String()
	if strings.Count(got, "\n") != 3 || strings.Contains(got, "XMUX") || strings.Contains(got, "default route") {
		t.Fatalf("unexpected helper stderr: %q", got)
	}
}
