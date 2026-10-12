package edgedeploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

const testCFToken = "cf-token-never-print-this"

var testNow = time.Unix(1_800_000_000, 0)

const apiPrefix = "/client/v4"
const acct = "/accounts/acct-id"

// fakeCloudflare implements the parts of Cloudflare's API that a deploy uses, as the documentation describes them
// (developers.cloudflare.com/api and /workers/static-assets/direct-upload). Anything it does not know is a test failure,
// and so is a request body that breaks the documented shape.
type fakeCloudflare struct {
	t         *testing.T
	mu        sync.Mutex
	statePath string

	requests    []string
	scripts     map[string]string // script name -> migration tag ("" = none)
	databases   []d1Database
	zoneStatus  string // "" = no zone
	rejectToken bool
	failScript  string
	echoToken   bool
	decoyStatus int

	metadatas    map[string][]string // script name -> raw metadata of every PUT
	putJWTs      map[string][]string // script name -> assets.jwt of every PUT
	sessions     map[string]*fakeSession
	completion   map[string]string // completion token -> script it was issued for
	uploaded     map[string]bool
	assetUploads int
	dbLocation   string
	seq          int
}

type fakeSession struct {
	script  string
	pending map[string]string // hash -> manifest path
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	t.Helper()
	return &fakeCloudflare{
		t: t, scripts: make(map[string]string), zoneStatus: "active", decoyStatus: http.StatusOK,
		metadatas: make(map[string][]string), putJWTs: make(map[string][]string),
		sessions: make(map[string]*fakeSession), completion: make(map[string]string), uploaded: make(map[string]bool),
		// The API's name filter is not exact: a database that merely contains the name comes back too.
		databases: []d1Database{{UUID: "other-id", Name: "mistgate-edge-staging"}},
	}
}

func (f *fakeCloudflare) fail(w http.ResponseWriter, format string, args ...any) {
	f.t.Helper()
	msg := fmt.Sprintf(format, args...)
	f.t.Errorf("fake Cloudflare: %s", msg)
	writeCFError(w, http.StatusBadRequest, msg)
}

func (f *fakeCloudflare) addDatabase(name string) {
	f.databases = append(f.databases, d1Database{UUID: "d1-id", Name: name})
}

func (f *fakeCloudflare) ours() (d1Database, bool) {
	for _, db := range f.databases {
		if db.Name == "mistgate-edge" {
			return db, true
		}
	}
	return d1Database{}, false
}

