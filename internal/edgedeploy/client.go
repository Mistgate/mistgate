package edgedeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// cloudflareAPI is the API host; every path below is appended to /client/v4.
const cloudflareAPI = "https://api.cloudflare.com"

const maxAPIResponse = 8 << 20

type cfClient struct {
	base       string
	token      string
	redactions []string
	http       *http.Client
}

type httpStatusError struct {
	status int
	text   string
}

func (e *httpStatusError) Error() string {
	if e.text == "" {
		return fmt.Sprintf("Cloudflare API returned HTTP %d", e.status)
	}
	return fmt.Sprintf("Cloudflare API returned HTTP %d: %s", e.status, e.text)
}

// newHTTPClient has no overall Client.Timeout: that would cut a ~40 MB script upload (or the bundle download) short. The
// connection phases are bounded by the transport and the whole call by the caller's context.
func newHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = 15 * time.Second
	t.ResponseHeaderTimeout = 2 * time.Minute
	return &http.Client{Transport: t}
}

func newCFClient(base, token string, client *http.Client) *cfClient {
	if base == "" {
		base = cloudflareAPI
	}
	if client == nil {
		client = newHTTPClient()
	}
	return &cfClient{base: strings.TrimRight(base, "/"), token: token, redactions: []string{token}, http: client}
}

func (c *cfClient) addSecrets(secrets ...string) {
	for _, secret := range secrets {
		if secret != "" {
			c.redactions = append(c.redactions, secret)
		}
	}
}

func (c *cfClient) redactString(s string) string {
	for _, secret := range c.redactions {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}

func (c *cfClient) redactError(err error) error {
	if err == nil {
		return nil
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return err // its text was redacted when it was built
	}
	return errors.New(c.redactString(err.Error()))
}

type cfAPIError struct {
	Message string `json:"message"`
}

type cfEnvelope struct {
	Success *bool           `json:"success"`
	Errors  []cfAPIError    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

func cfErrorMessages(errs []cfAPIError) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Message != "" {
			msgs = append(msgs, e.Message)
		}
	}
	if len(msgs) == 0 {
		return "Cloudflare API request failed"
	}
	return strings.Join(msgs, "; ")
}

// call sends one request, checks the status and Cloudflare's envelope, and decodes the envelope's result into out (if any).
// bearer is the token to send; "" means the API token (the asset upload sends its own session token instead).
func (c *cfClient) call(ctx context.Context, method, path, contentType string, body io.Reader, bearer string, out any) error {
	return c.redactError(c.do(ctx, method, path, contentType, body, bearer, out))
}

func (c *cfClient) do(ctx context.Context, method, path, contentType string, body io.Reader, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/client/v4"+path, body)
	if err != nil {
		return err
	}
	if bearer == "" {
		bearer = c.token
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maxAPIResponse {
		return errors.New("Cloudflare API response exceeded the size limit")
	}
	var env cfEnvelope
	envErr := json.Unmarshal(data, &env)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := ""
		if envErr == nil && len(env.Errors) != 0 {
			text = c.redactString(cfErrorMessages(env.Errors))
		}
		return &httpStatusError{status: resp.StatusCode, text: text}
	}
	if envErr != nil {
		return fmt.Errorf("decode Cloudflare API response: %w", envErr)
	}
	if (env.Success != nil && !*env.Success) || len(env.Errors) != 0 {
		return errors.New(cfErrorMessages(env.Errors))
	}
	if out == nil || len(env.Result) == 0 || string(env.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("decode Cloudflare API result: %w", err)
	}
	return nil
}

func (c *cfClient) json(ctx context.Context, method, path string, in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.call(ctx, method, path, "application/json", bytes.NewReader(b), "", out)
}

func (c *cfClient) get(ctx context.Context, path string, out any) error {
	return c.call(ctx, http.MethodGet, path, "", nil, "", out)
}

