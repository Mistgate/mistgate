//go:build !js

package fleet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"golang.org/x/net/publicsuffix"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The admin's DecoySiteService (proto/mistgate/decoysite/v1): the library of decoy sites, owner only. The Cloudflare edition
// keeps no bundles, so this is not built there (the app mounts a stub that refuses with unavailable_in_edition). File contents
// never go to the audit log: it gets the action, the site, the nodes and the hash.

type decoySiteService struct{ f *Fleet }

// DecoySiteHandler is the admin DecoySiteService; see NodeHandler for opts. Only the calls that carry files may be large.
func (f *Fleet) DecoySiteHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	// the gate comes after the caller's interceptors (the session check): the edge answers only a signed-in admin
	all := append(append([]connect.HandlerOption{connect.WithReadMaxBytes(3 << 20)}, opts...), connect.WithInterceptors(f.editionGate()))
	path, h := decoysitev1connect.NewDecoySiteServiceHandler(decoySiteService{f}, all...)
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; !strings.HasSuffix(p, "/CreateDecoySite") && !strings.HasSuffix(p, "/AddDecoySiteVersion") && !strings.HasSuffix(p, "/EditDecoySiteFile") {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		h.ServeHTTP(w, r)
	})
}

var siteIDPattern = regexp.MustCompile(`^dsi_[a-z0-9]{1,40}$`)

func refuse(code string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(code))
}

func (s decoySiteService) fail(what string, err error) error {
	if coded := decoyLibraryError(err); coded != nil {
		return coded
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case errors.Is(err, store.ErrNodeRetired):
		return errNodeRetired
	case errors.Is(err, store.ErrConflict):
		return refuse("node changed; retry the request")
	case errors.Is(err, decoy.ErrPageNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("decoy_page_not_found"))
	case errors.Is(err, decoy.ErrNotText):
		return refuse("decoy_file_binary")
	case errors.Is(err, decoy.ErrPreviewTooLarge):
		return refuse("decoy_preview_too_large")
	}
	return internalErr(s.f.log.Error, what, err)
}

// checkID refuses an id that cannot be a site's before it reaches the store.
func checkSiteID(id string) error {
	if !siteIDPattern.MatchString(id) {
		return connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	}
	return nil
}

// --- messages ---

var requiredChecks = []string{"index_html", "size", "file_count", "safe_paths"}

func checksOf(w []decoy.Warning) []*decoysitev1.DecoyCheck {
	out := make([]*decoysitev1.DecoyCheck, 0, len(requiredChecks)+len(w))
	for _, code := range requiredChecks {
		out = append(out, &decoysitev1.DecoyCheck{Code: code, Level: "required", Ok: true})
	}
	for _, x := range w {
		out = append(out, &decoysitev1.DecoyCheck{Code: x.Code, Level: "warning", Paths: x.Paths})
	}
	return out
}

func siteMsg(i store.DecoySiteInfo, panelSource string) *decoysitev1.DecoySite {
	return &decoysitev1.DecoySite{
		Id: i.ID, Name: i.Name, Note: i.Note, Source: i.Source, SourceLabel: i.SourceLabel, State: i.State, BundleSha256: i.BundleHash,
		CreatedUnix: i.CreatedAt.Unix(), CreatedBy: i.CreatedBy, UpdatedUnix: i.UpdatedAt.Unix(), UpdatedBy: i.UpdatedBy,
		Version: uint32(i.Version.Seq), SizeBytes: uint64(i.Version.Size), FileCount: uint32(i.Version.FileCount), Title: i.Version.Title,
		Checks: checksOf(i.Version.Warnings), NodeCount: uint32(i.NodeCount), OnPanel: panelSource == "site:"+i.ID,
	}
}

func versionMsg(v store.DecoyVersionRow, current int) *decoysitev1.DecoyVersion {
	return &decoysitev1.DecoyVersion{
		Seq: uint32(v.Seq), BundleSha256: v.BundleHash, CreatedUnix: v.CreatedAt.Unix(), CreatedBy: v.CreatedBy,
		SizeBytes: uint64(v.Size), FileCount: uint32(v.FileCount), Title: v.Title, Checks: checksOf(v.Warnings), Current: v.Seq == current,
	}
}

// panelSource is what the panel's public site is: the setting, else the --decoy-dir directory when there is one, else the built-in page.
func (f *Fleet) panelSource(ctx context.Context) (string, error) {
	v, err := f.st.PanelDecoySource(ctx)
	if err != nil {
		return "", err
	}
	if v == "" {
		v = "builtin"
		if f.cfg.DecoyDir != "" {
			v = "dir"
		}
	}
	return v, nil
}

