//go:build js

package fleet

import (
	"context"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The Cloudflare edition keeps no decoy bundles and no library (decoy_library.go), so an upload for a node is refused.
func (f *Fleet) ownSiteFromUpload(context.Context, string, *decoy.Bundle) (store.NodeRow, error) {
	return store.NodeRow{}, edition.Refuse(edition.DecoyOwn)
}
