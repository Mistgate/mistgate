package subs_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The Mihomo answer of a link with the owner's templates (SubscriptionSettings.mihomo_templates): each group gets its own document,
// a group without a template gets the answer it always had, a person who gets a note gets the note.

func testdataText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// templateRig is the Mihomo rig with a third group that no template names, and a handler with the settings cache.
type templateRig struct {
	*m3rig
	h      http.Handler
	cache  *subsettings.Cache
	plain  string // a group without a template (the one profile p)
	logs   *bytes.Buffer
	router string // the text of the router template
}

func newTemplateRig(t *testing.T, mut func(*subs.Config)) *templateRig {
	t.Helper()
	m := newM3Rig(t)
	r := &templateRig{m3rig: m, logs: &bytes.Buffer{}, router: testdataText(t, "mihomo_template_router.yaml")}
	r.plain = must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g3", ProfileIds: []string{m.profile}}))).Msg.Group.Id
	r.h, r.cache = m.handler(func(c *subs.Config) {
		c.MaxPerHour = -1
		c.Log = slog.New(slog.NewTextHandler(r.logs, nil))
		if mut != nil {
			mut(c)
		}
	})
	return r
}

func (r *templateRig) save(t *testing.T, mut func(*adminv1.SubscriptionSettings), list ...*adminv1.MihomoTemplate) {
	t.Helper()
	set := subsettings.Defaults()
	set.MihomoTemplates = list
	if mut != nil {
		mut(set)
	}
	if _, err := r.cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
}

func (r *templateRig) body(t *testing.T, token string, extra ...string) string {
	t.Helper()
	rec := fetch(r.h, "/"+token, mihomoUA, extra...)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Encoding") == "gzip" {
		return string(gunzip(t, rec.Body.Bytes()))
	}
	return rec.Body.String()
}

type tplAnswer struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	DNS      map[string]any `yaml:"dns"`
	Rules    []string       `yaml:"rules"`
	RedirPrt any            `yaml:"redir-port"`
	Mixed    any            `yaml:"mixed-port"`
}

func answerOf(t *testing.T, body string) tplAnswer {
	t.Helper()
	var a tplAnswer
	if err := yaml.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, body)
	}
	return a
}

func routerTemplate(r *templateRig, groups ...string) *adminv1.MihomoTemplate {
	return &adminv1.MihomoTemplate{Name: "router", Yaml: r.router, GroupIds: groups}
}

func TestMihomoTemplateAnswerOfAGroup(t *testing.T) {
	r := newTemplateRig(t, nil)
	_, alice := r.newUser("alice", nil) // group2: both profiles
	_, bob := r.user("bob", nil)        // the first group: one profile
	_, carol := r.plainUser("carol")    // a group no template names
	beforeAlice, beforeBob, beforeCarol := r.body(t, alice), r.body(t, bob), r.body(t, carol)

	r.save(t, nil, routerTemplate(r, r.group2), &adminv1.MihomoTemplate{Name: "pc", Yaml: testdataText(t, "mihomo_template_pc.yaml"), GroupIds: []string{r.group}})

	// The router's person: the template's keys, their two servers first, then Auto, and the dns the template lacks.
	a := answerOf(t, r.body(t, alice))
	if a.RedirPrt != 7892 || len(a.Proxies) != 2 || a.Proxies[0]["name"] != "p" || a.Proxies[1]["name"] != "awg31" {
		t.Fatalf("router: %+v", a)
	}
	if n := len(a.Groups); n != 3 || a.Groups[0].Name != "Proxy" || a.Groups[2].Name != "Auto" || !slices.Equal(a.Groups[2].Proxies, []string{"p", "awg31"}) {
		t.Errorf("router groups: %+v", a.Groups)
	}
	if a.DNS["respect-rules"] != true || a.DNS["nameserver-policy"] != nil || a.Rules[len(a.Rules)-1] != "MATCH,Proxy" {
		t.Errorf("router dns %v, rules %v", a.DNS, a.Rules)
	}
	// The other group has the other template, with its own dns and its static proxy next to the server of the person.
	b := answerOf(t, r.body(t, bob))
	if b.Mixed != 7890 || b.RedirPrt != nil || len(b.Proxies) != 2 || b.Proxies[0]["name"] != "p" || b.Proxies[1]["name"] != "office-socks" || b.DNS["respect-rules"] != nil {
		t.Errorf("pc: %+v", b)
	}
	if r.body(t, alice) == r.body(t, bob) {
		t.Error("two groups got one document")
	}
	// A group without a template: the answer it had, byte for byte (and the other groups' templates did not touch it).
	if got := r.body(t, carol); got != beforeCarol {
		t.Errorf("a group without a template changed:\n%s\n---\n%s", got, beforeCarol)
	}
	if beforeAlice == r.body(t, alice) || beforeBob == r.body(t, bob) {
		t.Error("a templated group still gets the built-in profile")
	}
	// The body is the same compressed, and the headers are those of every Mihomo answer.
	rec := fetch(r.h, "/"+alice, mihomoUA, "Accept-Encoding", "gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" || string(gunzip(t, rec.Body.Bytes())) != r.body(t, alice) ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/yaml") || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Profile-Title") == "" {
		t.Errorf("gzip answer: %v", rec.Header())
	}
	// Other formats are not templated.
	if list := fetch(r.h, "/"+alice, happUA); len(decodeList(t, list.Body.String())) != 1 {
		t.Errorf("link list: %q", list.Body.String())
	}
}

