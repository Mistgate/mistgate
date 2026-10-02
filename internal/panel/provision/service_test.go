package provision

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func TestCredentialOperationsRequireStepUpBeforeSSHOrPersistence(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	stepUpErr := connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	stepUpCalls, dialCalls := 0, 0
	ssh := NewClient()
	ssh.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	ssh.dialer = testDialer(func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("unexpected dial")
	})
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{}, SSH: ssh,
		StepUp: func(context.Context) error { stepUpCalls++; return stepUpErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	_, err = svc.CheckSSH(context.Background(), connect.NewRequest(&adminv1.CheckSSHRequest{
		Host: "node.example.com", Port: 22, Fingerprint: fingerprint, Password: "secret",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CheckSSH without step-up = %v", err)
	}
	_, err = svc.StartNodeProvision(context.Background(), connect.NewRequest(&adminv1.StartNodeProvisionRequest{
		ConfirmInstall: true, Name: "edge-1", Address: "edge.example.com", SshHost: "node.example.com",
		SshPort: 22, Fingerprint: fingerprint, Password: "secret",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("StartNodeProvision without step-up = %v", err)
	}
	if stepUpCalls != 2 || dialCalls != 0 {
		t.Fatalf("step-up calls = %d, SSH dials = %d", stepUpCalls, dialCalls)
	}
	jobs, err := st.NodeProvisionJobs(context.Background(), 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("unauthorized request persisted jobs: %+v, err %v", jobs, err)
	}
}

func TestSealedCredentialsAreBoundToJobAndNeverReturnedInJobView(t *testing.T) {
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{vault: vlt}
	secret := credentials{Password: "secret", EnrollmentToken: "one-time", CAFingerprint: "aa"}
	sealed, err := svc.sealCredentials("prv_one", secret)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := svc.openCredentials("prv_one", sealed)
	if err != nil || opened != secret {
		t.Fatalf("opened credentials = %+v, err %v", opened, err)
	}
	if _, err := svc.openCredentials("prv_two", sealed); err == nil {
		t.Fatal("credentials decrypted under another job id")
	}
	view := toProvisionJob(store.NodeProvisionJob{ID: "prv_one", Secret: sealed, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)})
	if view.Id != "prv_one" || view.Name != "" {
		t.Fatalf("public job mapping = %+v", view)
	}
}

type testNodeManager struct{}

func (testNodeManager) CreateProvisionEnrollment(context.Context, NodeSpec, string, time.Time, time.Time) (string, string, error) {
	return "token", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}
func (testNodeManager) ProvisionNodeState(context.Context, string) (string, error) {
	return "pending", nil
}
func (testNodeManager) ProvisionNodeConnected(string) bool { return false }

type testBinarySource struct{}

func (testBinarySource) OpenNodeBinary(string, string) (*os.File, int64, string, error) {
	return nil, 0, "", errors.New("unused")
}