// authFailure turns a 401/403 into a message that says what to check; any other error passes through.
func authFailure(err error) error {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && (statusErr.status == http.StatusUnauthorized || statusErr.status == http.StatusForbidden) {
		msg := fmt.Sprintf("Cloudflare rejected the API token (HTTP %d): check CLOUDFLARE_API_TOKEN, that it has not expired, and that it may read accounts and zones and edit Workers scripts, Workers routes and D1", statusErr.status)
		if statusErr.text != "" {
			msg += " (" + statusErr.text + ")"
		}
		return errors.New(msg)
	}
	return err
}

type account struct {
	ID string `json:"id"`
}

// accounts is the first request of a deploy, so it is also the token check (GET /user/tokens/verify refuses account-owned tokens).
func (c *cfClient) accounts(ctx context.Context) ([]account, error) {
	var all []account
	for page := 1; page <= 100; page++ {
		var result []account
		if err := c.get(ctx, fmt.Sprintf("/accounts?per_page=50&page=%d", page), &result); err != nil {
			return nil, fmt.Errorf("list Cloudflare accounts: %w", authFailure(err))
		}
		all = append(all, result...)
		if len(result) < 50 {
			return all, nil
		}
	}
	return nil, errors.New("Cloudflare returned too many accounts")
}

type zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (c *cfClient) zones(ctx context.Context, accountID, name string) ([]zone, error) {
	q := url.Values{}
	q.Set("name", name)
	q.Set("account.id", accountID)
	q.Set("per_page", "50")
	var result []zone
	if err := c.get(ctx, "/zones?"+q.Encode(), &result); err != nil {
		return nil, fmt.Errorf("list Cloudflare zones for host suffix %s: %w", name, authFailure(err))
	}
	return result, nil
}

type d1Database struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// databases lists the D1 databases whose name is exactly name. The API's name filter is not documented as exact, so the
// result is filtered here.
func (c *cfClient) databases(ctx context.Context, accountID, name string) ([]d1Database, error) {
	const perPage = 100
	var exact []d1Database
	for page := 1; page <= 50; page++ {
		q := url.Values{}
		q.Set("name", name)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(perPage))
		var result []d1Database
		if err := c.get(ctx, "/accounts/"+url.PathEscape(accountID)+"/d1/database?"+q.Encode(), &result); err != nil {
			return nil, fmt.Errorf("find D1 database: %w", err)
		}
		for _, db := range result {
			if db.Name == name {
				exact = append(exact, db)
			}
		}
		if len(result) < perPage {
			return exact, nil
		}
	}
	return nil, errors.New("find D1 database: too many results")
}

func (c *cfClient) createDatabase(ctx context.Context, accountID, name, location string) (d1Database, error) {
	var result d1Database
	body := map[string]string{"name": name, "primary_location_hint": location}
	if err := c.json(ctx, http.MethodPost, "/accounts/"+url.PathEscape(accountID)+"/d1/database", body, &result); err != nil {
		return d1Database{}, fmt.Errorf("create D1 database: %w", err)
	}
	return result, nil
}

// scripts maps the name of every Worker script of the account to its migration tag ("" if it has none). One list call
// replaces a GET per script: GET .../scripts/{name} returns the script's source, not JSON.
func (c *cfClient) scripts(ctx context.Context, accountID string) (map[string]string, error) {
	var result []struct {
		ID           string `json:"id"`
		MigrationTag string `json:"migration_tag"`
	}
	if err := c.get(ctx, "/accounts/"+url.PathEscape(accountID)+"/workers/scripts", &result); err != nil {
		return nil, fmt.Errorf("list Worker scripts: %w", err)
	}
	tags := make(map[string]string, len(result))
	for _, s := range result {
		tags[s.ID] = s.MigrationTag
	}
	return tags, nil
}

type uploadSession struct {
	JWT     string     `json:"jwt"`
	Buckets [][]string `json:"buckets"`
}

