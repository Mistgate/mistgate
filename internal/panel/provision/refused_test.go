package provision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// A platform that refuses the connection (or cannot reach the target) is its own public code, whichever way the error
// is wrapped on its way up, and it stays apart from the codes of an ordinary failure.
func TestPlatformRefusalHasItsOwnPublicCode(t *testing.T) {
	wrapped := fmt.Errorf("provision: SSH connection failed: %w", fmt.Errorf("sshConnect: %w", ErrPlatformRefused))
	if got := publicSSHCode(wrapped); got != "ssh_target_refused" {
		t.Fatalf("publicSSHCode = %q, want ssh_target_refused", got)
	}
	var connectErr *connect.Error
	if !errors.As(sshConnectError(wrapped), &connectErr) || connectErr.Code() != connect.CodeFailedPrecondition || connectErr.Message() != "ssh_target_refused" {
		t.Fatalf("sshConnectError = %v: want failed_precondition carrying the code alone", sshConnectError(wrapped))
	}
	if got := publicSSHCode(errors.New("connection reset")); got != "ssh_connection_failed" {
		t.Fatalf("an ordinary failure = %q", got)
	}
}

// The client reports the refusal from both of its entry points, so the wizard and the job answer the same way.
func TestClientReportsAPlatformRefusal(t *testing.T) {
	ctx := context.Background()
	client := NewClientWith(
		func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("sshConnect: %w", ErrPlatformRefused)
		},
		func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no lookup in this test") },
	)
	target, err := NewTarget("45.67.89.11", 22)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Fingerprint(ctx, target); publicSSHCode(err) != "ssh_target_refused" {
		t.Fatalf("Fingerprint: %v (%q)", err, publicSSHCode(err))
	}
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	if _, err := client.DialAs(ctx, target, "root", "secret-pass", fingerprint); publicSSHCode(err) != "ssh_target_refused" {
		t.Fatalf("DialAs: %v (%q)", err, publicSSHCode(err))
	}
}

// The wizard's first step, reading the host key, answers the same code, not the generic "unavailable".
func TestGetSSHFingerprintNamesAPlatformRefusal(t *testing.T) {
	client := NewClientWith(
		func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("sshConnect: %w", ErrPlatformRefused)
		},
		func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no lookup in this test") },
	)
	svc := drainService(t, drainStore(t), Config{SSH: client})
	_, err := svc.GetSSHFingerprint(context.Background(), connect.NewRequest(&adminv1.GetSSHFingerprintRequest{Host: "45.67.89.11", Port: 22}))
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeFailedPrecondition || connectErr.Message() != "ssh_target_refused" {
		t.Fatalf("GetSSHFingerprint = %v", err)
	}
}
