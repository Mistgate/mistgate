//go:build js && wasm

package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"syscall/js"

	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

var (
	errInitOptions           = errors.New("mgPanel.init requires an options object")
	errFetchRequest          = errors.New("mgPanel.fetch requires a request object")
	errNotInitialized        = errors.New("mgPanel.init must complete before mgPanel.fetch")
	errInvalidFetchRequest   = errors.New("mgPanel.fetch request requires a method and URL")
	errInvalidFetchURL       = errors.New("mgPanel.fetch URL must be an absolute HTTPS URL")
	errInvalidFetchHeaders   = errors.New("mgPanel.fetch headers must be an array of [name, value] pairs")
	errInvalidFetchBody      = errors.New("mgPanel.fetch body must be a Uint8Array or null")
	errPromisePanic          = errors.New("mgPanel operation failed")
	errMissingD1             = errors.New("mgPanel.init requires a D1 binding")
	errMissingMasterKey      = errors.New("mgPanel.init requires a 32-byte master key")
	errFreshPublicURL        = errors.New("mgPanel.init requires publicURL or publicHost for a new D1 database")
	errSeparateAdminListener = errors.New("the edge edition does not support a separate admin listener")
	errMissingEdgeAdminPath  = errors.New("the edge edition requires an admin host or a secret admin path prefix")
	errInvalidPublicURL      = errors.New("publicURL must be an HTTPS URL with a host and no path, query, or fragment")
	errInvalidAdminHost      = errors.New("adminHost must be a host name without a scheme or path")
	errAdminModeConflict     = errors.New("adminHost and adminPrefix are mutually exclusive")
	errInvalidAdminPrefix    = errors.New("adminPrefix must look like /secret/")
	errInvalidSubPrefix      = errors.New("subPrefix must look like /secret/")
	errInvalidLimitCallback  = errors.New("limit must be a function")
	errInvalidNodeLink       = errors.New("nodeLink must provide ask, close and poke functions")
	errInvalidSetupHash      = errors.New("setupTokenSHA256 must be 64 lowercase hex characters (the SHA-256 of the setup token)")
	errInvalidRecoveryHash   = errors.New("recoveryTokenSHA256 must be 64 lowercase hex characters (the SHA-256 of the recovery token)")
)

type initOptions struct {
	d1          js.Value
	assets      js.Value // optional async (path) => Uint8Array | null: the SPA build in the Worker's static assets
	limit       js.Value // optional async security-limit callback
	nodeLink    js.Value
	hasNodeLink bool
	provision   provisionOptions
	masterKey   []byte
	publicURL   string
	adminHost   string
	adminPath   string
	subPath     string
	rpID        string
	rpOrigins   []string
	sourceURL   string
	// setupTokenHash is the SHA-256 of a setup token chosen by whoever deploys (SETUP_TOKEN_SHA256); nil: the panel mints a random one.
	setupTokenHash    []byte
	recoveryTokenHash []byte // SHA-256 of the one-time token set by `mistgate edge reset-login`.
}

