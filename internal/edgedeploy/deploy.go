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
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	panelprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/release"
)

const (
	stateVersion             = 2
	domainReachabilityWarmup = 2 * time.Second
)

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
	Rollback              bool
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
	Resolve               func(context.Context, string) ([]netip.Addr, error)
}

// localState is the key file. Schema 2 is the split edge: the names of the six scripts, what each one was before the last
// bundle replaced it (for --rollback), the bundle that is being deployed and the steps of this run that are done.
type localState struct {
	Schema        int                    `json:"schema"`
	Host          string                 `json:"host"`
	Name          string                 `json:"name"`
	Scripts       map[string]scriptState `json:"scripts"` // by role: link, provision, public, admin, jobs, front
	AccountID     string                 `json:"account_id"`
	ZoneID        string                 `json:"zone_id"`
	DatabaseID    string                 `json:"database_id"` // "" until the D1 database exists
	DatabaseName  string                 `json:"database_name"`
	PublicURL     string                 `json:"public_url"`
	AdminPrefix   string                 `json:"admin_prefix"`
	SubPrefix     string                 `json:"sub_prefix"`
	MasterKey     string                 `json:"master_key"`
	SetupToken    string                 `json:"setup_token"`
	BundleVersion string                 `json:"bundle_version"`
	BundleSHA256  string                 `json:"bundle_sha256"`
	// Steps are the steps of the current run that finished, in order: a role name for a script, "domain". --resume prints
	// them; the work that is skipped is decided by each script's Fingerprint.
	Steps []string `json:"steps,omitempty"`
}

// scriptState is what the key file knows of one script.
type scriptState struct {
	Name string `json:"name"`
	// Fingerprint identifies what the last complete upload of the script sent (modules, bindings with the secret values,
	// migrations, assets). An upload that would send the same is skipped, so a release that leaves FRONT alone does not
	// restart the NodeLink objects. It is empty until an upload finishes and after a rollback.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Previous is the deployment the script had before the current bundle replaced it; nil for a script the bundle created.
	Previous *previousScript `json:"previous,omitempty"`
}

