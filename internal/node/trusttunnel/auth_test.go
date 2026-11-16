package trusttunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/decoy"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// exchange sends payload into a tunnel and reads the echo of the same length back.
func exchange(t *testing.T, writer io.Writer, body io.Reader, payload string) {
	t.Helper()
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatalf("writing %q into a tunnel: %v", payload, err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(body, got); err != nil || string(got) != payload {
		t.Fatalf("echo of %q = %q, %v", payload, got, err)
	}
}

// endsWithin reports whether reading body to its end stops (with any error) inside d.
func endsWithin(body io.Reader, d time.Duration) bool {
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, body)
		close(ended)
	}()
	select {
	case <-ended:
		return true
	case <-time.After(d):
		return false
	}
}

func statusOf(t *testing.T, client *testHTTP2Client, authority, auth string) *http.Response {
	t.Helper()
	response, _ := client.connect(t, authority, auth, false)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response
}

func sessionsOf(got nodeengine.Collected, credID string) int {
	n := 0
	for _, s := range got.Sessions {
		if s.CredID == credID {
			n++
		}
	}
	return n
}

// Two credentials share one HTTP/2 connection. Removing one ends its tunnel at once; the other keeps its tunnel and its
// connection, and the removed one's password is refused on that very connection.
func TestRemovedCredentialEndsAtOnceOnASharedConnectionAndIsRefusedThere(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)

	aliceResponse, aliceWriter := client.connect(t, "example.com:80", authValue(t, alice), true)
	bobResponse, bobWriter := client.connect(t, "example.com:80", authValue(t, bob), true)
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("the two tunnels use %d connections, want one shared", count)
	}
	exchange(t, aliceWriter, aliceResponse.Body, "alice before")
	exchange(t, bobWriter, bobResponse.Body, "bob before")

	start := time.Now()
	if _, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{bob.cred}); err != nil {
		t.Fatal(err)
	}
	if !endsWithin(aliceResponse.Body, time.Second) {
		t.Fatalf("the removed credential's tunnel was still open %s after Apply", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the removed credential's tunnel took %s to end", elapsed)
	}
	if h.in.idx.Load().byUser["alice"] != nil {
		t.Fatal("the removed credential is still installed")
	}
	exchange(t, bobWriter, bobResponse.Body, "bob after")

	refused := statusOf(t, client, "example.com:80", authValue(t, alice))
	if refused.StatusCode != http.StatusNotFound || refused.Header.Get("X-Decoy") != "same" {
		t.Fatalf("the removed credential's CONNECT on the shared connection = %d, want the decoy's answer", refused.StatusCode)
	}
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("the shared connection was dropped with the removed credential: %d", count)
	}
	if got := statusOf(t, client, "_check", authValue(t, bob)); got.StatusCode != http.StatusOK {
		t.Fatalf("bob's _check on the same connection = %d", got.StatusCode)
	}
	_ = bobWriter.Close()
	if !endsWithin(bobResponse.Body, time.Second) {
		t.Fatal("bob's tunnel did not finish")
	}
	_ = aliceWriter.Close()
}

// Kick ends one credential's tunnels, not its neighbour's, and keeps that credential off the connection it was kicked from
// (it may come back on a new one: it is still installed).
func TestKickEndsOneCredentialOnASharedConnection(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)

	aliceResponse, aliceWriter := client.connect(t, "example.com:80", authValue(t, alice), true)
	bobResponse, bobWriter := client.connect(t, "example.com:80", authValue(t, bob), true)
	exchange(t, aliceWriter, aliceResponse.Body, "alice before")
	exchange(t, bobWriter, bobResponse.Body, "bob before")
	before := collect(t, h.e)
	if sessionsOf(before, "alice") != 1 || sessionsOf(before, "bob") != 1 {
		t.Fatalf("sessions on a shared connection = %+v, want one per credential", before.Sessions)
	}

	start := time.Now()
	affected, err := h.e.Kick(context.Background(), []string{"alice"})
	if err != nil || affected != 1 {
		t.Fatalf("Kick = %d, %v", affected, err)
	}
	if !endsWithin(aliceResponse.Body, time.Second) {
		t.Fatalf("the kicked credential's tunnel was still open %s after Kick", time.Since(start))
	}
	exchange(t, bobWriter, bobResponse.Body, "bob after")
	if got := statusOf(t, client, "example.com:80", authValue(t, alice)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("the kicked credential's CONNECT on its old connection = %d, want the decoy's answer", got.StatusCode)
	}
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("Kick closed the shared connection: %d", count)
	}
	after := collect(t, h.e)
	if sessionsOf(after, "alice") != 0 || sessionsOf(after, "bob") != 1 {
		t.Fatalf("sessions after Kick = %+v, want bob's only", after.Sessions)
	}

	fresh := newHTTP2Client(t, h.addr)
	if got := statusOf(t, fresh, "_check", authValue(t, alice)); got.StatusCode != http.StatusOK {
		t.Fatalf("the kicked credential on a new connection = %d, want 200: Kick is not a ban", got.StatusCode)
	}
	_ = aliceWriter.Close()
	_ = bobWriter.Close()
}

