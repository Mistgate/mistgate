package vless

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// A client that sends its request and closes its side still gets an answer that starts later than a second and takes
// longer than one: once the upload ends, the download keeps the normal idle timeout (xray's 1 s DownlinkOnly would cut it).
func TestHalfClosedUploadStillGetsASlowAnswer(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "tcp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	const answer = 256 << 10
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := io.Copy(io.Discard, conn); err != nil { // the whole request, up to the client's half-close
			return
		}
		time.Sleep(1500 * time.Millisecond) // thinking
		chunk := payload(answer / 8)
		for i := 0; i < 8; i++ { // then a slow answer: about 1.4 s in all
			if _, err := conn.Write(chunk); err != nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()

	destination, err := xnet.ParseDestination("tcp:" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: &protocol.MemoryUser{Email: cred.CredID}})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: destination}})
	upR, upW := pipe.New(pipe.WithoutSizeLimit())
	downR, downW := pipe.New(pipe.WithoutSizeLimit())
	done := make(chan error, 1)
	go func() { done <- f.engine.inbounds[spec.ID].dispatch(ctx, &transport.Link{Reader: upR, Writer: downW}) }()

	if err := upW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("GET / HTTP/1.0\r\n\r\n"))); err != nil {
		t.Fatal(err)
	}
	_ = upW.Close() // the client's half-close

	got := 0
	deadline := time.After(10 * time.Second)
	for {
		read := make(chan error, 1)
		go func() {
			mb, err := downR.ReadMultiBuffer()
			got += int(mb.Len())
			buf.ReleaseMulti(mb)
			read <- err
		}()
		select {
		case err := <-read:
			if err == nil {
				continue
			}
			if err != io.EOF {
				t.Fatalf("download ended with %v after %d of %d bytes", err, got, answer)
			}
		case <-deadline:
			t.Fatalf("no end of the download after 10 s (%d of %d bytes)", got, answer)
		}
		break
	}
	if got != answer {
		t.Fatalf("download got %d bytes, want %d: the answer was cut after the client's half-close", got, answer)
	}
	if err := <-done; err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}
