//go:build !js

package fleet

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (e *env) useDir(dir string) {
	e.f.cfg.DecoyDir = dir
	e.f.panelDecoyAt = time.Time{} // the snapshot of the directory is kept for a few seconds
}

// The built-in page is read by the calls that read a site, and what they return is what the node and the panel serve.
func TestLibraryReadsTheBuiltinPage(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	pages, err := svc.ListDecoyPages(e.ctx, connect.NewRequest(&decoysitev1.ListDecoyPagesRequest{SiteId: "builtin"}))
	if err != nil || len(pages.Msg.Pages) != 1 || pages.Msg.Pages[0] != "index.html" || pages.Msg.Has_404 || len(pages.Msg.Files) != 1 || int(pages.Msg.Files[0].Size) != len(decoy.BuiltinHTML()) {
		t.Fatalf("pages = %+v, %v", pages, err)
	}
	pv, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: "builtin", Seq: 7}))
	if err != nil || pv.Msg.Path != "index.html" || !strings.Contains(pv.Msg.Html, "Coming soon") || strings.Contains(pv.Msg.Html, "<script") {
		t.Fatalf("preview = %+v, %v", pv, err)
	}
	// no 404.html: the built-in one is shown, as the node answers
	nf, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: "builtin", NotFound: true}))
	if err != nil || nf.Msg.Html != decoy.NotFoundHTML() {
		t.Fatalf("404 preview = %+v, %v", nf, err)
	}
	f, err := svc.GetDecoyFile(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyFileRequest{SiteId: "builtin", Path: "index.html"}))
	if err != nil || f.Msg.Text != decoy.BuiltinHTML() || f.Msg.Truncated {
		t.Fatalf("file = %+v, %v", f, err)
	}
	if _, err := svc.GetDecoyFile(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyFileRequest{SiteId: "builtin", Path: "missing.html"})); refusalOf(err) != "not_found decoy_file_not_found" {
		t.Fatalf("missing file = %v", err)
	}
	// only these three reads take the entries: a change refuses them as it refuses any id that is not a site's
	if _, err := svc.EditDecoySiteFile(e.ctx, connect.NewRequest(&decoysitev1.EditDecoySiteFileRequest{SiteId: "builtin", Path: "x.html", Content: []byte("x")})); refusalOf(err) != "not_found decoy_site_not_found" {
		t.Fatalf("edit of the built-in page = %v", err)
	}
	if _, err := svc.DownloadDecoySite(e.ctx, connect.NewRequest(&decoysitev1.DownloadDecoySiteRequest{SiteId: "panel_dir"})); refusalOf(err) != "not_found decoy_site_not_found" {
		t.Fatalf("download of the panel's directory = %v", err)
	}
}

// The panel's directory is read as it is copied to nodes: the same snapshot, the same limits, and the same codes when it cannot be.
func TestLibraryReadsThePanelDirectory(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	refusal := func() string {
		_, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: "panel_dir"}))
		return refusalOf(err)
	}
	if got := refusal(); got != "failed_precondition panel_unavailable" {
		t.Fatalf("no --decoy-dir = %q", got)
	}
	for _, tc := range []struct {
		name  string
		setup func(dir string)
		want  string
	}{
		{"no index", func(dir string) { _ = os.WriteFile(filepath.Join(dir, "about.html"), []byte("x"), 0o600) }, "failed_precondition panel_no_index"},
		{"over 2 MiB", func(dir string) {
			writeSite(t, dir, "x")
			_ = os.WriteFile(filepath.Join(dir, "big.bin"), make([]byte, decoy.MaxBundleBytes), 0o600)
		}, "failed_precondition panel_too_large"},
		{"too many files", func(dir string) {
			writeSite(t, dir, "x")
			for i := 0; i < 4096; i++ {
				_ = os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)+".txt"), nil, 0o600)
			}
		}, "failed_precondition panel_invalid"},
	} {
		dir := t.TempDir()
		tc.setup(dir)
		e.useDir(dir)
		if got := refusal(); got != tc.want {
			t.Errorf("%s: preview = %q, want %q", tc.name, got, tc.want)
		}
		if _, err := svc.ListDecoyPages(e.ctx, connect.NewRequest(&decoysitev1.ListDecoyPagesRequest{SiteId: "panel_dir"})); refusalOf(err) != tc.want {
			t.Errorf("%s: pages = %v", tc.name, err)
		}
		if _, err := svc.GetDecoyFile(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyFileRequest{SiteId: "panel_dir", Path: "index.html"})); refusalOf(err) != tc.want {
			t.Errorf("%s: file = %v", tc.name, err)
		}
	}
	dir := t.TempDir()
	writeSite(t, dir, "<title>Panel site</title><p>hello</p>")
	if err := os.WriteFile(filepath.Join(dir, "404.html"), []byte("<p>gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.useDir(dir)
	pages, err := svc.ListDecoyPages(e.ctx, connect.NewRequest(&decoysitev1.ListDecoyPagesRequest{SiteId: "panel_dir"}))
	if err != nil || len(pages.Msg.Pages) != 2 || pages.Msg.Pages[0] != "index.html" || !pages.Msg.Has_404 || len(pages.Msg.Files) != 2 {
		t.Fatalf("pages = %+v, %v", pages, err)
	}
	pv, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: "panel_dir"}))
	if err != nil || !strings.Contains(pv.Msg.Html, "Panel site") {
		t.Fatalf("preview = %+v, %v", pv, err)
	}
	f, err := svc.GetDecoyFile(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyFileRequest{SiteId: "panel_dir", Path: "404.html"}))
	if err != nil || f.Msg.Text != "<p>gone" {
		t.Fatalf("file = %+v, %v", f, err)
	}
}

