package warp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Client is the Cloudflare client API (the one the 1.1.1.1 app and wgcf talk to), limited to what the panel
// needs: register a device, read it back, delete it. Every call is made on the owner's click (see the package
// comment). It never logs a body, a header or a token.
type Client struct {
	base string
	hc   *http.Client
}

// NewClient returns a client for APIURL whose TLS ClientHello imitates the Android app, per p.TLSSpecID.
func NewClient(p Params) (*Client, error) {
	tr, err := newTransport(p.fill().TLSSpecID, nil)
	if err != nil {
		return nil, err
	}
	return &Client{base: APIURL, hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}}, nil
}

// NewClientFor is a client for another base URL and transport (tests run a fake API).
func NewClientFor(base string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(base, "/"), hc: hc}
}

// APIError is a non-2xx answer. Message is Cloudflare's own error text (it carries no secret of ours), cut short.
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("cloudflare answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("cloudflare answered HTTP %d: %s", e.Status, e.Message)
}

// apiStatus is the HTTP status of an APIError in err, 0 when err is not one.
func apiStatus(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Registration is what the panel keeps from a registration answer. Endpoints are IP literals without a port.
type Registration struct {
	ID, Token, License, AccountType string // secrets except AccountType
	ClientID                        string // base64; its first three bytes are the "reserved" bytes
	PeerPublicKey                   string
	EndpointV4, EndpointV6          string
	Ports                           []uint16
	AddressV4, AddressV6            string // with prefix: "172.16.0.2/32", "2606:4700:110:1::2/128"
}

type regResponse struct {
	ID      string `json:"id"`
	Token   string `json:"token"`
	Account struct {
		AccountType string `json:"account_type"`
		License     string `json:"license"`
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
				V4    string  `json:"v4"`
				V6    string  `json:"v6"`
				Ports []int64 `json:"ports"`
			} `json:"endpoint"`
		} `json:"peers"`
	} `json:"config"`
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

// defaultPorts are the consumer WARP ingress ports in the order to try them.
var defaultPorts = []uint16{2408, 500, 1701, 4500}

// Register creates an anonymous device for the WireGuard public key pub (base64) and returns the account.
// tos is the moment the owner accepted the terms. An answer of 429 is an *APIError with Status 429.
func (c *Client) Register(ctx context.Context, p Params, pub string, tos time.Time) (Registration, error) {
	body, _ := json.Marshal(regRequest{Key: pub, Locale: "en_US", Model: "PC", Tos: tos.Format(time.RFC3339Nano),
		OsVersion: "16.0.0", KeyType: "curve25519", TunnelType: "wireguard"})
	return c.do(ctx, p, http.MethodPost, "/reg", "", body)
}

// Get reads a registered device back (its endpoint, addresses and client id may have changed).
func (c *Client) Get(ctx context.Context, p Params, id, token string) (Registration, error) {
	return c.do(ctx, p, http.MethodGet, "/reg/"+url.PathEscape(id), token, nil)
}

// Delete removes our own device. An answer of 404 (already gone) is success.
func (c *Client) Delete(ctx context.Context, p Params, id, token string) error {
	_, err := c.roundTrip(ctx, p, http.MethodDelete, "/reg/"+url.PathEscape(id), token, nil)
	if apiStatus(err) == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *Client) do(ctx context.Context, p Params, method, path, token string, body []byte) (Registration, error) {
	raw, err := c.roundTrip(ctx, p, method, path, token, body)
	if err != nil {
		return Registration{}, err
	}
	var r regResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return Registration{}, errors.New("cloudflare answer is not JSON")
	}
	// Only the registration answer carries "token"; GET /reg/{id} does not (checked live, 2026-10-01). The caller
	// already holds the token it authenticated with.
	if r.Token == "" {
		r.Token = token
	}
	return r.registration()
}