// A credential that is alone on its connection takes the connection with it.
func TestKickAndRemovalCloseTheConnectionOfAloneCredential(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	aliceClient, bobClient := newHTTP2Client(t, h.addr), newHTTP2Client(t, h.addr)
	aliceResponse, aliceWriter := aliceClient.connect(t, "example.com:80", authValue(t, alice), true)
	bobResponse, bobWriter := bobClient.connect(t, "example.com:80", authValue(t, bob), true)
	exchange(t, aliceWriter, aliceResponse.Body, "a")
	exchange(t, bobWriter, bobResponse.Body, "b")
	if count := authenticatedConnectionCount(h.in); count != 2 {
		t.Fatalf("connections = %d, want 2", count)
	}
	if _, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{bob.cred}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return authenticatedConnectionCount(h.in) == 1 })
	exchange(t, bobWriter, bobResponse.Body, "b again")
	if affected, _ := h.e.Kick(context.Background(), []string{"bob"}); affected != 1 {
		t.Fatalf("Kick affected %d, want 1", affected)
	}
	waitFor(t, time.Second, func() bool { return authenticatedConnectionCount(h.in) == 0 })
	_ = aliceWriter.Close()
	_ = bobWriter.Close()
}

// The 16-connection limit and the sessions count connections per credential: a neighbour on the same connection takes
// nothing from a credential's budget, and a connection of two credentials is one session of each.
func TestConnectionLimitAndSessionsCountPerCredentialOnMixedConnections(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{}, timings{})
	clients := make([]*testHTTP2Client, 17)
	for i := range clients {
		clients[i] = newHTTP2Client(t, h.addr)
	}
	for i := 0; i < 16; i++ {
		if got := statusOf(t, clients[i], "_check", authValue(t, alice)); got.StatusCode != http.StatusOK {
			t.Fatalf("alice on connection %d = %d", i+1, got.StatusCode)
		}
	}
	if got := statusOf(t, clients[0], "_check", authValue(t, bob)); got.StatusCode != http.StatusOK {
		t.Fatalf("bob on alice's first connection = %d", got.StatusCode)
	}
	if got := statusOf(t, clients[16], "_check", authValue(t, alice)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("alice's 17th connection = %d, want the decoy's answer", got.StatusCode)
	}
	if got := statusOf(t, clients[16], "_check", authValue(t, bob)); got.StatusCode != http.StatusOK {
		t.Fatalf("bob on the connection alice was refused = %d, want 200: his budget is his own", got.StatusCode)
	}
	stats := collect(t, h.e)
	if sessionsOf(stats, "alice") != 16 || sessionsOf(stats, "bob") != 2 {
		t.Fatalf("sessions alice/bob = %d/%d, want 16/2", sessionsOf(stats, "alice"), sessionsOf(stats, "bob"))
	}
	var connects uint32
	for _, link := range stats.Links {
		if link.CredID == "alice" {
			connects += link.Connects
		}
	}
	if connects != 16 {
		t.Fatalf("alice connects = %d, want 16 (the refused one is not a connect)", connects)
	}
}

// referenceSite is a site of the node without a tunnel: the decoy under the shared HTTP/2 setup. What it answers to a
// CONNECT is what the node must answer to a CONNECT without a valid credential.
func referenceSite(t *testing.T) string {
	t.Helper()
	certificate, err := testCertificate()
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:   decoy.Handler(),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{*certificate}, NextProtos: []string{"h2", "http/1.1"}},
	}
	if err := sitehttp.ConfigureSiteHTTP2(server); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(tls.NewListener(ln, server.TLSConfig))
	t.Cleanup(func() { _ = server.Close() })
	return ln.Addr().String()
}

type answer struct {
	status int
	header http.Header
	body   string
}

func answerOf(t *testing.T, client *testHTTP2Client, authority, auth string) answer {
	t.Helper()
	response, _ := client.connect(t, authority, auth, false)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	header := response.Header.Clone()
	header.Del("Date")
	return answer{status: response.StatusCode, header: header, body: string(body)}
}

func sameAnswer(a, b answer) bool {
	if a.status != b.status || a.body != b.body || len(a.header) != len(b.header) {
		return false
	}
	for key, values := range a.header {
		if strings.Join(values, "\x00") != strings.Join(b.header[key], "\x00") {
			return false
		}
	}
	return true
}

