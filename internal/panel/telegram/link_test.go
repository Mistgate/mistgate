package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func testTokenRedacted() vault.Redacted { return vault.Redacted(testToken) }

func goroutines() int {
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	return runtime.NumGoroutine()
}

func connectCode(err error) connect.Code {
	if err == nil {
		return 0
	}
	return connect.CodeOf(err)
}

func (e *env) rpcSetBot(a store.Admin, token string, clear bool) (*connect.Response[adminv1.SetTelegramBotResponse], error) {
	return rpc{e.svc}.SetTelegramBot(e.ctxAs(a), connect.NewRequest(&adminv1.SetTelegramBotRequest{Token: token, Clear: clear}))
}

func (e *env) auditActions() []string {
	rows, err := e.st.ListAudit(context.Background(), "", 0, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Action+" "+r.Params)
	}
	return out
}

// The owner pastes the token: it is checked with getMe, sealed in the vault, never returned, and the audit row names the
// bot but not the token. Clearing forgets the bot and every link.
func TestSetAndClearBot(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	resp, err := e.rpcSetBot(owner, " "+testToken+" ", false)
	if err != nil {
		t.Fatal(err)
	}
	b := resp.Msg.Status.Bot
	if !b.Configured || b.Username != "mist_alert_bot" || b.Error != "" {
		t.Errorf("bot status: %+v", b)
	}
	if e.tg.webhooks != 1 {
		t.Errorf("deleteWebhook called %d times, want 1", e.tg.webhooks)
	}
	if raw, _ := json.Marshal(resp.Msg); strings.Contains(string(raw), "AAFake") {
		t.Errorf("the response carries the token: %s", raw)
	}
	row, err := e.st.TelegramBot(context.Background())
	if err != nil || strings.Contains(string(row.Token), "AAFake") {
		t.Fatalf("stored token is not sealed: %v", err)
	}
	if pt, err := e.svc.vault.Open(row.Token, tokenRecordID); err != nil || string(pt) != testToken {
		t.Errorf("stored token does not open: %v", err)
	}
	for _, line := range e.auditActions() {
		if strings.Contains(line, "AAFake") || strings.Contains(line, "123456789:") {
			t.Errorf("audit holds the token: %s", line)
		}
	}
	if got := e.auditActions(); len(got) != 1 || !strings.HasPrefix(got[0], "telegram_bot_set ") || !strings.Contains(got[0], "mist_alert_bot") {
		t.Errorf("audit rows: %v", got)
	}

	e.link(owner, 5)
	if _, err := e.rpcSetBot(owner, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.TelegramBot(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the bot is still stored after clear: %v", err)
	}
	if links, _ := e.st.TelegramLinks(context.Background()); len(links) != 0 {
		t.Errorf("links survived clear: %v", links)
	}
	if got := e.auditActions(); len(got) != 2 || !strings.HasPrefix(got[0], "telegram_bot_clear ") || !strings.Contains(got[0], `"dropped_links":1`) {
		t.Errorf("audit rows: %v", got)
	}
}

func TestSetBotRefusals(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	helper := e.addAdmin("adm_h", "Helper", store.RoleHelper)
	for _, tc := range []struct {
		name  string
		admin store.Admin
		token string
		want  connect.Code
	}{
		{"a helper", helper, testToken, connect.CodePermissionDenied},
		{"an API token", store.Admin{ID: "token:tok_1", Role: store.RoleOwner}, testToken, connect.CodePermissionDenied},
		{"an MCP agent", store.Admin{ID: "mcp:tok_1", Role: store.RoleOwner}, testToken, connect.CodePermissionDenied},
		{"a malformed token", owner, "not-a-token", connect.CodeInvalidArgument},
		{"a token Telegram refuses", owner, "987654321:BBFakeFakeFakeFakeFakeFakeFakeFake_y", connect.CodeFailedPrecondition},
	} {
		_, err := e.rpcSetBot(tc.admin, tc.token, false)
		if got := connectCode(err); got != tc.want {
			t.Errorf("%s: code %v (%v), want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := e.st.TelegramBot(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused call stored a bot: %v", err)
	}

	// no step-up, no change
	deny := newEnv(t, func(c *Config) {
		c.StepUp = func(context.Context) error {
			return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
		}
	})
	o2 := deny.addAdmin("adm_o", "Owner", store.RoleOwner)
	if _, err := deny.rpcSetBot(o2, testToken, false); connectCode(err) != connect.CodePermissionDenied {
		t.Errorf("without a step-up: %v", err)
	}
	if _, err := deny.st.TelegramBot(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a call without step-up stored a bot: %v", err)
	}
}

// Every other procedure refuses a token principal too, whatever its role.
func TestOwnAccountProceduresRefuseTokens(t *testing.T) {
	e := newEnv(t)
	e.addAdmin("adm_o", "Owner", store.RoleOwner)
	tokenCtx := auth.WithAdmin(context.Background(), store.Admin{ID: "token:tok_1", Role: store.RoleOwner})
	r := rpc{e.svc}
	calls := map[string]error{}
	_, calls["Get"] = r.GetTelegram(tokenCtx, connect.NewRequest(&adminv1.GetTelegramRequest{}))
	_, calls["Begin"] = r.BeginTelegramLink(tokenCtx, connect.NewRequest(&adminv1.BeginTelegramLinkRequest{}))
	_, calls["Unlink"] = r.UnlinkTelegram(tokenCtx, connect.NewRequest(&adminv1.UnlinkTelegramRequest{}))
	_, calls["Alerts"] = r.SetTelegramAlerts(tokenCtx, connect.NewRequest(&adminv1.SetTelegramAlertsRequest{Enabled: true}))
	_, calls["Test"] = r.SendTelegramTest(tokenCtx, connect.NewRequest(&adminv1.SendTelegramTestRequest{}))
	for name, err := range calls {
		if connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s with a token: %v", name, err)
		}
	}
}

// The whole path: BeginTelegramLink gives a deep link, the admin sends /start <code> to the bot, the poll hears it, the panel
// binds the chat and answers "linked". Strangers, a wrong code, a used code and a group chat get nothing.
func TestLinkFlow(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	helper := e.addAdmin("adm_h", "Helper", store.RoleHelper)
	if _, err := e.rpcSetBot(owner, testToken, false); err != nil {
		t.Fatal(err)
	}
	e.run()

	begin, err := rpc{e.svc}.BeginTelegramLink(e.ctxAs(owner), connect.NewRequest(&adminv1.BeginTelegramLinkRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	code := begin.Msg.Code
	if want := "https://t.me/mist_alert_bot?start=" + code; begin.Msg.DeepLink != want || len(code) != 12 {
		t.Errorf("deep link %q, code %q", begin.Msg.DeepLink, code)
	}
	if begin.Msg.ExpiresUnix != e.clk.now().Add(linkCodeTTL).Unix() {
		t.Errorf("expires %d", begin.Msg.ExpiresUnix)
	}

	e.tg.say(901, "/start")                 // a stranger saying hello
	e.tg.say(902, "/start nonsense")        // a stranger with a wrong code
	e.tg.say(903, "hello there")            // not a command
	e.tg.say(904, "/start@other_bot "+code) // a command for another bot still carries our code: it is the code that counts
	waitFor(t, "the link", func() bool { _, err := e.st.TelegramLink(context.Background(), owner.ID); return err == nil })
	link, _ := e.st.TelegramLink(context.Background(), owner.ID)
	if link.ChatID != 904 || !link.Enabled {
		t.Errorf("link: %+v", link)
	}
	waitFor(t, "the reply", func() bool { return len(e.tg.sentTo(904)) == 1 })
	if r := e.tg.sentTo(904)[0]; !strings.Contains(r, "Linked") || !strings.Contains(r, "Owner") {
		t.Errorf("reply: %q", r)
	}
	for _, stranger := range []int64{901, 902, 903} {
		if got := e.tg.sentTo(stranger); len(got) != 0 {
			t.Errorf("chat %d got a reply: %v", stranger, got)
		}
	}

	// the code worked once
	e.tg.say(905, "/start "+code)
	// a code of another admin, but sent from a group, is ignored
	hb, _ := rpc{e.svc}.BeginTelegramLink(e.ctxAs(helper), connect.NewRequest(&adminv1.BeginTelegramLinkRequest{}))
	e.tg.mu.Lock()
	e.tg.nextID++
	e.tg.updates = append(e.tg.updates, tgUpdate{UpdateID: e.tg.nextID, Message: &tgMessage{Chat: tgChat{ID: -100, Type: "group"}, Text: "/start " + hb.Msg.Code}})
	e.tg.mu.Unlock()
	// the linked chat saying /start without a code is told what it is
	e.tg.say(904, "/start")
	waitFor(t, "the status reply", func() bool { return len(e.tg.sentTo(904)) == 2 })
	if r := e.tg.sentTo(904)[1]; !strings.Contains(r, "gets alerts") {
		t.Errorf("status reply: %q", r)
	}
	if got := e.tg.sentTo(905); len(got) != 0 {
		t.Errorf("a used code was answered: %v", got)
	}
	if _, err := e.st.TelegramLink(context.Background(), helper.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a group chat linked the helper: %v", err)
	}
	// the helper's own code, from a private chat, links the helper
	e.tg.say(906, "/start "+hb.Msg.Code)
	waitFor(t, "the helper's link", func() bool {
		l, err := e.st.TelegramLink(context.Background(), helper.ID)
		return err == nil && l.ChatID == 906
	})
	if got := e.auditActions(); !containsAction(got, "telegram_link") {
		t.Errorf("no audit row for the link: %v", got)
	}
}

func containsAction(rows []string, action string) bool {
	for _, r := range rows {
		if strings.HasPrefix(r, action+" ") {
			return true
		}
	}
	return false
}

// A code that has expired, or one that was replaced by a newer one for the same admin, links nothing and gets no reply.
func TestExpiredAndReplacedCodes(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.run()

	old, _ := e.svc.newCode(owner.ID)
	stale, _ := e.svc.newCode(owner.ID) // replaces old
	e.tg.say(1, "/start "+old)
	e.clk.advance(linkCodeTTL + time.Second)
	e.tg.say(2, "/start "+stale)
	waitFor(t, "both updates to be read", func() bool {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		return len(e.tg.offsets) > 0 && e.tg.offsets[len(e.tg.offsets)-1] > e.tg.updates[1].UpdateID
	})
	if _, err := e.st.TelegramLink(context.Background(), owner.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a dead code linked the owner: %v", err)
	}
	if e.tg.total() != 0 {
		t.Errorf("a dead code was answered: %v", e.tg.sent)
	}
}

// A link needs a bot; the code is not given out without one.
func TestBeginLinkNeedsBot(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	_, err := rpc{e.svc}.BeginTelegramLink(e.ctxAs(owner), connect.NewRequest(&adminv1.BeginTelegramLinkRequest{}))
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("begin without a bot: %v", err)
	}
}

// Unlink, alerts on and off, the test message, and who sees whose links.
func TestOwnLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	helper := e.addAdmin("adm_h", "Helper", store.RoleHelper)
	e.setBot()
	r := rpc{e.svc}
	ctx := e.ctxAs(helper)

	if _, err := r.SendTelegramTest(ctx, connect.NewRequest(&adminv1.SendTelegramTestRequest{})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("test without a link: %v", err)
	}
	if _, err := r.SetTelegramAlerts(ctx, connect.NewRequest(&adminv1.SetTelegramAlertsRequest{Enabled: false})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("switch without a link: %v", err)
	}
	e.link(owner, 1)
	e.link(helper, 2)

	got, err := r.GetTelegram(ctx, connect.NewRequest(&adminv1.GetTelegramRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Msg.Status; s.Mine == nil || s.Mine.AdminId != helper.ID || len(s.Links) != 0 || s.AdminUrlKnown {
		t.Errorf("the helper sees: %+v", s)
	}
	og, _ := r.GetTelegram(e.ctxAs(owner), connect.NewRequest(&adminv1.GetTelegramRequest{}))
	if s := og.Msg.Status; len(s.Links) != 2 || s.Mine == nil || s.Mine.AdminId != owner.ID {
		t.Errorf("the owner sees: %+v", s)
	}
	if s := og.Msg.Status; s.Mine.ChatId != 1 {
		t.Errorf("the owner's own chat id is %d, want 1", s.Mine.ChatId)
	} else {
		for _, l := range s.Links {
			if l.ChatId != 0 {
				t.Errorf("the owner is told the chat id of %s", l.AdminName)
			}
		}
	}
	if s := got.Msg.Status; s.Mine.ChatId != 2 {
		t.Errorf("the helper's own chat id is %d, want 2", s.Mine.ChatId)
	}

	if _, err := r.SendTelegramTest(ctx, connect.NewRequest(&adminv1.SendTelegramTestRequest{})); err != nil {
		t.Fatal(err)
	}
	if m := e.tg.sentTo(2); len(m) != 1 || !strings.Contains(m[0], "Test message") {
		t.Errorf("test message: %v", m)
	}
	if _, err := r.SendTelegramTest(ctx, connect.NewRequest(&adminv1.SendTelegramTestRequest{})); connectCode(err) != connect.CodeResourceExhausted {
		t.Errorf("a second test at once: %v", err)
	}

	off, err := r.SetTelegramAlerts(ctx, connect.NewRequest(&adminv1.SetTelegramAlertsRequest{Enabled: false}))
	if err != nil || off.Msg.Status.Mine.Enabled {
		t.Errorf("switch off: %v %+v", err, off)
	}
	un, err := r.UnlinkTelegram(ctx, connect.NewRequest(&adminv1.UnlinkTelegramRequest{}))
	if err != nil || un.Msg.Status.Mine != nil {
		t.Errorf("unlink: %v %+v", err, un)
	}
	if _, err := r.UnlinkTelegram(ctx, connect.NewRequest(&adminv1.UnlinkTelegramRequest{})); err != nil {
		t.Errorf("unlink twice: %v", err)
	}
	if rows := e.auditActions(); !containsAction(rows, "telegram_unlink") {
		t.Errorf("no audit row for the unlink: %v", rows)
	}
	if n := countAction(e.auditActions(), "telegram_unlink"); n != 1 {
		t.Errorf("%d unlink audit rows, want 1", n)
	}
}

func countAction(rows []string, action string) int {
	n := 0
	for _, r := range rows {
		if strings.HasPrefix(r, action+" ") {
			n++
		}
	}
	return n
}

// A token Telegram stops accepting shows on the screen as "unauthorized"; a poll conflict as "conflict".
func TestBotErrorShowsOnTheStatus(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.tg.pollPlan = []planned{{status: 409, desc: "Conflict: terminated by other getUpdates request"}}
	e.run()
	r := rpc{e.svc}
	waitFor(t, "the conflict", func() bool {
		g, err := r.GetTelegram(e.ctxAs(owner), connect.NewRequest(&adminv1.GetTelegramRequest{}))
		return err == nil && g.Msg.Status.Bot.Error == "conflict"
	})
	waitFor(t, "the poll to recover", func() bool {
		g, _ := r.GetTelegram(e.ctxAs(owner), connect.NewRequest(&adminv1.GetTelegramRequest{}))
		return g.Msg.Status.Bot.Error == ""
	})
}

// The poll position survives a restart: a /start that was handled is not heard again.
func TestOffsetIsRemembered(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	cancel := e.run()
	code, _ := e.svc.newCode(owner.ID)
	e.tg.say(7, "/start "+code)
	waitFor(t, "the link", func() bool { _, err := e.st.TelegramLink(context.Background(), owner.ID); return err == nil })
	waitFor(t, "the offset to be saved", func() bool {
		v, err := e.st.Setting(context.Background(), offsetKey)
		return err == nil && v != "0" && v != ""
	})
	cancel()
	time.Sleep(60 * time.Millisecond)

	e.tg.mu.Lock()
	before := len(e.tg.offsets)
	e.tg.mu.Unlock()
	svc2, err := New(Config{Store: e.st, Vault: e.svc.vault, StepUp: e.svc.cfg.StepUp, APIBase: e.tg.srv.URL, Window: 40 * time.Millisecond, PollTimeout: 1, WatchFirst: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go svc2.Run(ctx)
	waitFor(t, "the new poll", func() bool { e.tg.mu.Lock(); defer e.tg.mu.Unlock(); return len(e.tg.offsets) > before })
	e.tg.mu.Lock()
	first := e.tg.offsets[before]
	last := e.tg.updates[len(e.tg.updates)-1].UpdateID
	e.tg.mu.Unlock()
	if first != last+1 {
		t.Errorf("the restarted poll asked from %d, want %d", first, last+1)
	}
}

// Update ids belong to a bot: when the owner swaps in another bot the poll starts from the beginning, or the new bot's first
// /start could fall below the old position and never be heard. The same bot pasted again keeps its place.
func TestAnotherBotStartsThePollFromTheBeginning(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	if _, err := e.rpcSetBot(owner, testToken, false); err != nil {
		t.Fatal(err)
	}
	e.run()
	e.tg.say(7, "hello") // nobody answers, but it moves the position on
	lastOffset := func() int64 {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		if len(e.tg.offsets) == 0 {
			return -1
		}
		return e.tg.offsets[len(e.tg.offsets)-1]
	}
	waitFor(t, "the position to move", func() bool { return lastOffset() > 100 })

	if _, err := e.rpcSetBot(owner, testToken, false); err != nil { // the same bot again
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := lastOffset(); got <= 100 {
		t.Errorf("the same bot lost its place: offset %d", got)
	}

	e.tg.mu.Lock()
	e.tg.botID = 555 // another bot (the fake accepts the same token for it)
	e.tg.mu.Unlock()
	if _, err := e.rpcSetBot(owner, testToken, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a poll from the beginning", func() bool {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		for _, o := range e.tg.offsets {
			if o == 0 {
				return true
			}
		}
		return false
	})
}
