package edgedeploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

const stateVersion = 1

var (
	// dnsLabelRe is a DNS label; a Worker script name has the same shape.
	dnsLabelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	prefixRe   = regexp.MustCompile(`^/[a-z2-7]{24}/$`)
	tokenRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// d1Locations are the primary_location_hint values of the D1 create call.
var d1Locations = map[string]bool{"wnam": true, "enam": true, "weur": true, "eeur": true, "apac": true, "oc": true}

type Options struct {
	Host       string
	Name       string
	D1Location string
	BundleDir  string
	// Version is the release tag to download (vX.Y.Z). DefaultVersion is used when neither Version nor BundleDir is given:
	// the tag of this binary's own release, empty for a development build (which then needs --version or --bundle).
	Version               string
	DefaultVersion        string
	Resume                bool
	DryRun                bool
	InsecureSkipSignature bool
	Token                 string
	StatePath             string
	PublicKey             ed25519.PublicKey
	APIBaseURL            string
	GitHubDownloadBaseURL string
	HTTPClient            *http.Client
	Now                   func() time.Time
	Random                io.Reader
	PollInterval          time.Duration
	PollAttempts          int
	Sleep                 func(context.Context, time.Duration) error
	DecoyURL              string
}

type localState struct {
	Schema        int    `json:"schema"`
	Host          string `json:"host"`
	Name          string `json:"name"`
	ProvisionName string `json:"provision_name"`
	AccountID     string `json:"account_id"`
	ZoneID        string `json:"zone_id"`
	DatabaseID    string `json:"database_id"` // "" until the D1 database exists
	DatabaseName  string `json:"database_name"`
	PublicURL     string `json:"public_url"`
	AdminPrefix   string `json:"admin_prefix"`
	SubPrefix     string `json:"sub_prefix"`
	MasterKey     string `json:"master_key"`
	SetupToken    string `json:"setup_token"`
	BundleVersion string `json:"bundle_version"`
	BundleSHA256  string `json:"bundle_sha256"`
}

type assetManifestEntry struct {
	Hash string `json:"hash"`
	Size int    `json:"size"`
}

type assetContent struct {
	base64      string
	contentType string
}

type scriptMetadata struct {
	MainModule         string               `json:"main_module"`
	CompatibilityDate  string               `json:"compatibility_date"`
	CompatibilityFlags []string             `json:"compatibility_flags"`
	Bindings           []workerBinding      `json:"bindings"`
	Migrations         *migrationRequest    `json:"migrations,omitempty"`
	Modules            []release.EdgeModule `json:"-"`
	Assets             *assetMetadata       `json:"assets,omitempty"`
	Observability      observability        `json:"observability"`
}

// migrationRequest is the API's migrations object: one step is {new_tag, new_sqlite_classes}, several are {new_tag, steps}.
// OldTag is the tag the script is at now; Cloudflare rejects the upload if it differs.
type migrationRequest struct {
	OldTag           string          `json:"old_tag,omitempty"`
	NewTag           string          `json:"new_tag"`
	NewSqliteClasses []string        `json:"new_sqlite_classes,omitempty"`
	Steps            []migrationStep `json:"steps,omitempty"`
}

type migrationStep struct {
	NewSqliteClasses []string `json:"new_sqlite_classes"`
}

type workerBinding struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	DatabaseID string  `json:"database_id,omitempty"`
	ClassName  string  `json:"class_name,omitempty"`
	ScriptName string  `json:"script_name,omitempty"`
	Text       *string `json:"text,omitempty"`
}

type assetMetadata struct {
	JWT    string        `json:"jwt"`
	Config assetSettings `json:"config"`
}

type assetSettings struct {
	RunWorkerFirst   bool   `json:"run_worker_first"`
	HTMLHandling     string `json:"html_handling"`
	NotFoundHandling string `json:"not_found_handling"`
}

type observability struct {
	Enabled bool `json:"enabled"`
}

