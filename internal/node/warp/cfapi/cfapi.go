// Package cfapi is the Cloudflare WARP registration client and the importer for
// wgcf profiles. It is used by the panel: one address registers every node, and the
// agent stays free of the uTLS stack. Nothing here runs by itself: every call to Cloudflare is an explicit call of
// Register, Fetch or Delete, which the panel makes only on the owner's click (or the opt-in auto re-registration).
//
// The constants a registration request is built from changed twice in 2026 and broke every client each time
// (Cloudflare answers HTTP 429 to a request that does not look like the Android app: API version in the path,
// User-Agent, CF-Client-Version and the TLS ClientHello). They are Params, data the panel stores and the owner
// can change, not code. The ClientHello is chosen by name (TLSSpecID) from a registry.
//
// The request format, headers and the uTLS ClientHello are taken from wgcf v2.3.0 (MIT, see tls.go for the notice).
// Tests talk only to a fake API server (NewTLS test server through Client.BaseURL and Client.RootCAs); the live
// service is never contacted from the test suite.
package cfapi

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	// BaseURL is the registration API.
	BaseURL = "https://api.cloudflareclient.com"
	// TOSURL is the terms page the UI links next to the register button.
	TOSURL = "https://www.cloudflare.com/application/terms/"

	maxBody = 1 << 20
)

// Params are the registration constants. Empty fields fall back to Defaults.
type Params struct {
	// APIVersion is the path segment before /reg ("v0a5641").
	APIVersion string
	// UserAgent and CFClientVersion imitate the Android app.
	UserAgent       string
	CFClientVersion string
	// TLSSpecID names the uTLS ClientHello ("wgcf_v2.3.0"), see TLSSpecIDs.
	TLSSpecID string
}

// Defaults are the values of wgcf v2.3.0 (2026-09-18).
func Defaults() Params {
	return Params{
		APIVersion:      "v0a5641",
		UserAgent:       "1.1.1.1/6.38.9-5641 (Android 16.0.0)",
		CFClientVersion: "a-6.38.9-5641",
		TLSSpecID:       "wgcf_v2.3.0",
	}
}

// WithDefaults fills empty fields from Defaults.
func (p Params) WithDefaults() Params {
	d := Defaults()
	if p.APIVersion == "" {
		p.APIVersion = d.APIVersion
	}
	if p.UserAgent == "" {
		p.UserAgent = d.UserAgent
	}
	if p.CFClientVersion == "" {
		p.CFClientVersion = d.CFClientVersion
	}
	if p.TLSSpecID == "" {
		p.TLSSpecID = d.TLSSpecID
	}
	return p
}

// Validate rejects values that cannot go into a URL or a header, or name an unknown ClientHello.
func (p Params) Validate() error {
	p = p.WithDefaults()
	if strings.ContainsAny(p.APIVersion, "/?#% \r\n") {
		return errors.New("cfapi: bad api version")
	}
	for _, v := range []string{p.UserAgent, p.CFClientVersion} {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("cfapi: bad header value")
		}
	}
	if _, ok := tlsSpecs[p.TLSSpecID]; !ok {
		return fmt.Errorf("cfapi: unknown tls spec %q", p.TLSSpecID)
	}
	return nil
}

// Secret is a string that prints as [REDACTED] everywhere it is formatted or logged; Reveal returns the value.
type Secret string

const redacted = "[REDACTED]"

func (Secret) String() string                 { return redacted }
func (Secret) GoString() string               { return redacted }
func (Secret) LogValue() slog.Value           { return slog.StringValue(redacted) }
func (s Secret) Reveal() string               { return string(s) }
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Account is a WARP device account in the forms the panel stores and the agent's WarpSpec needs. Endpoints are IP
// literals without a port; addresses carry their prefix length.
type Account struct {
	ID          string
	Token       Secret // Bearer secret for Fetch/Delete; empty for an imported profile without wgcf-account.toml
	License     Secret
	AccountType string // "free" | "plus" | ""
	ClientID    string // base64, 3 bytes: the source of Reserved

	PrivateKey    Secret // base64 X25519
	PeerPublicKey string
	EndpointHost  string // "engage.cloudflareclient.com:2408": informational, never dialed
	EndpointV4    string
	EndpointV6    string
	Ports         []uint16
	AddressV4     string // "172.16.0.2/32"
	AddressV6     string // "" = none
	MTU           uint16
	Reserved      []byte // 0 or 3 bytes
}

// APIError is a non-200 answer of the API.
type APIError struct {
	Status int
	// Body is the first bytes of the answer (Cloudflare error JSON; it carries no secrets).
	Body string
}

func (e *APIError) Error() string { return fmt.Sprintf("cloudflare: HTTP %d", e.Status) }

// IsRateLimited reports an HTTP 429: Cloudflare dislikes the caller (TLS fingerprint, headers, too many
// registrations from one address). The remedy is to import a wgcf profile, not to hammer.
func IsRateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests
}

