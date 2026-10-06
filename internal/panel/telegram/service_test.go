package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (e *env) addNode(id, name string) {
	e.t.Helper()
	if _, err := e.st.W.Exec(`INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, id, name, name+".example.com"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) lang(l string) {
	e.t.Helper()
	if err := e.st.SetSettings(context.Background(), map[string]string{"language": l}); err != nil {
		e.t.Fatal(err)
	}
}

// An alert that opens is one message, with the node and the reason; the same open alert reported again adds nothing.
func TestAlertOpenIsOneMessageAndDuplicatesAreSuppressed(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "de-fra-1")
	e.setBot()
	e.link(owner, 777)
	e.run()

	a := alert("node_down", "nod_1", 3)
	a.Params = map[string]string{"minutes": "12"}
	e.svc.AlertTransition(a, false)
	e.svc.AlertTransition(a, false)
	waitFor(t, "the alert message", func() bool { return len(e.tg.sentTo(777)) == 1 })
	e.quiet(1, 150*time.Millisecond)
	got := e.tg.sentTo(777)[0]
	for _, want := range []string{"Node is unreachable", "critical", "de-fra-1", "12 min"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "<a ") {
		t.Errorf("no admin URL is known, yet the message links: %q", got)
	}
}

// The panel language decides the words; a resolve says how long the alert lasted, and an alert that comes back after it
// resolved is announced again.
func TestResolveCarriesDurationInThePanelLanguage(t *testing.T) {
	e := newEnv(t)
	e.lang("ru")
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "de-fra-1")
	e.setBot()
	e.link(owner, 777)
	e.run()

	t0 := e.clk.now()
	a := alert("node_down", "nod_1", 3)
	a.FirstSeen = t0
	e.svc.AlertTransition(a, false)
	waitFor(t, "the open message", func() bool { return len(e.tg.sentTo(777)) == 1 })
	a.ResolvedAt, a.Resolution = t0.Add(14*time.Minute), "node_returned"
	e.svc.AlertTransition(a, true)
	waitFor(t, "the resolve message", func() bool { return len(e.tg.sentTo(777)) == 2 })
	got := e.tg.sentTo(777)
	if !strings.Contains(got[0], "Нода недоступна") {
		t.Errorf("open message is not Russian: %q", got[0])
	}
	for _, want := range []string{"Решено", "Нода недоступна", "de-fra-1", "длилось 14 мин"} {
		if !strings.Contains(got[1], want) {
			t.Errorf("resolve message %q lacks %q", got[1], want)
		}
	}
	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false) // down again
	waitFor(t, "the second open message", func() bool { return len(e.tg.sentTo(777)) == 3 })
}

// A burst inside the window becomes one message per chat, an identical line is not repeated, and a quiet period after it
// starts a new one.
func TestBurstIsCoalescedIntoOneMessage(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	for _, n := range []string{"a", "b", "c"} {
		e.addNode("nod_"+n, "node-"+n)
	}
	e.setBot()
	e.link(owner, 777)
	e.run()

	for _, n := range []string{"a", "b", "c"} {
		e.svc.AlertTransition(alert("node_down", "nod_"+n, 3), false)
	}
	e.svc.PlanWaiting("node_fix", "ops")
	e.svc.PlanWaiting("node_fix", "ops") // the same line twice
	waitFor(t, "the batch", func() bool { return len(e.tg.sentTo(777)) >= 1 })
	e.quiet(1, 150*time.Millisecond)
	got := e.tg.sentTo(777)[0]
	for _, want := range []string{"node-a", "node-b", "node-c", "Fix a node"} {
		if !strings.Contains(got, want) {
			t.Errorf("batch %q lacks %q", got, want)
		}
	}
	if strings.Count(got, "Fix a node") != 1 {
		t.Errorf("the repeated line is in the batch twice: %q", got)
	}
}

// Info alerts are below the floor; an alert that a worse one replaced, or that the owner accepted, ends without a message.
func TestSeverityFloorAndSilentResolutions(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 777)
	e.run()

	e.svc.AlertTransition(alert("doctor_warn", "nod_1", 1), false)
	for i, res := range []string{"superseded", "accepted", "node_retired"} {
		a := alert("doctor_warn", "nod_1", 2)
		a.Params = map[string]string{"check": "disk_space"}
		e.svc.AlertTransition(a, false)
		waitFor(t, "an open message", func() bool { return e.tg.total() >= i+1 })
		before := e.tg.total()
		a.Resolution, a.ResolvedAt = res, e.clk.now()
		e.svc.AlertTransition(a, true)
		e.quiet(before, 120*time.Millisecond)
	}
}

// Every role gets health alerts; the owner's business (a backup, a release, a waiting plan) is the owner's only.
func TestAudienceFollowsTheRole(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AdminURL = "https://panel.example/abc/" })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	helper := e.addAdmin("adm_h", "Helper", store.RoleHelper)
	reader := e.addAdmin("adm_r", "Reader", store.RoleReadonly)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 1)
	e.link(helper, 2)
	e.link(reader, 3)
	e.run()

	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
	e.svc.BackupFailed("backup_storage_failed")
	e.svc.PlanWaiting("rollout_start", "ops")
	waitFor(t, "all three chats", func() bool { return len(e.tg.sentTo(1)) == 1 && len(e.tg.sentTo(2)) == 1 && len(e.tg.sentTo(3)) == 1 })
	owned := e.tg.sentTo(1)[0]
	for _, want := range []string{"Node is unreachable", "backup failed", "waits for your approval", `href="https://panel.example/abc/health"`, `href="https://panel.example/abc/integrations"`} {
		if !strings.Contains(owned, want) {
			t.Errorf("owner message lacks %q: %q", want, owned)
		}
	}
	for _, chat := range []int64{2, 3} {
		m := e.tg.sentTo(chat)[0]
		if !strings.Contains(m, "Node is unreachable") || strings.Contains(m, "backup") || strings.Contains(m, "approval") {
			t.Errorf("chat %d got the wrong set: %q", chat, m)
		}
	}
}

// An admin who switched alerts off gets nothing; the others still do.
func TestDisabledLinkIsSkipped(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	helper := e.addAdmin("adm_h", "Helper", store.RoleHelper)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 1)
	e.link(helper, 2)
	if err := e.st.SetTelegramEnabled(context.Background(), helper.ID, false); err != nil {
		t.Fatal(err)
	}
	e.run()
	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
	waitFor(t, "the owner's message", func() bool { return len(e.tg.sentTo(1)) == 1 })
	e.quiet(1, 120*time.Millisecond)
}

// Names and details from data are escaped and cut to one line; nothing that looks like markup reaches the chat.
func TestUntrustedTextIsEscaped(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", `<b>evil</b> & "q"`)
	e.setBot()
	e.link(owner, 1)
	e.run()

	a := alert("check_failed", "nod_1", 2)
	a.Subject = "inb_1"
	a.WhyKey = "health.alert.check_failed.why.timeout"
	a.Params = map[string]string{"profile": "<script>alert(1)</script>\nline two", "port": "443"}
	e.svc.AlertTransition(a, false)
	d := alert("doctor_fail", "nod_1", 3)
	d.Params = map[string]string{"check": "disk_space", "detail": "<a href=\"http://x\">click</a>\r\nsecond line " + strings.Repeat("z", 400)}
	e.svc.AlertTransition(d, false)
	waitFor(t, "the message", func() bool { return len(e.tg.sentTo(1)) == 1 })
	got := e.tg.sentTo(1)[0]
	for _, bad := range []string{"<script>", "<b>evil", `<a href="http://x"`, "\r"} {
		if strings.Contains(got, bad) {
			t.Errorf("message holds raw %q: %q", bad, got)
		}
	}
	for _, want := range []string{"&lt;b&gt;evil&lt;/b&gt; &amp; &quot;q&quot;", "&lt;script&gt;alert(1)&lt;/script&gt; line two", "&lt;a href=&quot;http://x&quot;&gt;click&lt;/a&gt; second line"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q: %q", want, got)
		}
	}
	if len([]rune(got)) > maxMessage {
		t.Errorf("message is %d runes", len([]rune(got)))
	}
}

// Telegram's 429 is waited out for as long as it asks, then the message goes through once.
func TestRetryAfterIsRespected(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 1)
	e.tg.sendPlan = []planned{{status: 429, retryAfter: 1, desc: "Too Many Requests: retry after 1"}}
	e.run()

	start := time.Now()
	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
	waitFor(t, "the message after the wait", func() bool { return len(e.tg.sentTo(1)) == 1 })
	if took := time.Since(start); took < 900*time.Millisecond {
		t.Errorf("sent after %v: retry_after (1 s) was not waited out", took)
	}
	e.quiet(1, 100*time.Millisecond)
}

func TestRetryAfterBeyondOldSendCapDoesNotUseRetryBudget(t *testing.T) {
	waited := make(chan time.Duration, 1)
	release := make(chan struct{})
	e := newEnv(t, func(c *Config) {
		c.RetryMax = 1
		c.Wait = func(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
			waited <- d
			select {
			case <-release:
				return true
			case <-ctx.Done():
				return false
			case <-wake:
				return ctx.Err() == nil
			}
		}
	})
	e.tg.mu.Lock()
	e.tg.sendPlan = []planned{{status: 429, retryAfter: 180, desc: "Too Many Requests: retry after 180"}}
	e.tg.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.deliver(context.Background(), testTokenRedacted(), 1, "test message")
	}()
	select {
	case got := <-waited:
		if got != 3*time.Minute {
			t.Errorf("waited %v for retry_after, want 3m", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not wait for retry_after")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not finish after the retry wait")
	}
	if got := e.tg.count("sendMessage"); got != 2 {
		t.Errorf("sendMessage called %d times, want one 429 and one successful retry", got)
	}
	if got := e.tg.sentTo(1); len(got) != 1 || got[0] != "test message" {
		t.Errorf("sent messages: %v, want the message exactly once", got)
	}
}

func TestRetryAfterIsCappedAtOneHour(t *testing.T) {
	if got := retryAfterDuration(2 * 60 * 60); got != time.Hour {
		t.Errorf("parsed retry_after is %v, want the one-hour cap", got)
	}
	if got := retryAfterWait(2 * time.Hour); got != time.Hour {
		t.Errorf("retry wait is %v, want the one-hour cap", got)
	}
}

func TestPollRetryAfterBeyondOldCapIsCancellable(t *testing.T) {
	waited := make(chan time.Duration, 1)
	e := newEnv(t, func(c *Config) {
		c.Wait = func(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
			waited <- d
			select {
			case <-ctx.Done():
				return false
			case <-wake:
				return ctx.Err() == nil
			}
		}
	})
	e.setBot()
	e.tg.mu.Lock()
	e.tg.pollPlan = []planned{{status: 429, retryAfter: 360, desc: "Too Many Requests: retry after 360"}}
	e.tg.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.pollLoop(ctx) }()
	select {
	case got := <-waited:
		if got != 6*time.Minute {
			t.Errorf("waited %v for retry_after, want 6m", got)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("poll did not wait for retry_after")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not stop when its context was cancelled")
	}
}

// A network failure or a server error is retried with a backoff; a blocked bot is not retried at all.
func TestSendRetriesServerErrorsAndGivesUpOnRefusal(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 1)
	e.tg.sendPlan = []planned{{status: 502}, {status: 500}}
	e.run()
	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
	waitFor(t, "the message after two failures", func() bool { return len(e.tg.sentTo(1)) == 1 })

	e.tg.mu.Lock()
	e.tg.sendPlan = []planned{{status: 403, desc: "Forbidden: bot was blocked by the user"}}
	e.tg.mu.Unlock()
	n := e.tg.count("sendMessage")
	e.svc.PlanWaiting("node_fix", "ops")
	waitFor(t, "the refused send", func() bool { return e.tg.count("sendMessage") > n })
	e.quiet(1, 200*time.Millisecond)
	if got := e.tg.count("sendMessage") - n; got != 1 {
		t.Errorf("a refused message was sent %d times, want 1", got)
	}
}

// A backup failure is told once however often the scheduler retries; the next success resolves it, once.
func TestBackupFailureOnceThenResolved(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.link(owner, 1)
	e.run()

	e.svc.BackupOK() // nothing failed: nothing to resolve
	e.svc.BackupFailed("backup_create_failed")
	e.svc.BackupFailed("backup_create_failed")
	waitFor(t, "the failure", func() bool { return len(e.tg.sentTo(1)) == 1 })
	e.svc.BackupFailed("backup_storage_failed")
	e.quiet(1, 120*time.Millisecond)
	e.svc.BackupOK()
	waitFor(t, "the resolve", func() bool { return len(e.tg.sentTo(1)) == 2 })
	if !strings.Contains(e.tg.sentTo(1)[1], "works again") {
		t.Errorf("resolve: %q", e.tg.sentTo(1)[1])
	}
}

// A signed release and agents behind a release are each told once per version, to the owner, and only once somebody can hear it.
func TestReleaseAndOutdatedAgentsOncePerVersion(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AdminURL = "https://panel.example/" })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	version, nodes := "v0.2.0", []string{"alpha", "beta"}
	available := true
	e.svc.SetSources(Sources{
		PanelRelease:   func() (string, bool) { return version, available },
		OutdatedAgents: func(context.Context) (string, []string) { return version, nodes },
	})
	e.run()
	ctx := context.Background()

	e.svc.watch(ctx) // no bot, no chat: not told, not marked
	e.setBot()
	e.link(owner, 1)
	e.svc.watch(ctx)
	e.svc.watch(ctx)
	waitFor(t, "release and agents", func() bool { return len(e.tg.sentTo(1)) >= 1 })
	e.quiet(1, 150*time.Millisecond) // both lines in one message
	got := e.tg.sentTo(1)[0]
	for _, want := range []string{"v0.2.0", "alpha, beta", `href="https://panel.example/updates"`} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q: %q", want, got)
		}
	}
	version = "v0.3.0"
	e.svc.watch(ctx)
	waitFor(t, "the next version", func() bool { return len(e.tg.sentTo(1)) == 2 })
	available = false
	nodes = nil
	version = "v0.4.0"
	e.svc.watch(ctx)
	e.quiet(2, 120*time.Millisecond)
}

// A waiting plan names the tool and the token, never its arguments, and carries the link only when the address is known.
func TestPlanWaitingLink(t *testing.T) {
	for _, tc := range []struct {
		url     string
		hasLink bool
	}{{"", false}, {"https://panel.example/zz/", true}} {
		e := newEnv(t, func(c *Config) { c.AdminURL = tc.url })
		owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
		e.setBot()
		e.link(owner, 1)
		e.run()
		e.svc.PlanWaiting("node_install", "claude-ops")
		waitFor(t, "the message", func() bool { return len(e.tg.sentTo(1)) == 1 })
		got := e.tg.sentTo(1)[0]
		if !strings.Contains(got, "claude-ops") || !strings.Contains(got, "Install a node over SSH") {
			t.Errorf("message: %q", got)
		}
		if has := strings.Contains(got, `href="https://panel.example/zz/integrations"`); has != tc.hasLink {
			t.Errorf("url %q: link = %v, want %v: %q", tc.url, has, tc.hasLink, got)
		}
	}
}

// A lockout is the owner's news; a sign-in from a new address is told to the owner and to the admin themself, once per
// address; failed sign-ins alone are not.
func TestSecurityEvents(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	helper := e.addAdmin("adm_h", "Helper", store.RoleHelper)
	reader := e.addAdmin("adm_r", "Reader", store.RoleReadonly)
	e.setBot()
	e.link(owner, 1)
	e.link(helper, 2)
	e.link(reader, 3)
	e.run()

	e.svc.Security(auth.Event{Kind: auth.EventSignInFailed, IP: "198.51.100.9"})
	e.svc.Security(auth.Event{Kind: auth.EventLockout, Login: "root", IP: "198.51.100.9", Until: e.clk.now().Add(15 * time.Minute)})
	e.svc.Security(auth.Event{Kind: auth.EventSignIn, AdminID: helper.ID, Method: "passkey", IP: "203.0.113.5"})
	e.svc.Security(auth.Event{Kind: auth.EventSignIn, AdminID: helper.ID, Method: "passkey", IP: "203.0.113.5"}) // same place: nothing new
	e.svc.Security(auth.Event{Kind: auth.EventPasskeyAdded, AdminID: reader.ID, IP: "203.0.113.7"})
	waitFor(t, "the owner's and the helper's messages", func() bool { return len(e.tg.sentTo(1)) == 1 && len(e.tg.sentTo(2)) == 1 && len(e.tg.sentTo(3)) == 1 })
	e.quiet(3, 150*time.Millisecond)
	ownerMsg := e.tg.sentTo(1)[0]
	for _, want := range []string{"locked", "root", "198.51.100.9", "12:15 UTC", "Helper", "203.0.113.5", "passkey was added"} {
		if !strings.Contains(ownerMsg, want) {
			t.Errorf("owner message lacks %q: %q", want, ownerMsg)
		}
	}
	helperMsg := e.tg.sentTo(2)[0]
	if strings.Contains(helperMsg, "locked") || !strings.Contains(helperMsg, "203.0.113.5") || strings.Contains(helperMsg, "passkey was added") {
		t.Errorf("helper message: %q", helperMsg)
	}
	if m := e.tg.sentTo(3)[0]; !strings.Contains(m, "passkey was added") || strings.Contains(m, "locked") {
		t.Errorf("reader message: %q", m)
	}
}

// Messages are built from named fields only: whatever else rides in an alert's params (a token, a link) never appears.
func TestMessagesCarryNoSecretsFromParams(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AdminURL = "https://panel.example/zz/" })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 1)
	e.run()
	a := alert("doctor_fail", "nod_1", 3)
	a.Params = map[string]string{"check": "disk_space", "token": "tk1_SECRETSECRETSECRET", "subscription_url": "https://panel.example/sub/SECRET", "fix_id": "x"}
	e.svc.AlertTransition(a, false)
	waitFor(t, "the message", func() bool { return len(e.tg.sentTo(1)) == 1 })
	got := e.tg.sentTo(1)[0]
	for _, bad := range []string{"SECRET", "tk1_", "/sub/", testToken} {
		if strings.Contains(got, bad) {
			t.Errorf("message holds %q: %q", bad, got)
		}
	}
}

// An API description and a transport error never expose the bot token.
func TestClientErrorsDoNotLeakTheToken(t *testing.T) {
	e := newEnv(t)
	e.tg.mu.Lock()
	e.tg.sendPlan = []planned{{status: 400, desc: "bad " + testToken + " and bot987654321:Fake_Description"}}
	e.tg.mu.Unlock()
	err := e.svc.api.sendMessage(context.Background(), testTokenRedacted(), 1, "test")
	if err == nil {
		t.Fatal("sendMessage unexpectedly succeeded")
	}
	for _, secret := range []string{testToken, "bot987654321:Fake_Description"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("API description leaked %q: %v", secret, err)
		}
	}

	e.tg.srv.Close() // nothing listens any more
	_, err = e.svc.api.getMe(context.Background(), testTokenRedacted())
	if err == nil {
		t.Fatal("getMe succeeded against a closed server")
	}
	if strings.Contains(err.Error(), "123456789") || strings.Contains(err.Error(), "AAFake") {
		t.Errorf("the error carries the token: %v", err)
	}
}

func TestRunRequeuesOnlyUndeliveredItemsOnShutdown(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.link(owner, 1)

	markers := []string{"event-1", "event-2", "event-3"}
	for _, marker := range markers {
		line := marker + ":" + strings.Repeat("x", 2000)
		e.svc.enqueue(item{to: everyone, text: func(L, string) string { return line }})
	}
	blocked := make(chan struct{})
	e.tg.mu.Lock()
	e.tg.blockSendCall = 2 // the first chunk succeeds; the next request blocks until cancellation
	e.tg.blockSendStarted = blocked
	e.tg.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the second send did not block")
	}
	line := "event-4:" + strings.Repeat("x", 2000)
	e.svc.enqueue(item{to: everyone, text: func(L, string) string { return line }})
	markers = append(markers, "event-4")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish its final flush")
	}

	got := e.tg.sentTo(1)
	if len(got) != len(markers) {
		t.Fatalf("got %d sent chunks, want %d: %v", len(got), len(markers), got)
	}
	for i, marker := range markers {
		if !strings.HasPrefix(got[i], marker+":") {
			t.Errorf("chunk %d is %q, want %s", i, got[i][:min(len(got[i]), 40)], marker)
		}
	}
}

// Run returns when its context ends and leaves no goroutine behind, even with a poll in flight and messages queued.
func TestRunStopsCleanly(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.addNode("nod_1", "n1")
	e.setBot()
	e.link(owner, 1)
	before := goroutines()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.Run(ctx) }()
	waitFor(t, "a poll", func() bool { return e.tg.count("getUpdates") > 0 })
	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false) // queued, window not over
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if got := e.tg.sentTo(1); len(got) != 1 { // the final flush hands over what was queued
		t.Errorf("queued message after stop: %v", got)
	}
	e.tg.srv.CloseClientConnections()
	waitFor(t, "goroutines to end", func() bool { return goroutines() <= before })
}