// Deploy runs the edge deployment against Cloudflare's v4 API. Tests provide an httptest client and API base URL.
func Deploy(ctx context.Context, opts Options, out io.Writer) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = redactError(retErr, opts.Token)
		}
	}()
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintln(out, "Prerequisites: Cloudflare Workers Paid and a zone for this host on Cloudflare DNS.")
	if opts.Token == "" {
		return errors.New("CLOUDFLARE_API_TOKEN is required")
	}
	if opts.Host == "" {
		return errors.New("--host is required")
	}
	if err := validateHost(opts.Host); err != nil {
		return err
	}
	if opts.Name == "" {
		opts.Name = "mistgate-edge"
	}
	if !dnsLabelRe.MatchString(opts.Name) || len(opts.Name)+len("-provision") > 63 {
		return errors.New("--name must be a lowercase DNS-style Worker name of at most 53 characters")
	}
	if opts.D1Location == "" {
		opts.D1Location = "weur"
	}
	if !d1Locations[opts.D1Location] {
		return errors.New("--d1-location must be one of wnam, enam, weur, eeur, apac, oc")
	}
	if opts.BundleDir != "" && opts.Version != "" {
		return errors.New("--bundle and --version cannot be used together")
	}
	if opts.InsecureSkipSignature && opts.BundleDir == "" {
		return errors.New("--insecure-skip-signature is only available with --bundle DIR")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = newHTTPClient()
	}
	if opts.PollAttempts <= 0 {
		opts.PollAttempts = 60
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.Sleep == nil {
		opts.Sleep = waitContext
	}
	if opts.StatePath == "" {
		opts.StatePath = "mistgate-edge-" + opts.Name + ".json"
	}

	state, hasState, err := loadExistingState(opts.StatePath)
	if err != nil {
		return err
	}
	if opts.Resume && !hasState {
		return fmt.Errorf("--resume requires the local deployment file %s", opts.StatePath)
	}
	if hasState {
		if err := validateState(state); err != nil {
			return fmt.Errorf("local deployment file: %w", err)
		}
		if state.Host != strings.ToLower(opts.Host) || state.Name != opts.Name {
			return errors.New("local deployment file belongs to a different host or Worker name")
		}
	}
	if opts.BundleDir == "" && opts.Version == "" {
		switch {
		case opts.Resume:
			opts.Version = state.BundleVersion
		case opts.DefaultVersion != "":
			opts.Version = opts.DefaultVersion
		default:
			return errors.New("this is a development build with no release version: pass --version vX.Y.Z or --bundle DIR")
		}
	}

	bundle, err := readBundle(ctx, opts, opts.HTTPClient, opts.Now())
	if err != nil {
		return err
	}
	if opts.Resume && state.BundleSHA256 != bundle.manifest.SHA256 {
		return errors.New("resumed edge bundle differs from the bundle recorded in the local deployment file")
	}

	// The first account request is also the token check: a bad token fails here with a clear message.
	api := newCFClient(opts.APIBaseURL, opts.Token, opts.HTTPClient)
	accounts, err := api.accounts(ctx)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return errors.New("Cloudflare API token cannot access an account")
	}
	accountID, foundZone, err := findZone(ctx, api, strings.ToLower(opts.Host), accounts)
	if err != nil {
		return err
	}
	if hasState && (state.AccountID != accountID || state.ZoneID != foundZone.ID) {
		return errors.New("the Cloudflare account or zone for this host changed since the local deployment file was created")
	}

	provisionName := opts.Name + "-provision"
	tags, err := api.scripts(ctx, accountID)
	if err != nil {
		return err
	}
	_, mainExists := tags[opts.Name]
	_, provisionExists := tags[provisionName]
	if !hasState && (mainExists || provisionExists) {
		return errors.New("a Worker script with this name already exists but its local key file is missing; refusing to replace its secrets")
	}

	databases, err := api.databases(ctx, accountID, opts.Name)
	if err != nil {
		return err
	}
	switch {
	case !hasState && len(databases) != 0:
		return errors.New("a D1 database with this name already exists but its local key file is missing; refusing to adopt it")
	case hasState && state.DatabaseID != "":
		found := false
		for _, existing := range databases {
			found = found || existing.UUID == state.DatabaseID
		}
		if !found {
			return errors.New("the D1 database in the local deployment file no longer exists; refusing to replace it automatically")
		}
	case hasState && len(databases) > 1:
		return fmt.Errorf("more than one D1 database is named %s; remove the extra ones or set database_id in the local deployment file", opts.Name)
	}

	mainScript, provisionScript := bundle.config.Scripts[1], bundle.config.Scripts[0]
	// A bundle older than what is deployed is refused before anything is uploaded.
	for name, script := range map[string]release.EdgeScript{opts.Name: mainScript, provisionName: provisionScript} {
		if _, err := planMigration(script.Migrations, tags[name]); err != nil {
			return fmt.Errorf("script %s: %w", name, err)
		}
	}
	if opts.DryRun {
		printPlan(out, opts, bundle, databases, mainExists)
		return nil
	}

	// The keys reach the local file before anything is created or uploaded; a run without --resume mints a new setup token.
	if !hasState {
		state, err = newLocalState(opts, accountID, foundZone.ID, bundle, provisionName)
		if err != nil {
			return err
		}
		if err := createStateFile(opts.StatePath, state); err != nil {
			return fmt.Errorf("write local deployment file before creating anything: %w", err)
		}
	} else {
		changed := state.BundleVersion != bundle.manifest.Version || state.BundleSHA256 != bundle.manifest.SHA256
		state.BundleVersion, state.BundleSHA256 = bundle.manifest.Version, bundle.manifest.SHA256
		if !opts.Resume {
			if state.SetupToken, err = newSetupToken(opts.Random); err != nil {
				return err
			}
			changed = true
		}
		if changed {
			if err := replaceStateFile(opts.StatePath, state); err != nil {
				return fmt.Errorf("update local deployment file before Worker upload: %w", err)
			}
		}
	}
	printKeyFile(out, opts.StatePath)

	if state.DatabaseID == "" {
		var database d1Database
		if len(databases) == 1 {
			database = databases[0]
		} else if database, err = api.createDatabase(ctx, accountID, opts.Name, opts.D1Location); err != nil {
			return err
		}
		if database.UUID == "" {
			return errors.New("Cloudflare returned a D1 database without its id")
		}
		state.DatabaseID = database.UUID
		if err := replaceStateFile(opts.StatePath, state); err != nil {
			return fmt.Errorf("record the D1 database id in the local deployment file: %w", err)
		}
	}

	setupHash := sha256.Sum256([]byte(state.SetupToken))
	api.addSecrets(state.MasterKey, state.AdminPrefix, state.SubPrefix, state.SetupToken, hex.EncodeToString(setupHash[:]))

	assetManifest, assetContents, err := buildAssetManifest(bundle.files)
	if err != nil {
		return err
	}
	modules := map[string][]byte{"worker.js": bundle.files["worker.js"], "provision.js": bundle.files["provision.js"], "panel.wasm": bundle.files["panel.wasm"]}
	// put uploads one script. The main script's assets are uploaded again before every PUT (see uploadAssets), and its
	// migrations are only the steps after the tag the script is at; a successful PUT moves that tag to the last step.
	put := func(name string, script release.EdgeScript, includeExternal bool) error {
		var assets *assetMetadata
		if script.Assets != nil {
			jwt, err := api.uploadAssets(ctx, accountID, name, assetManifest, assetContents)
			if err != nil {
				return err
			}
			assets = &assetMetadata{JWT: jwt, Config: assetSettings{RunWorkerFirst: true, HTMLHandling: "none", NotFoundHandling: "none"}}
		}
		migration, err := planMigration(script.Migrations, tags[name])
		if err != nil {
			return fmt.Errorf("script %s: %w", name, err)
		}
		if err := api.putScript(ctx, accountID, name, makeScriptMetadata(script, state, bundle, assets, includeExternal, migration), modules); err != nil {
			return err
		}
		if n := len(script.Migrations); n != 0 {
			tags[name] = script.Migrations[n-1].Tag
		} else {
			tags[name] = ""
		}
		return nil
	}
	// The scripts bind each other's Durable Objects: a first deploy puts main without PROVISIONER, then provision, then main.
	if !mainExists {
		if err := put(opts.Name, mainScript, false); err != nil {
			return err
		}
	}
	if err := put(provisionName, provisionScript, true); err != nil {
		return err
	}
	if err := put(opts.Name, mainScript, true); err != nil {
		return err
	}

	if len(mainScript.Cron) != 0 {
		if err := api.putSchedules(ctx, accountID, opts.Name, mainScript.Cron); err != nil {
			return err
		}
	}
	for _, name := range []string{opts.Name, provisionName} {
		if err := api.disableSubdomain(ctx, accountID, name); err != nil {
			return err
		}
	}
	if mainScript.Routes {
		if err := api.putDomain(ctx, accountID, state.ZoneID, state.Host, opts.Name); err != nil {
			return err
		}
	}

	decoyURL := opts.DecoyURL
	if decoyURL == "" {
		decoyURL = state.PublicURL + "/"
	}
	ready := false
	for attempt := 0; attempt < opts.PollAttempts && !ready; attempt++ {
		if attempt > 0 {
			if err := opts.Sleep(ctx, opts.PollInterval); err != nil {
				return fmt.Errorf("wait for the decoy page: %w", err)
			}
		}
		status, err := publicGet(ctx, opts.HTTPClient, decoyURL)
		ready = err == nil && status == http.StatusOK
	}
	fmt.Fprintf(out, "Admin URL: %s%s\n", state.PublicURL, state.AdminPrefix)
	fmt.Fprintf(out, "Setup link (one-time; the token is valid for 30 minutes from the panel's first start): %s\n", setupLink(state))
	if !ready {
		fmt.Fprintf(out, "Note: %s did not answer HTTP 200 yet; Cloudflare may still be issuing the domain's certificate. The deploy is finished: open the link in a few minutes.\n", state.PublicURL)
	}
	return nil
}

