package provision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/ssh"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	subHostTestDomain = "sub.example.com"
	subHostTestSite   = "www.example.com"
	subHostTestPath   = "/AAAAAAAAAAAAAAAAAAAAAAAA"
	subHostTestPrefix = "/0123456789abcdef0123/"
	subHostTestJob    = "prv_host"
	subHostTestPin    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// fakeSubHost is the panel's subscription host as the install sees it.
type fakeSubHost struct {
	mu          sync.Mutex
	state       string
	pins        []string
	activated   []string // node ids
	deactivated []string
	deactErr    error
}

func (f *fakeSubHost) PrepareInstall(context.Context, string, string, string, bool) (string, error) {
	return subHostTestPath, nil
}

func (f *fakeSubHost) ActivateInstalled(_ context.Context, nodeID, domain, siteName, path, pin string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if domain != subHostTestDomain || siteName != subHostTestSite || path != subHostTestPath {
		return errors.New("wrong domain, site or path")
	}
	f.state = "on"
	f.activated = append(f.activated, nodeID)
	f.pins = append(f.pins, pin)
	return nil
}

func (f *fakeSubHost) DeactivateInstalled(_ context.Context, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deactivated = append(f.deactivated, nodeID)
	if f.deactErr != nil {
		return f.deactErr
	}
	f.state = ""
	return nil
}

func (f *fakeSubHost) calls() (activated, deactivated []string, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.activated), slices.Clone(f.deactivated), f.state
}

// hostNode is the server of a subscription host install: the panel is unreachable from it, and it enrols offline. Its
// pending key lives across connections like enroll-pending.pem: --offline-csr answers a CSR of the same key until
// --offline-finish succeeds.
type hostNode struct {
	*installNode
	pending      string
	csrs         int
	finish       struct{ command, stdin string }
	finishFails  int  // the next finishes that fail (an interrupted session)
	installFails bool // `mistgate-node install` fails
	neverOnline  bool // the agent installs but never connects (the panel's tunnel does not come up)
	onCSR        func()
	order        []string
}

func newHostNode(t *testing.T, manager *e2eNodeManager) *hostNode {
	t.Helper()
	n := &hostNode{installNode: &installNode{manager: manager, keep: func(n int) int { return n }}}
	n.installNode.fakeSSHD = newFakeSSHD(t, loginMethods{password: true}, n.execute)
	return n
}

func (n *hostNode) execute(command string, channel ssh.Channel) ([]byte, error) {
	switch {
	case strings.Contains(command, "panel_reachable=%s"):
		n.installNode.mu.Lock()
		n.cmds = append(n.cmds, command)
		n.installNode.mu.Unlock()
		return []byte(strings.Join([]string{
			"distribution=ubuntu", "version=22.04", "kernel=6.8.0", "architecture=x86_64",
			"cpu_count=4", "memory_bytes=1073741824", "disk_available_bytes=1073741824",
			"systemd=yes", "nftables=yes", "already_enrolled=no", "existing_node_id=", "panel_reachable=no", "",
		}, "\n")), nil
	case command == "/root/mistgate-node enroll --offline-csr":
		n.installNode.mu.Lock()
		n.order = append(n.order, "csr")
		if n.pending == "" {
			n.pending = "csr-of-key-" + string(rune('A'+n.csrs))
		}
		n.csrs++
		pending, hook := n.pending, n.onCSR
		n.installNode.mu.Unlock()
		if hook != nil {
			hook()
		}
		return []byte("csr=" + base64.StdEncoding.EncodeToString([]byte(pending)) + "\n"), nil
	case strings.HasPrefix(command, "/root/mistgate-node enroll --offline-finish "):
		data, err := io.ReadAll(channel)
		n.installNode.mu.Lock()
		defer n.installNode.mu.Unlock()
		n.order = append(n.order, "finish")
		n.finish.command, n.finish.stdin = command, string(data)
		if n.finishFails > 0 {
			n.finishFails--
			return nil, errors.New("connection lost")
		}
		n.pending = "" // the identity is written, enroll-pending.pem is gone
		return []byte("pin=" + subHostTestPin + "\n"), err
	case command == "/root/mistgate-node install" && n.installFails:
		return nil, errors.New("systemd refused")
	case command == "/root/mistgate-node install" && n.neverOnline:
		return nil, nil
	}
	return n.installNode.execute(command, channel)
}

