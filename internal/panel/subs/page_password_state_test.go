package subs_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The state of a person's page password (a generation and "removed", see package pagepass) against the real access module and
// database: "change" locks every earlier entry at once, "remove" opens the page on the link alone, "restore" asks again with a new
// password, a new link starts the generation again. The subscription itself is never touched.

func (g *gate) action(uid string, a adminv1.PagePasswordAction) *adminv1.User {
	return must(g.svc.UpdateUser(g.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{UserId: uid, PagePasswordAction: a}))).Msg.User
}

func TestChangeLocksEveryEarlierEntryAtOnceAndLeavesTheLinkAlone(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	old := g.password(uid)
	c := cookieOf(t, g.unlock(t, tok, old))
	call := call{g.h, t, tok}
	if rec := call.post("/devices", g.addBody("phone"), "Cookie", ck(c)); rec.Code != 200 {
		t.Fatalf("devices with the cookie before the change: %d %s", rec.Code, rec.Body.String())
	}
	if d := dataOf(t, g.page(tok, "Cookie", ck(c))); d["locked"] == true {
		t.Fatal("the page is locked with its cookie before the change")
	}

	u := g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE)
	if u.PageOpen {
		t.Error("a changed password is a removed one")
	}
	fresh := g.password(uid)
	if fresh == "" || fresh == old {
		t.Fatalf("the password after a change: %q (was %q)", fresh, old)
	}
	if want := pagepass.Password(pageKey(t), tok, 1); fresh != want {
		t.Errorf("the new password is not the one of generation 1: %q, want %q", fresh, want)
	}

	// the browser that entered the old password is locked at once: the page (served from the cached view) and the calls
	if d := dataOf(t, g.page(tok, "Cookie", ck(c))); d["locked"] != true {
		t.Error("the page stayed open for the cookie of the old password")
	}
	if rec := call.post("/devices", g.addBody("tablet"), "Cookie", ck(c)); rec.Code != 401 || decodeAnswer(t, rec).Error != "locked" {
		t.Errorf("devices with the old cookie: %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.unlock(t, tok, old); rec.Code != 401 {
		t.Errorf("the old password was taken: %d", rec.Code)
	}

	// the new password works and its cookie opens the page and the calls
	rec := g.unlock(t, tok, fresh)
	if rec.Code != 200 {
		t.Fatalf("the new password: %d %s", rec.Code, rec.Body.String())
	}
	nc := cookieOf(t, rec)
	if nc.Value == c.Value || !strings.HasPrefix(nc.Value, "1.") {
		t.Errorf("the cookie of generation 1: %q", nc.Value)
	}
	if d := dataOf(t, g.page(tok, "Cookie", ck(nc))); d["locked"] == true {
		t.Error("the page is locked for the cookie of the new password")
	}
	if rec := call.post("/devices", g.addBody("tablet"), "Cookie", ck(nc)); rec.Code != 200 {
		t.Errorf("devices with the new cookie: %d %s", rec.Code, rec.Body.String())
	}

	// the link and the apps are untouched
	if rec := fetch(g.h, "/"+tok, mihomoUA); rec.Code != 200 || !strings.Contains(rec.Body.String(), "proxies") {
		t.Errorf("the subscription after a change: %d", rec.Code)
	}
	link := must(g.svc.GetSubscriptionLink(g.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid}))).Msg
	if !strings.HasSuffix(link.Url, "/"+tok) {
		t.Error("the link changed with the password")
	}
	// a second change is a third password
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE)
	if third := g.password(uid); third == fresh || third == old || third != pagepass.Password(pageKey(t), tok, 2) {
		t.Errorf("generation 2: %q", third)
	}
}

func TestARemovedPasswordOpensThePageAndItsCallsOnTheLinkAlone(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	other, otherTok := g.newUser("bob", nil)
	old := g.password(uid)

	u := g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)
	if !u.PageOpen {
		t.Fatal("the user does not say that the password was removed")
	}
	if pw := g.password(uid); pw != "" {
		t.Errorf("a removed password is still shown: %q", pw)
	}
	// no cookie, no question
	rec := g.page(tok)
	if d := dataOf(t, rec); d["locked"] == true || d["subscription_url"] == "" {
		t.Errorf("the page of a person with no password is locked: %v", d["locked"])
	}
	call := call{g.h, t, tok}
	if rec := call.post("/devices", g.addBody("phone")); rec.Code != 200 {
		t.Errorf("a call without the cookie on a removed password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.unlock(t, tok, "aaaa-bbbb"); rec.Code != 200 || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("unlock of an open page: %d %v", rec.Code, rec.Header())
	}
	// somebody else's page still asks
	if d := dataOf(t, g.page(otherTok)); d["locked"] != true {
		t.Error("removing one person's password opened another person's page")
	}
	if rec := (call.withToken(otherTok)).post("/devices", g.addBody("phone")); rec.Code != 401 {
		t.Errorf("another person's call: %d", rec.Code)
	}
	_ = other

	// a repeat does nothing, and a password that is not there cannot be changed
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)
	_, err := g.svc.UpdateUser(g.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{UserId: uid, PagePasswordAction: adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "page_password_removed") {
		t.Errorf("a change of a removed password: %v", err)
	}

	// restore: asked again, with a new password; the old one does not come back
	u = g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_RESTORE)
	if u.PageOpen {
		t.Error("restored, and still open")
	}
	back := g.password(uid)
	if back == "" || back == old || back != pagepass.Password(pageKey(t), tok, 1) {
		t.Errorf("the password after a restore: %q (was %q)", back, old)
	}
	if d := dataOf(t, g.page(tok)); d["locked"] != true {
		t.Error("the page is open after a restore")
	}
	if rec := g.unlock(t, tok, old); rec.Code != 401 {
		t.Errorf("the password from before the removal came back: %d", rec.Code)
	}
	if rec := g.unlock(t, tok, back); rec.Code != 200 {
		t.Errorf("the restored password: %d", rec.Code)
	}
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_RESTORE) // a repeat does nothing
	if g.password(uid) != back {
		t.Error("a repeated restore changed the password")
	}
}