func parseInitOptions(value js.Value) (initOptions, error) {
	var out initOptions
	d1 := value.Get("d1")
	if d1.Type() != js.TypeObject || d1.IsNull() {
		return out, errMissingD1
	}
	out.d1 = d1
	if assets := value.Get("assets"); assets.Type() == js.TypeFunction {
		out.assets = assets
	}
	limit := value.Get("limit")
	if limit.Type() == js.TypeFunction {
		out.limit = limit
	} else if limit.Type() != js.TypeUndefined && !limit.IsNull() {
		return out, errInvalidLimitCallback
	}
	if optionPresent(value, "nodeLink") {
		nodeLink := value.Get("nodeLink")
		if nodeLink.Type() != js.TypeObject || nodeLink.IsNull() ||
			nodeLink.Get("ask").Type() != js.TypeFunction || nodeLink.Get("close").Type() != js.TypeFunction ||
			nodeLink.Get("poke").Type() != js.TypeFunction {
			return out, errInvalidNodeLink
		}
		send := nodeLink.Get("send")
		if send.Type() != js.TypeUndefined && !send.IsNull() && send.Type() != js.TypeFunction {
			return out, errInvalidNodeLink
		}
		out.nodeLink = nodeLink
		out.hasNodeLink = true
	}
	var err error
	if out.provision, err = parseProvisionOptions(value); err != nil {
		return out, err
	}
	if out.masterKey, err = masterKeyFromJS(value.Get("masterKey")); err != nil {
		return out, err
	}
	if out.publicURL, err = stringOption(value, "publicURL"); err != nil {
		return out, err
	}
	publicHost, err := stringOption(value, "publicHost")
	if err != nil {
		return out, err
	}
	if out.publicURL == "" && publicHost != "" {
		if strings.Contains(publicHost, "://") {
			out.publicURL = publicHost
		} else {
			out.publicURL = "https://" + publicHost
		}
	}
	if out.adminHost, err = stringOption(value, "adminHost"); err != nil {
		return out, err
	}
	if out.adminPath, err = stringOption(value, "adminPrefix"); err != nil {
		return out, err
	}
	if out.subPath, err = stringOption(value, "subPrefix"); err != nil {
		return out, err
	}
	if out.subPath == "" {
		if out.subPath, err = stringOption(value, "subscriptionPrefix"); err != nil {
			return out, err
		}
	}
	if out.rpID, err = stringOption(value, "rpID"); err != nil {
		return out, err
	}
	origins, err := stringOption(value, "rpOrigins")
	if err != nil {
		return out, err
	}
	if origins != "" {
		out.rpOrigins = splitList(origins)
		if len(out.rpOrigins) == 0 {
			return out, errors.New("rpOrigins is empty")
		}
	}
	hash, err := stringOption(value, "setupTokenSHA256")
	if err != nil {
		return out, err
	}
	if hash != "" {
		if out.setupTokenHash, err = parseSetupTokenHash(hash); err != nil {
			return out, err
		}
	}
	recoveryHash, err := stringOption(value, "recoveryTokenSHA256")
	if err != nil {
		return out, err
	}
	if recoveryHash != "" {
		if out.recoveryTokenHash, err = parseRecoveryTokenHash(recoveryHash); err != nil {
			return out, err
		}
	}
	if optionPresent(value, "sourceURL") {
		if out.sourceURL, err = stringOption(value, "sourceURL"); err != nil {
			return out, err
		}
	} else {
		out.sourceURL = "https://github.com/Mistgate/mistgate"
	}
	return out, nil
}

// parseSetupTokenHash accepts exactly what `sha256sum` prints for the token: 64 lowercase hex characters.
func parseSetupTokenHash(raw string) ([]byte, error) {
	if len(raw) != 2*sha256.Size || strings.ToLower(raw) != raw {
		return nil, errInvalidSetupHash
	}
	hash, err := hex.DecodeString(raw)
	if err != nil {
		return nil, errInvalidSetupHash
	}
	return hash, nil
}

func parseRecoveryTokenHash(raw string) ([]byte, error) {
	if len(raw) != 2*sha256.Size || strings.ToLower(raw) != raw {
		return nil, errInvalidRecoveryHash
	}
	hash, err := hex.DecodeString(raw)
	if err != nil {
		return nil, errInvalidRecoveryHash
	}
	return hash, nil
}