func (s decoySiteService) siteMessage(ctx context.Context, id string) (*decoysitev1.DecoySite, error) {
	info, err := s.f.st.DecoySiteInfoOf(ctx, id)
	if err != nil {
		return nil, err
	}
	source, err := s.f.panelSource(ctx)
	if err != nil {
		return nil, err
	}
	return siteMsg(info, source), nil
}

// --- reads ---

// panelState is the panel's row: its public address and what its public listener serves now. The listener falls back (see
// httpserver.libraryDecoy) when the site chosen for it is gone or is a draft, and a chosen directory it does not have is the built-in page.
func (f *Fleet) panelState(ctx context.Context, chosen string) (*decoysitev1.DecoyPanelState, error) {
	out := &decoysitev1.DecoyPanelState{Serving: chosen}
	if u, err := url.Parse(f.cfg.PublicURL); err == nil {
		out.Address = u.Host
	}
	rest := "builtin"
	if f.cfg.DecoyDir != "" {
		rest = "dir"
	}
	if id, isSite := strings.CutPrefix(chosen, "site:"); isSite {
		info, err := f.st.DecoySiteInfoOf(ctx, id)
		switch {
		case errors.Is(err, store.ErrDecoySiteNotFound):
			out.Serving, out.ReasonCode = rest, "decoy_site_not_found"
		case err != nil:
			return nil, err
		case info.State != store.DecoyStateReady:
			out.Serving, out.ReasonCode = rest, "decoy_site_draft"
		}
	} else if chosen == "dir" && rest == "builtin" {
		out.Serving, out.ReasonCode = rest, "panel_unavailable"
	}
	return out, nil
}

func (s decoySiteService) ListDecoySites(ctx context.Context, _ *connect.Request[decoysitev1.ListDecoySitesRequest]) (*connect.Response[decoysitev1.ListDecoySitesResponse], error) {
	f := s.f
	infos, err := f.st.DecoySites(ctx)
	if err != nil {
		return nil, s.fail("list decoy sites", err)
	}
	counts, err := f.st.NodeDecoyModeCounts(ctx)
	if err != nil {
		return nil, s.fail("count decoy modes", err)
	}
	source, err := f.panelSource(ctx)
	if err != nil {
		return nil, s.fail("read the panel decoy source", err)
	}
	_, perr := f.panelDecoyBundle()
	panel, err := f.panelState(ctx, source)
	if err != nil {
		return nil, s.fail("read the panel decoy state", err)
	}
	out := &decoysitev1.ListDecoySitesResponse{
		Panel: panel, PanelSource: source, SiteLimit: store.DecoySiteLimit, VersionLimit: store.DecoyVersionLimit,
		Builtin: []*decoysitev1.DecoyBuiltinItem{
			{Id: "builtin", Available: true, NodeCount: uint32(counts[decoy.ModeBuiltin]), OnPanel: source == "builtin"},
			{Id: "panel_dir", Available: perr == nil, ReasonCode: panelReason(perr), NodeCount: uint32(counts[decoy.ModePanel]), OnPanel: source == "dir"},
		},
	}
	for _, i := range infos {
		out.Sites = append(out.Sites, siteMsg(i, source))
	}
	return connect.NewResponse(out), nil
}

