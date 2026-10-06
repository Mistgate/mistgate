//go:build js && wasm

package main

import (
	"context"
	"crypto/rand"
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
)

type initOptions struct {
	d1        js.Value
	masterKey []byte
	publicURL string
	adminHost string
	adminPath string
	subPath   string
	rpID      string
	rpOrigins []string
	sourceURL string
}

func parseInitOptions(value js.Value) (initOptions, error) {
	var out initOptions
	d1 := value.Get("d1")
	if d1.Type() != js.TypeObject || d1.IsNull() {
		return out, errMissingD1
	}
	out.d1 = d1
	var err error
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
	if optionPresent(value, "sourceURL") {
		if out.sourceURL, err = stringOption(value, "sourceURL"); err != nil {
			return out, err
		}
	} else {
		out.sourceURL = "https://github.com/Mistgate/mistgate"
	}
	return out, nil
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

func loadEdgeInstance(ctx context.Context, st *store.Store, opts initOptions) (app.InstanceConfig, error) {
	rpID, err := st.Setting(ctx, "rp_id")
	if errors.Is(err, store.ErrNotFound) {
		return newEdgeInstance(ctx, st, opts)
	}
	if err != nil {
		return app.InstanceConfig{}, err
	}
	in := app.InstanceConfig{RPID: rpID}
	if origins, err := st.Setting(ctx, "rp_origins"); err != nil {
		return in, err
	} else if in.RPOrigins = splitList(origins); len(in.RPOrigins) == 0 {
		return in, errors.New("stored rp_origins setting is empty")
	}
	for key, target := range map[string]*string{
		"public_url": &in.PublicURL, "admin_host": &in.AdminHost,
		"admin_prefix": &in.AdminPrefix, "admin_listen": &in.AdminListen,
	} {
		if *target, err = st.Setting(ctx, key); err != nil {
			return in, err
		}
	}
	if in.AdminListen != "" && (in.AdminHost != "" || in.AdminPrefix != "/") {
		return in, errors.New("stored settings combine a separate admin listener with an admin host or path prefix")
	}
	if err := ensureEdgeSecrets(ctx, st, &in); err != nil {
		return in, err
	}
	return in, nil
}

func newEdgeInstance(ctx context.Context, st *store.Store, opts initOptions) (app.InstanceConfig, error) {
	var in app.InstanceConfig
	if opts.publicURL == "" {
		return in, errFreshPublicURL
	}
	publicURL, err := parsePublicURL(opts.publicURL)
	if err != nil {
		return in, err
	}
	in.PublicURL = publicURL.String()
	adminHost, err := parseAdminHost(opts.adminHost)
	if err != nil {
		return in, err
	}
	in.AdminHost = adminHost
	if in.AdminHost != "" && opts.adminPath != "" {
		return in, errAdminModeConflict
	}
	if in.AdminHost != "" {
		in.AdminPrefix = "/"
	} else if opts.adminPath == "" {
		in.AdminPrefix = newSecretPrefix()
	} else if in.AdminPrefix, err = validatePrefix(opts.adminPath, errInvalidAdminPrefix); err != nil {
		return in, err
	}
	if opts.subPath == "" {
		in.SubPrefix = newSecretPrefix()
	} else if in.SubPrefix, err = validatePrefix(opts.subPath, errInvalidSubPrefix); err != nil {
		return in, err
	}
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
	if err := st.SetSettings(ctx, map[string]string{
		"public_url": in.PublicURL, "admin_host": in.AdminHost,
		"admin_prefix": in.AdminPrefix, "admin_listen": "",
		"rp_id": in.RPID, "rp_origins": strings.Join(in.RPOrigins, ","),
		"agent_sni": in.AgentSNI, "sub_prefix": in.SubPrefix,
	}); err != nil {
		return in, err
	}
	return in, nil
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
	if len(raw) < 4 || !strings.HasPrefix(raw, "/") || !strings.HasSuffix(raw, "/") || strings.Contains(raw, "//") || strings.Contains(raw, "..") || strings.ContainsAny(raw, "?#") {
		return "", errInvalid
	}
	return raw, nil
}

func ensureEdgeSecrets(ctx context.Context, st *store.Store, in *app.InstanceConfig) error {
	fill := map[string]string{}
	var err error
	if in.AgentSNI, err = st.Setting(ctx, "agent_sni"); errors.Is(err, store.ErrNotFound) {
		in.AgentSNI = newAgentSNI(hostFromURL(in.PublicURL))
		fill["agent_sni"] = in.AgentSNI
	} else if err != nil {
		return err
	}
	if in.SubPrefix, err = st.Setting(ctx, "sub_prefix"); errors.Is(err, store.ErrNotFound) {
		in.SubPrefix = newSecretPrefix()
		fill["sub_prefix"] = in.SubPrefix
	} else if err != nil {
		return err
	}
	if len(fill) == 0 {
		return nil
	}
	return st.SetSettings(ctx, fill)
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
