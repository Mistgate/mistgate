package vless

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"

	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
)

// The REALITY "self" site answers HTTP/2 with the SETTINGS and the connection window of every other site of the node.
func TestSelfSiteSpeaksHTTP2LikeEverySite(t *testing.T) {
	cert := sitehttptest.Certificate(t)
	s, err := startSite("frames", func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := net.Dial(s.network, s.addr)
	if err != nil {
		t.Fatal(err)
	}
	sitehttptest.Check(t, "VLESS self", conn)
}
