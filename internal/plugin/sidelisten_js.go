//go:build js

package plugin

// TrustTunnel is not offered in the Cloudflare edition's first version (internal/panel/protocols/builtin plugins_js.go), so no
// inbound there has a side listener and none has HTTP/3; the real code is in sidelisten.go and stays out of the Workers.

func SideListens(InboundSpec) []Listen { return nil }

func TrustTunnelH3(InboundSpec) bool { return false }