// plainUser is a person of the group no template names.
func (r *templateRig) plainUser(name string) (id, token string) {
	return r.user(name, func(c *adminv1.CreateUserRequest) { c.GroupId = r.plain })
}

// A save shows at the next request, inside the 10 seconds the data of a view is kept.
func TestMihomoTemplateEditShowsAtOnce(t *testing.T) {
	r := newTemplateRig(t, nil) // MinInterval is the default
	_, alice := r.newUser("alice", nil)
	r.save(t, nil, routerTemplate(r, r.group2))
	first := r.body(t, alice)
	if !strings.Contains(first, "redir-port: 7892") {
		t.Fatalf("not the template:\n%s", first)
	}
	edited := strings.Replace(r.router, "redir-port: 7892", "redir-port: 7999", 1)
	r.save(t, nil, &adminv1.MihomoTemplate{Name: "router", Yaml: edited, GroupIds: []string{r.group2}})
	if second := r.body(t, alice); !strings.Contains(second, "redir-port: 7999") || strings.Contains(second, "7892") {
		t.Errorf("the edit is not served:\n%s", second)
	}
	// and the template taken away: the built-in profile again
	r.save(t, nil)
	if third := r.body(t, alice); strings.Contains(third, "redir-port") || !strings.Contains(third, "MATCH,Mistgate") {
		t.Errorf("no template:\n%s", third)
	}
}

// A person who gets a note instead of servers, and one with no servers, get the built-in profile.
func TestMihomoTemplateNotForANoteOrNoServers(t *testing.T) {
	r := newTemplateRig(t, nil)
	idOff, off := r.newUser("alice", nil)
	_, ok := r.newUser("bob", nil)
	r.m3rig.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, idOff)
	if err := r.svc.Recompute(r.ctx, []string{idOff}); err != nil {
		t.Fatal(err)
	}
	empty := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "empty"}))).Msg.Group.Id
	_, none := r.user("dave", func(c *adminv1.CreateUserRequest) { c.GroupId = empty })
	r.save(t, nil, routerTemplate(r, r.group2, empty))

	if !strings.Contains(r.body(t, ok), "redir-port: 7892") {
		t.Fatal("the control person does not get the template")
	}
	body := r.body(t, off)
	a := answerOf(t, body)
	if a.RedirPrt != nil || len(a.Proxies) != 1 || a.Proxies[0]["server"] != "0.0.0.0" || !strings.Contains(strings.ToLower(body), "access paused") {
		t.Errorf("no access: the note must be what is shown:\n%s", body)
	}
	if body := r.body(t, none); strings.Contains(body, "redir-port") || len(answerOf(t, body).Proxies) != 0 {
		t.Errorf("no servers:\n%s", body)
	}
	// AmneziaWG only, and the owner left it out of the link: the note that says so
	only := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "awg only", ProfileIds: []string{r.awg}}))).Msg.Group.Id
	_, eve := r.user("eve", func(c *adminv1.CreateUserRequest) { c.GroupId = only })
	r.save(t, func(s *adminv1.SubscriptionSettings) { s.LinkAwg = new(bool) }, routerTemplate(r, only))
	if body := r.body(t, eve); strings.Contains(body, "redir-port") || len(answerOf(t, body).Proxies) != 1 || answerOf(t, body).Proxies[0]["server"] != "0.0.0.0" {
		t.Errorf("only AWG, off in the link:\n%s", body)
	}
}