func printKeyFile(out io.Writer, statePath string) {
	path, err := filepath.Abs(statePath)
	if err != nil {
		path = statePath
	}
	fmt.Fprintf(out, "Keys saved to %s: back it up; if it is lost, the panel's data is lost with it.\n", path)
	if runtime.GOOS == "windows" {
		fmt.Fprintln(out, "On Windows file mode 0600 is not enforced: the file inherits the folder's permissions, so keep it in a private folder.")
	}
}

// planMigration returns the migrations a script at tag cur needs to reach the last step: all steps for a new script, the steps
// after cur for an existing one (with old_tag = cur), nothing when it is already at the last. A tag the bundle does not know
// means the bundle is older than what is deployed.
func planMigration(steps []release.EdgeMigrate, cur string) (*migrationRequest, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	last := steps[len(steps)-1].Tag
	start := 0
	if cur != "" {
		known := false
		for i, step := range steps {
			if step.Tag == cur {
				known, start = true, i+1
			}
		}
		if !known {
			return nil, fmt.Errorf("the deployed script is at migration %q, which this bundle does not know: the bundle is older than the deployment", cur)
		}
		if start == len(steps) {
			return nil, nil
		}
	}
	pending := steps[start:]
	m := &migrationRequest{OldTag: cur, NewTag: last}
	if len(pending) == 1 {
		m.NewSqliteClasses = pending[0].NewSqliteClasses
		return m, nil
	}
	for _, step := range pending {
		m.Steps = append(m.Steps, migrationStep{NewSqliteClasses: step.NewSqliteClasses})
	}
	return m, nil
}