func decodeStrict(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	if r.URL.Path == "/" {
		w.WriteHeader(f.decoyStatus)
		_, _ = io.WriteString(w, "decoy")
		return
	}
	if !strings.HasPrefix(r.URL.Path, apiPrefix+"/") {
		f.t.Errorf("unexpected request path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if path == acct+"/workers/assets/upload" {
		f.uploadAssets(w, r)
		return
	}
	if f.rejectToken || r.Header.Get("Authorization") != "Bearer "+testCFToken {
		writeCFError(w, http.StatusUnauthorized, "Authentication error")
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "/accounts":
		writeCFResult(w, http.StatusOK, []account{{ID: "acct-id"}})
	case r.Method == http.MethodGet && path == "/zones":
		name := r.URL.Query().Get("name")
		if f.zoneStatus != "" && name == "example.com" {
			writeCFResult(w, http.StatusOK, []zone{{ID: "zone-id", Name: "example.com", Status: f.zoneStatus}})
		} else {
			writeCFResult(w, http.StatusOK, []zone{})
		}
	case r.Method == http.MethodGet && path == acct+"/d1/database":
		perPage, perErr := strconv.Atoi(r.URL.Query().Get("per_page"))
		if r.URL.Query().Get("page") == "" || perErr != nil || perPage < 10 {
			f.fail(w, "D1 list needs page and per_page (minimum 10)")
			return
		}
		name := r.URL.Query().Get("name")
		var out []d1Database
		for _, db := range f.databases {
			if strings.Contains(db.Name, name) {
				out = append(out, db)
			}
		}
		writeCFResult(w, http.StatusOK, out)
	case r.Method == http.MethodPost && path == acct+"/d1/database":
		f.createDatabase(w, r)
	case r.Method == http.MethodGet && path == acct+"/workers/scripts":
		type item struct {
			ID           string `json:"id"`
			MigrationTag string `json:"migration_tag"`
		}
		list := []item{}
		for name, tag := range f.scripts {
			list = append(list, item{name, tag})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		writeCFResult(w, http.StatusOK, list)
	case r.Method == http.MethodGet && strings.HasPrefix(path, acct+"/workers/scripts/") && strings.Count(path, "/") == 5:
		// The documented answer is the script's raw content, not a JSON envelope.
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = io.WriteString(w, "export default { fetch() {} }")
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/assets-upload-session"):
		f.assetSession(w, r, strings.TrimSuffix(strings.TrimPrefix(path, acct+"/workers/scripts/"), "/assets-upload-session"))
	case r.Method == http.MethodPut && strings.HasPrefix(path, acct+"/workers/scripts/") && strings.Count(path, "/") == 5:
		f.putScript(w, r, strings.TrimPrefix(path, acct+"/workers/scripts/"))
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/schedules"):
		var body []struct {
			Cron string `json:"cron"`
		}
		if err := decodeStrict(r.Body, &body); err != nil || len(body) == 0 || body[0].Cron == "" {
			f.fail(w, "schedules body must be a JSON array of {cron}: %v", err)
			return
		}
		writeCFResult(w, http.StatusOK, map[string]any{"schedules": body})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/subdomain"):
		var body struct {
			Enabled         *bool `json:"enabled"`
			PreviewsEnabled *bool `json:"previews_enabled"`
		}
		name := strings.TrimSuffix(strings.TrimPrefix(path, acct+"/workers/scripts/"), "/subdomain")
		if _, ok := f.scripts[name]; !ok {
			f.fail(w, "subdomain of unknown script %s", name)
			return
		}
		if err := decodeStrict(r.Body, &body); err != nil || body.Enabled == nil || *body.Enabled || (body.PreviewsEnabled != nil && *body.PreviewsEnabled) {
			f.fail(w, "subdomain body must disable workers.dev and previews: %v", err)
			return
		}
		writeCFResult(w, http.StatusOK, map[string]any{"enabled": false, "previews_enabled": false})
	case r.Method == http.MethodPut && path == acct+"/workers/domains":
		var body struct {
			Hostname string `json:"hostname"`
			Service  string `json:"service"`
			ZoneID   string `json:"zone_id"`
		}
		if err := decodeStrict(r.Body, &body); err != nil || body.Hostname != "panel.example.com" || body.ZoneID != "zone-id" {
			f.fail(w, "bad custom domain body %+v: %v", body, err)
			return
		}
		if _, ok := f.scripts[body.Service]; !ok {
			f.fail(w, "custom domain for unknown service %s", body.Service)
			return
		}
		writeCFResult(w, http.StatusOK, map[string]any{"id": "domain-id", "hostname": body.Hostname, "service": body.Service})
	default:
		f.t.Errorf("unexpected Cloudflare request: %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

func (f *fakeCloudflare) createDatabase(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name                string `json:"name"`
		PrimaryLocationHint string `json:"primary_location_hint"`
	}
	if err := decodeStrict(r.Body, &body); err != nil || body.Name == "" {
		f.fail(w, "D1 create body takes name and primary_location_hint: %v", err)
		return
	}
	if !d1Locations[body.PrimaryLocationHint] {
		f.fail(w, "primary_location_hint %q", body.PrimaryLocationHint)
		return
	}
	if f.statePath != "" {
		state, ok, err := loadExistingState(f.statePath)
		if err != nil || !ok || state.DatabaseID != "" || state.MasterKey == "" {
			f.fail(w, "the key file must exist, with no database id yet, before the D1 database is created: ok=%v err=%v", ok, err)
			return
		}
	}
	f.dbLocation = body.PrimaryLocationHint
	f.databases = append(f.databases, d1Database{UUID: "d1-id", Name: body.Name})
	writeCFResult(w, http.StatusOK, map[string]any{"uuid": "d1-id", "name": body.Name, "version": "production"})
}

func (f *fakeCloudflare) assetSession(w http.ResponseWriter, r *http.Request, script string) {
	var in struct {
		Manifest map[string]struct {
			Hash string `json:"hash"`
			Size int    `json:"size"`
		} `json:"manifest"`
	}
	if err := decodeStrict(r.Body, &in); err != nil || len(in.Manifest) == 0 {
		f.fail(w, "assets manifest takes only hash and size: %v", err)
		return
	}
	session := &fakeSession{script: script, pending: make(map[string]string)}
	var buckets [][]string
	for path, entry := range in.Manifest {
		if !strings.HasPrefix(path, "/") || len(entry.Hash) != 32 {
			f.fail(w, "bad manifest entry %s %+v", path, entry)
			return
		}
		if !f.uploaded[entry.Hash] {
			session.pending[entry.Hash] = path
		}
	}
	hashes := make([]string, 0, len(session.pending))
	for hash := range session.pending {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	for _, hash := range hashes {
		buckets = append(buckets, []string{hash}) // one file per bucket, to exercise several uploads
	}
	f.seq++
	if len(buckets) == 0 {
		jwt := fmt.Sprintf("completion-%d", f.seq) // nothing to upload: the session token is the completion token
		f.completion[jwt] = script
		writeCFResult(w, http.StatusOK, map[string]any{"jwt": jwt, "buckets": [][]string{}})
		return
	}
	jwt := fmt.Sprintf("session-%d", f.seq)
	f.sessions[jwt] = session
	writeCFResult(w, http.StatusOK, map[string]any{"jwt": jwt, "buckets": buckets})
}

func (f *fakeCloudflare) uploadAssets(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth == "Bearer "+testCFToken {
		f.fail(w, "the asset upload must carry the session token, not the API token")
		return
	}
	session := f.sessions[strings.TrimPrefix(auth, "Bearer ")]
	if session == nil {
		f.t.Errorf("asset upload without a session token: %q", auth)
		writeCFError(w, http.StatusUnauthorized, "bad upload token")
		return
	}
	if r.Header.Get("CF-Worker-Assets-Token") != "" || r.URL.Query().Get("base64") != "true" {
		f.fail(w, "asset upload takes only the bearer session token and ?base64=true")
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		f.fail(w, "asset multipart: %v", err)
		return
	}
	if len(r.MultipartForm.Value) != 0 || len(r.MultipartForm.File) == 0 {
		f.fail(w, "asset upload carries one file part per hash and nothing else")
		return
	}
	for hash, files := range r.MultipartForm.File {
		path, ok := session.pending[hash]
		if !ok {
			f.fail(w, "uploaded a hash that was not asked for: %s", hash)
			return
		}
		if files[0].Header.Get("Content-Type") == "" {
			f.fail(w, "asset part %s has no content type", hash)
			return
		}
		fh, _ := files[0].Open()
		content, _ := io.ReadAll(fh)
		fh.Close()
		sum := sha256.Sum256([]byte(string(content) + strings.TrimPrefix(filepath.Ext(path), ".")))
		if hex.EncodeToString(sum[:])[:32] != hash {
			f.fail(w, "hash of %s does not follow the documented formula", path)
			return
		}
		delete(session.pending, hash)
		f.uploaded[hash] = true
	}
	f.assetUploads++
	if len(session.pending) != 0 {
		writeCFResult(w, http.StatusAccepted, map[string]any{})
		return
	}
	f.seq++
	jwt := fmt.Sprintf("completion-%d", f.seq)
	f.completion[jwt] = session.script
	writeCFResult(w, http.StatusCreated, map[string]any{"jwt": jwt})
}

func (f *fakeCloudflare) putScript(w http.ResponseWriter, r *http.Request, name string) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		f.fail(w, "script multipart: %v", err)
		return
	}
	raw := r.FormValue("metadata")
	var meta struct {
		MainModule         string           `json:"main_module"`
		CompatibilityDate  string           `json:"compatibility_date"`
		CompatibilityFlags []string         `json:"compatibility_flags"`
		Bindings           []map[string]any `json:"bindings"`
		Migrations         json.RawMessage  `json:"migrations"`
		Assets             *struct {
			JWT    string         `json:"jwt"`
			Config map[string]any `json:"config"`
		} `json:"assets"`
		Observability map[string]any `json:"observability"`
	}
	if err := decodeStrict(strings.NewReader(raw), &meta); err != nil {
		f.fail(w, "script metadata: %v", err)
		return
	}
	// A missing module is an error; so is an extra one.
	parts := r.MultipartForm.File
	for _, module := range []string{meta.MainModule, "panel.wasm"} {
		files := parts[module]
		if len(files) == 0 {
			f.fail(w, "script %s: module %s is not in the upload", name, module)
			return
		}
		want := "application/javascript+module"
		if module == "panel.wasm" {
			want = "application/wasm"
		}
		if got := files[0].Header.Get("Content-Type"); got != want {
			f.fail(w, "module %s content type %q, want %q", module, got, want)
			return
		}
	}
	if len(parts) != 2 {
		f.fail(w, "script %s: unexpected module parts", name)
		return
	}
	hasAssets := false
	seen := map[string]bool{}
	for _, b := range meta.Bindings {
		bname, _ := b["name"].(string)
		if bname == "" || seen[bname] {
			f.fail(w, "binding name %q empty or repeated", bname)
			return
		}
		seen[bname] = true
		switch b["type"] {
		case "d1":
			if _, old := b["id"]; old || b["database_id"] == nil {
				f.fail(w, "a d1 binding is {name, database_id}: %v", b)
				return
			}
			if db, ok := f.ours(); !ok || b["database_id"] != db.UUID {
				f.fail(w, "d1 binding points at %v, which does not exist", b["database_id"])
				return
			}
		case "durable_object_namespace":
			if target, ok := b["script_name"].(string); ok {
				if _, exists := f.scripts[target]; !exists || target == name {
					f.fail(w, "script %s binds %s of script %q, which does not exist yet", name, bname, target)
					return
				}
			}
		case "secret_text", "plain_text":
			if text, _ := b["text"].(string); text == "" {
				f.fail(w, "binding %s has an empty text", bname)
				return
			}
		case "assets":
			hasAssets = true
		default:
			f.fail(w, "unknown binding type %v", b["type"])
			return
		}
	}
	if f.statePath != "" {
		state, ok, err := loadExistingState(f.statePath)
		if err != nil || !ok || state.DatabaseID != "d1-id" {
			f.fail(w, "the key file must hold the database id before any script is uploaded: ok=%v err=%v", ok, err)
			return
		}
		for _, b := range meta.Bindings {
			if b["name"] == "MASTER_KEY" && b["text"] != state.MasterKey {
				f.fail(w, "the uploaded MASTER_KEY is not the one in the key file")
				return
			}
		}
	}
	if hasAssets {
		if meta.Assets == nil || f.completion[meta.Assets.JWT] != name {
			f.fail(w, "assets.jwt %+v is not a completion token issued for %s", meta.Assets, name)
			return
		}
		if !reflect.DeepEqual(meta.Assets.Config, map[string]any{"run_worker_first": true, "html_handling": "none", "not_found_handling": "none"}) {
			f.fail(w, "assets.config %v", meta.Assets.Config)
			return
		}
	} else if meta.Assets != nil {
		f.fail(w, "assets on a script with no assets binding")
		return
	}
	cur := f.scripts[name]
	newTag := cur
	if len(meta.Migrations) != 0 {
		var mig struct {
			OldTag           string   `json:"old_tag"`
			NewTag           string   `json:"new_tag"`
			NewSqliteClasses []string `json:"new_sqlite_classes"`
			Steps            []struct {
				NewSqliteClasses []string `json:"new_sqlite_classes"`
			} `json:"steps"`
		}
		if err := decodeStrict(bytes.NewReader(meta.Migrations), &mig); err != nil {
			f.fail(w, "migrations is an object, single-step or {old_tag, new_tag, steps}: %v", err)
			return
		}
		if mig.OldTag != cur {
			f.t.Errorf("fake Cloudflare: migration old_tag %q, script %s is at %q", mig.OldTag, name, cur)
			writeCFError(w, http.StatusBadRequest, "Migration tag precondition failed")
			return
		}
		if mig.NewTag == "" || (len(mig.NewSqliteClasses) == 0) == (len(mig.Steps) == 0) {
			f.fail(w, "migrations need new_tag and exactly one of new_sqlite_classes or steps: %s", meta.Migrations)
			return
		}
		newTag = mig.NewTag
	}
	f.metadatas[name] = append(f.metadatas[name], raw)
	if hasAssets {
		f.putJWTs[name] = append(f.putJWTs[name], meta.Assets.JWT)
	}
	if name == f.failScript {
		message := "script upload failed"
		if f.echoToken {
			message += ": " + testCFToken
		}
		writeCFError(w, http.StatusInternalServerError, message)
		return
	}
	f.scripts[name] = newTag
	writeCFResult(w, http.StatusOK, map[string]any{"id": name, "startup_time_ms": 1})
}

func writeCFResult(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": status >= 200 && status < 300, "result": result, "errors": []any{}, "messages": []any{}})
}

func writeCFError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "result": nil, "errors": []map[string]any{{"code": 1000, "message": message}}})
}

