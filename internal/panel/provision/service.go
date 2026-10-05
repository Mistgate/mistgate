package provision

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	defaultEnrollmentTTL = time.Hour
	sshPasswordMaxBytes  = 1024
	maxBinaryBytes       = 256 << 20
)

var provisionNodeName = regexp.MustCompile(`^[a-z0-9-]{2,24}$`)

// NodeSpec is the node metadata reserved by one provisioning job.
type NodeSpec struct {
	ID, Name, Address, CountryCode, Location, Provider string
}

// NodeManager joins a durable installation job to the fleet CA and agent stream.
type NodeManager interface {
	CreateProvisionEnrollment(context.Context, NodeSpec, string, time.Time, time.Time) (token, caFingerprint string, err error)
	ProvisionNodeState(context.Context, string) (string, error)
	ProvisionNodeConnected(string) bool
}

// BinarySource returns a file from the currently trusted release bundle and the digest in its signed manifest.
type BinarySource interface {
	OpenNodeBinary(goos, goarch string) (*os.File, int64, string, error)
}

// Config contains the services needed for durable SSH provisioning.
type Config struct {
	StepUp     func(context.Context) error
	Nodes      NodeManager
	Binaries   BinarySource
	PanelAddr  string
	AgentSNI   string
	Log        *slog.Logger
	Now        func() time.Time
	SSH        *Client
	WaitOnline time.Duration
	RetryDelay time.Duration
}

// Service exposes the owner-only provisioning API and runs durable jobs.
type Service struct {
	st       *store.Store
	vault    *vault.Vault
	cfg      Config
	ssh      *Client
	work     chan struct{}
	accessMu sync.Mutex
	cancelMu sync.Mutex
	active   map[string]context.CancelFunc
}

type credentials struct {
	Username         string `json:"username,omitempty"`
	Password         string `json:"password"`
	EnrollmentToken  string `json:"enrollment_token,omitempty"`
	CAFingerprint    string `json:"ca_fingerprint,omitempty"`
	TokenExpiresUnix int64  `json:"token_expires_unix,omitempty"`
}

// NewService builds the provisioning API and its background worker.
func NewService(st *store.Store, vlt *vault.Vault, cfg Config) (*Service, error) {
	if st == nil || vlt == nil || cfg.Nodes == nil || cfg.Binaries == nil || cfg.StepUp == nil {
		return nil, errors.New("provision: store, vault, node manager, binary source and step-up are required")
	}
	if cfg.AgentSNI == "" {
		return nil, errors.New("provision: agent SNI is required")
	}
	if cfg.PanelAddr != "" {
		host, port, err := net.SplitHostPort(cfg.PanelAddr)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || portNumber < 1 || portNumber > 65535 || strings.ContainsRune(host, '\x00') {
			return nil, errors.New("provision: agent panel address must be host:port")
		}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.SSH == nil {
		cfg.SSH = NewClient()
	}
	if cfg.WaitOnline <= 0 {
		cfg.WaitOnline = 2 * time.Minute
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	return &Service{st: st, vault: vlt, cfg: cfg, ssh: cfg.SSH, work: make(chan struct{}, 1), active: make(map[string]context.CancelFunc)}, nil
}

// Handler mounts the owner-only ProvisioningService Connect handler.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	options := append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)
	return adminv1connect.NewProvisioningServiceHandler(s, options...)
}

// GetSSHFingerprint reads the remote SSH host key without sending authentication.
func (s *Service) GetSSHFingerprint(ctx context.Context, req *connect.Request[adminv1.GetSSHFingerprintRequest]) (*connect.Response[adminv1.GetSSHFingerprintResponse], error) {
	target, err := NewTarget(req.Msg.Host, req.Msg.Port)
	if err != nil {
		return nil, invalidArgument("invalid SSH target")
	}
	fingerprint, err := s.ssh.Fingerprint(ctx, target)
	if err != nil {
		if errors.Is(err, ErrUnsafeTarget) || errors.Is(err, ErrNoAddress) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ssh_target_not_public"))
		}
		if errors.Is(err, context.Canceled) {
			return nil, connect.NewError(connect.CodeCanceled, errors.New("ssh_fingerprint_canceled"))
		}
		if isTimeoutError(err) {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("ssh_fingerprint_timeout"))
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("ssh_fingerprint_unavailable"))
	}
	return connect.NewResponse(&adminv1.GetSSHFingerprintResponse{
		Host: target.Host(), Port: uint32(target.Port()), Fingerprint: fingerprint,
	}), nil
}

