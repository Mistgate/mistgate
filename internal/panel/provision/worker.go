package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/panel/store"
	"golang.org/x/crypto/ssh"
)

const remotePreflightScript = `set -eu
if [ "$(id -u)" -ne 0 ]; then exit 10; fi
if [ ! -r /etc/os-release ]; then exit 11; fi
. /etc/os-release
cpu_count="$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf 0)"
memory_kb="$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo)"
disk_kb="$(df -Pk /root | awk 'NR == 2 {print $4}')"
if [ -f /var/lib/mistgate-node/identity.pem ]; then
  enrolled=yes
  node_id="$(sed -n 's/.*"node_id":"\([^"]*\)".*/\1/p' /var/lib/mistgate-node/agent.json 2>/dev/null || true)"
else
  enrolled=no
  node_id=""
fi
if [ -x /bin/systemctl ] || [ -x /usr/bin/systemctl ]; then systemd=yes; else systemd=no; fi
printf 'distribution=%s\n' "${ID:-unknown}"
printf 'version=%s\n' "${VERSION_ID:-unknown}"
printf 'kernel=%s\n' "$(uname -r)"
printf 'architecture=%s\n' "$(uname -m)"
printf 'cpu_count=%s\n' "$cpu_count"
printf 'memory_bytes=%s\n' "$((memory_kb * 1024))"
printf 'disk_available_bytes=%s\n' "$((disk_kb * 1024))"
printf 'systemd=%s\n' "$systemd"
printf 'already_enrolled=%s\n' "$enrolled"
printf 'existing_node_id=%s\n' "$node_id"
`

// remoteFirewallPreparationScript adjusts only an already-active host firewall.
// Provider firewalls cannot be reached through this SSH connection, and an
// inactive firewall is deliberately left inactive.
//
// UFW: 80/tcp, 443/tcp and 443/udp carry hostctl.ProvisionUFWTag so the agent removes them when the node is retired
// (hostctl.Cleanup); a rule the owner already had is left alone and untagged. The SSH rule is never tagged: removing
// it could lock the owner out. firewalld: each port goes in at runtime and permanently, without --reload, which would
// drop runtime-only rules such as fail2ban bans and Docker chains.
const remoteFirewallPreparationScript = `set -eu
ssh_port="${1:-}"
case "$ssh_port" in
  ''|*[!0-9]*) exit 10 ;;
esac
if [ "$ssh_port" -lt 1 ] || [ "$ssh_port" -gt 65535 ]; then exit 10; fi

if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw allow "$ssh_port/tcp" >/dev/null
  for rule in 80/tcp 443/tcp 443/udp; do
    if ufw show added 2>/dev/null | grep -qx "ufw allow $rule"; then continue; fi
    ufw allow "$rule" comment ` + hostctl.ProvisionUFWTag + ` >/dev/null
  done
fi

if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  zones="$(firewall-cmd --get-active-zones | awk '/^[^[:space:]]/ { print $1 }')"
  if [ -z "$zones" ]; then zones="$(firewall-cmd --get-default-zone)"; fi
  printf '%s\n' "$zones" | while IFS= read -r zone; do
    [ -n "$zone" ] || continue
    for rule in "$ssh_port/tcp" 80/tcp 443/tcp 443/udp; do
      firewall-cmd --zone="$zone" --add-port="$rule" >/dev/null
      firewall-cmd --zone="$zone" --add-port="$rule" --permanent >/dev/null
    done
  done
fi
`

type preflightFailure string

func (e preflightFailure) Error() string { return string(e) }

type remotePreflight struct {
	facts  *adminv1.NodePreflight
	nodeID string
}

var nodeIDPattern = regexp.MustCompile(`^nod_[a-z2-7]{26}$`)

// Run recovers interrupted jobs and executes queued installations serially.
func (s *Service) Run(ctx context.Context) error {
	if err := s.st.RequeueNodeProvisionJobs(ctx, s.cfg.Now()); err != nil {
		return fmt.Errorf("provision: recover jobs: %w", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		// Keep the claim and active-cancel registration in one critical section.
		// A cancellation request either wins before the claim or sees the cancel
		// function after the row becomes running.
		s.cancelMu.Lock()
		job, ok, err := s.st.ClaimNodeProvisionJob(ctx, s.cfg.Now())
		var jobCtx context.Context
		var cancel context.CancelFunc
		if ok {
			jobCtx, cancel = context.WithCancel(ctx)
			s.active[job.ID] = cancel
		}
		s.cancelMu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("provision: claim job: %w", err)
		}
		if ok {
			stopWatcher, watcherDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(watcherDone)
				s.watchWorkerCancellation(jobCtx, job.ID, stopWatcher, cancel)
			}()
			s.runJob(jobCtx, job)
			close(stopWatcher)
			<-watcherDone
			cancel()
			s.cancelMu.Lock()
			delete(s.active, job.ID)
			s.cancelMu.Unlock()
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.work:
		case <-ticker.C:
		}
	}
}

