//go:build !js

package health

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	mp "github.com/mistgate/mistgate/internal/mtproto"
	"github.com/mistgate/mistgate/internal/mtproto/mtprototest"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

const mtprotoTestDomain = "proxy.example.com"

type mtprotoDCResult struct {
	tag uint32
	dc  int16
	req []byte
}

func TestDialMTProtoClientEye(t *testing.T) {
	key := mtprotoTestKey(7)

	t.Run("success reaches DC 2 and receives resPQ", func(t *testing.T) {
		got := make(chan mtprotoDCResult, 1)
		dc := mtprototest.NewDC(t, func(c *mtprototest.DCConn) {
			req, err := readMTProtoRequest(c)
			if err != nil {
				return
			}
			got <- mtprotoDCResult{tag: c.Tag, dc: c.DC, req: req}
			_ = writeMTProtoResponse(c, responsePQ(req))
		})
		addr := startMTProtoNode(t, key, mtprotoTestDomain, dc.Addr, siteFallback)
		tunnel := mustMTProtoDial(t, mtprotoTarget(addr, mtprotoTestDomain, key), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		})
		defer tunnel.Close()
		result := tunnel.(interface{ ProbeResult() Result }).ProbeResult()
		if result.Status != adminv1.CheckStatus_CHECK_STATUS_OK || result.LatencyMS == 0 || result.ConnectMS == 0 || result.ResponseMS == 0 {
			t.Fatalf("MTProto result: %+v", result)
		}
		select {
		case received := <-got:
			if received.tag != uint32(mp.TagAbridged) || received.dc != 2 {
				t.Fatalf("DC header tag=%#x dc=%d", received.tag, received.dc)
			}
			if len(received.req) != 40 || binary.LittleEndian.Uint32(received.req[20:24]) != reqPQMulti {
				t.Fatalf("request body: %x", received.req)
			}
		case <-time.After(time.Second):
			t.Fatal("fake DC did not receive req_pq_multi")
		}
	})

	t.Run("wrong domain is rejected by fallback", func(t *testing.T) {
		addr := startMTProtoNode(t, key, mtprotoTestDomain, "127.0.0.1:1", siteFallback)
		assertMTProtoDialCode(t, mtprotoTarget(addr, "wrong.example.com", key), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		}, "auth")
	})

	t.Run("wrong key is rejected by fallback", func(t *testing.T) {
		addr := startMTProtoNode(t, key, mtprotoTestDomain, "127.0.0.1:1", siteFallback)
		assertMTProtoDialCode(t, mtprotoTarget(addr, mtprotoTestDomain, mtprotoTestKey(9)), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		}, "auth")
	})

	t.Run("invalid TLS response is a TLS failure", func(t *testing.T) {
		addr := startMTProtoNode(t, key, mtprotoTestDomain, "127.0.0.1:1", func(_ context.Context, conn net.Conn, _ []byte) error {
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
			return conn.Close()
		})
		assertMTProtoDialCode(t, mtprotoTarget(addr, "wrong.example.com", key), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		}, "tls")
	})

	t.Run("refused node port is distinct", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		assertMTProtoDialCode(t, mtprotoTarget(addr, mtprotoTestDomain, key), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		}, "refused")
	})

	t.Run("DC refusal is an exit failure", func(t *testing.T) {
		dead, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dcAddr := dead.Addr().String()
		if err := dead.Close(); err != nil {
			t.Fatal(err)
		}
		addr := startMTProtoNode(t, key, mtprotoTestDomain, dcAddr, siteFallback)
		assertMTProtoDialCode(t, mtprotoTarget(addr, mtprotoTestDomain, key), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: time.Second,
		}, "exit_unreachable")
	})

	t.Run("DC response timeout is reported as timeout", func(t *testing.T) {
		dc := mtprototest.NewDC(t, func(c *mtprototest.DCConn) {
			_, _ = readMTProtoRequest(c)
			time.Sleep(250 * time.Millisecond)
		})
		addr := startMTProtoNode(t, key, mtprotoTestDomain, dc.Addr, siteFallback)
		assertMTProtoDialCode(t, mtprotoTarget(addr, mtprotoTestDomain, key), DialOptions{
			HandshakeTimeout: time.Second, ProbeTimeout: 50 * time.Millisecond,
		}, "timeout")
	})

	t.Run("Fake TLS handshake timeout is reported as timeout", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				defer conn.Close()
				var hello [517]byte
				_, _ = io.ReadFull(conn, hello[:])
				time.Sleep(250 * time.Millisecond)
			}
		}()
		assertMTProtoDialCode(t, mtprotoTarget(ln.Addr().String(), mtprotoTestDomain, key), DialOptions{
			HandshakeTimeout: 50 * time.Millisecond, ProbeTimeout: time.Second,
		}, "timeout")
	})
}

