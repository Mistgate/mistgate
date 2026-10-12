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
	"sort"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/release"
)

const stateVersion = 1

var (
	workerNameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	dnsLabelRe   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	prefixRe     = regexp.MustCompile(`^/[a-z2-7]{24}/$`)
	tokenRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

type Options struct {
	Host                  string
	Name                  string
	D1Location            string
	BundleDir             string
	Version               string
	Resume                bool
	DryRun                bool
	InsecureSkipSignature bool
	Token                 string
	StatePath             string
	PublicKey             ed25519.PublicKey
	APIBaseURL            string
	GitHubAPIBaseURL      string
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
	DatabaseID    string `json:"database_id"`
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
	Hash        string `json:"hash"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
}

type assetContent struct {
	base64 string
}

type scriptMetadata struct {
	MainModule         string                `json:"main_module"`
	CompatibilityDate  string                `json:"compatibility_date"`
	CompatibilityFlags []string              `json:"compatibility_flags"`
	Bindings           []workerBinding       `json:"bindings"`
	Migrations         []release.EdgeMigrate `json:"migrations"`
	Modules            []release.EdgeModule  `json:"-"`
	Assets             *assetMetadata        `json:"assets,omitempty"`
	Observability      observability         `json:"observability"`
	KeepAssets         bool                  `json:"keep_assets,omitempty"`
}

type workerBinding struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	ID         string  `json:"id,omitempty"`
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
	if !workerNameRe.MatchString(opts.Name) || len(opts.Name)+len("-provision") > 63 {
		return errors.New("--name must be a lowercase DNS-style Worker name of at most 54 characters")
	}
	if opts.D1Location == "" {
		opts.D1Location = "weur"
	}
	if opts.BundleDir != "" && opts.Version != "" {
		return errors.New("--bundle and --version cannot be used together")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.PollAttempts <= 0 {
		opts.PollAttempts = 60
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
		if opts.Resume && opts.BundleDir == "" && opts.Version == "" {
			opts.Version = state.BundleVersion
		}
	}

	bundle, err := readBundle(ctx, opts, opts.HTTPClient, opts.Now())
	if err != nil {
		return err
	}
	if opts.Resume && hasState && state.BundleSHA256 != bundle.sha256 {
		return errors.New("resumed edge bundle differs from the bundle recorded in the local deployment file")
	}

	api := newCFClient(opts.APIBaseURL, opts.Token, opts.HTTPClient)
	if err := api.verifyToken(ctx); err != nil {
		return err
	}
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
	if foundZone.ID == "" {
		return fmt.Errorf("no Cloudflare DNS zone matches host %s; the host's zone must be on Cloudflare DNS", opts.Host)
	}
	if hasState && (state.AccountID != accountID || state.ZoneID != foundZone.ID) {
		return errors.New("the Cloudflare account or zone for this host changed since the local deployment file was created")
	}

	databaseName := opts.Name
	databases, err := api.databases(ctx, accountID, databaseName)
	if err != nil {
		return err
	}
	var database d1Database
	if hasState {
		for _, existing := range databases {
			if existing.UUID == state.DatabaseID {
				database = existing
				break
			}
		}
		if database.UUID == "" {
			return errors.New("the D1 database in the local deployment file no longer exists; refusing to replace it automatically")
		}
	} else {
		if len(databases) != 0 {
			return errors.New("a D1 database with this name already exists but its local key file is missing; refusing to adopt it")
		}
	}

	provisionName := opts.Name + "-provision"
	mainExists, provisionExists := false, false
	// Script lookups happen before any create so the upload order can match first deploys and updates.
	mainExists, err = api.scriptExists(ctx, accountID, opts.Name)
	if err != nil {
		return err
	}
	provisionExists, err = api.scriptExists(ctx, accountID, provisionName)
	if err != nil {
		return err
	}
	if !hasState && (mainExists || provisionExists) {
		return errors.New("a Worker script with this name already exists but its local key file is missing; refusing to replace its secrets")
	}
	if opts.DryRun {
		printPlan(out, opts, bundle, database, len(databases) != 0, mainExists, provisionExists)
		return nil
	}

	if !hasState {
		database, err = api.createDatabase(ctx, accountID, databaseName, opts.D1Location)
		if err != nil {
			return err
		}
		if database.UUID == "" {
			return errors.New("Cloudflare created a D1 database without returning its id")
		}
		state, err = newLocalState(opts, accountID, foundZone.ID, database.UUID, bundle, provisionName)
		if err != nil {
			return err
		}
		if err := createStateFile(opts.StatePath, state); err != nil {
			return fmt.Errorf("write local deployment file before Worker upload: %w", err)
		}
	} else {
		if state.BundleVersion != bundle.manifest.Version || state.BundleSHA256 != bundle.sha256 {
			state.BundleVersion = bundle.manifest.Version
			state.BundleSHA256 = bundle.sha256
			if err := replaceStateFile(opts.StatePath, state); err != nil {
				return fmt.Errorf("update local deployment file before Worker upload: %w", err)
			}
		}
	}

	if state.AccountID != accountID || state.ZoneID != foundZone.ID || state.DatabaseID != database.UUID {
		return errors.New("local deployment identifiers do not match Cloudflare")
	}
	setupHash := sha256.Sum256([]byte(state.SetupToken))
	api.addSecrets(state.MasterKey, state.AdminPrefix, state.SubPrefix, state.SetupToken, hex.EncodeToString(setupHash[:]))

	assetManifest, assetContents, err := buildAssetManifest(bundle.files)
	if err != nil {
		return err
	}
	session, err := api.assetSession(ctx, accountID, opts.Name, assetManifest)
	if err != nil {
		return err
	}
	api.addSecrets(session.JWT)
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			continue
		}
		if session.JWT == "" {
			return errors.New("Cloudflare assets upload session returned buckets without a token")
		}
		if err := api.uploadAssetBucket(ctx, accountID, session.JWT, bucket, assetContents); err != nil {
			return err
		}
	}

	mainScript, provisionScript := bundle.config.Scripts[1], bundle.config.Scripts[0]
	modules := map[string][]byte{"worker.js": bundle.files["worker.js"], "provision.js": bundle.files["provision.js"], "panel.wasm": bundle.files["panel.wasm"]}
	assets := &assetMetadata{JWT: session.JWT, Config: assetSettings{RunWorkerFirst: true, HTMLHandling: "none", NotFoundHandling: "none"}}
	keepAssets := mainExists || provisionExists

	switch {
	case !mainExists && !provisionExists:
		if err := api.putScript(ctx, accountID, opts.Name, makeScriptMetadata(mainScript, state, bundle, assets, false, false), modules); err != nil {
			return err
		}
		if err := api.putScript(ctx, accountID, provisionName, makeScriptMetadata(provisionScript, state, bundle, nil, true, false), modules); err != nil {
			return err
		}
		if err := api.putScript(ctx, accountID, opts.Name, makeScriptMetadata(mainScript, state, bundle, assets, true, false), modules); err != nil {
			return err
		}
	case mainExists && !provisionExists:
		if err := api.putScript(ctx, accountID, provisionName, makeScriptMetadata(provisionScript, state, bundle, nil, true, false), modules); err != nil {
			return err
		}
		if err := api.putScript(ctx, accountID, opts.Name, makeScriptMetadata(mainScript, state, bundle, assets, true, true), modules); err != nil {
			return err
		}
	case !mainExists && provisionExists:
		if err := api.putScript(ctx, accountID, opts.Name, makeScriptMetadata(mainScript, state, bundle, assets, true, false), modules); err != nil {
			return err
		}
	default:
		if err := api.putScript(ctx, accountID, provisionName, makeScriptMetadata(provisionScript, state, bundle, nil, true, false), modules); err != nil {
			return err
		}
		if err := api.putScript(ctx, accountID, opts.Name, makeScriptMetadata(mainScript, state, bundle, assets, true, keepAssets), modules); err != nil {
			return err
		}
	}

	if len(mainScript.Cron) != 0 {
		if err := api.putSchedules(ctx, accountID, opts.Name, mainScript.Cron); err != nil {
			return err
		}
	}
	if err := api.putSubdomain(ctx, accountID, opts.Name); err != nil {
		return err
	}
	if err := api.putSubdomain(ctx, accountID, provisionName); err != nil {
		return err
	}
	if mainScript.Routes {
		if err := api.putDomain(ctx, accountID, state.Host, opts.Name); err != nil {
			return err
		}
	}

	decoyURL := opts.DecoyURL
	if decoyURL == "" {
		decoyURL = state.PublicURL + "/"
	}
	ready := false
	for attempt := 0; attempt < opts.PollAttempts; attempt++ {
		status, err := publicGet(ctx, opts.HTTPClient, decoyURL)
		if err == nil && status == http.StatusOK {
			ready = true
			break
		}
		if attempt+1 < opts.PollAttempts {
			if err := opts.Sleep(ctx, opts.PollInterval); err != nil {
				return fmt.Errorf("wait for the decoy page: %w", err)
			}
		}
	}
	if !ready {
		return fmt.Errorf("the decoy page at %s did not return HTTP 200", state.PublicURL)
	}
	fmt.Fprintf(out, "Admin URL: %s%s\n", state.PublicURL, state.AdminPrefix)
	fmt.Fprintf(out, "Setup link (one-time, valid for 30 minutes): %s\n", setupLink(state))
	return nil
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
		if !dnsLabelRe.MatchString(part) || len(part) > 63 {
			return errors.New("--host contains an invalid DNS label")
		}
	}
	return nil
}

func findZone(ctx context.Context, api *cfClient, host string, accounts []account) (string, zone, error) {
	labels := strings.Split(host, ".")
	for start := 0; start < len(labels)-1; start++ {
		suffix := strings.Join(labels[start:], ".")
		var matches []struct {
			accountID string
			zone      zone
		}
		for _, a := range accounts {
			if a.ID == "" {
				continue
			}
			zones, err := api.zones(ctx, a.ID, suffix)
			if err != nil {
				return "", zone{}, err
			}
			for _, z := range zones {
				if strings.EqualFold(z.Name, suffix) && strings.EqualFold(z.Status, "active") && z.ID != "" {
					matches = append(matches, struct {
						accountID string
						zone      zone
					}{a.ID, z})
				}
			}
		}
		if len(matches) > 1 {
			return "", zone{}, fmt.Errorf("more than one Cloudflare account has the DNS zone %s", suffix)
		}
		if len(matches) == 1 {
			return matches[0].accountID, matches[0].zone, nil
		}
	}
	return "", zone{}, nil
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
	if state.Schema != stateVersion || validateHost(state.Host) != nil || !workerNameRe.MatchString(state.Name) || state.ProvisionName != state.Name+"-provision" {
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
	if state.AccountID == "" || state.ZoneID == "" || state.DatabaseID == "" || state.DatabaseName != state.Name {
		return errors.New("missing Cloudflare resource identifiers")
	}
	return nil
}

func newLocalState(opts Options, accountID, zoneID, databaseID string, bundle *bundle, provisionName string) (localState, error) {
	var master, tokenRaw [32]byte
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
	if _, err := io.ReadFull(opts.Random, tokenRaw[:]); err != nil {
		return localState{}, fmt.Errorf("generate setup token: %w", err)
	}
	return localState{
		Schema: stateVersion, Host: strings.ToLower(opts.Host), Name: opts.Name, ProvisionName: provisionName,
		AccountID: accountID, ZoneID: zoneID, DatabaseID: databaseID, DatabaseName: opts.Name,
		PublicURL:   "https://" + strings.ToLower(opts.Host),
		AdminPrefix: secretPrefix(prefixAdmin[:]), SubPrefix: secretPrefix(prefixSub[:]),
		MasterKey: hex.EncodeToString(master[:]), SetupToken: base64.RawURLEncoding.EncodeToString(tokenRaw[:]),
		BundleVersion: bundle.manifest.Version, BundleSHA256: bundle.sha256,
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
		h := sha256.Sum256([]byte(encoded + ext))
		hash := hex.EncodeToString(h[:])[:32]
		if previous, ok := contents[hash]; ok && previous.base64 != encoded {
			return nil, nil, errors.New("two assets have the same truncated SHA-256 hash")
		}
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		manifest["/"+strings.TrimPrefix(name, "assets/")] = assetManifestEntry{Hash: hash, Size: len(content), ContentType: contentType}
		contents[hash] = assetContent{base64: encoded}
	}
	return manifest, contents, nil
}

func makeScriptMetadata(script release.EdgeScript, state localState, b *bundle, assets *assetMetadata, includeExternal, keepAssets bool) scriptMetadata {
	meta := scriptMetadata{
		MainModule: script.MainModule, CompatibilityDate: b.config.CompatibilityDate,
		CompatibilityFlags: append([]string{}, b.config.CompatibilityFlags...),
		Migrations:         append([]release.EdgeMigrate{}, script.Migrations...),
		Modules:            append([]release.EdgeModule{}, script.Modules...),
		Observability:      observability{Enabled: false}, KeepAssets: keepAssets,
	}
	for _, db := range script.D1 {
		meta.Bindings = append(meta.Bindings, workerBinding{Type: "d1", Name: db.Binding, ID: state.DatabaseID})
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
		value := ""
		if variable == "PUBLIC_URL" {
			value = state.PublicURL
		}
		meta.Bindings = append(meta.Bindings, textBinding("plain_text", variable, value))
	}
	return meta
}

func textBinding(kind, name, text string) workerBinding {
	return workerBinding{Type: kind, Name: name, Text: &text}
}

func printPlan(out io.Writer, opts Options, b *bundle, db d1Database, databaseExists, mainExists, provisionExists bool) {
	fmt.Fprintf(out, "Plan for %s (edge bundle %s):\n", opts.Host, b.manifest.Version)
	fmt.Fprintf(out, "  account and zone: verify token, find the Cloudflare DNS zone for %s\n", opts.Host)
	if databaseExists {
		fmt.Fprintf(out, "  D1: reuse database %s (%s)\n", db.Name, db.UUID)
	} else {
		fmt.Fprintf(out, "  D1: create database %s in %s\n", opts.Name, opts.D1Location)
	}
	if !mainExists && !provisionExists {
		fmt.Fprintf(out, "  scripts: %s without PROVISIONER, %s, %s with PROVISIONER\n", opts.Name, opts.Name+"-provision", opts.Name)
	} else {
		fmt.Fprintf(out, "  scripts: %s, %s (keep assets)\n", opts.Name+"-provision", opts.Name)
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