func validateHost(host string) error {
	host = strings.ToLower(host)
	if len(host) == 0 || len(host) > 253 || strings.HasSuffix(host, ".") || strings.ContainsAny(host, ":/@?#[]") {
		return errors.New("--host must be a hostname without a scheme, port, path, or trailing dot")
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return errors.New("--host must be a fully qualified DNS hostname")
	}
	for _, part := range parts {
		if !dnsLabelRe.MatchString(part) {
			return errors.New("--host contains an invalid DNS label")
		}
	}
	return nil
}

// findZone looks for the zone of host, longest suffix first, in every account the token can see.
func findZone(ctx context.Context, api *cfClient, host string, accounts []account) (string, zone, error) {
	type match struct {
		accountID string
		zone      zone
	}
	labels := strings.Split(host, ".")
	for start := 0; start < len(labels)-1; start++ {
		suffix := strings.Join(labels[start:], ".")
		var active, inactive []match
		for _, a := range accounts {
			if a.ID == "" {
				continue
			}
			zones, err := api.zones(ctx, a.ID, suffix)
			if err != nil {
				return "", zone{}, err
			}
			for _, z := range zones {
				if !strings.EqualFold(z.Name, suffix) || z.ID == "" {
					continue
				}
				if strings.EqualFold(z.Status, "active") {
					active = append(active, match{a.ID, z})
				} else {
					inactive = append(inactive, match{a.ID, z})
				}
			}
		}
		switch {
		case len(active) > 1:
			return "", zone{}, fmt.Errorf("more than one Cloudflare account has the DNS zone %s", suffix)
		case len(active) == 1:
			return active[0].accountID, active[0].zone, nil
		case len(inactive) != 0:
			return "", zone{}, fmt.Errorf("the Cloudflare zone %s has status %q, not \"active\": finish pointing its nameservers at Cloudflare first", suffix, inactive[0].zone.Status)
		}
	}
	return "", zone{}, fmt.Errorf("no Cloudflare DNS zone matches host %s; the host's zone must be on Cloudflare DNS", host)
}

