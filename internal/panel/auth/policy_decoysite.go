//go:build !js

package auth

import "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"

// The DecoySiteService (proto/mistgate/decoysite/v1) is a package of its own, kept out of the Cloudflare Workers (the edge keeps no
// bundles; the app mounts a stub there). Every procedure is the owner's, like the node calls that choose a node's site, and
// closed to API tokens and the MCP: what changes what a node or the panel shows goes through the existing node_decoy tool until
// the library has tools of its own.
func init() {
	for _, p := range []string{
		decoysitev1connect.DecoySiteServiceListDecoySitesProcedure,
		decoysitev1connect.DecoySiteServiceGetDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceCreateDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceAddDecoySiteVersionProcedure,
		decoysitev1connect.DecoySiteServiceEditDecoySiteFileProcedure,
		decoysitev1connect.DecoySiteServiceUpdateDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceCopyDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure,
		decoysitev1connect.DecoySiteServiceAssignDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceUnassignDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceSetPanelDecoySourceProcedure,
		decoysitev1connect.DecoySiteServiceRevertDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceDeleteDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceDownloadDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceListDecoyPagesProcedure,
		decoysitev1connect.DecoySiteServiceGetDecoyPagePreviewProcedure,
		decoysitev1connect.DecoySiteServiceGetDecoyFileProcedure,
	} {
		procedureLevels[p] = levelOwner
	}
}
