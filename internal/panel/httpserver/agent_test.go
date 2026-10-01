package httpserver

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAgentSNI = "q3m8x2kd7w4ht9pa.invalid"

// agentEnv is a panel with dummy agent hooks, served over real TLS (HTTP/2 on) through the
// same tls.Config builder Serve uses.
type agentEnv struct {
	*testEnv
	ts        *httptest.Server
	agentHits atomic.Int32
	tlsCalls  atomic.Int32
}

func newAgentEnv(t *testing.T, tlsFn func(*tls.ClientHelloInfo) (*tls.Config, error), mutate ...func(*Config)) *agentEnv {
	t.Helper()
	ae := &agentEnv{}
	agentCert := selfSigned(t, testAgentSNI)
	if tlsFn == nil {
		tlsFn = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			ae.tlsCalls.Add(1)
			return &tls.Config{Certificates: []tls.Certificate{agentCert}, ClientAuth: tls.RequireAnyClientCert}, nil // no NextProtos on purpose
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ae.agentHits.Add(1)
		if r.URL.Path == "/slow" { // a long-lived stream: longer than the server's write timeout
			io.WriteString(w, "a")
			w.(http.Flusher).Flush()
			time.Sleep(400 * time.Millisecond)
			io.WriteString(w, "b")
			return
		}
		fmt.Fprintf(w, "agent|%d|%s|%s|%s", len(r.TLS.PeerCertificates), r.URL.Path, r.Host, r.Proto)
	})
	ae.testEnv = newTestEnv(t, append([]func(*Config){func(c *Config) {
		c.AgentSNI, c.AgentTLS, c.AgentHandler = testAgentSNI, tlsFn, handler
	}}, mutate...)...)
	certFile, keyFile := writePEM(t, t.TempDir(), selfSigned(t, "example.test"))
	cfg, _, err := ae.srv.publicTLS(ServeOptions{TLSCert: certFile, TLSKey: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	ae.ts = httptest.NewUnstartedServer(ae.srv.Public())
	ae.ts.TLS = cfg
	ae.ts.EnableHTTP2 = true
	ae.ts.Config.ReadTimeout, ae.ts.Config.WriteTimeout = 150*time.Millisecond, 150*time.Millisecond
	ae.ts.StartTLS()
	t.Cleanup(ae.ts.Close)
	return ae
}

func get(t *testing.T, c *http.Client, url, host string) (int, string, string, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Proto, err
}

func TestAgentEndpointBySNI(t *testing.T) {
	ae := newAgentEnv(t, nil)
	addr := ae.ts.Listener.Addr().String()
	clientCert := selfSigned(t, "some-node")
	agent := "https://" + testAgentSNI

	// The agent SNI gets the fleet's TLS configuration and reaches the agent handler, over
	// HTTP/2 although the fleet's config named no protocols.
	code, body, proto, err := get(t, tlsClient(addr, &clientCert), agent+"/mistgate.agent.v1.AgentService/Connect", "")
	if err != nil || code != 200 || body != "agent|1|/mistgate.agent.v1.AgentService/Connect|"+testAgentSNI+"|HTTP/2.0" || proto != "HTTP/2.0" {
		t.Fatalf("agent request: %d %q %s %v", code, body, proto, err)
	}
	// The fleet's configuration requires a client certificate: without one there is no connection.
	if _, _, _, err := get(t, tlsClient(addr, nil), agent+"/x", ""); err == nil {
		t.Fatal("agent SNI served a client without a certificate")
	}
	if ae.agentHits.Load() != 1 {
		t.Fatalf("agent handler hit %d times", ae.agentHits.Load())
	}
	before := ae.agentHits.Load()

	// Any other server name gets the public configuration and the decoy, whatever it
	// sends: client certificates, the agent name in the Host header, agent paths.
	c := tlsClient(addr, &clientCert)
	for name, tc := range map[string]struct{ url, host string }{
		"decoy":                    {"https://example.test/", ""},
		"agent path":               {"https://example.test/mistgate.agent.v1.AgentService/Connect", ""},
		"agent name as Host":       {"https://example.test/mistgate.agent.v1.AgentService/Connect", testAgentSNI},
		"agent name as Host, root": {"https://example.test/", testAgentSNI},
		"sibling name":             {"https://x." + testAgentSNI + "/", ""},
		"prefix of the name":       {"https://q3m8x2kd7w4ht9p.invalid/", ""},
		"name with another tail":   {"https://" + strings.TrimSuffix(testAgentSNI, ".invalid") + ".example/", ""},
	} {
		code, body, _, err := get(t, c, tc.url, tc.host)
		if err != nil || strings.HasPrefix(body, "agent|") || (code != 200 && code != 404) {
			t.Errorf("%s: %d %q %v", name, code, body, err)
		}
	}
	if code, body, _, _ := get(t, c, "https://example.test/", ""); code != 200 || !strings.Contains(body, "Coming soon") {
		t.Errorf("public SNI: %d %q", code, body)
	}
	if ae.agentHits.Load() != before {
		t.Fatalf("a non-agent connection reached the agent handler (%d hits)", ae.agentHits.Load()-before)
	}
	// The case of the name does not matter to TLS, nor a trailing dot.
	for _, name := range []string{strings.ToUpper(testAgentSNI), testAgentSNI + "."} {
		if code, body, _, err := get(t, tlsClient(addr, &clientCert), "https://"+name+"/x", ""); err != nil || code != 200 || !strings.HasPrefix(body, "agent|") {
			t.Errorf("SNI %q: %d %q %v", name, code, body, err)
		}
	}
}

// Agent streams outlive the listener's read/write timeouts; everything else does not get that
// (see TestAgentDeadlinesStayForUnauthenticatedCalls for the cases that keep them).
func TestAgentRequestsLiftTheServerDeadlines(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "a")
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "b")
	})
	clientCert := selfSigned(t, "node")
	ae := newAgentEnv(t, verifyingTLS(t, selfSigned(t, testAgentSNI), clientCert), func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: slow} })
	addr := ae.ts.Listener.Addr().String()

	code, body, err := fetch(t, tlsClient(addr, &clientCert), http.MethodPost, "https://"+testAgentSNI+"/slow", "application/connect+proto", nil)
	if err != nil || code != 200 || body != "ab" {
		t.Fatalf("agent stream was cut: %d %q %v", code, body, err)
	}
	// Control: the same slow handler on the ordinary path is cut by the 150 ms write timeout,
	// so the test above proves the deadline was really lifted.
	if _, body, _, err := get(t, tlsClient(addr, nil), "https://example.test"+subPrefix+"x", ""); err == nil && body == "ab" {
		t.Fatal("control: the write timeout did not apply to an ordinary request")
	}
}

