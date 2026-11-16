package hysteria2

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"

	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
)

// The Hysteria2 site answers HTTP/2 with the SETTINGS and the connection window of every other site of the node.
func TestSiteSpeaksHTTP2LikeEverySite(t *testing.T) {
	cert := sitehttptest.Certificate(t)
	port := freeTCPPort(t)
	m, err := startTCPMasq(port, func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), 4443)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	conn, err := net.Dial("tcp", bindAddr(port))
	if err != nil {
		t.Fatal(err)
	}
	sitehttptest.Check(t, "Hysteria2", conn)
}