// A CONNECT without a valid credential is answered by the site, byte for byte, whatever is wrong with the credential.
func TestRefusedConnectIsTheSitesOwnAnswer(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	expired := makeCredential(t, "old", "old", "person-old", 0, 0)
	expired.cred.ValidUntil = time.Now().Add(-time.Hour)
	wrong := makeCredential(t, "wrong", "alice", "person-alice", 0, 0)
	unknown := makeCredential(t, "unknown", "stranger", "person-x", 0, 0)
	h := newHarnessEnv(t, []plugin.UserCred{alice.cred, expired.cred}, &testEgress{}, timings{}, func(env *nodeengine.Env) {
		env.Masquerade = nil // the real decoy of the node
	})
	site := newHTTP2Client(t, referenceSite(t))
	client := newHTTP2Client(t, h.addr)

	for _, authority := range []string{"example.com:80", "_check", "_udp2", "[2001:db8::1]:443"} {
		want := answerOf(t, site, authority, "")
		if want.status != http.StatusNotFound || want.body == "" {
			t.Fatalf("the site's own answer to CONNECT %s = %d %q, want its 404 page", authority, want.status, want.body)
		}
		for name, auth := range map[string]string{
			"no header":      "",
			"wrong password": authValue(t, wrong),
			"unknown user":   authValue(t, unknown),
			"expired":        authValue(t, expired),
			"other scheme":   "Bearer abcdef",
			"broken base64":  "Basic !!!",
			"empty":          "Basic ",
		} {
			if got := answerOf(t, client, authority, auth); !sameAnswer(got, want) {
				t.Fatalf("CONNECT %s (%s) = %+v, the site answers %+v", authority, name, got, want)
			}
		}
	}
	// The counters tell the cases apart for the operator, the wire does not.
	stats := collect(t, h.e)
	var unknownKeys, failed uint32
	for _, link := range stats.Links {
		unknownKeys += link.UnknownKeys
		failed += link.AuthFailed
	}
	if unknownKeys == 0 || failed == 0 {
		t.Fatalf("auth counters unknown=%d failed=%d, want both counted", unknownKeys, failed)
	}
	var expiredFailed uint32
	for _, link := range stats.Links {
		if link.CredID == "old" {
			expiredFailed += link.AuthFailed
		}
	}
	if expiredFailed == 0 {
		t.Fatal("the expired credential's refused CONNECTs were not counted as failed authentications")
	}
}

// An unknown user name costs the same work as a wrong password: one digest of the offered password. (The answers are
// identical, so the time is the only thing left that could tell the two apart.)
func TestUnknownUserCostsTheSameWorkAsAWrongPassword(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	wrong := makeCredential(t, "wrong", "alice", "person-alice", 0, 0)
	unknown := makeCredential(t, "unknown", "stranger", "person-x", 0, 0)
	// Installed before the server starts, so the server's goroutines see it.
	var digests atomic.Int64
	real := passwordDigest
	passwordDigest = func(password string) [sha256.Size]byte {
		digests.Add(1)
		return real(password)
	}
	t.Cleanup(func() { passwordDigest = real })
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)

	for name, tc := range map[string]struct {
		auth string
		want int64
	}{
		"wrong password": {authValue(t, wrong), 1},
		"unknown user":   {authValue(t, unknown), 1},
		"right password": {authValue(t, alice), 1},
		"no header":      {"", 0},
	} {
		digests.Store(0)
		_ = statusOf(t, client, "_check", tc.auth)
		if got := digests.Load(); got != tc.want {
			t.Fatalf("%s: %d digests of the password, want %d", name, got, tc.want)
		}
	}
}

type recordingHandler struct {
	mu      sync.Mutex
	headers []http.Header
}

func (r *recordingHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.headers = append(r.headers, req.Header.Clone())
	r.mu.Unlock()
	w.WriteHeader(http.StatusNotFound)
}

// The credential header never reaches the site's handler and nothing secret reaches the log.
func TestCredentialsNeverReachTheSiteOrTheLog(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	wrong := makeCredential(t, "wrong", "alice", "person-alice", 0, 0)
	unknown := makeCredential(t, "unknown", "stranger", "person-x", 0, 0)
	handler := &recordingHandler{}
	var logs bytes.Buffer
	h := newHarnessEnv(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{}, func(env *nodeengine.Env) {
		env.Masquerade = func(string) http.Handler { return handler }
		env.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	client := newHTTP2Client(t, h.addr)
	for _, fixture := range []credentialFixture{wrong, unknown, alice} {
		_ = statusOf(t, client, "example.com:80", authValue(t, fixture))
	}
	_ = statusOf(t, client, "example.com:80", "Basic broken")
	_ = client.get(t).Body.Close()

	handler.mu.Lock()
	seen := len(handler.headers)
	for _, header := range handler.headers {
		for key, values := range header {
			if strings.EqualFold(key, "Proxy-Authorization") {
				t.Fatalf("the site's handler saw %s: %v", key, values)
			}
		}
	}
	handler.mu.Unlock()
	if seen < 4 {
		t.Fatalf("the site's handler saw %d requests, want the refused ones and the GET", seen)
	}
	if _, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{alice.cred}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{alice.password, wrong.password, unknown.password, "Basic", "Proxy-Authorization"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log contains %q: %s", secret, logs.String())
		}
	}
}