// uploadAssets runs the direct-upload flow for a script and returns the completion token that its next PUT carries
// in assets.jwt. It is called before every PUT of a script with assets: a hash Cloudflare already has is not asked for again,
// so the second call usually has no buckets and the session token is itself the completion token.
func (c *cfClient) uploadAssets(ctx context.Context, accountID, script string, manifest map[string]assetManifestEntry, contents map[string]assetContent) (string, error) {
	var session uploadSession
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(script) + "/assets-upload-session"
	if err := c.json(ctx, http.MethodPost, path, map[string]any{"manifest": manifest}, &session); err != nil {
		return "", fmt.Errorf("create Worker assets upload session: %w", err)
	}
	if session.JWT == "" {
		return "", errors.New("Cloudflare assets upload session returned no token")
	}
	c.addSecrets(session.JWT)
	completion, uploaded := "", false
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			continue
		}
		uploaded = true
		var err error
		if completion, err = c.uploadAssetBucket(ctx, accountID, session.JWT, bucket, contents); err != nil {
			return "", err
		}
		c.addSecrets(completion)
	}
	if !uploaded {
		return session.JWT, nil
	}
	if completion == "" {
		return "", errors.New("Cloudflare returned no completion token after the last asset upload")
	}
	return completion, nil
}

// uploadAssetBucket posts one bucket, authenticated with the session token (never the API token). The response of the
// upload that completes the manifest carries the completion token; the others carry none.
func (c *cfClient) uploadAssetBucket(ctx context.Context, accountID, sessionJWT string, bucket []string, assets map[string]assetContent) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, hash := range bucket {
		asset, ok := assets[hash]
		if !ok {
			return "", fmt.Errorf("Cloudflare requested unknown asset hash %s", hash)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, hash, hash))
		header.Set("Content-Type", asset.contentType)
		part, err := w.CreatePart(header)
		if err != nil {
			return "", err
		}
		if _, err := io.WriteString(part, asset.base64); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var result struct {
		JWT string `json:"jwt"`
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/assets/upload?base64=true"
	if err := c.call(ctx, http.MethodPost, path, w.FormDataContentType(), &buf, sessionJWT, &result); err != nil {
		return "", fmt.Errorf("upload Worker asset bucket: %w", err)
	}
	return result.JWT, nil
}

func (c *cfClient) putScript(ctx context.Context, accountID, name string, metadata scriptMetadata, modules map[string][]byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	metaHeader := make(textproto.MIMEHeader)
	metaHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metaHeader.Set("Content-Type", "application/json")
	part, err := w.CreatePart(metaHeader)
	if err != nil {
		return err
	}
	if _, err := part.Write(meta); err != nil {
		return err
	}
	for _, module := range metadata.Modules {
		content, ok := modules[module.Name]
		if !ok {
			return fmt.Errorf("bundle is missing Worker module %s", module.Name)
		}
		contentType := "application/octet-stream"
		switch module.Type {
		case "esm":
			contentType = "application/javascript+module"
		case "compiled_wasm":
			contentType = "application/wasm"
		}
		moduleHeader := make(textproto.MIMEHeader)
		moduleHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, module.Name, module.Name))
		moduleHeader.Set("Content-Type", contentType)
		part, err := w.CreatePart(moduleHeader)
		if err != nil {
			return err
		}
		if _, err := part.Write(content); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name)
	if err := c.call(ctx, http.MethodPut, path, w.FormDataContentType(), &buf, "", nil); err != nil {
		return fmt.Errorf("upload Worker script %s: %w", name, err)
	}
	return nil
}

// putSchedules replaces the cron triggers; the body is a bare JSON array.
func (c *cfClient) putSchedules(ctx context.Context, accountID, name string, crons []string) error {
	schedules := make([]map[string]string, 0, len(crons))
	for _, cron := range crons {
		schedules = append(schedules, map[string]string{"cron": cron})
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name) + "/schedules"
	if err := c.json(ctx, http.MethodPut, path, schedules, nil); err != nil {
		return fmt.Errorf("set Worker schedules: %w", err)
	}
	return nil
}

// deploymentVersion is one version of a deployment and the share of the traffic it takes.
type deploymentVersion struct {
	VersionID  string  `json:"version_id"`
	Percentage float64 `json:"percentage"`
}

