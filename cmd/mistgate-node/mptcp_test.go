package main

import (
	"context"
	"net"
	"testing"
)

// The node binary must open plain TCP listeners: go.mod sets godebug multipathtcp=0 (CVE-2026-98432 is reached through
// MPTCP listeners). A client that asks for MPTCP gets it only from an MPTCP listener, so the accepted connection tells.
// Without MPTCP in the kernel the answer is false either way: this proves something on Linux with net.mptcp.enabled=1,
// where it fails if the directive goes away.
func TestListenersDoNotUseMultipathTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	var d net.Dialer
	d.SetMultipathTCP(true)
	c, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	server, ok := <-accepted
	if !ok {
		t.Fatal("accept failed")
	}
	defer server.Close()
	mptcp, err := server.(*net.TCPConn).MultipathTCP()
	if err != nil {
		t.Fatal(err)
	}
	if mptcp {
		t.Fatal("the listener took a Multipath TCP connection: go.mod must keep `godebug multipathtcp=0`")
	}
}