func (s decoySiteService) GetDecoySite(ctx context.Context, req *connect.Request[decoysitev1.GetDecoySiteRequest]) (*connect.Response[decoysitev1.GetDecoySiteResponse], error) {
	f := s.f
	if err := checkSiteID(req.Msg.SiteId); err != nil {
		return nil, err
	}
	info, err := f.st.DecoySiteInfoOf(ctx, req.Msg.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	source, err := f.panelSource(ctx)
	if err != nil {
		return nil, s.fail("read the panel decoy source", err)
	}
	site := siteMsg(info, source)
	versions, err := f.st.DecoySiteVersions(ctx, req.Msg.SiteId)
	if err != nil {
		return nil, s.fail("read decoy versions", err)
	}
	out := &decoysitev1.GetDecoySiteResponse{Site: site}
	if out.Panel, err = f.panelState(ctx, source); err != nil {
		return nil, s.fail("read the panel decoy state", err)
	}
	for _, v := range versions {
		out.Versions = append(out.Versions, versionMsg(v, int(site.Version)))
		if v.Seq == int(site.Version) {
			for _, file := range v.Files {
				out.Files = append(out.Files, &decoysitev1.DecoyFileInfo{Path: file.Path, Size: uint64(file.Size)})
			}
		}
	}
	nodes, err := f.st.DecoySiteNodes(ctx, req.Msg.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site nodes", err)
	}
	if w := sharedDomains(nodes); w != nil {
		out.SharedDomains = &decoysitev1.DecoyCheck{Code: w.Code, Level: "warning", Paths: w.Paths}
	}
	msgs, err := f.nodeMessages(ctx, nodes)
	if err != nil {
		return nil, s.fail("read decoy site nodes", err)
	}
	for _, n := range msgs {
		out.Nodes = append(out.Nodes, &decoysitev1.DecoySiteNode{
			NodeId: n.Id, NodeName: n.Name, ReasonCode: n.DecoyReasonCode, Supported: n.DecoySupported, AgentSha256: n.DecoyAgentSha256,
		})
	}
	return connect.NewResponse(out), nil
}

// The two entries of the library that are not sites are read by the calls that read a site's files (pages, preview, file) under
// these ids: the built-in page, and a snapshot of the panel's --decoy-dir under the limits of its copy to nodes.
const (
	builtinEntryID  = "builtin"
	panelDirEntryID = "panel_dir"
)

// viewBundleOf is bundleOf for the three calls that only look at files: they also take the built-in entries (seq is ignored for those).
func (s decoySiteService) viewBundleOf(ctx context.Context, id string, seq uint32) (*decoy.Bundle, error) {
	switch id {
	case builtinEntryID:
		return decoy.BuiltinBundle(), nil
	case panelDirEntryID:
		b, err := s.f.panelDecoyBundle()
		if err != nil {
			return nil, panelUnavailable(err)
		}
		return b, nil
	}
	b, _, err := s.bundleOf(ctx, id, seq)
	return b, err
}

// bundleOf reads the files of a version of a site (seq 0 = the current one).
func (s decoySiteService) bundleOf(ctx context.Context, id string, seq uint32) (*decoy.Bundle, store.DecoyVersionRow, error) {
	if err := checkSiteID(id); err != nil {
		return nil, store.DecoyVersionRow{}, err
	}
	b, v, err := s.f.st.DecoySiteBundle(ctx, id, int(seq))
	if err != nil {
		return nil, v, s.fail("read decoy bundle", err)
	}
	return b, v, nil
}

func (s decoySiteService) ListDecoyPages(ctx context.Context, req *connect.Request[decoysitev1.ListDecoyPagesRequest]) (*connect.Response[decoysitev1.ListDecoyPagesResponse], error) {
	b, err := s.viewBundleOf(ctx, req.Msg.SiteId, req.Msg.Seq)
	if err != nil {
		return nil, err
	}
	out := &decoysitev1.ListDecoyPagesResponse{Pages: b.Pages()}
	for _, f := range b.Infos() {
		out.Files = append(out.Files, &decoysitev1.DecoyFileInfo{Path: f.Path, Size: uint64(f.Size)})
		out.Has_404 = out.Has_404 || f.Path == "404.html"
	}
	return connect.NewResponse(out), nil
}

func (s decoySiteService) GetDecoyPagePreview(ctx context.Context, req *connect.Request[decoysitev1.GetDecoyPagePreviewRequest]) (*connect.Response[decoysitev1.GetDecoyPagePreviewResponse], error) {
	b, err := s.viewBundleOf(ctx, req.Msg.SiteId, req.Msg.Seq)
	if err != nil {
		return nil, err
	}
	page := strings.TrimPrefix(req.Msg.Path, "/")
	if req.Msg.NotFound {
		page = "404.html"
		if _, ok := b.File(page); !ok {
			return connect.NewResponse(&decoysitev1.GetDecoyPagePreviewResponse{Html: decoy.NotFoundHTML(), Path: page}), nil
		}
	}
	doc, err := decoy.PreviewDocument(b, page)
	if err != nil {
		return nil, s.fail("preview decoy page", err)
	}
	if page == "" {
		page = "index.html"
	}
	return connect.NewResponse(&decoysitev1.GetDecoyPagePreviewResponse{Html: doc, Path: page}), nil
}

func (s decoySiteService) GetDecoyFile(ctx context.Context, req *connect.Request[decoysitev1.GetDecoyFileRequest]) (*connect.Response[decoysitev1.GetDecoyFileResponse], error) {
	b, err := s.viewBundleOf(ctx, req.Msg.SiteId, req.Msg.Seq)
	if err != nil {
		return nil, err
	}
	text, size, cut, err := decoy.TextFile(b, req.Msg.Path)
	if errors.Is(err, decoy.ErrPageNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_file_not_found"))
	}
	if err != nil {
		return nil, s.fail("read decoy file", err)
	}
	return connect.NewResponse(&decoysitev1.GetDecoyFileResponse{Text: text, Size: uint64(size), Truncated: cut}), nil
}

var fileNameUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

func (s decoySiteService) DownloadDecoySite(ctx context.Context, req *connect.Request[decoysitev1.DownloadDecoySiteRequest]) (*connect.Response[decoysitev1.DownloadDecoySiteResponse], error) {
	b, v, err := s.bundleOf(ctx, req.Msg.SiteId, req.Msg.Seq)
	if err != nil {
		return nil, err
	}
	site, err := s.f.st.DecoySite(ctx, req.Msg.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	slug := strings.Trim(fileNameUnsafe.ReplaceAllString(strings.ToLower(site.Name), "-"), "-")
	if slug == "" {
		slug = "site"
	}
	return connect.NewResponse(&decoysitev1.DownloadDecoySiteResponse{Filename: slug + "-v" + strconv.Itoa(v.Seq) + ".zip", Archive: b.Archive()}), nil
}

// --- changes ---

// actor is who the audit log and the site's "by" fields name: the administrator, or the name of the API token (or "Assistant of
// ...") that made the call.
func (f *Fleet) actor(ctx context.Context) string {
	if p := auth.PrincipalFrom(ctx); p.Token && p.Name != "" {
		return p.Name
	}
	return f.cfg.Actor(ctx)
}

// draftOnly is true for a call of an API token or the assistant that no approved plan covers: it may save drafts and nothing
// that changes what a node or the panel serves (the token policy lets it reach CreateDecoySite and AddDecoySiteVersion for that).
func draftOnly(ctx context.Context) bool {
	return auth.PrincipalFrom(ctx).Token && auth.ApprovedPlanTool(ctx) == ""
}

func (s decoySiteService) CreateDecoySite(ctx context.Context, req *connect.Request[decoysitev1.CreateDecoySiteRequest]) (*connect.Response[decoysitev1.CreateDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	bundle, err := decoy.ParseArchive(m.BundleArchive)
	if err != nil {
		return nil, invalid(decoy.ClientMessage(err))
	}
	source, state := m.Source, m.State
	switch source {
	case "":
		source = store.DecoySourceUpload
	case store.DecoySourceUpload, store.DecoySourceAssistant, store.DecoySourceMCP:
	default:
		return nil, invalid("source must be upload, assistant or mcp")
	}
	switch state {
	case "":
		state = store.DecoyStateReady
		if source != store.DecoySourceUpload {
			state = store.DecoyStateDraft
		}
	case store.DecoyStateReady, store.DecoyStateDraft:
	default:
		return nil, invalid("state must be draft or ready")
	}
	if state != store.DecoyStateDraft && draftOnly(ctx) {
		return nil, refuse("decoy_site_not_draft")
	}
	row, err := f.st.CreateDecoySite(ctx, f.now(), store.NewDecoySite{Name: m.Name, Note: m.Note, Source: source, State: state, By: f.actor(ctx), Bundle: bundle})
	if err != nil {
		return nil, s.fail("create decoy site", err)
	}
	f.audit(ctx, "decoy_site.create", map[string]string{"site_id": row.ID, "name": row.Name, "source": row.Source, "state": row.State, "bundle_sha256": row.BundleHash})
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.CreateDecoySiteResponse{Site: site}), nil
}

// newVersion saves a bundle as the next version of a site, sends it to the nodes that serve it and audits it.
func (s decoySiteService) newVersion(ctx context.Context, id string, b *decoy.Bundle) (*decoysitev1.DecoySite, []string, error) {
	f := s.f
	row, nodeIDs, changed, err := f.st.AddDecoySiteVersion(ctx, f.now(), id, b, f.actor(ctx))
	if err != nil {
		return nil, nil, s.fail("add decoy version", err)
	}
	if changed {
		f.audit(ctx, "decoy_site.version", map[string]string{"site_id": row.ID, "name": row.Name, "bundle_sha256": row.BundleHash, "nodes": strings.Join(nodeIDs, ",")})
		for _, nid := range nodeIDs {
			f.scheduleDecoySync(nid)
		}
	}
	site, err := s.siteMessage(ctx, id)
	if err != nil {
		return nil, nil, s.fail("read decoy site", err)
	}
	return site, nodeIDs, nil
}

func (s decoySiteService) AddDecoySiteVersion(ctx context.Context, req *connect.Request[decoysitev1.AddDecoySiteVersionRequest]) (*connect.Response[decoysitev1.AddDecoySiteVersionResponse], error) {
	if err := checkSiteID(req.Msg.SiteId); err != nil {
		return nil, err
	}
	b, err := decoy.ParseArchive(req.Msg.BundleArchive)
	if err != nil {
		return nil, invalid(decoy.ClientMessage(err))
	}
	if draftOnly(ctx) {
		if row, err := s.f.st.DecoySite(ctx, req.Msg.SiteId); err != nil {
			return nil, s.fail("read decoy site", err)
		} else if row.State != store.DecoyStateDraft {
			return nil, refuse("decoy_site_not_draft")
		}
	}
	site, nodes, err := s.newVersion(ctx, req.Msg.SiteId, b)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&decoysitev1.AddDecoySiteVersionResponse{Site: site, UpdatedNodeIds: nodes}), nil
}

func (s decoySiteService) EditDecoySiteFile(ctx context.Context, req *connect.Request[decoysitev1.EditDecoySiteFileRequest]) (*connect.Response[decoysitev1.EditDecoySiteFileResponse], error) {
	m := req.Msg
	b, _, err := s.bundleOf(ctx, m.SiteId, 0)
	if err != nil {
		return nil, err
	}
	row, err := s.f.st.DecoySite(ctx, m.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	if row.State != store.DecoyStateDraft {
		return nil, refuse("decoy_site_not_draft")
	}
	var next *decoy.Bundle
	if m.Delete {
		var found bool
		if next, found, err = b.Without(m.Path); !found && err == nil {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_file_not_found"))
		}
	} else {
		next, err = b.With(m.Path, m.Content)
	}
	if err != nil {
		return nil, invalid(decoy.ClientMessage(err))
	}
	site, _, err := s.newVersion(ctx, m.SiteId, next)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&decoysitev1.EditDecoySiteFileResponse{Site: site}), nil
}

func (s decoySiteService) UpdateDecoySite(ctx context.Context, req *connect.Request[decoysitev1.UpdateDecoySiteRequest]) (*connect.Response[decoysitev1.UpdateDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	if err := checkSiteID(m.SiteId); err != nil {
		return nil, err
	}
	row, err := f.st.UpdateDecoySite(ctx, f.now(), m.SiteId, m.Name, m.Note, f.actor(ctx))
	if err != nil {
		return nil, s.fail("update decoy site", err)
	}
	f.audit(ctx, "decoy_site.update", map[string]string{"site_id": row.ID, "name": row.Name})
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.UpdateDecoySiteResponse{Site: site}), nil
}

func (s decoySiteService) CopyDecoySite(ctx context.Context, req *connect.Request[decoysitev1.CopyDecoySiteRequest]) (*connect.Response[decoysitev1.CopyDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	b, _, err := s.bundleOf(ctx, m.SiteId, 0)
	if err != nil {
		return nil, err
	}
	from, err := f.st.DecoySite(ctx, m.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = from.Name + " copy"
		if utf8.RuneCountInString(name) > store.MaxDecoyNameRunes {
			name = string([]rune(from.Name)[:store.MaxDecoyNameRunes-5]) + " copy"
		}
	}
	row, err := f.st.CreateDecoySite(ctx, f.now(), store.NewDecoySite{Name: name, Note: from.Note, Source: from.Source, State: store.DecoyStateDraft, By: f.actor(ctx), Label: templateLabelOf(from.SourceLabel), Bundle: b})
	if err != nil {
		return nil, s.fail("copy decoy site", err)
	}
	f.audit(ctx, "decoy_site.copy", map[string]string{"site_id": row.ID, "name": row.Name, "from": from.ID, "bundle_sha256": row.BundleHash})
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.CopyDecoySiteResponse{Site: site}), nil
}

func (s decoySiteService) SetDecoySiteReady(ctx context.Context, req *connect.Request[decoysitev1.SetDecoySiteReadyRequest]) (*connect.Response[decoysitev1.SetDecoySiteReadyResponse], error) {
	f, m := s.f, req.Msg
	if err := checkSiteID(m.SiteId); err != nil {
		return nil, err
	}
	if m.Ready {
		// a site is ready only when its current version still passes the required checks: the files are there and are a valid bundle
		if _, _, err := f.st.DecoySiteBundle(ctx, m.SiteId, 0); err != nil {
			if coded := decoyLibraryError(err); coded != nil {
				return nil, coded
			}
			return nil, refuse("decoy_checks_failed")
		}
	}
	row, err := f.st.SetDecoySiteReady(ctx, f.now(), m.SiteId, m.Ready, f.actor(ctx))
	if err != nil {
		return nil, s.fail("set decoy site state", err)
	}
	f.audit(ctx, "decoy_site.state", map[string]string{"site_id": row.ID, "name": row.Name, "state": row.State})
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.SetDecoySiteReadyResponse{Site: site}), nil
}

