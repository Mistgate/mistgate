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
	"sort"
	"strings"
	"time"
)

// The edge bundle is the Cloudflare edition of the panel: two Worker scripts, the panel as WebAssembly and the admin SPA,
// in one tarball (mistgate-edge.tar.gz) that `mistgate edge deploy` uploads. It has its own manifest and its own signature
// context, so the one release key signs it without a panel or node signature ever being mistaken for it. The format is
// the contract of rounds E4 (producer) and E5 (consumer): a change needs both sides.
const (
	// EdgeBundleName, EdgeManifestName and EdgeSignatureName are the three files of a release that belong to the edge.
	EdgeBundleName    = "mistgate-edge.tar.gz"
	EdgeManifestName  = "edge-manifest.json"
	EdgeSignatureName = "edge-manifest.sig"
	// EdgeConfigName is the description of the scripts inside the tarball.
	EdgeConfigName = "edge.json"
	// EdgeSchema is the only edge.json schema this code writes and understands.
	EdgeSchema = 1
	// MaxEdgeBundle bounds the tarball a consumer will read (the wasm is about 50 MB; Cloudflare allows 64 MiB per script).
	MaxEdgeBundle = 256 << 20
)

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

// EdgeConfig is edge.json, the first entry of the tarball: what a deployer needs to upload the scripts without reading the
// wrangler templates. The templates in edge/worker stay the source of truth (a test compares them with NewEdgeConfig).
type EdgeConfig struct {
	Schema             int          `json:"schema"`
	Version            string       `json:"version"`
	CompatibilityDate  string       `json:"compatibility_date"`
	CompatibilityFlags []string     `json:"compatibility_flags"`
	Scripts            []EdgeScript `json:"scripts"`
}

// EdgeScript is one Worker script of the bundle.
type EdgeScript struct {
	Role            string        `json:"role"` // "provision" or "main"
	DefaultName     string        `json:"default_name"`
	MainModule      string        `json:"main_module"`
	Modules         []EdgeModule  `json:"modules"`
	DurableObjects  []EdgeDO      `json:"durable_objects"`
	ExternalObjects []EdgeDO      `json:"durable_objects_external"`
	Migrations      []EdgeMigrate `json:"migrations"`
	D1              []EdgeBinding `json:"d1"`
	Assets          *EdgeAssets   `json:"assets,omitempty"`
	Secrets         []string      `json:"secrets"`
	Vars            []string      `json:"vars,omitempty"`
	Cron            []string      `json:"cron,omitempty"`
	Routes          bool          `json:"routes"`
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

// EdgeMigrate is one [[migrations]] step.
type EdgeMigrate struct {
	Tag              string   `json:"tag"`
	NewSqliteClasses []string `json:"new_sqlite_classes"`
}

// EdgeBinding is a binding that is only a name (the D1 database it points at is the deployer's).
type EdgeBinding struct {
	Binding string `json:"binding"`
}

// EdgeAssets is the static assets binding of the main script; Directory is a folder of the tarball.
type EdgeAssets struct {
	Directory string `json:"directory"`
	Binding   string `json:"binding"`
}

// NewEdgeConfig is edge.json for a version. The values mirror edge/worker/wrangler.example.toml (main script) and
// wrangler.provision.example.toml (Provisioner); TestEdgeConfigMatchesWranglerTemplates fails when they drift apart.
func NewEdgeConfig(version string) EdgeConfig {
	modules := func(js string) []EdgeModule {
		return []EdgeModule{{Name: js, Type: "esm"}, {Name: "panel.wasm", Type: "compiled_wasm"}}
	}
	return EdgeConfig{
		Schema: EdgeSchema, Version: version, CompatibilityDate: "2026-10-01", CompatibilityFlags: []string{},
		Scripts: []EdgeScript{
			{
				Role: "provision", DefaultName: "mistgate-edge-provision", MainModule: "provision.js", Modules: modules("provision.js"),
				DurableObjects:  []EdgeDO{{Binding: "PROVISIONER", ClassName: "Provisioner"}},
				ExternalObjects: []EdgeDO{{Binding: "LIMITER", ClassName: "Limiter", ScriptRole: "main"}, {Binding: "NODELINK", ClassName: "NodeLink", ScriptRole: "main"}},
				Migrations:      []EdgeMigrate{{Tag: "v1", NewSqliteClasses: []string{"Provisioner"}}},
				D1:              []EdgeBinding{{Binding: "DB"}},
				Secrets:         []string{"MASTER_KEY"},
			},
			{
				Role: "main", DefaultName: "mistgate-edge", MainModule: "worker.js", Modules: modules("worker.js"),
				DurableObjects:  []EdgeDO{{Binding: "LIMITER", ClassName: "Limiter"}, {Binding: "NODELINK", ClassName: "NodeLink"}},
				ExternalObjects: []EdgeDO{{Binding: "PROVISIONER", ClassName: "Provisioner", ScriptRole: "provision"}},
				Migrations: []EdgeMigrate{
					{Tag: "v1", NewSqliteClasses: []string{"Limiter"}},
					{Tag: "v2", NewSqliteClasses: []string{"NodeLink"}},
				},
				D1:      []EdgeBinding{{Binding: "DB"}},
				Assets:  &EdgeAssets{Directory: "assets", Binding: "ASSETS"},
				Secrets: []string{"MASTER_KEY", "ADMIN_PREFIX", "SUB_PREFIX", "SETUP_TOKEN_SHA256"},
				Vars:    []string{"PUBLIC_URL", "ADMIN_HOST"},
				Cron:    []string{"* * * * *"},
				Routes:  true,
			},
		},
	}
}

// Marshal encodes edge.json.
func (c EdgeConfig) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// EdgeInputs are the built parts of a bundle. WorkerJS, ProvisionJS and Wasm are files; AssetsDir is web/dist.
type EdgeInputs struct {
	Version     string
	WorkerJS    string
	ProvisionJS string
	Wasm        string
	AssetsDir   string
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
	if in.WorkerJS == "" || in.ProvisionJS == "" || in.Wasm == "" || in.AssetsDir == "" {
		return errors.New("release: the edge bundle needs worker.js, provision.js, panel.wasm and the assets directory")
	}
	config, err := NewEdgeConfig(in.Version).Marshal()
	if err != nil {
		return err
	}
	entries := []tarEntry{
		{name: EdgeConfigName, data: config},
		{name: "worker.js", path: in.WorkerJS},
		{name: "provision.js", path: in.ProvisionJS},
		{name: "panel.wasm", path: in.Wasm},
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
		b, err := io.ReadAll(io.LimitReader(tr, MaxEdgeBundle))
		if err != nil {
			return nil, err
		}
		files[name] = b
	}
}
