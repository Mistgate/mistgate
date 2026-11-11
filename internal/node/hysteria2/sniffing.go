package hysteria2

import (
	"net"
	"net/netip"

	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/sniff"
)

// routeOnlyRequestHook keeps the client's destination when it is an IP literal while preserving the sniffer's
// inspection, protocol checks and putback bytes. Domain requests continue through the sniffer unchanged.
type routeOnlyRequestHook struct {
	delegate server.RequestHook
}

var _ server.RequestHook = (*routeOnlyRequestHook)(nil)

func newRouteOnlyRequestHook() server.RequestHook {
	return &routeOnlyRequestHook{delegate: &sniff.Sniffer{}}
}

func (h *routeOnlyRequestHook) Check(isUDP bool, reqAddr string) bool {
	return h.delegate.Check(isUDP, reqAddr)
}

func (h *routeOnlyRequestHook) TCP(stream server.HyStream, reqAddr *string) ([]byte, error) {
	original := *reqAddr
	putback, err := h.delegate.TCP(stream, reqAddr)
	if isIPLiteralRequestAddr(original) {
		*reqAddr = original
	}
	return putback, err
}

// UDP: since core v2.13.0 the server holds a session's first packets until the hook says done, and the sniffer asks for more
// while a QUIC ClientHello spans several packets. An IP literal keeps its address whatever the sniffer finds, so there is
// nothing to wait for: it is done at once and the packets are not delayed. A domain request goes through the sniffer, which
// may ask for more (done=false).
func (h *routeOnlyRequestHook) UDP(packets [][]byte, reqAddr *string) (bool, error) {
	if isIPLiteralRequestAddr(*reqAddr) {
		return true, nil
	}
	return h.delegate.UDP(packets, reqAddr)
}

func isIPLiteralRequestAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	_, err = netip.ParseAddr(host)
	return err == nil
}
