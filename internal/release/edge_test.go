package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func edgeManifestBytes(t *testing.T) []byte {
	t.Helper()
	m := &EdgeManifest{Version: "v0.1.40", Built: 1_789_000_000, Expires: 1_800_000_000, File: EdgeBundleName, Size: 11_799_257, SHA256: strings.Repeat("ab", 32)}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// One release key signs the node bundle, the panel and the edge, and no signature is good for another's manifest: the
// contexts keep them apart even when the bytes are the same.
func TestEdgeSignatureIsBoundToItsContext(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	body := edgeManifestBytes(t)

	sig := SignEdge(priv, body)
	m, err := VerifyEdge(pub, body, sig)
	if err != nil {
		t.Fatalf("an edge signature does not verify as edge: %v", err)
	}
	if m.Version != "v0.1.40" || m.File != EdgeBundleName || m.Size != 11_799_257 {
		t.Fatalf("manifest %+v", m)
	}
	// edge signature as panel / node
	if _, err := VerifyPanel(pub, body, sig); err == nil {
		t.Fatal("an edge signature verifies as a panel manifest")
	}
	if _, err := Verify(pub, body, sig); err == nil {
		t.Fatal("an edge signature verifies as a node manifest")
	}
	// panel / node signature as edge
	if _, err := VerifyEdge(pub, body, SignPanel(priv, body)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a panel signature verifies as edge: %v", err)
	}
	if _, err := VerifyEdge(pub, body, Sign(priv, body)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a node signature verifies as edge: %v", err)
	}
	// the other key, a changed byte, a short signature
	other, _, _ := GenerateKey()
	if _, err := VerifyEdge(other, body, sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a foreign key: %v", err)
	}
	if _, err := VerifyEdge(pub, bytes.Replace(body, []byte("v0.1.40"), []byte("v0.1.41"), 1), sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a changed manifest: %v", err)
	}
	if _, err := VerifyEdge(pub, body, sig[:63]); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a short signature: %v", err)
	}
	if _, err := VerifyEdge(ed25519.PublicKey{1}, body, sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a short key: %v", err)
	}
}

func TestEdgeManifestIsStrict(t *testing.T) {
	good := string(edgeManifestBytes(t))
	if _, err := ParseEdge([]byte(good)); err != nil {
		t.Fatal(err)
	}
	// field order and names are the contract with `edge deploy`
	if want := `{"version":"v0.1.40","built":1789000000,"expires":1800000000,"file":"mistgate-edge.tar.gz","size":11799257,"sha256":"` + strings.Repeat("ab", 32) + `"}`; compactJSON(good) != want {
		t.Fatalf("manifest layout:\n%s", good)
	}
	for name, bad := range map[string]string{
		"unknown field": strings.Replace(good, `"size"`, `"extra": 1, "size"`, 1),
		"trailing data": good + "{}",
		"wrong file":    strings.Replace(good, EdgeBundleName, "other.tar.gz", 1),
		"path in file":  strings.Replace(good, EdgeBundleName, "../mistgate-edge.tar.gz", 1),
		"zero size":     strings.Replace(good, "11799257", "0", 1),
		"huge size":     strings.Replace(good, "11799257", strconv.Itoa(MaxEdgeBundle+1), 1),
		"short sha":     strings.Replace(good, strings.Repeat("ab", 32), "abcd", 1),
		"upper sha":     strings.Replace(good, strings.Repeat("ab", 32), strings.Repeat("AB", 32), 1),
		"expired first": strings.Replace(good, `"expires": 1800000000`, `"expires": 1788999999`, 1),
		"bad version":   strings.Replace(good, "v0.1.40", "v0 1", 1),
		"empty":         "",
		"not json":      "nope",
	} {
		if _, err := ParseEdge([]byte(bad)); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	m, _ := ParseEdge([]byte(good))
	if m.Check(time.Unix(1_790_000_000, 0)) != nil || !errors.Is(m.Check(time.Unix(1_800_000_001, 0)), ErrExpired) {
		t.Fatal("Check")
	}
	if err := m.VerifyFile(strings.NewReader("short")); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("a short file: %v", err)
	}
}

func compactJSON(s string) string {
	return regexp.MustCompile(`\s*([{},:])\s*`).ReplaceAllString(s, "$1")
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func edgeInputs(t *testing.T) EdgeInputs {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"worker.js": "export default {};\n", "provision.js": "export default {};\n", "panel.wasm": "\x00asm\x01\x00\x00\x00",
		"web/index.html": "<html>", "web/assets/a-1.js": "a", "web/assets/b-2.css": "b", "web/.gitkeep": "",
	})
	return EdgeInputs{Version: "v0.1.40", WorkerJS: filepath.Join(dir, "worker.js"), ProvisionJS: filepath.Join(dir, "provision.js"),
		Wasm: filepath.Join(dir, "panel.wasm"), AssetsDir: filepath.Join(dir, "web")}
}

