package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	files := map[string]string{"web/index.html": "<html>", "web/assets/a-1.js": "a", "web/assets/b-2.css": "b", "web/.gitkeep": ""}
	for _, s := range NewEdgeConfig("v0.1.40").Scripts {
		for _, m := range s.Modules {
			files["modules/"+m.Name] = "export default {};\n"
			if strings.HasSuffix(m.Name, ".wasm") {
				files["modules/"+m.Name] = "\x00asm\x01\x00\x00\x00" + m.Name // a different body per role
			}
		}
	}
	writeTree(t, dir, files)
	return EdgeInputs{Version: "v0.1.40", ModulesDir: filepath.Join(dir, "modules"), AssetsDir: filepath.Join(dir, "web")}
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
	for _, p := range []string{filepath.Join(b.ModulesDir, "front.js"), filepath.Join(b.ModulesDir, "admin.wasm"), filepath.Join(b.AssetsDir, "index.html"), filepath.Join(b.AssetsDir, "assets", "a-1.js")} {
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
	want := []string{"admin.js", "admin.wasm", "assets/", "assets/assets/", "assets/assets/a-1.js", "assets/assets/b-2.css", "assets/index.html", "edge.json",
		"front.js", "jobs.js", "jobs.wasm", "link.js", "link.wasm", "provision.js", "provision.wasm", "public.js", "public.wasm"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("entries %v, want %v (.gitkeep is not part of the assets)", names, want)
	}

	files, err := ReadEdgeBundle(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if string(files["front.js"]) != "export default {};\n" || string(files["assets/index.html"]) != "<html>" || !bytes.HasPrefix(files["jobs.wasm"], []byte("\x00asm")) {
		t.Fatalf("contents: %q", files)
	}
	parsed, err := ParseEdgeConfig(files["edge.json"])
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.CheckFiles(files); err != nil {
		t.Fatal(err)
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
		"missing front":   func(i *EdgeInputs) { os.Remove(filepath.Join(i.ModulesDir, "front.js")) },
		"missing wasm":    func(i *EdgeInputs) { os.Remove(filepath.Join(i.ModulesDir, "public.wasm")) },
		"no modules dir":  func(i *EdgeInputs) { i.ModulesDir = "" },
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

// The compressed size is bounded by MaxEdgeBundle; the unpacked size has its own bound, so a tiny gzip bomb is refused.
func TestReadEdgeBundleBoundsTheUnpackedSize(t *testing.T) {
	old := maxEdgeUnpacked
	maxEdgeUnpacked = 1000
	defer func() { maxEdgeUnpacked = old }()
	pack := func(sizes ...int) *bytes.Buffer {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		for i, n := range sizes {
			if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Mode: 0o644, Size: int64(n), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			tw.Write(make([]byte, n))
		}
		tw.Close()
		zw.Close()
		return &buf
	}
	if _, err := ReadEdgeBundle(pack(400, 600)); err != nil {
		t.Fatalf("a bundle of exactly the limit: %v", err)
	}
	if _, err := ReadEdgeBundle(pack(400, 601)); err == nil {
		t.Fatal("accepted a bundle that unpacks past the limit")
	}
	if _, err := ReadEdgeBundle(pack(5000)); err == nil {
		t.Fatal("accepted one file larger than the limit")
	}
}

// ---- edge.json against the TypeScript side ----------------------------------------------------------------------------

// interfaceFields lists the members of `export interface <name> {` (or `... extends X {`) in env.ts.
func interfaceFields(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)export interface ` + name + `(?: extends \w+)? \{(.*?)\n\}`).FindStringSubmatch(src)
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

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// describe renders everything a script is bound to in one line, so that the table below reads like the bindings table of
// the design (design/cloudflare-edition) and a difference is one line in the failure.
func describe(s EdgeScript) string {
	list := func(items []string) string {
		if len(items) == 0 {
			return "-"
		}
		return strings.Join(items, ",")
	}
	var mods, own, ext, svc, d1 []string
	for _, m := range s.Modules {
		mods = append(mods, m.Name+":"+m.Type)
	}
	for _, d := range s.DurableObjects {
		own = append(own, d.Binding+"="+d.ClassName)
	}
	for _, d := range s.ExternalObjects {
		ext = append(ext, d.Binding+"="+d.ScriptRole+"."+d.ClassName)
	}
	for _, v := range s.Services {
		entry := ""
		if v.Entrypoint != "" {
			entry = "#" + v.Entrypoint
		}
		svc = append(svc, v.Binding+">"+v.ServiceRole+entry)
	}
	for _, b := range s.D1 {
		d1 = append(d1, b.Binding)
	}
	assets := "-"
	if s.Assets != nil {
		assets = s.Assets.Binding + "=" + s.Assets.Directory + "/"
	}
	var migrations []string
	for _, m := range s.Migrations {
		migrations = append(migrations, m.Tag+":"+strings.Join(m.NewSqliteClasses, "+"))
	}
	return fmt.Sprintf("name=%s modules=%s do=%s ext=%s mig=%s d1=%s svc=%s assets=%s secrets=%s vars=%s cron=%s routes=%t",
		s.DefaultName, list(mods), list(own), list(ext), list(migrations), list(d1), list(svc), assets, list(s.Secrets), list(s.Vars), list(s.Cron), s.Routes)
}

// The bindings of every script, as edge/worker/src/{front.ts,roles/*.ts,env.ts} read them (step 3's report, "Bindings each
// script expects"). The ADMIN_PREFIX and SUB_PREFIX of the admin role are secrets, not vars: they are the secret paths.
var wantBindings = map[string]string{
	RoleFront: "name=mistgate-edge modules=front.js:esm do=LIMITER=Limiter,NODELINK=NodeLink ext=- mig=v1:Limiter,v2:NodeLink d1=DB " +
		"svc=PUBLIC>public,ADMIN>admin,LINK>link,PANEL_LINK>link#PanelLink assets=- secrets=- vars=- cron=- routes=true",
	RoleLink: "name=mistgate-edge-link modules=link.js:esm,link.wasm:compiled_wasm do=- ext=LIMITER=front.Limiter,NODELINK=front.NodeLink mig=- d1=DB " +
		"svc=- assets=- secrets=MASTER_KEY vars=- cron=- routes=false",
	RolePublic: "name=mistgate-edge-public modules=public.js:esm,public.wasm:compiled_wasm do=- ext=LIMITER=front.Limiter,NODELINK=front.NodeLink mig=- d1=DB " +
		"svc=- assets=ASSETS=assets/ secrets=MASTER_KEY vars=- cron=- routes=false",
	RoleAdmin: "name=mistgate-edge-admin modules=admin.js:esm,admin.wasm:compiled_wasm do=- " +
		"ext=LIMITER=front.Limiter,NODELINK=front.NodeLink,PROVISIONER=provision.Provisioner mig=- d1=DB svc=- assets=ASSETS=assets/ " +
		"secrets=MASTER_KEY,ADMIN_PREFIX,SUB_PREFIX,SETUP_TOKEN_SHA256 vars=PUBLIC_URL,ADMIN_HOST cron=- routes=false",
	RoleJobs: "name=mistgate-edge-jobs modules=jobs.js:esm,jobs.wasm:compiled_wasm do=- ext=NODELINK=front.NodeLink,PROVISIONER=provision.Provisioner mig=- d1=DB " +
		"svc=- assets=- secrets=MASTER_KEY vars=- cron=* * * * * routes=false",
	RoleProvision: "name=mistgate-edge-provision modules=provision.js:esm,provision.wasm:compiled_wasm do=PROVISIONER=Provisioner " +
		"ext=LIMITER=front.Limiter,NODELINK=front.NodeLink mig=v1:Provisioner d1=DB svc=- assets=- secrets=MASTER_KEY vars=- cron=- routes=false",
}

func TestEdgeConfigBindings(t *testing.T) {
	cfg := NewEdgeConfig("v0.1.40")
	if cfg.Schema != 2 || EdgeSchema != 2 {
		t.Fatalf("schema %d", cfg.Schema)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range cfg.Scripts {
		order = append(order, s.Role)
		want, ok := wantBindings[s.Role]
		if !ok {
			t.Fatalf("role %q is not in the bindings table", s.Role)
		}
		if got := describe(s); got != want {
			t.Errorf("%s:\n got %s\nwant %s", s.Role, got, want)
		}
	}
	if want := []string{"link", "provision", "public", "admin", "jobs", "front"}; !reflect.DeepEqual(order, want) {
		t.Errorf("scripts are in the order %v, want the upload order %v", order, want)
	}
	if len(order) != len(wantBindings) {
		t.Errorf("%d scripts, %d in the table", len(order), len(wantBindings))
	}
	if cfg.CompatibilityDate != "2026-10-01" || len(cfg.CompatibilityFlags) != 0 {
		t.Errorf("compatibility %q %v", cfg.CompatibilityDate, cfg.CompatibilityFlags)
	}
	// the cron and the routes are each in one script
	for _, s := range cfg.Scripts {
		if (len(s.Cron) > 0) != (s.Role == RoleJobs) || s.Routes != (s.Role == RoleFront) {
			t.Errorf("%s: cron %v, routes %v", s.Role, s.Cron, s.Routes)
		}
	}
}

// Nothing in edge.json is bound that the TypeScript of the role does not read, and nothing it reads is missing: the names
// of the bindings, secrets and vars of a script are the fields of its environment interface in env.ts.
func TestEdgeConfigMatchesTheEnvironmentOfEachRole(t *testing.T) {
	env := string(readFile(t, filepath.Join("..", "..", "edge", "worker", "src", "env.ts")))
	interfaces := map[string][]string{
		RoleFront: {"FrontEnv", "NodeLinkEnv"}, RoleLink: {"LinkEnv"}, RolePublic: {"PublicEnv"},
		RoleAdmin: {"Env"}, RoleJobs: {"JobsEnv"}, RoleProvision: {"ProvisionEnv"},
	}
	for _, s := range NewEdgeConfig("v0.1.40").Scripts {
		var bound []string
		for _, b := range s.D1 {
			bound = append(bound, b.Binding)
		}
		for _, d := range append(append([]EdgeDO{}, s.DurableObjects...), s.ExternalObjects...) {
			bound = append(bound, d.Binding)
		}
		for _, v := range s.Services {
			bound = append(bound, v.Binding)
		}
		if s.Assets != nil {
			bound = append(bound, s.Assets.Binding)
		}
		bound = append(append(bound, s.Secrets...), s.Vars...)
		var fields []string
		for _, name := range interfaces[s.Role] {
			fields = append(fields, interfaceFields(t, env, name)...)
		}
		if !reflect.DeepEqual(sortedCopy(bound), sortedCopy(fields)) {
			t.Errorf("%s: edge.json binds %v, but its environment (%v) has %v", s.Role, sortedCopy(bound), interfaces[s.Role], sortedCopy(fields))
		}
	}
}

// A script that binds a role that does not exist, a class nobody owns, or itself, is refused, and so is an order other than
// the upload order.
func TestEdgeConfigRefusesBindingsThatLeadNowhere(t *testing.T) {
	mutations := map[string]func(c *EdgeConfig){
		"a service in a role that does not exist": func(c *EdgeConfig) { c.Script(RoleFront).Services[0].ServiceRole = "ghost" },
		"a service on itself":                     func(c *EdgeConfig) { c.Script(RoleFront).Services[0].ServiceRole = RoleFront },
		"an object in a role that does not exist": func(c *EdgeConfig) { c.Script(RoleAdmin).ExternalObjects[0].ScriptRole = "ghost" },
		"an object in the script itself":          func(c *EdgeConfig) { c.Script(RoleAdmin).ExternalObjects[0].ScriptRole = RoleAdmin },
		"a class the role does not own":           func(c *EdgeConfig) { c.Script(RoleAdmin).ExternalObjects[2].ClassName = "Ghost" },
		"a migration for a class not owned":       func(c *EdgeConfig) { c.Script(RoleFront).Migrations[0].NewSqliteClasses = []string{"Ghost"} },
		"an owned class without a migration":      func(c *EdgeConfig) { c.Script(RoleProvision).Migrations = nil },
		"a binding used twice":                    func(c *EdgeConfig) { c.Script(RoleJobs).Secrets = append(c.Script(RoleJobs).Secrets, "DB") },
		"a missing role":                          func(c *EdgeConfig) { c.Scripts = c.Scripts[:5] },
		"a repeated role":                         func(c *EdgeConfig) { c.Scripts[1] = c.Scripts[0] },
		"another order":                           func(c *EdgeConfig) { c.Scripts[0], c.Scripts[5] = c.Scripts[5], c.Scripts[0] },
		"a module of another role":                func(c *EdgeConfig) { c.Script(RolePublic).Modules[1].Name = "admin.wasm" },
		"a wasm in front": func(c *EdgeConfig) {
			c.Script(RoleFront).Modules = append(c.Script(RoleFront).Modules, EdgeModule{Name: "front.wasm", Type: "compiled_wasm"})
		},
		"an assets folder elsewhere": func(c *EdgeConfig) { c.Script(RolePublic).Assets.Directory = "static" },
		"a name of another role":     func(c *EdgeConfig) { c.Script(RoleLink).DefaultName = "mistgate-edge-admin" },
		"another schema":             func(c *EdgeConfig) { c.Schema = 1 },
	}
	for name, mutate := range mutations {
		cfg := NewEdgeConfig("v0.1.40")
		// deep copy through JSON, so that a mutation of a shared slice does not reach the next case
		raw, err := cfg.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		cfg = EdgeConfig{}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseEdgeConfig(t *testing.T) {
	good, err := NewEdgeConfig("v0.1.40").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseEdgeConfig(good)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*cfg, NewEdgeConfig("v0.1.40")) {
		t.Error("what edge.json says is not what NewEdgeConfig built")
	}
	// the single-Worker bundle gets a message that names the reason, not a decode error
	_, err = ParseEdgeConfig([]byte(`{"schema": 1, "version": "v0.1.40", "scripts": [{"role": "main"}]}`))
	if !errors.Is(err, ErrEdgeSchema) || !strings.Contains(err.Error(), "single-Worker") {
		t.Errorf("schema 1: %v", err)
	}
	if _, err = ParseEdgeConfig([]byte(`{"schema": 3}`)); !errors.Is(err, ErrEdgeSchema) {
		t.Errorf("schema 3: %v", err)
	}
	for name, bad := range map[string][]byte{
		"unknown field": bytes.Replace(good, []byte(`"routes"`), []byte(`"extra": 1, "routes"`), 1),
		"trailing data": append(append([]byte{}, good...), []byte("{}")...),
		"not json":      []byte("nope"),
		"no schema":     []byte(`{}`),
	} {
		if _, err := ParseEdgeConfig(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEdgeConfigCheckFiles(t *testing.T) {
	cfg := NewEdgeConfig("v0.1.40")
	full := func() map[string][]byte {
		files := map[string][]byte{EdgeConfigName: {'{'}, "assets/index.html": {'<'}, "assets/x/a.js": {'a'}}
		for _, s := range cfg.Scripts {
			for _, m := range s.Modules {
				files[m.Name] = []byte{1}
			}
		}
		return files
	}
	if err := cfg.CheckFiles(full()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string][]byte){
		"a missing wasm":  func(f map[string][]byte) { delete(f, "jobs.wasm") },
		"an empty module": func(f map[string][]byte) { f["front.js"] = nil },
		"an extra module": func(f map[string][]byte) { f["worker.js"] = []byte{1} },
		"a front wasm":    func(f map[string][]byte) { f["front.wasm"] = []byte{1} },
		"no SPA":          func(f map[string][]byte) { delete(f, "assets/index.html") },
	} {
		f := full()
		mutate(f)
		if err := cfg.CheckFiles(f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEdgeScriptNames(t *testing.T) {
	if EdgeScriptName("acme", RoleFront) != "acme" || EdgeScriptName("acme", RoleLink) != "acme-link" {
		t.Error("script names")
	}
}

// ---- the generated wrangler configs -----------------------------------------------------------------------------------

// The committed edge/worker/wrangler.<role>.example.toml are what NewEdgeConfig renders: a binding changed in one place is
// changed in both. Fix a failure with `go generate ./internal/release`.
func TestWranglerConfigsAreCurrent(t *testing.T) {
	dir := filepath.Join("..", "..", "edge", "worker")
	cfg := NewEdgeConfig("v0.0.0-dev")
	want := cfg.WranglerFiles()
	if len(want) != len(EdgeRoles) {
		t.Fatalf("%d configs for %d roles", len(want), len(EdgeRoles))
	}
	for name, body := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v (go generate ./internal/release)", name, err)
			continue
		}
		if strings.ReplaceAll(string(got), "\r\n", "\n") != body {
			t.Errorf("%s is not what NewEdgeConfig renders: go generate ./internal/release", name)
		}
	}
	// no hand-written config of the single-Worker days is left next to them
	listed, _ := filepath.Glob(filepath.Join(dir, "wrangler*.example.toml"))
	for _, f := range listed {
		if _, ok := want[filepath.Base(f)]; !ok {
			t.Errorf("%s is not rendered from edge.json: delete it", filepath.Base(f))
		}
	}
}

// Every name a config binds to is the script name of another role, and the entry file it names exists.
func TestWranglerConfigsPointAtTheirNeighbours(t *testing.T) {
	cfg := NewEdgeConfig("v0.0.0-dev")
	worker := filepath.Join("..", "..", "edge", "worker")
	for _, s := range cfg.Scripts {
		body := cfg.WranglerConfig(s.Role)
		if !strings.Contains(body, "name = \""+s.DefaultName+"\"\n") {
			t.Errorf("%s: name", s.Role)
		}
		entry := regexp.MustCompile(`(?m)^main = "(.*)"$`).FindStringSubmatch(body)
		if entry == nil {
			t.Fatalf("%s: no main", s.Role)
		}
		if _, err := os.Stat(filepath.Join(worker, filepath.FromSlash(entry[1]))); err != nil {
			t.Errorf("%s: %v", s.Role, err)
		}
		for _, d := range s.ExternalObjects {
			if !strings.Contains(body, "name = \""+d.Binding+"\"\nclass_name = \""+d.ClassName+"\"\nscript_name = \""+cfg.Script(d.ScriptRole).DefaultName+"\"\n") {
				t.Errorf("%s: object %s", s.Role, d.Binding)
			}
		}
		for _, v := range s.Services {
			if !strings.Contains(body, "binding = \""+v.Binding+"\"\nservice = \""+cfg.Script(v.ServiceRole).DefaultName+"\"\n") {
				t.Errorf("%s: service %s", s.Role, v.Binding)
			}
		}
		if (s.Role == RoleFront) == strings.Contains(body, "CompiledWasm") || strings.Contains(body, "[triggers]") != (s.Role == RoleJobs) {
			t.Errorf("%s: rules or triggers", s.Role)
		}
	}
}

// build-bundle.mjs makes exactly the <role>.js files edge.json names, each from the entry the wrangler config names.
func TestBundleScriptBuildsTheScriptsOfEdgeJSON(t *testing.T) {
	src := string(readFile(t, filepath.Join("..", "..", "edge", "worker", "build-bundle.mjs")))
	block := regexp.MustCompile(`(?s)entryPoints: \{(.*?)\},`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("no entryPoints in build-bundle.mjs")
	}
	cfg := NewEdgeConfig("v0.0.0-dev")
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+): "([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		got[m[1]] = m[2]
	}
	if len(got) != len(cfg.Scripts) {
		t.Errorf("build-bundle.mjs builds %v, edge.json has %d scripts", got, len(cfg.Scripts))
	}
	for _, s := range cfg.Scripts {
		if got[s.Role] != wranglerEntry(s.Role) {
			t.Errorf("%s: build-bundle.mjs builds %q, the wrangler config says %q", s.Role, got[s.Role], wranglerEntry(s.Role))
		}
	}
}