func (c *Client) roundTrip(ctx context.Context, p Params, method, path, token string, body []byte) ([]byte, error) {
	p = p.fill()
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/"+p.APIVersion+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// The request the Android app makes: its user agent and client version, JSON with an explicit charset, no Accept.
	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("CF-Client-Version", p.CFClientVersion)
	req.Header.Set("Connection", "Keep-Alive")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// The URL and the transport error carry no secret, but keep it to the class: the caller logs and maps it.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, errors.New("cloudflare request timed out")
		}
		return nil, fmt.Errorf("cloudflare request failed: %w", unwrapURLError(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("cloudflare answer cut off")
	}
	if resp.StatusCode/100 != 2 {
		return nil, apiError(resp.StatusCode, raw)
	}
	return raw, nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// apiError reads Cloudflare's {"errors":[{"code":1006,"message":"..."}]} envelope; the text is cut to 200 bytes and
// printable characters so that it is safe in a log line.
func apiError(status int, raw []byte) *APIError {
	e := &APIError{Status: status}
	var env struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Errors) > 0 {
		e.Code = env.Errors[0].Code
		m := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, env.Errors[0].Message)
		if len(m) > 200 {
			m = m[:200]
		}
		e.Message = m
	}
	return e
}

// registration checks and normalises an answer: the peer key, an IPv4 endpoint literal, the addresses.
func (r regResponse) registration() (Registration, error) {
	out := Registration{ID: r.ID, Token: r.Token, License: r.Account.License, AccountType: r.Account.AccountType,
		ClientID: r.Config.ClientID}
	if out.ID == "" || out.Token == "" {
		return Registration{}, errors.New("cloudflare answer has no device id or token")
	}
	if len(r.Config.Peers) == 0 {
		return Registration{}, errors.New("cloudflare answer has no peer")
	}
	peer := r.Config.Peers[0]
	if !validKey(peer.PublicKey) {
		return Registration{}, errors.New("cloudflare answer has a bad peer key")
	}
	out.PeerPublicKey = peer.PublicKey
	var err error
	if out.EndpointV4, err = endpointIP(peer.Endpoint.V4, true); err != nil {
		return Registration{}, err
	}
	if peer.Endpoint.V6 != "" {
		if out.EndpointV6, err = endpointIP(peer.Endpoint.V6, false); err != nil {
			out.EndpointV6 = "" // informational only: never sent to the node
		}
	}
	for _, port := range peer.Endpoint.Ports {
		if port >= 1 && port <= 65535 {
			out.Ports = append(out.Ports, uint16(port))
		}
	}
	if len(out.Ports) == 0 {
		out.Ports = append([]uint16(nil), defaultPorts...)
	}
	if out.AddressV4, err = interfaceAddress(r.Config.Interface.Addresses.V4, 32, true); err != nil {
		return Registration{}, err
	}
	if r.Config.Interface.Addresses.V6 != "" {
		if out.AddressV6, err = interfaceAddress(r.Config.Interface.Addresses.V6, 128, false); err != nil {
			out.AddressV6 = ""
		}
	}
	return out, nil
}

// endpointIP turns "162.159.192.1:0" or "[2606:4700:d0::a29f:c001]:0" into the bare address. v4 selects the family.
func endpointIP(s string, v4 bool) (string, error) {
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || a.Unmap().Is4() != v4 {
		return "", errors.New("cloudflare answer has a bad endpoint address")
	}
	return a.Unmap().String(), nil
}

// interfaceAddress adds the host prefix to a bare address and checks the family.
func interfaceAddress(s string, bits int, v4 bool) (string, error) {
	if strings.Contains(s, "/") {
		pf, err := netip.ParsePrefix(s)
		if err != nil || pf.Addr().Is4() != v4 {
			return "", errors.New("bad interface address")
		}
		return pf.String(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Is4() != v4 {
		return "", errors.New("bad interface address")
	}
	return netip.PrefixFrom(a, bits).String(), nil
}

// validKey is a WireGuard key: standard base64 of 32 bytes.
func validKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}
