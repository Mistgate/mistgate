//go:build !js

package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/decoy"
)

// A small site as an agent would write it.
func siteFiles() []map[string]any {
	return []map[string]any{
		{"path": "style.css", "content": "body{font:16px sans-serif}"},
		{"path": "index.html", "content": "<!doctype html><title>Marker-9c1e</title><link rel=stylesheet href=/style.css>"},
		{"path": "about/index.html", "content": "<p>About SECRETWORDS-7f3a</p>"},
		{"path": "404.html", "content": "<p>Not found</p>"},
	}
}

func filesArgs(files any) map[string]any {
	return map[string]any{"node": nodeA, "mode": decoy.ModeOwn, "files": files}
}

func TestDecoyFromFilesIsDeterministic(t *testing.T) {
	mk := func(order []int) *decoy.Bundle {
		in := []decoyFileArg{
			{Path: "index.html", Content: "<p>home</p>"}, {Path: "a/b.css", Content: "x{}"},
			{Path: "logo.png", ContentBase64: base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0, 1, 2})},
		}
		var out []decoyFileArg
		for _, i := range order {
			out = append(out, in[i])
		}
		b, err := decoyFromFiles(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b := mk([]int{0, 1, 2}), mk([]int{2, 1, 0})
	if a.Hash() != b.Hash() || !bytes.Equal(a.Archive(), b.Archive()) || !bytes.Equal(a.Archive(), mk([]int{0, 1, 2}).Archive()) {
		t.Fatalf("the same files gave different bundles: %s vs %s", a.Hash(), b.Hash())
	}
	// the hash is the one the node and the store use for the archive that is uploaded
	if parsed, err := decoy.ParseArchive(a.Archive()); err != nil || parsed.Hash() != a.Hash() {
		t.Fatalf("archive parses to %v, %v", parsed.Hash(), err)
	}
}

func TestNodeDecoyPlanRefusesBadFiles(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	index := map[string]any{"path": "index.html", "content": "<p>x</p>"}
	with := func(f ...map[string]any) []map[string]any { return append([]map[string]any{index}, f...) }
	file := func(path string) map[string]any { return map[string]any{"path": path, "content": "x"} }
	many := make([]map[string]any, 0, 65)
	for i := 0; i < 65; i++ {
		many = append(many, file(fmt.Sprintf("p%d.html", i)))
	}
	many[0] = index
	big := strings.Repeat("a", decoy.MaxBundleBytes/2+1)
	archive := base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{"index.html": "x"}))
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"dot dot":            {filesArgs(with(file("../x"))), "must not contain . or .."},
		"inner dot dot":      {filesArgs(with(file("a/../x"))), "must not contain . or .."},
		"absolute":           {filesArgs(with(file("/abs"))), "leading slash"},
		"backslash":          {filesArgs(with(file(`a\b`))), "backslash"},
		"double slash":       {filesArgs(with(file("a//b"))), "empty part"},
		"trailing slash":     {filesArgs(with(file("a/"))), "empty part"},
		"empty path":         {filesArgs(with(file(""))), "path is empty"},
		"newline":            {filesArgs(with(file("a\nb"))), "control"},
		"nul":                {filesArgs(with(file("a\x00b"))), "control"},
		"bidi override":      {filesArgs(with(file("a‮b"))), "control or invisible"},
		"dot file":           {filesArgs(with(file(".env"))), "bad_path"},
		"colon":              {filesArgs(with(file("a:b"))), "bad_path"},
		"same path":          {filesArgs(with(file("index.html"))), "repeats"},
		"other case":         {filesArgs([]map[string]any{index, file("Index.html")}), "ignoring case"},
		"folder other case":  {filesArgs(with(file("a"), file("A/b.css"))), "needs it to be a folder"},
		"long path":          {filesArgs(with(file(strings.Repeat("a", 198) + ".js"))), "longer than 200"},
		"path like a token":  {filesArgs(with(file("k/aZ3Kq9XvT2mP7wLc4RnB8yHd5GsJ6fE1uO0.html"))), "looks like a key"},
		"65 files":           {filesArgs(many), "at most 64"},
		"over 2 MiB":         {filesArgs(with(map[string]any{"path": "a.txt", "content": big}, map[string]any{"path": "b.txt", "content": big})), "in total"},
		"no index.html":      {filesArgs([]map[string]any{file("about.html")}), "decoy_bundle_no_index"},
		"index in a folder":  {filesArgs([]map[string]any{file("site/index.html")}), "decoy_bundle_no_index"},
		"no content":         {filesArgs(with(map[string]any{"path": "a.css"})), "exactly one"},
		"both contents":      {filesArgs(with(map[string]any{"path": "a.css", "content": "x", "content_base64": "eA=="})), "exactly one"},
		"bad base64":         {filesArgs(with(map[string]any{"path": "a.png", "content_base64": "!!!"})), "not valid base64"},
		"both fields":        {map[string]any{"node": nodeA, "mode": "own", "files": with(), "bundle_base64": archive}, "not both"},
		"neither":            {map[string]any{"node": nodeA, "mode": "own"}, "required"},
		"empty files":        {map[string]any{"node": nodeA, "mode": "own", "files": []map[string]any{}}, "required"},
		"files with builtin": {map[string]any{"node": nodeA, "mode": "builtin", "files": with()}, "only allowed with own"},
		"files with panel":   {map[string]any{"node": nodeA, "mode": "panel", "files": with()}, "only allowed with own"},
	}
	for name, tc := range cases {
		out, isErr := callTool(t, s, "node_decoy_plan", tc.args)
		if !isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: isError=%v out=%q, want it to contain %q", name, isErr, out, tc.want)
		}
	}
	if len(e.w.nodeDecoyReqs) != 0 || len(e.plans.byID) != 0 {
		t.Fatal("a refused plan was stored or reached the admin API")
	}
}

