//go:build !js

package auth

import (
	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The DecoySiteService (proto/mistgate/decoysite/v1) is a package of its own, kept out of the Cloudflare Workers (the edge keeps no
// bundles; the app mounts a stub there). A signed-in admin reaches every procedure as the owner. API tokens and the MCP reach only
// what the decoy_site tools need, and a token never changes what a node or the panel serves without the owner:
//
//   - reads (list, one site, one file): any profile, directly;
//   - saving a draft (CreateDecoySite, AddDecoySiteVersion) and reading a version's archive to edit it on top (DownloadDecoySite):
//     the operator profile, only inside the MCP while it plans or applies; the handler refuses a call with no approved plan unless
//     the site is a draft (fleet draftOnly);
//   - everything that puts a site on a node or the panel, takes it off, marks it ready, reverts it, deletes it, or saves a new
//     version of a ready site: the owner's approval, through the plan and apply tools.
//
// EditDecoySiteFile, UpdateDecoySite, CopyDecoySite, SetPanelDecoySource, ListDecoyPages / GetDecoyPagePreview and the three template
// calls (ListDecoyTemplates, PreviewDecoyTemplate, GenerateDecoySite) stay the owner's own: an agent makes its sites its own way.
// So do the three calls about the photo keys and source (GetDecoyPhotos, SetDecoyPhotosKey, SetDecoyPhotosSource): an agent cannot read or change a key.
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
		decoysitev1connect.DecoySiteServiceListDecoyTemplatesProcedure,
		decoysitev1connect.DecoySiteServicePreviewDecoyTemplateProcedure,
		decoysitev1connect.DecoySiteServiceGenerateDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceGetDecoyPhotosProcedure,
		decoysitev1connect.DecoySiteServiceSetDecoyPhotosKeyProcedure,
		decoysitev1connect.DecoySiteServiceSetDecoyPhotosSourceProcedure,
	} {
		procedureLevels[p] = levelOwner
	}
	for p, access := range map[string]int{
		decoysitev1connect.DecoySiteServiceListDecoySitesProcedure:      TokenAccessDirect,
		decoysitev1connect.DecoySiteServiceGetDecoySiteProcedure:        TokenAccessDirect,
		decoysitev1connect.DecoySiteServiceGetDecoyFileProcedure:        TokenAccessDirect,
		decoysitev1connect.DecoySiteServiceCreateDecoySiteProcedure:     TokenAccessPlanning,
		decoysitev1connect.DecoySiteServiceDownloadDecoySiteProcedure:   TokenAccessPlanning,
		decoysitev1connect.DecoySiteServiceAddDecoySiteVersionProcedure: TokenAccessDraft,
		decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure:   TokenAccessApproved,
		decoysitev1connect.DecoySiteServiceAssignDecoySiteProcedure:     TokenAccessApproved,
		decoysitev1connect.DecoySiteServiceUnassignDecoySiteProcedure:   TokenAccessApproved,
		decoysitev1connect.DecoySiteServiceRevertDecoySiteProcedure:     TokenAccessApproved,
		decoysitev1connect.DecoySiteServiceDeleteDecoySiteProcedure:     TokenAccessApproved,
	} {
		tokenProcedures[p] = access
	}
	for p, role := range map[string]string{
		decoysitev1connect.DecoySiteServiceListDecoySitesProcedure:      store.RoleReadonly,
		decoysitev1connect.DecoySiteServiceGetDecoySiteProcedure:        store.RoleReadonly,
		decoysitev1connect.DecoySiteServiceGetDecoyFileProcedure:        store.RoleReadonly,
		decoysitev1connect.DecoySiteServiceCreateDecoySiteProcedure:     store.RoleHelper,
		decoysitev1connect.DecoySiteServiceAddDecoySiteVersionProcedure: store.RoleHelper,
		decoysitev1connect.DecoySiteServiceDownloadDecoySiteProcedure:   store.RoleHelper,
	} {
		tokenProcedureRoles[p] = role
	}
	// the tools whose approved plan may call them (the keys are the tool names without _plan / _apply)
	grantProcedures["decoy_site_ready"] = []string{decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure}
	grantProcedures["decoy_site_assign"] = []string{decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure, decoysitev1connect.DecoySiteServiceAssignDecoySiteProcedure}
	grantProcedures["decoy_site_unassign"] = []string{decoysitev1connect.DecoySiteServiceUnassignDecoySiteProcedure}
	grantProcedures["decoy_site_version"] = []string{decoysitev1connect.DecoySiteServiceAddDecoySiteVersionProcedure}
	grantProcedures["decoy_site_revert"] = []string{decoysitev1connect.DecoySiteServiceRevertDecoySiteProcedure}
	grantProcedures["decoy_site_delete"] = []string{decoysitev1connect.DecoySiteServiceDeleteDecoySiteProcedure}
	// the plan of an assignment asks what it would replace: a dry run changes nothing, even for a draft that is not ready yet
	planningExtra[decoysitev1connect.DecoySiteServiceAssignDecoySiteProcedure] = planningCheck((*decoysitev1.AssignDecoySiteRequest).GetDryRun)
}