func TestAgentTLSMisbehaviourFailsClosed(t *testing.T) {
	clientCert := selfSigned(t, "node")
	// A nil configuration would make crypto/tls fall back to the public one; that must not
	// lead to the agent handler.
	ae := newAgentEnv(t, func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil })
	if _, body, _, err := get(t, tlsClient(ae.ts.Listener.Addr().String(), &clientCert), "https://"+testAgentSNI+"/x", ""); err == nil || strings.HasPrefix(body, "agent|") {
		t.Fatalf("nil agent config served: %q %v", body, err)
	}
	ae = newAgentEnv(t, func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, fmt.Errorf("fleet not ready") })
	if _, _, _, err := get(t, tlsClient(ae.ts.Listener.Addr().String(), &clientCert), "https://"+testAgentSNI+"/x", ""); err == nil {
		t.Fatal("handshake succeeded although the fleet refused")
	}
	if ae.agentHits.Load() != 0 {
		t.Fatal("agent handler reached")
	}
	// Without the hooks, the agent name is just another name: decoy.
	e := newTestEnv(t, func(c *Config) { c.AgentSNI = testAgentSNI })
	certFile, keyFile := writePEM(t, t.TempDir(), selfSigned(t, "example.test"))
	cfg, _, err := e.srv.publicTLS(ServeOptions{TLSCert: certFile, TLSKey: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(e.srv.Public())
	ts.TLS, ts.EnableHTTP2 = cfg, true
	ts.StartTLS()
	defer ts.Close()
	if code, body, _, err := get(t, tlsClient(ts.Listener.Addr().String(), nil), "https://"+testAgentSNI+"/x", ""); err != nil || code != 404 || strings.HasPrefix(body, "agent|") {
		t.Errorf("agent name without hooks: %d %q %v", code, body, err)
	}
}

func TestAgentConfigIsValidated(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"TLS without handler": func(c *Config) {
			c.AgentSNI, c.AgentTLS = testAgentSNI, func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil }
		},
		"handler without TLS": func(c *Config) { c.AgentSNI, c.AgentHandler = testAgentSNI, http.NotFoundHandler() },
		"hooks without SNI": func(c *Config) {
			c.AgentTLS, c.AgentHandler = func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil }, http.NotFoundHandler()
		},
		"SNI with a slash":  func(c *Config) { c.AgentSNI = "a/b.invalid" },
		"SNI with a port":   func(c *Config) { c.AgentSNI = "abc.invalid:443" },
		"SNI without a dot": func(c *Config) { c.AgentSNI = "abc" },
	} {
		if err := tryConfig(t, mutate); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, mutate := range map[string]func(*Config){
		"SNI alone": func(c *Config) { c.AgentSNI = testAgentSNI },
		"all three": func(c *Config) {
			c.AgentSNI, c.AgentTLS, c.AgentHandler = testAgentSNI, func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil }, http.NotFoundHandler()
		},
		"none": func(c *Config) {},
	} {
		if err := tryConfig(t, mutate); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
