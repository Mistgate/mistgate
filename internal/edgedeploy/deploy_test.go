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
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

const testCFToken = "cf-token-never-print-this"

var testNow = time.Unix(1_800_000_000, 0)

type fakeCloudflare struct {
	t                 *testing.T
	requests          []string
	scripts           map[string]bool
	database          d1Database
	databaseExists    bool
	zoneExists        bool
	failScript        string
	echoToken         bool
	metadatas         map[string][]string
	assetManifest     map[string]assetManifestEntry
	assetUploads      int
	lastDatabaseInput map[string]string
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	t.Helper()
	return &fakeCloudflare{
		t: t, scripts: make(map[string]bool), zoneExists: true,
		metadatas: make(map[string][]string), database: d1Database{UUID: "d1-id", Name: "mistgate-edge", Location: "weur"},
	}
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	if r.URL.Path == "/" {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "decoy")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/client/v4/") {
		f.t.Errorf("unexpected request path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testCFToken {
		f.t.Errorf("Cloudflare request has wrong auth header")
	}
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	switch {
	case r.Method == http.MethodGet && path == "/user/tokens/verify":
		writeCFResult(w, http.StatusOK, map[string]string{"status": "active"})
	case r.Method == http.MethodGet && path == "/accounts":
		writeCFResult(w, http.StatusOK, []account{{ID: "acct-id"}})
	case r.Method == http.MethodGet && path == "/zones":
		name := r.URL.Query().Get("name")
		if f.zoneExists && name == "example.com" {
			writeCFResult(w, http.StatusOK, []zone{{ID: "zone-id", Name: "example.com", Status: "active"}})
		} else {
			writeCFResult(w, http.StatusOK, []zone{})
		}
	case r.Method == http.MethodGet && path == "/accounts/acct-id/d1/database":
		if f.databaseExists {
			writeCFResult(w, http.StatusOK, []d1Database{f.database})
		} else {
			writeCFResult(w, http.StatusOK, []d1Database{})
		}
	case r.Method == http.MethodPost && path == "/accounts/acct-id/d1/database":
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Errorf("decode database request: %v", err)
		}
		f.lastDatabaseInput = in
		f.databaseExists = true
		f.database.Name, f.database.Location = in["name"], in["location"]
		writeCFResult(w, http.StatusOK, f.database)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/accounts/acct-id/workers/scripts/"):
		name := strings.TrimPrefix(path, "/accounts/acct-id/workers/scripts/")
		if f.scripts[name] {
			writeCFResult(w, http.StatusOK, map[string]any{})
		} else {
			writeCFResult(w, http.StatusNotFound, nil)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/assets-upload-session"):
		var in struct {
			Manifest map[string]assetManifestEntry `json:"manifest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Errorf("decode assets manifest: %v", err)
		}
		f.assetManifest = in.Manifest
		var hashes []string
		for _, asset := range in.Manifest {
			hashes = append(hashes, asset.Hash)
		}
		sort.Strings(hashes)
		writeCFResult(w, http.StatusOK, uploadSession{JWT: "asset-jwt", Buckets: [][]string{hashes}})
	case r.Method == http.MethodPost && path == "/accounts/acct-id/workers/assets/upload":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			f.t.Errorf("parse asset multipart: %v", err)
		}
		if r.Header.Get("CF-Worker-Assets-Token") != "asset-jwt" {
			f.t.Errorf("asset upload omitted the session token header")
		}
		f.assetUploads++
		writeCFResult(w, http.StatusOK, map[string]any{})
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/accounts/acct-id/workers/scripts/") && !strings.HasSuffix(path, "/schedules") && !strings.HasSuffix(path, "/subdomain"):
		name := strings.TrimPrefix(path, "/accounts/acct-id/workers/scripts/")
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			f.t.Errorf("parse script multipart: %v", err)
		}
		metadata := r.FormValue("metadata")
		if metadata == "" {
			f.t.Errorf("script %s has no metadata", name)
		}
		for module, contentType := range map[string]string{"worker.js": "application/javascript+module", "provision.js": "application/javascript+module", "panel.wasm": "application/wasm"} {
			files := r.MultipartForm.File[module]
			if len(files) == 0 {
				continue // the script bundle carries only the modules named by its edge.json entry
			}
			if got := files[0].Header.Get("Content-Type"); got != contentType {
				f.t.Errorf("module %s content type %q, want %q", module, got, contentType)
			}
		}
		f.metadatas[name] = append(f.metadatas[name], metadata)
		if name == f.failScript {
			message := "script upload failed"
			if f.echoToken {
				message += ": " + testCFToken
			}
			writeCFError(w, http.StatusInternalServerError, message)
			return
		}
		f.scripts[name] = true
		writeCFResult(w, http.StatusOK, map[string]any{})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/schedules"):
		writeCFResult(w, http.StatusOK, map[string]any{})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/subdomain"):
		writeCFResult(w, http.StatusOK, map[string]any{})
	case r.Method == http.MethodPut && path == "/accounts/acct-id/workers/domains":
		writeCFResult(w, http.StatusOK, map[string]any{})
	default:
		f.t.Errorf("unexpected Cloudflare request: %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

func writeCFResult(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": status >= 200 && status < 300, "result": result, "errors": []any{}})
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
	if err := os.WriteFile(filepath.Join(assetDir, "index.html"), []byte("<html>decoy</html>"), 0o644); err != nil {
		t.Fatal(err)
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

func setupDeploy(t *testing.T, bundleVersion string) (*fakeCloudflare, *httptest.Server, Options, ed25519.PublicKey, ed25519.PrivateKey) {
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
	seed := make([]byte, 128)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	opts := Options{
		Host: "panel.example.com", Name: "mistgate-edge", D1Location: "weur", BundleDir: bundleDir,
		Token: testCFToken, StatePath: filepath.Join(t.TempDir(), "mistgate-edge.json"), PublicKey: pub,
		APIBaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return testNow }, Random: bytes.NewReader(seed),
		PollAttempts: 2, PollInterval: 0, DecoyURL: server.URL + "/",
	}
	t.Cleanup(server.Close)
	return fake, server, opts, pub, priv
}

func TestDeployRequestOrderAndMetadataFirstDeploy(t *testing.T) {
	fake, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	var out bytes.Buffer
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /client/v4/user/tokens/verify",
		"GET /client/v4/accounts?per_page=50&page=1",
		"GET /client/v4/zones?account.id=acct-id&name=panel.example.com&per_page=50",
		"GET /client/v4/zones?account.id=acct-id&name=example.com&per_page=50",
		"GET /client/v4/accounts/acct-id/d1/database?name=mistgate-edge&per_page=100",
		"GET /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
		"GET /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision",
		"POST /client/v4/accounts/acct-id/d1/database",
		"POST /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/assets-upload-session",
		"POST /client/v4/accounts/acct-id/workers/assets/upload?base64=true",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/schedules",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/subdomain",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision/subdomain",
		"PUT /client/v4/accounts/acct-id/workers/domains",
		"GET /",
	}
	if !reflect.DeepEqual(fake.requests, want) {
		t.Fatalf("request order:\n got %q\nwant %q", fake.requests, want)
	}
	state, ok, err := loadExistingState(opts.StatePath)
	if err != nil || !ok {
		t.Fatalf("local state: %v, exists=%v", err, ok)
	}
	if len(fake.metadatas["mistgate-edge"]) != 2 || len(fake.metadatas["mistgate-edge-provision"]) != 1 {
		t.Fatalf("script upload counts: main=%d provision=%d", len(fake.metadatas["mistgate-edge"]), len(fake.metadatas["mistgate-edge-provision"]))
	}
	setupHash := sha256.Sum256([]byte(state.SetupToken))
	wantMainFirst := fmt.Sprintf(`{"main_module":"worker.js","compatibility_date":"2026-10-01","compatibility_flags":[],"bindings":[{"type":"d1","name":"DB","id":"d1-id"},{"type":"durable_object_namespace","name":"LIMITER","class_name":"Limiter"},{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink"},{"type":"assets","name":"ASSETS"},{"type":"secret_text","name":"MASTER_KEY","text":"%s"},{"type":"secret_text","name":"ADMIN_PREFIX","text":"%s"},{"type":"secret_text","name":"SUB_PREFIX","text":"%s"},{"type":"secret_text","name":"SETUP_TOKEN_SHA256","text":"%s"},{"type":"plain_text","name":"PUBLIC_URL","text":"https://panel.example.com"},{"type":"plain_text","name":"ADMIN_HOST","text":""}],"migrations":[{"tag":"v1","new_sqlite_classes":["Limiter"]},{"tag":"v2","new_sqlite_classes":["NodeLink"]}],"assets":{"jwt":"asset-jwt","config":{"run_worker_first":true,"html_handling":"none","not_found_handling":"none"}},"observability":{"enabled":false}}`, state.MasterKey, state.AdminPrefix, state.SubPrefix, hex.EncodeToString(setupHash[:]))
	wantProvision := fmt.Sprintf(`{"main_module":"provision.js","compatibility_date":"2026-10-01","compatibility_flags":[],"bindings":[{"type":"d1","name":"DB","id":"d1-id"},{"type":"durable_object_namespace","name":"PROVISIONER","class_name":"Provisioner"},{"type":"durable_object_namespace","name":"LIMITER","class_name":"Limiter","script_name":"mistgate-edge"},{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink","script_name":"mistgate-edge"},{"type":"secret_text","name":"MASTER_KEY","text":"%s"}],"migrations":[{"tag":"v1","new_sqlite_classes":["Provisioner"]}],"observability":{"enabled":false}}`, state.MasterKey)
	wantMainFinal := strings.Replace(wantMainFirst, `"observability":{"enabled":false}}`, `"observability":{"enabled":false}}`, 1)
	wantMainFinal = strings.Replace(wantMainFirst,
		`{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink"},{"type":"assets"`,
		`{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink"},{"type":"durable_object_namespace","name":"PROVISIONER","class_name":"Provisioner","script_name":"mistgate-edge-provision"},{"type":"assets"`, 1)
	for i, got := range fake.metadatas["mistgate-edge"] {
		wantMetadata := wantMainFinal
		if i == 0 {
			wantMetadata = wantMainFirst
		}
		if got != wantMetadata {
			t.Errorf("main metadata %d:\n got %s\nwant %s", i, got, wantMetadata)
		}
	}
	if got := fake.metadatas["mistgate-edge-provision"][0]; got != wantProvision {
		t.Errorf("provision metadata:\n got %s\nwant %s", got, wantProvision)
	}
	if !strings.Contains(out.String(), "Setup link (one-time") || !strings.Contains(out.String(), setupLink(state)) {
		t.Fatalf("deploy output omitted the one-time link: %s", out.String())
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(opts.StatePath); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("state file mode = %v, %v; want 0600", fi, err)
		}
	}
}

func TestDeployUpdateOrderAndMetadata(t *testing.T) {
	fake, _, opts, _, priv := setupDeploy(t, "v1.2.3")
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	stateBefore, _, err := loadExistingState(opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	fake.requests = nil
	fake.metadatas = make(map[string][]string)
	newBundle := filepath.Join(t.TempDir(), "bundle-update")
	if err := os.MkdirAll(newBundle, 0o755); err != nil {
		t.Fatal(err)
	}
	makeTestBundle(t, newBundle, "v1.2.4", testNow, priv, true, testNow.Add(time.Hour))
	opts.BundleDir = newBundle
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	wantRequests := []string{
		"GET /client/v4/user/tokens/verify",
		"GET /client/v4/accounts?per_page=50&page=1",
		"GET /client/v4/zones?account.id=acct-id&name=panel.example.com&per_page=50",
		"GET /client/v4/zones?account.id=acct-id&name=example.com&per_page=50",
		"GET /client/v4/accounts/acct-id/d1/database?name=mistgate-edge&per_page=100",
		"GET /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
		"GET /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision",
		"POST /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/assets-upload-session",
		"POST /client/v4/accounts/acct-id/workers/assets/upload?base64=true",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/schedules",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge/subdomain",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision/subdomain",
		"PUT /client/v4/accounts/acct-id/workers/domains",
		"GET /",
	}
	if !reflect.DeepEqual(fake.requests, wantRequests) {
		t.Fatalf("update request order:\n got %q\nwant %q", fake.requests, wantRequests)
	}
	if len(fake.metadatas["mistgate-edge"]) != 1 || len(fake.metadatas["mistgate-edge-provision"]) != 1 {
		t.Fatalf("update script counts: main=%d provision=%d", len(fake.metadatas["mistgate-edge"]), len(fake.metadatas["mistgate-edge-provision"]))
	}
	if got, want := fake.metadatas["mistgate-edge"][0], goldenMainMetadata(stateBefore, true, true); got != want {
		t.Errorf("update main metadata:\n got %s\nwant %s", got, want)
	}
	if got, want := fake.metadatas["mistgate-edge-provision"][0], goldenProvisionMetadata(stateBefore); got != want {
		t.Errorf("update provision metadata:\n got %s\nwant %s", got, want)
	}
	stateAfter, _, err := loadExistingState(opts.StatePath)
	if err != nil || stateAfter.MasterKey != stateBefore.MasterKey || stateAfter.SetupToken != stateBefore.SetupToken || stateAfter.DatabaseID != stateBefore.DatabaseID {
		t.Fatalf("update changed saved deployment secrets/resources: before=%+v after=%+v err=%v", stateBefore, stateAfter, err)
	}
	if stateAfter.BundleVersion != "v1.2.4" {
		t.Fatalf("local state did not record the bundle needed to resume: %q", stateAfter.BundleVersion)
	}
	if fake.assetUploads != 2 {
		t.Fatalf("assets uploaded %d times, want once per deploy", fake.assetUploads)
	}
}

func TestDeployUpdateFailureCanResumeTheSameBundle(t *testing.T) {
	fake, _, opts, _, priv := setupDeploy(t, "v1.2.3")
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	newBundle := filepath.Join(t.TempDir(), "bundle-update")
	if err := os.MkdirAll(newBundle, 0o755); err != nil {
		t.Fatal(err)
	}
	makeTestBundle(t, newBundle, "v1.2.4", testNow, priv, true, testNow.Add(time.Hour))
	opts.BundleDir = newBundle
	fake.failScript = "mistgate-edge-provision"
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted the failed update script upload")
	}
	state, _, err := loadExistingState(opts.StatePath)
	if err != nil || state.BundleVersion != "v1.2.4" {
		t.Fatalf("failed update did not persist its resume version: state=%+v err=%v", state, err)
	}
	fake.failScript = ""
	opts.Resume = true
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatalf("resume after update failure: %v", err)
	}
}

func TestDeployNoZoneCreatesNothing(t *testing.T) {
	fake, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	fake.zoneExists = false
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

func TestDeployPUTFailureKeepsKeyFileAndResumeFinishes(t *testing.T) {
	fake, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	fake.failScript, fake.echoToken = "mistgate-edge", true
	var out bytes.Buffer
	err := Deploy(context.Background(), opts, &out)
	if err == nil || strings.Contains(err.Error(), testCFToken) || strings.Contains(out.String(), testCFToken) {
		t.Fatalf("failed PUT error/output leaked token: err=%v output=%q", err, out.String())
	}
	state, ok, err := loadExistingState(opts.StatePath)
	if err != nil || !ok {
		t.Fatalf("key file was not written before PUT: exists=%v err=%v", ok, err)
	}
	if state.MasterKey == "" || state.SetupToken == "" {
		t.Fatalf("state file does not contain recoverable secrets")
	}
	fake.failScript, fake.echoToken = "", false
	fake.requests = nil
	opts.Resume = true
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	var scripts []string
	for _, req := range fake.requests {
		if strings.HasPrefix(req, "PUT /client/v4/accounts/acct-id/workers/scripts/") && !strings.HasSuffix(req, "/schedules") && !strings.HasSuffix(req, "/subdomain") {
			scripts = append(scripts, req)
		}
	}
	if !reflect.DeepEqual(scripts, []string{
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge-provision",
		"PUT /client/v4/accounts/acct-id/workers/scripts/mistgate-edge",
	}) {
		t.Fatalf("resume script order: %q", scripts)
	}
}

func TestDeployRerunIsIdempotent(t *testing.T) {
	fake, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	stateBefore, _, err := loadExistingState(opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	fake.requests = nil
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, req := range fake.requests {
		if req == "POST /client/v4/accounts/acct-id/d1/database" {
			t.Fatal("rerun created a second D1 database")
		}
	}
	stateAfter, _, err := loadExistingState(opts.StatePath)
	if err != nil || stateAfter.MasterKey != stateBefore.MasterKey || stateAfter.AdminPrefix != stateBefore.AdminPrefix || stateAfter.SubPrefix != stateBefore.SubPrefix || stateAfter.SetupToken != stateBefore.SetupToken {
		t.Fatalf("rerun changed local deployment secrets")
	}
	if len(fake.metadatas["mistgate-edge"]) != 3 || len(fake.metadatas["mistgate-edge-provision"]) != 2 {
		t.Fatalf("rerun did not upload the two scripts idempotently: main=%d provision=%d", len(fake.metadatas["mistgate-edge"]), len(fake.metadatas["mistgate-edge-provision"]))
	}
}

func TestDeployDryRunSendsOnlyGET(t *testing.T) {
	fake, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	opts.DryRun = true
	var out bytes.Buffer
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) == 0 {
		t.Fatal("dry-run made no discovery requests")
	}
	for _, request := range fake.requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("dry-run made a non-GET request: %s", request)
		}
	}
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote local secrets: %v", err)
	}
	if !strings.Contains(out.String(), "Plan for panel.example.com") {
		t.Fatalf("dry-run did not print plan: %s", out.String())
	}
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
	t.Run("wrong key", func(t *testing.T) {
		foreign, _, err := release.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		opts := baseOpts(newDir("wrong-key", "v1.2.3", true, testNow.Add(time.Hour)))
		opts.PublicKey = foreign
		if _, err := readBundle(context.Background(), opts, http.DefaultClient, testNow); err == nil || !strings.Contains(err.Error(), "signature does not verify") {
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
		if _, err := readBundle(context.Background(), baseOpts(dir), http.DefaultClient, testNow); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("tampered bundle error = %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		opts := baseOpts(newDir("expired", "v1.2.3", true, testNow.Add(-time.Second)))
		if _, err := readBundle(context.Background(), opts, http.DefaultClient, testNow); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired bundle error = %v", err)
		}
	})
	t.Run("version mismatch", func(t *testing.T) {
		opts := baseOpts(newDir("version", "v1.2.3", true, testNow.Add(time.Hour)))
		opts.Version = "v1.2.4"
		if _, err := readBundle(context.Background(), opts, http.DefaultClient, testNow); err == nil || !strings.Contains(err.Error(), "does not match requested version") {
			t.Fatalf("version error = %v", err)
		}
	})
	t.Run("unsigned local directory requires flag", func(t *testing.T) {
		dir := newDir("unsigned", "v1.2.3", false, testNow.Add(time.Hour))
		if _, err := readBundle(context.Background(), baseOpts(dir), http.DefaultClient, testNow); err == nil || !strings.Contains(err.Error(), "no edge-manifest.sig") {
			t.Fatalf("unsigned bundle error = %v", err)
		}
		opts := baseOpts(dir)
		opts.InsecureSkipSignature = true
		if _, err := readBundle(context.Background(), opts, http.DefaultClient, testNow); err != nil {
			t.Fatalf("unsigned development bundle with explicit bypass: %v", err)
		}
	})
}

func TestDeployTokenIsRedactedFromAPIError(t *testing.T) {
	_, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	var out bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/client/v4/user/tokens/verify" {
			writeCFError(w, http.StatusForbidden, "rejected "+testCFToken)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	opts.APIBaseURL = server.URL
	opts.StatePath = filepath.Join(t.TempDir(), "other.json")
	err := Deploy(context.Background(), opts, &out)
	if err == nil || strings.Contains(err.Error(), testCFToken) || strings.Contains(out.String(), testCFToken) {
		t.Fatalf("token escaped through API error: err=%v output=%q", err, out.String())
	}
}

func TestSetupLinkReadsOnlyTheLocalFile(t *testing.T) {
	fake, _, opts, _, _ := setupDeploy(t, "v1.2.3")
	if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	fake.requests = nil
	var out bytes.Buffer
	if err := SetupLink(opts.StatePath, &out); err != nil {
		t.Fatal(err)
	}
	state, _, err := loadExistingState(opts.StatePath)
	if err != nil || strings.TrimSpace(out.String()) != setupLink(state) {
		t.Fatalf("setup link %q; state=%+v err=%v", out.String(), state, err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("setup-link made API requests: %q", fake.requests)
	}
}

func TestAssetHashContract(t *testing.T) {
	content := []byte("hello")
	manifest, _, err := buildAssetManifest(map[string][]byte{"assets/a.txt": content})
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	sum := sha256.Sum256([]byte(encoded + ".txt"))
	want := hex.EncodeToString(sum[:])[:32]
	if got := manifest["/a.txt"].Hash; got != want {
		t.Fatalf("asset hash %s, want %s", got, want)
	}
}

func goldenMainMetadata(state localState, withProvisioner, keepAssets bool) string {
	setupHash := sha256.Sum256([]byte(state.SetupToken))
	bindings := []string{
		`{"type":"d1","name":"DB","id":"d1-id"}`,
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
		`{"type":"plain_text","name":"ADMIN_HOST","text":""}`,
	)
	keep := ""
	if keepAssets {
		keep = `,"keep_assets":true`
	}
	return fmt.Sprintf(`{"main_module":"worker.js","compatibility_date":"2026-10-01","compatibility_flags":[],"bindings":[%s],"migrations":[{"tag":"v1","new_sqlite_classes":["Limiter"]},{"tag":"v2","new_sqlite_classes":["NodeLink"]}],"assets":{"jwt":"asset-jwt","config":{"run_worker_first":true,"html_handling":"none","not_found_handling":"none"}},"observability":{"enabled":false}%s}`, strings.Join(bindings, ","), keep)
}

func goldenProvisionMetadata(state localState) string {
	return fmt.Sprintf(`{"main_module":"provision.js","compatibility_date":"2026-10-01","compatibility_flags":[],"bindings":[{"type":"d1","name":"DB","id":"d1-id"},{"type":"durable_object_namespace","name":"PROVISIONER","class_name":"Provisioner"},{"type":"durable_object_namespace","name":"LIMITER","class_name":"Limiter","script_name":"mistgate-edge"},{"type":"durable_object_namespace","name":"NODELINK","class_name":"NodeLink","script_name":"mistgate-edge"},{"type":"secret_text","name":"MASTER_KEY","text":"%s"}],"migrations":[{"tag":"v1","new_sqlite_classes":["Provisioner"]}],"observability":{"enabled":false}}`, state.MasterKey)
}