func makeTestBundle(t *testing.T, dir, version string, now time.Time, priv ed25519.PrivateKey, signed bool, expires time.Time) {
	t.Helper()
	for name, content := range map[string][]byte{
		"worker.js":    []byte("export default { fetch() {} }"),
		"provision.js": []byte("export default { fetch() {} }"),
		"panel.wasm":   []byte("wasm"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	assetDir := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"index.html": "<html>decoy</html>", "app.js": "console.log(1)"} {
		if err := os.WriteFile(filepath.Join(assetDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var tarball bytes.Buffer
	if err := release.WriteEdgeBundle(&tarball, release.EdgeInputs{
		Version: version, WorkerJS: filepath.Join(dir, "worker.js"), ProvisionJS: filepath.Join(dir, "provision.js"),
		Wasm: filepath.Join(dir, "panel.wasm"), AssetsDir: assetDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, release.EdgeBundleName), tarball.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := release.EdgeManifestFor(filepath.Join(dir, release.EdgeBundleName), version, now.Add(-time.Minute).Unix(), expires.Unix())
	if err != nil {
		t.Fatal(err)
	}
	body, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, release.EdgeManifestName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if signed {
		if err := os.WriteFile(filepath.Join(dir, release.EdgeSignatureName), release.SignEdge(priv, body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func setupDeploy(t *testing.T, bundleVersion string) (*fakeCloudflare, Options, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeTestBundle(t, bundleDir, bundleVersion, testNow, priv, true, testNow.Add(time.Hour))
	fake := newFakeCloudflare(t)
	server := httptest.NewServer(fake)
	seed := make([]byte, 4096)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	opts := Options{
		Host: "panel.example.com", Name: "mistgate-edge", D1Location: "weur", BundleDir: bundleDir,
		Token: testCFToken, StatePath: filepath.Join(t.TempDir(), "mistgate-edge.json"), PublicKey: pub,
		APIBaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return testNow }, Random: bytes.NewReader(seed),
		PollAttempts: 2, PollInterval: time.Millisecond, DecoyURL: server.URL + "/",
	}
	fake.statePath = opts.StatePath
	t.Cleanup(server.Close)
	return fake, opts, priv
}

func newBundleDir(t *testing.T, priv ed25519.PrivateKey, version string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeTestBundle(t, dir, version, testNow, priv, true, testNow.Add(time.Hour))
	return dir
}

func mustDeploy(t *testing.T, opts Options) string {
	t.Helper()
	var out bytes.Buffer
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatalf("deploy: %v\noutput: %s", err, out.String())
	}
	return out.String()
}

func mustState(t *testing.T, path string) localState {
	t.Helper()
	state, ok, err := loadExistingState(path)
	if err != nil || !ok {
		t.Fatalf("local state: %v, exists=%v", err, ok)
	}
	return state
}

func scriptPuts(requests []string) []string {
	var puts []string
	for _, req := range requests {
		if strings.HasPrefix(req, "PUT "+apiPrefix+acct+"/workers/scripts/") && !strings.HasSuffix(req, "/schedules") {
			puts = append(puts, strings.TrimPrefix(req, "PUT "+apiPrefix+acct+"/workers/scripts/"))
		}
	}
	return puts
}

func requireList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got %q\nwant %q", what, got, want)
	}
}

const (
	reqAccounts = "GET /client/v4/accounts?per_page=50&page=1"
	reqZone1    = "GET /client/v4/zones?account.id=acct-id&name=panel.example.com&per_page=50"
	reqZone2    = "GET /client/v4/zones?account.id=acct-id&name=example.com&per_page=50"
	reqScripts  = "GET /client/v4/accounts/acct-id/workers/scripts"
	reqD1List   = "GET /client/v4/accounts/acct-id/d1/database?name=mistgate-edge&page=1&per_page=100"
	reqD1Create = "POST /client/v4/accounts/acct-id/d1/database"
	reqSession  = "POST /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/assets-upload-session"
	reqUpload   = "POST /client/v4/accounts/acct-id/workers/assets/upload?base64=true"
	reqPutMain  = "PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge"
	reqPutProv  = "PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision"
	reqCron     = "PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/schedules"
	reqSubMain  = "POST /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/subdomain"
	reqSubProv  = "POST /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision/subdomain"
	reqDomain   = "PUT /client/v4/accounts/acct-id/workers/domains"
	reqDecoy    = "GET /"
)

func TestDeployFirstDeployRequestsAndMetadata(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	out := mustDeploy(t, opts)
	requireList(t, "request order", fake.requests, []string{
		reqAccounts, reqZone1, reqZone2, reqScripts, reqD1List, reqD1Create,
		reqSession, reqUpload, reqUpload, reqPutMain,
		reqPutProv,
		reqSession, reqPutMain,
		reqCron, reqSubMain, reqSubProv, reqDomain, reqDecoy,
	})
	state := mustState(t, opts.StatePath)
	if state.DatabaseID != "d1-id" {
		t.Fatalf("database id was not recorded: %+v", state)
	}
	if got := fake.putJWTs["mistgate-edge"]; len(got) != 2 || got[0] == got[1] || !strings.HasPrefix(got[0], "completion-") {
		t.Fatalf("each main PUT needs its own completion token: %q", got)
	}
	main, prov := fake.metadatas["mistgate-edge"], fake.metadatas["mistgate-edge-provision"]
	if len(main) != 2 || len(prov) != 1 {
		t.Fatalf("script upload counts: main=%d provision=%d", len(main), len(prov))
	}
	allSteps := `{"new_tag":"v2","steps":[{"new_sqlite_classes":["Limiter"]},{"new_sqlite_classes":["NodeLink"]}]}`
	if want := goldenMain(state, false, fake.putJWTs["mistgate-edge"][0], allSteps); main[0] != want {
		t.Errorf("first main metadata:\n got %s\nwant %s", main[0], want)
	}
	if want := goldenMain(state, true, fake.putJWTs["mistgate-edge"][1], ""); main[1] != want {
		t.Errorf("final main metadata:\n got %s\nwant %s", main[1], want)
	}
	if want := goldenProvision(state, `{"new_tag":"v1","new_sqlite_classes":["Provisioner"]}`); prov[0] != want {
		t.Errorf("provision metadata:\n got %s\nwant %s", prov[0], want)
	}
	if fake.dbLocation != "weur" {
		t.Errorf("D1 location hint %q", fake.dbLocation)
	}
	if !strings.Contains(out, "Setup link (one-time") || !strings.Contains(out, setupLink(state)) || !strings.Contains(out, "30 minutes from the panel's first start") {
		t.Fatalf("deploy output omitted the one-time link: %s", out)
	}
	if !strings.Contains(out, "Keys saved to ") || !strings.Contains(out, "back it up") || (runtime.GOOS == "windows") != strings.Contains(out, "0600 is not enforced") {
		t.Fatalf("deploy output omitted the key file notice: %s", out)
	}
	for _, secret := range []string{testCFToken, state.MasterKey, "completion-", "session-"} {
		if strings.Contains(out, secret) {
			t.Fatalf("deploy output leaked %q", secret)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(opts.StatePath); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("state file mode = %v, %v; want 0600", fi, err)
		}
	}
	raw, _ := os.ReadFile(opts.StatePath)
	if strings.Contains(string(raw), testCFToken) {
		t.Fatal("the API token reached the state file")
	}
}

func TestDeployUpdateRequestsAndMetadata(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	before := mustState(t, opts.StatePath)
	fake.requests, fake.metadatas, fake.putJWTs = nil, make(map[string][]string), make(map[string][]string)
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4")
	mustDeploy(t, opts)
	// Both scripts are at their last migration and the assets are known: no migrations, no asset upload.
	requireList(t, "update request order", fake.requests, []string{
		reqAccounts, reqZone1, reqZone2, reqScripts, reqD1List,
		reqPutProv,
		reqSession, reqPutMain,
		reqCron, reqSubMain, reqSubProv, reqDomain, reqDecoy,
	})
	after := mustState(t, opts.StatePath)
	if after.MasterKey != before.MasterKey || after.AdminPrefix != before.AdminPrefix || after.SubPrefix != before.SubPrefix || after.DatabaseID != before.DatabaseID {
		t.Fatalf("update changed the saved keys or resources: before=%+v after=%+v", before, after)
	}
	if after.SetupToken == before.SetupToken {
		t.Fatal("a run without --resume must mint a new setup token")
	}
	if after.BundleVersion != "v1.2.4" {
		t.Fatalf("local state did not record the bundle: %q", after.BundleVersion)
	}
	if got, want := fake.metadatas["mistgate-edge"][0], goldenMain(after, true, fake.putJWTs["mistgate-edge"][0], ""); got != want {
		t.Errorf("update main metadata:\n got %s\nwant %s", got, want)
	}
	if got, want := fake.metadatas["mistgate-edge-provision"][0], goldenProvision(after, ""); got != want {
		t.Errorf("update provision metadata:\n got %s\nwant %s", got, want)
	}
	if fake.assetUploads != 2 {
		t.Fatalf("assets uploaded %d times, want only the first deploy's 2 buckets", fake.assetUploads)
	}
}

func TestDeployResumeKeepsTheSetupToken(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	before := mustState(t, opts.StatePath)
	fake.requests = nil
	opts.Resume = true
	mustDeploy(t, opts)
	if after := mustState(t, opts.StatePath); after != before {
		t.Fatalf("--resume changed the key file: before=%+v after=%+v", before, after)
	}
	for _, req := range fake.requests {
		if req == reqD1Create {
			t.Fatal("resume created a second D1 database")
		}
	}
}

func TestDeployUpdateFailureCanResumeTheSameBundle(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4")
	fake.failScript = "mistgate-edge-provision"
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted the failed update script upload")
	}
	state := mustState(t, opts.StatePath)
	if state.BundleVersion != "v1.2.4" {
		t.Fatalf("failed update did not persist its resume version: %+v", state)
	}
	fake.failScript = ""
	opts.Resume = true
	mustDeploy(t, opts)
	if after := mustState(t, opts.StatePath); after.SetupToken != state.SetupToken {
		t.Fatal("resume minted a new setup token")
	}
}

func TestDeployNoZoneCreatesNothing(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.zoneStatus = ""
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "zone must be on Cloudflare DNS") {
		t.Fatalf("missing zone error = %v", err)
	}
	for _, request := range fake.requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("request after missing zone was not GET: %s", request)
		}
	}
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Fatalf("created a local config without a zone: %v", err)
	}
}