func stringOption(object js.Value, name string) (string, error) {
	value := object.Get(name)
	if value.Type() == js.TypeUndefined || value.IsNull() {
		return "", nil
	}
	if value.Type() != js.TypeString {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value.String(), nil
}

func optionPresent(object js.Value, name string) bool {
	return js.Global().Get("Reflect").Call("has", object, name).Bool()
}

func masterKeyFromJS(value js.Value) ([]byte, error) {
	var key []byte
	switch {
	case value.Type() == js.TypeObject && !value.IsNull() && value.InstanceOf(js.Global().Get("Uint8Array")):
		key = make([]byte, value.Length())
		if js.CopyBytesToGo(key, value) != len(key) {
			return nil, errMissingMasterKey
		}
	case value.Type() == js.TypeString:
		decoded, err := hex.DecodeString(value.String())
		if err != nil {
			return nil, errMissingMasterKey
		}
		key = decoded
	default:
		return nil, errMissingMasterKey
	}
	if len(key) != vault.KeySize {
		return nil, errMissingMasterKey
	}
	return key, nil
}

// edgeInstanceKeys are the settings an initialised instance is made of; one D1 read fetches them all (a cold isolate
// does this on every start, and a D1 round trip is milliseconds: §7.7 counted nine sequential reads here).
var edgeInstanceKeys = []string{
	"rp_id", "rp_origins", "public_url", "admin_host", "admin_prefix", "admin_listen", "agent_sni", "sub_prefix", "link_prefix",
}

// applyStoredInstance sets the fields of in that the stored settings in values name.
func applyStoredInstance(in *app.InstanceConfig, values map[string]string) error {
	for key, value := range values {
		switch key {
		case "rp_id":
			in.RPID = value
		case "rp_origins":
			if in.RPOrigins = splitList(value); len(in.RPOrigins) == 0 {
				return errors.New("stored rp_origins setting is empty")
			}
		case "public_url":
			in.PublicURL = value
		case "admin_host":
			in.AdminHost = value
		case "admin_prefix":
			in.AdminPrefix = value
		case "admin_listen":
			in.AdminListen = value
		case "agent_sni":
			in.AgentSNI = value
		case "sub_prefix":
			in.SubPrefix = value
		case "link_prefix":
			in.LinkPrefix = value
		}
	}
	return nil
}

// settleEdgeInstance stores the generated settings unless another isolate stored its own first (isolates that start on an
// empty database at the same time each generate their own prefixes and agent SNI) and returns the instance as stored, so
// every isolate builds from the same values.
func settleEdgeInstance(ctx context.Context, st *store.Store, in app.InstanceConfig, generated map[string]string) (app.InstanceConfig, error) {
	stored, err := st.SetSettingsIfAbsent(ctx, generated)
	if err != nil {
		return in, err
	}
	return in, applyStoredInstance(&in, stored)
}

func loadEdgeInstance(ctx context.Context, st *store.Store, opts initOptions) (app.InstanceConfig, map[string]string, error) {
	values, err := st.SettingValues(ctx, edgeInstanceKeys)
	if err != nil {
		return app.InstanceConfig{}, nil, err
	}
	rpID, ok := values["rp_id"]
	if !ok {
		return newEdgeInstance(opts)
	}
	in := app.InstanceConfig{RPID: rpID}
	origins, ok := values["rp_origins"]
	if !ok {
		return in, nil, store.ErrNotFound
	}
	if in.RPOrigins = splitList(origins); len(in.RPOrigins) == 0 {
		return in, nil, errors.New("stored rp_origins setting is empty")
	}
	for key, target := range map[string]*string{
		"public_url": &in.PublicURL, "admin_host": &in.AdminHost,
		"admin_prefix": &in.AdminPrefix, "admin_listen": &in.AdminListen,
	} {
		if *target, ok = values[key]; !ok {
			return in, nil, store.ErrNotFound
		}
	}
	if in.AdminListen != "" && (in.AdminHost != "" || in.AdminPrefix != "/") {
		return in, nil, errors.New("stored settings combine a separate admin listener with an admin host or path prefix")
	}
	return in, fillEdgeSecrets(values, &in), nil
}

func newEdgeInstance(opts initOptions) (app.InstanceConfig, map[string]string, error) {
	var in app.InstanceConfig
	if opts.publicURL == "" {
		return in, nil, errFreshPublicURL
	}
	publicURL, err := parsePublicURL(opts.publicURL)
	if err != nil {
		return in, nil, err
	}
	in.PublicURL = publicURL.String()
	adminHost, err := parseAdminHost(opts.adminHost)
	if err != nil {
		return in, nil, err
	}
	in.AdminHost = adminHost
	if in.AdminHost != "" && opts.adminPath != "" {
		return in, nil, errAdminModeConflict
	}
	if in.AdminHost != "" {
		in.AdminPrefix = "/"
	} else if opts.adminPath == "" {
		in.AdminPrefix = newSecretPrefix()
	} else if in.AdminPrefix, err = validatePrefix(opts.adminPath, errInvalidAdminPrefix); err != nil {
		return in, nil, err
	}
	if opts.subPath == "" {
		in.SubPrefix = newSecretPrefix()
	} else if in.SubPrefix, err = validatePrefix(opts.subPath, errInvalidSubPrefix); err != nil {
		return in, nil, err
	}
	in.LinkPrefix = newSecretPrefix()
	if opts.rpID != "" {
		in.RPID = opts.rpID
	} else if in.AdminHost != "" {
		in.RPID = in.AdminHost
	} else {
		in.RPID = publicURL.Hostname()
	}
	if len(opts.rpOrigins) != 0 {
		in.RPOrigins = opts.rpOrigins
	} else {
		originHost := publicURL.Host
		if in.AdminHost != "" {
			originHost = in.AdminHost
			if publicURL.Port() != "" {
				originHost = in.AdminHost + ":" + publicURL.Port()
			}
		}
		in.RPOrigins = []string{publicURL.Scheme + "://" + originHost}
	}
	in.AgentSNI = newAgentSNI(publicURL.Hostname())
	pending := map[string]string{
		"public_url": in.PublicURL, "admin_host": in.AdminHost,
		"admin_prefix": in.AdminPrefix, "admin_listen": "",
		"rp_id": in.RPID, "rp_origins": strings.Join(in.RPOrigins, ","),
		"agent_sni": in.AgentSNI, "sub_prefix": in.SubPrefix, "link_prefix": in.LinkPrefix,
	}
	return in, pending, nil
}

func parsePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errInvalidPublicURL
	}
	return u, nil
}

