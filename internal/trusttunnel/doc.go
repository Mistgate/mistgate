// Package trusttunnel implements the TrustTunnel wire format from the public
// specification of AdGuard's Apache-2.0 TrustTunnel project. This is an
// independent implementation and contains no upstream code; it imports only
// the standard library.
//
// It covers the format only, not transport: the _udp2 datagram frames
// (UDPDecoder, EncodeUDPClientFrame, EncodeUDPServerFrame), the
// Proxy-Authorization Basic credentials, the CONNECT :authority including the
// _check, _udp2 and _icmp pseudo-hosts, and tt:// deep links.
//
// The format is defined by what the official client and server do, so the
// readers here accept what they accept and the tests pin each rule with bytes
// written out by hand. Round-trip tests alone are not evidence of
// compatibility: a codec that is wrong in the same way on both sides passes
// them.
package trusttunnel