func (n *hostNode) seedJob(t *testing.T, st *store.Store, svc *Service) {
	t.Helper()
	ctx := context.Background()
	sealed, err := svc.sealCredentials(subHostTestJob, credentials{Username: "root", Password: authTestPassword,
		SubHostDomain: subHostTestDomain, SubHostSiteName: subHostTestSite, SubHostPath: subHostTestPath})
	if err != nil {
		t.Fatal(err)
	}
	job := store.NodeProvisionJob{ID: subHostTestJob, NodeID: e2eSessionNodeID, Name: "host-a", Address: "203.0.113.10",
		SSHHost: "node.example.com", SSHPort: 22, HostFingerprint: n.fingerprint(), Secret: sealed, CreatedBy: "adm_test", CreatedAt: drainT0, UpdatedAt: drainT0}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'pending', ?)`,
		job.NodeID, job.Name, job.Address, drainT0.Unix()); err != nil {
		t.Fatal(err)
	}
}

func subHostService(t *testing.T, node *hostNode, manager *e2eNodeManager, sub *fakeSubHost) (*store.Store, *Service) {
	t.Helper()
	st := drainStore(t)
	rel := newDrainRelease(t, bytes.Repeat([]byte("signed-agent-"), 1000))
	svc := drainService(t, st, Config{SSH: node.client(), Nodes: manager, Binaries: rel.source(), WaitOnline: time.Second,
		RetryDelay: 10 * time.Millisecond, SubHost: sub, LinkPrefix: subHostTestPrefix})
	node.seedJob(t, st, svc)
	return st, svc
}

func drainOnce(t *testing.T, svc *Service) {
	t.Helper()
	if _, _, err := svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The first subscription host: no reachable panel needed, enrolment inside the session (CSR out, certificate in, the role
// and the link on stdin), then the role turns on with the site's pin the node printed.
func TestSubscriptionHostInstallEnrollsInTheSession(t *testing.T) {
	manager := &e2eNodeManager{}
	sub := &fakeSubHost{}
	node := newHostNode(t, manager)
	st, svc := subHostService(t, node, manager, sub)
	drainOnce(t, svc)

	job := jobState(t, st, subHostTestJob)
	if job.State != "completed" || len(job.Secret) != 0 {
		t.Fatalf("job = %q %q, secret %d bytes; events %v", job.State, job.ErrorCode, len(job.Secret), eventCodes(t, st, subHostTestJob))
	}
	node.installNode.mu.Lock()
	defer node.installNode.mu.Unlock()
	if !slices.Equal(node.order, []string{"csr", "finish"}) {
		t.Fatalf("enrolment steps %v, want csr then finish", node.order)
	}
	want := "/root/mistgate-node enroll --offline-finish --node-id '" + e2eSessionNodeID + "' --ca-sha256 '" + strings.Repeat("a", 64) + "'"
	if node.finish.command != want {
		t.Fatalf("finish command\n%q\nwant\n%q (the role and the link never in the command line)", node.finish.command, want)
	}
	line, rest, _ := strings.Cut(node.finish.stdin, "\n")
	var role offlineRole
	if err := json.Unmarshal([]byte(line), &role); err != nil {
		t.Fatalf("role line %q: %v", line, err)
	}
	if role.LinkURL != "wss://"+subHostTestDomain+subHostTestPrefix || !slices.Equal(role.SubHost.Names, []string{subHostTestDomain}) ||
		role.SubHost.Path != subHostTestPath || role.SubHost.SiteName != subHostTestSite {
		t.Fatalf("role line %+v", role)
	}
	if rest != string(e2eSessionCert)+string(e2eSessionCA) {
		t.Fatalf("finish stdin after the role %q, want the certificate, then the CA", rest)
	}
	if len(manager.sessionCSRs) != 1 || string(manager.sessionCSRs[0]) != "csr-of-key-A" {
		t.Fatalf("CSRs given to the fleet: %q", manager.sessionCSRs)
	}
	probed := false
	for _, c := range node.cmds {
		if strings.Contains(c, installNodeToken) || strings.Contains(c, authTestPassword) || strings.Contains(c, subHostTestPath) ||
			strings.Contains(c, subHostTestPrefix) {
			t.Fatalf("a secret is in the command line: %q", c)
		}
		if strings.Contains(c, "'"+subHostTestDomain+"'") && strings.Contains(c, "'443'") {
			probed = true
		}
	}
	if !probed {
		t.Fatal("the reachability probe does not look at the subscription domain")
	}
	activated, deactivated, state := sub.calls()
	if !slices.Equal(activated, []string{e2eSessionNodeID}) || len(deactivated) != 0 || state != "on" {
		t.Fatalf("activated %v deactivated %v state %q", activated, deactivated, state)
	}
	if sub.pins[0] != subHostTestPin {
		t.Fatalf("bootstrap pin %q, want the one the node printed", sub.pins[0])
	}
}

// A session that breaks between the CSR and the finish: the retry asks for a CSR again, the node answers one of the
// same pending key, the fleet issues it with a new token, and the install completes.
func TestSubscriptionHostSessionEnrollRetryReusesThePendingKey(t *testing.T) {
	manager := &e2eNodeManager{}
	sub := &fakeSubHost{}
	node := newHostNode(t, manager)
	node.finishFails = 1
	st, svc := subHostService(t, node, manager, sub)
	drainOnce(t, svc)

	job := jobState(t, st, subHostTestJob)
	if job.State != "failed" || job.ErrorCode != "node_enrollment_failed" || len(job.Secret) == 0 {
		t.Fatalf("job = %q %q, secret %d bytes", job.State, job.ErrorCode, len(job.Secret))
	}
	if activated, _, _ := sub.calls(); len(activated) != 0 {
		t.Fatal("activated after a failed enrolment")
	}
	if _, err := svc.openCredentials(job.ID, job.Secret); err == nil {
		t.Fatal("the failed job kept a login")
	}
	if _, err := svc.RetryNodeProvision(context.Background(), connect.NewRequest(&adminv1.RetryNodeProvisionRequest{
		JobId: subHostTestJob, ConfirmInstall: true, Password: authTestPassword, SshUsername: "root",
	})); err != nil {
		t.Fatal(err)
	}
	drainOnce(t, svc)

	job = jobState(t, st, subHostTestJob)
	if job.State != "completed" {
		t.Fatalf("retry = %q %q; events %v", job.State, job.ErrorCode, eventCodes(t, st, subHostTestJob))
	}
	node.installNode.mu.Lock()
	defer node.installNode.mu.Unlock()
	if !slices.Equal(node.order, []string{"csr", "finish", "csr", "finish"}) {
		t.Fatalf("enrolment steps %v", node.order)
	}
	if len(manager.sessionCSRs) != 2 || !bytes.Equal(manager.sessionCSRs[0], manager.sessionCSRs[1]) {
		t.Fatalf("the retry did not reuse the pending key: %q", manager.sessionCSRs)
	}
	if manager.enrollments != 2 {
		t.Fatalf("enrolment tokens issued %d, want a new one for the retry", manager.enrollments)
	}
	if activated, _, _ := sub.calls(); !slices.Equal(activated, []string{e2eSessionNodeID}) {
		t.Fatalf("activated %v", activated)
	}
}

// After the role is on, a job that does not complete turns it off again.
func TestSubscriptionHostDeactivatedWhenTheInstallFailsAfterActivation(t *testing.T) {
	manager := &e2eNodeManager{}
	sub := &fakeSubHost{}
	node := newHostNode(t, manager)
	node.installFails = true
	st, svc := subHostService(t, node, manager, sub)
	drainOnce(t, svc)

	if job := jobState(t, st, subHostTestJob); job.State != "failed" || job.ErrorCode != "systemd_install_failed" {
		t.Fatalf("job = %q %q", job.State, job.ErrorCode)
	}
	activated, deactivated, state := sub.calls()
	if !slices.Equal(activated, []string{e2eSessionNodeID}) || !slices.Equal(deactivated, []string{e2eSessionNodeID}) || state != "" {
		t.Fatalf("activated %v deactivated %v state %q", activated, deactivated, state)
	}
}

// The agent installs but the panel's tunnel never comes up: the job names that path, and the role goes off again.
func TestSubscriptionHostTunnelDown(t *testing.T) {
	manager := &e2eNodeManager{}
	sub := &fakeSubHost{}
	node := newHostNode(t, manager)
	node.neverOnline = true
	st, svc := subHostService(t, node, manager, sub)
	drainOnce(t, svc)

	if job := jobState(t, st, subHostTestJob); job.State != "failed" || job.ErrorCode != "subscription_host_tunnel_down" {
		t.Fatalf("job = %q %q", job.State, job.ErrorCode)
	}
	if activated, deactivated, _ := sub.calls(); len(activated) != 1 || len(deactivated) != 1 {
		t.Fatalf("activated %v deactivated %v", activated, deactivated)
	}
}

// Cancelling the job during the session enrolment leaves the role off and issues no certificate.
func TestSubscriptionHostCancelDuringSessionEnrollLeavesTheRoleOff(t *testing.T) {
	manager := &e2eNodeManager{}
	sub := &fakeSubHost{}
	node := newHostNode(t, manager)
	var st *store.Store
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	node.onCSR = func() {
		if _, _, err := st.RequestCancelNodeProvisionJob(context.Background(), subHostTestJob, drainT0); err != nil {
			t.Error(err)
		}
		select { // hold the command until the worker gives up on it
		case <-release:
		case <-time.After(3 * time.Second):
		}
	}
	st, svc := subHostService(t, node, manager, sub)
	drainOnce(t, svc)

	if job := jobState(t, st, subHostTestJob); job.State != "cancelled" {
		t.Fatalf("job = %q %q", job.State, job.ErrorCode)
	}
	if activated, _, state := sub.calls(); len(activated) != 0 || state != "" {
		t.Fatalf("activated %v state %q after a cancel", activated, state)
	}
	if len(manager.sessionCSRs) != 0 {
		t.Fatal("the fleet issued a certificate for a cancelled install")
	}
}

// While the panel has a subscription host, an ordinary node enrols over the link behind it and the probe dials it.
func TestSubscriptionHostOnOrdinaryInstallUsesTheLink(t *testing.T) {
	manager := &e2eNodeManager{}
	node := newInstallNode(t, manager, func(n int) int { return n })
	st := drainStore(t)
	rel := newDrainRelease(t, bytes.Repeat([]byte("signed-agent-"), 1000))
	link := "wss://" + subHostTestDomain + subHostTestPrefix
	svc := drainService(t, st, Config{SSH: node.client(), Nodes: manager, Binaries: rel.source(), WaitOnline: time.Second,
		SubHost: &fakeSubHost{state: "on"}, LinkPrefix: subHostTestPrefix, EnrollLinkURL: func() string { return link }})
	node.seedJob(t, st, svc)
	drainOnce(t, svc)

	if job := jobState(t, st, "prv_link"); job.State != "completed" {
		t.Fatalf("job = %q %q", job.State, job.ErrorCode)
	}
	want := "/root/mistgate-node enroll --link-url '" + link + "' --ca-sha256 '" + strings.Repeat("a", 64) + "' --token-stdin --resume-key"
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.enroll.command != want {
		t.Fatalf("enroll command %q, want %q", node.enroll.command, want)
	}
	probed := false
	for _, c := range node.cmds {
		if strings.Contains(c, "'"+subHostTestDomain+"'") && strings.Contains(c, "'443'") {
			probed = true
		}
	}
	if !probed {
		t.Fatal("the probe does not dial the subscription host's 443")
	}
}

// An edition without the subscription host refuses such an install.
func TestSubscriptionHostInstallUnavailableWithoutTheService(t *testing.T) {
	st := drainStore(t)
	svc := drainService(t, st, Config{LinkPrefix: subHostTestPrefix})
	_, err := svc.StartNodeProvision(context.Background(), connect.NewRequest(&adminv1.StartNodeProvisionRequest{
		ConfirmInstall: true, SubscriptionHostDomain: subHostTestDomain,
	}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("err = %v, want Unimplemented", err)
	}
}

// A panel behind NAT has no public address to give and the server cannot reach it: the check for the first subscription
// host still passes, probing the subdomain and reporting it unreachable.
func TestSubscriptionHostCheckSSHWithoutAPanelAddress(t *testing.T) {
	manager := &e2eNodeManager{}
	node := newHostNode(t, manager)
	st := drainStore(t)
	svc := drainService(t, st, Config{SSH: node.client(), Nodes: manager, SubHost: &fakeSubHost{}, LinkPrefix: subHostTestPrefix})
	svc.cfg.PanelAddr = "" // nothing configured, as on a fresh panel at home
	check := func(domain string) (*adminv1.CheckSSHResponse, error) {
		resp, err := svc.CheckSSH(context.Background(), connect.NewRequest(&adminv1.CheckSSHRequest{
			Host: "node.example.com", Port: 22, Fingerprint: node.fingerprint(), Username: "root", Password: authTestPassword,
			SubscriptionHostDomain: domain,
		}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	if _, err := check(""); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "panel_address_not_configured") {
		t.Fatalf("an ordinary check without a panel address = %v", err)
	}
	if _, err := check("not a domain"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a bad domain = %v", err)
	}
	resp, err := check(subHostTestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPreflight().GetPanelReachable() {
		t.Fatal("the preflight says reachable")
	}
	node.installNode.mu.Lock()
	defer node.installNode.mu.Unlock()
	if len(node.cmds) == 0 || !strings.Contains(node.cmds[len(node.cmds)-1], "'"+subHostTestDomain+"'") {
		t.Fatal("the probe did not look at the subdomain")
	}
}

// A host install the panel restarted under too often fails with job_interrupted, keeping its role (no login): the owner's
// retry is a subscription host install again.
func TestSubscriptionHostInterruptedJobStaysAHostInstall(t *testing.T) {
	manager := &e2eNodeManager{}
	sub := &fakeSubHost{}
	node := newHostNode(t, manager)
	st, svc := subHostService(t, node, manager, sub)
	if _, err := st.W.ExecContext(context.Background(), `UPDATE node_provision_job SET state = 'running', attempt = ? WHERE id = ?`,
		store.MaxProvisionAttempts, subHostTestJob); err != nil {
		t.Fatal(err)
	}
	drainOnce(t, svc) // recovers: the job fails as interrupted
	job := jobState(t, st, subHostTestJob)
	if job.State != "failed" || job.ErrorCode != "job_interrupted" {
		t.Fatalf("job = %q %q", job.State, job.ErrorCode)
	}
	if kept := svc.hostFields(job.ID, job.Secret); kept.SubHostDomain != subHostTestDomain || kept.SubHostPath != subHostTestPath ||
		kept.SubHostSiteName != subHostTestSite {
		t.Fatalf("kept %+v", kept)
	}
	if creds, err := svc.openCredentials(job.ID, job.Secret); err == nil && (creds.Password != "" || len(creds.PrivateKey) != 0) {
		t.Fatal("the interrupted job kept a login")
	}
	if _, err := svc.RetryNodeProvision(context.Background(), connect.NewRequest(&adminv1.RetryNodeProvisionRequest{
		JobId: subHostTestJob, ConfirmInstall: true, Password: authTestPassword, SshUsername: "root",
	})); err != nil {
		t.Fatal(err)
	}
	drainOnce(t, svc)
	if job := jobState(t, st, subHostTestJob); job.State != "completed" {
		t.Fatalf("retry = %q %q; events %v", job.State, job.ErrorCode, eventCodes(t, st, subHostTestJob))
	}
	if activated, _, _ := sub.calls(); !slices.Equal(activated, []string{e2eSessionNodeID}) {
		t.Fatalf("activated %v", activated)
	}
}

func TestParseOfflineOutput(t *testing.T) {
	if _, err := parseOfflineCSR([]byte("noise\n")); err == nil {
		t.Fatal("no csr line accepted")
	}
	if _, err := parseOfflineCSR([]byte("csr=!!!\n")); err == nil {
		t.Fatal("a bad csr line accepted")
	}
	if der, err := parseOfflineCSR([]byte("x\ncsr=" + base64.StdEncoding.EncodeToString([]byte("der")) + "\n")); err != nil || string(der) != "der" {
		t.Fatalf("csr = %q %v", der, err)
	}
	if pin := parseSitePin([]byte("pin=" + strings.ToUpper(subHostTestPin) + "\n")); pin != subHostTestPin {
		t.Fatalf("pin = %q", pin)
	}
	for _, bad := range []string{"", "pin=abc\n", "pin=" + strings.Repeat("z", 64) + "\n"} {
		if pin := parseSitePin([]byte(bad)); pin != "" {
			t.Fatalf("pin of %q = %q", bad, pin)
		}
	}
}