func (s decoySiteService) RevertDecoySite(ctx context.Context, req *connect.Request[decoysitev1.RevertDecoySiteRequest]) (*connect.Response[decoysitev1.RevertDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	if err := checkSiteID(m.SiteId); err != nil {
		return nil, err
	}
	row, nodeIDs, changed, err := f.st.RevertDecoySite(ctx, f.now(), m.SiteId, int(m.Seq), f.actor(ctx))
	if err != nil {
		return nil, s.fail("revert decoy site", err)
	}
	if changed {
		f.audit(ctx, "decoy_site.revert", map[string]string{"site_id": row.ID, "name": row.Name, "version": strconv.Itoa(int(m.Seq)), "bundle_sha256": row.BundleHash, "nodes": strings.Join(nodeIDs, ",")})
		for _, nid := range nodeIDs {
			f.scheduleDecoySync(nid)
		}
	}
	site, err := s.siteMessage(ctx, row.ID)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	return connect.NewResponse(&decoysitev1.RevertDecoySiteResponse{Site: site, UpdatedNodeIds: nodeIDs}), nil
}

func (s decoySiteService) DeleteDecoySite(ctx context.Context, req *connect.Request[decoysitev1.DeleteDecoySiteRequest]) (*connect.Response[decoysitev1.DeleteDecoySiteResponse], error) {
	f, id := s.f, req.Msg.SiteId
	if err := checkSiteID(id); err != nil {
		return nil, err
	}
	row, err := f.st.DecoySite(ctx, id)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	if err := f.st.DeleteDecoySite(ctx, id); err != nil {
		return nil, s.fail("delete decoy site", err)
	}
	f.audit(ctx, "decoy_site.delete", map[string]string{"site_id": id, "name": row.Name, "bundle_sha256": row.BundleHash})
	return connect.NewResponse(&decoysitev1.DeleteDecoySiteResponse{}), nil
}

// --- where a site stands ---

// targets reads the nodes of a request: each must exist, be live and be named once.
func (s decoySiteService) targets(ctx context.Context, ids []string) ([]store.NodeRow, error) {
	if len(ids) > 50 {
		return nil, invalid("at most 50 nodes at a time")
	}
	seen := map[string]bool{}
	out := make([]store.NodeRow, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, invalid("a node is named twice")
		}
		seen[id] = true
		n, err := s.f.st.Node(ctx, id)
		if err != nil {
			return nil, s.fail("read node", err)
		}
		if n.State == "retired" {
			return nil, errNodeRetired
		}
		out = append(out, n)
	}
	return out, nil
}

