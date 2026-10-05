package provision

import (
	"context"
	"errors"
	"strconv"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// RevealNodeServerPassword is an owner-only, step-up protected panel operation. It is intentionally not
// registered as an MCP tool: the admin can retrieve the encrypted-at-rest credential in the panel, while
// an agent can only request a generated password rotation and receives its status.
func (s *Service) RevealNodeServerPassword(ctx context.Context, req *connect.Request[adminv1.RevealNodeServerPasswordRequest]) (*connect.Response[adminv1.RevealNodeServerPasswordResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" || len(nodeID) > 64 {
		return nil, invalidArgument("invalid node id")
	}

	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	access, err := s.st.NodeServerAccess(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_server_access_not_found"))
	}
	if err != nil {
		if s.cfg.Log != nil {
			s.cfg.Log.Error("read node server password for reveal", "err", err)
		}
		return nil, internalConnectError()
	}
	password, err := s.openAccessPassword(access.NodeID, access.Password)
	if err != nil {
		return nil, internalConnectError()
	}
	defer func() { password = "" }()

	var pending string
	if access.PendingPassword != nil {
		password, pending, err = s.resolvePendingAccessPassword(ctx, access, password)
		if err != nil {
			return nil, err
		}
	}
	defer func() { pending = "" }()
	if err := s.audit(ctx, "node.ssh_password_reveal", map[string]string{
		"node_id": access.NodeID, "unverified": strconv.FormatBool(pending != ""),
	}); err != nil {
		return nil, internalConnectError()
	}
	response := connect.NewResponse(&adminv1.RevealNodeServerPasswordResponse{Password: password, PendingPassword: pending, Unverified: pending != ""})
	response.Header().Set("Cache-Control", "no-store")
	return response, nil
}

// resolvePendingAccessPassword recovers an interrupted password change. It promotes the pending value when that
// credential authenticates, and drops it when the current one does (the change never landed). When neither login
// succeeds and the server did not reject both (it was unreachable, timed out, or showed another host key), nothing is
// decided: it returns the current password with the pending one, both unverified. Both rejected:
// ssh_rotation_recovery_required.
func (s *Service) resolvePendingAccessPassword(ctx context.Context, access store.NodeServerAccess, current string) (password, unverifiedPending string, err error) {
	target, err := NewTarget(access.SSHHost, uint32(access.SSHPort))
	if err != nil {
		return "", "", internalConnectError()
	}
	pending, err := s.openPendingPassword(access.NodeID, access.PendingPassword)
	if err != nil {
		return "", "", internalConnectError()
	}
	pendingErr := s.tryLogin(ctx, target, access, pending)
	if pendingErr == nil {
		promoted := s.sealAccessPassword(access.NodeID, pending)
		if err := s.st.CommitPendingNodeServerPassword(ctx, access.NodeID, promoted, false, s.cfg.Now().UTC()); err != nil {
			return "", "", internalConnectError()
		}
		return pending, "", nil
	}
	currentErr := s.tryLogin(ctx, target, access, current)
	if currentErr == nil {
		if err := s.st.ClearPendingNodeServerPassword(ctx, access.NodeID); err != nil {
			return "", "", internalConnectError()
		}
		return current, "", nil
	}
	if publicSSHCode(pendingErr) == "ssh_authentication_failed" && publicSSHCode(currentErr) == "ssh_authentication_failed" {
		return "", "", connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_rotation_recovery_required"))
	}
	return current, pending, nil
}

func (s *Service) tryLogin(ctx context.Context, target Target, access store.NodeServerAccess, password string) error {
	conn, err := s.ssh.DialAs(ctx, target, access.SSHUser, password, access.HostFingerprint)
	if err == nil {
		_ = conn.Close()
	}
	return err
}
