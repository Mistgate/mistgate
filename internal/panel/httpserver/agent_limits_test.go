package httpserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// verifyingTLS is an agent TLS configuration like the fleet's: a client certificate is optional, but one that
// is offered must verify against the pool (here: the one certificate the test client holds).
func verifyingTLS(t *testing.T, server, clientCert tls.Certificate) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(clientCert.Leaf)
	return func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{server}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool, NextProtos: []string{"h2"}}, nil
	}
}

// fetch sends one request and reads the whole answer.
func fetch(t *testing.T, c *http.Client, method, url, contentType string, body io.Reader) (int, string, error) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

const enrollPath = "/mistgate.agent.v1.EnrollmentService/Enroll"

// F2, listener side: the listener's deadlines are lifted only for a stream of an authenticated agent. Every
// other way onto the agent SNI keeps them: Enroll (even with a certificate), a unary call, a request
// without a certificate.
func TestAgentDeadlinesStayForUnauthenticatedCalls(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "a")
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond) // the env's write timeout is 150 ms
		io.WriteString(w, "b")
	})
	clientCert := selfSigned(t, "node")
	ae := newAgentEnv(t, verifyingTLS(t, selfSigned(t, testAgentSNI), clientCert), func(c *Config) { c.AgentHandler = slow })
	addr := ae.ts.Listener.Addr().String()
	url := func(path string) string { return "https://" + testAgentSNI + path }

	for name, tc := range map[string]struct {
		cert bool
		path string
		ct   string
		live bool // the stream survives the write timeout
	}{
		"authenticated stream":             {true, "/mistgate.agent.v1.AgentService/Connect", "application/connect+proto", true},
		"authenticated grpc stream":        {true, "/mistgate.agent.v1.AgentService/Connect", "application/grpc", true},
		"authenticated unary call":         {true, "/mistgate.agent.v1.EnrollmentService/Renew", "application/proto", false},
		"Enroll with a certificate":        {true, enrollPath, "application/connect+proto", false},
		"no certificate, stream type":      {false, "/mistgate.agent.v1.AgentService/Connect", "application/connect+proto", false},
		"no certificate, Enroll":           {false, enrollPath, "application/proto", false},
		"no certificate, stream on Enroll": {false, enrollPath, "application/connect+proto", false},
	} {
		var cert *tls.Certificate
		if tc.cert {
			cert = &clientCert
		}
		code, body, err := fetch(t, tlsClient(addr, cert), http.MethodPost, url(tc.path), tc.ct, nil)
		survived := err == nil && code == 200 && body == "ab"
		if survived != tc.live {
			t.Errorf("%s: survived=%v (%d %q %v), want %v", name, survived, code, body, err, tc.live)
		}
	}
}

// A client that opens Enroll and then goes quiet is cut at the read timeout instead of holding the
// connection for ever, and a large body is refused.
func TestAgentEnrollSlowAndLargeBodies(t *testing.T) {
	type result struct {
		n   int
		err error
		at  time.Duration
	}
	got := make(chan result, 2)
	var start time.Time
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		got <- result{int(n), err, time.Since(start)}
	})
	clientCert := selfSigned(t, "node")
	ae := newAgentEnv(t, verifyingTLS(t, selfSigned(t, testAgentSNI), clientCert), func(c *Config) { c.AgentHandler = h })
	c := tlsClient(ae.ts.Listener.Addr().String(), nil) // no certificate: Enroll
	c.Timeout = 10 * time.Second

	// One byte, then silence. Before the fix the deadlines were lifted and this hung until the client gave up.
	pr, pw := io.Pipe()
	go func() { pw.Write([]byte{1}) }() // never closed on purpose
	t.Cleanup(func() { pw.Close() })
	start = time.Now()
	done := make(chan struct{})
	go func() {
		fetch(t, c, http.MethodPost, "https://"+testAgentSNI+enrollPath, "application/proto", pr)
		close(done)
	}()
	select {
	case r := <-got:
		if r.err == nil || r.at > 3*time.Second {
			t.Fatalf("slow body: read ended with %v after %v, want an error within the read timeout", r.err, r.at)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a slow Enroll body held the connection past the read timeout")
	}
	pw.Close()
	<-done

	start = time.Now()
	_, _, _ = fetch(t, c, http.MethodPost, "https://"+testAgentSNI+enrollPath, "application/proto", strings.NewReader(strings.Repeat("x", 2*agentUnauthBody)))
	r := <-got
	var tooBig *http.MaxBytesError
	if !errors.As(r.err, &tooBig) {
		t.Fatalf("large Enroll body: read ended with %v (after %d bytes), want a body limit error", r.err, r.n)
	}
}
