package cfapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

// Every test talks to this fake API server. The real Cloudflare API is never contacted from the test suite.

type fakeAPI struct {
	srv *httptest.Server
	mu  sync.Mutex

	requests  []*http.Request
	bodies    [][]byte
	hellos    []*tls.ClientHelloInfo
	status    int    // forced status for every call (0 = normal)
	token     string // Bearer the server expects for GET/DELETE
	endpoint4 string
	count     int
}

const (
	fakePeerKey  = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=" // the historical value, a public key
	fakeClientID = "AQIDBA=="                                     // bytes 01 02 03 04
)

func newFakeAPI(t *testing.T) (*fakeAPI, *Client) {
	t.Helper()
	f := &fakeAPI{token: "tok-secret-0001", endpoint4: "198.51.100.7:0"}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	f.srv.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		f.mu.Lock()
		f.hellos = append(f.hellos, h)
		f.mu.Unlock()
		return nil, nil
	}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	return f, &Client{BaseURL: f.srv.URL, RootCAs: pool, Timeout: 10 * time.Second}
}

func (f *fakeAPI) response(id, v6 string) string {
	return fmt.Sprintf(`{"id":%q,"token":%q,"key":"x","key_type":"curve25519","tunnel_type":"wireguard","warp_enabled":true,
	"account":{"id":"acc-1","account_type":"free","license":"lic-secret-0002","warp_plus":false,"role":"child"},
	"config":{"client_id":%q,"interface":{"addresses":{"v4":"172.16.0.2","v6":%q}},
	"peers":[{"public_key":%q,"endpoint":{"host":"engage.example.com:2408","v4":%q,"v6":"[2001:db8::a29f:c001]:0","ports":[2408,500,1701,4500]}}],
	"services":{"http_proxy":"x"}},"policy":{},"override_codes":{}}`, id, f.token, fakeClientID, v6, fakePeerKey, f.endpoint4)
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, b)
	f.count++
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, `{"result":null,"success":false,"errors":[{"code":1015,"message":"rate limited"}]}`)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // v0a5641 reg [id]
	switch {
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "reg":
		io.WriteString(w, f.response("reg-1234", "2001:db8:110::2"))
	case len(parts) == 3 && parts[1] == "reg":
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			io.WriteString(w, f.response(parts[2], ""))
			return
		}
		if r.Method == http.MethodDelete {
			io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeAPI) seen() int { f.mu.Lock(); defer f.mu.Unlock(); return f.count }

func TestRegister(t *testing.T) {
	f, c := newFakeAPI(t)
	tos := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)
	a, err := c.Register(context.Background(), tos)
	if err != nil {
		t.Fatal(err)
	}

	// what the server received: method, path, headers, body
	r, body := f.requests[0], f.bodies[0]
	if r.Method != "POST" || r.URL.Path != "/v0a5641/reg" {
		t.Fatalf("%s %s", r.Method, r.URL.Path)
	}
	for k, want := range map[string]string{
		"User-Agent": "1.1.1.1/6.38.9-5641 (Android 16.0.0)", "CF-Client-Version": "a-6.38.9-5641",
		"Content-Type": "application/json; charset=UTF-8",
	} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if r.Header.Get("Accept") != "" || r.Header.Get("Authorization") != "" {
		t.Errorf("unexpected headers: %v", r.Header)
	}
	var req map[string]string
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req["tunnel_type"] != "wireguard" || req["key_type"] != "curve25519" || req["model"] != "PC" || req["tos"] != "2026-10-01T12:00:00.123456789Z" {
		t.Fatalf("body: %s", body)
	}
	// the private key we keep belongs to the public key we sent
	priv, err := base64.StdEncoding.DecodeString(a.PrivateKey.Reveal())
	if err != nil || len(priv) != 32 || priv[0]&7 != 0 || priv[31]&0xc0 != 0x40 {
		t.Fatalf("private key: %v len %d", err, len(priv))
	}
	wantPub, _ := curve25519.X25519(priv, curve25519.Basepoint)
	if req["key"] != base64.StdEncoding.EncodeToString(wantPub) {
		t.Fatal("the registered public key does not match the stored private key")
	}

	// the ClientHello looks like the Android app: TLS 1.2 only, two ECDHE AES-256-GCM suites, http/1.1 only
	h := f.hellos[0]
	if fmt.Sprint(h.CipherSuites) != fmt.Sprint([]uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}) {
		t.Errorf("cipher suites: %v", h.CipherSuites)
	}
	if fmt.Sprint(h.SupportedProtos) != "[http/1.1]" {
		t.Errorf("alpn: %v", h.SupportedProtos)
	}
	for _, v := range h.SupportedVersions {
		if v == tls.VersionTLS13 {
			t.Errorf("offers TLS 1.3: %v", h.SupportedVersions)
		}
	}

	// the account
	if a.ID != "reg-1234" || a.Token.Reveal() != f.token || a.License.Reveal() != "lic-secret-0002" || a.AccountType != "free" {
		t.Fatalf("%+v", a)
	}
	if a.EndpointV4 != "198.51.100.7" || a.EndpointV6 != "2001:db8::a29f:c001" || fmt.Sprint(a.Ports) != "[2408 500 1701 4500]" {
		t.Fatalf("endpoints: %+v", a)
	}
	if a.AddressV4 != "172.16.0.2/32" || a.AddressV6 != "2001:db8:110::2/128" || a.MTU != 1280 || a.PeerPublicKey != fakePeerKey {
		t.Fatalf("addresses: %+v", a)
	}
	if fmt.Sprint(a.Reserved) != "[1 2 3]" || a.ClientID != fakeClientID {
		t.Fatalf("reserved: %v", a.Reserved)
	}
}

