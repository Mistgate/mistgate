//go:build !js

package decoy

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"hash/maphash"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

var (
	decoyDump  = flag.String("decoy-dump", "", "write sample sites of every kind into this folder, one subfolder per site, for looking at them")
	decoyDumpN = flag.Int("decoy-dump-n", 2, "how many seeds of every kind -decoy-dump writes")
)

const seedsPerKind = 200

func testSeed(i int) []byte { return []byte(fmt.Sprintf("decoy-test-seed-%d", i)) }

func mustGenerate(t *testing.T, kind string, i int) []File {
	t.Helper()
	files, err := Generate(kind, testSeed(i))
	if err != nil {
		t.Fatalf("%s seed %d: %v", kind, i, err)
	}
	return files
}

func kindIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, k := range Kinds() {
		ids = append(ids, k.ID)
	}
	if len(ids) == 0 {
		t.Fatal("no kinds")
	}
	return ids
}

func TestKinds(t *testing.T) {
	want := []string{"files", "login"}
	var got []string
	for _, k := range Kinds() {
		got = append(got, k.ID)
		if k.NameEN == "" || k.NameRU == "" {
			t.Errorf("kind %s has no name in both languages", k.ID)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if _, err := Generate("nope", []byte("x")); err == nil {
		t.Error("an unknown kind was generated")
	}
}

// Kinds that must have scripts and a form: they are the only ones allowed the scripts_or_forms warning.
var interactive = map[string]bool{"login": true, "files": true}

var (
	leftoverMark = regexp.MustCompile(`__[a-z][a-z0-9-]*__|\$\$[a-z]|\{\{|<no value>|%!`)
	voidTags     = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}
)

// checkPage parses one HTML page strictly (every element closed) and returns the local addresses it links to.
func checkPage(t *testing.T, name string, src []byte) (links []string) {
	t.Helper()
	z := html.NewTokenizer(bytes.NewReader(src))
	var stack []string
	ids := map[string]bool{}
	h1, titles := 0, 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			tag := string(tn)
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				switch string(k) {
				case "id":
					if ids[string(v)] {
						t.Errorf("%s: id %q twice", name, v)
					}
					ids[string(v)] = true
				case "href", "src":
					links = append(links, string(v))
				}
			}
			switch tag {
			case "h1":
				h1++
			case "title":
				titles++
			}
			if tt == html.StartTagToken && !voidTags[tag] {
				stack = append(stack, tag)
			}
		case html.EndTagToken:
			tn, _ := z.TagName()
			tag := string(tn)
			if len(stack) == 0 || stack[len(stack)-1] != tag {
				t.Errorf("%s: </%s> closes %v", name, tag, stack)
				return links
			}
			stack = stack[:len(stack)-1]
		}
	}
	if err := z.Err(); err != nil && err.Error() != "EOF" {
		t.Errorf("%s: %v", name, err)
	}
	if len(stack) != 0 {
		t.Errorf("%s: left open: %v", name, stack)
	}
	if h1 != 1 || titles != 1 {
		t.Errorf("%s: %d <h1> and %d <title>, want one of each", name, h1, titles)
	}
	return links
}

