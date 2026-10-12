package edgedeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

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

func newCFClient(base, token string, client *http.Client) *cfClient {
	if base == "" {
		base = cloudflareAPI
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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
	return errors.New(c.redactString(err.Error()))
}

func (c *cfClient) endpoint(path string) string {
	if strings.HasSuffix(c.base, "/client/v4") {
		return c.base + path
	}
	return c.base + "/client/v4" + path
}

func (c *cfClient) request(ctx context.Context, method, path, contentType string, body io.Reader) ([]byte, error) {
	return c.requestWithHeaders(ctx, method, path, contentType, body, nil)
}

func (c *cfClient) requestWithHeaders(ctx context.Context, method, path, contentType string, body io.Reader, extra http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, c.redactError(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, values := range extra {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.redactError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil {
		return nil, c.redactError(err)
	}
	if len(data) > maxAPIResponse {
		return nil, errors.New("Cloudflare API response exceeded the size limit")
	}
	data = []byte(c.redactString(string(data)))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{status: resp.StatusCode, text: c.redactString(cfErrorText(data))}
	}
	if err := checkCFSuccess(data); err != nil {
		return nil, c.redactError(err)
	}
	return data, nil
}

func checkCFSuccess(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var env struct {
		Success *bool        `json:"success"`
		Errors  []cfAPIError `json:"errors"`
	}
	if json.Unmarshal(trimmed, &env) != nil {
		return nil
	}
	if (env.Success != nil && !*env.Success) || len(env.Errors) != 0 {
		return errors.New(cfErrorMessages(env.Errors))
	}
	return nil
}

func (c *cfClient) json(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	contentType := ""
	if in != nil {
		contentType = "application/json"
	}
	data, err := c.request(ctx, method, path, contentType, body)
	if err != nil {
		return err
	}
	var env struct {
		Success *bool           `json:"success"`
		Errors  []cfAPIError    `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("decode Cloudflare API response: %w", err)
	}
	if (env.Success != nil && !*env.Success) || len(env.Errors) != 0 {
		return errors.New(c.redactString(cfErrorMessages(env.Errors)))
	}
	if out == nil || len(env.Result) == 0 || string(env.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("decode Cloudflare API result: %w", err)
	}
	return nil
}

type cfAPIError struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

func cfErrorText(data []byte) string {
	var env struct {
		Errors []cfAPIError `json:"errors"`
	}
	if json.Unmarshal(data, &env) == nil && len(env.Errors) != 0 {
		return cfErrorMessages(env.Errors)
	}
	return ""
}

func cfErrorMessages(errs []cfAPIError) string {
	if len(errs) == 0 {
		return "Cloudflare API request failed"
	}
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

func (c *cfClient) verifyToken(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := c.json(ctx, http.MethodGet, "/user/tokens/verify", nil, &result); err != nil {
		return fmt.Errorf("verify Cloudflare API token: %w", err)
	}
	if !strings.EqualFold(result.Status, "active") {
		return errors.New("Cloudflare API token is not active")
	}
	return nil
}

type account struct {
	ID string `json:"id"`
}

func (c *cfClient) accounts(ctx context.Context) ([]account, error) {
	var all []account
	for page := 1; page <= 100; page++ {
		var result []account
		path := fmt.Sprintf("/accounts?per_page=50&page=%d", page)
		if err := c.json(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, fmt.Errorf("list Cloudflare accounts: %w", err)
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
	path := "/zones?" + q.Encode()
	if err := c.json(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("list Cloudflare zones for host suffix %s: %w", name, err)
	}
	return result, nil
}

type d1Database struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

func (c *cfClient) databases(ctx context.Context, accountID, name string) ([]d1Database, error) {
	q := url.Values{}
	q.Set("name", name)
	q.Set("per_page", "100")
	var result []d1Database
	path := "/accounts/" + url.PathEscape(accountID) + "/d1/database?" + q.Encode()
	if err := c.json(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("find D1 database: %w", err)
	}
	return result, nil
}

func (c *cfClient) createDatabase(ctx context.Context, accountID, name, location string) (d1Database, error) {
	var result d1Database
	path := "/accounts/" + url.PathEscape(accountID) + "/d1/database"
	body := map[string]string{"name": name, "location": location}
	if err := c.json(ctx, http.MethodPost, path, body, &result); err != nil {
		return d1Database{}, fmt.Errorf("create D1 database: %w", err)
	}
	return result, nil
}

func (c *cfClient) scriptExists(ctx context.Context, accountID, name string) (bool, error) {
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name)
	_, err := c.request(ctx, http.MethodGet, path, "", nil)
	if err == nil {
		return true, nil
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("check Worker script %s: %w", name, err)
}

type uploadSession struct {
	JWT     string     `json:"jwt"`
	Buckets [][]string `json:"buckets"`
}

func (c *cfClient) assetSession(ctx context.Context, accountID, mainName string, manifest map[string]assetManifestEntry) (uploadSession, error) {
	var result uploadSession
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(mainName) + "/assets-upload-session"
	if err := c.json(ctx, http.MethodPost, path, map[string]any{"manifest": manifest}, &result); err != nil {
		return uploadSession{}, fmt.Errorf("create Worker assets upload session: %w", err)
	}
	return result, nil
}

func (c *cfClient) uploadAssetBucket(ctx context.Context, accountID, jwt string, bucket []string, assets map[string]assetContent) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	bucketJSON, err := json.Marshal(bucket)
	if err != nil {
		return err
	}
	if err := w.WriteField("bucket", string(bucketJSON)); err != nil {
		return err
	}
	for _, hash := range bucket {
		asset, ok := assets[hash]
		if !ok {
			return fmt.Errorf("Cloudflare requested unknown asset hash %s", hash)
		}
		if err := w.WriteField(hash, asset.base64); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/assets/upload?base64=true"
	headers := make(http.Header)
	headers.Set("CF-Worker-Assets-Token", jwt)
	if _, err := c.requestWithHeaders(ctx, http.MethodPost, path, w.FormDataContentType(), &buf, headers); err != nil {
		return fmt.Errorf("upload Worker asset bucket: %w", err)
	}
	return nil
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
	if _, err := c.request(ctx, http.MethodPut, path, w.FormDataContentType(), &buf); err != nil {
		return fmt.Errorf("upload Worker script %s: %w", name, err)
	}
	return nil
}

func (c *cfClient) putSchedules(ctx context.Context, accountID, name string, crons []string) error {
	schedules := make([]map[string]string, 0, len(crons))
	for _, cron := range crons {
		schedules = append(schedules, map[string]string{"cron": cron})
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name) + "/schedules"
	if err := c.json(ctx, http.MethodPut, path, map[string]any{"schedules": schedules}, nil); err != nil {
		return fmt.Errorf("set Worker schedules: %w", err)
	}
	return nil
}

func (c *cfClient) putSubdomain(ctx context.Context, accountID, name string) error {
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(name) + "/subdomain"
	body := map[string]bool{"enabled": false, "previews_enabled": false}
	if err := c.json(ctx, http.MethodPut, path, body, nil); err != nil {
		return fmt.Errorf("disable Worker subdomain for %s: %w", name, err)
	}
	return nil
}

func (c *cfClient) putDomain(ctx context.Context, accountID, host, service string) error {
	path := "/accounts/" + url.PathEscape(accountID) + "/workers/domains"
	body := map[string]string{"hostname": host, "service": service, "environment": "production"}
	if err := c.json(ctx, http.MethodPut, path, body, nil); err != nil {
		return fmt.Errorf("attach custom domain: %w", err)
	}
	return nil
}

func publicGet(ctx context.Context, client *http.Client, rawURL string) (int, error) {
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
