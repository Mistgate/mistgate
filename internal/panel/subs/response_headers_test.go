package subs_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subhdr"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The owner's own response headers (SubscriptionSettings.response_headers): which answers carry them, and which names the
// panel keeps for itself.

// hv reads an own header by the exact name: the recorder keeps the map as the handler built it, and Get would canonicalize.
func hv(h http.Header, name string) string { return strings.Join(h[name], ",") }

func ownHeader(name, value string, formats []adminv1.SubFormat, groups ...string) *adminv1.ResponseHeader {
	return &adminv1.ResponseHeader{Name: name, Value: value, Formats: formats, OnlyGroups: len(groups) > 0, GroupIds: groups}
}

func TestOwnResponseHeadersGoWithASuccessfulAnswerOfTheirScope(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1; c.AfterResponse = func(f func()) { f() } })
	_, in := m.newUser("alice", nil) // group g2
	_, out := m.user("bob", nil)     // the other group
	set := subsettings.Defaults()
	set.ResponseHeaders = []*adminv1.ResponseHeader{
		ownHeader("flclashx-servicename", "My VPN", nil),
		{Name: "flclashx-view", Value: "Мой VPN", Base64: true, Formats: []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML}},
		ownHeader("X-Only-G2", "g2", nil, m.group2),
		ownHeader("X-Only-Singbox", "s", []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON}),
		ownHeader("X-Twice", "first", nil),
		ownHeader("x-twice", "second", nil),
	}
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	got := func(token, ua string, extra ...string) http.Header { return fetch(h, "/"+token, ua, extra...).Header() }

	// A link list: the one for every format and the one for the group; not the Mihomo-only, not the sing-box-only.
	hd := got(in, happUA)
	if hv(hd, "flclashx-servicename") != "My VPN" || hv(hd, "X-Only-G2") != "g2" || hv(hd, "flclashx-view") != "" || hv(hd, "X-Only-Singbox") != "" {
		t.Errorf("link list: %v", hd)
	}
	// The name goes out as the owner wrote it, and when two entries share a name the first wins.
	if v, ok := hd["flclashx-servicename"]; !ok || len(v) != 1 {
		t.Errorf("the name was canonicalized or repeated: %v", hd)
	}
	if v := hd["X-Twice"]; len(v) != 1 || v[0] != "first" || len(hd["x-twice"]) != 0 {
		t.Errorf("X-Twice: %v", hd)
	}
	// Mihomo: base64 the way the panel sends the title.
	hd = got(in, mihomoUA)
	if unb64(t, hv(hd, "flclashx-view")) != "Мой VPN" || hv(hd, "flclashx-servicename") != "My VPN" || hv(hd, "X-Only-Singbox") != "" {
		t.Errorf("mihomo: %v", hd)
	}
	// sing-box.
	if hd = got(in, sfaUA); hv(hd, "X-Only-Singbox") != "s" || hv(hd, "flclashx-view") != "" {
		t.Errorf("sing-box: %v", hd)
	}
	// The other group: no group header.
	if hd = got(out, happUA); hv(hd, "X-Only-G2") != "" || hv(hd, "flclashx-servicename") != "My VPN" {
		t.Errorf("other group: %v", hd)
	}
	// A browser gets the page, not a subscription.
	if hd = got(in, chrome); hv(hd, "flclashx-servicename") != "" {
		t.Errorf("the page carries an own header: %v", hd)
	}
	// No access: the answer is a note, with none of them.
	id, off := m.user("carol", nil)
	m.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, id)
	if err := m.svc.Recompute(m.ctx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if hd = got(off, happUA); hv(hd, "flclashx-servicename") != "" || hd.Get("Profile-Title") == "" {
		t.Errorf("a person without access: %v", hd)
	}
}

func TestOwnResponseHeadersAreNotOnARefusal(t *testing.T) {
	r, h, _, tok := refusalRig(t, store.LinkModeEnforce, 1)
	set := subsettings.Defaults()
	set.ResponseHeaders = []*adminv1.ResponseHeader{ownHeader("flclashx-servicename", "My VPN", nil)}
	if _, err := subsettings.NewCache(r.st, nil).Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	if hd := fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Header(); hv(hd, "flclashx-servicename") != "My VPN" {
		t.Fatalf("the first device is served: %v", hd)
	}
	for _, ua := range []string{happUA, mihomoUA} {
		rec := fetch(h, "/"+tok, ua, "X-HWID", hwidB)
		if rec.Header().Get("X-Hwid-Max-Devices-Reached") != "true" || hv(rec.Header(), "flclashx-servicename") != "" {
			t.Errorf("%s: a refused device must keep the refusal as it is: %v", ua, rec.Header())
		}
	}
}