func (s *Service) runJob(ctx context.Context, job store.NodeProvisionJob) {
	defer s.finishCancellation(job.ID)
	if !s.mayContinue(ctx, &job) {
		return
	}
	secret, err := s.openCredentials(job.ID, job.Secret)
	if err != nil {
		s.failJob(ctx, job, "credentials_unavailable")
		return
	}
	defer func() {
		secret.Password = ""
		secret.EnrollmentToken = ""
	}()
	target, err := NewTarget(job.SSHHost, uint32(job.SSHPort))
	if err != nil {
		s.failJob(ctx, job, "ssh_target_invalid")
		return
	}
	conn, err := s.ssh.DialAs(ctx, target, secret.Username, secret.Password, job.HostFingerprint)
	if err != nil {
		if ctx.Err() == nil {
			s.failJob(ctx, job, publicSSHCode(err))
		}
		return
	}
	defer conn.Close()

	if err := s.setPhase(ctx, &job, "preflight", "checking_host", secret); err != nil {
		return
	}
	remote, err := readPreflight(ctx, conn, s.cfg.PanelAddr)
	if err != nil {
		if ctx.Err() == nil {
			s.failJob(ctx, job, publicPreflightCode(err))
		}
		return
	}
	if remote.facts.AlreadyEnrolled && remote.nodeID != job.NodeID {
		s.failJob(ctx, job, "node_identity_mismatch")
		return
	}
	if !remote.facts.AlreadyEnrolled && remote.nodeID != "" {
		s.failJob(ctx, job, "node_identity_unreadable")
		return
	}
	arch, code := supportedPreflight(remote.facts)
	if code != "" {
		s.failJob(ctx, job, code)
		return
	}

	binary, size, digest, err := s.cfg.Binaries.OpenNodeBinary("linux", arch)
	if err != nil || binary == nil {
		s.failJob(ctx, job, "agent_bundle_unavailable")
		return
	}
	defer binary.Close()
	if size < 1 || size > maxBinaryBytes || remote.facts.DiskAvailableBytes < uint64(size)+(64<<20) {
		s.failJob(ctx, job, "insufficient_disk_space")
		return
	}
	nodeState, _, stateErr := s.cfg.Nodes.ProvisionNodeState(ctx, job.NodeID)
	if stateErr != nil && !errors.Is(stateErr, store.ErrNotFound) {
		s.failJob(ctx, job, "node_state_unavailable")
		return
	}
	if errors.Is(stateErr, store.ErrNotFound) && remote.facts.AlreadyEnrolled {
		s.failJob(ctx, job, "node_state_unavailable")
		return
	}
	if nodeState == "retired" {
		s.failJob(ctx, job, "node_retired")
		return
	}

	if err := s.setPhase(ctx, &job, "firewall", "preparing_host_firewall", secret); err != nil {
		return
	}
	if !s.mayContinue(ctx, &job) {
		return
	}
	if err := prepareHostFirewall(ctx, conn, job.SSHPort); err != nil {
		if ctx.Err() == nil {
			s.failJob(ctx, job, "host_firewall_configuration_failed")
		}
		return
	}

	if !remote.facts.AlreadyEnrolled {
		// A used token is replayable only briefly. If the panel recorded enrollment but the
		// node did not finish writing its identity, rotate the token and retry with the same
		// persisted CSR key.
		if nodeState == "active" || secret.EnrollmentToken == "" || secret.TokenExpiresUnix <= s.cfg.Now().Unix() || secret.CAFingerprint == "" {
			spec := NodeSpec{ID: job.NodeID, Name: job.Name, Address: job.Address, CountryCode: job.CountryCode, Location: job.Location, Provider: job.Provider}
			now := s.cfg.Now().UTC()
			expires := now.Add(defaultEnrollmentTTL)
			token, caFingerprint, err := s.cfg.Nodes.CreateProvisionEnrollment(ctx, spec, job.CreatedBy, now, expires)
			if err != nil {
				s.failJob(ctx, job, publicEnrollmentCode(err))
				return
			}
			secret.EnrollmentToken, secret.CAFingerprint = token, caFingerprint
			secret.TokenExpiresUnix = expires.Unix()
			if !validCAFingerprint(caFingerprint) {
				s.failJob(ctx, job, "panel_ca_unavailable")
				return
			}
			if err := s.persistSecret(ctx, job.ID, &job, secret); err != nil {
				s.failJob(ctx, job, "job_state_unavailable")
				return
			}
		}
	}

	if err := s.setPhase(ctx, &job, "transfer", "uploading_agent", secret); err != nil {
		return
	}
	if !s.mayContinue(ctx, &job) {
		return
	}
	if err := uploadBinary(ctx, conn, binary, size, digest); err != nil {
		if ctx.Err() == nil {
			s.failJob(ctx, job, "agent_transfer_failed")
		}
		return
	}
	if !s.mayContinue(ctx, &job) {
		return
	}
	if err := runSSH(ctx, conn, "install -o root -g root -m 0755 /root/mistgate-node.new /root/mistgate-node && rm -f /root/mistgate-node.new", nil, 20*time.Second); err != nil {
		if ctx.Err() == nil {
			s.failJob(ctx, job, "agent_install_failed")
		}
		return
	}

	if !remote.facts.AlreadyEnrolled {
		if err := s.setPhase(ctx, &job, "enrollment", "enrolling_node", secret); err != nil {
			return
		}
		if !s.mayContinue(ctx, &job) {
			return
		}
		if err := runEnrollment(ctx, conn, s.cfg.PanelAddr, s.cfg.AgentSNI, secret.CAFingerprint, secret.EnrollmentToken); err != nil {
			if ctx.Err() == nil {
				s.failJob(ctx, job, "node_enrollment_failed")
			}
			return
		}
	}

	if err := s.setPhase(ctx, &job, "install", "starting_agent", secret); err != nil {
		return
	}
	if !s.mayContinue(ctx, &job) {
		return
	}
	if err := runSSH(ctx, conn, "/root/mistgate-node install", nil, 45*time.Second); err != nil {
		if ctx.Err() == nil {
			s.failJob(ctx, job, "systemd_install_failed")
		}
		return
	}
	if err := s.setPhase(ctx, &job, "waiting_node", "waiting_for_agent", secret); err != nil {
		return
	}
	if err := s.waitOnline(ctx, job.NodeID); err != nil {
		if ctx.Err() == nil {
			if errors.Is(err, store.ErrNodeRetired) {
				s.failJob(ctx, job, "node_retired")
			} else {
				s.failJob(ctx, job, "node_not_connected")
			}
		}
		return
	}
	s.finishJob(ctx, job, secret)
}

