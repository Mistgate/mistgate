package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// otherIsolate is a second edge isolate on the same store as e.svc.
func (e *env) otherIsolate() *Service {
	e.t.Helper()
	s, err := New(Config{Store: e.st, Vault: e.svc.vault, StepUp: e.svc.cfg.StepUp, APIBase: e.tg.srv.URL, Now: e.clk.now, Edge: true,
		RetryBase: 20 * time.Millisecond})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// Open in isolate A, resolve in isolate B, re-open in A: all three are news. A per-isolate "already open" map would drop the
// third; on the edge the alert row and the evaluator's lease decide what is a transition.
func TestEdgeReopenedAlertIsAnnouncedAcrossIsolates(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Edge = true })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.link(owner, 1)
	b := e.otherIsolate()
	ctx := context.Background()
	a := alert("node_down", "nod_1", 3)
	e.svc.AlertTransition(a, false)
	e.svc.FlushNow(ctx)
	r := a
	r.ResolvedAt, r.Resolution = e.clk.now(), "cleared"
	b.AlertTransition(r, true)
	b.FlushNow(ctx)
	e.svc.AlertTransition(a, false)
	e.svc.FlushNow(ctx)
	if got := e.tg.total(); got != 3 {
		t.Fatalf("messages = %d, want 3 (open, resolved, re-opened)", got)
	}
}

// The VPS keeps its map: a repeat of an open alert is one message.
func TestVPSKeepsTheOpenAlertMap(t *testing.T) {
	e := newEnv(t)
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.link(owner, 1)
	a := alert("node_down", "nod_1", 3)
	e.svc.AlertTransition(a, false)
	e.svc.AlertTransition(a, false)
	e.svc.FlushNow(context.Background())
	if got := e.tg.total(); got != 1 {
		t.Fatalf("VPS messages = %d, want 1", got)
	}
}

// No bot is the same on both editions: the news is dropped, not kept for ever.
func TestEdgeFlushWithoutABotDropsTheQueue(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Edge = true })
	e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
	e.svc.FlushNow(context.Background())
	e.svc.mu.Lock()
	n := len(e.svc.pending)
	e.svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("pending after a flush with no bot = %d, want 0", n)
	}
}

// A refusal (the user blocked the bot, a deleted chat) cannot be cured by waiting: dropped on both editions.
func TestFlushDoesNotRetryARefusedChat(t *testing.T) {
	for _, edge := range []bool{false, true} {
		t.Run(fmt.Sprintf("edge=%v", edge), func(t *testing.T) {
			e := newEnv(t, func(c *Config) { c.Edge = edge })
			owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
			e.setBot()
			e.link(owner, 79)
			e.tg.mu.Lock()
			for i := 0; i < 5; i++ {
				e.tg.sendPlan = append(e.tg.sendPlan, planned{status: http.StatusForbidden, desc: "Forbidden: bot was blocked by the user"})
			}
			e.tg.mu.Unlock()
			e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
			for i := 0; i < 5; i++ {
				e.svc.FlushNow(context.Background())
			}
			if got := e.tg.count("sendMessage"); got != 1 {
				t.Fatalf("sendMessage calls to a blocked chat = %d, want 1", got)
			}
		})
	}
}

// When the retries are used up the edge keeps the message for the next tick; the VPS drops it, as it always did.
func TestRetriesUsedUpRequeueOnTheEdgeOnly(t *testing.T) {
	for _, edge := range []bool{false, true} {
		t.Run(fmt.Sprintf("edge=%v", edge), func(t *testing.T) {
			e := newEnv(t, func(c *Config) { c.Edge = edge; c.RetryMax = 1 })
			owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
			e.setBot()
			e.link(owner, 79)
			e.tg.mu.Lock()
			e.tg.sendPlan = append(e.tg.sendPlan, planned{status: http.StatusInternalServerError, desc: "temporary"})
			e.tg.mu.Unlock()
			e.svc.AlertTransition(alert("node_down", "nod_1", 3), false)
			e.svc.FlushNow(context.Background())
			e.svc.FlushNow(context.Background())
			want := 0
			if edge {
				want = 1
			}
			if got := e.tg.total(); got != want {
				t.Fatalf("messages after the retry flush = %d, want %d", got, want)
			}
		})
	}
}

// The edge has no send loop, so every producer must schedule its own flush. The auth hook runs in the admin isolate, and a
// lockout or a new sign-in is exactly the news that must not wait for a cron tick.
func TestEdgeSecurityEventsFlushAfterResponse(t *testing.T) {
	var after []func()
	e := newEnv(t, func(c *Config) { c.Edge = true; c.AfterResponse = func(w func()) { after = append(after, w) } })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.link(owner, 5)
	e.svc.Security(auth.Event{Kind: auth.EventLockout, Login: "alice", IP: "203.0.113.5", Until: e.clk.now().Add(time.Hour)})
	if len(after) != 1 {
		t.Fatalf("a lockout scheduled %d after-response tasks, want 1", len(after))
	}
	e.svc.Security(auth.Event{Kind: auth.EventSignIn, AdminID: owner.ID, Method: "passkey", IP: "203.0.113.5"})
	if len(after) != 2 {
		t.Fatalf("a new-address sign-in scheduled %d tasks in all, want 2", len(after))
	}
	for _, w := range after {
		w()
	}
	if e.tg.total() == 0 {
		t.Fatal("the after-response tasks sent nothing")
	}
	e.svc.mu.Lock()
	n := len(e.svc.pending)
	e.svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d messages still queued after the tasks ran", n)
	}
}