// CheckSSH authenticates only after the submitted fingerprint exactly matches the host key.
func (s *Service) CheckSSH(ctx context.Context, req *connect.Request[adminv1.CheckSSHRequest]) (*connect.Response[adminv1.CheckSSHResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if s.cfg.PanelAddr == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("panel_address_not_configured"))
	}
	target, err := NewTarget(req.Msg.Host, req.Msg.Port)
	username := sshUsername(req.Msg.Username)
	if err != nil || !validFingerprint(req.Msg.Fingerprint) || !validSSHUsername(username) || !validPassword(req.Msg.Password) {
		return nil, invalidArgument("invalid SSH credentials or target")
	}
	conn, err := s.ssh.DialAs(ctx, target, username, req.Msg.Password, req.Msg.Fingerprint)
	if err != nil {
		return nil, sshConnectError(err)
	}
	defer conn.Close()
	remote, err := readPreflight(ctx, conn, s.cfg.PanelAddr)
	if err != nil {
		return nil, preflightConnectError(err)
	}
	if _, code := supportedPreflight(remote.facts); code != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(code))
	}
	return connect.NewResponse(&adminv1.CheckSSHResponse{Preflight: remote.facts}), nil
}

// StartNodeProvision stores the SSH credential sealed to this job and queues installation.
func (s *Service) StartNodeProvision(ctx context.Context, req *connect.Request[adminv1.StartNodeProvisionRequest]) (*connect.Response[adminv1.StartNodeProvisionResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if !req.Msg.ConfirmInstall {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("confirm_install_required"))
	}
	if s.cfg.PanelAddr == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("panel_address_not_configured"))
	}
	target, err := NewTarget(req.Msg.SshHost, req.Msg.SshPort)
	username := sshUsername(req.Msg.SshUsername)
	if err != nil || !validFingerprint(req.Msg.Fingerprint) || !validSSHUsername(username) || !validPassword(req.Msg.Password) {
		return nil, invalidArgument("invalid SSH credentials or target")
	}
	name := strings.ToLower(req.Msg.Name)
	country := strings.ToUpper(req.Msg.CountryCode)
	if !provisionNodeName.MatchString(name) || !validNodeAddress(req.Msg.Address) || !validCountryCode(country) ||
		!validPlainText(req.Msg.Location, 100) || !validPlainText(req.Msg.Provider, 100) {
		return nil, invalidArgument("invalid node metadata")
	}
	admin, ok := auth.AdminFrom(ctx)
	if !ok || admin.ID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))
	}
	now := s.cfg.Now().UTC()
	job := store.NodeProvisionJob{
		ID: store.NewID("prv_"), NodeID: store.NewID("nod_"), Name: name, Address: req.Msg.Address,
		CountryCode: country, Location: req.Msg.Location, Provider: req.Msg.Provider,
		SSHHost: target.Host(), SSHPort: target.Port(), HostFingerprint: req.Msg.Fingerprint,
		CreatedBy: admin.ID, CreatedAt: now, UpdatedAt: now,
	}
	secret, err := s.sealCredentials(job.ID, credentials{Username: username, Password: req.Msg.Password})
	if err != nil {
		return nil, internalConnectError()
	}
	job.Secret = secret
	if err := s.st.CreateNodeProvisionJob(ctx, job); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("name_taken"))
		}
		s.cfg.Log.Error("save node provisioning job", "err", err)
		return nil, internalConnectError()
	}
	s.audit(ctx, "node.ssh_provision_start", map[string]string{"job_id": job.ID, "node_id": job.NodeID, "name": job.Name})
	s.signalWorker()
	return connect.NewResponse(&adminv1.StartNodeProvisionResponse{Job: toProvisionJob(job)}), nil
}

