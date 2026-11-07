package tcpcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestConnectKindIsAPlainConnectWithATiming(t *testing.T) {
	open, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	openPort := uint16(open.Addr().(*net.TCPAddr).Port)
	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := uint16(closed.Addr().(*net.TCPAddr).Port)
	_ = closed.Close()

	family, verdicts, ms, err := ProbeTimed(context.Background(), "127.0.0.1", []Target{{Port: openPort, Kind: KindConnect}, {Port: closedPort, Kind: KindConnect}})
	if err != nil || family != "4" || verdicts[openPort] != VerdictOurs || verdicts[closedPort] != VerdictRefused {
		t.Fatalf("ProbeTimed = %q %v %v", family, verdicts, err)
	}
	for _, p := range []uint16{openPort, closedPort} {
		if v, ok := ms[p]; !ok || v < 0 || v > 5000 {
			t.Errorf("the time of port %d = %d, %v", p, v, ok)
		}
	}
	// Probe is unchanged: the same verdicts, no timing
	if _, plain, err := Probe(context.Background(), "127.0.0.1", []Target{{Port: openPort, Kind: KindConnect}}); err != nil || plain[openPort] != VerdictOurs {
		t.Fatalf("Probe = %v, %v", plain, err)
	}
	for name, target := range map[string]Target{
		"connect with an SNI":    {Port: 443, Kind: KindConnect, SNI: "example.com"},
		"connect with a pin":     {Port: 443, Kind: KindConnect, PinSHA256: hex.EncodeToString(make([]byte, 32))},
		"connect with a token":   {Port: 443, Kind: KindConnect, Token: make([]byte, MarkSize)},
		"connect with an answer": {Port: 443, Kind: KindConnect, Answer: make([]byte, MarkSize)},
	} {
		if err := Validate("127.0.0.1", []Target{target}); err != ErrBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTLSTimingCoversTheHandshakeAndMatchesThePin(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.StartTLS()
	defer server.Close()
	port := uint16(server.Listener.Addr().(*net.TCPAddr).Port)
	leaf := server.TLS.Certificates[0].Certificate[0]
	sum := sha256.Sum256(leaf)

	_, verdicts, ms, err := probeTimed(context.Background(), "127.0.0.1", []Target{{Port: port, Kind: KindTLS, SNI: "example.com", PinSHA256: hex.EncodeToString(sum[:])}}, probeOptions{})
	if err != nil || verdicts[port] != VerdictOurs {
		t.Fatalf("the pinned certificate: %v %v", verdicts, err)
	}
	if _, ok := ms[port]; !ok {
		t.Errorf("no timing for port %s", strconv.Itoa(int(port)))
	}
	wrong := sha256.Sum256([]byte("another certificate"))
	_, verdicts, _, err = probeTimed(context.Background(), "127.0.0.1", []Target{{Port: port, Kind: KindTLS, SNI: "example.com", PinSHA256: hex.EncodeToString(wrong[:])}}, probeOptions{})
	if err != nil || verdicts[port] == VerdictOurs {
		t.Fatalf("a wrong pin was accepted: %v %v", verdicts, err)
	}
}