// IsNotFound reports an HTTP 404 (registration unknown or already deleted).
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Client talks to the API. The zero value is usable (defaults, the real base URL).
type Client struct {
	Params Params
	// BaseURL overrides BaseURL (tests).
	BaseURL string
	// RootCAs verifies the server certificate; nil = system roots. Tests put their server's CA here.
	RootCAs *x509.CertPool
	// Timeout for one call, dial and handshake included (30 s).
	Timeout time.Duration
	// Rand is the key source (crypto/rand).
	Rand io.Reader
}

type regRequest struct {
	FcmToken     string `json:"fcm_token"`
	InstallID    string `json:"install_id"`
	Key          string `json:"key"`
	Locale       string `json:"locale"`
	Model        string `json:"model"`
	Tos          string `json:"tos"`
	SerialNumber string `json:"serial_number"`
	OsVersion    string `json:"os_version"`
	KeyType      string `json:"key_type"`
	TunnelType   string `json:"tunnel_type"`
}

type regResponse struct {
	ID          string `json:"id"`
	Token       string `json:"token"`
	Key         string `json:"key"`
	WarpEnabled bool   `json:"warp_enabled"`
	Account     struct {
		AccountType string `json:"account_type"`
		License     string `json:"license"`
		WarpPlus    bool   `json:"warp_plus"`
	} `json:"account"`
	Config struct {
		ClientID  string `json:"client_id"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
		Peers []struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				Host  string  `json:"host"`
				V4    string  `json:"v4"`
				V6    string  `json:"v6"`
				Ports []int64 `json:"ports"`
			} `json:"endpoint"`
		} `json:"peers"`
	} `json:"config"`
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return BaseURL
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

func (c *Client) randReader() io.Reader {
	if c.Rand != nil {
		return c.Rand
	}
	return rand.Reader
}

// GenerateKey makes a WireGuard key pair (base64): a clamped X25519 private key and its public key.
func GenerateKey(r io.Reader) (priv, pub string, err error) {
	if r == nil {
		r = rand.Reader
	}
	var b [32]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", "", err
	}
	b[0] &= 248
	b[31] = (b[31] & 127) | 64
	k, err := ecdh.X25519().NewPrivateKey(b[:])
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// Register creates a new anonymous WARP device: a fresh key pair, POST /reg. tos is when the owner accepted the
// terms (TOSURL); it goes into the request like the official app does. Creates a Cloudflare account: only call it
// on the owner's action.
func (c *Client) Register(ctx context.Context, tos time.Time) (Account, error) {
	if tos.IsZero() {
		return Account{}, errors.New("cfapi: the terms must be accepted (tos time is zero)")
	}
	p := c.Params.WithDefaults()
	if err := p.Validate(); err != nil {
		return Account{}, err
	}
	priv, pub, err := GenerateKey(c.randReader())
	if err != nil {
		return Account{}, fmt.Errorf("cfapi: generate key: %w", err)
	}
	body, _ := json.Marshal(regRequest{
		Key: pub, Locale: "en_US", Model: "PC", Tos: tos.Format(time.RFC3339Nano),
		OsVersion: "16.0.0", KeyType: "curve25519", TunnelType: "wireguard",
	})
	resp, err := c.do(ctx, p, http.MethodPost, "/"+p.APIVersion+"/reg", "", body)
	if err != nil {
		return Account{}, err
	}
	a, err := resp.account()
	if err != nil {
		return Account{}, err
	}
	a.PrivateKey = Secret(priv)
	return a, nil
}

// Fetch reads the registration again (GET /reg/{id}): endpoint, addresses and client id may have changed. It
// creates nothing. privateKey is the account's own key (Cloudflare never returns it); it is copied into the result.
func (c *Client) Fetch(ctx context.Context, id, token, privateKey string) (Account, error) {
	p := c.Params.WithDefaults()
	if err := p.Validate(); err != nil {
		return Account{}, err
	}
	if err := checkID(id); err != nil {
		return Account{}, err
	}
	resp, err := c.do(ctx, p, http.MethodGet, "/"+p.APIVersion+"/reg/"+id, token, nil)
	if err != nil {
		return Account{}, err
	}
	a, err := resp.account()
	if err != nil {
		return Account{}, err
	}
	a.PrivateKey = Secret(privateKey)
	if a.Token == "" {
		a.Token = Secret(token)
	}
	if a.ID == "" {
		a.ID = id
	}
	return a, nil
}

// Delete removes our registration at Cloudflare (DELETE /reg/{id}); the token is required.
func (c *Client) Delete(ctx context.Context, id, token string) error {
	p := c.Params.WithDefaults()
	if err := p.Validate(); err != nil {
		return err
	}
	if err := checkID(id); err != nil {
		return err
	}
	if token == "" {
		return errors.New("cfapi: no access token")
	}
	_, err := c.do(ctx, p, http.MethodDelete, "/"+p.APIVersion+"/reg/"+id, token, nil)
	return err
}

func checkID(id string) error {
	if id == "" || len(id) > 64 || strings.ContainsAny(id, "/?#% \r\n") {
		return errors.New("cfapi: bad registration id")
	}
	return nil
}

type reply struct {
	status int
	body   []byte
}

func (c *Client) do(ctx context.Context, p Params, method, path, token string, body []byte) (*reply, error) {
	u, err := url.Parse(c.base() + path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("CF-Client-Version", p.CFClientVersion)
	req.Header.Set("Connection", "Keep-Alive")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	tr, err := newTransport(p.TLSSpecID, c.RootCAs)
	if err != nil {
		return nil, err
	}
	defer tr.CloseIdleConnections()
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		var ue *url.Error // its text repeats the URL, and with it the registration id
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("cfapi: %s %s: %w", method, redactURL(path), err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("cfapi: read answer: %w", err)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		short := b
		if len(short) > 300 {
			short = short[:300]
		}
		return nil, &APIError{Status: res.StatusCode, Body: string(short)}
	}
	return &reply{status: res.StatusCode, body: b}, nil
}

// redactURL keeps the registration id out of error strings: the id is half of the credential pair.
func redactURL(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 3 {
		parts[3] = "{id}"
	}
	return strings.Join(parts, "/")
}

func (r *reply) account() (Account, error) {
	var d regResponse
	if err := json.Unmarshal(r.body, &d); err != nil {
		return Account{}, fmt.Errorf("cfapi: bad answer: %w", err)
	}
	if len(d.Config.Peers) == 0 {
		return Account{}, errors.New("cfapi: answer has no peer")
	}
	pe := d.Config.Peers[0]
	a := Account{
		ID: d.ID, Token: Secret(d.Token), License: Secret(d.Account.License), AccountType: accountType(d.Account.AccountType, d.Account.WarpPlus),
		ClientID: d.Config.ClientID, PeerPublicKey: pe.PublicKey, EndpointHost: pe.Endpoint.Host, MTU: 1280,
	}
	if k, err := base64.StdEncoding.DecodeString(pe.PublicKey); err != nil || len(k) != 32 {
		return Account{}, errors.New("cfapi: answer has a bad peer key")
	}
	var err error
	if a.EndpointV4, err = stripPort(pe.Endpoint.V4, false); err != nil {
		return Account{}, fmt.Errorf("cfapi: endpoint v4: %w", err)
	}
	if a.EndpointV6, err = stripPort(pe.Endpoint.V6, true); err != nil {
		return Account{}, fmt.Errorf("cfapi: endpoint v6: %w", err)
	}
	if a.EndpointV4 == "" && a.EndpointV6 == "" {
		return Account{}, errors.New("cfapi: answer has no endpoint address")
	}
	for _, p := range pe.Endpoint.Ports {
		if p > 0 && p <= 65535 {
			a.Ports = append(a.Ports, uint16(p))
		}
	}
	if len(a.Ports) == 0 {
		a.Ports = []uint16{2408, 500, 1701, 4500}
	}
	if a.AddressV4, err = hostPrefix(d.Config.Interface.Addresses.V4, false); err != nil || a.AddressV4 == "" {
		return Account{}, errors.New("cfapi: answer has no IPv4 address")
	}
	if a.AddressV6, err = hostPrefix(d.Config.Interface.Addresses.V6, true); err != nil {
		return Account{}, fmt.Errorf("cfapi: address v6: %w", err)
	}
	a.Reserved = reservedOf(d.Config.ClientID)
	return a, nil
}

func accountType(t string, plus bool) string {
	if plus {
		return "plus"
	}
	switch t {
	case "free", "plus":
		return t
	}
	return ""
}

// reservedOf is the first three bytes of the base64 client id; nil when there are not three.
func reservedOf(clientID string) []byte {
	b, err := base64.StdEncoding.DecodeString(clientID)
	if err != nil || len(b) < 3 {
		return nil
	}
	return append([]byte(nil), b[:3]...)
}

// stripPort turns "162.159.192.1:0" / "[2606:4700:d0::a29f:c001]:0" into the bare literal. "" stays "".
func stripPort(s string, v6 bool) (string, error) {
	if s == "" {
		return "", nil
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return checkFamily(ap.Addr(), v6)
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return "", errors.New("not an IP literal")
	}
	return checkFamily(a, v6)
}

func checkFamily(a netip.Addr, v6 bool) (string, error) {
	a = a.Unmap()
	if a.Is6() != v6 || a.IsUnspecified() || a.IsMulticast() {
		return "", errors.New("wrong family or not a unicast address")
	}
	return a.String(), nil
}

// hostPrefix turns "172.16.0.2" into "172.16.0.2/32" (a /128 for IPv6). "" stays "".
func hostPrefix(s string, v6 bool) (string, error) {
	if s == "" {
		return "", nil
	}
	if pf, err := netip.ParsePrefix(s); err == nil {
		if _, err := checkFamily(pf.Addr(), v6); err != nil {
			return "", err
		}
		return pf.String(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", errors.New("not an address")
	}
	if _, err := checkFamily(a, v6); err != nil {
		return "", err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()).String(), nil
}