// RetryNodeProvision requeues a failed or cancelled job with freshly supplied SSH login credentials. A cancelled job
// may have changed the host already; the worker's resume path (preflight identity check, enroll --resume-key) picks
// up from there. A job whose node was retired cannot come back.
func (s *Service) RetryNodeProvision(ctx context.Context, req *connect.Request[adminv1.RetryNodeProvisionRequest]) (*connect.Response[adminv1.RetryNodeProvisionResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	username := sshUsername(req.Msg.SshUsername)
	if !req.Msg.ConfirmInstall || !validSSHUsername(username) || !validPassword(req.Msg.Password) {
		return nil, invalidArgument("confirmation and a valid SSH password are required")
	}
	job, err := s.st.NodeProvisionJob(ctx, req.Msg.JobId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("provision_job_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read node provisioning job", "err", err)
		return nil, internalConnectError()
	}
	notRetryable := connect.NewError(connect.CodeFailedPrecondition, errors.New("provision_job_not_retryable"))
	if job.State != "failed" && job.State != "cancelled" {
		return nil, notRetryable
	}
	if state, err := s.cfg.Nodes.ProvisionNodeState(ctx, job.NodeID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.cfg.Log.Error("read node state before provisioning retry", "job_id", job.ID, "err", err)
		return nil, internalConnectError()
	} else if state == "retired" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	}
	secret, err := s.sealCredentials(job.ID, credentials{Username: username, Password: req.Msg.Password})
	if err != nil {
		return nil, internalConnectError()
	}
	now := s.cfg.Now().UTC()
	switch err := s.st.RetryNodeProvisionJob(ctx, job.ID, job.State, secret, now); {
	case errors.Is(err, store.ErrConflict):
		return nil, notRetryable // a concurrent retry or cancellation won
	case errors.Is(err, store.ErrNameTaken):
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("name_taken"))
	case err != nil:
		s.cfg.Log.Error("retry node provisioning job", "job_id", job.ID, "err", err)
		return nil, internalConnectError()
	}
	job.State, job.Phase, job.ErrorCode, job.Secret, job.UpdatedAt = "queued", "queued", "", secret, now
	s.audit(ctx, "node.ssh_provision_retry", map[string]string{"job_id": job.ID, "node_id": job.NodeID})
	s.signalWorker()
	return connect.NewResponse(&adminv1.RetryNodeProvisionResponse{Job: toProvisionJob(job)}), nil
}

// CancelNodeProvision stops a queued install immediately or asks the active SSH
// worker to stop. An active cancellation is reported as uncertain because remote
// commands may already have changed the host.
func (s *Service) CancelNodeProvision(ctx context.Context, jobID string) (string, error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return "", err
	}
	jobID = strings.TrimSpace(jobID)
	if len(jobID) < 5 || len(jobID) > 64 {
		return "", invalidArgument("invalid provisioning job id")
	}
	job, err := s.st.NodeProvisionJob(ctx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		return "", connect.NewError(connect.CodeNotFound, errors.New("provision_job_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read node provisioning job before cancellation", "err", err)
		return "", internalConnectError()
	}
	state, changed, err := s.st.RequestCancelNodeProvisionJob(ctx, jobID, s.cfg.Now().UTC())
	if errors.Is(err, store.ErrConflict) {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("provision_job_not_cancellable"))
	}
	if err != nil {
		s.cfg.Log.Error("request node provisioning cancellation", "job_id", jobID, "err", err)
		return "", internalConnectError()
	}
	if state == "cancel_requested" {
		s.cancelWorker(jobID)
	}
	if changed {
		s.audit(ctx, "node.ssh_provision_cancel", map[string]string{
			"job_id": jobID, "node_id": job.NodeID, "state": state,
		})
	}
	return state, nil
}

func (s *Service) cancelWorker(jobID string) {
	s.cancelMu.Lock()
	cancel := s.active[jobID]
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// GetNodeProvision returns public job status; its encrypted secret is omitted.
func (s *Service) GetNodeProvision(ctx context.Context, req *connect.Request[adminv1.GetNodeProvisionRequest]) (*connect.Response[adminv1.GetNodeProvisionResponse], error) {
	job, err := s.st.NodeProvisionJob(ctx, req.Msg.JobId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("provision_job_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read node provisioning job", "err", err)
		return nil, internalConnectError()
	}
	return connect.NewResponse(&adminv1.GetNodeProvisionResponse{Job: toProvisionJob(job)}), nil
}

// ListNodeProvisions returns the most recently updated provisioning jobs.
func (s *Service) ListNodeProvisions(ctx context.Context, _ *connect.Request[adminv1.ListNodeProvisionsRequest]) (*connect.Response[adminv1.ListNodeProvisionsResponse], error) {
	jobs, err := s.st.NodeProvisionJobs(ctx, 100)
	if err != nil {
		s.cfg.Log.Error("list node provisioning jobs", "err", err)
		return nil, internalConnectError()
	}
	out := &adminv1.ListNodeProvisionsResponse{Jobs: make([]*adminv1.NodeProvisionJob, 0, len(jobs))}
	for _, job := range jobs {
		out.Jobs = append(out.Jobs, toProvisionJob(job))
	}
	return connect.NewResponse(out), nil
}

// ListNodeProvisionEvents returns only stable event codes, never remote output.
func (s *Service) ListNodeProvisionEvents(ctx context.Context, req *connect.Request[adminv1.ListNodeProvisionEventsRequest]) (*connect.Response[adminv1.ListNodeProvisionEventsResponse], error) {
	if _, err := s.st.NodeProvisionJob(ctx, req.Msg.JobId); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("provision_job_not_found"))
	} else if err != nil {
		s.cfg.Log.Error("read node provisioning job for events", "err", err)
		return nil, internalConnectError()
	}
	if req.Msg.AfterId > uint64(^uint64(0)>>1) {
		return nil, invalidArgument("event cursor is out of range")
	}
	limit := int(req.Msg.Limit)
	if limit == 0 {
		limit = 100
	}
	events, next, err := s.st.NodeProvisionEvents(ctx, req.Msg.JobId, int64(req.Msg.AfterId), limit)
	if err != nil {
		s.cfg.Log.Error("list node provisioning events", "err", err)
		return nil, internalConnectError()
	}
	out := &adminv1.ListNodeProvisionEventsResponse{Events: make([]*adminv1.NodeProvisionEvent, 0, len(events)), NextAfterId: uint64(next)}
	for _, event := range events {
		out.Events = append(out.Events, &adminv1.NodeProvisionEvent{Id: uint64(event.ID), Phase: event.Phase, Code: event.Code, CreatedUnix: event.CreatedAt.Unix()})
	}
	return connect.NewResponse(out), nil
}

