package access

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
)

// The page password of a person through the API: UpdateUser's page_password_action changes, removes and restores it; the password
// itself is computed from the link and the generation, so the database and the audit hold neither it nor a hash of it.

func pageAction(id string, a adminv1.PagePasswordAction) *connect.Request[adminv1.UpdateUserRequest] {
	return connect.NewRequest(&adminv1.UpdateUserRequest{UserId: id, PagePasswordAction: a})
}

func (e *env) pagePassword(id string) string {
	e.t.Helper()
	return must(e.s.GetSubscriptionLink(e.ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: id}))).Msg.PagePassword
}

func TestPagePasswordChangeRemoveRestoreThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	e.node("nod_1", "de1", "de1.example.com", "active")
	g := e.group("g", e.profile("hy", "").Id)
	u := e.user("alice", g, nil).User
	tok := e.tokenOf(u.Id)
	key := e.s.vault.Derive(pagepass.KeyLabel)

	// as it was: generation 0, the password every person had, and the user does not say it was removed
	first := e.pagePassword(u.Id)
	if first != pagepass.Password(key, tok, 0) || u.PageOpen {
		t.Fatalf("the starting state: %q open=%v", first, u.PageOpen)
	}

	// a dry run writes nothing
	dry := pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE)
	dry.Msg.DryRun = true
	must(e.s.UpdateUser(e.ctx, dry))
	if e.pagePassword(u.Id) != first {
		t.Error("a dry run changed the password")
	}

	// change: a new password for the same link
	got := must(e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE))).Msg.User
	second := e.pagePassword(u.Id)
	if got.PageOpen || second == first || second != pagepass.Password(key, tok, 1) {
		t.Errorf("after a change: %q (was %q) open=%v", second, first, got.PageOpen)
	}
	if e.tokenOf(u.Id) != tok {
		t.Error("the link changed with the password")
	}

	// remove: nothing is shown, the user says so; a repeat does nothing; a change has nothing to change
	got = must(e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE))).Msg.User
	if !got.PageOpen || e.pagePassword(u.Id) != "" {
		t.Errorf("after a removal: open=%v password=%q", got.PageOpen, e.pagePassword(u.Id))
	}
	must(e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)))
	_, err := e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "page_password_removed") {
		t.Errorf("a change of a removed password: %v", err)
	}
	_, err = e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction(99)))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown action: %v", err)
	}

	// restore: asked again, a new password; neither earlier one comes back
	got = must(e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_RESTORE))).Msg.User
	third := e.pagePassword(u.Id)
	if got.PageOpen || third == first || third == second || third != pagepass.Password(key, tok, 2) {
		t.Errorf("after a restore: %q (earlier %q, %q) open=%v", third, first, second, got.PageOpen)
	}

	// a person that does not exist
	_, err = e.s.UpdateUser(e.ctx, pageAction("usr_nobody", adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an unknown person: %v", err)
	}

	// a new link: the generation starts again, the removed flag stays as it was
	rot := must(e.s.GetSubscriptionLink(e.ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: u.Id, Rotate: true}))).Msg
	newTok := rot.Url[strings.LastIndex(rot.Url, "/")+1:]
	if rot.PagePassword != pagepass.Password(key, newTok, 0) {
		t.Errorf("the password of a new link: %q", rot.PagePassword)
	}
	must(e.s.UpdateUser(e.ctx, pageAction(u.Id, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)))
	rot = must(e.s.GetSubscriptionLink(e.ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: u.Id, Rotate: true}))).Msg
	if rot.PagePassword != "" || !must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.User.PageOpen {
		t.Errorf("a new link of a person with no password: %q", rot.PagePassword)
	}
}

func TestPagePasswordIsNeverStoredOrAudited(t *testing.T) {
	e := newEnv(t)
	e.node("nod_1", "de1", "de1.example.com", "active")
	g := e.group("g", e.profile("hy", "").Id)
	u := e.user("alice", g, nil).User
	tok := e.tokenOf(u.Id)
	key := e.s.vault.Derive(pagepass.KeyLabel)
	var shown []string
	for _, a := range []adminv1.PagePasswordAction{
		adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_RESTORE,
	} {
		must(e.s.UpdateUser(e.ctx, pageAction(u.Id, a)))
		shown = append(shown, pagepass.Password(key, tok, 0), pagepass.Password(key, tok, 1), pagepass.Password(key, tok, 2))
	}
	words := map[string]bool{}
	for _, row := range e.audited("user_update") {
		if strings.Contains(row.Params, `"page_password"`) {
			for _, w := range []string{"changed", "removed", "restored"} {
				if strings.Contains(row.Params, `"page_password":"`+w+`"`) {
					words[w] = true
				}
			}
		}
		for _, pw := range shown {
			if strings.Contains(row.Params, pw) || strings.Contains(row.Params, strings.ReplaceAll(pw, "-", "")) {
				t.Errorf("the audit row holds a password: %s", row.Params)
			}
		}
	}
	if len(words) != 3 {
		t.Errorf("the audit says %v, want changed, removed and restored", words)
	}
	// the row holds a generation and a flag and nothing that could be a password or its hash
	var gen, open int
	if err := e.st.R.QueryRow(`SELECT page_pass_gen, page_open FROM user WHERE id = ?`, u.Id).Scan(&gen, &open); err != nil || gen != 2 || open != 0 {
		t.Errorf("the state of the row: gen %d, open %d, %v", gen, open, err)
	}
	var cols []string
	rows, err := e.st.R.Query(`SELECT name FROM pragma_table_info('user')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		rows.Scan(&n)
		cols = append(cols, n)
	}
	for _, c := range cols {
		if strings.Contains(c, "pass") && c != "page_pass_gen" {
			t.Errorf("a column that may hold a password: %s", c)
		}
	}
}

// A token changes a password like any other change of a person: one unit of its hourly limit, and a repeat that changes nothing is
// free. It never gets a password back: the response carries none and GetSubscriptionLink is closed to it.
func TestTokenPagePasswordChangesCountAsUserChanges(t *testing.T) {
	c := newCapEnv(t)
	ctx := c.e.ctx
	ids := c.users(1)
	id, api := c.token("ci", false)

	resp, err := api.UpdateUser(ctx, pageAction(ids[0], adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_CHANGE))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != TokenUserChangeLimit-1 {
		t.Errorf("a change of the password: %d units left, want %d", got, TokenUserChangeLimit-1)
	}
	if strings.Contains(strings.ToLower(resp.Msg.String()), "password") {
		t.Errorf("the response to a token mentions a password: %s", resp.Msg)
	}
	if _, err := api.UpdateUser(ctx, pageAction(ids[0], adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateUser(ctx, pageAction(ids[0], adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)); err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != TokenUserChangeLimit-2 {
		t.Errorf("a removal and its repeat: %d units left, want %d", got, TokenUserChangeLimit-2)
	}
	var open int
	if err := c.e.st.R.QueryRow(`SELECT page_open FROM user WHERE id = ?`, ids[0]).Scan(&open); err != nil || open != 1 {
		t.Errorf("the removal did not reach the row: %d %v", open, err)
	}
}
