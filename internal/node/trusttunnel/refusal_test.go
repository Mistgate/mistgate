package trusttunnel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

// refusalRecords is a slog handler that keeps what the engine logs.
type refusalRecords struct {
	min  slog.Level
	mu   sync.Mutex
	recs []loggedRecord
}

type loggedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (r *refusalRecords) Enabled(_ context.Context, level slog.Level) bool { return level >= r.min }
func (r *refusalRecords) WithAttrs([]slog.Attr) slog.Handler               { return r }
func (r *refusalRecords) WithGroup(string) slog.Handler                    { return r }
func (r *refusalRecords) Handle(_ context.Context, rec slog.Record) error {
	got := loggedRecord{level: rec.Level, msg: rec.Message, attrs: map[string]string{}}
	rec.Attrs(func(a slog.Attr) bool {
		got.attrs[a.Key] = a.Value.String()
		return true
	})
	r.mu.Lock()
	r.recs = append(r.recs, got)
	r.mu.Unlock()
	return nil
}

// at returns the records of one level that are about refusals (the engine's other warnings, a listener that stopped, are not
// what these tests count).
func (r *refusalRecords) at(level slog.Level) []loggedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []loggedRecord
	for _, rec := range r.recs {
		if rec.level == level && strings.Contains(rec.msg, "refus") {
			out = append(out, rec)
		}
	}
	return out
}

// inboundRefusals adds up the refusals of an inbound in what Collect returned, and fails if any credential's row carries one:
// a refusal belongs to the inbound, never to a person or a device.
func inboundRefusals(t *testing.T, got nodeengine.Collected, inboundID string) plugin.TunnelRefusals {
	t.Helper()
	var sum plugin.TunnelRefusals
	for _, link := range got.Links {
		if link.CredID != "" {
			if !link.Refusals.Zero() {
				t.Fatalf("refusals on the row of credential %q: %v", link.CredID, link.Refusals)
			}
			continue
		}
		if link.InboundID != inboundID {
			continue
		}
		for i, n := range link.Refusals {
			sum[i] += n
		}
	}
	return sum
}

// only fails unless exactly reason was counted, n times.
func only(t *testing.T, got plugin.TunnelRefusals, reason plugin.TunnelRefusal, n uint32) {
	t.Helper()
	var want plugin.TunnelRefusals
	want[reason] = n
	if got != want {
		t.Fatalf("refusals = %v, want %d of %s only", describeRefusals(got), n, reason.Name())
	}
}

func describeRefusals(r plugin.TunnelRefusals) map[string]uint32 {
	out := map[string]uint32{}
	for i, n := range r {
		if n != 0 {
			out[plugin.TunnelRefusal(i).Name()] = n
		}
	}
	return out
}

func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// Every reason of a refused CONNECT is counted once on the inbound, and the client gets the answer it always got.
func TestTunnelRequestsRefusedByReasonAreCountedAndAnsweredAsBefore(t *testing.T) {
	forEachTransport(t, func(t *testing.T, v transportKind) {
		alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
		closed := closedLoopbackAddr(t) // a real "connection refused" of the operating system
		errs := map[string]error{
			"blocked.example:80":     egress.ErrBlocked,
			"nov4.example:80":        egress.ErrNoIPv4,
			"refused.example:80":     nil, // dialled for real below
			"unreachable.example:80": &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", testUnreachable)},
			"dns.example:80":         &net.DNSError{Err: "no such host", Name: "dns.example", IsNotFound: true},
			"conn.example:80":        &net.OpError{Op: "dial", Err: errors.New("network is down")},
			"timeout.example:80":     &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded},
			"other.example:80":       errors.New("something else"),
		}
		outbound := &testEgress{tcpDial: func(target string) (net.Conn, error) {
			if target == "refused.example:80" {
				return net.Dial("tcp", closed)
			}
			return nil, errs[target]
		}}
		h := v.harness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
		client := v.client(t, h.addr)
		for _, tc := range []struct {
			target  string
			reason  plugin.TunnelRefusal
			warning string
		}{
			{"blocked.example:80", plugin.RefusedBlocked, "310 - Destination address is blocked"},
			{"nov4.example:80", plugin.RefusedNoIPv4, "301 - Destination is unavailable"},
			{"refused.example:80", plugin.RefusedConnRefused, "301 - Destination is unavailable"},
			{"unreachable.example:80", plugin.RefusedConnUnreachable, "301 - Destination is unavailable"},
			{"dns.example:80", plugin.RefusedConnDNS, "301 - Destination is unavailable"},
			{"conn.example:80", plugin.RefusedConnOther, "301 - Destination is unavailable"},
			{"timeout.example:80", plugin.RefusedTimeout, "302 - Connection timed out"},
			{"other.example:80", plugin.RefusedOtherDial, "300 - Connection failed for some reason"},
		} {
			response, _ := client.connect(t, tc.target, authValue(t, alice), false)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadGateway || response.Header.Get("X-Warning") != tc.warning ||
				response.Header.Get("Content-Length") != "0" {
				t.Fatalf("%s = %d %q (length %q), want 502 %q", tc.target, response.StatusCode, response.Header.Get("X-Warning"),
					response.Header.Get("Content-Length"), tc.warning)
			}
			only(t, inboundRefusals(t, collect(t, h.e), "de1"), tc.reason, 1)
		}
	})
}