func (s *Service) sealCredentials(jobID string, secret credentials) ([]byte, error) {
	plain, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	ciphertext := s.vault.Seal(plain, "node-provision:"+jobID)
	clearBytes(plain)
	return ciphertext, nil
}

func (s *Service) openCredentials(jobID string, ciphertext []byte) (credentials, error) {
	plain, err := s.vault.Open(ciphertext, "node-provision:"+jobID)
	if err != nil {
		return credentials{}, err
	}
	defer clearBytes(plain)
	var secret credentials
	if err := json.Unmarshal(plain, &secret); err != nil {
		return credentials{}, err
	}
	if secret.Username == "" { // Credentials sealed by the previous root-only wizard.
		secret.Username = "root"
	}
	if !validSSHUsername(secret.Username) || !validPassword(secret.Password) {
		return credentials{}, errors.New("provision: encrypted SSH password is invalid")
	}
	return secret, nil
}

func sshUsername(value string) string {
	if value == "" {
		return "root"
	}
	return value
}

// ListNodeServerAccess returns connection metadata only; encrypted credentials stay inside the panel.
func (s *Service) ListNodeServerAccess(ctx context.Context, _ *connect.Request[adminv1.ListNodeServerAccessRequest]) (*connect.Response[adminv1.ListNodeServerAccessResponse], error) {
	items, err := s.st.NodeServerAccesses(ctx)
	if err != nil {
		s.cfg.Log.Error("list node server access", "err", err)
		return nil, internalConnectError()
	}
	out := &adminv1.ListNodeServerAccessResponse{Access: make([]*adminv1.NodeServerAccess, 0, len(items))}
	for _, item := range items {
		out.Access = append(out.Access, nodeServerAccessView(item))
	}
	return connect.NewResponse(out), nil
}