func parseAdminHost(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse("https://" + raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "://") {
		return "", errInvalidAdminHost
	}
	return strings.ToLower(u.Host), nil
}

func validatePrefix(raw string, errInvalid error) (string, error) {
	// nothing a URL path cannot carry as is either: the admin prefix ends up in the <base href> of the SPA
	if len(raw) < 4 || !strings.HasPrefix(raw, "/") || !strings.HasSuffix(raw, "/") || strings.Contains(raw, "//") || strings.Contains(raw, "..") || strings.ContainsAny(raw, "?#\"'<>`\\") ||
		strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", errInvalid
	}
	return raw, nil
}

func ensureEdgeSecrets(ctx context.Context, st *store.Store, in *app.InstanceConfig) (map[string]string, error) {
	values, err := st.SettingValues(ctx, []string{"agent_sni", "sub_prefix", "link_prefix"})
	if err != nil {
		return nil, err
	}
	return fillEdgeSecrets(values, in), nil
}

// fillEdgeSecrets takes the stored agent_sni, sub_prefix and link_prefix from values, generates the ones that are
// missing (an older database) and returns those to be stored.
func fillEdgeSecrets(values map[string]string, in *app.InstanceConfig) map[string]string {
	fill := map[string]string{}
	var ok bool
	if in.AgentSNI, ok = values["agent_sni"]; !ok {
		in.AgentSNI = newAgentSNI(hostFromURL(in.PublicURL))
		fill["agent_sni"] = in.AgentSNI
	}
	if in.SubPrefix, ok = values["sub_prefix"]; !ok {
		in.SubPrefix = newSecretPrefix()
		fill["sub_prefix"] = in.SubPrefix
	}
	if in.LinkPrefix, ok = values["link_prefix"]; !ok {
		in.LinkPrefix = newSecretPrefix()
		fill["link_prefix"] = in.LinkPrefix
	}
	return fill
}

func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "com"
	}
	return u.Hostname()
}

func newSecretPrefix() string {
	var raw [15]byte
	_, _ = rand.Read(raw[:])
	return "/" + strings.ToLower(base32.StdEncoding.EncodeToString(raw[:])) + "/"
}

func newAgentSNI(domain string) string {
	if domain == "" {
		domain = "com"
	}
	var raw [10]byte
	_, _ = rand.Read(raw[:])
	return strings.ToLower(base32.StdEncoding.EncodeToString(raw[:])) + "." + domain
}

func splitList(value string) []string {
	var values []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, strings.TrimRight(part, "/"))
		}
	}
	return values
}