func TestDeployPendingZoneNamesItsStatus(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.zoneStatus = "pending"
	err := Deploy(context.Background(), opts, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `status "pending"`) || strings.Contains(err.Error(), "must be on Cloudflare DNS") {
		t.Fatalf("pending zone error = %v", err)
	}
}

func TestDeployBadTokenSaysSo(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.rejectToken = true
	err := Deploy(context.Background(), opts, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "rejected the API token (HTTP 401)") || strings.Contains(err.Error(), testCFToken) {
		t.Fatalf("bad token error = %v", err)
	}
	requireList(t, "requests", fake.requests, []string{reqAccounts})
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Fatalf("a rejected token left a key file: %v", err)
	}
}

func TestDeployFirstPUTFailureKeepsKeyFileAndResumeFinishes(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.failScript, fake.echoToken = "mistgate-edge", true
	var out bytes.Buffer
	err := Deploy(context.Background(), opts, &out)
	if err == nil || strings.Contains(err.Error(), testCFToken) || strings.Contains(out.String(), testCFToken) {
		t.Fatalf("failed PUT error/output leaked token: err=%v output=%q", err, out.String())
	}
	state := mustState(t, opts.StatePath)
	if state.MasterKey == "" || state.SetupToken == "" || state.DatabaseID != "d1-id" {
		t.Fatalf("key file does not hold the recoverable secrets: %+v", state)
	}
	fake.failScript, fake.echoToken = "", false
	fake.requests = nil
	opts.Resume = true
	mustDeploy(t, opts)
	requireList(t, "resume script order", scriptPuts(fake.requests), []string{"mistgate-edge", "mistgate-edge-provision", "mistgate-edge"})
}

