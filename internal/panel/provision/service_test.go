package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
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
	secret := credentials{Username: "root", Password: "secret", EnrollmentToken: "one-time", CAFingerprint: "aa"}
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

func TestRetryNodeProvisionTakesCancelledJobsButNotRetiredNodes(t *testing.T) {
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
	manager := &e2eNodeManager{}
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: manager, Binaries: testBinarySource{},
		StepUp: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := store.NodeProvisionJob{ID: "prv_retry_cancelled", NodeID: "nod_retry_cancelled", Name: "edge-retry", Address: "edge.example.com",
		SSHHost: "203.0.113.9", SSHPort: 22, HostFingerprint: "SHA256:pin", Secret: []byte("sealed"), CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	retry := func() error {
		_, err := svc.RetryNodeProvision(ctx, connect.NewRequest(&adminv1.RetryNodeProvisionRequest{JobId: job.ID, ConfirmInstall: true, Password: "secret"}))
		return err
	}
	if err := retry(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("retry of a queued job = %v", err)
	}
	if _, _, err := st.RequestCancelNodeProvisionJob(ctx, job.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := retry(); err != nil {
		t.Fatalf("retry of a cancelled job = %v", err)
	}
	if got, _ := st.NodeProvisionJob(ctx, job.ID); got.State != "queued" || len(got.Secret) == 0 {
		t.Fatalf("retried job = %+v", got)
	}
	if _, _, err := st.RequestCancelNodeProvisionJob(ctx, job.ID, now); err != nil {
		t.Fatal(err)
	}
	manager.state = "retired"
	if err := retry(); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "node_retired") {
		t.Fatalf("retry for a retired node = %v", err)
	}
}

// Retiring keeps the saved access: the panel no longer changes that server, the owner can still reveal the password,
// and only the owner's own step-up protected Forget deletes it.
func TestRetiredNodeAccessIsKeptUntilTheOwnerForgetsIt(t *testing.T) {
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
	const nodeID, password = "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", "0123456789abcdef"
	now := time.Unix(1_800_000_000, 0).UTC()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, 'edge-1', 'edge.example.com', 'active', ?)`, nodeID, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node_server_access (node_id, node_name, ssh_host, ssh_port, ssh_username, host_fingerprint, password, configured_at)
		VALUES (?, 'edge-1', 'node.example.com', 22, 'root', 'SHA256:test', ?, ?)`, nodeID, vlt.Seal([]byte(password), "node-access:"+nodeID), now.Unix()); err != nil {
		t.Fatal(err)
	}
	ssh := NewClient()
	ssh.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	ssh.dialer = testDialer(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no SSH for a retired node")
	})
	var stepUpErr error
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{}, SSH: ssh,
		StepUp: func(context.Context) error { return stepUpErr }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	forget := func() error {
		_, err := svc.ForgetNodeServerAccess(ctx, connect.NewRequest(&adminv1.ForgetNodeServerAccessRequest{NodeId: nodeID}))
		return err
	}
	if err := forget(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("forgetting a live node's access = %v", err)
	}
	if err := st.RetireNode(ctx, nodeID, now); err != nil {
		t.Fatal(err)
	}
	listed, err := svc.ListNodeServerAccess(ctx, connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
	if err != nil || len(listed.Msg.GetAccess()) != 1 || !listed.Msg.GetAccess()[0].GetNodeRetired() {
		t.Fatalf("access after retire = %v, err %v", listed, err)
	}
	revealed, err := svc.RevealNodeServerPassword(ctx, connect.NewRequest(&adminv1.RevealNodeServerPasswordRequest{NodeId: nodeID}))
	if err != nil || revealed.Msg.GetPassword() != password {
		t.Fatalf("reveal after retire: err %v", err)
	}
	if _, err := svc.RotateNodeServerPassword(ctx, connect.NewRequest(&adminv1.RotateNodeServerPasswordRequest{NodeId: nodeID, Generate: true, Confirm: true})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("rotation on a retired node = %v", err)
	}
	stepUpErr = connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	if err := forget(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forget without step-up = %v", err)
	}
	stepUpErr = nil
	if err := forget(); err != nil {
		t.Fatal(err)
	}
	if err := forget(); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("forget twice = %v", err)
	}
}

