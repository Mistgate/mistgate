//go:build js

package subs

import "github.com/mistgate/mistgate/internal/panel/access"

// TrustTunnel is not offered in the Cloudflare edition's first version (internal/panel/protocols/builtin plugins_js.go): no
// person has a key there, and the page builder of its links stays out of the Workers.
func pageTrustTunnelOf(access.SubView, string) *pageTrustTunnel { return nil }