// The main script exists after the first PUT, the Provisioner does not: resume uploads the Provisioner, then main with
// PROVISIONER and with no migrations (main is already at its last tag).
func TestDeployProvisionPUTFailureOnFirstDeployResumeFinishes(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.failScript = "mistgate-edge-provision"
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted the failed provision upload")
	}
	if fake.scripts["mistgate-edge"] != "v2" {
		t.Fatalf("main should exist at its last migration: %v", fake.scripts)
	}
	fake.failScript, fake.requests = "", nil
	fake.metadatas, fake.putJWTs = make(map[string][]string), make(map[string][]string)
	opts.Resume = true
	mustDeploy(t, opts)
	requireList(t, "resume script order", scriptPuts(fake.requests), []string{"mistgate-edge-provision", "mistgate-edge"})
	state := mustState(t, opts.StatePath)
	if got, want := fake.metadatas["mistgate-edge"][0], goldenMain(state, true, fake.putJWTs["mistgate-edge"][0], ""); got != want {
		t.Errorf("resumed main metadata:\n got %s\nwant %s", got, want)
	}
	if got, want := fake.metadatas["mistgate-edge-provision"][0], goldenProvision(state, `{"new_tag":"v1","new_sqlite_classes":["Provisioner"]}`); got != want {
		t.Errorf("resumed provision metadata:\n got %s\nwant %s", got, want)
	}
}