// activeDeployment returns the versions of the script's current deployment: the first of the list, which the API sorts newest
// first. nil if the script has no deployment.
func (c *cfClient) activeDeployment(ctx context.Context, accountID, name string) ([]deploymentVersion, error) {
	var result struct {
		Deployments []struct {
			Versions []deploymentVersion `json:"versions"`
		} `json:"deployments"`
	}
	if err := c.get(ctx, "/accounts/"+url.PathEscape(accountID)+"/workers/scripts/"+url.PathEscape(name)+"/deployments", &result); err != nil {
		return nil, fmt.Errorf("read the deployment of Worker %s: %w", name, err)
	}
	if len(result.Deployments) == 0 {
		return nil, nil
	}
	return result.Deployments[0].Versions, nil
}

// setDeployment makes the given versions the active deployment of the script. force is needed because a secret may have been
// changed since the version was uploaded (Cloudflare refuses a deployment of older secrets otherwise).
func (c *cfClient) setDeployment(ctx context.Context, accountID, name string, versions []deploymentVersion) error {
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name) + "/deployments?force=true"
	body := map[string]any{"strategy": "percentage", "versions": versions}
	if err := c.json(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("set the deployment of Worker %s: %w", name, err)
	}
	return nil
}

// schedules returns the cron expressions of the script, in the order the API gives them.
func (c *cfClient) schedules(ctx context.Context, accountID, name string) ([]string, error) {
	var result struct {
		Schedules []struct {
			Cron string `json:"cron"`
		} `json:"schedules"`
	}
	if err := c.get(ctx, "/accounts/"+url.PathEscape(accountID)+"/workers/scripts/"+url.PathEscape(name)+"/schedules", &result); err != nil {
		return nil, fmt.Errorf("read the schedules of Worker %s: %w", name, err)
	}
	crons := make([]string, 0, len(result.Schedules))
	for _, s := range result.Schedules {
		crons = append(crons, s.Cron)
	}
	return crons, nil
}

// disableSubdomain turns off the script's workers.dev name and its preview URLs (POST, not PUT).
func (c *cfClient) disableSubdomain(ctx context.Context, accountID, name string) error {
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name) + "/subdomain"
	body := map[string]bool{"enabled": false, "previews_enabled": false}
	if err := c.json(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("disable Worker subdomain for %s: %w", name, err)
	}
	return nil
}

func (c *cfClient) putDomain(ctx context.Context, accountID, zoneID, host, service string) error {
	body := map[string]string{"hostname": host, "service": service, "zone_id": zoneID}
	if err := c.json(ctx, http.MethodPut, "/accounts/"+url.PathEscape(accountID)+"/workers/domains", body, nil); err != nil {
		return fmt.Errorf("attach custom domain: %w", err)
	}
	return nil
}

// publicGet fetches the public decoy page once; a stalled server is cut off after 15 seconds.
func publicGet(ctx context.Context, client *http.Client, rawURL string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

// newReachabilityClient pins the final custom-domain poll to addresses supplied by Resolve while leaving the URL hostname
// intact for TLS SNI and the HTTP Host header. Copying the client and transport preserves their configured timeouts.
func newReachabilityClient(client *http.Client, resolve func(context.Context, string) ([]netip.Addr, error)) (*http.Client, error) {
	if resolve == nil {
		return nil, errors.New("no resolver configured")
	}
	if client == nil {
		client = newHTTPClient()
	}
	var base *http.Transport
	switch t := client.Transport.(type) {
	case nil:
		base = http.DefaultTransport.(*http.Transport)
	case *http.Transport:
		base = t
	default:
		return nil, errors.New("HTTP client transport does not support resolver pinning")
	}
	transport := base.Clone()
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var addrs []netip.Addr
		if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
			addrs = []netip.Addr{ip}
		} else {
			addrs, err = resolve(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", host, err)
			}
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}
		var dialErrors []error
		for _, addr := range addrs {
			if !addr.IsValid() {
				dialErrors = append(dialErrors, errors.New("resolver returned an invalid address"))
				continue
			}
			conn, err := dial(ctx, network, net.JoinHostPort(addr.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, err)
		}
		return nil, errors.Join(dialErrors...)
	}
	out := *client
	out.Transport = transport
	return &out, nil
}

func waitContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
