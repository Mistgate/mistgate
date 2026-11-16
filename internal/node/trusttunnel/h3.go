package trusttunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// H3 limits. Receive flow-control credit is capped at 1 MiB per stream and 8 MiB per QUIC connection (4 MiB to start), at
// most 250 streams per connection. The 16-connection credential limit counts QUIC with TCP, so the receive windows of one
// credential are at most 16 x 8 MiB = 128 MiB over HTTP/3 (HTTP/2: 16 x 16 MiB = 256 MiB). The credential's _udp2 memory
// comes on top and is bounded for the credential as a whole (udp.go): 4 MiB of read buffers and 4 MiB of queued replies.
const (
	maxH3IncomingStreams      = 250
	h3InitialStreamWindow     = 256 << 10
	h3MaxStreamWindow         = 1 << 20
	h3InitialConnectionWindow = 4 << 20
	h3MaxConnectionWindow     = 8 << 20
	maxH3IncomingUniStreams   = 16
	// h3KeepAlive: the server pings a connection that has been silent this long (at most half the negotiated idle time),
	// so a quiet tunnel is not ended by the client's shorter idle time or a NAT that forgets a silent UDP flow. A peer that
	// is gone does not answer, and the connection ends at the idle time as it would without pings.
	h3KeepAlive = 20 * time.Second
)

type h3ConnectionKey struct{}

func (in *inbound) startH3(addr string) error {
	packetConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listen udp %d: %w", in.spec.Listen.Port, err)
	}

	limiter := sitehttp.NewAnonymousLimiter()
	tlsConfig := in.srv.TLSConfig.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{"h3"}
	quicConfig := &quic.Config{
		HandshakeIdleTimeout:           in.e.timing.readHeader,
		MaxIdleTimeout:                 max(in.e.timing.serverIdle, in.e.timing.streamIdle),
		InitialStreamReceiveWindow:     h3InitialStreamWindow,
		MaxStreamReceiveWindow:         h3MaxStreamWindow,
		InitialConnectionReceiveWindow: h3InitialConnectionWindow,
		MaxConnectionReceiveWindow:     h3MaxConnectionWindow,
		MaxIncomingStreams:             maxH3IncomingStreams,
		MaxIncomingUniStreams:          maxH3IncomingUniStreams,
		KeepAlivePeriod:                h3KeepAlive,
	}
	// A client's address is validated by the handshake itself (and the server sends at most three times what it has
	// received until then). While the anonymous set is crowded, a new attempt must also prove its address with a Retry
	// first: spoofed sources never answer it, so a flood of forged packets cannot fill the set and push real handshakes out.
	transport := &quic.Transport{
		Conn: packetConn,
		VerifySourceAddress: func(net.Addr) bool {
			if !limiter.Crowded() {
				return false
			}
			in.h3Retries.Add(1)
			return true
		},
	}
	listener, err := transport.ListenEarly(http3.ConfigureTLSConfig(tlsConfig), quicConfig)
	if err != nil {
		_ = transport.Close()
		_ = packetConn.Close()
		return fmt.Errorf("listen quic %d: %w", in.spec.Listen.Port, err)
	}
	server := &http3.Server{
		TLSConfig:      tlsConfig,
		QUICConfig:     quicConfig,
		Handler:        http.HandlerFunc(in.serveHTTP),
		IdleTimeout:    in.e.timing.connectionIdle,
		MaxHeaderBytes: 16 << 10,
	}
	server.ConnContext = func(ctx context.Context, conn *quic.Conn) context.Context {
		// A site that drops a connection it has no more room for says H3_NO_ERROR with no reason, and that is all an observer
		// sees.
		limiter.Add(conn, conn.RemoteAddr(), func() {
			_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "")
		})
		go func() {
			<-conn.Context().Done()
			limiter.Remove(conn)
			in.closeConnection(conn)
		}()
		return context.WithValue(ctx, h3ConnectionKey{}, conn)
	}

	in.h3Ln = packetConn
	in.h3Transport = transport
	in.h3Listener = listener
	in.h3Server = server
	in.h3ServeDone = make(chan struct{})
	in.h3Anonymous = limiter
	return nil
}

func (in *inbound) serveH3() {
	server, listener := in.h3Server, in.h3Listener
	in.everH3Serve = true
	go func() {
		err := server.ServeListener(listener)
		close(in.h3ServeDone)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			in.e.log.Warn("TrustTunnel HTTP/3 listener stopped", "code", codeH3ServeFailed, "inbound", in.spec.ID, "err", err)
			in.mu.Lock()
			if in.state == plugin.RunRunning {
				in.state, in.detail, in.since = plugin.RunFailed, err.Error(), in.e.now()
			}
			in.mu.Unlock()
		}
	}()
}

// stopH3 ends the QUIC side: no new connections, the open ones closed with H3_NO_ERROR, the UDP port released. Closing the
// listener first stops the intake while the transport can still carry the closing packets of the connections.
func (in *inbound) stopH3() {
	if in.h3Listener != nil {
		_ = in.h3Listener.Close()
	}
	if in.h3Server != nil {
		_ = in.h3Server.Close()
	}
	if in.h3Transport != nil {
		_ = in.h3Transport.Close()
	}
	if in.h3Ln != nil {
		_ = in.h3Ln.Close()
	}
}

func h3ConnFromContext(ctx context.Context) *quic.Conn {
	conn, _ := ctx.Value(h3ConnectionKey{}).(*quic.Conn)
	return conn
}

func (stream *streamState) startHTTP3() error {
	if !stream.h3 {
		return nil
	}
	streamer, ok := stream.w.(http3.HTTPStreamer)
	if !ok {
		return errors.New("http3 response writer does not expose its request stream")
	}
	raw := streamer.HTTPStream()
	stream.useHTTP3(raw, func() {
		code := quic.StreamErrorCode(http3.ErrCodeRequestCanceled)
		raw.CancelRead(code)
		raw.CancelWrite(code)
	}, func() {
		_ = raw.Close()
	})
	return nil
}
