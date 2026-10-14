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
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// The edge bundle is the Cloudflare edition of the panel: six Worker scripts (the FRONT router and five roles of the panel,
// each role with its own WebAssembly) and the admin SPA, in one tarball (mistgate-edge.tar.gz) that `mistgate edge deploy`
// uploads. It has its own manifest and its own signature context, so the one release key signs it without a panel or node
// signature ever being mistaken for it. The format is the contract of rounds E4 (producer) and E5 (consumer): a change needs
// both sides.
const (
	// EdgeBundleName, EdgeManifestName and EdgeSignatureName are the three files of a release that belong to the edge.
	EdgeBundleName    = "mistgate-edge.tar.gz"
	EdgeManifestName  = "edge-manifest.json"
	EdgeSignatureName = "edge-manifest.sig"
	// EdgeConfigName is the description of the scripts inside the tarball.
	EdgeConfigName = "edge.json"
	// EdgeSchema is the only edge.json schema this code writes and understands. Schema 1 was the single Worker plus the
	// Provisioner; no install of it exists, so nothing reads it any more (ParseEdgeConfig says so).
	EdgeSchema = 2
	// MaxEdgeBundle bounds the tarball a consumer will read (the wasm is about 50 MB; Cloudflare allows 64 MiB per script).
	MaxEdgeBundle = 256 << 20
)

// maxEdgeUnpacked bounds the sum of all file sizes in a tarball that ReadEdgeBundle returns. MaxEdgeBundle bounds the
// compressed bytes, which a gzip bomb stays far below. A variable so that a test can lower it.
var maxEdgeUnpacked int64 = 256 << 20

// edgeContext is the Ed25519ctx context of an edge manifest, next to panelContext: the same key, another context.
var edgeContext = &ed25519.Options{Context: "mistgate edge manifest v1"}

// EdgeManifest is what the owner signs for the edge bundle. Built and Expires follow the panel manifest's conventions.
type EdgeManifest struct {
	Version string `json:"version"`
	Built   int64  `json:"built"`   // Unix time of the source commit
	Expires int64  `json:"expires"` // Unix seconds; bounds how long a captured manifest stays usable
	File    string `json:"file"`    // always EdgeBundleName
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"` // lowercase hex of the tarball
}

// Validate checks the manifest is well formed. It says nothing about time (see Check).
func (m *EdgeManifest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: edge manifest: %s", ErrBadManifest, fmt.Sprintf(format, a...))
	}
	switch {
	case !versionRe.MatchString(m.Version):
		return bad("version %q", m.Version)
	case m.Built <= 0:
		return bad("built must be a positive Unix time")
	case m.Expires <= m.Built:
		return bad("expires must be after built")
	case m.File != EdgeBundleName:
		return bad("file %q (want %s)", m.File, EdgeBundleName)
	case m.Size <= 0 || m.Size > MaxEdgeBundle:
		return bad("size %d", m.Size)
	case !hexRe.MatchString(m.SHA256):
		return bad("sha256")
	}
	return nil
}