// A connect that outlasts the engine's own limit is a timeout, and the late connection is still closed.
func TestConnectTimeoutIsCountedAsATimeout(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	release := make(chan struct{})
	outbound := &testEgress{tcpDial: func(string) (net.Conn, error) {
		<-release
		return nil, errors.New("late")
	}}
	defer close(release)
	ts := defaultTimings
	ts.connect = 25 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, ts)
	client := newHTTP2Client(t, h.addr)
	response, _ := client.connect(t, "example.com:80", authValue(t, alice), false)
	_ = response.Body.Close()
	if response.Header.Get("X-Warning") != "302 - Connection timed out" {
		t.Fatalf("X-Warning = %q", response.Header.Get("X-Warning"))
	}
	only(t, inboundRefusals(t, collect(t, h.e), "de1"), plugin.RefusedTimeout, 1)
}

// What the engine cannot carry, or cannot read, is counted under "other" with its own sub-reason.
func TestUnservedRequestsAreCountedUnderOther(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)

	icmp, _ := client.connect(t, "_icmp", authValue(t, alice), false)
	_ = icmp.Body.Close()
	if icmp.StatusCode != http.StatusBadGateway || icmp.Header.Get("X-Warning") != "300 - Connection failed for some reason" {
		t.Fatalf("_icmp = %d %q", icmp.StatusCode, icmp.Header.Get("X-Warning"))
	}
	only(t, inboundRefusals(t, collect(t, h.e), "de1"), plugin.RefusedOtherICMP, 1)

	bad, _ := client.connect(t, "no-port.example", authValue(t, alice), false)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadGateway || bad.Header.Get("X-Warning") != "300 - Connection failed for some reason" {
		t.Fatalf("authority without a port = %d %q", bad.StatusCode, bad.Header.Get("X-Warning"))
	}
	only(t, inboundRefusals(t, collect(t, h.e), "de1"), plugin.RefusedOtherAuthority, 1)
}

// A response writer that cannot lift the site's deadlines ends the request with 300 and the "deadlines" reason.
func TestDeadlinesThatCannotBeLiftedAreCounted(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	recorder := httptest.NewRecorder() // no SetReadDeadline: http.ErrNotSupported
	stream := &streamState{rc: http.NewResponseController(recorder)}
	request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil)
	h.in.serveTunnel(recorder, request, stream)
	if recorder.Code != http.StatusBadGateway || recorder.Header().Get("X-Warning") != "300 - Connection failed for some reason" {
		t.Fatalf("answer = %d %q", recorder.Code, recorder.Header().Get("X-Warning"))
	}
	only(t, inboundRefusals(t, collect(t, h.e), "de1"), plugin.RefusedOtherDeadlines, 1)
}