func TestSecretsDoNotLeakIntoFormatting(t *testing.T) {
	f, c := newFakeAPI(t)
	a, err := c.Register(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("account", "account", a, "token", a.Token, "key", a.PrivateKey)
	js, _ := json.Marshal(a)
	for _, s := range []string{fmt.Sprintf("%v", a), fmt.Sprintf("%+v", a), fmt.Sprintf("%#v", a), sb.String(), string(js)} {
		for _, secret := range []string{f.token, "lic-secret-0002", a.PrivateKey.Reveal()} {
			if strings.Contains(s, secret) {
				t.Fatalf("secret %q leaked into %q", secret[:4], s)
			}
		}
	}
}

func TestRegisterWithoutAcceptedTermsSendsNothing(t *testing.T) {
	f, c := newFakeAPI(t)
	if _, err := c.Register(context.Background(), time.Time{}); err == nil {
		t.Fatal("registered without accepted terms")
	}
	if f.seen() != 0 {
		t.Fatal("a request was sent")
	}
}

func TestParamsAreData(t *testing.T) {
	f, c := newFakeAPI(t)
	c.Params = Params{APIVersion: "v0a9999", UserAgent: "AppX/9 (Android 99)", CFClientVersion: "a-9.9-9999"}
	if _, err := c.Register(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	r := f.requests[0]
	if r.URL.Path != "/v0a9999/reg" || r.Header.Get("User-Agent") != "AppX/9 (Android 99)" || r.Header.Get("CF-Client-Version") != "a-9.9-9999" {
		t.Fatalf("%s %v", r.URL.Path, r.Header)
	}

	for name, p := range map[string]Params{
		"tls spec":     {TLSSpecID: "no-such-spec"},
		"api version":  {APIVersion: "v1/../x"},
		"header split": {UserAgent: "a\r\nX: y"},
	} {
		c.Params = p
		n := f.seen()
		if _, err := c.Register(context.Background(), time.Now()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if f.seen() != n {
			t.Errorf("%s: a request was sent", name)
		}
	}
	if !contains(TLSSpecIDs(), "wgcf_v2.3.0") {
		t.Fatalf("%v", TLSSpecIDs())
	}
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestRateLimited(t *testing.T) {
	f, c := newFakeAPI(t)
	f.status = http.StatusTooManyRequests
	_, err := c.Register(context.Background(), time.Now())
	if !IsRateLimited(err) || IsNotFound(err) {
		t.Fatalf("%v", err)
	}
	var ae *APIError
	if !asAPIError(err, &ae) || !strings.Contains(ae.Body, "rate limited") {
		t.Fatalf("%v", err)
	}
	f.status = http.StatusNotFound
	if _, err := c.Register(context.Background(), time.Now()); !IsNotFound(err) {
		t.Fatalf("%v", err)
	}
}

func asAPIError(err error, target **APIError) bool {
	for err != nil {
		if ae, ok := err.(*APIError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestFetchAndDelete(t *testing.T) {
	f, c := newFakeAPI(t)
	a, err := c.Fetch(context.Background(), "reg-1234", f.token, "stored-private-key")
	if err != nil {
		t.Fatal(err)
	}
	r := f.requests[0]
	if r.Method != "GET" || r.URL.Path != "/v0a5641/reg/reg-1234" || r.Header.Get("Authorization") != "Bearer "+f.token {
		t.Fatalf("%s %s %v", r.Method, r.URL.Path, r.Header)
	}
	if a.PrivateKey.Reveal() != "stored-private-key" || a.AddressV6 != "" || a.EndpointV4 != "198.51.100.7" || a.ID != "reg-1234" {
		t.Fatalf("%+v", a)
	}
	// the endpoint moved at Cloudflare
	f.endpoint4 = "198.51.100.99:0"
	if a, err = c.Fetch(context.Background(), "reg-1234", f.token, "k"); err != nil || a.EndpointV4 != "198.51.100.99" {
		t.Fatalf("%+v %v", a, err)
	}

	if _, err := c.Fetch(context.Background(), "reg-1234", "wrong", "k"); err == nil {
		t.Fatal("wrong token accepted")
	}
	if _, err := c.Fetch(context.Background(), "../etc", f.token, "k"); err == nil {
		t.Fatal("a path in the id was accepted")
	}
	if err := c.Delete(context.Background(), "reg-1234", f.token); err != nil {
		t.Fatal(err)
	}
	if last := f.requests[len(f.requests)-1]; last.Method != "DELETE" || last.URL.Path != "/v0a5641/reg/reg-1234" {
		t.Fatalf("%s %s", last.Method, last.URL.Path)
	}
	n := f.seen()
	if err := c.Delete(context.Background(), "reg-1234", ""); err == nil || f.seen() != n {
		t.Fatalf("delete without a token must fail locally: %v", err)
	}
	if err := c.Delete(context.Background(), "reg-1234", "wrong"); err == nil {
		t.Fatal("wrong token accepted")
	}
}

func TestErrorsDoNotContainTheRegistrationID(t *testing.T) {
	f, c := newFakeAPI(t)
	f.srv.Close() // the connection fails
	_, err := c.Fetch(context.Background(), "reg-secretid-77", "t", "k")
	if err == nil || strings.Contains(err.Error(), "reg-secretid-77") {
		t.Fatalf("%v", err)
	}
}

func TestBadAnswers(t *testing.T) {
	cases := map[string]string{
		"no peer":       `{"id":"a","config":{"interface":{"addresses":{"v4":"172.16.0.2"}},"peers":[]}}`,
		"bad peer key":  `{"id":"a","config":{"interface":{"addresses":{"v4":"172.16.0.2"}},"peers":[{"public_key":"zz","endpoint":{"v4":"198.51.100.7:0"}}]}}`,
		"no endpoint":   `{"id":"a","config":{"interface":{"addresses":{"v4":"172.16.0.2"}},"peers":[{"public_key":"` + fakePeerKey + `","endpoint":{"host":"x:1"}}]}}`,
		"hostname ep":   `{"id":"a","config":{"interface":{"addresses":{"v4":"172.16.0.2"}},"peers":[{"public_key":"` + fakePeerKey + `","endpoint":{"v4":"engage.example.com:0"}}]}}`,
		"no v4 address": `{"id":"a","config":{"interface":{"addresses":{"v6":"2001:db8::2"}},"peers":[{"public_key":"` + fakePeerKey + `","endpoint":{"v4":"198.51.100.7:0"}}]}}`,
		"v6 in v4 slot": `{"id":"a","config":{"interface":{"addresses":{"v4":"172.16.0.2"}},"peers":[{"public_key":"` + fakePeerKey + `","endpoint":{"v4":"[2001:db8::1]:0"}}]}}`,
		"not json":      `<html>`,
	}
	for name, body := range cases {
		if _, err := (&reply{body: []byte(body)}).account(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// no ports in the answer: the standard four
	a, err := (&reply{body: []byte(`{"id":"a","config":{"interface":{"addresses":{"v4":"172.16.0.2/32"}},"peers":[{"public_key":"` + fakePeerKey + `","endpoint":{"v4":"198.51.100.7:0"}}]}}`)}).account()
	if err != nil || fmt.Sprint(a.Ports) != "[2408 500 1701 4500]" || a.Reserved != nil || a.AddressV6 != "" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestGenerateKeyIsDeterministicForASeedAndClamped(t *testing.T) {
	seed := strings.NewReader(strings.Repeat("\xff", 32))
	priv, pub, err := GenerateKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := base64.StdEncoding.DecodeString(priv)
	if b[0] != 0xf8 || b[31] != 0x7f {
		t.Fatalf("not clamped: %x ... %x", b[0], b[31])
	}
	want, _ := curve25519.X25519(b, curve25519.Basepoint)
	if pub != base64.StdEncoding.EncodeToString(want) {
		t.Fatal("public key mismatch")
	}
	if _, _, err := GenerateKey(strings.NewReader("short")); err == nil {
		t.Fatal("short entropy accepted")
	}
}
