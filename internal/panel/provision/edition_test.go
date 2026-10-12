package provision

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The edge cannot reach a server over SSH: the wizard's calls and the installs are refused with the coded reason before
// anything is dialed, checked or stored. On the VPS the same calls go on to their usual checks (here: the step-up).
func TestEdgeRefusesSSHProvisioning(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	stepUps := 0
	stepUpErr := connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	build := func(edge bool) *Service {
		ssh := NewClient()
		ssh.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
		ssh.dialer = testDialer(func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no dialing in this test")
		})
		svc, err := NewService(st, vlt, Config{
			Edge: edge, SSH: ssh, PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{},
			StepUp: func(context.Context) error { stepUps++; return stepUpErr },
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	calls := map[string]func(*Service) error{
		"GetSSHFingerprint": func(s *Service) error {
			_, err := s.GetSSHFingerprint(ctx, connect.NewRequest(&adminv1.GetSSHFingerprintRequest{Host: "node.example.com", Port: 22}))
			return err
		},
		"CheckSSH": func(s *Service) error {
			_, err := s.CheckSSH(ctx, connect.NewRequest(&adminv1.CheckSSHRequest{Host: "node.example.com", Port: 22}))
			return err
		},
		"StartNodeProvision": func(s *Service) error {
			_, err := s.StartNodeProvision(ctx, connect.NewRequest(&adminv1.StartNodeProvisionRequest{ConfirmInstall: true, Name: "node-a"}))
			return err
		},
		"RetryNodeProvision": func(s *Service) error {
			_, err := s.RetryNodeProvision(ctx, connect.NewRequest(&adminv1.RetryNodeProvisionRequest{JobId: "prv_x", ConfirmInstall: true}))
			return err
		},
	}
	for name, call := range calls {
		err := call(build(true))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable_in_edition: feature=provision") {
			t.Errorf("edge %s = %v, want the coded refusal", name, err)
		}
	}
	if stepUps != 0 {
		t.Fatalf("a refused call reached the step-up %d times", stepUps)
	}
	for name, call := range calls {
		if err := call(build(false)); strings.Contains(errString(err), "unavailable_in_edition") {
			t.Errorf("VPS %s = %v, must not be refused as an edition limit", name, err)
		}
	}
	if jobs, err := st.NodeProvisionJobs(ctx, 100); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs = %+v, err %v", jobs, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