func startMTProtoNode(t *testing.T, key [mp.KeyLen]byte, domain, dcAddr string, fallback func(context.Context, net.Conn, []byte) error) string {
	t.Helper()
	srv, err := mp.New(mp.Config{
		Credentials:      []mp.Credential{{ID: "probe", Key: key}},
		Domains:          []string{domain},
		HandshakeTimeout: time.Second,
		DCAddress: func(dc int16) (string, bool) {
			return dcAddr, dc == 2
		},
		Fallback: fallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.ServeListener(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		srv.CloseAll()
		<-done
	})
	return ln.Addr().String()
}

func siteFallback(_ context.Context, conn net.Conn, _ []byte) error {
	response := []byte{
		0x16, 0x03, 0x03, 0x00, 0x26, // a TLS ServerHello with an unkeyed random
		0x02, 0x00, 0x00, 0x22, 0x03, 0x03,
	}
	response = append(response, make([]byte, 32)...)
	response = append(response,
		0x14, 0x03, 0x03, 0x00, 0x01, 0x01,
		0x17, 0x03, 0x03, 0x00, 0x01, 0x00,
	)
	_, _ = conn.Write(response)
	return conn.Close()
}

func mtprotoTarget(addr, domain string, key [mp.KeyLen]byte) Target {
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)
	settings, _ := json.Marshal(struct {
		Domains []string
	}{Domains: []string{domain}})
	return Target{
		Node:    store.NodeRow{Address: host},
		Inbound: store.FleetInboundRow{ID: "in_mtproto", Protocol: "mtproto"},
		Spec: plugin.InboundSpec{
			Listen:   plugin.Listen{Network: "tcp", Port: uint16(port)},
			Settings: settings,
		},
		CredID: "crd_health", Secret: hex.EncodeToString(key[:]),
	}
}

func mustMTProtoDial(t *testing.T, target Target, options DialOptions) Tunnel {
	t.Helper()
	tunnel, err := dialMTProto(context.Background(), target, options)
	if err != nil {
		t.Fatal(err)
	}
	return tunnel
}

func assertMTProtoDialCode(t *testing.T, target Target, options DialOptions, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := dialMTProto(ctx, target, options)
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) || probeErr.Code != want {
		t.Fatalf("dial error = %v, want code %q", err, want)
	}
}

// readMTProtoRequest reads one abridged packet as a Telegram DC does after an obfuscated2 header: no 0xef byte (the tag
// was in the header), the length in words, then an unencrypted message whose message_data_length matches its body.
func readMTProtoRequest(c *mtprototest.DCConn) ([]byte, error) {
	var first [1]byte
	if _, err := io.ReadFull(c, first[:]); err != nil {
		return nil, err
	}
	words := int(first[0])
	if first[0] == 0x7f {
		var length [3]byte
		if _, err := io.ReadFull(c, length[:]); err != nil {
			return nil, err
		}
		words = int(length[0]) | int(length[1])<<8 | int(length[2])<<16
	}
	if words == 0 || words > 64<<10/4 {
		return nil, errors.New("invalid request length")
	}
	msg := make([]byte, words*4)
	if _, err := io.ReadFull(c, msg); err != nil {
		return nil, err
	}
	if len(msg) < 20 || binary.LittleEndian.Uint32(msg[16:20]) != uint32(len(msg)-20) {
		return nil, errors.New("message_data_length does not match the message")
	}
	return msg, nil
}

func responsePQ(request []byte) []byte {
	msg := make([]byte, 76)
	binary.LittleEndian.PutUint64(msg[8:16], uint64(time.Now().Unix())<<32)
	binary.LittleEndian.PutUint32(msg[16:20], uint32(len(msg)-20))
	binary.LittleEndian.PutUint32(msg[20:24], resPQ)
	copy(msg[24:40], request[24:40])
	copy(msg[40:56], []byte("server-nonce-123"))
	msg[56] = 3 // TL bytes "pq", followed by one byte for alignment.
	copy(msg[57:60], []byte{1, 2, 3})
	binary.LittleEndian.PutUint32(msg[60:64], vectorConstructor)
	binary.LittleEndian.PutUint32(msg[64:68], 1)
	binary.LittleEndian.PutUint64(msg[68:76], 0x1122334455667788)
	return msg
}

func writeMTProtoResponse(c *mtprototest.DCConn, msg []byte) error {
	words := len(msg) / 4
	if len(msg)%4 != 0 {
		return errors.New("unaligned fake resPQ")
	}
	frame := []byte{byte(words)}
	if words >= 0x7f {
		frame = []byte{0x7f, byte(words), byte(words >> 8), byte(words >> 16)}
	}
	frame = append(frame, msg...)
	for len(frame) > 0 {
		n, err := c.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}

func mtprotoTestKey(b byte) [mp.KeyLen]byte {
	var key [mp.KeyLen]byte
	for i := range key {
		key[i] = b + byte(i)
	}
	return key
}
