//go:build !js

package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func librarySite(t *testing.T, body string, extra ...decoy.File) *decoy.Bundle {
	t.Helper()
	b, err := decoy.FromFiles(append([]decoy.File{
		{Path: "index.html", Content: []byte("<title>" + body + "</title><p>" + body)},
		{Path: "404.html", Content: []byte("<p>gone")},
	}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) createSite(name string, b *decoy.Bundle, state string) *decoysitev1.DecoySite {
	e.t.Helper()
	got, err := (decoySiteService{e.f}).CreateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CreateDecoySiteRequest{Name: name, BundleArchive: b.Archive(), State: state}))
	if err != nil {
		e.t.Fatalf("create %q: %v", name, err)
	}
	return got.Msg.Site
}

func refusalOf(err error) string {
	if err == nil {
		return ""
	}
	return connect.CodeOf(err).String() + " " + strings.TrimPrefix(err.Error(), connect.CodeOf(err).String()+": ")
}

// A site of the library goes to a node, a new version of it reaches the node at once, and the audit log names the site, the nodes
// and the hash but never a file's content.
func TestLibrarySiteAndItsNewVersionReachTheNode(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	a := e.enroll("nodea")
	c, _, _ := connectCaps(a, "inst-1", agentcap.Decoy)
	first := librarySite(t, "secret bakery text")
	// a node that reports no site is first told the built-in page
	if b := nextSetDecoy(c); b.Mode != decoy.ModeBuiltin {
		t.Fatalf("first push = %+v", b)
	} else {
		replyPortCommand(c, b.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"mode": b.Mode, "sha256": ""}})
	}
	site := e.createSite("bakery", first, "")
	if site.State != "ready" || site.Source != "upload" || site.Version != 1 || site.FileCount != 2 || site.Title != "secret bakery text" {
		t.Fatalf("created site = %+v", site)
	}
	if _, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: site.Id, NodeIds: []string{a.nodeID}})); err != nil {
		t.Fatal(err)
	}
	set := nextSetDecoy(c)
	if set.Mode != decoy.ModeOwn || set.BundleSha256 != first.Hash() || len(set.Files) != 2 {
		t.Fatalf("assignment push = %+v", set)
	}
	replyPortCommand(c, set.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"mode": set.Mode, "sha256": set.BundleSha256}})
	waitFor(t, func() bool {
		n, err := e.st.Node(e.ctx, a.nodeID)
		return err == nil && n.DecoyAgentHash == first.Hash() && n.DecoySiteID == site.Id
	})

	second := librarySite(t, "second shop text")
	added, err := svc.AddDecoySiteVersion(e.ctx, connect.NewRequest(&decoysitev1.AddDecoySiteVersionRequest{SiteId: site.Id, BundleArchive: second.Archive()}))
	if err != nil || added.Msg.Site.Version != 2 || len(added.Msg.UpdatedNodeIds) != 1 || added.Msg.UpdatedNodeIds[0] != a.nodeID {
		t.Fatalf("new version = %v, %v", added, err)
	}
	set = nextSetDecoy(c)
	if set.Mode != decoy.ModeOwn || set.BundleSha256 != second.Hash() || len(set.Files) != 2 {
		t.Fatalf("new version push = %+v", set)
	}
	replyPortCommand(c, set.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"mode": set.Mode, "sha256": set.BundleSha256}})
	n, err := e.st.Node(e.ctx, a.nodeID)
	if err != nil || n.DecoyBundleHash != second.Hash() {
		t.Fatalf("node hash = %q, %v", n.DecoyBundleHash, err)
	}
	// the node's card says which site it serves, and the library says how far the delivery is
	got, err := (nodeService{e.f}).GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
	if err != nil || got.Msg.Node.DecoySiteId != site.Id {
		t.Fatalf("node decoy_site_id = %q, %v", got.Msg.Node.DecoySiteId, err)
	}
	detail, err := svc.GetDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GetDecoySiteRequest{SiteId: site.Id}))
	if err != nil || len(detail.Msg.Nodes) != 1 || detail.Msg.Nodes[0].NodeId != a.nodeID || len(detail.Msg.Versions) != 2 || len(detail.Msg.Files) != 2 {
		t.Fatalf("site detail = %+v, %v", detail, err)
	}

	rows, err := e.st.ListAudit(e.ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]string{}
	for _, row := range rows {
		if strings.Contains(row.Params, "bakery text") || strings.Contains(row.Params, "shop text") || strings.Contains(row.Params, "<p>") {
			t.Errorf("a file's content reached the audit log: %s %s", row.Action, row.Params)
		}
		var params map[string]string
		if json.Unmarshal([]byte(row.Params), &params) == nil && strings.HasPrefix(row.Action, "decoy_site.") {
			seen[row.Action] = params
		}
	}
	if seen["decoy_site.create"]["bundle_sha256"] != first.Hash() || seen["decoy_site.create"]["name"] != "bakery" ||
		seen["decoy_site.assign"]["nodes"] != a.nodeID || seen["decoy_site.assign"]["site_id"] != site.Id ||
		seen["decoy_site.version"]["bundle_sha256"] != second.Hash() || seen["decoy_site.version"]["nodes"] != a.nodeID {
		t.Fatalf("decoy audit = %v", seen)
	}
}