// Marshal validates and encodes the manifest: the bytes that get signed and written to edge-manifest.json.
func (m *EdgeManifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseEdge decodes and validates edge manifest bytes. It is strict (an unknown field or trailing data is ErrBadManifest).
// Verify the signature first (or use VerifyEdge): ParseEdge alone trusts nothing.
func ParseEdge(b []byte) (*EdgeManifest, error) {
	if len(b) == 0 || len(b) > MaxManifestBytes {
		return nil, fmt.Errorf("%w: edge manifest: %d bytes", ErrBadManifest, len(b))
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m EdgeManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: edge manifest: %v", ErrBadManifest, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: edge manifest: trailing data", ErrBadManifest)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// SignEdge returns the detached signature of an edge manifest.
func SignEdge(priv ed25519.PrivateKey, manifest []byte) []byte {
	sig, err := priv.Sign(nil, manifest, edgeContext)
	if err != nil {
		panic(err) // only for invalid options, which edgeContext is not
	}
	return sig
}

// VerifyEdge checks the signature over the exact manifest bytes and then parses them. A panel or node signature of the
// same bytes does not verify here, and an edge signature does not verify as theirs.
func VerifyEdge(pub ed25519.PublicKey, manifest, sig []byte) (*EdgeManifest, error) {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || ed25519.VerifyWithOptions(pub, manifest, sig, edgeContext) != nil {
		return nil, ErrBadSignature
	}
	return ParseEdge(manifest)
}

// Check is ErrExpired once the manifest's lifetime is over. Ordering by Built is the caller's business: a deploy of an older
// edge is an operator's choice, not an attack on a node.
func (m *EdgeManifest) Check(now time.Time) error {
	if now.Unix() > m.Expires {
		return ErrExpired
	}
	return nil
}

// VerifyFile reads r (the tarball) to the end and compares size and SHA-256 with the manifest.
func (m *EdgeManifest) VerifyFile(r io.Reader) error {
	return VerifyFile(File{Name: m.File, Size: m.Size, SHA256: m.SHA256}, r)
}

// HashFile returns the size and the lowercase hex SHA-256 of a file.
func HashFile(path string) (int64, string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// EdgeManifestFor hashes the tarball at path and builds its manifest.
func EdgeManifestFor(path, version string, built, expires int64) (*EdgeManifest, error) {
	size, sum, err := HashFile(path)
	if err != nil {
		return nil, err
	}
	return &EdgeManifest{Version: version, Built: built, Expires: expires, File: EdgeBundleName, Size: size, SHA256: sum}, nil
}

// The roles of the scripts of a bundle. FRONT is the router and owns the Limiter and NodeLink objects; the five others are
// the panel built in a role (cmd/mistgate-edge-<role>) with the TypeScript shell around it (edge/worker/src/roles).
const (
	RoleFront     = "front"
	RoleLink      = "link"
	RolePublic    = "public"
	RoleAdmin     = "admin"
	RoleJobs      = "jobs"
	RoleProvision = "provision"
)

// EdgeRoles are all roles of a schema 2 bundle, in the order a deployer uploads them: what a script binds is uploaded before
// the script that binds it, and FRONT, the one entry from outside, is last (design/cloudflare-edition, "Deploy order").
var EdgeRoles = []string{RoleLink, RoleProvision, RolePublic, RoleAdmin, RoleJobs, RoleFront}

// EdgeScriptName is the name of the Worker of a role under the deployer's base name: FRONT keeps the base name (the domain,
// the Durable Object data and the agents' address belong to it), a role is `<name>-<role>`.
func EdgeScriptName(name, role string) string {
	if role == RoleFront {
		return name
	}
	return name + "-" + role
}

// EdgeConfig is edge.json, the first entry of the tarball: what a deployer needs to upload the scripts without reading the
// wrangler configs. Scripts is in EdgeRoles order. The wrangler dev configs are generated from NewEdgeConfig, so this is the
// one description of the bindings.
type EdgeConfig struct {
	Schema             int          `json:"schema"`
	Version            string       `json:"version"`
	CompatibilityDate  string       `json:"compatibility_date"`
	CompatibilityFlags []string     `json:"compatibility_flags"`
	Scripts            []EdgeScript `json:"scripts"`
}

// EdgeScript is one Worker script of the bundle.
type EdgeScript struct {
	Role            string        `json:"role"` // one of EdgeRoles
	DefaultName     string        `json:"default_name"`
	MainModule      string        `json:"main_module"`
	Modules         []EdgeModule  `json:"modules"`
	DurableObjects  []EdgeDO      `json:"durable_objects,omitempty"`          // classes this script owns
	ExternalObjects []EdgeDO      `json:"durable_objects_external,omitempty"` // classes of another script (script_role)
	Migrations      []EdgeMigrate `json:"migrations,omitempty"`
	D1              []EdgeBinding `json:"d1"`
	Services        []EdgeService `json:"services,omitempty"`
	Assets          *EdgeAssets   `json:"assets,omitempty"`
	Secrets         []string      `json:"secrets,omitempty"`
	Vars            []string      `json:"vars,omitempty"`
	Cron            []string      `json:"cron,omitempty"`
	Routes          bool          `json:"routes"` // takes the routes or the custom domain of the install
}

// EdgeModule is a file of the tarball uploaded as a module of the script. Type is "esm" or "compiled_wasm".
type EdgeModule struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// EdgeDO is a Durable Object binding. ScriptRole is set on an object that belongs to the other script.
type EdgeDO struct {
	Binding    string `json:"binding"`
	ClassName  string `json:"class_name"`
	ScriptRole string `json:"script_role,omitempty"`
}

// EdgeService is a service binding to the Worker of another role; Entrypoint names a WorkerEntrypoint class of it.
type EdgeService struct {
	Binding     string `json:"binding"`
	ServiceRole string `json:"service_role"`
	Entrypoint  string `json:"entrypoint,omitempty"`
}

// EdgeMigrate is one [[migrations]] step.
type EdgeMigrate struct {
	Tag              string   `json:"tag"`
	NewSqliteClasses []string `json:"new_sqlite_classes"`
}

// EdgeBinding is a binding that is only a name (the D1 database it points at is the deployer's).
type EdgeBinding struct {
	Binding string `json:"binding"`
}

// EdgeAssets is the static assets binding of a script; Directory is a folder of the tarball. The public and the admin role
// point at the same folder.
type EdgeAssets struct {
	Directory string `json:"directory"`
	Binding   string `json:"binding"`
}

// NewEdgeConfig is edge.json for a version. TestEdgeConfigBindings holds it to the TypeScript side: the bindings each script
// reads (edge/worker/src/env.ts, roles/*.ts, front.ts).
func NewEdgeConfig(version string) EdgeConfig {
	d1 := []EdgeBinding{{Binding: "DB"}}
	masterKey := []string{"MASTER_KEY"}
	front := func(binding, class string) EdgeDO {
		return EdgeDO{Binding: binding, ClassName: class, ScriptRole: RoleFront}
	}
	provisioner := EdgeDO{Binding: "PROVISIONER", ClassName: "Provisioner", ScriptRole: RoleProvision}
	script := func(role string, wasm bool) EdgeScript {
		s := EdgeScript{Role: role, DefaultName: EdgeScriptName("mistgate-edge", role), MainModule: role + ".js", D1: d1}
		s.Modules = []EdgeModule{{Name: role + ".js", Type: "esm"}}
		if wasm {
			s.Modules = append(s.Modules, EdgeModule{Name: role + ".wasm", Type: "compiled_wasm"})
		}
		return s
	}
	assets := &EdgeAssets{Directory: "assets", Binding: "ASSETS"}

	link := script(RoleLink, true)
	link.ExternalObjects = []EdgeDO{front("LIMITER", "Limiter"), front("NODELINK", "NodeLink")}
	link.Secrets = masterKey

	provision := script(RoleProvision, true)
	provision.DurableObjects = []EdgeDO{{Binding: "PROVISIONER", ClassName: "Provisioner"}}
	provision.ExternalObjects = []EdgeDO{front("LIMITER", "Limiter"), front("NODELINK", "NodeLink")}
	provision.Migrations = []EdgeMigrate{{Tag: "v1", NewSqliteClasses: []string{"Provisioner"}}}
	provision.Secrets = masterKey

	public := script(RolePublic, true)
	public.ExternalObjects = []EdgeDO{front("LIMITER", "Limiter"), front("NODELINK", "NodeLink")}
	public.Assets = assets
	public.Secrets = masterKey

	admin := script(RoleAdmin, true)
	admin.ExternalObjects = []EdgeDO{front("LIMITER", "Limiter"), front("NODELINK", "NodeLink"), provisioner}
	admin.Assets = assets
	// The prefixes are secrets: they are the secret paths of the panel. The first-run settings count only while the database
	// is empty, and only this role creates the instance.
	admin.Secrets = []string{"MASTER_KEY", "ADMIN_PREFIX", "SUB_PREFIX", "SETUP_TOKEN_SHA256"}
	admin.Vars = []string{"PUBLIC_URL", "ADMIN_HOST"}

	jobs := script(RoleJobs, true)
	jobs.ExternalObjects = []EdgeDO{front("NODELINK", "NodeLink"), provisioner}
	jobs.Secrets = masterKey
	jobs.Cron = []string{"* * * * *"}

	fr := script(RoleFront, false)
	fr.DurableObjects = []EdgeDO{{Binding: "LIMITER", ClassName: "Limiter"}, {Binding: "NODELINK", ClassName: "NodeLink"}}
	fr.Migrations = []EdgeMigrate{{Tag: "v1", NewSqliteClasses: []string{"Limiter"}}, {Tag: "v2", NewSqliteClasses: []string{"NodeLink"}}}
	fr.Services = []EdgeService{
		{Binding: "PUBLIC", ServiceRole: RolePublic}, {Binding: "ADMIN", ServiceRole: RoleAdmin}, {Binding: "LINK", ServiceRole: RoleLink},
		{Binding: "PANEL_LINK", ServiceRole: RoleLink, Entrypoint: "PanelLink"},
	}
	fr.Routes = true

	return EdgeConfig{
		Schema: EdgeSchema, Version: version, CompatibilityDate: "2026-10-01", CompatibilityFlags: []string{},
		Scripts: []EdgeScript{link, provision, public, admin, jobs, fr},
	}
}

// Script returns the script of a role, or nil.
func (c EdgeConfig) Script(role string) *EdgeScript {
	for i := range c.Scripts {
		if c.Scripts[i].Role == role {
			return &c.Scripts[i]
		}
	}
	return nil
}

// Validate checks that edge.json describes a bundle that can be deployed: every role once and in upload order, no binding to
// a role or a class that does not exist, no two bindings of one name in a script, a migration for every class a script owns
// and for nothing else, and modules named after the role. It checks the references, not the values (the tests hold
// NewEdgeConfig to those).
func (c EdgeConfig) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("release: edge.json: %s", fmt.Sprintf(format, a...))
	}
	if c.Schema != EdgeSchema {
		return bad("schema %d, want %d", c.Schema, EdgeSchema)
	}
	if len(c.Scripts) != len(EdgeRoles) {
		return bad("%d scripts, want %d (%s)", len(c.Scripts), len(EdgeRoles), strings.Join(EdgeRoles, ", "))
	}
	for i, s := range c.Scripts {
		if s.Role != EdgeRoles[i] {
			return bad("script %d is role %q, want %q (the order is the upload order)", i, s.Role, EdgeRoles[i])
		}
	}
	for _, s := range c.Scripts {
		seen := map[string]bool{}
		binding := func(kind, name string) error {
			if name == "" || seen[name] {
				return bad("%s: %s binding %q is empty or used twice", s.Role, kind, name)
			}
			seen[name] = true
			return nil
		}
		wantModules := []EdgeModule{{Name: s.Role + ".js", Type: "esm"}}
		if s.Role != RoleFront {
			wantModules = append(wantModules, EdgeModule{Name: s.Role + ".wasm", Type: "compiled_wasm"})
		}
		if s.MainModule != s.Role+".js" || !reflect.DeepEqual(s.Modules, wantModules) {
			return bad("%s: modules %v, main %q", s.Role, s.Modules, s.MainModule)
		}
		if s.DefaultName != EdgeScriptName("mistgate-edge", s.Role) {
			return bad("%s: default_name %q", s.Role, s.DefaultName)
		}
		owned, created := map[string]bool{}, map[string]bool{}
		for _, d := range s.DurableObjects {
			if err := binding("durable object", d.Binding); err != nil {
				return err
			}
			if d.ScriptRole != "" || d.ClassName == "" {
				return bad("%s: own object %s names a script or no class", s.Role, d.Binding)
			}
			owned[d.ClassName] = true
		}
		for _, m := range s.Migrations {
			for _, class := range m.NewSqliteClasses {
				created[class] = true
			}
		}
		if !reflect.DeepEqual(owned, created) {
			return bad("%s: classes %v and migrations %v do not match", s.Role, owned, created)
		}
		for _, d := range s.ExternalObjects {
			if err := binding("durable object", d.Binding); err != nil {
				return err
			}
			target := c.Script(d.ScriptRole)
			if target == nil || target.Role == s.Role {
				return bad("%s: object %s is in role %q, which is not another script", s.Role, d.Binding, d.ScriptRole)
			}
			found := false
			for _, o := range target.DurableObjects {
				found = found || o.ClassName == d.ClassName
			}
			if !found {
				return bad("%s: object %s is class %s, which role %s does not own", s.Role, d.Binding, d.ClassName, d.ScriptRole)
			}
		}
		for _, b := range s.D1 {
			if err := binding("d1", b.Binding); err != nil {
				return err
			}
		}
		for _, sv := range s.Services {
			if err := binding("service", sv.Binding); err != nil {
				return err
			}
			if sv.ServiceRole == s.Role || c.Script(sv.ServiceRole) == nil {
				return bad("%s: service %s is role %q, which is not another script", s.Role, sv.Binding, sv.ServiceRole)
			}
		}
		if s.Assets != nil {
			if err := binding("assets", s.Assets.Binding); err != nil {
				return err
			}
			if s.Assets.Directory != "assets" {
				return bad("%s: assets directory %q", s.Role, s.Assets.Directory)
			}
		}
		for _, n := range append(append([]string{}, s.Secrets...), s.Vars...) {
			if err := binding("secret or var", n); err != nil {
				return err
			}
		}
	}
	return nil
}

// ErrEdgeSchema is a bundle whose edge.json this code does not read.
var ErrEdgeSchema = errors.New("release: unsupported edge.json schema")

// ParseEdgeConfig decodes edge.json strictly and validates it. A schema other than EdgeSchema is ErrEdgeSchema with a message
// that says what to do: schema 1 is the single-Worker bundle, which no install runs on and nothing reads any more.
func ParseEdgeConfig(b []byte) (*EdgeConfig, error) {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, fmt.Errorf("release: edge.json: %w", err)
	}
	if head.Schema != EdgeSchema {
		if head.Schema == 1 {
			return nil, fmt.Errorf("%w 1: this bundle is the single-Worker edition, which this mistgate no longer deploys (it deploys schema %d, six Workers); use a mistgate release that matches the bundle", ErrEdgeSchema, EdgeSchema)
		}
		return nil, fmt.Errorf("%w %d (this mistgate reads schema %d)", ErrEdgeSchema, head.Schema, EdgeSchema)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c EdgeConfig
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("release: edge.json: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("release: edge.json: trailing data")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// CheckFiles compares the files ReadEdgeBundle returned with the config: every module of every script is there and not empty,
// and nothing else is but edge.json and the assets (a file the config does not name is never uploaded, so it must not be
// signed in).
func (c EdgeConfig) CheckFiles(files map[string][]byte) error {
	want := map[string]bool{EdgeConfigName: true}
	for _, s := range c.Scripts {
		for _, m := range s.Modules {
			if len(files[m.Name]) == 0 {
				return fmt.Errorf("release: the edge bundle is missing %s (script %s)", m.Name, s.Role)
			}
			want[m.Name] = true
		}
	}
	for name := range files {
		if !want[name] && !strings.HasPrefix(name, "assets/") {
			return fmt.Errorf("release: the edge bundle holds %s, which edge.json does not name", name)
		}
	}
	if len(files["assets/index.html"]) == 0 {
		return errors.New("release: the edge bundle has no assets/index.html")
	}
	return nil
}

// Marshal encodes edge.json.
func (c EdgeConfig) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// EdgeInputs are the built parts of a bundle. ModulesDir holds every module file the config names (front.js, link.js,
// link.wasm ... directly in it); AssetsDir is web/dist.
type EdgeInputs struct {
	Version    string
	ModulesDir string
	AssetsDir  string
}

type tarEntry struct {
	name string // slash-separated path inside the tarball, no trailing slash
	dir  bool
	path string // file on disk, or "" with data
	data []byte
}

// WriteEdgeBundle writes the deterministic tarball: entries sorted by path, mode 0644 (directories 0755), uid and gid 0 with
// no names, mtime 0, gzip with no name and mtime 0. The same inputs give the same bytes on every machine and run.
func WriteEdgeBundle(w io.Writer, in EdgeInputs) error {
	if !versionRe.MatchString(in.Version) {
		return fmt.Errorf("%w: version %q", ErrBadManifest, in.Version)
	}
	if in.ModulesDir == "" || in.AssetsDir == "" {
		return errors.New("release: the edge bundle needs the directory of the script modules and the assets directory")
	}
	cfg := NewEdgeConfig(in.Version)
	config, err := cfg.Marshal()
	if err != nil {
		return err
	}
	entries := []tarEntry{{name: EdgeConfigName, data: config}}
	for _, script := range cfg.Scripts {
		for _, m := range script.Modules {
			entries = append(entries, tarEntry{name: m.Name, path: filepath.Join(in.ModulesDir, m.Name)})
		}
	}
	assets, err := assetEntries(in.AssetsDir)
	if err != nil {
		return err
	}
	entries = append(entries, assets...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if e.dir {
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, e.name+"/", 0o755
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("tar %s: %w", e.name, err)
			}
			continue
		}
		hdr.Typeflag, hdr.Mode = tar.TypeReg, 0o644
		var src io.Reader = bytes.NewReader(e.data)
		var closer io.Closer
		if e.path != "" {
			fh, err := os.Open(e.path)
			if err != nil {
				return err
			}
			fi, err := fh.Stat()
			if err != nil {
				fh.Close()
				return err
			}
			hdr.Size, src, closer = fi.Size(), fh, fh
		} else {
			hdr.Size = int64(len(e.data))
		}
		err := tw.WriteHeader(hdr)
		if err == nil {
			_, err = io.Copy(tw, src)
		}
		if closer != nil {
			closer.Close()
		}
		if err != nil {
			return fmt.Errorf("tar %s: %w", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// assetEntries lists dir (the built SPA) under assets/. A symbolic link or another special file is refused: the bundle holds
// regular files only. .gitkeep, the placeholder that keeps web/dist in a checkout, is left out.
func assetEntries(dir string) ([]tarEntry, error) {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, fmt.Errorf("%s has no index.html: build the SPA first (cd web && pnpm install --frozen-lockfile && pnpm build)", dir)
	}
	out := []tarEntry{{name: "assets", dir: true}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		name := "assets/" + filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			out = append(out, tarEntry{name: name, dir: true})
		case d.Type().IsRegular():
			if d.Name() != ".gitkeep" {
				out = append(out, tarEntry{name: name, path: p})
			}
		default:
			return fmt.Errorf("%s is not a regular file", p)
		}
		return nil
	})
	return out, err
}

// ReadEdgeBundle returns the regular files of a tarball by path (directories are skipped), refusing a path that is absolute,
// has "..", or is listed twice. It is for tests and for a consumer that has verified the manifest.
func ReadEdgeBundle(r io.Reader) (map[string][]byte, error) {
	zr, err := gzip.NewReader(io.LimitReader(r, MaxEdgeBundle))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	files := map[string][]byte{}
	remaining := maxEdgeUnpacked
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("release: unsafe path %q in the edge bundle", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("release: %q in the edge bundle is not a regular file", hdr.Name)
		}
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("release: %q is listed twice in the edge bundle", name)
		}
		b, err := io.ReadAll(io.LimitReader(tr, remaining+1))
		if err != nil {
			return nil, err
		}
		if remaining -= int64(len(b)); remaining < 0 {
			return nil, fmt.Errorf("release: the edge bundle unpacks to more than %d bytes", maxEdgeUnpacked)
		}
		files[name] = b
	}
}
