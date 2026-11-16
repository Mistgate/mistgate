//go:build edge_link

package trusttunnel

import (
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The Link Worker only sends desired credentials to nodes; it does not render client deep links.
func renderTTLink(protocols.RenderInput) (plugin.Fragment, bool) {
	return plugin.Fragment{}, false
}