// RotateNodeServerPassword changes the OS login password over the pinned SSH connection and verifies it
// with a fresh authentication before the panel promotes the encrypted credential. With generate the panel makes the
// password itself (the MCP rotation): it is never returned, the owner can reveal it after step-up.
func (s *Service) RotateNodeServerPassword(ctx context.Context, req *connect.Request[adminv1.RotateNodeServerPasswordRequest]) (*connect.Response[adminv1.RotateNodeServerPasswordResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	newPassword := req.Msg.NewPassword
	if req.Msg.Generate && newPassword == "" {
		newPassword = rand.Text()
	}
	defer func() { newPassword = "" }()
	if !req.Msg.Confirm || req.Msg.Generate && req.Msg.NewPassword != "" || len(newPassword) < 12 || !validPassword(newPassword) ||
		len(req.Msg.NodeId) > 64 || req.Msg.NodeId == "" {
		return nil, invalidArgument("confirmation and a strong SSH password are required")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	access, err := s.st.NodeServerAccess(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_server_access_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read node server access", "err", err)
		return nil, internalConnectError()
	}
	if access.NodeRetired {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	}
	target, err := NewTarget(access.SSHHost, uint32(access.SSHPort))
	if err != nil {
		return nil, internalConnectError()
	}
	current, err := s.openAccessPassword(access.NodeID, access.Password)
	if err != nil {
		return nil, internalConnectError()
	}
	defer func() { current = "" }()
	if access.PendingPassword != nil {
		pending, openErr := s.openPendingPassword(access.NodeID, access.PendingPassword)
		if openErr != nil {
			return nil, internalConnectError()
		}
		defer func() { pending = "" }()
		if conn, dialErr := s.ssh.DialAs(ctx, target, access.SSHUser, pending, access.HostFingerprint); dialErr == nil {
			_ = conn.Close()
			promoted := s.sealAccessPassword(access.NodeID, pending)
			if err := s.st.CommitPendingNodeServerPassword(ctx, access.NodeID, promoted, false, s.cfg.Now().UTC()); err != nil {
				return nil, internalConnectError()
			}
			current, access.Password, access.PendingPassword = pending, access.PendingPassword, nil
		} else {
			conn, oldErr := s.ssh.DialAs(ctx, target, access.SSHUser, current, access.HostFingerprint)
			if oldErr != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_rotation_recovery_required"))
			}
			_ = conn.Close()
			if err := s.st.ClearPendingNodeServerPassword(ctx, access.NodeID); err != nil {
				return nil, internalConnectError()
			}
		}
	}
	if newPassword == current {
		return nil, invalidArgument("new password must differ from the current password")
	}
	conn, err := s.ssh.DialAs(ctx, target, access.SSHUser, current, access.HostFingerprint)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_authentication_failed"))
	}
	defer conn.Close()
	pendingPlain := []byte(newPassword)
	pendingCiphertext := s.vault.Seal(pendingPlain, "node-access-pending:"+access.NodeID)
	clearBytes(pendingPlain)
	if err := s.st.SetPendingNodeServerPassword(ctx, access.NodeID, pendingCiphertext, req.Msg.Generate, s.cfg.Now().UTC()); err != nil {
		return nil, internalConnectError()
	}
	if err := runSSH(ctx, conn, "chpasswd", strings.NewReader(access.SSHUser+":"+newPassword+"\n"), 20*time.Second); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("ssh_password_change_unverified"))
	}
	verified, err := s.ssh.DialAs(ctx, target, access.SSHUser, newPassword, access.HostFingerprint)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("ssh_password_change_unverified"))
	}
	_ = verified.Close()
	promoted := s.sealAccessPassword(access.NodeID, newPassword)
	if err := s.st.CommitPendingNodeServerPassword(ctx, access.NodeID, promoted, !req.Msg.Generate, s.cfg.Now().UTC()); err != nil {
		return nil, internalConnectError()
	}
	s.audit(ctx, "node.ssh_password_rotate", map[string]string{"node_id": access.NodeID, "generated": strconv.FormatBool(req.Msg.Generate)})
	return connect.NewResponse(&adminv1.RotateNodeServerPasswordResponse{Rotated: true}), nil
}

