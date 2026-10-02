package provision

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"golang.org/x/crypto/ssh"
)

func TestParsePreflightOutput(t *testing.T) {
	nodeID := "nod_" + strings.Repeat("a", 26)
	output := []byte(strings.Join([]string{
		"distribution=ubuntu", "version=22.04", "kernel=6.8.0", "architecture=x86_64",
		"cpu_count=4", "memory_bytes=1073741824", "disk_available_bytes=2147483648",
		"systemd=yes", "already_enrolled=yes", "existing_node_id=" + nodeID, "panel_reachable=yes",
	}, "\n"))
	got, err := parsePreflightOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if got.nodeID != nodeID || !got.facts.AlreadyEnrolled || got.facts.CpuCount != 4 || got.facts.MemoryBytes != 1<<30 {
		t.Fatalf("parsed preflight = %+v", got)
	}
	if arch, code := supportedPreflight(got.facts); arch != "amd64" || code != "" {
		t.Fatalf("supportedPreflight = %q, %q", arch, code)
	}
}

func TestParsePreflightRejectsMalformedIdentityAndOutput(t *testing.T) {
	base := strings.Join([]string{
		"distribution=debian", "version=12", "kernel=6.1", "architecture=aarch64",
		"cpu_count=2", "memory_bytes=536870912", "disk_available_bytes=1073741824",
		"systemd=yes", "already_enrolled=no", "existing_node_id=", "panel_reachable=yes",
	}, "\n")
	if _, err := parsePreflightOutput([]byte(base)); err != nil {
		t.Fatalf("supported Debian 12 facts rejected: %v", err)
	}
	if arch, code := supportedPreflight(&adminv1.NodePreflight{
		Distribution: "debian", Version: "12", Architecture: "aarch64", CpuCount: 2,
		MemoryBytes: 512 << 20, Systemd: true, PanelReachable: true,
	}); arch != "arm64" || code != "" {
		t.Fatalf("Debian 12 support = %q, %q", arch, code)
	}
	if _, err := parsePreflightOutput([]byte(strings.Replace(base, "existing_node_id=", "existing_node_id=nod_bad", 1))); err == nil {
		t.Fatal("malformed node identity accepted")
	}
	if _, err := parsePreflightOutput([]byte(base + "\nignored=unexpected")); err == nil {
		t.Fatal("unknown preflight field accepted")
	}
	if _, err := parsePreflightOutput([]byte(strings.Replace(base, "\nexisting_node_id=", "", 1))); err == nil {
		t.Fatal("missing existing-node field accepted")
	}
	duplicate := base + "\nsystemd=yes"
	if _, err := parsePreflightOutput([]byte(duplicate)); err == nil {
		t.Fatal("duplicate preflight field accepted")
	}
}

func TestPreflightVersionAndArchitectureRequirements(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts *adminv1.NodePreflight
		code  string
	}{
		{"old Ubuntu", &adminv1.NodePreflight{Distribution: "ubuntu", Version: "20.04", Architecture: "x86_64", CpuCount: 2, MemoryBytes: 1 << 30, Systemd: true, PanelReachable: true}, "unsupported_os_version"},
		{"unsupported system", &adminv1.NodePreflight{Distribution: "alpine", Version: "3.20", Architecture: "x86_64", CpuCount: 2, MemoryBytes: 1 << 30, Systemd: true, PanelReachable: true}, "unsupported_os"},
		{"no systemd", &adminv1.NodePreflight{Distribution: "debian", Version: "12", Architecture: "x86_64", CpuCount: 2, MemoryBytes: 1 << 30, PanelReachable: true}, "systemd_required"},
		{"unsupported arch", &adminv1.NodePreflight{Distribution: "debian", Version: "12", Architecture: "armv7l", CpuCount: 2, MemoryBytes: 1 << 30, Systemd: true, PanelReachable: true}, "unsupported_architecture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, code := supportedPreflight(tc.facts); code != tc.code {
				t.Fatalf("supportedPreflight code = %q, want %q", code, tc.code)
			}
		})
	}
}

func TestShellQuoteAndPublicErrorsDoNotExposeInput(t *testing.T) {
	if got, want := shellQuote("a'b"), "'a'\\''b'"; got != want {
		t.Fatalf("shellQuote = %q, want %q", got, want)
	}
	secret := "ssh:password=never-log-this"
	if got := publicSSHCode(errors.New(secret)); got != "ssh_connection_failed" || strings.Contains(got, secret) {
		t.Fatalf("public SSH error = %q", got)
	}
}

func TestSSHFailureCodesDistinguishAuthenticationAndNetwork(t *testing.T) {
	if got := publicSSHCode(ssh.ServerAuthError{Errors: []error{errors.New("private detail")}}); got != "ssh_authentication_failed" {
		t.Fatalf("authentication failure code = %q", got)
	}
	if got := publicSSHCode(context.DeadlineExceeded); got != "ssh_connection_timeout" {
		t.Fatalf("timeout failure code = %q", got)
	}
	if got := connect.CodeOf(sshConnectError(errors.New("connection refused"))); got != connect.CodeUnavailable {
		t.Fatalf("connection error Connect code = %v", got)
	}
	if got := connect.CodeOf(sshConnectError(ssh.ServerAuthError{Errors: []error{errors.New("private detail")}})); got != connect.CodeUnauthenticated {
		t.Fatalf("authentication error Connect code = %v", got)
	}
	if got := connect.CodeOf(preflightConnectError(context.DeadlineExceeded)); got != connect.CodeDeadlineExceeded {
		t.Fatalf("preflight timeout Connect code = %v", got)
	}
	if got := publicPreflightCode(context.DeadlineExceeded); got != "ssh_preflight_timeout" {
		t.Fatalf("preflight timeout job code = %q", got)
	}
}