// beginStream is the last gate; each way it says no has its reason, and the answer to the client stays the site's.
func TestStreamsThatAreNotAcceptedAreCountedByReason(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	credential := h.in.idx.Load().byUser["alice"]
	begin := func(ctx context.Context, key net.Conn) bool {
		request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil).WithContext(ctx)
		_, _, ok := h.in.beginStreamOnConn(request, httptest.NewRecorder(), key, func() { _ = key.Close() }, io.Discard, credential)
		return ok
	}
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	expect := func(want plugin.TunnelRefusal) {
		t.Helper()
		only(t, inboundRefusals(t, collect(t, h.e), "de1"), want, 1)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if begin(canceled, local) {
		t.Fatal("a request of a closing connection began a tunnel")
	}
	expect(plugin.RefusedStreamClosed)

	if !begin(context.Background(), local) { // the connection exists from here on
		t.Fatal("a valid request was refused")
	}
	connection := h.in.conns[local]
	connection.mu.Lock()
	connection.expiring = true
	connection.mu.Unlock()
	if begin(context.Background(), local) {
		t.Fatal("a connection that is expiring took a tunnel")
	}
	expect(plugin.RefusedStreamClosed)
	connection.mu.Lock()
	connection.expiring = false
	connection.banned[credential] = struct{}{}
	connection.mu.Unlock()
	if begin(context.Background(), local) {
		t.Fatal("a kicked credential began a tunnel on its connection")
	}
	expect(plugin.RefusedStreamCredGone)
	connection.mu.Lock()
	delete(connection.banned, credential)
	connection.mu.Unlock()

	credential.validUntil.Store(time.Now().Add(-time.Minute).Unix())
	if begin(context.Background(), local) {
		t.Fatal("an expired credential began a tunnel")
	}
	expect(plugin.RefusedStreamCredGone)
	credential.validUntil.Store(0)
	credential.removed.Store(true)
	if begin(context.Background(), local) {
		t.Fatal("a removed credential began a tunnel")
	}
	expect(plugin.RefusedStreamCredGone)
	credential.removed.Store(false)

	other, otherRemote := net.Pipe()
	t.Cleanup(func() { _ = other.Close(); _ = otherRemote.Close() })
	credential.mu.Lock()
	for range maxCredentialConnections { // sixteen connections, each with a tunnel of the credential
		busy := &connectionState{}
		credential.connections[busy] = time.Now()
		credential.streams[&streamState{conn: busy, cred: credential}] = struct{}{}
	}
	credential.mu.Unlock()
	if begin(context.Background(), other) {
		t.Fatal("a credential past its connection limit began a tunnel on a new connection")
	}
	expect(plugin.RefusedStreamLimit)
	credential.mu.Lock() // the stand-ins are not real streams: nothing is to abort them when the harness closes
	clear(credential.streams)
	clear(credential.connections)
	credential.mu.Unlock()
}

// fullCredential gives alice sixteen connections (the first is the oldest) and returns them with the far ends of their
// pipes. A connection in live has a tunnel of alice still open; on the others it has ended.
func fullCredential(t *testing.T, h *testHarness, credential *credState, live map[int]bool) ([]*connectionState, []net.Conn, []net.Conn) {
	t.Helper()
	var states []*connectionState
	var keys, remotes []net.Conn
	for i := range maxCredentialConnections {
		local, remote := net.Pipe()
		t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
		request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil)
		stream, _, ok := h.in.beginStream(request, httptest.NewRecorder(), local, credential)
		if !ok {
			t.Fatalf("connection %d of alice was refused", i+1)
		}
		if !live[i] {
			h.in.finishStream(stream)
		}
		h.in.connMu.Lock()
		states = append(states, h.in.conns[local])
		h.in.connMu.Unlock()
		keys, remotes = append(keys, local), append(remotes, remote)
	}
	credential.mu.Lock()
	for i, connection := range states {
		credential.connections[connection] = time.Unix(1000+int64(i), 0)
	}
	credential.mu.Unlock()
	return states, keys, remotes
}