func TestGenerateValid(t *testing.T) {
	for _, kind := range kindIDs(t) {
		t.Run(kind, func(t *testing.T) {
			largest := 0
			for i := 0; i < seedsPerKind; i++ {
				files := mustGenerate(t, kind, i)
				b, err := FromFiles(files)
				if err != nil {
					t.Fatalf("seed %d: FromFiles: %v", i, err)
				}
				if b.Size() > largest {
					largest = b.Size()
				}
				if b.Size() > 400<<10 || len(b.Archive()) > maxStoredArchiveBytes {
					t.Fatalf("seed %d: %d bytes, more than a decoy site should be", i, b.Size())
				}
				rep := Analyze(b, true)
				for _, w := range rep.Warnings {
					if w.Code == WarnScripts && interactive[kind] {
						continue // the form and its script are what the kind is for
					}
					t.Errorf("seed %d: warning %s %v", i, w.Code, w.Paths)
				}
				if rep.Title == "" {
					t.Errorf("seed %d: no title", i)
				}
				has := map[string]bool{}
				for _, f := range files {
					has[f.Path] = true
				}
				for _, f := range files {
					if leftoverMark.Match(f.Content) {
						t.Errorf("seed %d: %s keeps a template mark: %q", i, f.Path, leftoverMark.Find(f.Content))
					}
					if path.Ext(f.Path) != ".html" {
						continue
					}
					for _, l := range checkPage(t, fmt.Sprintf("seed %d %s", i, f.Path), f.Content) {
						if strings.HasPrefix(l, "#") {
							continue
						}
						p := strings.TrimPrefix(strings.SplitN(strings.SplitN(l, "#", 2)[0], "?", 2)[0], "/")
						if !strings.HasPrefix(l, "/") || !(has[p] || has[path.Join(p, "index.html")] || p == "") {
							t.Errorf("seed %d: %s links to %q, which is not in the site", i, f.Path, l)
						}
					}
				}
			}
			t.Logf("%s: largest of %d sites %d bytes", kind, seedsPerKind, largest)
		})
	}
}

func hashes(files []File) map[[32]byte]string {
	m := map[[32]byte]string{}
	for _, f := range files {
		m[sha256.Sum256(f.Content)] = f.Path
	}
	return m
}

func titleOf(files []File) string {
	for _, f := range files {
		if f.Path == "index.html" {
			if m := titleTag.FindSubmatch(f.Content); m != nil {
				return string(m[1])
			}
		}
	}
	return ""
}

func TestGenerateDiffers(t *testing.T) {
	for _, kind := range kindIDs(t) {
		t.Run(kind, func(t *testing.T) {
			for i := 0; i < 60; i++ {
				a, b := mustGenerate(t, kind, i), mustGenerate(t, kind, i+1000)
				hb := hashes(b)
				for h, p := range hashes(a) {
					if q, same := hb[h]; same {
						t.Errorf("pair %d: %s of one site is %s of the other, byte for byte", i, p, q)
					}
				}
				if titleOf(a) == titleOf(b) {
					t.Errorf("pair %d: the same title %q", i, titleOf(a))
				}
			}
		})
	}
}

// sharedRun is the longest run of bytes (over limit) two sites have in common, or "".
func sharedRun(a, b []File, limit int) string {
	seed := maphash.MakeSeed()
	win := limit + 1
	seen := map[uint64][]byte{}
	for _, f := range b {
		for i := 0; i+win <= len(f.Content); i++ {
			seen[maphash.Bytes(seed, f.Content[i:i+win])] = f.Content[i : i+win]
		}
	}
	for _, f := range a {
		for i := 0; i+win <= len(f.Content); i++ {
			if w, ok := seen[maphash.Bytes(seed, f.Content[i:i+win])]; ok && bytes.Equal(w, f.Content[i:i+win]) {
				return f.Path + ": " + string(w[:min(len(w), 80)])
			}
		}
	}
	return ""
}

func TestGenerateNoSharedRuns(t *testing.T) {
	for _, kind := range kindIDs(t) {
		t.Run(kind, func(t *testing.T) {
			for i := 0; i < 12; i++ {
				if run := sharedRun(mustGenerate(t, kind, i), mustGenerate(t, kind, i+500), 200); run != "" {
					t.Errorf("pair %d: more than 200 bytes in common: %s", i, run)
				}
			}
		})
	}
}

func TestGenerateDeterministic(t *testing.T) {
	for _, kind := range kindIDs(t) {
		for i := 0; i < 5; i++ {
			a, b := mustGenerate(t, kind, i), mustGenerate(t, kind, i)
			if len(a) != len(b) {
				t.Fatalf("%s seed %d: %d files, then %d", kind, i, len(a), len(b))
			}
			for j := range a {
				if a[j].Path != b[j].Path || !bytes.Equal(a[j].Content, b[j].Content) {
					t.Fatalf("%s seed %d: %s differs between two runs", kind, i, a[j].Path)
				}
			}
			ba, _ := FromFiles(a)
			bb, _ := FromFiles(b)
			if ba.Hash() != bb.Hash() {
				t.Fatalf("%s seed %d: bundle hash differs", kind, i)
			}
		}
	}
}