// Every header an answer of the endpoint carries is one the owner may not name, whatever the answer is.
func TestEveryHeaderOfAnAnswerIsReserved(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) {
		c.MinInterval = -1
		c.MaxPerHour = -1
		c.AfterResponse = func(f func()) { f() }
	})
	set := subsettings.Defaults()
	set.HappPing, set.HappPingOnOpen = adminv1.HappPing_HAPP_PING_TCP, true
	set.SupportUrl, set.Announcement = "https://example.com/help", "Hello"
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 1 })
	m.setLinkMode(store.LinkModeStrict)
	seen := map[string]bool{}
	note := func(rec *httptest.ResponseRecorder) {
		for k := range rec.Header() {
			seen[k] = true
		}
	}
	for _, ua := range []string{happUA, mihomoUA, v2rayNGUA, sfaUA, curlUA, chrome, "", "Slackbot-LinkExpanding 1.0"} {
		for _, hw := range [][]string{nil, {"X-HWID", hwidA}, {"X-HWID", hwidB}, {"X-HWID", "bad"}, {"X-Mistgate-Caps", "1"}} {
			note(fetch(h, "/"+tok, ua, hw...))
		}
	}
	note(fetch(h, "/"+unknown, happUA)) // the decoy
	note(fetch(h, "/"+tok+"?format=xray-json", happUA))
	note(fetch(h, "/"+tok, mihomoUA, "Accept-Encoding", "gzip"))
	if !seen["Profile-Title"] || !seen["Content-Disposition"] || !seen["X-Hwid-Active"] || !seen["Ping-Type"] {
		t.Fatalf("the matrix did not reach the answers it is for: %v", seen)
	}
	for name := range seen {
		if !subhdr.Reserved(name) {
			t.Errorf("an answer carries %q, which an own header could be named: add it to subhdr (and set it through the constant)", name)
		}
	}
}

// Reading the source of this package: a header is named only through a subhdr constant that is in subhdr.Panel, so that the list
// of the names an owner may not take is the list of what this code sets, and a new header cannot be added without being in it.
func TestHeadersAreSetOnlyThroughSubhdr(t *testing.T) {
	consts := panelConsts(t)
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files: %v", err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// key checks the name of a header at a place that sets or removes a response header.
		key := func(e ast.Expr) {
			checked++
			sel, ok := e.(*ast.SelectorExpr)
			if !ok {
				t.Errorf("%s: a response header is named by something else than a subhdr constant (add it to internal/panel/subhdr and use it)", fset.Position(e.Pos()))
				return
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "subhdr" {
				t.Errorf("%s: a response header is named by something else than a subhdr constant (add it to internal/panel/subhdr and use it)", fset.Position(e.Pos()))
				return
			}
			if v, ok := consts[sel.Sel.Name]; !ok || !slices.Contains(subhdr.Panel, v) {
				t.Errorf("%s: subhdr.%s is not in subhdr.Panel", fset.Position(e.Pos()), sel.Sel.Name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if s, ok := n.Fun.(*ast.SelectorExpr); ok && (s.Sel.Name == "Set" || s.Sel.Name == "Add" || s.Sel.Name == "Del") && headerMap(s.X) && len(n.Args) > 0 {
					key(n.Args[0])
				}
			case *ast.IndexExpr:
				if headerMap(n.X) {
					key(n.Index)
				}
			case *ast.AssignStmt:
				// the map of a response is held under one name, which the checks above know
				for i, rhs := range n.Rhs {
					if c, ok := rhs.(*ast.CallExpr); ok && isHeaderCall(c) && len(n.Lhs) == len(n.Rhs) {
						if id, ok := n.Lhs[i].(*ast.Ident); !ok || id.Name != "hd" {
							t.Errorf("%s: hold the response header map as hd, so that its names are checked", fset.Position(n.Pos()))
						}
					}
				}
			}
			return true
		})
	}
	if checked < 30 {
		t.Fatalf("only %d header names were checked: the scan no longer sees the code", checked)
	}
}

func isHeaderCall(c *ast.CallExpr) bool {
	s, ok := c.Fun.(*ast.SelectorExpr)
	return ok && s.Sel.Name == "Header" && len(c.Args) == 0
}

