package httpserver

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// holdsConnection opens a raw HTTP/1.1 connection to ts, sends the head of a POST to path with the given content type
// and a declared body of 100 bytes of which only 1 is sent, and reports whether the server still holds the connection
// well after the listener's ReadTimeout (400 ms in these tests).
func holdsConnection(t *testing.T, ts *httptest.Server, path, contentType string) bool {
	t.Helper()
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST %s HTTP/1.1\r\nHost: x\r\nContent-Type: %s\r\nContent-Length: 100\r\n\r\n{", path, contentType)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err == nil {
			continue // a response; keep reading until the server closes
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return true
		}
		return false
	}
}

// A caller without a session (the public procedures reach the router that way) keeps the listener's read deadline
// whatever content type it claims; a signed-in streaming call has it lifted.
func TestStreamContentTypeLiftsDeadlinesOnlyForSignedInCalls(t *testing.T) {
	drain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) })
	router := apiRouter{{Path: "/mistgate.admin.v1.AuthService/", Handler: drain}}
	const path = "/mistgate.admin.v1.AuthService/GetLoginInfo"
	for _, tc := range []struct {
		name     string
		h        http.Handler
		ct       string
		wantHeld bool
	}{
		{"anonymous connect stream", router, "application/connect+json", false},
		{"anonymous grpc", router, "application/grpc", false},
		{"signed in connect stream", signedIn(router), "application/connect+json", true},
		{"signed in plain json", signedIn(router), "application/json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewUnstartedServer(tc.h)
			ts.Config.ReadTimeout = 400 * time.Millisecond
			ts.Start()
			t.Cleanup(ts.Close)
			if got := holdsConnection(t, ts, path, tc.ct); got != tc.wantHeld {
				t.Fatalf("held after the read timeout = %v, want %v", got, tc.wantHeld)
			}
		})
	}
}