// GetDecoySite carries the warning AssignDecoySite computes, for the nodes the site stands on now.
func TestGetDecoySiteSharedDomains(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	a, b := e.enroll("nodea"), e.enroll("nodeb")
	site := e.createSite("shared", librarySite(t, "one"), "")
	get := func() *decoysitev1.GetDecoySiteResponse {
		r, err := svc.GetDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GetDecoySiteRequest{SiteId: site.Id}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if _, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: site.Id, NodeIds: []string{a.nodeID}})); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.SharedDomains != nil {
		t.Fatalf("one node = %+v", got.SharedDomains)
	}
	if _, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: site.Id, NodeIds: []string{b.nodeID}})); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.SharedDomains != nil { // nodea.example.com and nodeb.example.com: one domain
		t.Fatalf("one domain = %+v", got.SharedDomains)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET address = ? WHERE id = ?`, "nodeb.example.org", b.nodeID); err != nil {
		t.Fatal(err)
	}
	w := get().SharedDomains
	if w == nil || w.Code != decoy.WarnSharedDomains || w.Level != "warning" || strings.Join(w.Paths, ",") != "nodea,nodeb" {
		t.Fatalf("two domains = %+v", w)
	}
	if _, err := svc.UnassignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.UnassignDecoySiteRequest{SiteId: site.Id, NodeIds: []string{b.nodeID}, Fallback: "builtin"})); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.SharedDomains != nil {
		t.Fatalf("one node left = %+v", got.SharedDomains)
	}
}

// The panel's row: its address, what it serves, and why that is not what was chosen. The listener never serves nothing.
func TestLibraryPanelState(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	state := func() *decoysitev1.DecoyPanelState {
		l, err := svc.ListDecoySites(e.ctx, connect.NewRequest(&decoysitev1.ListDecoySitesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return l.Msg.Panel
	}
	if p := state(); p == nil || p.Address != "" || p.Serving != "builtin" || p.ReasonCode != "" {
		t.Fatalf("a panel with no address and no directory = %+v", p)
	}
	e.f.cfg.PublicURL = "https://panel.example.com:8443/"
	if p := state(); p.Address != "panel.example.com:8443" {
		t.Fatalf("address = %q", p.Address)
	}
	// "dir" chosen on a panel that has none: the built-in page is served
	if err := e.st.SetPanelDecoySource(e.ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if p := state(); p.Serving != "builtin" || p.ReasonCode != "panel_unavailable" {
		t.Fatalf("dir without a directory = %+v", p)
	}
	e.useDir(t.TempDir())
	if p := state(); p.Serving != "dir" || p.ReasonCode != "" {
		t.Fatalf("dir = %+v", p)
	}
	site := e.createSite("front", librarySite(t, "front"), "")
	if _, err := svc.AssignDecoySite(e.ctx, connect.NewRequest(&decoysitev1.AssignDecoySiteRequest{SiteId: site.Id, Panel: true})); err != nil {
		t.Fatal(err)
	}
	if p := state(); p.Serving != "site:"+site.Id || p.ReasonCode != "" {
		t.Fatalf("site = %+v", p)
	}
	one, err := svc.GetDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GetDecoySiteRequest{SiteId: site.Id}))
	if err != nil || one.Msg.Panel == nil || one.Msg.Panel.Serving != "site:"+site.Id || one.Msg.Panel.Address != "panel.example.com:8443" {
		t.Fatalf("panel in GetDecoySite = %+v, %v", one, err)
	}
	// the chosen site is gone, or is a draft again: the directory is served, and the response says why
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE setting SET v = 'site:dsi_gone' WHERE k = ?`, store.PanelDecoySourceKey); err != nil {
		t.Fatal(err)
	}
	if p := state(); p.Serving != "dir" || p.ReasonCode != "decoy_site_not_found" {
		t.Fatalf("a gone site = %+v", p)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE decoy_site SET state = 'draft' WHERE id = ?`, site.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE setting SET v = ? WHERE k = ?`, "site:"+site.Id, store.PanelDecoySourceKey); err != nil {
		t.Fatal(err)
	}
	if p := state(); p.Serving != "dir" || p.ReasonCode != "decoy_site_draft" {
		t.Fatalf("a draft = %+v", p)
	}
	e.useDir("")
	if p := state(); p.Serving != "builtin" || p.ReasonCode != "decoy_site_draft" {
		t.Fatalf("a draft and no directory = %+v", p)
	}
}
