package httpserver

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/descope/virtualwebauthn"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (b *browser) addPasskey(name string, cred virtualwebauthn.Credential) (*adminv1.Passkey, *virtualwebauthn.AttestationOptions, error) {
	ctx := context.Background()
	begin, err := b.api.BeginAddPasskey(ctx, connect.NewRequest(&adminv1.BeginAddPasskeyRequest{Name: name}))
	if err != nil {
		return nil, nil, err
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(begin.Msg.OptionsJson)
	if err != nil {
		b.t.Fatalf("options: %v", err)
	}
	b.dev.Options.UserHandle = []byte(opts.UserID)
	cj := virtualwebauthn.CreateAttestationResponse(b.rp, b.dev, cred, *opts)
	fin, err := b.api.FinishAddPasskey(ctx, connect.NewRequest(&adminv1.FinishAddPasskeyRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: cj}))
	if err != nil {
		return nil, opts, err
	}
	return fin.Msg.Passkey, opts, nil
}

func TestPasskeysSettings(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	base := e.admin.URL + "/"
	b := newBrowser(t, base, "")
	if err := b.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	list := func() []*adminv1.Passkey {
		t.Helper()
		r, err := b.api.ListPasskeys(ctx, connect.NewRequest(&adminv1.ListPasskeysRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Passkeys
	}
	cred1 := b.cred
	first := list()
	if len(first) != 1 || first[0].Name != "First passkey" || first[0].CreatedAtUnix == 0 || first[0].Id == "" {
		t.Fatalf("list: %+v", first)
	}

	// The only passkey of an admin without a password cannot be removed.
	if _, err := b.api.RemovePasskey(ctx, connect.NewRequest(&adminv1.RemovePasskeyRequest{Id: first[0].Id})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("removing the only way in: %v", err)
	}

	// Another passkey: excluded credentials in the options, verification required, then it works.
	cred2 := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	b.dev.Options.UserNotVerified = true
	if _, _, err := b.addPasskey("Unverified", cred2); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("adding a passkey without user verification: %v", err)
	}
	b.dev.Options.UserNotVerified = false
	pk, opts, err := b.addPasskey("  Phone ", cred2)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.ExcludeCredentials) != 1 || opts.ExcludeCredentials[0] != base64.RawURLEncoding.EncodeToString(b.cred.ID) {
		t.Errorf("existing passkey not excluded: %v", opts.ExcludeCredentials)
	}
	if pk.Name != "Phone" || pk.Id == "" {
		t.Fatalf("added passkey: %+v", pk)
	}
	if l := list(); len(l) != 2 || l[1].Id != pk.Id {
		t.Fatalf("list after add: %+v", l)
	}
	// The same authenticator cannot be registered twice.
	if _, _, err := b.addPasskey("Again", cred2); err == nil {
		t.Error("duplicate credential accepted")
	}
	b.logout()
	b.cred = cred2
	b.dev.AddCredential(cred2)
	if err := b.login(); err != nil {
		t.Fatalf("login with the added passkey: %v", err)
	}
	if l := list(); l[1].LastUsedAtUnix == 0 || l[0].LastUsedAtUnix != 0 {
		t.Errorf("last use not recorded on the right passkey: %+v", l)
	}

	// Ceremony checks: a passkey ceremony cannot be used for sign-in, and needs a session.
	begin, _ := b.api.BeginAddPasskey(ctx, connect.NewRequest(&adminv1.BeginAddPasskeyRequest{}))
	if _, err := b.api.FinishLogin(ctx, connect.NewRequest(&adminv1.FinishLoginRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: "{}"})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("add-passkey ceremony used to sign in: %v", err)
	}
	if _, err := b.api.BeginAddPasskey(ctx, connect.NewRequest(&adminv1.BeginAddPasskeyRequest{Name: strings.Repeat("n", 65)})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("long name: %v", err)
	}
	anon := newBrowser(t, base, "")
	if _, err := anon.api.ListPasskeys(ctx, connect.NewRequest(&adminv1.ListPasskeysRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous ListPasskeys: %v", err)
	}
	if _, err := anon.api.BeginAddPasskey(ctx, connect.NewRequest(&adminv1.BeginAddPasskeyRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous BeginAddPasskey: %v", err)
	}

	// Remove one, unknown ids, then the last one is protected again.
	if _, err := b.api.RemovePasskey(ctx, connect.NewRequest(&adminv1.RemovePasskeyRequest{Id: "pk_nope"})); code(err) != connect.CodeNotFound {
		t.Errorf("unknown passkey: %v", err)
	}
	if _, err := b.api.RemovePasskey(ctx, connect.NewRequest(&adminv1.RemovePasskeyRequest{Id: first[0].Id})); err != nil {
		t.Fatalf("removing one of two: %v", err)
	}
	if l := list(); len(l) != 1 || l[0].Id != pk.Id {
		t.Fatalf("list after remove: %+v", l)
	}
	if _, err := b.api.RemovePasskey(ctx, connect.NewRequest(&adminv1.RemovePasskeyRequest{Id: pk.Id})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("removing the last: %v", err)
	}
	// The removed passkey no longer signs anyone in.
	b.logout()
	b.cred = cred1
	if err := b.login(); code(err) != connect.CodeUnauthenticated {
		t.Errorf("removed passkey still signs in: %v", err)
	}
	b.cred = cred2
	if err := b.login(); err != nil {
		t.Errorf("remaining passkey: %v", err)
	}

	rows, _ := e.st.ListAudit(ctx, "", 0, 100)
	var added, removed int
	for _, r := range rows {
		if r.Action == "passkey_add" {
			added++
		}
		if r.Action == "passkey_remove" {
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("audit: %d adds, %d removes", added, removed)
	}
}

func TestSessionsSettings(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	base := e.admin.URL + "/"
	laptop := newBrowser(t, base, "")
	if err := laptop.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	laptopCookie := laptop.jar.cookie

	// A second browser signs in with the same passkey: two sessions.
	phone := newBrowser(t, base, "")
	phone.dev, phone.cred = laptop.dev, laptop.cred
	if err := phone.login(); err != nil {
		t.Fatal(err)
	}
	list := func(b *browser) []*adminv1.AdminSession {
		t.Helper()
		r, err := b.api.ListSessions(ctx, connect.NewRequest(&adminv1.ListSessionsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Sessions
	}
	sessions := list(laptop)
	if len(sessions) != 2 {
		t.Fatalf("sessions: %+v", sessions)
	}
	var cur, other *adminv1.AdminSession
	for _, s := range sessions {
		if s.Current {
			cur = s
		} else {
			other = s
		}
		if s.Id == "" || s.CreatedAtUnix == 0 || s.LastSeenAtUnix == 0 || s.Ip != "127.0.0.1" || strings.Contains(s.Id, strings.TrimPrefix(laptopCookie, auth.CookieName+"=")) {
			t.Errorf("session fields: %+v", s)
		}
	}
	if cur == nil || other == nil {
		t.Fatalf("exactly one session is current: %+v", sessions)
	}
	// From the phone, the roles swap.
	for _, s := range list(phone) {
		if s.Current == (s.Id == cur.Id) {
			t.Errorf("current flag from the phone: %+v", s)
		}
	}

	// A made-up id, and another admin's session id, end nothing.
	if _, err := laptop.api.EndSession(ctx, connect.NewRequest(&adminv1.EndSessionRequest{Id: "zz"})); code(err) != connect.CodeNotFound {
		t.Errorf("garbage id: %v", err)
	}
	if _, err := laptop.api.EndSession(ctx, connect.NewRequest(&adminv1.EndSessionRequest{Id: strings.Repeat("ab", 32)})); code(err) != connect.CodeNotFound {
		t.Errorf("unknown id: %v", err)
	}
	helper := &cookieJar{cookie: helperSession(t, e.st)}
	hb := newBrowserOn(t, base, helper)
	if _, err := hb.api.EndSession(ctx, connect.NewRequest(&adminv1.EndSessionRequest{Id: cur.Id})); code(err) != connect.CodeNotFound {
		t.Errorf("ending someone else's session: %v", err)
	}
	if _, err := laptop.me(); err != nil {
		t.Fatal("laptop session ended by another admin")
	}

	// End the phone's session from the laptop.
	if _, err := laptop.api.EndSession(ctx, connect.NewRequest(&adminv1.EndSessionRequest{Id: other.Id})); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.me(); code(err) != connect.CodeUnauthenticated {
		t.Errorf("ended session still works: %v", err)
	}
	if l := list(laptop); len(l) != 1 || !l[0].Current {
		t.Fatalf("after ending the phone: %+v", l)
	}

	// "End all others" leaves the caller alone.
	if err := phone.login(); err != nil {
		t.Fatal(err)
	}
	r, err := laptop.api.EndOtherSessions(ctx, connect.NewRequest(&adminv1.EndOtherSessionsRequest{}))
	if err != nil || r.Msg.Ended != 1 {
		t.Fatalf("EndOtherSessions: %+v %v", r, err)
	}
	if _, err := phone.me(); code(err) != connect.CodeUnauthenticated {
		t.Error("other session survived")
	}
	if _, err := laptop.me(); err != nil {
		t.Error("the caller was logged out by EndOtherSessions")
	}

	// Ending the current session logs out and clears the cookie.
	if _, err := laptop.api.EndSession(ctx, connect.NewRequest(&adminv1.EndSessionRequest{Id: list(laptop)[0].Id})); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.me(); code(err) != connect.CodeUnauthenticated {
		t.Error("session survived ending itself")
	}
	if laptop.jar.cookie != "" {
		t.Errorf("cookie not cleared: %q", laptop.jar.cookie)
	}
	// An anonymous caller gets nothing.
	if _, err := newBrowser(t, base, "").api.ListSessions(ctx, connect.NewRequest(&adminv1.ListSessionsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous ListSessions: %v", err)
	}
	// Expired sessions are not listed.
	if err := laptop.login(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-auth.SessionMaxAge - time.Hour).Unix()
	e.st.W.Exec(`INSERT INTO session (token_hash, admin_id, created_at, last_seen_at, expires_at) SELECT x'aa', id, ?, ?, ? FROM admin WHERE role = 'owner'`, old, old, old+10)
	for _, s := range list(laptop) {
		if s.CreatedAtUnix == old {
			t.Error("expired session listed")
		}
	}
}

func TestAuditSettings(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	base := e.admin.URL + "/"
	b := newBrowser(t, base, "")
	if err := b.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	// Rows from the other sources, as the bot, MCP server and API will write them.
	for i, src := range []string{store.AuditBot, store.AuditMCP, store.AuditAPI, store.AuditBot, store.AuditPanel} {
		if err := e.st.Audit(ctx, time.Now(), store.AuditEntry{Actor: "adm_x", Action: "act" + string(rune('0'+i)), Result: "ok", Source: src, IP: "203.0.113.9"}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(src adminv1.AuditSource, before int64, size uint32) *adminv1.ListAuditResponse {
		t.Helper()
		r, err := b.api.ListAudit(ctx, connect.NewRequest(&adminv1.ListAuditRequest{Source: src, BeforeId: before, PageSize: size}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	all := list(adminv1.AuditSource_AUDIT_SOURCE_UNSPECIFIED, 0, 0)
	if len(all.Entries) < 6 || all.NextBeforeId != 0 {
		t.Fatalf("all: %d entries, next %d", len(all.Entries), all.NextBeforeId)
	}
	if e0 := all.Entries[0]; e0.Action != "act4" || e0.Source != adminv1.AuditSource_AUDIT_SOURCE_PANEL || e0.Ip != "203.0.113.9" || e0.ActorId != "adm_x" || e0.TimeUnix == 0 || e0.ParamsJson != "{}" || e0.Result != "ok" {
		t.Errorf("newest entry: %+v", e0)
	}
	var setup *adminv1.AuditEntry
	for _, en := range all.Entries {
		if en.Action == "setup" {
			setup = en
		}
	}
	if setup == nil || setup.ActorName != "Ada" || setup.Ip != "127.0.0.1" {
		t.Errorf("setup entry: %+v", setup)
	}
	bot := list(adminv1.AuditSource_AUDIT_SOURCE_BOT, 0, 0)
	if len(bot.Entries) != 2 || bot.Entries[0].Action != "act3" || bot.Entries[1].Action != "act0" || bot.Entries[0].Source != adminv1.AuditSource_AUDIT_SOURCE_BOT {
		t.Errorf("bot filter: %+v", bot.Entries)
	}
	if n := len(list(adminv1.AuditSource_AUDIT_SOURCE_MCP, 0, 0).Entries); n != 1 {
		t.Errorf("mcp filter: %d", n)
	}
	// Paging: size 2, follow the cursor to the end, every entry once.
	seen := map[int64]bool{}
	var before int64
	for pages := 0; ; pages++ {
		p := list(adminv1.AuditSource_AUDIT_SOURCE_UNSPECIFIED, before, 2)
		if len(p.Entries) > 2 || pages > 20 {
			t.Fatalf("page %d has %d entries", pages, len(p.Entries))
		}
		for _, en := range p.Entries {
			if seen[en.Id] {
				t.Errorf("entry %d on two pages", en.Id)
			}
			seen[en.Id] = true
		}
		if p.NextBeforeId == 0 {
			break
		}
		before = p.NextBeforeId
	}
	if len(seen) != len(all.Entries) {
		t.Errorf("paging saw %d of %d entries", len(seen), len(all.Entries))
	}
	if n := len(list(adminv1.AuditSource_AUDIT_SOURCE_UNSPECIFIED, 0, 1000).Entries); n != len(all.Entries) {
		t.Errorf("page size above the cap: %d", n)
	}
	if _, err := b.api.ListAudit(ctx, connect.NewRequest(&adminv1.ListAuditRequest{Source: adminv1.AuditSource(99)})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown source: %v", err)
	}

	// Owner only; anonymous callers get nothing.
	hb := newBrowserOn(t, base, &cookieJar{cookie: helperSession(t, e.st)})
	if _, err := hb.api.ListAudit(ctx, connect.NewRequest(&adminv1.ListAuditRequest{})); code(err) != connect.CodePermissionDenied {
		t.Errorf("helper ListAudit: %v", err)
	}
	if _, err := newBrowser(t, base, "").api.ListAudit(ctx, connect.NewRequest(&adminv1.ListAuditRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous ListAudit: %v", err)
	}
}
