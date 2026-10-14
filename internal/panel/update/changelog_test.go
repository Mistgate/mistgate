package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
)

// Bodies shaped like the real releases (gh release view v0.1.38): English, a line "---", Russian, the Full Changelog line.
const (
	bodyEnRu = "## Fixes\n\n- The user page named eight built-in DNS presets in Russian whatever the visitor's language.\n" +
		"- A doctor fix that the node answers with \"nothing to do\" now re-runs the node's checks.\n\n" +
		"**Full Changelog**: https://github.com/Mistgate/mistgate/compare/v0.1.36...v0.1.37\n\n---\n\n" +
		"## Исправления\n\n- Страница пользователя называла восемь встроенных DNS-пресетов по-русски на любом языке посетителя.\n" +
		"- Исправление доктора, на которое нода ответила «нечего делать», теперь перезапускает проверки ноды.\n\n" +
		"**Full Changelog**: https://github.com/Mistgate/mistgate/compare/v0.1.36...v0.1.37"
	// the English part of the real v0.1.38 mentions 简体中文 and a Chinese link: it is still English
	bodyEnRuZh = "## Simplified Chinese\n\n- Mistgate now speaks Simplified Chinese (简体中文) as well as English and Russian: the admin, the user page and the docs at [mistgate.app/zh/](https://mistgate.app/zh/).\n\n" +
		"---\n\n## Упрощённый китайский\n\n- Mistgate теперь говорит и на упрощённом китайском (简体中文): админка, страница пользователя и документация на [mistgate.app/zh/](https://mistgate.app/zh/).\n\n" +
		"---\n\n## 简体中文\n\n- Mistgate 现在除英语和俄语外，也支持简体中文：管理后台、用户页面、节点安装页面和 Telegram 消息。\n- 语言来自面板的 **默认语言**，也可以从菜单切换。\n\n" +
		"**Full Changelog**: https://github.com/Mistgate/mistgate/compare/v0.1.37...v0.1.38"
	bodyPlain = "- One small fix.\n\n**Full Changelog**: https://github.com/Mistgate/mistgate/compare/v0.1.0...v0.1.1"
)