func (s *Service) watchWorkerCancellation(ctx context.Context, jobID string, stop <-chan struct{}, cancel context.CancelFunc) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			state, err := s.st.NodeProvisionJobState(ctx, jobID)
			if errors.Is(err, store.ErrNotFound) {
				cancel()
				return
			}
			if err == nil && state != "running" {
				cancel()
				return
			}
		}
	}
}

// mayContinue checks persisted state immediately before a remote mutation. The watcher cancels commands
// already in flight; such a command can still have an unknown partial outcome on the remote host.
func (s *Service) mayContinue(ctx context.Context, job *store.NodeProvisionJob) bool {
	if ctx.Err() != nil {
		return false
	}
	state, err := s.st.NodeProvisionJobState(ctx, job.ID)
	if err == nil && state == "running" {
		return true
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.failJob(ctx, *job, "job_state_unavailable")
	}
	return false
}

func (s *Service) setPhase(ctx context.Context, job *store.NodeProvisionJob, phase, code string, secret credentials) error {
	sealed, err := s.sealCredentials(job.ID, secret)
	if err != nil {
		s.failJob(ctx, *job, "job_state_unavailable")
		return err
	}
	now := s.cfg.Now().UTC()
	if err := s.st.UpdateRunningNodeProvisionJobWithEvent(ctx, job.ID, "running", phase, "", sealed, code, now); err != nil {
		s.failJob(ctx, *job, "job_state_unavailable")
		return err
	}
	job.Phase, job.UpdatedAt, job.Secret = phase, now, sealed
	return nil
}