type previousScript struct {
	Versions []deploymentVersion `json:"versions"`
	Crons    []string            `json:"crons"`
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
	Service    string  `json:"service,omitempty"`
	Entrypoint string  `json:"entrypoint,omitempty"`
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
	// The longest script name is <name>-provision.
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
	if opts.Rollback && (opts.Resume || opts.BundleDir != "" || opts.Version != "") {
		return errors.New("--rollback restores what the local deployment file recorded and cannot be used with --resume, --bundle or --version")
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
	if opts.Resolve == nil {
		opts.Resolve = panelprovision.DoHLookup(opts.HTTPClient)
	}
	reachabilityClient, err := newReachabilityClient(opts.HTTPClient, opts.Resolve)
	if err != nil {
		return fmt.Errorf("prepare custom-domain reachability check: %w", err)
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
	if (opts.Resume || opts.Rollback) && !hasState {
		flag := "--resume"
		if opts.Rollback {
			flag = "--rollback"
		}
		return fmt.Errorf("%s requires the local deployment file %s", flag, opts.StatePath)
	}
	if hasState {
		if err := validateState(state); err != nil {
			return fmt.Errorf("local deployment file: %w", err)
		}
		if state.Host != strings.ToLower(opts.Host) || state.Name != opts.Name {
			return errors.New("local deployment file belongs to a different host or Worker name")
		}
	}
	if opts.Rollback {
		return rollback(ctx, opts, state, out)
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

	api := newCFClient(opts.APIBaseURL, opts.Token, opts.HTTPClient)
	accountID, foundZone, err := locate(ctx, api, opts, state, hasState)
	if err != nil {
		return err
	}

	names := scriptNames(opts.Name)
	tags, err := api.scripts(ctx, accountID)
	if err != nil {
		return err
	}
	if !hasState {
		for _, name := range names {
			if _, exists := tags[name]; exists {
				return errors.New("a Worker script with this name already exists but its local key file is missing; refusing to replace its secrets")
			}
		}
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

	// A bundle older than what is deployed is refused before anything is uploaded.
	for _, script := range bundle.config.Scripts {
		if _, err := planMigration(script.Migrations, tags[names[script.Role]]); err != nil {
			return fmt.Errorf("script %s: %w", names[script.Role], err)
		}
	}
	if opts.DryRun {
		printPlan(out, opts, bundle, databases, tags)
		return nil
	}

	// The keys reach the local file before anything is created or uploaded; a run without --resume mints a new setup token.
	if !hasState {
		state, err = newLocalState(opts, accountID, foundZone.ID, bundle)
		if err != nil {
			return err
		}
		if err := createStateFile(opts.StatePath, state); err != nil {
			return fmt.Errorf("write local deployment file before creating anything: %w", err)
		}
	} else if !opts.Resume {
		// What the scripts were before this bundle is read once, when the bundle is new to the file. --resume and a rerun of
		// the same bundle keep it: by then some scripts already hold the new version.
		if state.BundleSHA256 != bundle.manifest.SHA256 {
			if err := recordPrevious(ctx, api, accountID, &state, tags); err != nil {
				return err
			}
		}
		state.BundleVersion, state.BundleSHA256 = bundle.manifest.Version, bundle.manifest.SHA256
		if state.SetupToken, err = newSetupToken(opts.Random); err != nil {
			return err
		}
		state.Steps = nil
		if err := replaceStateFile(opts.StatePath, state); err != nil {
			return fmt.Errorf("update local deployment file before Worker upload: %w", err)
		}
	}
	printKeyFile(out, opts.StatePath)
	if opts.Resume && len(state.Steps) != 0 {
		fmt.Fprintf(out, "Resuming; steps of the stopped run that finished: %s\n", strings.Join(state.Steps, ", "))
	}

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
	run := &deployRun{
		ctx: ctx, api: api, opts: opts, state: &state, bundle: bundle, tags: tags, accountID: accountID,
		assetManifest: assetManifest, assetContents: assetContents,
	}
	if err := run.deploy(); err != nil {
		return err
	}
	if err := opts.Sleep(ctx, domainReachabilityWarmup); err != nil {
		return fmt.Errorf("wait after attaching the custom domain: %w", err)
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
		status, err := publicGet(ctx, reachabilityClient, decoyURL)
		ready = err == nil && status == http.StatusOK
	}
	fmt.Fprintf(out, "Admin URL: %s%s\n", state.PublicURL, state.AdminPrefix)
	fmt.Fprintf(out, "Setup link (one-time; the token is valid for 30 minutes from the panel's first start): %s\n", setupLink(state))
	if !ready {
		fmt.Fprintf(out, "Note: %s did not answer HTTP 200 yet; Cloudflare may still be issuing the domain's certificate. The deploy is finished: open the link in a few minutes.\n", state.PublicURL)
	}
	return nil
}

// locate is the first account request, which is also the token check (a bad token fails here with a clear message), and
// finds the account and the zone of the host. A key file pins both: a different account or zone is an error.
func locate(ctx context.Context, api *cfClient, opts Options, state localState, hasState bool) (string, zone, error) {
	accounts, err := api.accounts(ctx)
	if err != nil {
		return "", zone{}, err
	}
	if len(accounts) == 0 {
		return "", zone{}, errors.New("Cloudflare API token cannot access an account")
	}
	accountID, foundZone, err := findZone(ctx, api, strings.ToLower(opts.Host), accounts)
	if err != nil {
		return "", zone{}, err
	}
	if hasState && (state.AccountID != accountID || state.ZoneID != foundZone.ID) {
		return "", zone{}, errors.New("the Cloudflare account or zone for this host changed since the local deployment file was created")
	}
	return accountID, foundZone, nil
}

// scriptNames maps a role to the name of its script under the base name.
func scriptNames(name string) map[string]string {
	names := make(map[string]string, len(release.EdgeRoles))
	for _, role := range release.EdgeRoles {
		names[role] = release.EdgeScriptName(name, role)
	}
	return names
}

// recordPrevious writes down, for every script that exists, the versions of its active deployment and its cron schedule:
// what --rollback puts back. A script that does not exist yet has nothing to go back to.
func recordPrevious(ctx context.Context, api *cfClient, accountID string, state *localState, tags map[string]string) error {
	for _, role := range release.EdgeRoles {
		script := state.Scripts[role]
		script.Previous = nil
		if _, exists := tags[script.Name]; exists {
			versions, err := api.activeDeployment(ctx, accountID, script.Name)
			if err != nil {
				return err
			}
			crons, err := api.schedules(ctx, accountID, script.Name)
			if err != nil {
				return err
			}
			if len(versions) != 0 {
				script.Previous = &previousScript{Versions: versions, Crons: crons}
			}
		}
		state.Scripts[role] = script
	}
	return nil
}

// deployRun is one upload of the bundle. Every step is idempotent, so a stopped run is repeated from the start with --resume;
// a script whose last upload finished with the same content is not uploaded again.
type deployRun struct {
	ctx           context.Context
	api           *cfClient
	opts          Options
	state         *localState
	bundle        *bundle
	tags          map[string]string // script name -> migration tag; present = the script exists
	accountID     string
	assetManifest map[string]assetManifestEntry
	assetContents map[string]assetContent
}

// deploy uploads the scripts in the order of the bundle (what a script binds is uploaded before it, FRONT last) and attaches
// the domain to the script that takes the routes. A FRONT that does not exist yet goes up first with no service bindings:
// link binds FRONT's Durable Objects and FRONT binds the other scripts as services.
func (d *deployRun) deploy() error {
	for _, script := range d.bundle.config.Scripts {
		if _, exists := d.tags[d.name(script.Role)]; len(script.Services) != 0 && !exists {
			// The script that is about to exist without its services must not count as uploaded, whatever an earlier
			// upload of it left in the key file: its upload with the services must happen.
			scriptState := d.state.Scripts[script.Role]
			scriptState.Fingerprint = ""
			d.state.Scripts[script.Role] = scriptState
			if err := replaceStateFile(d.opts.StatePath, *d.state); err != nil {
				return fmt.Errorf("update local deployment file before Worker upload: %w", err)
			}
			if err := d.put(script, false); err != nil {
				return err
			}
			if err := d.api.disableSubdomain(d.ctx, d.accountID, d.name(script.Role)); err != nil {
				return err
			}
		}
	}
	for _, script := range d.bundle.config.Scripts {
		if err := d.deployScript(script); err != nil {
			return err
		}
	}
	for _, script := range d.bundle.config.Scripts {
		if script.Routes {
			if err := d.api.putDomain(d.ctx, d.accountID, d.state.ZoneID, d.state.Host, d.name(script.Role)); err != nil {
				return err
			}
			if err := d.done("domain"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *deployRun) name(role string) string { return d.state.Scripts[role].Name }

// done records a finished step in the key file; a step that a resumed run repeats is recorded once.
func (d *deployRun) done(step string) error {
	if !slices.Contains(d.state.Steps, step) {
		d.state.Steps = append(d.state.Steps, step)
	}
	if err := replaceStateFile(d.opts.StatePath, *d.state); err != nil {
		return fmt.Errorf("record step %s in the local deployment file: %w", step, err)
	}
	return nil
}

func (d *deployRun) deployScript(script release.EdgeScript) error {
	name := d.name(script.Role)
	fingerprint, err := d.fingerprint(script)
	if err != nil {
		return err
	}
	pending, err := planMigration(script.Migrations, d.tags[name])
	if err != nil {
		return fmt.Errorf("script %s: %w", name, err)
	}
	_, exists := d.tags[name]
	if !exists || pending != nil || d.state.Scripts[script.Role].Fingerprint != fingerprint {
		if err := d.put(script, true); err != nil {
			return err
		}
	}
	// A script that is not uploaded again still gets the cheap, idempotent calls: they repair what was changed by hand.
	if err := d.api.disableSubdomain(d.ctx, d.accountID, name); err != nil {
		return err
	}
	if len(script.Cron) != 0 {
		if err := d.api.putSchedules(d.ctx, d.accountID, name, script.Cron); err != nil {
			return err
		}
	}
	scriptState := d.state.Scripts[script.Role]
	scriptState.Fingerprint = fingerprint
	d.state.Scripts[script.Role] = scriptState
	return d.done(script.Role)
}

// put uploads one script. The assets of a script are uploaded again before every PUT (see uploadAssets), and its migrations
// are only the steps after the tag the script is at; a successful PUT moves that tag to the last step.
func (d *deployRun) put(script release.EdgeScript, withServices bool) error {
	name := d.name(script.Role)
	var assets *assetMetadata
	if script.Assets != nil {
		jwt, err := d.api.uploadAssets(d.ctx, d.accountID, name, d.assetManifest, d.assetContents)
		if err != nil {
			return err
		}
		assets = &assetMetadata{JWT: jwt, Config: assetSettings{RunWorkerFirst: true, HTMLHandling: "none", NotFoundHandling: "none"}}
	}
	migration, err := planMigration(script.Migrations, d.tags[name])
	if err != nil {
		return fmt.Errorf("script %s: %w", name, err)
	}
	if err := d.api.putScript(d.ctx, d.accountID, name, makeScriptMetadata(script, *d.state, d.bundle, assets, withServices, migration), d.bundle.files); err != nil {
		return err
	}
	d.tags[name] = ""
	if n := len(script.Migrations); n != 0 {
		d.tags[name] = script.Migrations[n-1].Tag
	}
	return nil
}

// fingerprint hashes everything an upload of the script would send, but the one-off assets token: the metadata with the
// secret values (so a new secret uploads the script), the migration steps, the modules and the assets. The secrets are in the
// hash only; the key file keeps nothing it did not keep before.
func (d *deployRun) fingerprint(script release.EdgeScript) (string, error) {
	modules := make(map[string]string, len(script.Modules))
	for _, m := range script.Modules {
		sum := sha256.Sum256(d.bundle.files[m.Name])
		modules[m.Name] = hex.EncodeToString(sum[:])
	}
	content := struct {
		Metadata   scriptMetadata
		Migrations []release.EdgeMigrate
		Modules    map[string]string
		Assets     map[string]assetManifestEntry
	}{Metadata: makeScriptMetadata(script, *d.state, d.bundle, nil, true, nil), Migrations: script.Migrations, Modules: modules}
	if script.Assets != nil {
		content.Assets = d.assetManifest
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
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
	if state.Schema != stateVersion || validateHost(state.Host) != nil || !dnsLabelRe.MatchString(state.Name) {
		return fmt.Errorf("unsupported or malformed state (this mistgate reads schema %d)", stateVersion)
	}
	if len(state.Scripts) != len(release.EdgeRoles) {
		return errors.New("the script names are missing")
	}
	for role, name := range scriptNames(state.Name) {
		if state.Scripts[role].Name != name {
			return fmt.Errorf("the name of the %s script is not %s", role, name)
		}
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

func newLocalState(opts Options, accountID, zoneID string, bundle *bundle) (localState, error) {
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
	scripts := make(map[string]scriptState, len(release.EdgeRoles))
	for role, name := range scriptNames(opts.Name) {
		scripts[role] = scriptState{Name: name}
	}
	return localState{
		Schema: stateVersion, Host: strings.ToLower(opts.Host), Name: opts.Name, Scripts: scripts,
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

func makeScriptMetadata(script release.EdgeScript, state localState, b *bundle, assets *assetMetadata, withServices bool, migration *migrationRequest) scriptMetadata {
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
	for _, object := range script.ExternalObjects {
		meta.Bindings = append(meta.Bindings, workerBinding{Type: "durable_object_namespace", Name: object.Binding, ClassName: object.ClassName, ScriptName: state.Scripts[object.ScriptRole].Name})
	}
	// A FRONT that does not exist yet goes up without these: the scripts they name are not there (see deployRun.deploy).
	if withServices {
		for _, service := range script.Services {
			meta.Bindings = append(meta.Bindings, workerBinding{Type: "service", Name: service.Binding, Service: state.Scripts[service.ServiceRole].Name, Entrypoint: service.Entrypoint})
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

// printPlan lists the calls of a real run, in order. It names the bindings and never prints a value.
func printPlan(out io.Writer, opts Options, b *bundle, databases []d1Database, tags map[string]string) {
	names := scriptNames(opts.Name)
	scripts := make(map[string]scriptState, len(names))
	for role, name := range names {
		scripts[role] = scriptState{Name: name}
	}
	state := localState{Name: opts.Name, Scripts: scripts, DatabaseID: "(the D1 database)"}
	n := 0
	call := func(format string, a ...any) {
		n++
		fmt.Fprintf(out, "  %2d. %s\n", n, fmt.Sprintf(format, a...))
	}
	fmt.Fprintf(out, "Plan for %s (edge bundle %s). Already done by this dry run: GET the accounts, the zone of the host, the scripts and the D1 databases.\n", opts.Host, b.manifest.Version)
	if len(databases) == 1 {
		call("D1: reuse database %s (%s)", databases[0].Name, databases[0].UUID)
	} else {
		call("POST d1/database: create %s in %s", opts.Name, opts.D1Location)
	}
	upload := func(script release.EdgeScript, withServices bool, note string) {
		name := names[script.Role]
		if script.Assets != nil {
			call("POST scripts/%s/assets-upload-session, then POST workers/assets/upload for the files Cloudflare asks for", name)
		}
		var items []string
		for _, binding := range makeScriptMetadata(script, state, b, nil, withServices, nil).Bindings {
			items = append(items, describeBinding(binding))
		}
		modules := make([]string, 0, len(script.Modules))
		for _, m := range script.Modules {
			modules = append(modules, m.Name)
		}
		call("PUT scripts/%s%s: %s; %s", name, note, strings.Join(modules, ", "), strings.Join(items, ", "))
	}
	for _, script := range b.config.Scripts {
		if _, exists := tags[names[script.Role]]; len(script.Services) != 0 && !exists {
			upload(script, false, " (first create, without its service bindings)")
			call("POST scripts/%s/subdomain: workers.dev and previews off", names[script.Role])
		}
	}
	for _, script := range b.config.Scripts {
		note := ""
		if _, exists := tags[names[script.Role]]; exists {
			note = " (skipped when it is unchanged since the last deploy)"
		}
		upload(script, true, note)
		call("POST scripts/%s/subdomain: workers.dev and previews off", names[script.Role])
		if len(script.Cron) != 0 {
			call("PUT scripts/%s/schedules: %s", names[script.Role], strings.Join(script.Cron, ", "))
		}
	}
	for _, script := range b.config.Scripts {
		if script.Routes {
			call("PUT workers/domains: %s -> %s", opts.Host, names[script.Role])
		}
	}
	call("GET https://%s/ until it returns HTTP 200", opts.Host)
}

func describeBinding(b workerBinding) string {
	switch b.Type {
	case "d1":
		return "d1 " + b.Name
	case "durable_object_namespace":
		if b.ScriptName != "" {
			return fmt.Sprintf("do %s (%s of %s)", b.Name, b.ClassName, b.ScriptName)
		}
		return fmt.Sprintf("do %s (own %s)", b.Name, b.ClassName)
	case "service":
		if b.Entrypoint != "" {
			return fmt.Sprintf("service %s -> %s#%s", b.Name, b.Service, b.Entrypoint)
		}
		return fmt.Sprintf("service %s -> %s", b.Name, b.Service)
	case "secret_text":
		return "secret " + b.Name
	case "plain_text":
		return "var " + b.Name
	}
	return b.Type + " " + b.Name
}

func redactError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
}
