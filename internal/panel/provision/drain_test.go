package provision

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"golang.org/x/crypto/ssh"
)

var drainT0 = time.Unix(1_800_000_000, 0).UTC()

func drainStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func drainService(t *testing.T, st *store.Store, cfg Config) *Service {
	t.Helper()
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Nodes == nil {
		cfg.Nodes = &e2eNodeManager{}
	}
	if cfg.Binaries == nil {
		cfg.Binaries = testBinarySource{}
	}
	if cfg.AgentSNI == "" {
		cfg.AgentSNI = "agent.example.com"
	}
	if cfg.PanelAddr == "" && cfg.LinkURL == "" {
		cfg.PanelAddr = "panel.example.com:443"
	}
	cfg.StepUp = func(context.Context) error { return nil }
	cfg.Now = func() time.Time { return drainT0 }
	svc, err := NewService(st, vlt, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// seedDrainJob stores a job whose sealed credentials cannot be opened, so running it ends at once with
// credentials_unavailable and the tests see the scheduling without any SSH.
func seedDrainJob(t *testing.T, st *store.Store, id, state string, attempt int, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	at := drainT0.Add(age)
	job := store.NodeProvisionJob{ID: id, NodeID: "nod_" + id, Name: strings.ReplaceAll(id, "_", "-"), Address: "198.51.100.5",
		SSHHost: "node.example.com", SSHPort: 22, HostFingerprint: "SHA256:pin", Secret: []byte("sealed"), CreatedBy: "adm_test",
		CreatedAt: at, UpdatedAt: at}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE node_provision_job SET state = ?, attempt = ? WHERE id = ?`, state, attempt, id); err != nil {
		t.Fatal(err)
	}
}

func jobState(t *testing.T, st *store.Store, id string) store.NodeProvisionJob {
	t.Helper()
	job, err := st.NodeProvisionJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func jobAttempt(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	var n int
	if err := st.R.QueryRowContext(context.Background(), `SELECT attempt FROM node_provision_job WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func eventCodes(t *testing.T, st *store.Store, id string) []string {
	t.Helper()
	events, _, err := st.NodeProvisionEvents(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, e := range events {
		codes = append(codes, e.Code)
	}
	return codes
}

func count(codes []string, code string) int {
	n := 0
	for _, c := range codes {
		if c == code {
			n++
		}
	}
	return n
}

func TestDrainIsBusyWhilePassIsRunning(t *testing.T) {
	st := drainStore(t)
	svc := drainService(t, st, Config{})
	seedDrainJob(t, st, "prv_busy", "queued", 0, 0)
	svc.drainMu.Lock()
	more, busy, err := svc.Drain(context.Background())
	svc.drainMu.Unlock()
	if err != nil || !busy || more {
		t.Fatalf("Drain while another pass runs = more %v busy %v err %v", more, busy, err)
	}
	if got := jobState(t, st, "prv_busy"); got.State != "queued" {
		t.Fatalf("a busy Drain touched the queue: %+v", got)
	}
}

func TestDrainRunsOneJobPerCallAndReportsMore(t *testing.T) {
	st := drainStore(t)
	svc := drainService(t, st, Config{})
	seedDrainJob(t, st, "prv_first", "queued", 0, 0)
	seedDrainJob(t, st, "prv_second", "queued", 0, time.Second)

	more, busy, err := svc.Drain(context.Background())
	if err != nil || busy || !more {
		t.Fatalf("first Drain = more %v busy %v err %v, want more", more, busy, err)
	}
	if first, second := jobState(t, st, "prv_first"), jobState(t, st, "prv_second"); first.State != "failed" || second.State != "queued" {
		t.Fatalf("after one pass: first %q, second %q; want the oldest run and the next left waiting", first.State, second.State)
	}
	more, busy, err = svc.Drain(context.Background())
	if err != nil || busy || more {
		t.Fatalf("second Drain = more %v busy %v err %v, want nothing more", more, busy, err)
	}
	if second := jobState(t, st, "prv_second"); second.State != "failed" || second.ErrorCode != "credentials_unavailable" {
		t.Fatalf("second job = %+v", second)
	}
	if more, busy, err = svc.Drain(context.Background()); err != nil || busy || more {
		t.Fatalf("Drain of an empty queue = more %v busy %v err %v", more, busy, err)
	}
}

// Like Run at its start, Drain recovers the jobs a dead process left running, but only on the first pass of a Service:
// after that a running job is this process's own.
func TestDrainRequeuesInterruptedJobsOnlyOnTheFirstPass(t *testing.T) {
	st := drainStore(t)
	svc := drainService(t, st, Config{})
	seedDrainJob(t, st, "prv_orphan", "running", 1, 0)
	if _, _, err := svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	orphan := jobState(t, st, "prv_orphan")
	if orphan.State != "failed" || count(eventCodes(t, st, "prv_orphan"), "resumed_after_restart") != 1 || jobAttempt(t, st, "prv_orphan") != 2 {
		t.Fatalf("recovered job = %+v, events %v, attempt %d: want requeued once, then claimed again",
			orphan, eventCodes(t, st, "prv_orphan"), jobAttempt(t, st, "prv_orphan"))
	}
	seedDrainJob(t, st, "prv_live", "running", 1, time.Second)
	if _, _, err := svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if live := jobState(t, st, "prv_live"); live.State != "running" || count(eventCodes(t, st, "prv_live"), "resumed_after_restart") != 0 {
		t.Fatalf("a later pass requeued a running job: %+v", live)
	}
}

func TestDrainFailsAJobInterruptedForTheThirdTime(t *testing.T) {
	st := drainStore(t)
	svc := drainService(t, st, Config{})
	seedDrainJob(t, st, "prv_looping", "running", store.MaxProvisionAttempts, 0)
	more, busy, err := svc.Drain(context.Background())
	if err != nil || busy || more {
		t.Fatalf("Drain = more %v busy %v err %v", more, busy, err)
	}
	job := jobState(t, st, "prv_looping")
	if job.State != "failed" || job.ErrorCode != "job_interrupted" || len(job.Secret) != 0 {
		t.Fatalf("job = state %q code %q secret %d bytes; want failed, job_interrupted, no secret", job.State, job.ErrorCode, len(job.Secret))
	}
	codes := eventCodes(t, st, "prv_looping")
	if count(codes, "job_interrupted") != 1 || count(codes, "resumed_after_restart") != 0 || count(codes, "started") != 0 {
		t.Fatalf("events = %v; the job must fail without being run again", codes)
	}
}

// A pass that runs out of time leaves its job queued for the next pass instead of running forever.
func TestDrainRequeuesTheJobItCouldNotFinishInTime(t *testing.T) {
	released := make(chan struct{})
	node := newFakeSSHD(t, loginMethods{password: true}, func(string, ssh.Channel) ([]byte, error) {
		<-released // the preflight never answers
		return nil, nil
	})
	t.Cleanup(func() { close(released) })
	st := drainStore(t)
	svc := drainService(t, st, Config{SSH: node.client()})
	sealed, err := svc.sealCredentials("prv_slow", credentials{Username: "root", Password: authTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	job := store.NodeProvisionJob{ID: "prv_slow", NodeID: "nod_slow", Name: "slow", Address: "198.51.100.5", SSHHost: "node.example.com", SSHPort: 22,
		HostFingerprint: node.fingerprint(), Secret: sealed, CreatedBy: "adm_test", CreatedAt: drainT0, UpdatedAt: drainT0}
	if err := st.CreateNodeProvisionJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	more, busy, err := svc.Drain(ctx)
	if err != nil || busy || !more {
		t.Fatalf("Drain = more %v busy %v err %v; want the job back in the queue", more, busy, err)
	}
	got := jobState(t, st, "prv_slow")
	if got.State != "queued" || len(got.Secret) == 0 || count(eventCodes(t, st, "prv_slow"), "resumed_after_restart") != 1 || jobAttempt(t, st, "prv_slow") != 1 {
		t.Fatalf("job = %+v, events %v", got, eventCodes(t, st, "prv_slow"))
	}
}

// Kick replaces the in-process wake-up: a host that has no loop wakes its own runner.
func TestKickIsCalledInsteadOfWakingTheLoop(t *testing.T) {
	ctx := context.Background()
	st := drainStore(t)
	kicks := 0
	svc := drainService(t, st, Config{Kick: func() { kicks++ }})
	seedDrainJob(t, st, "prv_kick", "failed", 0, 0)
	_, err := svc.RetryNodeProvision(ctx, connect.NewRequest(&adminv1.RetryNodeProvisionRequest{JobId: "prv_kick", ConfirmInstall: true, Password: "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if kicks != 1 {
		t.Fatalf("Kick called %d times, want 1", kicks)
	}
	select {
	case <-svc.work:
		t.Fatal("the in-process worker was signalled as well")
	default:
	}

	plain := drainService(t, st, Config{})
	plain.signalWorker()
	select {
	case <-plain.work:
	default:
		t.Fatal("without Kick the worker loop is not signalled")
	}
}

func TestRetryGivesAFailedJobItsAttemptsBack(t *testing.T) {
	ctx := context.Background()
	st := drainStore(t)
	svc := drainService(t, st, Config{})
	seedDrainJob(t, st, "prv_again", "running", store.MaxProvisionAttempts, 0)
	if _, _, err := svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := jobState(t, st, "prv_again"); got.ErrorCode != "job_interrupted" {
		t.Fatalf("job = %+v", got)
	}
	if _, err := svc.RetryNodeProvision(ctx, connect.NewRequest(&adminv1.RetryNodeProvisionRequest{JobId: "prv_again", ConfirmInstall: true, Password: "secret"})); err != nil {
		t.Fatal(err)
	}
	if got := jobAttempt(t, st, "prv_again"); got != 0 {
		t.Fatalf("attempt after retry = %d, want 0", got)
	}
}

func TestLinkURLIsValidatedAndGivesThePanelProbeAddress(t *testing.T) {
	st := drainStore(t)
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	base := Config{Nodes: &e2eNodeManager{}, Binaries: testBinarySource{}, AgentSNI: "agent.example.com", StepUp: func(context.Context) error { return nil }}
	for _, bad := range []string{"https://panel.example.com/abc/", "wss:///abc/", "wss://user@panel.example.com/abc/", "wss://panel.example.com/abc/?x=1", "not a url"} {
		cfg := base
		cfg.LinkURL = bad
		if _, err := NewService(st, vlt, cfg); err == nil {
			t.Errorf("NewService accepted LinkURL %q", bad)
		}
	}
	for link, want := range map[string]string{
		"wss://panel.example.com/0123456789abcdef/":      "panel.example.com:443",
		"wss://panel.example.com:8443/0123456789abcdef/": "panel.example.com:8443",
	} {
		if got := (Config{LinkURL: link, PanelAddr: "other.example.com:1"}).panelAddress(); got != want {
			t.Errorf("panelAddress(%q) = %q, want %q", link, got, want)
		}
	}
	if got := (Config{PanelAddr: "panel.example.com:443"}).panelAddress(); got != "panel.example.com:443" {
		t.Errorf("panelAddress without a link = %q", got)
	}
	// with neither address an install says so, with either it does not
	svc := drainService(t, st, Config{})
	svc.cfg.PanelAddr, svc.cfg.LinkURL = "", ""
	_, err = svc.StartNodeProvision(context.Background(), connect.NewRequest(&adminv1.StartNodeProvisionRequest{ConfirmInstall: true}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "panel_address_not_configured") {
		t.Fatalf("StartNodeProvision with no panel address = %v", err)
	}
}

func TestEnrollCommandOverLinkAndOverMTLS(t *testing.T) {
	const ca = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	link := Config{LinkURL: "wss://panel.example.com/0123456789abcdef/", PanelAddr: "ignored.example.com:443", AgentSNI: "agent.example.com"}
	want := "/root/mistgate-node enroll --link-url 'wss://panel.example.com/0123456789abcdef/' --ca-sha256 '" + ca + "' --token-stdin --resume-key"
	if got := enrollCommand(link, ca); got != want {
		t.Fatalf("link enroll command = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"--panel", "--sni", "--token "} {
		if strings.Contains(want, forbidden) {
			t.Fatalf("link enroll command carries %q", forbidden)
		}
	}
	mtls := Config{PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com"}
	want = "/root/mistgate-node enroll --panel 'panel.example.com:443' --sni 'agent.example.com' --ca-sha256 '" + ca + "' --token-stdin --resume-key"
	if got := enrollCommand(mtls, ca); got != want {
		t.Fatalf("mTLS enroll command = %q, want %q", got, want)
	}
}