// The seventeenth connection takes the place of the oldest connection that has no tunnel of the credential; one with a
// tunnel is never touched, however old. Nothing is refused.
func TestFullCredentialGivesUpItsOldestIdleConnection(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	credential := h.in.idx.Load().byUser["alice"]
	states, _, remotes := fullCredential(t, h, credential, map[int]bool{0: true}) // the oldest still carries a tunnel

	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil)
	if _, refusal, ok := h.in.beginStream(request, httptest.NewRecorder(), local, credential); !ok {
		t.Fatalf("the seventeenth connection was refused (%s), want it to take an idle one's place", refusal.Name())
	}
	credential.mu.Lock()
	_, busyKept := credential.connections[states[0]]
	_, idleKept := credential.connections[states[1]]
	count := len(credential.connections)
	credential.mu.Unlock()
	if count != maxCredentialConnections || !busyKept || idleKept {
		t.Fatalf("connections = %d, oldest busy kept = %v, oldest idle kept = %v; want 16, true, false", count, busyKept, idleKept)
	}
	// the evicted connection is closed (nothing else is on it); the one with the tunnel is not
	_ = remotes[1].SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remotes[1].Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("the evicted connection: read = %v, want it closed", err)
	}
	_ = remotes[0].SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := remotes[0].Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the connection with a tunnel: read = %v, want it open", err)
	}
	if got := inboundRefusals(t, collect(t, h.e), "de1"); !got.Zero() {
		t.Fatalf("refusals = %v, want none", describeRefusals(got))
	}
}

// With a tunnel on every connection there is nobody to give up: the seventeenth is refused and counted.
func TestFullCredentialWithTunnelsOnEveryConnectionIsRefused(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	credential := h.in.idx.Load().byUser["alice"]
	live := map[int]bool{}
	for i := range maxCredentialConnections {
		live[i] = true
	}
	fullCredential(t, h, credential, live)

	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil)
	if _, refusal, ok := h.in.beginStream(request, httptest.NewRecorder(), local, credential); ok || refusal != plugin.RefusedStreamLimit {
		t.Fatalf("seventeenth connection: ok = %v, refusal = %s; want refused for the limit", ok, refusal.Name())
	}
	only(t, inboundRefusals(t, collect(t, h.e), "de1"), plugin.RefusedStreamLimit, 1)
}

// Sixteen idle connections and many new ones at once: the limit is never exceeded, and every new connection gets in.
func TestFullCredentialRacingConnectionsNeverExceedTheLimit(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	credential := h.in.idx.Load().byUser["alice"]
	fullCredential(t, h, credential, nil)

	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		local, remote := net.Pipe()
		t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil)
			_, _, results[i] = h.in.beginStream(request, httptest.NewRecorder(), local, credential)
		}()
	}
	wg.Wait()
	for i, ok := range results {
		if !ok {
			t.Fatalf("racing connection %d was refused although idle ones could be given up", i)
		}
	}
	credential.mu.Lock()
	count := len(credential.connections)
	credential.mu.Unlock()
	if count != maxCredentialConnections {
		t.Fatalf("connections = %d, want %d", count, maxCredentialConnections)
	}
}

// Someone who is not alice learns nothing from the limit: a wrong password, even with every connection of alice busy, gets
// the site and no 503, and nothing is counted against alice.
func TestFullCredentialStaysInvisibleToAStranger(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	clients := make([]*testHTTP2Client, maxCredentialConnections)
	for i := range clients {
		clients[i] = newHTTP2Client(t, h.addr)
		if response, _ := clients[i].connect(t, "example.com:80", authValue(t, alice), true); response.StatusCode != http.StatusOK {
			t.Fatalf("connection %d = %d", i+1, response.StatusCode)
		}
	}
	wrong := alice
	wrong.password = "not-" + alice.password
	stranger := newHTTP2Client(t, h.addr)
	response, _ := stranger.connect(t, "example.com:80", authValue(t, wrong), false)
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound || string(body) != "decoy site" || response.Header.Get("X-Warning") != "" {
		t.Fatalf("wrong password = %d %q (X-Warning %q), want the site", response.StatusCode, body, response.Header.Get("X-Warning"))
	}
	if got := inboundRefusals(t, collect(t, h.e), "de1"); !got.Zero() {
		t.Fatalf("refusals = %v, want none", describeRefusals(got))
	}
}

// The same on the wire: a kicked credential's next CONNECT on the connection still gets the site, not an error.
func TestStreamRefusalOnTheWireIsStillTheSite(t *testing.T) {
	forEachTransport(t, func(t *testing.T, v transportKind) {
		alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
		h := v.harness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
		client := v.client(t, h.addr)
		check, _ := client.connect(t, "_check", authValue(t, alice), false)
		_ = check.Body.Close()
		h.in.idx.Load().byUser["alice"].removed.Store(true)
		response, _ := client.connect(t, "example.com:80", authValue(t, alice), false)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound || string(body) != "decoy site" {
			t.Fatalf("a removed credential got %d %q, want the site", response.StatusCode, body)
		}
		got := inboundRefusals(t, collect(t, h.e), "de1")
		if got[plugin.RefusedStreamCredGone] == 0 {
			t.Fatalf("refusals = %v, want stream_cred_gone", describeRefusals(got))
		}
	})
}