// The MCP apply of node_install carries no password: the panel takes, once, the one the owner entered when approving
// that plan, and only with the host key the owner confirmed there. The call goes through the real token middleware.
func TestStartNodeProvisionTakesTheOwnersPasswordFromTheApprovedPlan(t *testing.T) {
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
	adminAuth, err := auth.New(st, auth.Config{RPID: "localhost", Origins: []string{"http://localhost"}, Vault: vlt}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	const ownerID = "adm_owner"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, 'Owner', ?, ?, ?)`,
		ownerID, store.RoleOwner, []byte{1}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	secret := auth.NewTokenSecret()
	hash := sha256.Sum256([]byte(secret))
	tok := store.APIToken{ID: store.NewID("tok_"), Name: "agent", Profile: store.ProfileAdmin, Hint: secret[len(secret)-4:], RatePerMin: 600,
		CreatedBy: ownerID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if err := st.CreateAPIToken(ctx, tok, hash[:]); err != nil {
		t.Fatal(err)
	}
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	// an approved node_install plan whose apply has begun, with what the owner entered sealed to it
	approvedPlan := func(confirmed string) string {
		confirm := sha256.Sum256([]byte(store.NewID("cf_")))
		p := store.MCPPlan{TokenID: tok.ID, Tool: auth.NodeInstallTool, ParamsJSON: `{}`, ConfirmHash: confirm[:], FactsJSON: `[]`,
			Summary: "s", Danger: `["fleet"]`, NeedsApproval: true, Status: store.PlanAwaiting, CreatedAt: now}
		if err := st.CreateMCPPlan(ctx, p, 20, 50); err != nil {
			t.Fatal(err)
		}
		p, err := st.MCPPlanByConfirm(ctx, tok.ID, confirm[:])
		if err != nil {
			t.Fatal(err)
		}
		sealed := auth.SealNodeInstallSecret(vlt, p.ID, auth.NodeInstallSecret{Password: "owner-typed-secret", Fingerprint: confirmed})
		if _, err := st.ApproveMCPPlanWithSecret(ctx, p.ID, ownerID, sealed, now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.BeginApply(ctx, p.ID, tok.ID, p.Tool, p.ParamsHash, p.ConfirmHash, now); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{},
		StepUp: adminAuth.RequireStepUp,
	})
	if err != nil {
		t.Fatal(err)
	}
	// start is the MCP layer's in-process call: the token, the MCP channel and the plan's approved grant
	start := func(planID, name string) (*adminv1.NodeProvisionJob, error) {
		var job *adminv1.NodeProvisionJob
		var callErr error
		reached := false
		h := adminAuth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			resp, err := svc.StartNodeProvision(r.Context(), connect.NewRequest(&adminv1.StartNodeProvisionRequest{
				ConfirmInstall: true, Name: name, Address: "edge.example.com", SshHost: "203.0.113.30", SshPort: 22,
				Fingerprint: fingerprint, PlanId: planID,
			}))
			if err == nil {
				job = resp.Msg.GetJob()
			}
			callErr = err
		}))
		callCtx := adminAuth.WithApprovedStepUp(auth.WithChannel(ctx, auth.ChannelMCP), planID)
		r := httptest.NewRequest(http.MethodPost, adminv1connect.ProvisioningServiceStartNodeProvisionProcedure, nil).WithContext(callCtx)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !reached {
			t.Fatalf("the MCP call was refused at the door: %d %s", w.Code, w.Body.String())
		}
		return job, callErr
	}

	planID := approvedPlan(fingerprint)
	job, err := start(planID, "edge-1")
	if err != nil {
		t.Fatalf("install from the approved plan: %v", err)
	}
	stored, err := st.NodeProvisionJob(ctx, job.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if creds, err := svc.openCredentials(stored.ID, stored.Secret); err != nil || creds.Password != "owner-typed-secret" {
		t.Fatalf("job credentials: err %v", err)
	}
	if _, err := start(planID, "edge-2"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "owner_password_missing") {
		t.Fatalf("a second install from one approval = %v", err)
	}
	other := approvedPlan("SHA256:" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if _, err := start(other, "edge-3"); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "host_key_not_confirmed_by_owner") {
		t.Fatalf("install with a host key the owner did not confirm = %v", err)
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

// The install form's location and provider take 100 characters (its maxlength), Cyrillic included.
func TestPlainTextCountsCharacters(t *testing.T) {
	if !validPlainText(strings.Repeat("я", 100), 100) || validPlainText(strings.Repeat("я", 101), 100) || validPlainText("bad\xff", 100) {
		t.Fatal("validPlainText must count characters, not bytes, and refuse invalid UTF-8")
	}
}
