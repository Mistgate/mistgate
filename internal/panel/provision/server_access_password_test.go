package provision

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

// An interrupted rotation and a server that cannot be reached: nothing can tell which login works, so the owner gets
// both candidates, labelled unverified, instead of nothing. The rotation stays pending, and a new rotation refuses.
func TestRevealWithAnUnreachableServerReturnsBothCandidatesUnverified(t *testing.T) {
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
	const nodeID, current, pending = "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", "current-password-1", "pending-password-2"
	now := time.Unix(1_800_000_000, 0).UTC()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, 'edge-1', 'edge.example.com', 'active', ?)`, nodeID, now.Unix()); err != nil {
		t.Fatal(err)
	}
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node_server_access (node_id, node_name, ssh_host, ssh_port, ssh_username, host_fingerprint, password, pending_password, configured_at)
		VALUES (?, 'edge-1', 'node.example.com', 22, 'root', ?, ?, ?, ?)`, nodeID, fingerprint,
		vlt.Seal([]byte(current), "node-access:"+nodeID), vlt.Seal([]byte(pending), "node-access-pending:"+nodeID), now.Unix()); err != nil {
		t.Fatal(err)
	}
	ssh := NewClient()
	ssh.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	ssh.dialer = testDialer(func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") })
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{}, SSH: ssh,
		StepUp: func(context.Context) error { return nil }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := svc.RevealNodeServerPassword(ctx, connect.NewRequest(&adminv1.RevealNodeServerPasswordRequest{NodeId: nodeID}))
	if err != nil {
		t.Fatalf("reveal with the server unreachable: %v", err)
	}
	if m := revealed.Msg; m.GetPassword() != current || m.GetPendingPassword() != pending || !m.GetUnverified() {
		t.Fatalf("reveal = current %t, pending %t, unverified %t", m.GetPassword() == current, m.GetPendingPassword() == pending, m.GetUnverified())
	}
	if access, err := st.NodeServerAccess(ctx, nodeID); err != nil || access.PendingPassword == nil {
		t.Fatalf("an undecided rotation was resolved: %+v, err %v", access, err)
	}
	_, err = svc.RotateNodeServerPassword(ctx, connect.NewRequest(&adminv1.RotateNodeServerPasswordRequest{NodeId: nodeID, Generate: true, Confirm: true}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "ssh_rotation_recovery_required") {
		t.Fatalf("rotation on top of an undecided one = %v", err)
	}
}

func TestRevealNodeServerPasswordRequiresStepUpAndDisablesCaching(t *testing.T) {
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
	const nodeID, password = "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", "0123456789abcdef0123456789abcdef"
	now := time.Unix(1_800_000_000, 0).UTC()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'pending', ?)`,
		nodeID, "edge-1", "edge.example.com", now.Unix()); err != nil {
		t.Fatal(err)
	}
	plain := []byte(password)
	ciphertext := vlt.Seal(plain, "node-access:"+nodeID)
	clearBytes(plain)
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node_server_access
		(node_id, node_name, ssh_host, ssh_port, ssh_username, host_fingerprint, password, configured_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, "edge-1", "node.example.com", 22, "root", "SHA256:test", ciphertext, now.Unix()); err != nil {
		t.Fatal(err)
	}

	stepErr := connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	ssh := NewClient()
	ssh.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	ssh.dialer = testDialer(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("SSH must not be used for a stable credential")
	})
	newService := func(stepUpErr error) *Service {
		svc, err := NewService(st, vlt, Config{
			PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{}, SSH: ssh,
			StepUp: func(context.Context) error { return stepUpErr }, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}

	unauthorized, err := newService(stepErr).RevealNodeServerPassword(ctx, connect.NewRequest(&adminv1.RevealNodeServerPasswordRequest{NodeId: nodeID}))
	if unauthorized != nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reveal without step-up = %v, err %v", unauthorized, err)
	}

	revealed, err := newService(nil).RevealNodeServerPassword(ctx, connect.NewRequest(&adminv1.RevealNodeServerPasswordRequest{NodeId: nodeID}))
	if err != nil || revealed == nil || revealed.Msg.GetPassword() != password {
		t.Fatalf("reveal failed: response_present=%t, err %v", revealed != nil, err)
	}
	if got := revealed.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	listed, err := newService(nil).ListNodeServerAccess(ctx, connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
	if err != nil || len(listed.Msg.GetAccess()) != 1 || listed.Msg.GetAccess()[0].GetNodeId() != nodeID {
		t.Fatalf("access list = %v, err %v", listed, err)
	}

	t.Run("audit write failure returns no credential", func(t *testing.T) {
		if _, err := st.W.ExecContext(ctx, `CREATE TRIGGER fail_reveal_audit BEFORE INSERT ON audit
			WHEN NEW.action = 'node.ssh_password_reveal'
			BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
			t.Fatal(err)
		}

		const adminID, sessionToken = "adm_reveal_audit", "reveal-audit-session"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at)
			VALUES (?, ?, ?, ?, ?)`, adminID, "Owner", store.RoleOwner, []byte{1}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		adminAuth, err := auth.New(st, auth.Config{RPID: "localhost", Origins: []string{"http://localhost"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		nowSession := time.Now().UTC()
		tokenHash := sha256.Sum256([]byte(sessionToken))
		if err := st.CreateSession(ctx, store.Session{
			TokenHash: tokenHash[:], AdminID: adminID, CreatedAt: nowSession, LastSeenAt: nowSession, ExpiresAt: nowSession.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}

		var auditFailureResponse *connect.Response[adminv1.RevealNodeServerPasswordResponse]
		var auditFailure error
		called := false
		protected := adminAuth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			auditFailureResponse, auditFailure = newService(nil).RevealNodeServerPassword(r.Context(), connect.NewRequest(
				&adminv1.RevealNodeServerPasswordRequest{NodeId: nodeID},
			))
		}))
		request := httptest.NewRequest(http.MethodPost, adminv1connect.ProvisioningServiceRevealNodeServerPasswordProcedure, nil)
		request.RemoteAddr = "203.0.113.9:12345"
		request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sessionToken})
		protected.ServeHTTP(httptest.NewRecorder(), request)
		if !called {
			t.Fatal("owner request did not reach password reveal")
		}
		if auditFailureResponse != nil || connect.CodeOf(auditFailure) != connect.CodeInternal {
			t.Fatalf("reveal with failed audit write returned_response=%t, code=%v; want no response and INTERNAL", auditFailureResponse != nil, connect.CodeOf(auditFailure))
		}
	})
}
