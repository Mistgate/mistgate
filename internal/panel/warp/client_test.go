package warp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// The production transport must put the Android app's ClientHello on the wire: TLS 1.2 only, the two AES-256-GCM
// suites, http/1.1 only. The test runs a TLS server that records what it was offered; it is not Cloudflare.
func TestClientHelloImitatesTheApp(t *testing.T) {
	var mu sync.Mutex
	var seen *tls.ClientHelloInfo
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1006,"message":"Registration not found"}]}`))
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			seen = h
			mu.Unlock()
			return nil, nil
		}}
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr, err := newTransport(tlsSpecWgcf230, roots)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClientFor(srv.URL, &http.Client{Transport: tr, Timeout: 10 * time.Second})
	_, err = c.Get(context.Background(), Defaults(), "00000000-0000-0000-0000-000000000000", "t")
	if apiStatus(err) != http.StatusNotFound {
		t.Fatalf("Get over the imitated hello: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen == nil {
		t.Fatal("the server saw no hello")
	}
	if len(seen.SupportedVersions) == 0 || seen.SupportedVersions[0] != tls.VersionTLS12 { // no supported_versions extension: Go lists 1.2 and what lies below
		t.Errorf("versions = %x, want TLS 1.2 at most", seen.SupportedVersions)
	}
	if !slices.Equal(seen.CipherSuites, []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}) {
		t.Errorf("cipher suites = %x", seen.CipherSuites)
	}
	if !slices.Equal(seen.SupportedProtos, []string{"http/1.1"}) {
		t.Errorf("ALPN = %v, want http/1.1 only", seen.SupportedProtos)
	}
	if !slices.Equal(seen.SupportedCurves, []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}) {
		t.Errorf("curves = %v", seen.SupportedCurves)
	}
}

func TestUnknownTLSSpec(t *testing.T) {
	if _, err := newTransport("nope", nil); err == nil {
		t.Error("an unknown spec id gave a transport")
	}
	if ids := tlsSpecIDs(); len(ids) != 1 || ids[0] != tlsSpecWgcf230 {
		t.Errorf("ids = %v", ids)
	}
}

func TestRegistrationAnswerChecks(t *testing.T) {
	ok := regResponse{ID: "i", Token: "t"}
	ok.Config.Peers = append(ok.Config.Peers, struct {
		PublicKey string `json:"public_key"`
		Endpoint  struct {
			V4    string  `json:"v4"`
			V6    string  `json:"v6"`
			Ports []int64 `json:"ports"`
		} `json:"endpoint"`
	}{PublicKey: testPeerKey})
	ok.Config.Peers[0].Endpoint.V4 = "162.159.192.1:0"
	ok.Config.Interface.Addresses.V4 = "172.16.0.2"
	r, err := ok.registration()
	if err != nil || r.EndpointV4 != "162.159.192.1" || r.AddressV4 != "172.16.0.2/32" || len(r.Ports) != 4 || r.AddressV6 != "" {
		t.Fatalf("registration = %+v, %v", r, err)
	}
	for name, mut := range map[string]func(*regResponse){
		"no token":     func(x *regResponse) { x.Token = "" },
		"no peer":      func(x *regResponse) { x.Config.Peers = nil },
		"bad key":      func(x *regResponse) { x.Config.Peers[0].PublicKey = "AAAA" },
		"no endpoint":  func(x *regResponse) { x.Config.Peers[0].Endpoint.V4 = "" },
		"v6 as v4":     func(x *regResponse) { x.Config.Peers[0].Endpoint.V4 = "[2606:4700:d0::a29f:c001]:0" },
		"no address":   func(x *regResponse) { x.Config.Interface.Addresses.V4 = "" },
		"v6 as v4 adr": func(x *regResponse) { x.Config.Interface.Addresses.V4 = "2606:4700::1" },
	} {
		x := ok
		x.Config.Peers = append(x.Config.Peers[:0:0], ok.Config.Peers...)
		mut(&x)
		if _, err := x.registration(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A bad IPv6 part is dropped, not fatal: it is informational.
	x := ok
	x.Config.Peers = append(x.Config.Peers[:0:0], ok.Config.Peers...)
	x.Config.Peers[0].Endpoint.V6 = "garbage"
	x.Config.Interface.Addresses.V6 = "garbage"
	if r, err := x.registration(); err != nil || r.EndpointV6 != "" || r.AddressV6 != "" {
		t.Errorf("registration with bad v6 = %+v, %v", r, err)
	}
}
