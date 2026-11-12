package vless

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/mistgate/mistgate/internal/plugin"
)

// xray-core's dispatcher sniffer (app/dispatcher/default.go sniffer()) holds the first bytes of a flow for up to 200 ms
// while it looks for a protocol: a UDP datagram under 20 bytes (the uTP sniffer wants 20) is held until the wait runs out.
// Nothing on the node uses what it finds, so the first datagram of a flow must answer as fast as the second.
func TestVLESSFirstUDPDatagramDoesNotWaitForSniffer(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			spec := f.spec("node-a", transport)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, "66ad4540-b58c-4ad2-9926-ea63445a9b57")

			echo := startUDPEcho(t)
			for name, payload := range map[string][]byte{"dns": dnsQuery(t), "short": []byte("0123456789abcdef")} {
				// Best of three fresh flows: a loaded machine slows one flow, not all of them, while the sniffer waits 200 ms every time.
				first, second := time.Hour, time.Hour
				for range 3 {
					a, b := client.udpFlowTimes(t, echo, payload)
					first, second = min(first, a), min(second, b)
				}
				t.Logf("UDP %s: first datagram %v, second %v", name, first, second)
				if first > second+100*time.Millisecond {
					t.Errorf("UDP %s: the first datagram of a flow took %v, the next %v: the dispatcher sniffer holds it", name, first, second)
				}
			}
		})
	}
}

func dnsQuery(t *testing.T) []byte {
	t.Helper()
	name := dnsmessage.MustNewName("example.test.")
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 0x4d27, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	out, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func startUDPEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(b[:n], from)
		}
	}()
	return pc.LocalAddr().String()
}

// udpFlowTimes opens a SOCKS5 UDP association (one flow through the tunnel) and returns how long the echo of the first and
// of the second datagram took.
func (c *xrayClient) udpFlowTimes(t *testing.T, target string, payload []byte) (first, second time.Duration) {
	t.Helper()
	tcp, err := net.DialTimeout("tcp", c.socks, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	_ = tcp.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := tcp.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(tcp, method[:]); err != nil || method != [2]byte{5, 0} {
		t.Fatalf("SOCKS method negotiation: %v %v", method, err)
	}
	if _, err := tcp.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(tcp, reply[:]); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatalf("SOCKS UDP ASSOCIATE reply %v: %v", reply, err)
	}
	relay := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(binary.BigEndian.Uint16(reply[8:]))}
	udp, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()

	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	dst, port := net.ParseIP(host).To4(), mustPort(t, portText)
	packet := append([]byte{0, 0, 0, 1, dst[0], dst[1], dst[2], dst[3], byte(port >> 8), byte(port)}, payload...)
	one := func() time.Duration {
		_ = udp.SetDeadline(time.Now().Add(5 * time.Second))
		start := time.Now()
		if _, err := udp.Write(packet); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 2048)
		n, err := udp.Read(got)
		if err != nil {
			t.Fatalf("no UDP answer through the tunnel: %v", err)
		}
		elapsed := time.Since(start)
		if string(got[10:n]) != string(payload) {
			t.Fatalf("UDP answer %x differs from the question %x", got[10:n], payload)
		}
		return elapsed
	}
	first = one()
	second = one()
	return first, second
}

func mustPort(t *testing.T, s string) int {
	t.Helper()
	p, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