// The Provisioner exists, main does not: main goes up without PROVISIONER first, with every migration.
func TestDeployMainMissingProvisionPresent(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	delete(fake.scripts, "mistgate-edge")
	fake.requests = nil
	fake.metadatas, fake.putJWTs = make(map[string][]string), make(map[string][]string)
	opts.Resume = true
	mustDeploy(t, opts)
	requireList(t, "script order", scriptPuts(fake.requests), []string{"mistgate-edge", "mistgate-edge-provision", "mistgate-edge"})
	state := mustState(t, opts.StatePath)
	main := fake.metadatas["mistgate-edge"]
	allSteps := `{"new_tag":"v2","steps":[{"new_sqlite_classes":["Limiter"]},{"new_sqlite_classes":["NodeLink"]}]}`
	if want := goldenMain(state, false, fake.putJWTs["mistgate-edge"][0], allSteps); main[0] != want {
		t.Errorf("recreated main metadata:\n got %s\nwant %s", main[0], want)
	}
	if want := goldenMain(state, true, fake.putJWTs["mistgate-edge"][1], ""); main[1] != want {
		t.Errorf("final main metadata:\n got %s\nwant %s", main[1], want)
	}
	if want := goldenProvision(state, ""); fake.metadatas["mistgate-edge-provision"][0] != want {
		t.Errorf("provision metadata:\n got %s\nwant %s", fake.metadatas["mistgate-edge-provision"][0], want)
	}
}

func TestDeployAppliesOnlyTheMigrationsAfterTheDeployedTag(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	fake.scripts["mistgate-edge"] = "v1" // an older edge: only NodeLink is new
	fake.metadatas = make(map[string][]string)
	mustDeploy(t, opts)
	var meta struct {
		Migrations json.RawMessage `json:"migrations"`
	}
	if err := json.Unmarshal([]byte(fake.metadatas["mistgate-edge"][0]), &meta); err != nil {
		t.Fatal(err)
	}
	if want := `{"old_tag":"v1","new_tag":"v2","new_sqlite_classes":["NodeLink"]}`; string(meta.Migrations) != want {
		t.Fatalf("migrations = %s, want %s", meta.Migrations, want)
	}
	if fake.scripts["mistgate-edge"] != "v2" {
		t.Fatalf("main tag after update: %q", fake.scripts["mistgate-edge"])
	}
}

func TestDeployRefusesABundleOlderThanTheDeployment(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	fake.scripts["mistgate-edge"] = "v9"
	fake.requests = nil
	err := Deploy(context.Background(), opts, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `"v9"`) || !strings.Contains(err.Error(), "older") {
		t.Fatalf("older bundle error = %v", err)
	}
	if puts := scriptPuts(fake.requests); len(puts) != 0 {
		t.Fatalf("uploaded %q before refusing", puts)
	}
}

func TestPlanMigration(t *testing.T) {
	steps := []release.EdgeMigrate{{Tag: "v1", NewSqliteClasses: []string{"A"}}, {Tag: "v2", NewSqliteClasses: []string{"B"}}, {Tag: "v3", NewSqliteClasses: []string{"C"}}}
	cases := []struct {
		cur, want string
		err       bool
	}{
		{"", `{"new_tag":"v3","steps":[{"new_sqlite_classes":["A"]},{"new_sqlite_classes":["B"]},{"new_sqlite_classes":["C"]}]}`, false},
		{"v1", `{"old_tag":"v1","new_tag":"v3","steps":[{"new_sqlite_classes":["B"]},{"new_sqlite_classes":["C"]}]}`, false},
		{"v2", `{"old_tag":"v2","new_tag":"v3","new_sqlite_classes":["C"]}`, false},
		{"v3", ``, false},
		{"v0", ``, true},
	}
	for _, c := range cases {
		got, err := planMigration(steps, c.cur)
		if (err != nil) != c.err {
			t.Fatalf("tag %q: err=%v", c.cur, err)
		}
		raw := ""
		if got != nil {
			b, _ := json.Marshal(got)
			raw = string(b)
		}
		if raw != c.want {
			t.Errorf("tag %q: %s, want %s", c.cur, raw, c.want)
		}
	}
	if got, err := planMigration(nil, "x"); got != nil || err != nil {
		t.Errorf("no steps: %v %v", got, err)
	}
}

func TestDeployRerunIsIdempotent(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	before := mustState(t, opts.StatePath)
	fake.requests = nil
	mustDeploy(t, opts)
	for _, req := range fake.requests {
		if req == reqD1Create {
			t.Fatal("rerun created a second D1 database")
		}
	}
	after := mustState(t, opts.StatePath)
	if after.MasterKey != before.MasterKey || after.AdminPrefix != before.AdminPrefix || after.SubPrefix != before.SubPrefix || after.DatabaseID != before.DatabaseID {
		t.Fatal("rerun changed the saved keys")
	}
	if len(fake.metadatas["mistgate-edge"]) != 3 || len(fake.metadatas["mistgate-edge-provision"]) != 2 {
		t.Fatalf("rerun uploads: main=%d provision=%d", len(fake.metadatas["mistgate-edge"]), len(fake.metadatas["mistgate-edge-provision"]))
	}
}

func TestDeployDryRunSendsOnlyGET(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	opts.DryRun = true
	var out bytes.Buffer
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatal(err)
	}
	requireList(t, "dry-run requests", fake.requests, []string{reqAccounts, reqZone1, reqZone2, reqScripts, reqD1List})
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote local secrets: %v", err)
	}
	if !strings.Contains(out.String(), "Plan for panel.example.com") {
		t.Fatalf("dry-run did not print plan: %s", out.String())
	}
}