var (
	externalRef = regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}|@import|url\(\s*["']?(https?:|//)|mailto:|tel:`)
	badWord     = regexp.MustCompile(`(?i)\b(vpn|proxy|proxies|mistgate|tunnel|hysteria|vless|wireguard)\b`)
	storageAPI  = regexp.MustCompile(`localStorage|sessionStorage|indexedDB|document\.cookie|XMLHttpRequest|fetch\s*\(|sendBeacon|WebSocket|EventSource|\.submit\s*\(`)
	formTag     = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	buttonTag   = regexp.MustCompile(`(?is)<button\b[^>]*>`)
	submitInput = regexp.MustCompile(`(?i)type\s*=\s*["']?submit`)
	svgNS       = regexp.MustCompile(`http://www\.w3\.org/(2000/svg|1999/xlink)`)
)

func TestGenerateClean(t *testing.T) {
	for _, kind := range kindIDs(t) {
		t.Run(kind, func(t *testing.T) {
			for i := 0; i < seedsPerKind; i++ {
				for _, f := range mustGenerate(t, kind, i) {
					if f.Path == "robots.txt" {
						continue
					}
					text := svgNS.ReplaceAll(f.Content, nil)
					if m := externalRef.Find(text); m != nil {
						t.Errorf("seed %d %s: reaches outside the site: %q", i, f.Path, m)
					}
					if m := badWord.Find(text); m != nil {
						t.Errorf("seed %d %s: telltale word %q", i, f.Path, m)
					}
					if m := storageAPI.Find(text); m != nil {
						t.Errorf("seed %d %s: %q", i, f.Path, m)
					}
					for _, form := range formTag.FindAll(text, -1) {
						if regexp.MustCompile(`(?i)\b(action|method|enctype)\s*=`).Match(form) {
							t.Errorf("seed %d %s: a form that can send: %s", i, f.Path, form)
						}
					}
					for _, b := range buttonTag.FindAll(text, -1) {
						if !regexp.MustCompile(`(?i)type\s*=\s*["']?button`).Match(b) {
							t.Errorf("seed %d %s: a button that could submit: %s", i, f.Path, b)
						}
					}
					if submitInput.Match(text) {
						t.Errorf("seed %d %s: a submit input", i, f.Path)
					}
					if path.Ext(f.Path) == ".html" && !interactive[kind] && (bytes.Contains(text, []byte("<script")) || bytes.Contains(text, []byte("<form"))) {
						t.Errorf("seed %d %s: a script or form in a kind without them", i, f.Path)
					}
				}
			}
		})
	}
}

// The same seed at different times of the run, and a seed's files, never depend on other seeds having been made first.
func TestGenerateIndependentOfOrder(t *testing.T) {
	kind := kindIDs(t)[0]
	first := mustGenerate(t, kind, 7)
	for i := 0; i < 20; i++ {
		mustGenerate(t, kind, i)
	}
	again := mustGenerate(t, kind, 7)
	for i := range first {
		if !bytes.Equal(first[i].Content, again[i].Content) {
			t.Fatalf("%s differs after other sites were made", first[i].Path)
		}
	}
}

// With -decoy-dump=<dir> the test writes -decoy-dump-n sites of every kind into <dir>/<kind>-<n>/ so they can be opened and looked at.
func TestDumpSites(t *testing.T) {
	if *decoyDump == "" {
		t.Skip("no -decoy-dump")
	}
	for _, kind := range kindIDs(t) {
		for n := 1; n <= *decoyDumpN; n++ {
			files, err := Generate(kind, []byte(fmt.Sprintf("dump-%s-%d", kind, n)))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(*decoyDump, fmt.Sprintf("%s-%d", kind, n))
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				p := filepath.Join(dir, filepath.FromSlash(f.Path))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, f.Content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