// linkKey is what ties a node to the others in a visitor's eyes: the registrable domain of its address (de1.example.com and
// nl1.example.com are one), the node itself for an address that is an IP or has no registrable domain.
func linkKey(n store.NodeRow) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n.Address), "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err != nil {
		if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
			return d
		}
	}
	return "node:" + n.ID
}

// sharedDomains is the warning shared_domains: the site would stand on nodes that no registrable domain ties together, so a
// visitor who sees it on two of them learns they belong together. Its paths are the names of those nodes.
func sharedDomains(nodes []store.NodeRow) *decoy.Warning {
	keys := map[string]bool{}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		keys[linkKey(n)] = true
		names = append(names, n.Name)
	}
	if len(keys) < 2 {
		return nil
	}
	sort.Strings(names)
	return &decoy.Warning{Code: decoy.WarnSharedDomains, Paths: names}
}

func (s decoySiteService) AssignDecoySite(ctx context.Context, req *connect.Request[decoysitev1.AssignDecoySiteRequest]) (*connect.Response[decoysitev1.AssignDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	if err := checkSiteID(m.SiteId); err != nil {
		return nil, err
	}
	if len(m.NodeIds) == 0 && !m.Panel {
		return nil, invalid("name at least one node, or the panel")
	}
	info, err := f.st.DecoySiteInfoOf(ctx, m.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	// a dry run only answers what would change, so it also tells what a draft would replace once it is marked ready
	if info.State != store.DecoyStateReady && !m.DryRun {
		return nil, s.fail("assign decoy site", store.ErrDecoySiteDraft)
	}
	nodes, err := s.targets(ctx, m.NodeIds)
	if err != nil {
		return nil, err
	}
	source, err := f.panelSource(ctx)
	if err != nil {
		return nil, s.fail("read the panel decoy source", err)
	}
	out := &decoysitev1.AssignDecoySiteResponse{}
	var changing []store.NodeRow
	for _, n := range nodes {
		if n.DecoyMode == decoy.ModeOwn && n.DecoySiteID == info.ID {
			continue
		}
		changing = append(changing, n)
		r := &decoysitev1.DecoyReplaced{Target: n.ID, Previous: n.DecoyMode}
		if n.DecoyMode == decoy.ModeOwn && n.DecoySiteID != "" {
			r.PreviousSiteId = n.DecoySiteID
			if prev, err := f.st.DecoySite(ctx, n.DecoySiteID); err == nil {
				r.PreviousSiteName = prev.Name
			}
		}
		out.Replaced = append(out.Replaced, r)
	}
	if m.Panel && source != "site:"+info.ID {
		r := &decoysitev1.DecoyReplaced{Target: "panel", Previous: "dir"}
		switch {
		case source == "builtin":
			r.Previous = "builtin"
		case strings.HasPrefix(source, "site:"):
			r.Previous, r.PreviousSiteId = "site", strings.TrimPrefix(source, "site:")
			if prev, err := f.st.DecoySite(ctx, r.PreviousSiteId); err == nil {
				r.PreviousSiteName = prev.Name
			}
		}
		out.Replaced = append(out.Replaced, r)
	}
	warnings := info.Version.Warnings
	standing, err := f.st.DecoySiteNodes(ctx, info.ID)
	if err != nil {
		return nil, s.fail("read decoy site nodes", err)
	}
	if w := sharedDomains(append(standing, changing...)); w != nil && len(changing) > 0 {
		warnings = append(append([]decoy.Warning(nil), warnings...), *w)
	}
	for _, w := range warnings {
		out.Warnings = append(out.Warnings, &decoysitev1.DecoyCheck{Code: w.Code, Level: "warning", Paths: w.Paths})
	}
	if m.DryRun {
		return connect.NewResponse(out), nil
	}
	if len(nodes) > 0 {
		rows, err := f.st.AssignDecoySite(ctx, info.ID, m.NodeIds)
		if err != nil {
			return nil, s.fail("assign decoy site", err)
		}
		for _, n := range changing {
			f.scheduleDecoySync(n.ID)
		}
		if out.Nodes, err = f.nodeMessages(ctx, rows); err != nil {
			return nil, s.fail("read nodes", err)
		}
	}
	if m.Panel {
		if err := f.st.SetPanelDecoySource(ctx, "site:"+info.ID); err != nil {
			return nil, s.fail("assign decoy site to the panel", err)
		}
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	f.audit(ctx, "decoy_site.assign", map[string]string{"site_id": info.ID, "name": info.Name, "nodes": strings.Join(ids, ","), "panel": strconv.FormatBool(m.Panel), "bundle_sha256": info.BundleHash})
	out.Applied = true
	return connect.NewResponse(out), nil
}

func (s decoySiteService) UnassignDecoySite(ctx context.Context, req *connect.Request[decoysitev1.UnassignDecoySiteRequest]) (*connect.Response[decoysitev1.UnassignDecoySiteResponse], error) {
	f, m := s.f, req.Msg
	if err := checkSiteID(m.SiteId); err != nil {
		return nil, err
	}
	if m.Fallback != decoy.ModeBuiltin && m.Fallback != decoy.ModePanel {
		return nil, invalid("fallback must be builtin or panel")
	}
	if len(m.NodeIds) == 0 && !m.Panel {
		return nil, invalid("name at least one node, or the panel")
	}
	site, err := f.st.DecoySite(ctx, m.SiteId)
	if err != nil {
		return nil, s.fail("read decoy site", err)
	}
	if m.Fallback == decoy.ModePanel {
		if _, err := f.panelDecoyBundle(); err != nil {
			return nil, panelUnavailable(err)
		}
	}
	source, err := f.panelSource(ctx)
	if err != nil {
		return nil, s.fail("read the panel decoy source", err)
	}
	if m.Panel && source != "site:"+site.ID {
		return nil, refuse("panel_not_assigned")
	}
	out := &decoysitev1.UnassignDecoySiteResponse{PanelSource: source}
	if len(m.NodeIds) > 0 {
		rows, err := f.st.UnassignDecoySite(ctx, site.ID, m.NodeIds, m.Fallback)
		if err != nil {
			return nil, s.fail("unassign decoy site", err)
		}
		for _, n := range rows {
			f.scheduleDecoySync(n.ID)
		}
		if out.Nodes, err = f.nodeMessages(ctx, rows); err != nil {
			return nil, s.fail("read nodes", err)
		}
	}
	if m.Panel {
		next := "builtin"
		if m.Fallback == decoy.ModePanel {
			next = "dir"
		}
		if err := f.st.SetPanelDecoySource(ctx, next); err != nil {
			return nil, s.fail("take the decoy site off the panel", err)
		}
		out.PanelSource = next
	}
	f.audit(ctx, "decoy_site.unassign", map[string]string{"site_id": site.ID, "name": site.Name, "nodes": strings.Join(m.NodeIds, ","), "panel": strconv.FormatBool(m.Panel), "fallback": m.Fallback})
	return connect.NewResponse(out), nil
}

func (s decoySiteService) SetPanelDecoySource(ctx context.Context, req *connect.Request[decoysitev1.SetPanelDecoySourceRequest]) (*connect.Response[decoysitev1.SetPanelDecoySourceResponse], error) {
	f, source := s.f, req.Msg.Source
	if source != "builtin" && source != "dir" {
		return nil, invalid("source must be builtin or dir")
	}
	if source == "dir" {
		if _, err := f.panelDecoyBundle(); err != nil {
			return nil, panelUnavailable(err)
		}
	}
	if err := f.st.SetPanelDecoySource(ctx, source); err != nil {
		return nil, s.fail("set the panel decoy source", err)
	}
	f.audit(ctx, "decoy_site.panel_source", map[string]string{"source": source})
	return connect.NewResponse(&decoysitev1.SetPanelDecoySourceResponse{PanelSource: source}), nil
}

// ownSiteFromUpload is what an archive uploaded for one node does now (UploadNodeDecoy, SetNodeDecoyMode with a bundle, MCP
// node_decoy): the node serves it as a site of the library. A site of the same content is reused; the node's own upload site
// that nobody else serves gets a new version; otherwise a new site named after the node is made.
func (f *Fleet) ownSiteFromUpload(ctx context.Context, nodeID string, b *decoy.Bundle) (store.NodeRow, error) {
	if err := edition.Check(f.cfg.Remote != nil, edition.DecoyOwn); err != nil {
		return store.NodeRow{}, err
	}
	node, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return store.NodeRow{}, err
	}
	if node.State == "retired" {
		return store.NodeRow{}, store.ErrNodeRetired
	}
	infos, err := f.st.DecoySites(ctx)
	if err != nil {
		return store.NodeRow{}, err
	}
	siteID := ""
	for _, i := range infos {
		if i.BundleHash == b.Hash() && i.State == store.DecoyStateReady {
			siteID = i.ID
			break
		}
	}
	if siteID == "" && node.DecoyMode == decoy.ModeOwn && node.DecoySiteID != "" {
		for _, i := range infos {
			if i.ID == node.DecoySiteID && i.Source == store.DecoySourceUpload && i.State == store.DecoyStateReady && i.NodeCount == 1 {
				if src, _ := f.panelSource(ctx); src != "site:"+i.ID {
					row, _, _, err := f.st.AddDecoySiteVersion(ctx, f.now(), i.ID, b, f.actor(ctx))
					if err != nil {
						return store.NodeRow{}, err
					}
					f.audit(ctx, "decoy_site.version", map[string]string{"site_id": row.ID, "name": row.Name, "bundle_sha256": row.BundleHash, "nodes": nodeID})
					siteID = i.ID
				}
			}
		}
	}
	if siteID == "" {
		for try := 1; siteID == ""; try++ {
			name := node.Name
			if try > 1 {
				name = strings.TrimSpace(string([]rune(node.Name)[:min(utf8.RuneCountInString(node.Name), store.MaxDecoyNameRunes-6)])) + " " + strconv.Itoa(try)
			}
			row, err := f.st.CreateDecoySite(ctx, f.now(), store.NewDecoySite{Name: name, Source: store.DecoySourceUpload, State: store.DecoyStateReady, By: f.actor(ctx), Bundle: b})
			switch {
			case err == nil:
				f.audit(ctx, "decoy_site.create", map[string]string{"site_id": row.ID, "name": row.Name, "source": row.Source, "state": row.State, "bundle_sha256": row.BundleHash})
				siteID = row.ID
			case errors.Is(err, store.ErrDecoyNameTaken) && try < 20:
			default:
				return store.NodeRow{}, err
			}
		}
	}
	rows, err := f.st.AssignDecoySite(ctx, siteID, []string{nodeID})
	if err != nil {
		return store.NodeRow{}, err
	}
	return rows[0], nil
}

// editionGate refuses every call in an edition that keeps no bundles (the build for the edge never mounts this service; a VPS
// build that runs the edge's configuration, as the tests do, answers the same).
func (f *Fleet) editionGate() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if err := edition.Check(f.cfg.Remote != nil, edition.DecoyOwn); err != nil {
				return nil, err
			}
			return next(ctx, req)
		}
	})
}