func bundleOf(t *testing.T, in EdgeInputs) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteEdgeBundle(&buf, in); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The tarball is a function of its inputs: file times, modes and the order the OS lists a directory in do not reach it.
func TestEdgeBundleIsDeterministic(t *testing.T) {
	a := edgeInputs(t)
	first := bundleOf(t, a)
	// the same content in another place, with other mtimes and modes
	b := edgeInputs(t)
	later := time.Now().Add(-1000 * time.Hour)
	for _, p := range []string{b.WorkerJS, b.Wasm, filepath.Join(b.AssetsDir, "index.html"), filepath.Join(b.AssetsDir, "assets", "a-1.js")} {
		if err := os.Chtimes(p, later, later); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o640); err != nil && runtime.GOOS != "windows" {
			t.Fatal(err)
		}
	}
	second := bundleOf(t, b)
	if sha(first) != sha(second) {
		t.Fatalf("two builds differ: %s %s", sha(first), sha(second))
	}
	// but not insensitive: a changed asset changes the tarball
	writeTree(t, b.AssetsDir, map[string]string{"assets/a-1.js": "changed"})
	if sha(bundleOf(t, b)) == sha(first) {
		t.Fatal("a changed asset does not change the bundle")
	}

	zr, err := gzip.NewReader(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if zr.Name != "" || zr.Comment != "" || !zr.ModTime.IsZero() && zr.ModTime.Unix() != 0 {
		t.Fatalf("gzip header %q %q %v", zr.Name, zr.Comment, zr.ModTime)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		want := int64(0o644)
		if h.Typeflag == tar.TypeDir {
			want = 0o755
		}
		if h.Mode != want || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.ModTime.Unix() != 0 {
			t.Errorf("%s: mode %o uid %d gid %d uname %q gname %q mtime %v", h.Name, h.Mode, h.Uid, h.Gid, h.Uname, h.Gname, h.ModTime)
		}
	}
	trimmed := make([]string, len(names))
	for i, n := range names {
		trimmed[i] = strings.TrimSuffix(n, "/")
	}
	if !sort.StringsAreSorted(trimmed) {
		t.Fatalf("entries are not sorted: %v", names)
	}
	want := []string{"assets/", "assets/assets/", "assets/assets/a-1.js", "assets/assets/b-2.css", "assets/index.html", "edge.json", "panel.wasm", "provision.js", "worker.js"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("entries %v, want %v (.gitkeep is not part of the assets)", names, want)
	}

	files, err := ReadEdgeBundle(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if string(files["worker.js"]) != "export default {};\n" || string(files["assets/index.html"]) != "<html>" || !bytes.HasPrefix(files["panel.wasm"], []byte("\x00asm")) {
		t.Fatalf("contents: %q", files)
	}
	cfg, err := NewEdgeConfig("v0.1.40").Marshal()
	if err != nil || !bytes.Equal(files["edge.json"], cfg) {
		t.Fatalf("edge.json: %v", err)
	}
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestEdgeBundleRefusals(t *testing.T) {
	in := edgeInputs(t)
	for name, mutate := range map[string]func(*EdgeInputs){
		"no index.html":   func(i *EdgeInputs) { os.Remove(filepath.Join(i.AssetsDir, "index.html")) },
		"missing worker":  func(i *EdgeInputs) { i.WorkerJS = filepath.Join(filepath.Dir(i.WorkerJS), "nope.js") },
		"missing wasm":    func(i *EdgeInputs) { i.Wasm = "" },
		"a bad version":   func(i *EdgeInputs) { i.Version = "v1 2" },
		"missing assets":  func(i *EdgeInputs) { i.AssetsDir = filepath.Join(i.AssetsDir, "nope") },
		"an empty folder": func(i *EdgeInputs) { i.AssetsDir = t.TempDir() },
	} {
		c := edgeInputs(t)
		mutate(&c)
		if err := WriteEdgeBundle(io.Discard, c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("/etc/passwd", filepath.Join(in.AssetsDir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := WriteEdgeBundle(io.Discard, in); err == nil {
			t.Error("a symbolic link in the assets was accepted")
		}
	}
}

func TestReadEdgeBundleRefusesUnsafePaths(t *testing.T) {
	for _, name := range []string{"../evil", "/abs", "a/../../b", `a\b`} {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
		tw.Close()
		zw.Close()
		if _, err := ReadEdgeBundle(&buf); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

// ---- edge.json against the wrangler templates --------------------------------------------------------------------------

type tomlDoc map[string][]map[string]any // "" is the top level; [x] and [[x]] append a table named x

// parseTOML reads the subset the wrangler templates use: tables, and key = "string" | true | false | ["a", "b"] lines. Anything
// else is an error, so a template that grows past this fails the test instead of being half read.
func parseTOML(t *testing.T, path string) tomlDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := tomlDoc{"": {{}}}
	cur := doc[""][0]
	for n, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name := strings.Trim(line, "[]")
			cur = map[string]any{}
			if strings.HasPrefix(line, "[[") || len(doc[name]) == 0 {
				doc[name] = append(doc[name], cur)
			} else {
				cur = doc[name][0]
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("%s:%d: not key = value: %q", path, n+1, line)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch {
		case strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) && strings.Count(val, `"`) == 2:
			cur[key] = val[1 : len(val)-1]
		case val == "true" || val == "false":
			cur[key] = val == "true"
		case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
			list := []string{}
			for _, item := range strings.Split(strings.Trim(val, "[]"), ",") {
				if item = strings.TrimSpace(item); item != "" {
					if !strings.HasPrefix(item, `"`) || !strings.HasSuffix(item, `"`) {
						t.Fatalf("%s:%d: array item %q", path, n+1, item)
					}
					list = append(list, strings.Trim(item, `"`))
				}
			}
			cur[key] = list
		default:
			t.Fatalf("%s:%d: value %q", path, n+1, val)
		}
	}
	return doc
}

func (d tomlDoc) str(table, key string) string {
	if len(d[table]) == 0 {
		return ""
	}
	s, _ := d[table][0][key].(string)
	return s
}

// interfaceFields lists the members of `export interface <name> {` in env.ts.
func interfaceFields(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)export interface ` + name + ` \{(.*?)\n\}`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no interface %s in env.ts", name)
	}
	var out []string
	for _, f := range regexp.MustCompile(`(?m)^  ([A-Z][A-Z0-9_]*)\??:`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, f[1])
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// edge.json is written by Go (NewEdgeConfig); the templates in edge/worker are what a developer deploys by hand and what the
// Worker code was written against. They must say the same thing about bindings, classes, migrations, cron, secrets and date.
func TestEdgeConfigMatchesWranglerTemplates(t *testing.T) {
	worker := filepath.Join("..", "..", "edge", "worker")
	cfg := NewEdgeConfig("v0.1.40")
	templates := map[string]struct {
		file, entry, envInterface string
	}{
		"main":      {"wrangler.example.toml", "src/index.ts", "Env"},
		"provision": {"wrangler.provision.example.toml", "src/provision-index.ts", "ProvisionEnv"},
	}
	docs := map[string]tomlDoc{}
	roleOfScript := map[string]string{}
	for role, tpl := range templates {
		docs[role] = parseTOML(t, filepath.Join(worker, tpl.file))
		roleOfScript[docs[role].str("", "name")] = role
	}
	envRaw, err := os.ReadFile(filepath.Join(worker, "src", "env.ts"))
	if err != nil {
		t.Fatal(err)
	}
	bundleScript, err := os.ReadFile(filepath.Join(worker, "build-bundle.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scripts) != len(templates) {
		t.Fatalf("%d scripts in edge.json, %d templates", len(cfg.Scripts), len(templates))
	}

	for _, s := range cfg.Scripts {
		tpl, ok := templates[s.Role]
		if !ok {
			t.Fatalf("script role %q has no template", s.Role)
		}
		doc := docs[s.Role]
		t.Run(s.Role, func(t *testing.T) {
			if name := doc.str("", "name"); s.DefaultName != name {
				t.Errorf("default_name %q, template %q", s.DefaultName, name)
			}
			if date := doc.str("", "compatibility_date"); cfg.CompatibilityDate != date {
				t.Errorf("compatibility_date %q, template %q", cfg.CompatibilityDate, date)
			}
			if len(cfg.CompatibilityFlags) != 0 || doc[""][0]["compatibility_flags"] != nil {
				t.Error("compatibility flags: edge.json and the template must both have none")
			}
			// the entry module of the template is the one build-bundle.mjs bundles into main_module
			if !strings.Contains(string(bundleScript), strings.TrimSuffix(s.MainModule, ".js")+`: "`+doc.str("", "main")+`"`) || doc.str("", "main") != tpl.entry {
				t.Errorf("main_module %s is not built from the template's main %q", s.MainModule, doc.str("", "main"))
			}

			// modules: the script and the wasm the template's CompiledWasm rule is for
			wantModules := []EdgeModule{{Name: s.MainModule, Type: "esm"}, {Name: "panel.wasm", Type: "compiled_wasm"}}
			if !reflect.DeepEqual(s.Modules, wantModules) {
				t.Errorf("modules %v", s.Modules)
			}
			if rules := doc["rules"]; len(rules) != 1 || rules[0]["type"] != "CompiledWasm" || !reflect.DeepEqual(rules[0]["globs"], []string{"**/*.wasm"}) {
				t.Errorf("rules %v: the wasm is not imported as CompiledWasm", rules)
			}

			// Durable Objects: no script_name = ours; script_name = the other script's, named by role
			var own, external []EdgeDO
			for _, b := range doc["durable_objects.bindings"] {
				d := EdgeDO{Binding: b["name"].(string), ClassName: b["class_name"].(string)}
				if sn, ok := b["script_name"].(string); ok {
					if d.ScriptRole = roleOfScript[sn]; d.ScriptRole == "" || d.ScriptRole == s.Role {
						t.Errorf("binding %s: script_name %q is not the other script", d.Binding, sn)
					}
					external = append(external, d)
				} else {
					own = append(own, d)
				}
			}
			if !reflect.DeepEqual(s.DurableObjects, own) {
				t.Errorf("durable_objects %v, template %v", s.DurableObjects, own)
			}
			if !reflect.DeepEqual(s.ExternalObjects, external) {
				t.Errorf("durable_objects_external %v, template %v", s.ExternalObjects, external)
			}

			var migrations []EdgeMigrate
			for _, m := range doc["migrations"] {
				if len(m) != 2 {
					t.Fatalf("migration with keys other than tag and new_sqlite_classes: %v", m)
				}
				migrations = append(migrations, EdgeMigrate{Tag: m["tag"].(string), NewSqliteClasses: m["new_sqlite_classes"].([]string)})
			}
			if !reflect.DeepEqual(s.Migrations, migrations) {
				t.Errorf("migrations %v, template %v", s.Migrations, migrations)
			}
			// every class of the script has a migration that creates it
			created := map[string]bool{}
			for _, m := range migrations {
				for _, c := range m.NewSqliteClasses {
					created[c] = true
				}
			}
			for _, d := range s.DurableObjects {
				if !created[d.ClassName] {
					t.Errorf("class %s has no migration", d.ClassName)
				}
			}

			var d1 []EdgeBinding
			for _, b := range doc["d1_databases"] {
				d1 = append(d1, EdgeBinding{Binding: b["binding"].(string)})
			}
			if !reflect.DeepEqual(s.D1, d1) {
				t.Errorf("d1 %v, template %v", s.D1, d1)
			}

			bound := map[string]bool{}
			for _, b := range d1 {
				bound[b.Binding] = true
			}
			for _, d := range append(append([]EdgeDO{}, own...), external...) {
				bound[d.Binding] = true
			}
			if len(doc["assets"]) == 0 {
				if s.Assets != nil {
					t.Errorf("assets %v but the template has none", s.Assets)
				}
			} else {
				a := doc["assets"][0]
				if s.Assets == nil || s.Assets.Binding != a["binding"] || s.Assets.Directory != "assets" {
					t.Errorf("assets %v, template %v (the directory is the tarball's assets/)", s.Assets, a)
				}
				bound[a["binding"].(string)] = true
			}

			var cron []string
			if len(doc["triggers"]) > 0 {
				cron = doc["triggers"][0]["crons"].([]string)
			}
			if !reflect.DeepEqual(s.Cron, cron) {
				t.Errorf("cron %v, template %v", s.Cron, cron)
			}

			// secrets and vars: everything in the script's env interface that is not a binding is one or the other, once, and
			// the template mentions each by name
			var names []string
			for _, n := range append(append([]string{}, s.Secrets...), s.Vars...) {
				names = append(names, n)
				if !regexp.MustCompile(`\b` + n + `\b`).MatchString(string(readFile(t, filepath.Join(worker, tpl.file)))) {
					t.Errorf("%s is not mentioned in %s", n, tpl.file)
				}
			}
			var settings []string
			for _, f := range interfaceFields(t, string(envRaw), tpl.envInterface) {
				if !bound[f] {
					settings = append(settings, f)
				}
			}
			if !reflect.DeepEqual(sortedCopy(names), settings) {
				t.Errorf("secrets+vars %v, but %s has these settings besides its bindings: %v", sortedCopy(names), tpl.envInterface, settings)
			}
			// ...and every binding in the interface is declared by the template
			for _, f := range interfaceFields(t, string(envRaw), tpl.envInterface) {
				if bound[f] {
					delete(bound, f)
				}
			}
			if len(bound) != 0 {
				t.Errorf("bindings in the template but not in %s: %v", tpl.envInterface, bound)
			}
		})
	}
	if !cfg.Scripts[1].Routes || cfg.Scripts[0].Routes {
		t.Error("only the main script takes routes")
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