func TestDeployD1(t *testing.T) {
	t.Run("location is validated before any request", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		opts.D1Location = "mars"
		if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--d1-location") {
			t.Fatalf("bad location error = %v", err)
		}
		if len(fake.requests) != 0 {
			t.Fatalf("requests before validation: %q", fake.requests)
		}
	})
	t.Run("the location hint reaches the create call", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		opts.D1Location = "apac"
		mustDeploy(t, opts)
		if fake.dbLocation != "apac" {
			t.Fatalf("location hint %q", fake.dbLocation)
		}
	})
	t.Run("a database that only contains the name is not ours", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		mustDeploy(t, opts) // the preloaded mistgate-edge-staging matched the API's name filter and was ignored
		if db, ok := fake.ours(); !ok || db.UUID != "d1-id" {
			t.Fatalf("databases: %+v", fake.databases)
		}
	})
	t.Run("an existing database without a key file is not adopted", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		fake.addDatabase("mistgate-edge")
		if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
			t.Fatalf("adopt error = %v", err)
		}
		if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
			t.Fatal("wrote a key file")
		}
	})
	t.Run("a key file with no database id adopts the one database that exists", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		mustDeploy(t, opts)
		state := mustState(t, opts.StatePath)
		state.DatabaseID = ""
		if err := replaceStateFile(opts.StatePath, state); err != nil {
			t.Fatal(err)
		}
		fake.requests = nil
		opts.Resume = true
		mustDeploy(t, opts)
		for _, req := range fake.requests {
			if req == reqD1Create {
				t.Fatal("created a second database")
			}
		}
		if got := mustState(t, opts.StatePath).DatabaseID; got != "d1-id" {
			t.Fatalf("adopted id %q", got)
		}
	})
	t.Run("a key file with no database id creates it when none exists", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		fake.failScript = "mistgate-edge" // stop right after the D1 step
		_ = Deploy(context.Background(), opts, &bytes.Buffer{})
		fake.failScript = ""
		state := mustState(t, opts.StatePath)
		state.DatabaseID = ""
		if err := replaceStateFile(opts.StatePath, state); err != nil {
			t.Fatal(err)
		}
		fake.databases = fake.databases[:1] // the database is gone
		opts.Resume = true
		mustDeploy(t, opts)
		if got := mustState(t, opts.StatePath).DatabaseID; got != "d1-id" {
			t.Fatalf("created id %q", got)
		}
	})
	t.Run("a vanished database is not replaced", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		mustDeploy(t, opts)
		fake.databases = fake.databases[:1]
		if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatalf("vanished database error = %v", err)
		}
	})
}

func TestBundleVerificationCases(t *testing.T) {
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newDir := func(name, version string, signed bool, expires time.Time) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		makeTestBundle(t, dir, version, testNow, priv, signed, expires)
		return dir
	}
	baseOpts := func(dir string) Options {
		return Options{BundleDir: dir, PublicKey: pub, Now: func() time.Time { return testNow }}
	}
	read := func(opts Options) error {
		_, err := readBundle(context.Background(), opts, http.DefaultClient, testNow)
		return err
	}
	foreign, _, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("wrong key", func(t *testing.T) {
		opts := baseOpts(newDir("wrong-key", "v1.2.3", true, testNow.Add(time.Hour)))
		opts.PublicKey = foreign
		if err := read(opts); err == nil || !strings.Contains(err.Error(), "signature does not verify") {
			t.Fatalf("wrong key error = %v", err)
		}
	})
	t.Run("tampered tarball", func(t *testing.T) {
		dir := newDir("tampered", "v1.2.3", true, testNow.Add(time.Hour))
		path := filepath.Join(dir, release.EdgeBundleName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)/2] ^= 0xff
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := read(baseOpts(dir)); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("tampered bundle error = %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		if err := read(baseOpts(newDir("expired", "v1.2.3", true, testNow.Add(-time.Second)))); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired bundle error = %v", err)
		}
	})
	t.Run("version mismatch", func(t *testing.T) {
		opts := baseOpts(newDir("version", "v1.2.3", true, testNow.Add(time.Hour)))
		opts.Version = "v1.2.4"
		if err := read(opts); err == nil || !strings.Contains(err.Error(), "does not match requested version") {
			t.Fatalf("version error = %v", err)
		}
	})
	t.Run("unsigned local directory requires flag", func(t *testing.T) {
		dir := newDir("unsigned", "v1.2.3", false, testNow.Add(time.Hour))
		if err := read(baseOpts(dir)); err == nil || !strings.Contains(err.Error(), "no edge-manifest.sig") {
			t.Fatalf("unsigned bundle error = %v", err)
		}
		opts := baseOpts(dir)
		opts.InsecureSkipSignature = true
		if err := read(opts); err != nil {
			t.Fatalf("unsigned development bundle with explicit bypass: %v", err)
		}
	})
	t.Run("a signature that is there is verified even with the flag", func(t *testing.T) {
		opts := baseOpts(newDir("flag-sig", "v1.2.3", true, testNow.Add(time.Hour)))
		opts.InsecureSkipSignature = true
		if err := read(opts); err != nil {
			t.Fatalf("good signature with the flag: %v", err)
		}
		opts.PublicKey = foreign
		if err := read(opts); err == nil || !strings.Contains(err.Error(), "signature does not verify") {
			t.Fatalf("bad signature with the flag = %v", err)
		}
	})
	t.Run("the flag is only for a local directory", func(t *testing.T) {
		opts := Options{Version: "v1.2.3", InsecureSkipSignature: true, PublicKey: pub}
		if err := read(opts); err == nil || !strings.Contains(err.Error(), "only available with --bundle") {
			t.Fatalf("flag with a download = %v", err)
		}
		deployOpts := Options{Host: "panel.example.com", Token: "x", Version: "v1.2.3", InsecureSkipSignature: true}
		if err := Deploy(context.Background(), deployOpts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "only available with --bundle") {
			t.Fatalf("Deploy with the flag and a download = %v", err)
		}
	})
}

func TestBundleDownloadAndVersionDefault(t *testing.T) {
	_, opts, priv := setupDeploy(t, "v1.2.3")
	dir := newBundleDir(t, priv, "v1.2.3")
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		name := strings.TrimPrefix(r.URL.Path, "/v1.2.3/")
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer server.Close()
	opts.BundleDir, opts.GitHubDownloadBaseURL, opts.DryRun = "", server.URL, true

	t.Run("a development build with no version is refused", func(t *testing.T) {
		if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "development build") {
			t.Fatalf("no version error = %v", err)
		}
		if len(gotPaths) != 0 {
			t.Fatalf("downloaded %q without a version", gotPaths)
		}
	})
	t.Run("the binary's own release is the default", func(t *testing.T) {
		opts := opts
		opts.DefaultVersion = "v1.2.3"
		var out bytes.Buffer
		if err := Deploy(context.Background(), opts, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Join(gotPaths, " ") != "/v1.2.3/edge-manifest.json /v1.2.3/edge-manifest.sig /v1.2.3/mistgate-edge.tar.gz" {
			t.Fatalf("downloaded %q", gotPaths)
		}
		if !strings.Contains(out.String(), "edge bundle v1.2.3") {
			t.Fatalf("plan: %s", out.String())
		}
	})
	t.Run("the tag must equal the manifest's version", func(t *testing.T) {
		// served under the tag v1.2.3 although the manifest says v9.9.9
		root := t.TempDir()
		wrong := filepath.Join(root, "v1.2.3")
		if err := os.MkdirAll(wrong, 0o755); err != nil {
			t.Fatal(err)
		}
		makeTestBundle(t, wrong, "v9.9.9", testNow, priv, true, testNow.Add(time.Hour))
		server2 := httptest.NewServer(http.FileServer(http.Dir(root)))
		defer server2.Close()
		opts := opts
		opts.GitHubDownloadBaseURL, opts.DefaultVersion = server2.URL, "v1.2.3"
		if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not match requested version") {
			t.Fatalf("tag/manifest mismatch error = %v", err)
		}
	})
}