func TestNodeDecoyPlanWithFilesShowsAListAndAHashNotContents(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "node_decoy", filesArgs(siteFiles()))
	if !p.NeedsApproval || len(e.w.nodeDecoyReqs) != 0 {
		t.Fatalf("plan = %+v, requests=%d", p, len(e.w.nodeDecoyReqs))
	}
	shown, _ := json.Marshal(p)
	for _, content := range []string{"Marker-9c1e", "SECRETWORDS-7f3a", "sans-serif", "Not found"} {
		if strings.Contains(string(shown), content) {
			t.Fatalf("the plan shows file contents (%q): %s", content, shown)
		}
	}
	want, err := decoyFromFiles([]decoyFileArg{{Path: "style.css", Content: "body{font:16px sans-serif}"},
		{Path: "index.html", Content: "<!doctype html><title>Marker-9c1e</title><link rel=stylesheet href=/style.css>"},
		{Path: "about/index.html", Content: "<p>About SECRETWORDS-7f3a</p>"}, {Path: "404.html", Content: "<p>Not found</p>"}})
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]string{}
	var list strings.Builder
	for _, f := range p.Facts {
		// what the assistant's card takes: the panel's own scrubber must leave each value whole
		if scrub(f.Value) != f.Value || utf8.RuneCountInString(f.Value) > 1000 {
			t.Errorf("fact %s cannot be shown whole in a card: %q", f.Key, f.Value)
		}
		if f.Key == "file_list" {
			list.WriteString(f.Value)
			if !f.Untrusted {
				t.Error("the file list holds names an agent chose and must be marked untrusted")
			}
		} else {
			facts[f.Key] = f.Value
		}
	}
	if strings.ReplaceAll(facts["bundle"], " ", "") != want.Hash() || facts["files"] != "4" || facts["bytes"] != fmt.Sprint(26+78+29+16) {
		t.Fatalf("facts = %v, want hash %s", facts, want.Hash())
	}
	for _, line := range []string{"404.html: 16 bytes", "about/index.html: 29 bytes", "index.html: 78 bytes", "style.css: 26 bytes"} {
		if !strings.Contains(list.String(), line) {
			t.Errorf("the file list lacks %q: %q", line, list.String())
		}
	}
	if !strings.Contains(p.Summary, "4 files") {
		t.Errorf("summary = %q", p.Summary)
	}
	// nothing of the contents in the audit log either; what is stored for the apply is the archive, not the list
	for _, a := range append(e.audits("mcp_plan"), e.audits("mcp_apply")...) {
		b, _ := json.Marshal(a)
		if strings.Contains(string(b), "Marker-9c1e") || strings.Contains(string(b), "SECRETWORDS") {
			t.Fatalf("audit row holds contents: %s", b)
		}
	}
	var stored nodeDecoyArgs
	for _, pl := range e.plans.byID {
		if err := json.Unmarshal([]byte(pl.ParamsJSON), &stored); err != nil {
			t.Fatal(err)
		}
	}
	if len(stored.Files) != 0 || stored.BundleBase64 == "" || stored.Node != nodeA {
		t.Fatalf("stored arguments = %+v", stored)
	}
}

