package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/agent"
)

// A server that is not the panel (a CDN edge, a reverse proxy) answers the agent's TLS name with an alert. The enroll
// command then says what that means instead of leaving "handshake failure" to be read as a certificate problem.
func TestEnrollHintExplainsARemoteTLSAlert(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, errors.New("unknown name") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake()
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = agent.Enroll(ctx, agent.EnrollConfig{
		StateDir: t.TempDir(), Panel: ln.Addr().String(), SNI: "abc123.invalid", CASHA256: strings.Repeat("ab", 32),
		Token: "not-a-real-token", Timeout: 10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "remote error: tls:") {
		t.Fatalf("enroll against a server that refuses the name = %v, want a remote TLS alert", err)
	}
	hint := enrollHint(err, false)
	for _, want := range []string{"--agent-addr", "--agent-listen", "nothing to replace"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q lacks %q", hint, want)
		}
	}
	if enrollHint(err, true) != "" {
		t.Fatal("the link enrollment has no agent name: no hint there")
	}
	if enrollHint(errors.New("enrollment token unknown, expired or used"), false) != "" {
		t.Fatal("an answer of the panel itself gets no hint")
	}
	if enrollHint(&net.OpError{Op: "dial", Err: errors.New("connection refused")}, false) != "" {
		t.Fatal("a connection error is not a TLS alert")
	}
}