func TestDeployTokenIsRedactedFromAPIError(t *testing.T) {
	_, opts, _ := setupDeploy(t, "v1.2.3")
	var out bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCFError(w, http.StatusForbidden, "rejected "+testCFToken)
	}))
	defer server.Close()
	opts.APIBaseURL = server.URL
	opts.StatePath = filepath.Join(t.TempDir(), "other.json")
	err := Deploy(context.Background(), opts, &out)
	if err == nil || strings.Contains(err.Error(), testCFToken) || strings.Contains(out.String(), testCFToken) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("token escaped through API error: err=%v output=%q", err, out.String())
	}
}

func TestDeployDecoyTimeoutStillPrintsTheLink(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.decoyStatus = http.StatusNotFound
	opts.PollAttempts, opts.PollInterval = 3, 0
	var sleeps []time.Duration
	opts.Sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	out := mustDeploy(t, opts)
	if !reflect.DeepEqual(sleeps, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Fatalf("poll sleeps %v: the default interval is 5 s, with no sleep after the last try", sleeps)
	}
	state := mustState(t, opts.StatePath)
	if !strings.Contains(out, setupLink(state)) || !strings.Contains(out, "certificate") {
		t.Fatalf("timeout output: %s", out)
	}
}

func TestSetupLinkReadsOnlyTheLocalFile(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	fake.requests = nil
	var out bytes.Buffer
	if err := SetupLink(opts.StatePath, &out); err != nil {
		t.Fatal(err)
	}
	if state := mustState(t, opts.StatePath); strings.TrimSpace(out.String()) != setupLink(state) {
		t.Fatalf("setup link %q", out.String())
	}
	if len(fake.requests) != 0 {
		t.Fatalf("setup-link made API requests: %q", fake.requests)
	}
}

func TestAssetHashContract(t *testing.T) {
	content := []byte("hello")
	manifest, contents, err := buildAssetManifest(map[string][]byte{"assets/a.txt": content, "assets/LICENSE": content})
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	for path, ext := range map[string]string{"/a.txt": "txt", "/LICENSE": ""} {
		sum := sha256.Sum256([]byte(encoded + ext)) // the extension has no dot
		want := hex.EncodeToString(sum[:])[:32]
		if got := manifest[path]; got.Hash != want || got.Size != len(content) {
			t.Fatalf("%s: %+v, want hash %s", path, got, want)
		}
		if contents[want].contentType == "" {
			t.Fatalf("%s has no content type", path)
		}
	}
	raw, _ := json.Marshal(manifest["/a.txt"])
	if strings.Contains(string(raw), "content_type") {
		t.Fatalf("manifest entry carries a content type: %s", raw)
	}
}

func TestHTTPClientHasNoOverallTimeout(t *testing.T) {
	c := newHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if c.Timeout != 0 || !ok || tr.ResponseHeaderTimeout == 0 || tr.TLSHandshakeTimeout == 0 {
		t.Fatalf("client %+v transport %+v", c, tr)
	}
}

func goldenMain(state localState, withProvisioner bool, jwt, migrations string) string {
	setupHash := sha256.Sum256([]byte(state.SetupToken))
	bindings := []string{
		`{"type":"d1","name":"DB","database_id":"d1-id"}`,
		`{"type":"durable_object_namespace","name":"LIMITER","class_name":"Limiter"}`,
		`{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink"}`,
	}
	if withProvisioner {
		bindings = append(bindings, `{"type":"durable_object_namespace","name":"PROVISIONER","class_name":"Provisioner","script_name":"mistgate-edge-provision"}`)
	}
	bindings = append(bindings,
		`{"type":"assets","name":"ASSETS"}`,
		fmt.Sprintf(`{"type":"secret_text","name":"MASTER_KEY","text":"%s"}`, state.MasterKey),
		fmt.Sprintf(`{"type":"secret_text","name":"ADMIN_PREFIX","text":"%s"}`, state.AdminPrefix),
		fmt.Sprintf(`{"type":"secret_text","name":"SUB_PREFIX","text":"%s"}`, state.SubPrefix),
		fmt.Sprintf(`{"type":"secret_text","name":"SETUP_TOKEN_SHA256","text":"%s"}`, hex.EncodeToString(setupHash[:])),
		`{"type":"plain_text","name":"PUBLIC_URL","text":"https://panel.example.com"}`,
	)
	if migrations != "" {
		migrations = `"migrations":` + migrations + ","
	}
	return fmt.Sprintf(`{"main_module":"worker.js","compatibility_date":"2026-10-01","compatibility_flags":[],"bindings":[%s],%s"assets":{"jwt":"%s","config":{"run_worker_first":true,"html_handling":"none","not_found_handling":"none"}},"observability":{"enabled":false}}`, strings.Join(bindings, ","), migrations, jwt)
}

func goldenProvision(state localState, migrations string) string {
	if migrations != "" {
		migrations = `"migrations":` + migrations + ","
	}
	return fmt.Sprintf(`{"main_module":"provision.js","compatibility_date":"2026-10-01","compatibility_flags":[],"bindings":[{"type":"d1","name":"DB","database_id":"d1-id"},{"type":"durable_object_namespace","name":"PROVISIONER","class_name":"Provisioner"},{"type":"durable_object_namespace","name":"LIMITER","class_name":"Limiter","script_name":"mistgate-edge"},{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink","script_name":"mistgate-edge"},{"type":"secret_text","name":"MASTER_KEY","text":"%s"}],%s"observability":{"enabled":false}}`, state.MasterKey, migrations)
}