func TestNodeDecoyFilesPlanThenApplySendsExactlyThoseFiles(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	icon := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0xff, 0xfe}
	files := append(siteFiles(), map[string]any{"path": "img/icon.png", "content_base64": base64.StdEncoding.EncodeToString(icon)})
	p := planOf(t, s, "node_decoy", filesArgs(files))
	if len(e.w.nodeDecoyReqs) != 0 {
		t.Fatal("applied before the owner approved")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if out := applyOf(t, s, "node_decoy", p.ConfirmToken); out.Status != "applied" {
		t.Fatalf("apply = %+v", out)
	}
	if len(e.w.nodeDecoyReqs) != 1 {
		t.Fatalf("SetNodeDecoyMode called %d times", len(e.w.nodeDecoyReqs))
	}
	req := e.w.nodeDecoyReqs[0]
	if req.GetNodeId() != nodeA || req.GetMode() != decoy.ModeOwn {
		t.Fatalf("request = %+v", req)
	}
	got, err := decoy.ParseArchive(req.GetBundleArchive())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"style.css": "body{font:16px sans-serif}", "404.html": "<p>Not found</p>", "img/icon.png": string(icon),
		"index.html":       "<!doctype html><title>Marker-9c1e</title><link rel=stylesheet href=/style.css>",
		"about/index.html": "<p>About SECRETWORDS-7f3a</p>"}
	if len(got.Files()) != len(want) {
		t.Fatalf("the node gets %d files, want %d", len(got.Files()), len(want))
	}
	for _, f := range got.Files() {
		if w, ok := want[f.Path]; !ok || w != string(f.Content) {
			t.Errorf("file %q = %q", f.Path, f.Content)
		}
	}
	// the same list again is the same bundle: the panel stores it by this hash and a node that has it is not sent it again
	p2 := planOf(t, s, "node_decoy", filesArgs(files))
	if planFact(p2, "bundle") != planFact(p, "bundle") {
		t.Fatalf("hashes differ: %q vs %q", planFact(p2, "bundle"), planFact(p, "bundle"))
	}
}

func planFact(p PlanOut, key string) string {
	for _, f := range p.Facts {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// The edition that does not store own bundles refuses the list exactly as it refuses an archive, before looking at either.
func TestNodeDecoyFilesRefusedWhereOwnSitesAreNot(t *testing.T) {
	e := newTestEnv(t)
	e.w.noOwnDecoy = true
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	archive := base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{"index.html": "x"}))
	for name, args := range map[string]map[string]any{
		"files":  filesArgs(siteFiles()),
		"bundle": {"node": nodeA, "mode": "own", "bundle_base64": archive},
		"bad":    filesArgs([]map[string]any{{"path": "../x", "content": "x"}}),
	} {
		out, isErr := callTool(t, s, "node_decoy_plan", args)
		if !isErr || !strings.Contains(out, "own decoy sites are unavailable in this edition") {
			t.Errorf("%s: isError=%v out=%q", name, isErr, out)
		}
	}
}