func (s *Service) persistSecret(ctx context.Context, id string, job *store.NodeProvisionJob, secret credentials) error {
	sealed, err := s.sealCredentials(id, secret)
	if err != nil {
		return err
	}
	if err := s.st.UpdateRunningNodeProvisionJob(ctx, id, "running", job.Phase, "", sealed, s.cfg.Now().UTC()); err != nil {
		return err
	}
	job.Secret = sealed
	return nil
}

func (s *Service) failJob(ctx context.Context, job store.NodeProvisionJob, code string) {
	if ctx.Err() != nil {
		return
	}
	now := s.cfg.Now().UTC()
	if err := s.st.UpdateRunningNodeProvisionJobWithEvent(ctx, job.ID, "failed", "failed", code, []byte{}, code, now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return // A cancellation or another terminal transition won the race.
		}
		s.cfg.Log.Error("mark node provisioning failed", "job_id", job.ID, "error_code", code, "err", err)
		if fallbackErr := s.st.UpdateRunningNodeProvisionJob(ctx, job.ID, "failed", "failed", code, []byte{}, now); fallbackErr != nil {
			s.cfg.Log.Error("clear failed node provisioning job secret", "job_id", job.ID, "err", fallbackErr)
		}
		return
	}
	s.cfg.Log.Warn("node provisioning failed", "job_id", job.ID, "error_code", code)
}

func (s *Service) finishJob(ctx context.Context, job store.NodeProvisionJob, secret credentials) {
	now := s.cfg.Now().UTC()
	plain := []byte(secret.Password)
	ciphertext := s.vault.Seal(plain, "node-access:"+job.NodeID)
	clearBytes(plain)
	access := store.NodeServerAccess{
		NodeID: job.NodeID, NodeName: job.Name, SSHHost: job.SSHHost, SSHPort: job.SSHPort,
		SSHUser: secret.Username, HostFingerprint: job.HostFingerprint, Password: ciphertext,
	}
	if err := s.st.CompleteNodeProvisionJob(ctx, job.ID, access, now); err != nil {
		s.cfg.Log.Error("complete node provisioning job", "job_id", job.ID, "err", err)
		s.failJob(ctx, job, "job_state_unavailable")
		return
	}
	_ = s.auditAs(context.WithoutCancel(ctx), job.CreatedBy, "node.ssh_provision_complete", map[string]string{"job_id": job.ID, "node_id": job.NodeID, "name": job.Name})
	secret.Password, secret.EnrollmentToken = "", ""
}

func (s *Service) finishCancellation(jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changed, err := s.st.FinishCancelledNodeProvisionJob(ctx, jobID, s.cfg.Now().UTC())
	if err != nil {
		s.cfg.Log.Error("finish cancelled node provisioning job", "job_id", jobID, "err", err)
		return
	}
	if changed {
		s.cfg.Log.Warn("node provisioning cancelled; remote state may be partial", "job_id", jobID)
	}
}

func (s *Service) waitOnline(ctx context.Context, nodeID string) error {
	deadline := time.NewTimer(s.cfg.WaitOnline)
	defer deadline.Stop()
	ticker := time.NewTicker(s.cfg.RetryDelay)
	defer ticker.Stop()
	for {
		state, connected, err := s.cfg.Nodes.ProvisionNodeState(ctx, nodeID)
		if err != nil {
			return err
		}
		if state == "retired" {
			return store.ErrNodeRetired
		}
		if connected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("provision: timed out waiting for the node")
		case <-ticker.C:
		}
	}
}

func readPreflight(ctx context.Context, conn *Connection, panelAddr string) (*remotePreflight, error) {
	command, err := preflightCommand(panelAddr)
	if err != nil {
		return nil, preflightFailure("panel_address_not_configured")
	}
	output, err := runSSHCapture(ctx, conn, command, nil, 20*time.Second, 16<<10)
	if err != nil {
		return nil, err
	}
	return parsePreflightOutput(output)
}

func prepareHostFirewall(ctx context.Context, conn *Connection, sshPort uint16) error {
	if sshPort == 0 {
		return errors.New("provision: invalid SSH port for firewall preparation")
	}
	return runSSH(ctx, conn, "sh -s -- "+strconv.Itoa(int(sshPort)), strings.NewReader(remoteFirewallPreparationScript), 30*time.Second)
}