func TestACookieOfBeforeARemovalAndRestoreIsLocked(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	c := cookieOf(t, g.unlock(t, tok, g.password(uid)))
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_RESTORE)
	if d := dataOf(t, g.page(tok, "Cookie", ck(c))); d["locked"] != true {
		t.Error("the cookie of the first password opened the page again after a removal and a restore")
	}
	if rec := (call{g.h, t, tok}).post("/devices", g.addBody("phone"), "Cookie", ck(c)); rec.Code != 401 {
		t.Errorf("the old cookie on a call: %d", rec.Code)
	}
}

func (g *gate) instanceAsks(on bool) {
	g.t.Helper()
	set := subsettings.Defaults()
	set.UserPage.RequirePagePassword = proto.Bool(on)
	if _, err := g.cache.Update(g.ctx, set); err != nil {
		g.t.Fatal(err)
	}
}

func TestTheInstanceSwitchStaysTheMainOne(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	// off for the whole instance: nobody is asked, nobody is shown a password, whatever their own state is
	g.instanceAsks(false)
	if pw := g.password(uid); pw != "" {
		t.Errorf("a password is shown with the instance switch off: %q", pw)
	}
	if d := dataOf(t, g.page(tok)); d["locked"] == true {
		t.Error("the page asks for a password with the instance switch off")
	}
	if rec := (call{g.h, t, tok}).post("/devices", g.addBody("phone")); rec.Code != 200 {
		t.Errorf("a call with the instance switch off: %d %s", rec.Code, rec.Body.String())
	}
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE) // a change is still allowed and counted
	g.now = g.now.Add(11 * time.Second)
	if d := dataOf(t, g.page(tok)); d["locked"] == true {
		t.Error("a change of the person's password locked the page of an instance that asks for none")
	}
	g.instanceAsks(true)
	if d := dataOf(t, g.page(tok)); d["locked"] != true {
		t.Error("the page is open with the instance switch back on")
	}
	// a person whose password was removed stays open when the instance asks again, and asked again after a restore
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)
	g.now = g.now.Add(11 * time.Second)
	if d := dataOf(t, g.page(tok)); d["locked"] == true {
		t.Error("a removed password was asked for")
	}
}

func TestANewLinkStartsTheGenerationAgainAndKeepsTheRemovedFlag(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE)
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE)
	if g.password(uid) != pagepass.Password(pageKey(t), tok, 2) {
		t.Fatal("generation 2 expected")
	}
	rot := must(g.svc.GetSubscriptionLink(g.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true}))).Msg
	newTok := rot.Url[strings.LastIndex(rot.Url, "/")+1:]
	if newTok == tok {
		t.Fatal("the link did not change")
	}
	if rot.PagePassword != pagepass.Password(pageKey(t), newTok, 0) || g.password(uid) != rot.PagePassword {
		t.Errorf("a new link must start at generation 0: %q", rot.PagePassword)
	}
	if d := dataOf(t, g.page(newTok)); d["locked"] != true {
		t.Error("the new link does not ask for the password")
	}

	// removed stays removed across a new link
	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)
	rot = must(g.svc.GetSubscriptionLink(g.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true}))).Msg
	third := rot.Url[strings.LastIndex(rot.Url, "/")+1:]
	if rot.PagePassword != "" {
		t.Errorf("a removed password is shown after a new link: %q", rot.PagePassword)
	}
	if d := dataOf(t, g.page(third)); d["locked"] == true {
		t.Error("a new link of a person with no password asks for one")
	}
}

// The cookie that existed before generations (the bare HMAC of a generation 0 password) keeps working for everyone who was
// never touched: nothing changes for the people the migration finds.
func TestAnExistingCookieKeepsWorkingForAnUntouchedPerson(t *testing.T) {
	g := newGate(t, nil)
	_, tok := g.newUser("alice", nil)
	old := &http.Cookie{Name: "mg_page", Value: pagepass.Cookie(pageKey(t), tok, 0)}
	if strings.Contains(old.Value, ".") {
		t.Fatalf("the cookie of generation 0 changed its shape: %q", old.Value)
	}
	if d := dataOf(t, g.page(tok, "Cookie", ck(old))); d["locked"] == true {
		t.Error("the bare cookie of generation 0 is refused")
	}
	if rec := (call{g.h, t, tok}).post("/devices", g.addBody("phone"), "Cookie", ck(old)); rec.Code != 200 {
		t.Errorf("the bare cookie on a call: %d %s", rec.Code, rec.Body.String())
	}
	forged := &http.Cookie{Name: "mg_page", Value: "5." + strings.TrimPrefix(pagepass.Cookie(pageKey(t), tok, 5), "5.")[:10] + "xxxx"}
	if rec := (call{g.h, t, tok}).post("/devices", g.addBody("phone"), "Cookie", ck(forged)); rec.Code != 401 {
		t.Errorf("a forged cookie: %d", rec.Code)
	}
}

func (c call) withToken(token string) call { c.token = token; return c }
