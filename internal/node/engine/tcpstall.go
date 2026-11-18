package engine

// The TCP stall rule shared by the engines that hold the client's TCP connection (VLESS over REALITY and behind its TLS front,
// TrustTunnel over HTTP/2): the connection ended with bytes of the node the client never acknowledged, after at least
// stallMinRetrans retransmissions. A stall whose client had acknowledged first16KMin to first16KMax bytes is the TSPU's
// freeze after the first bytes. TCPStallOf (tcpstall_linux.go) reads the socket; the counters are the engine's own.
const (
	stallMinRetrans = 3
	first16KMin     = 8 << 10
	first16KMax     = 32 << 10
)

// TCPStallRule applies the rule to the TCP_INFO numbers: bytesSent counts retransmitted bytes too (0 on kernels before
// 4.19, where unackedSegs, the segments still in flight, decides).
func TCPStallRule(totalRetrans uint32, bytesSent, bytesRetrans, bytesAcked uint64, unackedSegs uint32) (stalled, first16K bool) {
	if totalRetrans < stallMinRetrans {
		return false, false
	}
	switch {
	case bytesSent > 0:
		fresh := bytesSent - min(bytesRetrans, bytesSent)
		if fresh <= bytesAcked {
			return false, false
		}
	case unackedSegs == 0:
		return false, false
	}
	return true, bytesAcked >= first16KMin && bytesAcked <= first16KMax
}