func parsePreflightOutput(output []byte) (*remotePreflight, error) {
	values := make(map[string]string, 12)
	allowed := map[string]struct{}{
		"distribution": {}, "version": {}, "kernel": {}, "architecture": {}, "cpu_count": {},
		"memory_bytes": {}, "disk_available_bytes": {}, "systemd": {}, "already_enrolled": {},
		"existing_node_id": {}, "panel_reachable": {},
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !ok || key == "" || len(value) > 256 {
			return nil, invalidPreflightFacts()
		}
		if _, ok := allowed[key]; !ok {
			return nil, invalidPreflightFacts()
		}
		if _, exists := values[key]; exists {
			return nil, invalidPreflightFacts()
		}
		values[key] = value
	}
	get := func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
	parseUint := func(key string, bits int) (uint64, error) {
		v, ok := get(key)
		if !ok || v == "" {
			return 0, invalidPreflightFacts()
		}
		return strconv.ParseUint(v, 10, bits)
	}
	distribution, ok := get("distribution")
	if !ok {
		return nil, invalidPreflightFacts()
	}
	version, ok := get("version")
	if !ok {
		return nil, invalidPreflightFacts()
	}
	kernel, ok := get("kernel")
	if !ok || kernel == "" || strings.ContainsAny(kernel, "\x00\r") {
		return nil, invalidPreflightFacts()
	}
	architecture, ok := get("architecture")
	if !ok {
		return nil, invalidPreflightFacts()
	}
	cpus, err := parseUint("cpu_count", 32)
	if err != nil {
		return nil, invalidPreflightFacts()
	}
	memory, err := parseUint("memory_bytes", 64)
	if err != nil {
		return nil, invalidPreflightFacts()
	}
	disk, err := parseUint("disk_available_bytes", 64)
	if err != nil {
		return nil, invalidPreflightFacts()
	}
	systemd, ok := get("systemd")
	if !ok || systemd != "yes" && systemd != "no" {
		return nil, invalidPreflightFacts()
	}
	enrolled, ok := get("already_enrolled")
	if !ok || enrolled != "yes" && enrolled != "no" {
		return nil, invalidPreflightFacts()
	}
	nodeID, ok := get("existing_node_id")
	if !ok {
		return nil, invalidPreflightFacts()
	}
	if enrolled == "yes" && !nodeIDPattern.MatchString(nodeID) || enrolled == "no" && nodeID != "" {
		return nil, preflightFailure("node_identity_unreadable")
	}
	panelReachable, ok := get("panel_reachable")
	if !ok || panelReachable != "yes" && panelReachable != "no" {
		return nil, invalidPreflightFacts()
	}
	if panelReachable != "yes" {
		return nil, preflightFailure("panel_unreachable")
	}
	return &remotePreflight{
		facts: &adminv1.NodePreflight{
			Distribution: distribution, Version: version, Kernel: kernel, Architecture: architecture,
			CpuCount: uint32(cpus), MemoryBytes: memory, DiskAvailableBytes: disk,
			Systemd: systemd == "yes", AlreadyEnrolled: enrolled == "yes", PanelReachable: panelReachable == "yes",
		},
		nodeID: nodeID,
	}, nil
}

func preflightCommand(panelAddr string) (string, error) {
	host, port, err := net.SplitHostPort(panelAddr)
	if err != nil || host == "" || port == "" {
		return "", errors.New("provision: invalid panel address")
	}
	// The TCP probe gets 5 s of the 20 s preflight budget: an unanswered SYN would otherwise hang for about two minutes
	// and the panel would report ssh_preflight_timeout instead of panel_unreachable.
	return remotePreflightScript + `
probe_timeout=""
if command -v timeout >/dev/null 2>&1; then probe_timeout="timeout 5"; fi
if command -v bash >/dev/null 2>&1 && $probe_timeout bash -c 'exec 3<>/dev/tcp/$1/$2' mistgate-probe ` + shellQuote(host) + ` ` + shellQuote(port) + ` >/dev/null 2>&1; then
  panel_reachable=yes
else
  panel_reachable=no
fi
printf 'panel_reachable=%s\n' "$panel_reachable"
`, nil
}