func TestNodeDecoyToolDescribesFiles(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	for tool, err := range s.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name != "node_decoy_plan" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		for _, want := range []string{"files", "content_base64", "bundle_base64", "no external scripts", "never repeat the subject or look"} {
			if !strings.Contains(tool.Description+string(schema), want) {
				t.Errorf("node_decoy_plan does not mention %q", want)
			}
		}
		return
	}
	t.Fatal("node_decoy_plan is missing")
}

// A list of files from anywhere must end in an error or a bundle: no panic, and memory stays near the 2 MiB cap.
func FuzzDecoyFromFiles(f *testing.F) {
	f.Add("index.html", "<p>x</p>", "", "a/b.css", "x{}", "")
	f.Add("index.html", "", "eA==", "../x", "x", "")
	f.Add("A", "x", "", "a/b", "y", "")
	f.Add("index.html", strings.Repeat("a", 1<<10), "", "Index.html", "", "!!")
	f.Fuzz(func(t *testing.T, p1, c1, b1, p2, c2, b2 string) {
		in := []decoyFileArg{{Path: p1, Content: c1, ContentBase64: b1}, {Path: p2, Content: c2, ContentBase64: b2}, {Path: "index.html", Content: "x"}}
		b, err := decoyFromFiles(in)
		if err != nil {
			if b != nil {
				t.Fatal("a refused list returned a bundle")
			}
			return
		}
		if parsed, perr := decoy.ParseArchive(b.Archive()); perr != nil || parsed.Hash() != b.Hash() {
			t.Fatalf("a built bundle does not parse back to itself: %v", perr)
		}
		for _, f := range b.Files() {
			if checkDecoyPath(f.Path) != nil {
				t.Fatalf("bundle holds a path the checks refuse: %q", f.Path)
			}
		}
	})
}

// The biggest list that fits: about 2 MiB of bytes that do not compress, as base64 in the request. The plan keeps the archive,
// not the list, so what it stores stays under the limit of an upload plan without raising it for the other tools.
func TestNodeDecoyPlanKeepsAFullSizeSiteWithinTheStoredLimit(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	noise := make([]byte, decoy.MaxBundleBytes-8<<10)
	for i, x := 0, uint32(1); i < len(noise); i++ {
		x = x*1664525 + 1013904223
		noise[i] = byte(x >> 24)
	}
	p := planOf(t, s, "node_decoy", filesArgs([]map[string]any{
		{"path": "index.html", "content": "<p>x</p>"},
		{"path": "media/photo.webp", "content_base64": base64.StdEncoding.EncodeToString(noise)},
	}))
	for _, pl := range e.plans.byID {
		if len(pl.ParamsJSON) > maxUploadArgsJSON || len(pl.ParamsJSON) < len(noise) {
			t.Fatalf("stored arguments are %d bytes (limit %d)", len(pl.ParamsJSON), maxUploadArgsJSON)
		}
	}
	if planFact(p, "files") != "2" {
		t.Fatalf("facts = %+v", p.Facts)
	}
}

// The longest list the card has to carry: 64 files with the longest paths still fits the result of a plan and every fact the card takes.
func TestNodeDecoyPlanListsSixtyFourLongPaths(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	files := []map[string]any{{"path": "index.html", "content": "<p>x</p>"}}
	for i := 1; i < 64; i++ {
		files = append(files, map[string]any{"path": strings.Repeat("d/", 94) + fmt.Sprintf("page%03d.html", i), "content": "x"})
	}
	p := planOf(t, s, "node_decoy", filesArgs(files))
	var chunks int
	for _, f := range p.Facts {
		if scrub(f.Value) != f.Value || utf8.RuneCountInString(f.Value) > 1000 {
			t.Fatalf("fact %s cannot be shown whole in a card (%d runes)", f.Key, utf8.RuneCountInString(f.Value))
		}
		if f.Key == "file_list" {
			chunks++
		}
	}
	if chunks < 10 || planFact(p, "files") != "64" {
		t.Fatalf("chunks=%d files=%s", chunks, planFact(p, "files"))
	}
}
