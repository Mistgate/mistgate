package agent

import (
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
)

// The site the bridge arrives at (the entry's own port) answers HTTP/2 with the SETTINGS and the connection window of every
// other site of the node.
func TestBridgeSiteSpeaksHTTP2LikeEverySite(t *testing.T) {
	cert := sitehttptest.Certificate(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	site := &bridgeSiteServer{}
	defer site.Close()
	if err := site.Sync(uint16(port), func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }, "",
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	sitehttptest.Check(t, "bridge", conn)
}