func loadExistingState(path string) (localState, bool, error) {
	var state localState
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return state, false, fmt.Errorf("open local deployment file: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return state, false, fmt.Errorf("decode local deployment file: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return state, false, errors.New("local deployment file has trailing data")
	}
	return state, true, nil
}

func validateState(state localState) error {
	if state.Schema != stateVersion || validateHost(state.Host) != nil || !dnsLabelRe.MatchString(state.Name) || state.ProvisionName != state.Name+"-provision" {
		return errors.New("unsupported or malformed state")
	}
	if state.PublicURL != "https://"+state.Host || !prefixRe.MatchString(state.AdminPrefix) || !prefixRe.MatchString(state.SubPrefix) {
		return errors.New("invalid public URL or secret path prefix")
	}
	key, err := hex.DecodeString(state.MasterKey)
	if err != nil || len(key) != 32 || strings.ToLower(state.MasterKey) != state.MasterKey {
		return errors.New("invalid master key")
	}
	tok, err := base64.RawURLEncoding.DecodeString(state.SetupToken)
	if err != nil || len(tok) != 32 || !tokenRe.MatchString(state.SetupToken) {
		return errors.New("invalid setup token")
	}
	// DatabaseID may be empty: the file is written before the D1 database is created.
	if state.AccountID == "" || state.ZoneID == "" || state.DatabaseName != state.Name {
		return errors.New("missing Cloudflare resource identifiers")
	}
	return nil
}