func TestParseReleaseBody(t *testing.T) {
	t.Run("en+ru", func(t *testing.T) {
		n := parseReleaseBody(bodyEnRu)
		if !strings.HasPrefix(n.En, "## Fixes") || !strings.HasPrefix(n.Ru, "## Исправления") || n.Zh != "" || n.Security {
			t.Fatalf("%+v", n)
		}
		if strings.Contains(n.En+n.Ru, "Full Changelog") || strings.Contains(n.En, "---") || strings.Contains(n.Ru, "---") {
			t.Fatalf("separator or Full Changelog kept:\n%s\n%s", n.En, n.Ru)
		}
	})
	t.Run("en+ru+zh by script, not by place", func(t *testing.T) {
		n := parseReleaseBody(bodyEnRuZh)
		if !strings.HasPrefix(n.En, "## Simplified Chinese") || !strings.HasPrefix(n.Ru, "## Упрощённый китайский") || !strings.HasPrefix(n.Zh, "## 简体中文") {
			t.Fatalf("%+v", n)
		}
		// the same parts in another order land in the same languages
		parts := strings.Split(strings.ReplaceAll(bodyEnRuZh, "\n\n**Full Changelog**: https://github.com/Mistgate/mistgate/compare/v0.1.37...v0.1.38", ""), "\n\n---\n\n")
		shuffled := parseReleaseBody(parts[2] + "\n---\n" + parts[0] + "\n---\n" + parts[1])
		if shuffled.En != n.En || shuffled.Ru != n.Ru || shuffled.Zh != strings.TrimSpace(parts[2]) {
			t.Fatalf("order changed the languages: %+v", shuffled)
		}
	})
	t.Run("no separator is English", func(t *testing.T) {
		n := parseReleaseBody(bodyPlain)
		if n.En != "- One small fix." || n.Ru != "" || n.Zh != "" {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("only Russian stays Russian, empty stays empty", func(t *testing.T) {
		if n := parseReleaseBody("- Починили.\n"); n.Ru == "" || n.En != "" {
			t.Fatalf("%+v", n)
		}
		if n := parseReleaseBody("\n  \n"); n.En != "" || n.Ru != "" || n.Zh != "" {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("CRLF and two parts of one language", func(t *testing.T) {
		n := parseReleaseBody("- A.\r\n\r\n---\r\n\r\n- B.\r\n")
		if n.En != "- A.\n\n- B." {
			t.Fatalf("%q", n.En)
		}
	})
	t.Run("a longer horizontal rule is not a separator", func(t *testing.T) {
		if n := parseReleaseBody("- A.\n----\n- B."); n.En != "- A.\n----\n- B." {
			t.Fatalf("%q", n.En)
		}
	})
}

func TestParseReleaseBodySecurity(t *testing.T) {
	for name, body := range map[string]string{
		"en heading":    "## Security\n\n- Fixed a thing.",
		"en fixes":      "### Security fixes\n\n- Fixed a thing.",
		"ru heading":    "## Fixes\n\n- x\n\n---\n\n## Безопасность\n\n- Исправили.",
		"zh heading":    "## Fixes\n\n- x\n\n---\n\n- у\n\n---\n\n## 安全修复\n\n- 已修复。",
		"bold heading":  "## **Security**\n\n- Fixed.",
		"lower, deeper": "#### security\n\n- Fixed.",
	} {
		if !parseReleaseBody(body).Security {
			t.Errorf("%s: not marked", name)
		}
	}
	for name, body := range map[string]string{
		"a bullet":       "## Fixes\n\n- Security of the login page was improved.",
		"prose":          "Nothing about security here.\n\n## Fixes\n\n- x",
		"another word":   "## Insecure defaults",
		"empty":          "",
		"not a heading":  "Security\n\n- x",
		"in a code span": "## Fixes\n\n`## Security`",
		"in a fence":     "## Fixes\n\n```md\n## Security\n```\n",
		"in a ~~~ fence": "~~~\n### Безопасность\n~~~",
	} {
		if parseReleaseBody(body).Security {
			t.Errorf("%s: marked", name)
		}
	}
}

func TestVersionRange(t *testing.T) {
	all := []ReleaseNote{{Version: "v0.1.40"}, {Version: "v0.1.39"}, {Version: "v0.1.38"}, {Version: "v0.1.37"}, {Version: "v0.1.36"}}
	got := func(after, upTo string) string {
		var v []string
		for _, n := range notesBetween(all, after, upTo) {
			v = append(v, n.Version)
		}
		return strings.Join(v, " ")
	}
	for _, tc := range []struct{ after, upTo, want string }{
		{"v0.1.37", "v0.1.39", "v0.1.39 v0.1.38"},            // newer than the oldest, up to the target, newest first
		{"v0.1.37", "v0.1.37", ""},                           // nothing is behind
		{"v0.1.39", "v0.1.37", ""},                           // a node ahead of the bundle
		{"0.1.37", "v0.1.38", "v0.1.38"},                     // without the v
		{"v0.1.37-5-gabc1234", "v0.1.39", "v0.1.39 v0.1.38"}, // a build from git describe counts as the release it follows
		{"v0.1.37-5-gabc1234-dirty", "v0.1.38", "v0.1.38"},
		{"v0.1.39-rc.1", "v0.1.39", "v0.1.39"}, // a prerelease comes before its release
		{"dev", "v0.1.39", ""},                 // no version, no claim
		{"v0.1.37", "", ""},
		{"v0.1.20", "v0.1.40", "v0.1.40 v0.1.39 v0.1.38 v0.1.37 v0.1.36"},
	} {
		if g := got(tc.after, tc.upTo); g != tc.want {
			t.Errorf("notesBetween(%q, %q) = %q, want %q", tc.after, tc.upTo, g, tc.want)
		}
	}
	if o := oldestVersion([]string{"v0.1.38", "", "dev", "v0.1.36", "v0.1.37"}); o != "v0.1.36" {
		t.Fatalf("oldest = %q", o)
	}
	if o := oldestVersion([]string{"", "dev"}); o != "" {
		t.Fatalf("oldest of nothing = %q", o)
	}
}

type notesFake struct {
	PanelUpdater
	notes []ReleaseNote
}

func (f notesFake) ReleaseNotes() []ReleaseNote { return f.notes }

// The node card shows what a node update brings (from the OLDEST agent to the trusted bundle), the panel card what a panel
// update brings (from this panel to the release it can install).
func TestServiceReleaseNotesRanges(t *testing.T) {
	all := []ReleaseNote{{Version: "v0.1.40", En: "forty"}, {Version: "v0.1.39", En: "thirty-nine"}, {Version: "v0.1.38", En: "thirty-eight"}, {Version: "v0.1.37", En: "thirty-seven"}}
	s := &Service{cfg: Config{PanelUpdater: notesFake{notes: all}, PanelVersion: "v0.1.37"}}
	nodes := []nodeView{
		{row: store.NodeRow{AgentVersion: "v0.1.39"}},
		{row: store.NodeRow{AgentVersion: "v0.1.37"}}, // the oldest
		{row: store.NodeRow{AgentVersion: ""}},        // never said
	}
	bundle := &bundleState{trusted: true, manifest: &release.Manifest{Version: "v0.1.39"}}
	agent, pnl := s.releaseNotes(bundle, nodes, &adminv1.PanelUpdate{Available: true, Version: "v0.1.40"})
	if len(agent) != 2 || agent[0].Version != "v0.1.39" || agent[1].Version != "v0.1.38" || agent[0].En != "thirty-nine" {
		t.Fatalf("agent notes = %v", agent)
	}
	if len(pnl) != 3 || pnl[0].Version != "v0.1.40" || pnl[2].Version != "v0.1.38" {
		t.Fatalf("panel notes = %v", pnl)
	}

	// no trusted bundle, no node claim; a release that cannot be installed (or is not there) gives the panel no notes
	agent, pnl = s.releaseNotes(&bundleState{manifest: &release.Manifest{Version: "v0.1.39"}}, nodes, &adminv1.PanelUpdate{Available: false, Version: "v0.1.40"})
	if len(agent) != 0 || len(pnl) != 0 {
		t.Fatalf("unverified: %v %v", agent, pnl)
	}
	if agent, pnl = s.releaseNotes(nil, nil, nil); len(agent) != 0 || len(pnl) != 0 {
		t.Fatalf("nothing known: %v %v", agent, pnl)
	}
	// an updater that reads no notes (the edition, a fake) is not an error
	s.cfg.PanelUpdater = nil
	if agent, pnl = s.releaseNotes(bundle, nodes, &adminv1.PanelUpdate{Available: true, Version: "v0.1.40"}); len(agent) != 0 || len(pnl) != 0 {
		t.Fatalf("no source: %v %v", agent, pnl)
	}
}

// notesAPI serves a release list with an ETag and counts what it was asked.
type notesAPI struct {
	mu       sync.Mutex
	body     []byte
	etag     string
	status   int // 0 = 200
	requests int
	gotMatch []string
}

func (a *notesAPI) set(list []githubNotesRelease) {
	b, _ := json.Marshal(list)
	a.mu.Lock()
	a.body = b
	a.mu.Unlock()
}

func (a *notesAPI) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.requests++
		a.gotMatch = append(a.gotMatch, r.Header.Get("If-None-Match"))
		if a.status != 0 {
			return githubResponse(a.status, nil), nil
		}
		if a.etag != "" && r.Header.Get("If-None-Match") == a.etag {
			return githubResponse(http.StatusNotModified, nil), nil
		}
		resp := githubResponse(http.StatusOK, a.body)
		if a.etag != "" {
			resp.Header.Set("ETag", a.etag)
		}
		return resp, nil
	})}
}

func rel(tag, body string) githubNotesRelease {
	return githubNotesRelease{TagName: tag, HTMLURL: githubReleasePageBase + tag, PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Body: body}
}

func TestFetchReleaseNotesFiltersAndOrders(t *testing.T) {
	api := &notesAPI{etag: `W/"abc"`}
	draft, pre := rel("v0.1.99", "draft"), rel("v0.1.98", "pre")
	draft.Draft, pre.Prerelease = true, true
	badTag := rel("nightly", "x")
	foreignPage := rel("v0.1.97", "x")
	foreignPage.HTMLURL = "https://evil.example/releases/tag/v0.1.97"
	rcTag := rel("v0.1.96-rc1", "x")
	api.set([]githubNotesRelease{draft, pre, badTag, foreignPage, rcTag, rel("v0.1.37", bodyEnRu), rel("v0.1.38", bodyEnRuZh)})

	notes, etag, same, err := fetchReleaseNotes(context.Background(), api.client(), GitHubReleaseNotesURL, "")
	if err != nil || same || etag != `W/"abc"` {
		t.Fatalf("notes=%v etag=%q same=%v err=%v", notes, etag, same, err)
	}
	if len(notes) != 2 || notes[0].Version != "v0.1.38" || notes[1].Version != "v0.1.37" {
		t.Fatalf("drafts, prereleases and foreign pages must be skipped, newest first: %+v", notes)
	}
	if notes[0].Zh == "" || notes[0].Ru == "" || notes[1].Zh != "" || notes[0].PublishedUnix == 0 || notes[0].URL != githubReleasePageBase+"v0.1.38" {
		t.Fatalf("%+v", notes[0])
	}
}

func TestFetchReleaseNotesCaps(t *testing.T) {
	api := &notesAPI{}
	var list []githubNotesRelease
	for i := 44; i >= 0; i-- { // GitHub lists newest first; only the first page's worth is read
		list = append(list, rel(fmt.Sprintf("v0.2.%d", i), "- x"))
	}
	api.set(list)
	notes, _, _, err := fetchReleaseNotes(context.Background(), api.client(), GitHubReleaseNotesURL, "")
	if err != nil || len(notes) != notesMaxReleases || notes[0].Version != "v0.2.44" {
		t.Fatalf("count cap: %d notes, err %v", len(notes), err)
	}

	// one body of 150 KiB of two-byte letters: cut to the cap on a character boundary
	api.set([]githubNotesRelease{rel("v0.3.0", strings.Repeat("ж", 75<<10))})
	notes, _, _, err = fetchReleaseNotes(context.Background(), api.client(), GitHubReleaseNotesURL, "")
	if err != nil || len(notes) != 1 || len(notes[0].Ru) > notesMaxBody || len(notes[0].Ru) < notesMaxBody-3 || !utf8.ValidString(notes[0].Ru) {
		t.Fatalf("body cap: %d bytes, err %v", len(notes[0].Ru), err)
	}

	// a listing beyond the response cap is refused as a whole
	api.set([]githubNotesRelease{rel("v0.3.1", strings.Repeat("a", notesMaxResponse+1))})
	if _, _, _, err = fetchReleaseNotes(context.Background(), api.client(), GitHubReleaseNotesURL, ""); err == nil {
		t.Fatal("an oversized listing was accepted")
	}

	// errors are errors
	api.status = http.StatusForbidden
	if _, _, _, err = fetchReleaseNotes(context.Background(), api.client(), GitHubReleaseNotesURL, ""); err == nil {
		t.Fatal("HTTP 403 was accepted")
	}
}

// A listing of many small entries is not allocated whole: decoding stops at the cap.
func TestDecodeReleaseListStopsAtTheCap(t *testing.T) {
	body := "[" + strings.Repeat("{},", 100_000) + "{}]"
	list, err := decodeReleaseList(strings.NewReader(body))
	if err != nil || len(list) != notesMaxReleases {
		t.Fatalf("decoded %d entries, err %v", len(list), err)
	}
	if _, err := decodeReleaseList(strings.NewReader(`{"message":"Not Found"}`)); err == nil {
		t.Fatal("an object was read as a list")
	}
}

// The second look is a conditional request; GitHub's 304 changes nothing and costs no quota, a failed look keeps the notes.
func TestPanelUpdaterReadsNotesConditionally(t *testing.T) {
	f := newPanelFixture()
	updater, _, _ := newPanelUpdater(t, f, &recorder{}, nil)
	api := &notesAPI{etag: `W/"v1"`}
	api.set([]githubNotesRelease{rel("v0.1.38", bodyEnRuZh), rel("v0.1.37", bodyEnRu)})
	updater.client = api.client()
	updater.cfg.NotesURL = GitHubReleaseNotesURL
	updater.log = slog.New(slog.NewTextHandler(io.Discard, nil))

	updater.refreshNotes(context.Background())
	if got := updater.ReleaseNotes(); len(got) != 2 || api.gotMatch[0] != "" {
		t.Fatalf("first read: %d notes, If-None-Match %q", len(got), api.gotMatch)
	}
	first := updater.ReleaseNotes()

	updater.refreshNotes(context.Background())
	if len(api.gotMatch) != 2 || api.gotMatch[1] != `W/"v1"` {
		t.Fatalf("the second request must carry the ETag: %q", api.gotMatch)
	}
	if got := updater.ReleaseNotes(); len(got) != 2 || &got[0] != &first[0] {
		t.Fatal("a 304 must leave the notes as they were")
	}

	// the release list changed: new ETag, new notes
	api.mu.Lock()
	api.etag = `W/"v2"`
	api.mu.Unlock()
	api.set([]githubNotesRelease{rel("v0.1.39", "- new"), rel("v0.1.38", bodyEnRuZh)})
	updater.refreshNotes(context.Background())
	if got := updater.ReleaseNotes(); len(got) != 2 || got[0].Version != "v0.1.39" {
		t.Fatalf("changed list: %+v", got)
	}

	// GitHub is unreachable: the notes stay
	api.mu.Lock()
	api.status = http.StatusBadGateway
	api.mu.Unlock()
	updater.refreshNotes(context.Background())
	if got := updater.ReleaseNotes(); len(got) != 2 || got[0].Version != "v0.1.39" {
		t.Fatalf("a failed read dropped the notes: %+v", got)
	}
}

// Check reads the notes after its own verdict, and never when no URL is configured (the tests, the helper process, the edge).
func TestPanelUpdaterCheckReadsNotesOnlyWhenConfigured(t *testing.T) {
	f := newPanelFixture()
	updater, counts, _ := newPanelUpdater(t, f, &recorder{}, nil)
	updater.Check(context.Background())
	if counts["api"] != 1 || len(updater.ReleaseNotes()) != 0 {
		t.Fatalf("no URL: api requests %v", counts)
	}

	updater, _, _ = newPanelUpdater(t, f, &recorder{}, nil)
	api := &notesAPI{}
	api.set([]githubNotesRelease{rel("v1.2.0", "- x")})
	base := updater.client
	updater.cfg.NotesURL = GitHubReleaseNotesURL
	updater.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/Mistgate/mistgate/releases" {
			return api.client().Do(r)
		}
		return base.Do(r)
	})}
	if s := updater.Check(context.Background()); !s.Available {
		t.Fatalf("the verdict must not depend on the notes: %+v", s)
	}
	if got := updater.ReleaseNotes(); len(got) != 1 || got[0].Version != "v1.2.0" {
		t.Fatalf("notes after Check: %+v", got)
	}
}

// The notes go through the same client as the release check, so its redirect policy (GitHub hosts only, HTTPS) holds.
func TestReleaseNotesKeepTheRedirectAllowlist(t *testing.T) {
	var asked []string
	client := &http.Client{CheckRedirect: githubReleaseRedirect, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.Host)
		if r.URL.Host == "api.github.com" {
			resp := githubResponse(http.StatusFound, nil)
			resp.Header.Set("Location", "https://evil.example/releases")
			return resp, nil
		}
		return githubResponse(http.StatusOK, []byte("[]")), nil
	})}
	if _, _, _, err := fetchReleaseNotes(context.Background(), client, GitHubReleaseNotesURL, ""); err == nil {
		t.Fatal("a redirect to a foreign host was followed")
	}
	if len(asked) != 1 {
		t.Fatalf("hosts asked: %v", asked)
	}
	if GitHubReleaseNotesURL != "https://api.github.com/repos/Mistgate/mistgate/releases?per_page=30" {
		t.Fatalf("the notes come from %s", GitHubReleaseNotesURL)
	}
}