// A datagram the egress will not take is counted, with the same classes; the stream goes on.
func TestUDPDatagramsTheEgressRefusesAreCounted(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	outbound := &testEgress{udpEcho: true}
	outbound.udpFactory = func(target string) (nodeengine.EgressUDP, error) {
		if target == "198.51.100.9:53" {
			return nil, egress.ErrBlocked
		}
		conn := newTestUDP(true)
		conn.writeErr = func(address string) error {
			if address == "192.0.2.77:9" {
				return &net.OpError{Op: "write", Err: os.NewSyscallError("sendto", testUnreachable)}
			}
			return nil
		}
		return conn, nil
	}
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "_udp2", authValue(t, alice), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("_udp2 status = %d", response.StatusCode)
	}
	frame := func(source, target string) wire.UDPFrame {
		return wire.UDPFrame{Source: mustAddrPort(source), Target: mustAddrPort(target), Payload: []byte("x")}
	}
	writeUDPFrame(t, writer, frame("192.0.2.1:1000", "198.51.100.9:53"))
	writeUDPFrame(t, writer, frame("192.0.2.2:1000", "192.0.2.77:9"))
	writeUDPFrame(t, writer, frame("192.0.2.3:1000", "192.0.2.10:443")) // goes through, so the stream is alive
	if got, err := readUDPFrame(response.Body); err != nil || string(got.Payload) != "x" {
		t.Fatalf("the stream did not go on after refused datagrams: %v, %v", got, err)
	}
	var sum plugin.TunnelRefusals
	waitFor(t, 2*time.Second, func() bool {
		for i, n := range inboundRefusals(t, collect(t, h.e), "de1") {
			sum[i] += n
		}
		return sum[plugin.RefusedBlocked] == 1 && sum[plugin.RefusedConnUnreachable] == 1
	})
	for i, n := range sum {
		if n != 0 && plugin.TunnelRefusal(i) != plugin.RefusedBlocked && plugin.TunnelRefusal(i) != plugin.RefusedConnUnreachable {
			t.Fatalf("refusals = %v", describeRefusals(sum))
		}
	}
	_ = writer.Close()
	_ = response.Body.Close()
}

func journalHarness(t *testing.T, level slog.Level, ts timings) (*testHarness, *refusalRecords, *testClock, credentialFixture) {
	t.Helper()
	records := &refusalRecords{min: level}
	clock := &testClock{}
	clock.set(time.Now())
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarnessEnv(t, []plugin.UserCred{alice.cred}, &testEgress{}, ts, func(env *nodeengine.Env) {
		env.Log = slog.New(records)
		env.Now = clock.Now
	})
	return h, records, clock, alice
}

// A reason that points at the node goes to the journal at once, then at most once in the window with the number since, and
// never carries more than the inbound, the reason, the code and the count.
func TestJournalHearsOfARefusalOnceInTheWindowWithItsCount(t *testing.T) {
	h, records, clock, _ := journalHarness(t, slog.LevelWarn, timings{})
	h.in.refuse(plugin.RefusedOtherDeadlines, 0)
	for range 5 {
		h.in.refuse(plugin.RefusedOtherDeadlines, 0)
		h.in.refuse(plugin.RefusedStreamClosed, 0)
	}
	warns := records.at(slog.LevelWarn)
	if len(warns) != 2 { // one per reason
		t.Fatalf("%d records after the first burst, want 2: %+v", len(warns), warns)
	}
	first := warns[0]
	if first.attrs["code"] != "trusttunnel_refused_other_deadlines" || first.attrs["inbound"] != "de1" ||
		first.attrs["reason"] != "other_deadlines" || first.attrs["count"] != "1" || len(first.attrs) != 4 {
		t.Fatalf("first record = %+v", first)
	}
	clock.set(clock.Now().Add(refusalLogEvery + time.Second))
	h.in.refuse(plugin.RefusedOtherDeadlines, 0)
	warns = records.at(slog.LevelWarn)
	if len(warns) != 3 || warns[2].attrs["count"] != "6" {
		t.Fatalf("after the window: %+v, want a third record counting 6", warns)
	}
	if got := inboundRefusals(t, collect(t, h.e), "de1"); got[plugin.RefusedOtherDeadlines] != 7 || got[plugin.RefusedStreamClosed] != 5 {
		t.Fatalf("counters = %v", describeRefusals(got))
	}
}