// The names of nodes and brands are values: whatever they hold, the document keeps its structure.
func TestMihomoTemplateHostileNodeName(t *testing.T) {
	r := newTemplateRig(t, nil)
	_, alice := r.newUser("alice", nil)
	r.save(t, func(s *adminv1.SubscriptionSettings) { s.ServerNameTemplate = "{node} {profile}" }, routerTemplate(r, r.group2))
	base := answerOf(t, r.body(t, alice))
	for _, name := range []string{
		`a: b # c {d} [e], f & *g ! h | i > 'j' "k" %l`, `- name: evil`, "x\n  - name: evil\n    type: socks5", `"quoted"`, `Auto`, `Proxy`, `DIRECT`, `<<: *check`,
	} {
		r.m3rig.st.W.Exec(`UPDATE node SET name = ? WHERE id = 'nod_1'`, name)
		body := r.body(t, alice)
		a := answerOf(t, body)
		if len(a.Proxies) != 2 || len(a.Groups) != len(base.Groups) || !slices.Equal(a.Rules, base.Rules) || a.RedirPrt != 7892 {
			t.Errorf("node %q changed the structure:\n%s", name, body)
		}
		seen := map[string]bool{}
		for _, p := range a.Proxies {
			n, _ := p["name"].(string)
			if n == "" || seen[n] || n == "DIRECT" || n == "Proxy" || n == "Auto" {
				t.Errorf("node %q: a server is named %q", name, n)
			}
			seen[n] = true
		}
	}
}

func TestMihomoTemplateAutoSwitch(t *testing.T) {
	r := newTemplateRig(t, nil)
	_, alice := r.newUser("alice", nil)
	r.save(t, nil, routerTemplate(r, r.group2))
	if a := answerOf(t, r.body(t, alice)); a.Groups[len(a.Groups)-1].Name != "Auto" {
		t.Errorf("switch on: %+v", a.Groups)
	}
	// With the switch off the template must not name Auto: the save refuses the document (the template above does), so it is
	// rewritten without the reference first.
	off := strings.Replace(r.router, "proxies: [Auto, DIRECT] # the panel's Auto group", "proxies: [DIRECT]", 1)
	set := subsettings.Defaults()
	set.LinkAutoSelect = new(bool)
	set.MihomoTemplates = []*adminv1.MihomoTemplate{routerTemplate(r, r.group2)}
	if _, err := r.cache.Update(r.ctx, set); err == nil {
		t.Fatal("a template that refers to Auto was saved with the switch off")
	}
	set.MihomoTemplates[0].Yaml = off
	if _, err := r.cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	if a := answerOf(t, r.body(t, alice)); len(a.Groups) != 2 || a.Groups[1].Name != "Fast" {
		t.Errorf("switch off: %+v", a.Groups)
	}
}

// A stored template this version cannot read (written by a newer panel, or into the database by hand) is not an error for the
// person: the built-in profile is sent and the log says which template it was.
func TestMihomoTemplateUnreadableInTheDatabase(t *testing.T) {
	r := newTemplateRig(t, nil)
	_, alice := r.newUser("alice", nil)
	set := subsettings.Defaults() // stored as it is, past the checks of a save
	set.MihomoTemplates = []*adminv1.MihomoTemplate{{Name: "Router v9", Yaml: "proxy-groups: [\n  secret-token-text", GroupIds: []string{r.group2}}}
	doc, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetSettings(r.ctx, map[string]string{subsettings.Key: string(doc)}); err != nil {
		t.Fatal(err)
	}
	r.cache.Invalidate()
	rec := fetch(r.h, "/"+alice, mihomoUA)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "MATCH,Mistgate") {
		t.Fatalf("status %d:\n%s", rec.Code, rec.Body.String())
	}
	if l := r.logs.String(); !strings.Contains(l, "code=mihomo_template_unusable") || !strings.Contains(l, `template="Router v9"`) || strings.Contains(l, "secret-token-text") {
		t.Errorf("log: %q", l)
	}
}

// The template is read only as text: the panel opens no connection of its own for the URLs in it (the providers are the apps').
func TestMihomoTemplateNeverFetchesItsURLs(t *testing.T) {
	hit := make(chan string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- r.URL.Path }))
	defer ts.Close()
	r := newTemplateRig(t, nil)
	_, alice := r.newUser("alice", nil)
	text := strings.Replace(r.router, "https://example.com/ads.mrs", ts.URL+"/ads.mrs", 1) + "x-list: " + ts.URL + "/list\n"
	r.save(t, nil, &adminv1.MihomoTemplate{Name: "router", Yaml: text, GroupIds: []string{r.group2}})
	r.body(t, alice)
	select {
	case p := <-hit:
		t.Fatalf("the panel fetched %s", p)
	default:
	}
}