func TestLibraryAssignmentRules(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	a, b := e.enroll("nodea"), e.enroll("nodeb")
	draft := e.createSite("draft", librarySite(t, "one"), "draft")
	if _, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID}})); refusalOf(err) != "failed_precondition decoy_site_draft" {
		t.Fatalf("assigning a draft = %v", err)
	}
	// a dry run on a draft only says what marking it ready and assigning it would replace
	if d, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID}, DryRun: true})); err != nil || d.Msg.Applied || len(d.Msg.Replaced) != 1 {
		t.Fatalf("dry run on a draft = %+v, %v", d, err)
	}
	if n, _ := e.st.Node(e.ctx, a.nodeID); n.DecoyMode == decoy.ModeOwn || n.DecoySiteID != "" {
		t.Fatal("a dry run on a draft changed a node")
	}
	if _, err := svc.SetDecoySiteReady(e.ctx, connect.NewRequest(&decoysitev1.SetDecoySiteReadyRequest{SiteId: draft.Id, Ready: true})); err != nil {
		t.Fatal(err)
	}
	// nothing is changed by a dry run, which tells what would be replaced. Two hosts of one domain (nodea.example.com, nodeb.example.com)
	// are tied together already: no warning; another domain makes one.
	dry, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID, b.nodeID}, Panel: true, DryRun: true}))
	if err != nil || dry.Msg.Applied || len(dry.Msg.Replaced) != 3 || dry.Msg.Replaced[0].Previous != decoy.ModeBuiltin || len(dry.Msg.Warnings) != 0 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET address = ? WHERE id = ?`, "nodeb.example.org", b.nodeID); err != nil {
		t.Fatal(err)
	}
	dry, err = svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID, b.nodeID}, Panel: true, DryRun: true}))
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, w := range dry.Msg.Warnings {
		codes = append(codes, w.Code)
	}
	if strings.Join(codes, ",") != decoy.WarnSharedDomains {
		t.Fatalf("warnings = %v, want only %s (the site has a 404 and nothing else)", codes, decoy.WarnSharedDomains)
	}
	if n, _ := e.st.Node(e.ctx, a.nodeID); n.DecoyMode == decoy.ModeOwn {
		t.Fatal("a dry run changed a node")
	}
	applied, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID, b.nodeID}, Panel: true}))
	if err != nil || !applied.Msg.Applied || len(applied.Msg.Nodes) != 2 {
		t.Fatalf("assign = %+v, %v", applied, err)
	}
	if src, _ := e.st.PanelDecoySource(e.ctx); src != "site:"+draft.Id {
		t.Fatalf("panel source = %q", src)
	}
	// a site that stands anywhere cannot be deleted or made a draft again, and the refusal lists where it stands
	_, err = svc.DeleteDecoySite(e.ctx, connect.NewRequest(&decoysitev1.DeleteDecoySiteRequest{SiteId: draft.Id}))
	if got := refusalOf(err); !strings.HasPrefix(got, "failed_precondition decoy_site_in_use: nodes=") || !strings.Contains(got, a.nodeID) || !strings.Contains(got, "panel=1") {
		t.Fatalf("delete of a used site = %q", got)
	}
	if _, err := svc.SetDecoySiteReady(e.ctx, connect.NewRequest(&decoysitev1.SetDecoySiteReadyRequest{SiteId: draft.Id})); !strings.Contains(refusalOf(err), "decoy_site_in_use") {
		t.Fatalf("back to a draft while used = %v", err)
	}
	// taking the nodes and the panel off frees it; a node goes to builtin or to the panel's directory
	dir := t.TempDir()
	writeSite(t, dir, "panel site")
	e.f.cfg.DecoyDir = dir
	un, err := svc.UnassignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.UnassignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID}, Panel: true, Fallback: "panel"}))
	if err != nil || un.Msg.PanelSource != "dir" || un.Msg.Nodes[0].DecoyMode != decoy.ModePanel {
		t.Fatalf("unassign = %+v, %v", un, err)
	}
	if _, err := svc.UnassignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.UnassignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{a.nodeID}, Fallback: "builtin"})); refusalOf(err) != "failed_precondition node_not_assigned" {
		t.Fatalf("taking a node off twice = %v", err)
	}
	if _, err := svc.UnassignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.UnassignDecoySiteRequest{SiteId: draft.Id, NodeIds: []string{b.nodeID}, Fallback: "builtin"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteDecoySite(e.ctx, connect.NewRequest(&decoysitev1.DeleteDecoySiteRequest{SiteId: draft.Id})); err != nil {
		t.Fatal(err)
	}
	if n, err := e.st.NodeDecoyBundle(e.ctx, librarySite(t, "one").Hash()); err == nil {
		t.Fatalf("the deleted site's bundle is still stored (%d bytes)", len(n))
	}
}

func TestLibraryListAndErrors(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	site := e.createSite("bakery", librarySite(t, "Bakery"), "")
	e.createSite("assistant sketch", librarySite(t, "Sketch"), "draft")
	if _, err := svc.CreateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CreateDecoySiteRequest{Name: "BAKERY", BundleArchive: librarySite(t, "x").Archive()})); refusalOf(err) != "already_exists decoy_name_taken" {
		t.Fatalf("taken name = %v", err)
	}
	for _, tc := range []struct{ name, want string }{{"", "invalid_argument decoy_name_invalid"}, {strings.Repeat("x", 61), "invalid_argument decoy_name_invalid"}} {
		if _, err := svc.CreateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CreateDecoySiteRequest{Name: tc.name, BundleArchive: librarySite(t, "y").Archive()})); refusalOf(err) != tc.want {
			t.Fatalf("name %q = %v", tc.name, err)
		}
	}
	if _, err := svc.CreateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CreateDecoySiteRequest{Name: "broken", BundleArchive: []byte("not an archive")})); refusalOf(err) != "invalid_argument decoy_bundle_bad_archive" {
		t.Fatalf("bad archive = %v", err)
	}
	if _, err := svc.GetDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GetDecoySiteRequest{SiteId: "dsi_nothing"})); refusalOf(err) != "not_found decoy_site_not_found" {
		t.Fatalf("unknown site = %v", err)
	}
	if _, err := svc.GetDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GetDecoySiteRequest{SiteId: "x' OR 1=1"})); refusalOf(err) != "not_found decoy_site_not_found" {
		t.Fatalf("malformed id = %v", err)
	}
	list, err := svc.ListDecoySites(e.ctx, connect.NewRequest(&decoysitev1.ListDecoySitesRequest{}))
	if err != nil || len(list.Msg.Sites) != 2 || list.Msg.SiteLimit != 100 || list.Msg.VersionLimit != 5 || list.Msg.PanelSource != "builtin" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if len(list.Msg.Builtin) != 2 || list.Msg.Builtin[0].Id != "builtin" || !list.Msg.Builtin[0].OnPanel || list.Msg.Builtin[1].Available || list.Msg.Builtin[1].ReasonCode != "panel_unavailable" {
		t.Fatalf("built-in entries = %+v", list.Msg.Builtin)
	}
	// a copy is a draft with a name of its own, and the panel's directory becomes available when there is one
	cp, err := svc.CopyDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CopyDecoySiteRequest{SiteId: site.Id}))
	if err != nil || cp.Msg.Site.State != "draft" || cp.Msg.Site.Name != "bakery copy" || cp.Msg.Site.BundleSha256 != site.BundleSha256 {
		t.Fatalf("copy = %+v, %v", cp, err)
	}
	if got := cp.Msg.Site.Title; got != "Bakery" {
		t.Fatalf("copy title = %q", got)
	}
	var dup bool
	for _, c := range cp.Msg.Site.Checks {
		dup = dup || c.Code == decoy.WarnDuplicateName
	}
	if !dup {
		t.Fatalf("a copy has the title of its original, which is a warning: %+v", cp.Msg.Site.Checks)
	}
}

// Files of a draft are replaced and deleted one by one as new versions; a ready site keeps its files.
func TestLibraryEditsADraftFileByFile(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	site := e.createSite("sketch", librarySite(t, "Sketch"), "draft")
	edit := func(path, content string, del bool) (*decoysitev1.EditDecoySiteFileResponse, error) {
		got, err := svc.EditDecoySiteFile(e.ctx, connect.NewRequest(&decoysitev1.EditDecoySiteFileRequest{SiteId: site.Id, Path: path, Content: []byte(content), Delete: del}))
		if err != nil {
			return nil, err
		}
		return got.Msg, nil
	}
	if got, err := edit("about.html", "<p>about", false); err != nil || got.Site.Version != 2 || got.Site.FileCount != 3 {
		t.Fatalf("add a file = %+v, %v", got, err)
	}
	if got, err := edit("about.html", "<p>about us", false); err != nil || got.Site.Version != 3 {
		t.Fatalf("replace a file = %+v, %v", got, err)
	}
	if got, err := edit("404.html", "", true); err != nil || got.Site.FileCount != 2 {
		t.Fatalf("delete a file = %+v, %v", got, err)
	}
	if _, err := edit("nothing.html", "", true); refusalOf(err) != "not_found decoy_file_not_found" {
		t.Fatalf("delete a missing file = %v", err)
	}
	if _, err := edit("index.html", "", true); refusalOf(err) != "invalid_argument decoy_bundle_no_index" {
		t.Fatalf("delete the index = %v", err)
	}
	if _, err := edit("../x.html", "x", false); refusalOf(err) != "invalid_argument decoy_bundle_bad_path: path=..%2Fx.html" {
		t.Fatalf("unsafe path = %v", err)
	}
	if _, err := svc.SetDecoySiteReady(e.ctx, connect.NewRequest(&decoysitev1.SetDecoySiteReadyRequest{SiteId: site.Id, Ready: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := edit("about.html", "<p>no", false); refusalOf(err) != "failed_precondition decoy_site_not_draft" {
		t.Fatalf("edit of a ready site = %v", err)
	}
	// the page, the text of a file and the zip of a version
	page, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: site.Id, Path: "about.html"}))
	if err != nil || !strings.Contains(page.Msg.Html, "about us") || page.Msg.Path != "about.html" {
		t.Fatalf("preview = %+v, %v", page, err)
	}
	nf, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: site.Id, NotFound: true}))
	if err != nil || !strings.Contains(nf.Msg.Html, "Not Found") {
		t.Fatalf("404 preview of a site without 404.html = %+v, %v", nf, err)
	}
	if _, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: site.Id, Path: "x.html"})); refusalOf(err) != "not_found decoy_page_not_found" {
		t.Fatalf("preview of a missing page = %v", err)
	}
	pages, err := svc.ListDecoyPages(e.ctx, connect.NewRequest(&decoysitev1.ListDecoyPagesRequest{SiteId: site.Id}))
	if err != nil || strings.Join(pages.Msg.Pages, ",") != "index.html,about.html" || pages.Msg.Has_404 || len(pages.Msg.Files) != 2 {
		t.Fatalf("pages = %+v, %v", pages, err)
	}
	file, err := svc.GetDecoyFile(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyFileRequest{SiteId: site.Id, Path: "about.html"}))
	if err != nil || file.Msg.Text != "<p>about us" {
		t.Fatalf("file = %+v, %v", file, err)
	}
	if _, err := svc.GetDecoyFile(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyFileRequest{SiteId: site.Id, Seq: 9, Path: "about.html"})); refusalOf(err) != "not_found decoy_version_not_found" {
		t.Fatalf("file of a missing version = %v", err)
	}
	zip, err := svc.DownloadDecoySite(e.ctx, connect.NewRequest(&decoysitev1.DownloadDecoySiteRequest{SiteId: site.Id}))
	if err != nil || zip.Msg.Filename != "sketch-v"+"4.zip" {
		t.Fatalf("download = %+v, %v", zip.Msg.Filename, err)
	}
	if b, err := decoy.ParseArchive(zip.Msg.Archive); err != nil || len(b.Infos()) != 2 {
		t.Fatalf("downloaded archive = %v, %v", b, err)
	}
	// back to the first version
	rev, err := svc.RevertDecoySite(e.ctx, connect.NewRequest(&decoysitev1.RevertDecoySiteRequest{SiteId: site.Id, Seq: 1}))
	if err != nil || rev.Msg.Site.Version != 5 || rev.Msg.Site.FileCount != 2 {
		t.Fatalf("revert = %+v, %v", rev, err)
	}
}

// The edge keeps no bundles: every call of the library refuses, the way an own upload does.
func TestLibraryRefusesOnTheEdge(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.Remote = &testRemote{}
	_, h := e.f.DecoySiteHandler()
	if h == nil {
		t.Fatal("no handler")
	}
	if _, err := e.f.editionGate().WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("the call went through")
		return nil, nil
	})(e.ctx, connect.NewRequest(&decoysitev1.ListDecoySitesRequest{})); err == nil || !strings.Contains(err.Error(), "unavailable_in_edition: feature=decoy_own") {
		t.Fatalf("edge call = %v", err)
	}
	// the built-in entries are no way round it: the gate stands before every procedure
	if _, err := e.f.editionGate().WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("the call went through")
		return nil, nil
	})(e.ctx, connect.NewRequest(&decoysitev1.ListDecoyPagesRequest{SiteId: "panel_dir"})); err == nil || !strings.Contains(err.Error(), "unavailable_in_edition: feature=decoy_own") {
		t.Fatalf("edge call for the panel's directory = %v", err)
	}
	if _, err := e.f.ownSiteFromUpload(e.ctx, "nod_x", librarySite(t, "x")); err == nil || !strings.Contains(err.Error(), "feature=decoy_own") {
		t.Fatalf("edge upload = %v", err)
	}
}

// shared_domains warns only when no registrable domain ties the nodes together: de1.example.com and nl1.example.com are one
// domain, another domain or an IP address is a node of its own (an IP is separate from every other node, another IP included).
func TestSharedDomainsKeyedByRegistrableDomain(t *testing.T) {
	node := func(id, address string) store.NodeRow { return store.NodeRow{ID: id, Name: id, Address: address} }
	for name, tc := range map[string]struct {
		nodes []store.NodeRow
		warn  bool
	}{
		"one node":                     {[]store.NodeRow{node("de1", "de1.example.com")}, false},
		"two hosts of one domain":      {[]store.NodeRow{node("de1", "de1.example.com"), node("nl1", "nl1.example.com")}, false},
		"deeper hosts of one domain":   {[]store.NodeRow{node("de1", "a.b.example.com"), node("nl1", "c.example.com")}, false},
		"the domain itself and a host": {[]store.NodeRow{node("de1", "example.com"), node("nl1", "nl1.example.com")}, false},
		"upper case and a final dot":   {[]store.NodeRow{node("de1", "DE1.Example.com."), node("nl1", "nl1.example.com")}, false},
		"a port after the host":        {[]store.NodeRow{node("de1", "de1.example.com:8443"), node("nl1", "nl1.example.com")}, false},
		"two domains":                  {[]store.NodeRow{node("de1", "de1.example.com"), node("nl1", "a.example.org")}, true},
		"a domain and an IP":           {[]store.NodeRow{node("de1", "de1.example.com"), node("nl1", "192.0.2.10")}, true},
		"two IPs":                      {[]store.NodeRow{node("de1", "192.0.2.10"), node("nl1", "192.0.2.11")}, true},
		"the same IP twice":            {[]store.NodeRow{node("de1", "192.0.2.10"), node("nl1", "192.0.2.10")}, true},
		"an IPv6 address and a host":   {[]store.NodeRow{node("de1", "2001:db8::1"), node("nl1", "nl1.example.com")}, true},
		"one IP alone":                 {[]store.NodeRow{node("de1", "192.0.2.10")}, false},
		"hosts under a public suffix":  {[]store.NodeRow{node("de1", "a.example.co.uk"), node("nl1", "b.example.co.uk")}, false},
		"two names under co.uk":        {[]store.NodeRow{node("de1", "a.example.co.uk"), node("nl1", "a.other.co.uk")}, true},
		"a name with no domain":        {[]store.NodeRow{node("de1", "localhost"), node("nl1", "nl1.example.com")}, true},
	} {
		got := sharedDomains(tc.nodes)
		if (got != nil) != tc.warn {
			t.Errorf("%s: warning = %v, want %v", name, got, tc.warn)
		}
		if got != nil && (got.Code != decoy.WarnSharedDomains || len(got.Paths) != len(tc.nodes)) {
			t.Errorf("%s: warning = %+v", name, got)
		}
	}
}
