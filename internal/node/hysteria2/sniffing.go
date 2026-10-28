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

func (h *routeOnlyRequestHook) UDP(data []byte, reqAddr *string) error {
	original := *reqAddr
	err := h.delegate.UDP(data, reqAddr)
	if isIPLiteralRequestAddr(original) {
		*reqAddr = original
	}
	return err
}

func isIPLiteralRequestAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	_, err = netip.ParseAddr(host)
	return err == nil
}
