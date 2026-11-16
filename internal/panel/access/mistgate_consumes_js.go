//go:build js

package access

// The only protocol the first-party app takes without a link app's switch is TrustTunnel, which the Cloudflare edition does not
// offer (internal/panel/protocols/builtin plugins_js.go).
func (*Service) mistgateConsumes(string) bool { return false }