func newSetupToken(r io.Reader) (string, error) {
	var raw [32]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return "", fmt.Errorf("generate setup token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func newLocalState(opts Options, accountID, zoneID string, bundle *bundle, provisionName string) (localState, error) {
	var master [32]byte
	var prefixAdmin, prefixSub [15]byte
	if _, err := io.ReadFull(opts.Random, master[:]); err != nil {
		return localState{}, fmt.Errorf("generate master key: %w", err)
	}
	if _, err := io.ReadFull(opts.Random, prefixAdmin[:]); err != nil {
		return localState{}, fmt.Errorf("generate admin prefix: %w", err)
	}
	if _, err := io.ReadFull(opts.Random, prefixSub[:]); err != nil {
		return localState{}, fmt.Errorf("generate subscription prefix: %w", err)
	}
	token, err := newSetupToken(opts.Random)
	if err != nil {
		return localState{}, err
	}
	return localState{
		Schema: stateVersion, Host: strings.ToLower(opts.Host), Name: opts.Name, ProvisionName: provisionName,
		AccountID: accountID, ZoneID: zoneID, DatabaseName: opts.Name,
		PublicURL:   "https://" + strings.ToLower(opts.Host),
		AdminPrefix: secretPrefix(prefixAdmin[:]), SubPrefix: secretPrefix(prefixSub[:]),
		MasterKey: hex.EncodeToString(master[:]), SetupToken: token,
		BundleVersion: bundle.manifest.Version, BundleSHA256: bundle.manifest.SHA256,
	}, nil
}

func secretPrefix(raw []byte) string {
	return "/" + strings.ToLower(base32.StdEncoding.EncodeToString(raw)) + "/"
}

func createStateFile(path string, state localState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	return nil
}

func replaceStateFile(path string, state localState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir, base := filepath.Dir(path), filepath.Base(path)
	f, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func SetupLink(path string, out io.Writer) error {
	state, ok, err := loadExistingState(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("local deployment file %s does not exist", path)
	}
	if err := validateState(state); err != nil {
		return fmt.Errorf("local deployment file: %w", err)
	}
	fmt.Fprintln(out, setupLink(state))
	return nil
}

func setupLink(state localState) string {
	return state.PublicURL + state.AdminPrefix + "setup#" + state.SetupToken
}

// buildAssetManifest hashes the bundle's assets as Cloudflare's direct upload does: the first 32 hex characters of
// SHA-256(base64(content) + extension without the dot).
func buildAssetManifest(files map[string][]byte) (map[string]assetManifestEntry, map[string]assetContent, error) {
	paths := make([]string, 0)
	for name := range files {
		if strings.HasPrefix(name, "assets/") {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	manifest := make(map[string]assetManifestEntry, len(paths))
	contents := make(map[string]assetContent, len(paths))
	for _, name := range paths {
		content := files[name]
		ext := filepath.Ext(strings.TrimPrefix(name, "assets/"))
		encoded := base64.StdEncoding.EncodeToString(content)
		h := sha256.Sum256([]byte(encoded + strings.TrimPrefix(ext, ".")))
		hash := hex.EncodeToString(h[:])[:32]
		if previous, ok := contents[hash]; ok && previous.base64 != encoded {
			return nil, nil, errors.New("two assets have the same truncated SHA-256 hash")
		}
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		manifest["/"+strings.TrimPrefix(name, "assets/")] = assetManifestEntry{Hash: hash, Size: len(content)}
		contents[hash] = assetContent{base64: encoded, contentType: contentType}
	}
	return manifest, contents, nil
}

func makeScriptMetadata(script release.EdgeScript, state localState, b *bundle, assets *assetMetadata, includeExternal bool, migration *migrationRequest) scriptMetadata {
	meta := scriptMetadata{
		MainModule: script.MainModule, CompatibilityDate: b.config.CompatibilityDate,
		CompatibilityFlags: append([]string{}, b.config.CompatibilityFlags...),
		Migrations:         migration,
		Modules:            append([]release.EdgeModule{}, script.Modules...),
		Observability:      observability{Enabled: false},
	}
	for _, db := range script.D1 {
		meta.Bindings = append(meta.Bindings, workerBinding{Type: "d1", Name: db.Binding, DatabaseID: state.DatabaseID})
	}
	for _, object := range script.DurableObjects {
		meta.Bindings = append(meta.Bindings, workerBinding{Type: "durable_object_namespace", Name: object.Binding, ClassName: object.ClassName})
	}
	if includeExternal {
		for _, object := range script.ExternalObjects {
			scriptName := state.Name
			if object.ScriptRole == "provision" {
				scriptName = state.ProvisionName
			}
			meta.Bindings = append(meta.Bindings, workerBinding{Type: "durable_object_namespace", Name: object.Binding, ClassName: object.ClassName, ScriptName: scriptName})
		}
	}
	if script.Assets != nil {
		meta.Bindings = append(meta.Bindings, workerBinding{Type: "assets", Name: script.Assets.Binding})
		meta.Assets = assets
	}
	for _, secret := range script.Secrets {
		value := ""
		switch secret {
		case "MASTER_KEY":
			value = state.MasterKey
		case "ADMIN_PREFIX":
			value = state.AdminPrefix
		case "SUB_PREFIX":
			value = state.SubPrefix
		case "SETUP_TOKEN_SHA256":
			h := sha256.Sum256([]byte(state.SetupToken))
			value = hex.EncodeToString(h[:])
		}
		meta.Bindings = append(meta.Bindings, textBinding("secret_text", secret, value))
	}
	for _, variable := range script.Vars {
		// Only PUBLIC_URL has a value; a var such as ADMIN_HOST is left unset, not sent empty.
		if variable == "PUBLIC_URL" {
			meta.Bindings = append(meta.Bindings, textBinding("plain_text", variable, state.PublicURL))
		}
	}
	return meta
}

func textBinding(kind, name, text string) workerBinding {
	return workerBinding{Type: kind, Name: name, Text: &text}
}

func printPlan(out io.Writer, opts Options, b *bundle, databases []d1Database, mainExists bool) {
	fmt.Fprintf(out, "Plan for %s (edge bundle %s):\n", opts.Host, b.manifest.Version)
	fmt.Fprintf(out, "  account and zone: check the token, find the Cloudflare DNS zone for %s\n", opts.Host)
	if len(databases) == 1 {
		fmt.Fprintf(out, "  D1: reuse database %s (%s)\n", databases[0].Name, databases[0].UUID)
	} else {
		fmt.Fprintf(out, "  D1: create database %s in %s\n", opts.Name, opts.D1Location)
	}
	provisionName := opts.Name + "-provision"
	if !mainExists {
		fmt.Fprintf(out, "  scripts: %s without PROVISIONER, %s, %s with PROVISIONER\n", opts.Name, provisionName, opts.Name)
	} else {
		fmt.Fprintf(out, "  scripts: %s, %s\n", provisionName, opts.Name)
	}
	fmt.Fprintln(out, "  schedules: main script; disable workers.dev and previews on both scripts; attach the custom domain")
	fmt.Fprintln(out, "  final check: GET the decoy page until it returns HTTP 200")
}

func redactError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
}