// ForgetNodeServerAccess deletes the saved access of a retired node, sealed password included. Retiring keeps it on
// purpose (the password may be one only the panel knows), so forgetting is the owner's own step-up protected decision.
func (s *Service) ForgetNodeServerAccess(ctx context.Context, req *connect.Request[adminv1.ForgetNodeServerAccessRequest]) (*connect.Response[adminv1.ForgetNodeServerAccessResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	nodeID := req.Msg.GetNodeId()
	if nodeID == "" || len(nodeID) > 64 {
		return nil, invalidArgument("invalid node id")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	switch err := s.st.ForgetNodeServerAccess(ctx, nodeID); {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_server_access_not_found"))
	case errors.Is(err, store.ErrConflict):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_not_retired"))
	case err != nil:
		s.cfg.Log.Error("forget node server access", "err", err)
		return nil, internalConnectError()
	}
	s.audit(ctx, "node.ssh_access_forget", map[string]string{"node_id": nodeID})
	return connect.NewResponse(&adminv1.ForgetNodeServerAccessResponse{}), nil
}

func nodeServerAccessView(item store.NodeServerAccess) *adminv1.NodeServerAccess {
	return &adminv1.NodeServerAccess{NodeId: item.NodeID, NodeName: item.NodeName, Host: item.SSHHost,
		Port: uint32(item.SSHPort), Username: item.SSHUser, Fingerprint: item.HostFingerprint,
		ConfiguredUnix: item.ConfiguredAt.Unix(), RotationPending: item.PendingPassword != nil,
		PasswordGenerated: item.PasswordGenerated, NodeRetired: item.NodeRetired}
}

func (s *Service) openAccessPassword(nodeID string, ciphertext []byte) (string, error) {
	plain, err := s.vault.Open(ciphertext, "node-access:"+nodeID)
	if err != nil {
		return "", err
	}
	defer clearBytes(plain)
	if !validPassword(string(plain)) {
		return "", errors.New("provision: encrypted SSH password is invalid")
	}
	return string(plain), nil
}

func (s *Service) sealAccessPassword(nodeID, password string) []byte {
	plain := []byte(password)
	ciphertext := s.vault.Seal(plain, "node-access:"+nodeID)
	clearBytes(plain)
	return ciphertext
}

func (s *Service) openPendingPassword(nodeID string, ciphertext []byte) (string, error) {
	plain, err := s.vault.Open(ciphertext, "node-access-pending:"+nodeID)
	if err != nil {
		return "", err
	}
	defer clearBytes(plain)
	if len(plain) < 12 || !validPassword(string(plain)) {
		return "", errors.New("provision: encrypted pending SSH password is invalid")
	}
	return string(plain), nil
}

// audit writes one provisioning event as the caller and returns storage errors so sensitive responses can fail closed.
func (s *Service) audit(ctx context.Context, action string, params map[string]string) error {
	admin, ok := auth.AdminFrom(ctx)
	if !ok {
		return nil
	}
	return s.auditAs(ctx, admin.ID, action, params)
}

// auditAs writes the event for actor: the background worker has no caller, so it names who started the job.
func (s *Service) auditAs(ctx context.Context, actor, action string, params map[string]string) error {
	b, _ := json.Marshal(params)
	if err := s.st.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: actor, Action: action, Params: string(b), Result: "ok"}); err != nil {
		s.cfg.Log.Warn("audit node provisioning", "action", action, "err", err)
		return err
	}
	return nil
}

func (s *Service) signalWorker() {
	select {
	case s.work <- struct{}{}:
	default:
	}
}

func toProvisionJob(job store.NodeProvisionJob) *adminv1.NodeProvisionJob {
	return &adminv1.NodeProvisionJob{
		Id: job.ID, NodeId: job.NodeID, Name: job.Name, SshHost: job.SSHHost, SshPort: uint32(job.SSHPort),
		State: job.State, Phase: job.Phase, ErrorCode: job.ErrorCode,
		CreatedUnix: job.CreatedAt.Unix(), UpdatedUnix: job.UpdatedAt.Unix(),
	}
}

func validPassword(password string) bool {
	return password != "" && len(password) <= sshPasswordMaxBytes && utf8.ValidString(password) && !strings.ContainsAny(password, "\x00\r\n")
}

func validCountryCode(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 2 {
		return false
	}
	return value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}

func validPlainText(value string, max int) bool {
	if len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validNodeAddress(value string) bool {
	if len(value) > 253 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00/\\@?#[]") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	target, err := NewTarget(value, 22)
	return err == nil && target.Host() == strings.ToLower(strings.TrimSuffix(value, "."))
}

func invalidArgument(message string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(message))
}

func internalConnectError() error {
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

func sshConnectError(err error) error {
	code := publicSSHCode(err)
	status := connect.CodeUnavailable
	switch code {
	case "ssh_target_not_public":
		status = connect.CodeInvalidArgument
	case "ssh_host_key_changed":
		status = connect.CodeFailedPrecondition
	case "ssh_connection_canceled":
		status = connect.CodeCanceled
	case "ssh_connection_timeout":
		status = connect.CodeDeadlineExceeded
	case "ssh_authentication_failed":
		status = connect.CodeUnauthenticated
	}
	return connect.NewError(status, errors.New(code))
}

func preflightConnectError(err error) error {
	var code preflightFailure
	if errors.As(err, &code) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(string(code)))
	}
	if errors.Is(err, context.Canceled) {
		return connect.NewError(connect.CodeCanceled, errors.New("ssh_preflight_canceled"))
	}
	if isTimeoutError(err) {
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("ssh_preflight_timeout"))
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_preflight_failed"))
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && networkErr.Timeout()
}

func clearBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

func versionAtLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) == 0 {
		return false
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor := 0
	if len(parts) > 1 {
		gotMinor, err = strconv.Atoi(parts[1])
		if err != nil {
			return false
		}
	}
	return gotMajor > major || gotMajor == major && gotMinor >= minor
}

func invalidPreflightFacts() error {
	return errors.New("provision: malformed remote preflight response")
}
