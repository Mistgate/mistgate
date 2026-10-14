package edgedeploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	failScript  string // the PUT of this script fails
	echoToken   bool
	decoyStatus int
	// failNth makes the Nth state-changing request (any method but GET, counted in mutations) fail with HTTP 500 before it
	// changes anything; 0 = never.
	failNth   int
	mutations int

	metadatas    map[string][]string // script name -> raw metadata of every PUT
	putJWTs      map[string][]string // script name -> assets.jwt of every PUT
	sessions     map[string]*fakeSession
	completion   map[string]string // completion token -> script it was issued for
	uploaded     map[string]bool
	assetUploads int
	dbLocation   string
	seq          int

	versions        map[string][]string              // script name -> version ids, oldest first
	deployments     map[string][][]deploymentVersion // script name -> deployments, oldest first
	deploymentPosts []string                         // script names, in the order of the POST .../deployments calls
	crons           map[string][]string
	subdomainOff    map[string]bool
	domainService   string
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
		versions: make(map[string][]string), deployments: make(map[string][][]deploymentVersion),
		crons: make(map[string][]string), subdomainOff: make(map[string]bool),
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

// active is the version of the script's newest deployment.
func (f *fakeCloudflare) active(name string) string {
	list := f.deployments[name]
	if len(list) == 0 {
		return ""
	}
	return list[len(list)-1][0].VersionID
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

// scriptOf cuts the script name out of /accounts/acct-id/workers/scripts/<name><suffix>.
func scriptOf(path, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, acct+"/workers/scripts/"), suffix)
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
	if r.Method != http.MethodGet {
		f.mutations++
		if f.failNth != 0 && f.mutations == f.failNth {
			writeCFError(w, http.StatusInternalServerError, "injected failure")
			return
		}
	}
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
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/deployments"):
		f.listDeployments(w, scriptOf(path, "/deployments"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/deployments"):
		f.createDeployment(w, r, scriptOf(path, "/deployments"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/assets-upload-session"):
		f.assetSession(w, r, scriptOf(path, "/assets-upload-session"))
	case r.Method == http.MethodPut && strings.HasPrefix(path, acct+"/workers/scripts/") && strings.Count(path, "/") == 5:
		f.putScript(w, r, strings.TrimPrefix(path, acct+"/workers/scripts/"))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/schedules"):
		name := scriptOf(path, "/schedules")
		if _, ok := f.scripts[name]; !ok {
			f.fail(w, "schedules of unknown script %s", name)
			return
		}
		list := []map[string]string{}
		for _, cron := range f.crons[name] {
			list = append(list, map[string]string{"cron": cron, "created_on": "2026-10-01T00:00:00Z", "modified_on": "2026-10-01T00:00:00Z"})
		}
		writeCFResult(w, http.StatusOK, map[string]any{"schedules": list})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/schedules"):
		name := scriptOf(path, "/schedules")
		var body []struct {
			Cron string `json:"cron"`
		}
		if err := decodeStrict(r.Body, &body); err != nil || body == nil {
			f.fail(w, "schedules body must be a JSON array of {cron}: %v", err)
			return
		}
		if _, ok := f.scripts[name]; !ok {
			f.fail(w, "schedules of unknown script %s", name)
			return
		}
		f.crons[name] = nil
		for _, entry := range body {
			if entry.Cron == "" {
				f.fail(w, "empty cron")
				return
			}
			f.crons[name] = append(f.crons[name], entry.Cron)
		}
		writeCFResult(w, http.StatusOK, map[string]any{"schedules": body})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/subdomain"):
		var body struct {
			Enabled         *bool `json:"enabled"`
			PreviewsEnabled *bool `json:"previews_enabled"`
		}
		name := scriptOf(path, "/subdomain")
		if _, ok := f.scripts[name]; !ok {
			f.fail(w, "subdomain of unknown script %s", name)
			return
		}
		if err := decodeStrict(r.Body, &body); err != nil || body.Enabled == nil || *body.Enabled || (body.PreviewsEnabled != nil && *body.PreviewsEnabled) {
			f.fail(w, "subdomain body must disable workers.dev and previews: %v", err)
			return
		}
		f.subdomainOff[name] = true
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
		f.domainService = body.Service
		writeCFResult(w, http.StatusOK, map[string]any{"id": "domain-id", "hostname": body.Hostname, "service": body.Service})
	default:
		f.t.Errorf("unexpected Cloudflare request: %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

func (f *fakeCloudflare) listDeployments(w http.ResponseWriter, name string) {
	if _, ok := f.scripts[name]; !ok {
		f.fail(w, "deployments of unknown script %s", name)
		return
	}
	type deployment struct {
		ID        string              `json:"id"`
		Source    string              `json:"source"`
		CreatedOn string              `json:"created_on"`
		Versions  []deploymentVersion `json:"versions"`
	}
	list := []deployment{}
	all := f.deployments[name]
	for i := len(all) - 1; i >= 0; i-- { // newest first, as the API sorts them
		list = append(list, deployment{ID: fmt.Sprintf("%s-deployment-%d", name, i+1), Source: "api", CreatedOn: "2026-10-01T00:00:00Z", Versions: all[i]})
	}
	writeCFResult(w, http.StatusOK, map[string]any{"deployments": list})
}

// createDeployment is POST .../deployments: it needs force=true (the deployment of an older version may restore older
// secrets, which Cloudflare refuses otherwise) and versions the script has.
func (f *fakeCloudflare) createDeployment(w http.ResponseWriter, r *http.Request, name string) {
	if r.URL.Query().Get("force") != "true" {
		f.fail(w, "a rollback must pass force=true")
		return
	}
	var body struct {
		Strategy string              `json:"strategy"`
		Versions []deploymentVersion `json:"versions"`
	}
	if err := decodeStrict(r.Body, &body); err != nil || body.Strategy != "percentage" || len(body.Versions) == 0 {
		f.fail(w, "deployment body takes strategy percentage and versions: %v", err)
		return
	}
	total := 0.0
	for _, v := range body.Versions {
		known := false
		for _, id := range f.versions[name] {
			known = known || id == v.VersionID
		}
		if !known {
			f.fail(w, "script %s has no version %s", name, v.VersionID)
			return
		}
		total += v.Percentage
	}
	if total != 100 {
		f.fail(w, "percentages add up to %v, not 100", total)
		return
	}
	f.deployments[name] = append(f.deployments[name], body.Versions)
	f.deploymentPosts = append(f.deploymentPosts, name)
	writeCFResult(w, http.StatusOK, map[string]any{"id": name + "-deployment", "versions": body.Versions})
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
	// The modules are <role>.js and, for the five Go roles, <role>.wasm: a missing module is an error, so is an extra one,
	// and so is the module of another role.
	role := "front"
	if name != "mistgate-edge" {
		role = strings.TrimPrefix(name, "mistgate-edge-")
	}
	want := map[string]string{role + ".js": "application/javascript+module"}
	if role != "front" {
		want[role+".wasm"] = "application/wasm"
	}
	if meta.MainModule != role+".js" {
		f.fail(w, "script %s: main module %q", name, meta.MainModule)
		return
	}
	parts := r.MultipartForm.File
	if len(parts) != len(want) {
		f.fail(w, "script %s: %d module parts, want %d", name, len(parts), len(want))
		return
	}
	for module, contentType := range want {
		files := parts[module]
		if len(files) == 0 {
			f.fail(w, "script %s: module %s is not in the upload", name, module)
			return
		}
		if got := files[0].Header.Get("Content-Type"); got != contentType {
			f.fail(w, "module %s content type %q, want %q", module, got, contentType)
			return
		}
		fh, _ := files[0].Open()
		content, _ := io.ReadAll(fh)
		fh.Close()
		if !strings.HasPrefix(string(content), "module "+module) {
			f.fail(w, "module %s has the content of another file: %q", module, content)
			return
		}
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
		case "service":
			target, _ := b["service"].(string)
			if _, exists := f.scripts[target]; !exists || target == name {
				f.fail(w, "script %s binds service %s to script %q, which does not exist yet", name, bname, target)
				return
			}
			for key := range b {
				if key != "type" && key != "name" && key != "service" && key != "entrypoint" {
					f.fail(w, "service binding %s has the field %s", bname, key)
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
	// Every upload is a new version and, with no deployment given, a deployment of that version at 100%.
	version := fmt.Sprintf("%s@v%d", name, len(f.versions[name])+1)
	f.versions[name] = append(f.versions[name], version)
	f.deployments[name] = append(f.deployments[name], []deploymentVersion{{VersionID: version, Percentage: 100}})
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

// snapshot is what the fake holds after a deploy, without what legitimately differs between a clean run and a run that was
// stopped and resumed: the number of versions a script has, the one-off assets token and the migrations of the last upload.
func (f *fakeCloudflare) snapshot(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	names := make([]string, 0, len(f.scripts))
	for name := range f.scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "script %s tag=%q cron=%q workers.dev-off=%v\n", name, f.scripts[name], f.crons[name], f.subdomainOff[name])
		if uploads := f.metadatas[name]; len(uploads) != 0 {
			var meta map[string]any
			if err := json.Unmarshal([]byte(uploads[len(uploads)-1]), &meta); err != nil {
				t.Fatal(err)
			}
			delete(meta, "migrations")
			if assets, ok := meta["assets"].(map[string]any); ok {
				assets["jwt"] = "-"
			}
			last, _ := json.Marshal(meta)
			fmt.Fprintf(&b, "  last upload %s\n", last)
		}
	}
	fmt.Fprintf(&b, "domain -> %s\n", f.domainService)
	for _, db := range f.databases {
		fmt.Fprintf(&b, "d1 %s %s\n", db.UUID, db.Name)
	}
	return b.String()
}

// makeTestBundle writes a signed bundle of the six scripts. Every module is "module <file name>"; override gives other
// content to a file, to make a script differ from the same script of another version.
func makeTestBundle(t *testing.T, dir, version string, now time.Time, priv ed25519.PrivateKey, signed bool, expires time.Time, override map[string]string) {
	t.Helper()
	moduleDir := filepath.Join(dir, "modules")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, script := range release.NewEdgeConfig(version).Scripts {
		for _, m := range script.Modules {
			content := "module " + m.Name + override[m.Name]
			if err := os.WriteFile(filepath.Join(moduleDir, m.Name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
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
		Version: version, ModulesDir: moduleDir, AssetsDir: assetDir,
	}); err != nil {
		t.Fatal(err)
	}
	signBundle(t, dir, version, now, priv, signed, expires, tarball.Bytes())
}

// signBundle writes the tarball with its manifest and, if signed, the signature.
func signBundle(t *testing.T, dir, version string, now time.Time, priv ed25519.PrivateKey, signed bool, expires time.Time, tarball []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, release.EdgeBundleName), tarball, 0o644); err != nil {
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
	makeTestBundle(t, bundleDir, bundleVersion, testNow, priv, true, testNow.Add(time.Hour), nil)
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

func newBundleDir(t *testing.T, priv ed25519.PrivateKey, version string, override map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeTestBundle(t, dir, version, testNow, priv, true, testNow.Add(time.Hour), override)
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
	reqUpload   = "POST /client/v4/accounts/acct-id/workers/assets/upload?base64=true"
	reqDomain   = "PUT /client/v4/accounts/acct-id/workers/domains"
	reqDecoy    = "GET /"

	front, link, provision, public, admin, jobs = "mistgate-edge", "mistgate-edge-link", "mistgate-edge-provision", "mistgate-edge-public", "mistgate-edge-admin", "mistgate-edge-jobs"
)

func scriptReq(method, name, suffix string) string {
	return method + " /client/v4/accounts/acct-id/workers/scripts/" + name + suffix
}
func reqPut(name string) string       { return scriptReq("PUT", name, "") }
func reqSub(name string) string       { return scriptReq("POST", name, "/subdomain") }
func reqCron(name string) string      { return scriptReq("PUT", name, "/schedules") }
func reqSession(name string) string   { return scriptReq("POST", name, "/assets-upload-session") }
func reqDeployed(name string) string  { return scriptReq("GET", name, "/deployments") }
func reqSchedules(name string) string { return scriptReq("GET", name, "/schedules") }
func reqRestore(name string) string   { return scriptReq("POST", name, "/deployments?force=true") }

// previousRequests are the reads of an update: the deployment and the schedule of each of the six scripts, in upload order.
func previousRequests() []string {
	var reqs []string
	for _, name := range []string{link, provision, public, admin, jobs, front} {
		reqs = append(reqs, reqDeployed(name), reqSchedules(name))
	}
	return reqs
}

// bindingLines renders the bindings of one PUT, in the order they were sent, and returns the text of the secrets and vars.
func bindingLines(t *testing.T, raw string) (lines []string, values map[string]string) {
	t.Helper()
	var meta struct {
		Bindings []map[string]any `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	values = map[string]string{}
	for _, b := range meta.Bindings {
		name := b["name"].(string)
		switch b["type"] {
		case "d1":
			lines = append(lines, "d1 "+name)
		case "durable_object_namespace":
			line := fmt.Sprintf("do %s %s", name, b["class_name"])
			if script, ok := b["script_name"]; ok {
				line += " @" + script.(string)
			}
			lines = append(lines, line)
		case "service":
			line := fmt.Sprintf("service %s -> %s", name, b["service"])
			if entry, ok := b["entrypoint"]; ok {
				line += "#" + entry.(string)
			}
			lines = append(lines, line)
		case "assets":
			lines = append(lines, "assets "+name)
		case "secret_text":
			lines = append(lines, "secret "+name)
			values[name] = b["text"].(string)
		case "plain_text":
			lines = append(lines, "var "+name)
			values[name] = b["text"].(string)
		default:
			t.Fatalf("binding %v", b)
		}
	}
	return lines, values
}

func migrationsOf(t *testing.T, raw string) string {
	t.Helper()
	var meta struct {
		Migrations json.RawMessage `json:"migrations"`
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	return string(meta.Migrations)
}

var limiterAndNodeLink = []string{"do LIMITER Limiter @mistgate-edge", "do NODELINK NodeLink @mistgate-edge"}

// wantBindings are the bindings of the final upload of each script (the table of the TypeScript side: what each role's code
// reads from env), written out by hand, not derived from edge.json.
var wantBindings = map[string][]string{
	front: {
		"d1 DB", "do LIMITER Limiter", "do NODELINK NodeLink",
		"service PUBLIC -> mistgate-edge-public", "service ADMIN -> mistgate-edge-admin", "service LINK -> mistgate-edge-link", "service PANEL_LINK -> mistgate-edge-link#PanelLink",
	},
	link:      append(append([]string{"d1 DB"}, limiterAndNodeLink...), "secret MASTER_KEY"),
	provision: {"d1 DB", "do PROVISIONER Provisioner", "do LIMITER Limiter @mistgate-edge", "do NODELINK NodeLink @mistgate-edge", "secret MASTER_KEY"},
	public:    append(append([]string{"d1 DB"}, limiterAndNodeLink...), "assets ASSETS", "secret MASTER_KEY"),
	admin: append(append([]string{"d1 DB"}, limiterAndNodeLink...), "do PROVISIONER Provisioner @mistgate-edge-provision", "assets ASSETS",
		"secret MASTER_KEY", "secret ADMIN_PREFIX", "secret SUB_PREFIX", "secret SETUP_TOKEN_SHA256", "var PUBLIC_URL"),
	jobs: {"d1 DB", "do NODELINK NodeLink @mistgate-edge", "do PROVISIONER Provisioner @mistgate-edge-provision", "secret MASTER_KEY"},
}

func TestDeployFreshInstall(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	out := mustDeploy(t, opts)
	// link binds FRONT's Durable Objects and FRONT binds the other scripts: FRONT goes up first, with no services, and again last.
	requireList(t, "request order", fake.requests, []string{
		reqAccounts, reqZone1, reqZone2, reqScripts, reqD1List, reqD1Create,
		reqPut(front), reqSub(front),
		reqPut(link), reqSub(link),
		reqPut(provision), reqSub(provision),
		reqSession(public), reqUpload, reqUpload, reqPut(public), reqSub(public),
		reqSession(admin), reqPut(admin), reqSub(admin),
		reqPut(jobs), reqSub(jobs), reqCron(jobs),
		reqPut(front), reqSub(front),
		reqDomain, reqDecoy,
	})
	state := mustState(t, opts.StatePath)
	if state.DatabaseID != "d1-id" || state.Schema != 2 {
		t.Fatalf("state: %+v", state)
	}

	// the bindings of each PUT
	for name, want := range wantBindings {
		uploads := fake.metadatas[name]
		wantUploads := 1
		if name == front {
			wantUploads = 2
		}
		if len(uploads) != wantUploads {
			t.Fatalf("%s uploaded %d times, want %d", name, len(uploads), wantUploads)
		}
		got, values := bindingLines(t, uploads[len(uploads)-1])
		requireList(t, name+" bindings", got, want)
		// the secrets: MASTER_KEY on the five Go scripts; the setup and admin values on admin only
		if name != front && values["MASTER_KEY"] != state.MasterKey {
			t.Errorf("%s: MASTER_KEY is not the key of the key file", name)
		}
		setupHash := sha256.Sum256([]byte(state.SetupToken))
		wantValues := map[string]string{}
		if name != front {
			wantValues["MASTER_KEY"] = state.MasterKey
		}
		if name == admin {
			wantValues["ADMIN_PREFIX"], wantValues["SUB_PREFIX"] = state.AdminPrefix, state.SubPrefix
			wantValues["SETUP_TOKEN_SHA256"] = hex.EncodeToString(setupHash[:])
			wantValues["PUBLIC_URL"] = "https://panel.example.com"
		}
		if !reflect.DeepEqual(values, wantValues) {
			t.Errorf("%s secrets and vars: %v, want %v", name, values, wantValues)
		}
	}
	first, _ := bindingLines(t, fake.metadatas[front][0])
	requireList(t, "first FRONT bindings (no services)", first, wantBindings[front][:3])

	// migrations: FRONT's two steps on its first upload only, the Provisioner's one
	allSteps := `{"new_tag":"v2","steps":[{"new_sqlite_classes":["Limiter"]},{"new_sqlite_classes":["NodeLink"]}]}`
	if got := migrationsOf(t, fake.metadatas[front][0]); got != allSteps {
		t.Errorf("first FRONT migrations %s", got)
	}
	if got := migrationsOf(t, fake.metadatas[front][1]); got != "" {
		t.Errorf("final FRONT upload carries migrations %s", got)
	}
	if got := migrationsOf(t, fake.metadatas[provision][0]); got != `{"new_tag":"v1","new_sqlite_classes":["Provisioner"]}` {
		t.Errorf("provision migrations %s", got)
	}
	for _, name := range []string{link, public, admin, jobs} {
		if got := migrationsOf(t, fake.metadatas[name][0]); got != "" {
			t.Errorf("%s migrations %s", name, got)
		}
	}

	// assets on public and admin only, each with a token issued for that script; cron on jobs only; workers.dev off on all six
	if len(fake.putJWTs) != 2 || len(fake.putJWTs[public]) != 1 || len(fake.putJWTs[admin]) != 1 || fake.assetUploads != 2 {
		t.Errorf("assets: tokens %v, bucket uploads %d", fake.putJWTs, fake.assetUploads)
	}
	if want := map[string][]string{jobs: {"* * * * *"}}; !reflect.DeepEqual(fake.crons, want) {
		t.Errorf("crons %v, want %v", fake.crons, want)
	}
	if len(fake.subdomainOff) != 6 {
		t.Errorf("workers.dev is off on %v, want all six", fake.subdomainOff)
	}
	if fake.domainService != front {
		t.Errorf("the domain belongs to %q, want FRONT %q", fake.domainService, front)
	}
	if want := []string{"link", "provision", "public", "admin", "jobs", "front", "domain"}; !reflect.DeepEqual(state.Steps, want) {
		t.Errorf("steps %v, want %v", state.Steps, want)
	}
	for role, script := range state.Scripts {
		if script.Fingerprint == "" || script.Previous != nil || script.Name != release.EdgeScriptName("mistgate-edge", role) {
			t.Errorf("script state %s: %+v", role, script)
		}
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
	if strings.Contains(string(raw), testCFToken) || strings.Contains(string(raw), "completion-") {
		t.Fatal("a token reached the state file")
	}
}

// A release that leaves a script alone does not upload it: FRONT is the one that matters, because uploading it restarts the
// NodeLink objects and every agent reconnects.
func TestDeployUpdateSkipsTheScriptsThatDidNotChange(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	before := mustState(t, opts.StatePath)
	activeBefore := map[string]string{}
	for _, name := range []string{link, provision, public, admin, jobs, front} {
		activeBefore[name] = fake.active(name)
	}
	fake.requests, fake.metadatas, fake.putJWTs, fake.assetUploads = nil, make(map[string][]string), make(map[string][]string), 0
	// only link's wasm differs; the setup token is new on every run without --resume, so admin is uploaded too
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"link.wasm": " changed"})
	mustDeploy(t, opts)
	requireList(t, "update request order", fake.requests, append(append([]string{reqAccounts, reqZone1, reqZone2, reqScripts, reqD1List}, previousRequests()...), []string{
		reqPut(link), reqSub(link),
		reqSub(provision),
		reqSub(public),
		reqSession(admin), reqPut(admin), reqSub(admin),
		reqSub(jobs), reqCron(jobs),
		reqSub(front),
		reqDomain, reqDecoy,
	}...))
	for _, name := range []string{provision, public, jobs, front} {
		if fake.active(name) != activeBefore[name] {
			t.Errorf("%s was uploaded although nothing changed", name)
		}
	}
	for _, name := range []string{link, admin} {
		if fake.active(name) == activeBefore[name] {
			t.Errorf("%s was not uploaded", name)
		}
	}
	if fake.assetUploads != 0 {
		t.Errorf("the assets were known to Cloudflare already, but %d buckets were uploaded", fake.assetUploads)
	}
	after := mustState(t, opts.StatePath)
	if after.MasterKey != before.MasterKey || after.AdminPrefix != before.AdminPrefix || after.SubPrefix != before.SubPrefix || after.DatabaseID != before.DatabaseID {
		t.Fatalf("update changed the saved keys or resources: before=%+v after=%+v", before, after)
	}
	if after.SetupToken == before.SetupToken {
		t.Fatal("a run without --resume must mint a new setup token")
	}
	if after.BundleVersion != "v1.2.4" || after.BundleSHA256 == before.BundleSHA256 {
		t.Fatalf("local state did not record the bundle: %q", after.BundleVersion)
	}
	for role, script := range after.Scripts {
		if script.Previous == nil || script.Previous.Versions[0].VersionID != activeBefore[script.Name] {
			t.Errorf("%s: the version before the update was not recorded: %+v", role, script.Previous)
		}
		if script.Fingerprint == "" {
			t.Errorf("%s has no fingerprint", role)
		}
	}
	if got := after.Scripts[release.RoleJobs].Previous.Crons; !reflect.DeepEqual(got, []string{"* * * * *"}) {
		t.Errorf("recorded schedule of jobs %v", got)
	}
	if got := after.Scripts[release.RoleLink].Previous.Crons; len(got) != 0 {
		t.Errorf("recorded schedule of link %v", got)
	}
	// FRONT and the scripts that were not uploaded keep their fingerprint
	if after.Scripts[release.RoleFront].Fingerprint != before.Scripts[release.RoleFront].Fingerprint {
		t.Error("FRONT's fingerprint changed although its upload did not")
	}
	if after.Scripts[release.RoleLink].Fingerprint == before.Scripts[release.RoleLink].Fingerprint {
		t.Error("link's fingerprint did not change with its wasm")
	}
}

// A change to FRONT is uploaded; so is a changed secret of a script that the bundle did not touch.
func TestDeployUploadsWhatChanged(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	fake.requests = nil
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"front.js": " changed"})
	mustDeploy(t, opts)
	requireList(t, "uploads", scriptPuts(fake.requests), []string{admin, front})
	// the final upload of FRONT is a plain update: no migrations, and it has its services
	last := fake.metadatas[front][len(fake.metadatas[front])-1]
	if got, _ := bindingLines(t, last); !reflect.DeepEqual(got, wantBindings[front]) {
		t.Errorf("FRONT bindings %v", got)
	}
	if got := migrationsOf(t, last); got != "" {
		t.Errorf("FRONT migrations %s", got)
	}

	// a new master key in the key file is a changed secret on every Go script, though no module changed
	state := mustState(t, opts.StatePath)
	state.MasterKey = strings.Repeat("ab", 32)
	if err := replaceStateFile(opts.StatePath, state); err != nil {
		t.Fatal(err)
	}
	fake.requests = nil
	mustDeploy(t, opts)
	requireList(t, "uploads after a new master key", scriptPuts(fake.requests), []string{link, provision, public, admin, jobs})
}

func TestDeployResumeKeepsTheSetupToken(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	before := mustState(t, opts.StatePath)
	fake.requests = nil
	opts.Resume = true
	out := mustDeploy(t, opts)
	if after := mustState(t, opts.StatePath); !reflect.DeepEqual(after, before) {
		t.Fatalf("--resume changed the key file: before=%+v after=%+v", before, after)
	}
	for _, req := range fake.requests {
		if req == reqD1Create {
			t.Fatal("resume created a second D1 database")
		}
	}
	if puts := scriptPuts(fake.requests); len(puts) != 0 {
		t.Fatalf("resume uploaded %q although every script was uploaded already", puts)
	}
	if !strings.Contains(out, "Resuming; steps of the stopped run that finished: link, provision, public, admin, jobs, front, domain") {
		t.Fatalf("resume output: %s", out)
	}
}

// Each of the state-changing calls of a deploy fails once, and --resume finishes with the same Cloudflare and the same key
// file as a run that did not stop.
func TestDeployFailsAtEveryStepAndResumeFinishesLikeACleanRun(t *testing.T) {
	type outcome struct {
		cloudflare string
		state      localState
		mutations  int
	}
	run := func(t *testing.T, update bool, failAt int) outcome {
		t.Helper()
		fake, opts, priv := setupDeploy(t, "v1.2.3")
		if update {
			mustDeploy(t, opts)
			opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"front.js": " changed", "jobs.wasm": " changed"})
			fake.mutations = 0
		}
		fake.failNth = failAt
		err := Deploy(context.Background(), opts, io.Discard)
		if failAt != 0 {
			if err == nil {
				t.Fatalf("the injected failure at call %d did not stop the deploy", failAt)
			}
			fake.failNth = 0
			opts.Resume = true
			if err = Deploy(context.Background(), opts, io.Discard); err != nil {
				t.Fatalf("resume after a failure at call %d: %v", failAt, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		return outcome{fake.snapshot(t), mustState(t, opts.StatePath), fake.mutations}
	}
	for _, update := range []bool{false, true} {
		name := map[bool]string{false: "fresh install", true: "update"}[update]
		reference := run(t, update, 0)
		for call := 1; call <= reference.mutations; call++ {
			t.Run(fmt.Sprintf("%s, call %d of %d fails", name, call, reference.mutations), func(t *testing.T) {
				got := run(t, update, call)
				if got.cloudflare != reference.cloudflare {
					t.Errorf("Cloudflare after the resume differs from a clean run:\n got %s\nwant %s", got.cloudflare, reference.cloudflare)
				}
				if !reflect.DeepEqual(got.state, reference.state) {
					t.Errorf("key file after the resume differs from a clean run:\n got %+v\nwant %+v", got.state, reference.state)
				}
			})
		}
	}
}

func TestDeployFirstPUTFailureKeepsKeyFileAndResumeFinishes(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.failScript, fake.echoToken = front, true
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
	requireList(t, "resume script order", scriptPuts(fake.requests), []string{front, link, provision, public, admin, jobs, front})
}

// The scripts that were uploaded before the failure are not uploaded again.
func TestDeployResumeSkipsTheScriptsThatAreDone(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	fake.failScript = admin
	if err := Deploy(context.Background(), opts, io.Discard); err == nil {
		t.Fatal("accepted the failed admin upload")
	}
	if want := []string{"link", "provision", "public"}; !reflect.DeepEqual(mustState(t, opts.StatePath).Steps, want) {
		t.Fatalf("steps after the failure: %v", mustState(t, opts.StatePath).Steps)
	}
	fake.failScript, fake.requests = "", nil
	opts.Resume = true
	mustDeploy(t, opts)
	requireList(t, "resume script order", scriptPuts(fake.requests), []string{admin, jobs, front})
}

// FRONT is gone while the others stay (deleted by hand, or the first FRONT never made it): it goes up without its services
// first, as on a fresh install, because link binds its Durable Objects.
func TestDeployFrontMissingOthersPresent(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	delete(fake.scripts, front)
	fake.requests = nil
	fake.metadatas = make(map[string][]string)
	opts.Resume = true
	mustDeploy(t, opts)
	requireList(t, "script order", scriptPuts(fake.requests), []string{front, front})
	allSteps := `{"new_tag":"v2","steps":[{"new_sqlite_classes":["Limiter"]},{"new_sqlite_classes":["NodeLink"]}]}`
	first, last := fake.metadatas[front][0], fake.metadatas[front][1]
	if got, _ := bindingLines(t, first); !reflect.DeepEqual(got, wantBindings[front][:3]) || migrationsOf(t, first) != allSteps {
		t.Errorf("recreated FRONT: %v %s", got, migrationsOf(t, first))
	}
	if got, _ := bindingLines(t, last); !reflect.DeepEqual(got, wantBindings[front]) || migrationsOf(t, last) != "" {
		t.Errorf("final FRONT: %v %s", got, migrationsOf(t, last))
	}
}

// The Provisioner is gone while the others stay: it goes up with its migration, and so do the scripts that bind it.
func TestDeployProvisionMissingOthersPresent(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	delete(fake.scripts, provision)
	delete(fake.versions, provision)
	delete(fake.deployments, provision)
	fake.requests = nil
	fake.metadatas = make(map[string][]string)
	opts.Resume = true
	mustDeploy(t, opts)
	requireList(t, "script order", scriptPuts(fake.requests), []string{provision})
	if got := migrationsOf(t, fake.metadatas[provision][0]); got != `{"new_tag":"v1","new_sqlite_classes":["Provisioner"]}` {
		t.Errorf("provision migrations %s", got)
	}
}

func TestDeployAppliesOnlyTheMigrationsAfterTheDeployedTag(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	fake.scripts[front] = "v1" // an older edge: only NodeLink is new
	fake.metadatas = make(map[string][]string)
	mustDeploy(t, opts)
	if want := `{"old_tag":"v1","new_tag":"v2","new_sqlite_classes":["NodeLink"]}`; migrationsOf(t, fake.metadatas[front][0]) != want {
		t.Fatalf("migrations = %s, want %s", migrationsOf(t, fake.metadatas[front][0]), want)
	}
	if fake.scripts[front] != "v2" {
		t.Fatalf("FRONT tag after update: %q", fake.scripts[front])
	}
}

func TestDeployRefusesABundleOlderThanTheDeployment(t *testing.T) {
	for _, name := range []string{front, provision} {
		t.Run(name, func(t *testing.T) {
			fake, opts, _ := setupDeploy(t, "v1.2.3")
			mustDeploy(t, opts)
			fake.scripts[name] = "v9"
			fake.requests = nil
			err := Deploy(context.Background(), opts, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), `"v9"`) || !strings.Contains(err.Error(), "older") || !strings.Contains(err.Error(), name) {
				t.Fatalf("older bundle error = %v", err)
			}
			for _, req := range fake.requests {
				if !strings.HasPrefix(req, "GET ") {
					t.Fatalf("%s before refusing", req)
				}
			}
		})
	}
}

// edge.json schema 1 is the single-Worker bundle: there is no install to move, so it is refused, with the reason.
func TestDeployRefusesASchemaOneBundle(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	dir := filepath.Join(t.TempDir(), "schema1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rewriteBundle(t, dir, "v1.2.3", priv, newBundleDir(t, priv, "v1.2.3", nil), func(files map[string][]byte) {
		files[release.EdgeConfigName] = []byte(`{"schema":1,"version":"v1.2.3","compatibility_date":"2026-10-01","compatibility_flags":[],"scripts":[]}`)
		files["worker.js"], files["provision.js"], files["panel.wasm"] = []byte("x"), []byte("x"), []byte("x")
	})
	opts.BundleDir = dir
	err := Deploy(context.Background(), opts, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "schema 1") || !strings.Contains(err.Error(), "single-Worker edition") {
		t.Fatalf("schema 1 bundle: %v", err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("requests before the refusal: %q", fake.requests)
	}
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Fatal("wrote a key file")
	}
}

// A bundle whose files are not the ones edge.json names is refused before anything is sent.
func TestDeployRefusesABundleThatDoesNotMatchItsConfig(t *testing.T) {
	_, opts, priv := setupDeploy(t, "v1.2.3")
	cases := map[string]struct {
		mutate func(map[string][]byte)
		want   string
	}{
		"a module is missing": {func(f map[string][]byte) { delete(f, "jobs.wasm") }, "jobs.wasm"},
		"an unnamed file":     {func(f map[string][]byte) { f["extra.js"] = []byte("x") }, "extra.js"},
		"a changed binding": {func(f map[string][]byte) {
			f[release.EdgeConfigName] = bytes.Replace(f[release.EdgeConfigName], []byte(`"PANEL_LINK"`), []byte(`"PANEL_LINK2"`), 1)
		}, "release contract"},
		"a script out of order": {func(f map[string][]byte) {
			f[release.EdgeConfigName] = bytes.Replace(f[release.EdgeConfigName], []byte(`"role": "link"`), []byte(`"role": "jobs"`), 1)
		}, "upload order"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "bad")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			rewriteBundle(t, dir, "v1.2.3", priv, newBundleDir(t, priv, "v1.2.3", nil), c.mutate)
			opts.BundleDir = dir
			if err := Deploy(context.Background(), opts, io.Discard); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}

// rewriteBundle reads the bundle in from, lets mutate change its files and writes (and signs) the result in dir.
func rewriteBundle(t *testing.T, dir, version string, priv ed25519.PrivateKey, from string, mutate func(map[string][]byte)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(from, release.EdgeBundleName))
	if err != nil {
		t.Fatal(err)
	}
	files, err := release.ReadEdgeBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mutate(files)
	var tarball bytes.Buffer
	zw := gzip.NewWriter(&tarball)
	tw := tar.NewWriter(zw)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	signBundle(t, dir, version, testNow, priv, true, testNow.Add(time.Hour), tarball.Bytes())
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

// A rerun with no --resume is an update: the bundle did not change, so nothing but the admin (new setup token) is uploaded.
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
	requireList(t, "uploads", scriptPuts(fake.requests), []string{admin})
	// the same bundle is not a new bundle: the versions recorded for a rollback stay
	if !reflect.DeepEqual(after.Scripts[release.RoleLink].Previous, before.Scripts[release.RoleLink].Previous) {
		t.Fatal("a rerun of the same bundle recorded new previous versions")
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
	// the calls of a fresh install, in order
	text := out.String()
	at := -1
	for _, want := range []string{
		"Plan for panel.example.com (edge bundle v1.2.3)",
		"POST d1/database: create mistgate-edge in weur",
		"PUT scripts/mistgate-edge (first create, without its service bindings): front.js; d1 DB, do LIMITER (own Limiter), do NODELINK (own NodeLink)",
		"POST scripts/mistgate-edge/subdomain",
		"PUT scripts/mistgate-edge-link: link.js, link.wasm; d1 DB, do LIMITER (Limiter of mistgate-edge), do NODELINK (NodeLink of mistgate-edge), secret MASTER_KEY",
		"PUT scripts/mistgate-edge-provision: provision.js, provision.wasm; d1 DB, do PROVISIONER (own Provisioner)",
		"POST scripts/mistgate-edge-public/assets-upload-session",
		"PUT scripts/mistgate-edge-public: public.js, public.wasm; d1 DB, do LIMITER (Limiter of mistgate-edge), do NODELINK (NodeLink of mistgate-edge), assets ASSETS, secret MASTER_KEY",
		"POST scripts/mistgate-edge-admin/assets-upload-session",
		"secret SETUP_TOKEN_SHA256, var PUBLIC_URL",
		"PUT scripts/mistgate-edge-jobs: jobs.js, jobs.wasm; d1 DB, do NODELINK (NodeLink of mistgate-edge), do PROVISIONER (Provisioner of mistgate-edge-provision), secret MASTER_KEY",
		"PUT scripts/mistgate-edge-jobs/schedules: * * * * *",
		"PUT scripts/mistgate-edge: front.js; d1 DB, do LIMITER (own Limiter), do NODELINK (own NodeLink), service PUBLIC -> mistgate-edge-public, service ADMIN -> mistgate-edge-admin, service LINK -> mistgate-edge-link, service PANEL_LINK -> mistgate-edge-link#PanelLink",
		"PUT workers/domains: panel.example.com -> mistgate-edge",
		"GET https://panel.example.com/ until it returns HTTP 200",
	} {
		next := strings.Index(text[at+1:], want)
		if next < 0 {
			t.Fatalf("the plan does not have %q after the previous line:\n%s", want, text)
		}
		at += 1 + next
	}
}

// A dry run on a deployment whose key file exists prints the plan with no secret value in it.
func TestDeployDryRunShowsNoSecret(t *testing.T) {
	fake, opts, _ := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	state := mustState(t, opts.StatePath)
	fake.requests = nil
	opts.DryRun = true
	var out bytes.Buffer
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatal(err)
	}
	setupHash := sha256.Sum256([]byte(state.SetupToken))
	for _, secret := range []string{testCFToken, state.MasterKey, state.AdminPrefix, state.SubPrefix, state.SetupToken, hex.EncodeToString(setupHash[:])} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("the plan shows a secret: %s", out.String())
		}
	}
	for _, req := range fake.requests {
		if !strings.HasPrefix(req, "GET ") {
			t.Fatalf("dry-run sent %s", req)
		}
	}
	if !strings.Contains(out.String(), "(skipped when it is unchanged since the last deploy)") || !strings.Contains(out.String(), "secret MASTER_KEY") {
		t.Fatalf("plan of an update: %s", out.String())
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
	t.Run("a script of the six without a key file is not replaced", func(t *testing.T) {
		for _, name := range []string{front, link, provision, public, admin, jobs} {
			fake, opts, _ := setupDeploy(t, "v1.2.3")
			fake.scripts[name] = ""
			if err := Deploy(context.Background(), opts, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "refusing to replace its secrets") {
				t.Fatalf("%s: existing script error = %v", name, err)
			}
			if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
				t.Fatalf("%s: wrote a key file", name)
			}
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
		fake.failScript = front // stop right after the D1 step
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

// --rollback: the scripts go back to what they were before the last bundle, FRONT first, and so do the schedules.
func TestRollback(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	fake.crons[jobs] = []string{"*/5 * * * *"} // the schedule before the update is not the one the bundle sets
	activeBefore := map[string]string{}
	for _, name := range []string{link, provision, public, admin, jobs, front} {
		activeBefore[name] = fake.active(name)
	}
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"front.js": " new", "link.wasm": " new", "jobs.wasm": " new"})
	mustDeploy(t, opts)
	if fake.active(front) == activeBefore[front] || !reflect.DeepEqual(fake.crons[jobs], []string{"* * * * *"}) {
		t.Fatal("the update did not change FRONT or the schedule")
	}
	afterUpdate := mustState(t, opts.StatePath)

	t.Run("a dry run changes nothing", func(t *testing.T) {
		fake.requests = nil
		dry := opts
		dry.BundleDir, dry.DryRun, dry.Rollback = "", true, true
		var out bytes.Buffer
		if err := Deploy(context.Background(), dry, &out); err != nil {
			t.Fatal(err)
		}
		for _, req := range fake.requests {
			if !strings.HasPrefix(req, "GET ") {
				t.Fatalf("dry-run rollback sent %s", req)
			}
		}
		if !reflect.DeepEqual(mustState(t, opts.StatePath), afterUpdate) || !strings.Contains(out.String(), "would be restored") {
			t.Fatalf("state changed or no plan: %s", out.String())
		}
	})

	fake.requests, fake.deploymentPosts = nil, nil
	opts.BundleDir, opts.Rollback = "", true
	var out bytes.Buffer
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out.String())
	}
	requireList(t, "rolled back in the reverse order", fake.deploymentPosts, []string{front, jobs, admin, public, provision, link})
	for name, version := range activeBefore {
		if fake.active(name) != version {
			t.Errorf("%s is on %s, want %s", name, fake.active(name), version)
		}
	}
	if !reflect.DeepEqual(fake.crons[jobs], []string{"*/5 * * * *"}) {
		t.Errorf("the schedule of jobs is %v, want the one before the update", fake.crons[jobs])
	}
	// jobs: the schedule is restored right after its version; the others had none and still have none
	var schedulePuts []string
	for _, req := range fake.requests {
		if strings.HasPrefix(req, "PUT ") && strings.HasSuffix(req, "/schedules") {
			schedulePuts = append(schedulePuts, req)
		}
	}
	requireList(t, "schedule restores", schedulePuts, []string{reqCron(jobs)})
	for _, req := range fake.requests {
		if (strings.HasPrefix(req, "PUT "+apiPrefix+acct+"/workers/scripts/") && !strings.HasSuffix(req, "/schedules")) || strings.Contains(req, "/d1/") {
			t.Errorf("a rollback uploads nothing and leaves D1 alone: %s", req)
		}
	}
	if !strings.Contains(out.String(), "D1 database was not touched") {
		t.Errorf("output: %s", out.String())
	}
	state := mustState(t, opts.StatePath)
	for role, script := range state.Scripts {
		if script.Fingerprint != "" {
			t.Errorf("%s keeps a fingerprint after the rollback: the next deploy would skip it", role)
		}
	}
	if state.MasterKey != afterUpdate.MasterKey || state.SetupToken != afterUpdate.SetupToken {
		t.Error("the rollback changed the keys")
	}

	// after a rollback the same bundle is uploaded in full again
	fake.requests = nil
	opts.Rollback = false
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"front.js": " new", "link.wasm": " new", "jobs.wasm": " new"})
	mustDeploy(t, opts)
	requireList(t, "deploy after a rollback", scriptPuts(fake.requests), []string{link, provision, public, admin, jobs, front})
	if fake.active(front) == activeBefore[front] || !reflect.DeepEqual(fake.crons[jobs], []string{"* * * * *"}) {
		t.Error("the deploy after the rollback did not move forward")
	}
}

func TestRollbackRefusals(t *testing.T) {
	t.Run("a first install has nothing to go back to", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		mustDeploy(t, opts)
		fake.requests = nil
		opts.Rollback, opts.BundleDir = true, ""
		err := Deploy(context.Background(), opts, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "nothing to roll back") {
			t.Fatalf("rollback of a first install: %v", err)
		}
		for _, req := range fake.requests {
			if !strings.HasPrefix(req, "GET ") {
				t.Fatalf("sent %s", req)
			}
		}
	})
	t.Run("no key file", func(t *testing.T) {
		fake, opts, _ := setupDeploy(t, "v1.2.3")
		opts.Rollback, opts.BundleDir = true, ""
		err := Deploy(context.Background(), opts, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "--rollback requires the local deployment file") || len(fake.requests) != 0 {
			t.Fatalf("rollback without a key file: %v, %q", err, fake.requests)
		}
	})
	t.Run("not with a bundle or --resume", func(t *testing.T) {
		_, opts, _ := setupDeploy(t, "v1.2.3")
		opts.Rollback = true
		if err := Deploy(context.Background(), opts, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be used with") {
			t.Fatalf("rollback with --bundle: %v", err)
		}
		opts.BundleDir, opts.Resume = "", true
		if err := Deploy(context.Background(), opts, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be used with") {
			t.Fatalf("rollback with --resume: %v", err)
		}
	})
	t.Run("a script that is gone", func(t *testing.T) {
		fake, opts, priv := setupDeploy(t, "v1.2.3")
		mustDeploy(t, opts)
		opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"front.js": " new"})
		mustDeploy(t, opts)
		delete(fake.scripts, jobs)
		opts.Rollback, opts.BundleDir = true, ""
		if err := Deploy(context.Background(), opts, io.Discard); err == nil || !strings.Contains(err.Error(), jobs) {
			t.Fatalf("rollback with a script missing: %v", err)
		}
	})
}

// A script the last bundle created has no earlier version: it stays, and the others go back.
func TestRollbackLeavesAScriptThatWasNew(t *testing.T) {
	fake, opts, priv := setupDeploy(t, "v1.2.3")
	mustDeploy(t, opts)
	delete(fake.scripts, jobs) // as if jobs had not been there before this update
	delete(fake.versions, jobs)
	delete(fake.deployments, jobs)
	delete(fake.crons, jobs)
	opts.BundleDir = newBundleDir(t, priv, "v1.2.4", map[string]string{"front.js": " new"})
	activeBefore := fake.active(front)
	mustDeploy(t, opts)
	opts.Rollback, opts.BundleDir = true, ""
	var out bytes.Buffer
	fake.deploymentPosts = nil
	if err := Deploy(context.Background(), opts, &out); err != nil {
		t.Fatal(err)
	}
	requireList(t, "rolled back", fake.deploymentPosts, []string{front, admin, public, provision, link})
	if fake.active(front) != activeBefore || !strings.Contains(out.String(), jobs+": no earlier version recorded") {
		t.Fatalf("front on %s; output %s", fake.active(front), out.String())
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
		makeTestBundle(t, dir, version, testNow, priv, signed, expires, nil)
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
	dir := newBundleDir(t, priv, "v1.2.3", nil)
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
		makeTestBundle(t, wrong, "v9.9.9", testNow, priv, true, testNow.Add(time.Hour), nil)
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