// What is the client's own doing never reaches the journal, however often it happens.
func TestJournalNeverHearsOfOrdinaryTraffic(t *testing.T) {
	h, records, _, _ := journalHarness(t, slog.LevelWarn, timings{})
	for range 500 {
		h.in.refuse(plugin.RefusedBlocked, 443)
		h.in.refuse(plugin.RefusedNoIPv4, 443)
		h.in.refuse(plugin.RefusedOtherICMP, 0)
		h.in.refuse(plugin.RefusedOtherAuthority, 0)
	}
	if warns := records.at(slog.LevelWarn); len(warns) != 0 {
		t.Fatalf("the journal heard of ordinary traffic: %+v", warns)
	}
	got := inboundRefusals(t, collect(t, h.e), "de1")
	if got[plugin.RefusedBlocked] != 500 || got[plugin.RefusedNoIPv4] != 500 || got[plugin.RefusedOtherICMP] != 500 || got[plugin.RefusedOtherAuthority] != 500 {
		t.Fatalf("counters = %v", describeRefusals(got))
	}
}

// Connection errors and timeouts are the traffic of any node until they come in numbers.
func TestJournalHearsOfConnectionErrorsAndTimeoutsOnlyAboveTheThreshold(t *testing.T) {
	for _, reason := range []plugin.TunnelRefusal{plugin.RefusedConnRefused, plugin.RefusedConnUnreachable, plugin.RefusedConnDNS,
		plugin.RefusedConnOther, plugin.RefusedTimeout} {
		t.Run(reason.Name(), func(t *testing.T) {
			h, records, clock, _ := journalHarness(t, slog.LevelWarn, timings{})
			for range refusalFaultThreshold - 1 {
				h.in.refuse(reason, 443)
			}
			if warns := records.at(slog.LevelWarn); len(warns) != 0 {
				t.Fatalf("%d records below the threshold", len(warns))
			}
			h.in.refuse(reason, 443)
			warns := records.at(slog.LevelWarn)
			if len(warns) != 1 || warns[0].attrs["count"] != "30" || warns[0].attrs["reason"] != reason.Name() {
				t.Fatalf("at the threshold: %+v", warns)
			}
			// A slow trickle across windows never adds up to the threshold.
			for range 3 * refusalFaultThreshold {
				clock.set(clock.Now().Add(refusalLogEvery / 2))
				h.in.refuse(reason, 443)
			}
			if got := len(records.at(slog.LevelWarn)); got != 1 {
				t.Fatalf("%d records for a trickle, want the one from before", got)
			}
		})
	}
}

// Debug records carry the reason and the port, never an address, a name or a credential.
func TestDebugRecordHasReasonAndPortButNoDestination(t *testing.T) {
	h, records, _, alice := journalHarness(t, slog.LevelDebug, timings{})
	h.egress.tcpDial = func(string) (net.Conn, error) { return nil, egress.ErrBlocked }
	client := newHTTP2Client(t, h.addr)
	response, _ := client.connect(t, "secret-host.example:8443", authValue(t, alice), false)
	_ = response.Body.Close()
	debugs := records.at(slog.LevelDebug)
	if len(debugs) != 1 || debugs[0].attrs["reason"] != "blocked" || debugs[0].attrs["port"] != "8443" || debugs[0].attrs["inbound"] != "de1" {
		t.Fatalf("debug records = %+v", debugs)
	}
	for _, rec := range records.recs {
		for key, value := range rec.attrs {
			if strings.Contains(value, "secret-host") || strings.Contains(value, "alice") || strings.Contains(key, "host") {
				t.Fatalf("record %+v names the destination or the credential", rec)
			}
		}
	}
}