// headerMap reports an expression that is a response's header map: hd, or <something>.Header().
func headerMap(e ast.Expr) bool {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name == "hd"
	}
	c, ok := e.(*ast.CallExpr)
	return ok && isHeaderCall(c)
}

// panelConsts reads the string constants of subhdr.go: name -> value.
func panelConsts(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "subhdr", "subhdr.go"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "subhdr.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
			for _, sp := range g.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						out[n.Name] = strings.Trim(lit.Value, `"`)
					}
				}
			}
		}
	}
	return out
}

func headerService(m *m3rig) *subs.Service {
	return subs.NewService(m.st, subsettings.NewCache(m.st, nil), builtin.Registry(),
		func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, m.st) }, nil, nil)
}

func TestPreviewResponseHeaders(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	alice, _ := m.newUser("alice", nil)
	list := []*adminv1.ResponseHeader{
		ownHeader("flclashx-servicename", "My VPN", nil),
		ownHeader("flclashx-special", "providermode", []adminv1.SubFormat{adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML}, m.group2),
	}
	preview := func(uid, ua string, l []*adminv1.ResponseHeader) (*adminv1.PreviewResponseHeadersResponse, error) {
		r, err := svc.PreviewResponseHeaders(m.ctx, connect.NewRequest(&adminv1.PreviewResponseHeadersRequest{UserId: uid, UserAgent: ua, Headers: l}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	r, err := preview(alice, mihomoUA, list)
	if err != nil || len(r.Headers) != 2 || r.Headers[1].Name != "flclashx-special" || r.Format != adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML || r.GroupName != "g2" || r.NotServed != "" {
		t.Fatalf("mihomo: %+v, %v", r, err)
	}
	if r, err = preview(alice, happUA, list); err != nil || len(r.Headers) != 1 || r.Headers[0].Name != "flclashx-servicename" {
		t.Fatalf("link list: %+v, %v", r, err)
	}
	if r, err = preview(alice, chrome, list); err != nil || r.NotServed != "page" || len(r.Headers) != 0 {
		t.Fatalf("browser: %+v, %v", r, err)
	}
	if _, err = preview("usr_nobody", happUA, list); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown person: %v", err)
	}
	if _, err = preview(alice, happUA, []*adminv1.ResponseHeader{ownHeader("Content-Type", "x", nil)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a reserved name in the draft: %v", err)
	}
	m.st.W.Exec(`UPDATE user SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).Unix(), alice)
	if r, err = preview(alice, happUA, list); err != nil || r.NotServed != "no_access" {
		t.Fatalf("expired: %+v, %v", r, err)
	}
}

// The groups of a save: one that never existed is refused; one that was deleted since the headers were saved drops out, so that a
// save from another tab of the admin does not fail over it.
func TestResponseHeaderGroupsOfASave(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	ctx := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_headers"})
	temp := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "temp", ProfileIds: []string{m.profile}}))).Msg.Group.Id
	save := func(groups ...string) (*adminv1.SubscriptionSettings, error) {
		set := subsettings.Defaults()
		set.ResponseHeaders = []*adminv1.ResponseHeader{ownHeader("flclashx-view", "dark", nil, groups...)}
		r, err := svc.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Settings, nil
	}
	if _, err := save("grp_nosuchgroup"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a group that never existed: %v", err)
	}
	if got, err := save(temp, m.group2); err != nil || len(got.ResponseHeaders[0].GroupIds) != 2 {
		t.Fatalf("two groups: %v, %v", got, err)
	}
	must(m.svc.DeleteGroup(m.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: temp})))
	got, err := save(temp, m.group2) // the editor of another tab still sends the old list
	if err != nil || len(got.ResponseHeaders[0].GroupIds) != 1 || got.ResponseHeaders[0].GroupIds[0] != m.group2 {
		t.Fatalf("a deleted group: %v, %v", got, err)
	}
	if _, err := save("grp_nosuchgroup"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("still refused: %v", err)
	}
}

// The reserved names come with the settings, for the editor.
func TestSettingsCarryTheReservedHeaderNames(t *testing.T) {
	m := newM3Rig(t)
	resp := must(headerService(m).GetSubscriptionSettings(m.ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg
	for _, want := range []string{"Profile-*", "Content-*", "X-Hwid-*", "Set-Cookie", "Mistgate-*", "Announce"} {
		if !slices.Contains(resp.ReservedHeaderNames, want) {
			t.Errorf("reserved_header_names lacks %s: %v", want, resp.ReservedHeaderNames)
		}
	}
}