func supportedPreflight(facts *adminv1.NodePreflight) (string, string) {
	if facts == nil {
		return "", "ssh_preflight_failed"
	}
	if facts.Distribution == "ubuntu" {
		if !versionAtLeast(facts.Version, 22, 4) {
			return "", "unsupported_os_version"
		}
	} else if facts.Distribution == "debian" {
		if !versionAtLeast(facts.Version, 12, 0) {
			return "", "unsupported_os_version"
		}
	} else {
		return "", "unsupported_os"
	}
	if !facts.Systemd {
		return "", "systemd_required"
	}
	if facts.CpuCount == 0 || facts.MemoryBytes < 256<<20 {
		return "", "insufficient_resources"
	}
	if !facts.PanelReachable {
		return "", "panel_unreachable"
	}
	arch, ok := supportedArchitecture(facts.Architecture)
	if !ok {
		return "", "unsupported_architecture"
	}
	return arch, ""
}

func supportedArchitecture(raw string) (string, bool) {
	switch raw {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

func uploadBinary(ctx context.Context, conn *Connection, binary io.Reader, size int64, expected string) error {
	limited := &io.LimitedReader{R: binary, N: size + 1}
	digest := sha256.New()
	input := io.TeeReader(limited, digest)
	if err := runSSH(ctx, conn, "cat > /root/mistgate-node.new", input, 3*time.Minute); err != nil {
		return err
	}
	read := size + 1 - limited.N
	if read != size || hex.EncodeToString(digest.Sum(nil)) != expected {
		_ = runSSH(ctx, conn, "rm -f /root/mistgate-node.new", nil, 10*time.Second)
		return errors.New("provision: agent digest mismatch")
	}
	return nil
}

func runEnrollment(ctx context.Context, conn *Connection, panelAddr, sni, caFingerprint, token string) error {
	command := "/root/mistgate-node enroll --panel " + shellQuote(panelAddr) +
		" --sni " + shellQuote(sni) + " --ca-sha256 " + shellQuote(caFingerprint) + " --token-stdin --resume-key"
	return runSSH(ctx, conn, command, strings.NewReader(token+"\n"), 45*time.Second)
}

func runSSH(ctx context.Context, conn *Connection, command string, input io.Reader, timeout time.Duration) error {
	_, err := runSSHCapture(ctx, conn, command, input, timeout, 0)
	return err
}

func runSSHCapture(ctx context.Context, conn *Connection, command string, input io.Reader, timeout time.Duration, outputLimit int) ([]byte, error) {
	session, err := conn.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	session.Stdin = input
	var captured *limitedBuffer
	if outputLimit > 0 {
		captured = &limitedBuffer{limit: outputLimit}
		session.Stdout, session.Stderr = captured, captured
	} else {
		session.Stdout, session.Stderr = io.Discard, io.Discard
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(opCtx, func() { _ = session.Close() })
	err = session.Run(conn.PrivilegedCommand(command))
	stop()
	if opCtx.Err() != nil {
		return nil, opCtx.Err()
	}
	if err != nil {
		return nil, errors.New("provision: remote command failed")
	}
	if captured != nil {
		if captured.truncated {
			return nil, preflightFailure("preflight_output_invalid")
		}
		return captured.Bytes(), nil
	}
	return nil, nil
}

type limitedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(p) > remaining {
			b.buf = append(b.buf, p[:remaining]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, p...)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func publicSSHCode(err error) string {
	switch {
	case errors.Is(err, ErrUnsafeTarget), errors.Is(err, ErrNoAddress):
		return "ssh_target_not_public"
	case errors.Is(err, ErrHostKey):
		return "ssh_host_key_changed"
	case errors.Is(err, context.Canceled):
		return "ssh_connection_canceled"
	case isTimeoutError(err):
		return "ssh_connection_timeout"
	}
	var authErr ssh.ServerAuthError
	if errors.As(err, &authErr) {
		return "ssh_authentication_failed"
	}
	var authErrPtr *ssh.ServerAuthError
	if errors.As(err, &authErrPtr) && authErrPtr != nil {
		return "ssh_authentication_failed"
	}
	return "ssh_connection_failed"
}

func publicPreflightCode(err error) string {
	var code preflightFailure
	if errors.As(err, &code) {
		return string(code)
	}
	if errors.Is(err, context.Canceled) {
		return "ssh_preflight_canceled"
	}
	if isTimeoutError(err) {
		return "ssh_preflight_timeout"
	}
	return "ssh_preflight_failed"
}

func publicEnrollmentCode(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		return "node_name_taken"
	case errors.Is(err, store.ErrNodeRetired):
		return "node_retired"
	default:
		return "node_enrollment_unavailable"
	}
}

func validCAFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