// A transition from the admin isolate (COUNTRY_BLOCKED from ReconcileCountry arrives this way) reaches Telegram too.
func TestEdgeAlertTransitionFlushesAfterResponse(t *testing.T) {
	var after []func()
	e := newEnv(t, func(c *Config) { c.Edge = true; c.AfterResponse = func(w func()) { after = append(after, w) } })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	e.link(owner, 6)
	e.svc.AlertTransition(alert("country_blocked", "nod_1", 2), false)
	if len(after) != 1 {
		t.Fatalf("an alert scheduled %d after-response tasks, want 1", len(after))
	}
	after[0]()
	if got := e.tg.sentTo(6); len(got) != 1 {
		t.Fatalf("the alert was sent %d times, want once", len(got))
	}
}

// On the VPS nothing is scheduled: the send loop owns delivery.
func TestVPSEnqueueSchedulesNothing(t *testing.T) {
	n := 0
	e := newEnv(t, func(c *Config) { c.AfterResponse = func(func()) { n++ } })
	e.svc.PlanWaiting("node_install", "alice")
	e.svc.Security(auth.Event{Kind: auth.EventLockout, Login: "alice"})
	if n != 0 {
		t.Fatalf("the VPS scheduled %d after-response tasks", n)
	}
}

// A chat is linked by the edge poll: the update carrying the code binds the chat, the offset is saved, and with the code
// consumed the next tick does not poll Telegram at all.
func TestEdgePollLinksAChat(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Edge = true })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	ctx := context.Background()
	code, _, err := e.svc.newCode(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.tg.say(7, "/start "+code)
	if err := e.svc.PollPendingLinkCode(ctx); err != nil {
		t.Fatal(err)
	}
	link, err := e.st.TelegramLink(ctx, owner.ID)
	if err != nil || link.ChatID != 7 {
		t.Fatalf("link after the poll = %+v, %v; want chat 7", link, err)
	}
	if got := e.tg.sentTo(7); len(got) != 1 {
		t.Fatalf("the linked chat got %d replies, want 1", len(got))
	}
	off, err := e.st.Setting(ctx, offsetKey)
	if want := strconv.FormatInt(e.tg.nextID+1, 10); err != nil || off != want {
		t.Fatalf("saved offset = %q, %v; want %s", off, err, want)
	}
	polls := e.tg.count("getUpdates")
	if err := e.svc.PollPendingLinkCode(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.tg.count("getUpdates"); got != polls {
		t.Fatalf("getUpdates calls %d -> %d: a poll ran with no pending code", polls, got)
	}
}

// Another isolate replaced the bot while this poll was in flight: its updates belong to the old bot, so nothing is bound and
// the offset is not moved (the new bot starts at 0).
func TestEdgePollIgnoresUpdatesWhenTheBotWasReplaced(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Edge = true })
	owner := e.addAdmin("adm_o", "Owner", store.RoleOwner)
	e.setBot()
	ctx := context.Background()
	code, _, err := e.svc.newCode(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The replacement drops the pending codes with the old bot; the code is put back so that only the poll's own check
		// stands between the stale update and a link.
		b := store.TelegramBotRow{Token: e.svc.vault.Seal([]byte(testToken), tokenRecordID), BotID: e.tg.botID + 1, Username: "other_bot"}
		if _, err := e.st.SetTelegramBot(context.Background(), b, e.clk.now()); err != nil {
			t.Error(err)
		}
		if err := e.st.SetTelegramLinkCode(context.Background(), codeHash(code), owner.ID, e.clk.now().Add(time.Hour)); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":500,"message":{"chat":{"id":9,"type":"private"},"text":"/start %s"}}]}`, code)
	}))
	defer srv.Close()
	e.svc.api.base = srv.URL
	if err := e.svc.PollPendingLinkCode(ctx); err != nil {
		t.Fatal(err)
	}
	if link, err := e.st.TelegramLink(ctx, owner.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stale update linked a chat to the new bot: %+v, %v", link, err)
	}
	if off, err := e.st.Setting(ctx, offsetKey); err == nil && off != "0" {
		t.Fatalf("offset after a stale poll = %q, want it untouched", off)
	}
	if got := e.tg.sentTo(9); len(got) != 0 {
		t.Fatalf("the stale chat got a reply: %v", got)
	}
}
