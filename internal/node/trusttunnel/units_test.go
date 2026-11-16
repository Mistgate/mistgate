package trusttunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// connectionOf returns the one connection the inbound holds.
func connectionOf(t *testing.T, in *inbound) *connectionState {
	t.Helper()
	in.connMu.Lock()
	defer in.connMu.Unlock()
	if len(in.conns) != 1 {
		t.Fatalf("the inbound holds %d connections, want 1", len(in.conns))
	}
	for _, connection := range in.conns {
		return connection
	}
	return nil
}

// detach is what Kick and Apply call for a credential on one connection; it must end that credential's tunnels itself, not
// rely on its callers to do it (a tunnel that begins while they run is found only here).
func TestDetachEndsOneCredentialsTunnelsOnTheConnection(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)
	aliceResponse, aliceWriter := client.connect(t, "example.com:80", authValue(t, alice), true)
	bobResponse, bobWriter := client.connect(t, "example.com:80", authValue(t, bob), true)
	exchange(t, aliceWriter, aliceResponse.Body, "alice")
	exchange(t, bobWriter, bobResponse.Body, "bob")

	connection, credential := connectionOf(t, h.in), h.in.idx.Load().byUser["alice"]
	if !connection.detach(credential) {
		t.Fatal("detach found no tunnel of the credential")
	}
	if !endsWithin(aliceResponse.Body, time.Second) {
		t.Fatal("the detached credential's tunnel is still open")
	}
	exchange(t, bobWriter, bobResponse.Body, "bob again")
	if connection.detach(credential) {
		t.Fatal("detach reported a tunnel of a credential that has none left")
	}
	_ = aliceWriter.Close()
	_ = bobWriter.Close()
}

// beginStream is the last gate: it refuses a credential that was removed or expired after it passed authentication.
func TestBeginStreamRefusesAnExpiredOrRemovedCredential(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	credential := h.in.idx.Load().byUser["alice"]
	begin := func() (*streamState, bool) {
		local, remote := net.Pipe()
		t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
		request := httptest.NewRequest(http.MethodConnect, "https://example.com:80", nil)
		return h.in.beginStream(request, httptest.NewRecorder(), local, credential)
	}

	stream, ok := begin()
	if !ok {
		t.Fatal("a valid credential was refused")
	}
	h.in.finishStream(stream)

	credential.validUntil.Store(time.Now().Add(-time.Minute).Unix())
	if _, ok := begin(); ok {
		t.Fatal("an expired credential began a tunnel")
	}
	credential.validUntil.Store(0)
	if stream, ok := begin(); !ok {
		t.Fatal("the credential is valid again and was refused")
	} else {
		h.in.finishStream(stream)
	}
	credential.removed.Store(true)
	if _, ok := begin(); ok {
		t.Fatal("a removed credential began a tunnel")
	}
	credential.removed.Store(false)
}

// A socket that arrives after the stream has ended is closed at once, and a cleanup that arrives then runs at once.
func TestSocketAndCleanupThatArriveAfterTheStreamEndedAreReleased(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &streamState{ctx: ctx, cancel: cancel}
	stream.free()

	local, remote := net.Pipe()
	defer remote.Close()
	if stream.setUpstream(local) {
		t.Fatal("an ended stream took a new upstream")
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("the upstream of an ended stream was not closed: %v", err)
	}
	ran := false
	stream.setCleanup(func() { ran = true })
	if !ran {
		t.Fatal("the cleanup of an ended stream did not run")
	}
}

// The replies a client has not taken yet are bounded in count and in bytes, and queueing never waits.
func TestReplyQueueIsBoundedInFramesAndInBytes(t *testing.T) {
	in := &inbound{e: &eng{timing: defaultTimings}}
	stream := &streamState{ctx: context.Background(), cred: &credState{}}

	small := newUDPTunnel(in, stream)
	for range 5 * udpReplyFrames {
		small.enqueue(make([]byte, 40), 1)
	}
	if got := len(small.out); got != udpReplyFrames {
		t.Fatalf("queued %d small frames, want the bound of %d", got, udpReplyFrames)
	}
	if got := small.queued.Load(); got != int64(udpReplyFrames*40) {
		t.Fatalf("queued bytes = %d, want %d", got, udpReplyFrames*40)
	}

	large := newUDPTunnel(in, stream)
	const frame = 64 << 10
	for range 4 * udpReplyBytes / frame {
		large.enqueue(make([]byte, frame), frame)
	}
	if got := len(large.out); got != udpReplyBytes/frame {
		t.Fatalf("queued %d large frames, want %d (the byte bound)", got, udpReplyBytes/frame)
	}
	if got := large.queued.Load(); got > udpReplyBytes {
		t.Fatalf("queued bytes = %d, over the bound of %d", got, udpReplyBytes)
	}
}

// The queued replies of all the streams of a credential together stay within one bound, so a credential cannot hold the
// per-stream bound once for every connection it opens; the bytes come back as the writers take the frames.
func TestReplyQueueBytesAreBoundedPerCredentialAcrossStreams(t *testing.T) {
	in := &inbound{e: &eng{timing: defaultTimings}}
	credential := &credState{}
	const frame = 64 << 10
	var tunnels []*udpTunnel
	for range 16 { // as many streams as a credential has connections
		tunnels = append(tunnels, newUDPTunnel(in, &streamState{ctx: context.Background(), cred: credential}))
	}
	for range 4 * udpAccountReplyBytes / frame {
		for _, tunnel := range tunnels {
			tunnel.enqueue(make([]byte, frame), frame)
		}
	}
	if got := credential.udpQueued.Load(); got > udpAccountReplyBytes || got < udpAccountReplyBytes-frame {
		t.Fatalf("queued bytes of the credential = %d, want the bound of %d", got, udpAccountReplyBytes)
	}
	var held int64
	for _, tunnel := range tunnels {
		held += tunnel.queued.Load()
	}
	if held != credential.udpQueued.Load() {
		t.Fatalf("streams hold %d bytes, the credential counts %d", held, credential.udpQueued.Load())
	}
	// Another credential is not held back by this one.
	other := newUDPTunnel(in, &streamState{ctx: context.Background(), cred: &credState{}})
	other.enqueue(make([]byte, frame), frame)
	if len(other.out) != 1 {
		t.Fatal("a credential's full queues blocked another credential")
	}
	// A writer that takes a frame gives its bytes back to the credential.
	before := credential.udpQueued.Load()
	tunnels[0].stream.cred.udpQueued.Add(-frame)
	tunnels[0].queued.Add(-frame)
	<-tunnels[0].out
	tunnels[0].enqueue(make([]byte, frame), frame)
	if credential.udpQueued.Load() != before {
		t.Fatalf("queued bytes after take and refill = %d, want %d", credential.udpQueued.Load(), before)
	}
}
