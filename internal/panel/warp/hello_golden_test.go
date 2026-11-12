//go:build !js

package warp

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// wgcfHelloGolden is the first record WARP's registration client writes (the ClientHello of wgcf v2.3.0 for the host
// "api.example.test"), with the 32 random bytes zeroed, as the uTLS of Xray 26.3.27 (aa6edf4b) built it. A uTLS bump that changes
// a byte of it changes what Cloudflare sees of the registration, and fails here first.
const wgcfHelloGolden = "16030100990100009503030000000000000000000000000000000000000000000000000000000000000000000004c02cc030010000680000001500130000106170692e6578616d706c652e7465737400170000ff01000100000a00080006001d00170018000b00020100002300000010000b000908687474702f312e31000500050100000000000d00140012040308040401050308050501080606010201"

func TestWgcfClientHelloBytesAreStable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		head := make([]byte, 5)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		rec := append(head, make([]byte, binary.BigEndian.Uint16(head[3:]))...)
		if _, err := io.ReadFull(c, rec[5:]); err == nil {
			got <- rec
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = handshake(ctx, raw, "api.example.test", nil, utls.NewLRUClientSessionCache(1), wgcf230ClientHelloSpec()) // fails: nobody answers
	var rec []byte
	select {
	case rec = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no ClientHello arrived")
	}
	// record header (5), handshake header (4), version (2), random (32)
	for i := 11; i < 43; i++ {
		rec[i] = 0
	}
	if gotHex := hex.EncodeToString(rec); gotHex != wgcfHelloGolden {
		t.Fatalf("the WARP ClientHello changed:\n got %s\nwant %s", gotHex, wgcfHelloGolden)
	}
}
