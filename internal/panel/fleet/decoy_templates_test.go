//go:build !js

package fleet

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/decoy/sitegen"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestListDecoyTemplatesNamesEveryKind(t *testing.T) {
	e := newEnv(t)
	got, err := (decoySiteService{e.f}).ListDecoyTemplates(e.ctx, connect.NewRequest(&decoysitev1.ListDecoyTemplatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	kinds := sitegen.Kinds()
	if len(kinds) == 0 || len(got.Msg.Templates) != len(kinds) {
		t.Fatalf("templates = %d, kinds = %d", len(got.Msg.Templates), len(kinds))
	}
	for i, k := range kinds {
		if g := got.Msg.Templates[i]; g.Id != k.ID || g.NameEn == "" || g.NameRu == "" || len(g.Countries) == 0 {
			t.Errorf("template %d = %+v, want %s with both names and its countries", i, g, k.ID)
		}
	}
	countries := got.Msg.Countries
	if len(countries) < 6 || countries[0] != sitegen.DefaultCountry {
		t.Fatalf("countries = %v, want the default first", countries)
	}
	known := map[string]bool{}
	for _, c := range countries {
		known[c] = true
	}
	for _, g := range got.Msg.Templates {
		for _, c := range g.Countries {
			if !known[c] {
				t.Errorf("%s names the country %q, which the list does not", g.Id, c)
			}
		}
	}
}

// The country the site is made for is part of what a preview is: another country is another site, in its language, and a kind with
// no texts there is refused with a code of its own.
func TestPreviewAndGenerateTakeACountry(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	preview := func(kind, country, seed string) (*decoysitev1.PreviewDecoyTemplateResponse, error) {
		got, err := svc.PreviewDecoyTemplate(e.ctx, connect.NewRequest(&decoysitev1.PreviewDecoyTemplateRequest{Kind: kind, Seed: seed, Country: country}))
		if err != nil {
			return nil, err
		}
		return got.Msg, nil
	}
	de, err := preview("blog", "de", "")
	if err != nil {
		t.Fatal(err)
	}
	us, err := preview("blog", "us", de.Seed)
	if err != nil || us.Html == de.Html {
		t.Fatalf("the same seed made the same page for two countries (%v)", err)
	}
	if def, err := preview("blog", "", de.Seed); err != nil || def.Html != us.Html {
		t.Fatalf("an empty country is not the default country (%v)", err)
	}
	if again, err := preview("blog", "de", de.Seed); err != nil || again.Html != de.Html {
		t.Fatalf("the German preview came back different (%v)", err)
	}
	for _, c := range []struct{ kind, country string }{{"blog", "zz"}, {"blog", "nl"}, {"business", "se"}} {
		if _, err := preview(c.kind, c.country, ""); refusalOf(err) != "invalid_argument decoy_template_country" {
			t.Errorf("%s for %s = %v, want decoy_template_country", c.kind, c.country, err)
		}
	}
	if _, err := preview("files", "nl", ""); err != nil {
		t.Fatalf("the Dutch file transfer page: %v", err)
	}
	got, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "blog", Seed: de.Seed, Country: "de", Note: "Template · Blog · Germany"}))
	if err != nil {
		t.Fatal(err)
	}
	files, _, _ := sitegen.GenerateWith("blog", mustHex(t, de.Seed), sitegen.Options{Country: "de"})
	want, err := decoy.FromFiles(files)
	if err != nil || got.Msg.Site.BundleSha256 != want.Hash() || got.Msg.Site.Note != "Template · Blog · Germany" {
		t.Fatalf("the saved site is not the German one that was shown (%v): %+v", err, got.Msg.Site)
	}
	if _, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "blog", Country: "nl"})); refusalOf(err) != "invalid_argument decoy_template_country" {
		t.Errorf("a blog for the Netherlands = %v", err)
	}
}

// A preview saves nothing; the seed it hands back makes exactly the site it showed, and an empty seed makes a different one.
func TestPreviewThenGenerateGivesTheSameSiteAndSavesNoDrafts(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	preview := func(seed string) *decoysitev1.PreviewDecoyTemplateResponse {
		t.Helper()
		got, err := svc.PreviewDecoyTemplate(e.ctx, connect.NewRequest(&decoysitev1.PreviewDecoyTemplateRequest{Kind: "blog", Seed: seed}))
		if err != nil {
			t.Fatal(err)
		}
		return got.Msg
	}
	a, b := preview(""), preview("")
	if a.Seed == "" || a.Seed == b.Seed || a.Html == "" || a.Name == "" {
		t.Fatalf("two previews = %q/%q seeds, name %q", a.Seed, b.Seed, a.Name)
	}
	if again := preview(a.Seed); again.Html != a.Html || again.Name != a.Name || again.Seed != a.Seed {
		t.Fatal("the same seed gave another preview")
	}
	if list, _ := svc.ListDecoySites(e.ctx, connect.NewRequest(&decoysitev1.ListDecoySitesRequest{})); len(list.Msg.Sites) != 0 {
		t.Fatalf("previews left %d sites in the library", len(list.Msg.Sites))
	}

	got, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "blog", Seed: a.Seed}))
	if err != nil {
		t.Fatal(err)
	}
	site := got.Msg.Site
	if site.Name != a.Name || site.Note != "Personal blog" || site.State != "ready" || site.Source != "upload" || site.SourceLabel != "template:blog" || site.FileCount < 5 {
		t.Fatalf("site = %+v", site)
	}
	files, _, _ := sitegen.GenerateNamed("blog", mustHex(t, a.Seed))
	want, err := decoy.FromFiles(files)
	if err != nil || site.BundleSha256 != want.Hash() {
		t.Fatalf("saved hash = %s, want the previewed site's %s (%v)", site.BundleSha256, want.Hash(), err)
	}
	// the preview of the saved site is the one that was shown
	saved, err := svc.GetDecoyPagePreview(e.ctx, connect.NewRequest(&decoysitev1.GetDecoyPagePreviewRequest{SiteId: site.Id}))
	if err != nil || saved.Msg.Html != a.Html {
		t.Fatalf("saved page differs from the preview (%v)", err)
	}

	rows, err := e.st.ListAudit(e.ctx, "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		var p map[string]string
		if row.Action == "decoy_site.create" && json.Unmarshal([]byte(row.Params), &p) == nil && p["template"] == "blog" && p["site_id"] == site.Id && p["bundle_sha256"] == site.BundleSha256 {
			found = true
		}
	}
	if !found {
		t.Fatal("no decoy_site.create audit event naming the template")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Every press of "Create" without a seed is another site, and a name another site already has is numbered, not refused.
func TestGenerateTwiceMakesTwoSitesWithDistinctNames(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	gen := func(req *decoysitev1.GenerateDecoySiteRequest) (*decoysitev1.DecoySite, error) {
		got, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return got.Msg.Site, nil
	}
	a, err := gen(&decoysitev1.GenerateDecoySiteRequest{Kind: "status"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := gen(&decoysitev1.GenerateDecoySiteRequest{Kind: "status"})
	if err != nil || a.BundleSha256 == b.BundleSha256 || a.Name == b.Name {
		t.Fatalf("two sites = %+v / %+v (%v)", a, b, err)
	}
	// the same seed twice: the same brand, so the second is numbered
	p, _ := svc.PreviewDecoyTemplate(e.ctx, connect.NewRequest(&decoysitev1.PreviewDecoyTemplateRequest{Kind: "card"}))
	one, err1 := gen(&decoysitev1.GenerateDecoySiteRequest{Kind: "card", Seed: p.Msg.Seed})
	two, err2 := gen(&decoysitev1.GenerateDecoySiteRequest{Kind: "card", Seed: p.Msg.Seed})
	if err1 != nil || err2 != nil || two.Name != one.Name+" 2" {
		t.Fatalf("same seed twice = %q, %q (%v, %v)", one.Name, two.Name, err1, err2)
	}
	// a name the owner typed is used as it is, and refused when taken; the note can be typed too
	if c, err := gen(&decoysitev1.GenerateDecoySiteRequest{Kind: "club", Name: "Our page", Note: "for the choir"}); err != nil || c.Name != "Our page" || c.Note != "for the choir" {
		t.Fatalf("named = %+v, %v", c, err)
	}
	if _, err := gen(&decoysitev1.GenerateDecoySiteRequest{Kind: "club", Name: "our PAGE"}); refusalOf(err) != "already_exists decoy_name_taken" {
		t.Fatalf("taken name = %v", err)
	}
}

func TestGenerateRefusesWhatItCannotMake(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	for _, req := range []*decoysitev1.GenerateDecoySiteRequest{
		{Kind: "nope"},
		{Kind: ""},
		{Kind: "blog", Seed: "zz"},
		{Kind: "blog", Seed: "00"},
		{Kind: "blog", Seed: strings.Repeat("a", 66)},
	} {
		_, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(req))
		want := "invalid_argument decoy_template_unknown"
		if req.Kind == "blog" {
			want = "invalid_argument decoy_template_seed"
		}
		if refusalOf(err) != want {
			t.Errorf("%+v = %v, want %s", req, err, want)
		}
	}
	if _, err := svc.PreviewDecoyTemplate(e.ctx, connect.NewRequest(&decoysitev1.PreviewDecoyTemplateRequest{Kind: "nope"})); refusalOf(err) != "invalid_argument decoy_template_unknown" {
		t.Errorf("preview of an unknown kind = %v", err)
	}
	if list, _ := svc.ListDecoySites(e.ctx, connect.NewRequest(&decoysitev1.ListDecoySitesRequest{})); len(list.Msg.Sites) != 0 {
		t.Fatal("a refusal left a site")
	}
}

// The library limit holds for generated sites too, and a copy of a generated site stays marked as one.
func TestGenerateKeepsTheLibraryLimitAndTheCopyKeepsTheLabel(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	for i := 0; i < store.DecoySiteLimit-1; i++ {
		e.createSite(fmt.Sprintf("filler %d", i), librarySite(t, fmt.Sprintf("filler site %d", i)), "draft")
	}
	last, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "docs"}))
	if err != nil {
		t.Fatalf("the hundredth site: %v", err)
	}
	if _, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "docs"})); refusalOf(err) != "failed_precondition decoy_library_full" {
		t.Fatalf("the 101st site = %v", err)
	}
	if list, _ := svc.ListDecoySites(e.ctx, connect.NewRequest(&decoysitev1.ListDecoySitesRequest{})); len(list.Msg.Sites) != store.DecoySiteLimit {
		t.Fatalf("library holds %d sites", len(list.Msg.Sites))
	}
	row, err := e.st.DecoySite(e.ctx, last.Msg.Site.Id)
	if err != nil || row.SourceLabel != "template:docs" || row.CreatedBy == "" {
		t.Fatalf("row = %+v, %v", row, err)
	}
}

func TestCopyOfAGeneratedSiteKeepsItsLabel(t *testing.T) {
	e := newEnv(t)
	svc := decoySiteService{e.f}
	g, err := svc.GenerateDecoySite(e.ctx, connect.NewRequest(&decoysitev1.GenerateDecoySiteRequest{Kind: "project"}))
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.CopyDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CopyDecoySiteRequest{SiteId: g.Msg.Site.Id}))
	if err != nil || c.Msg.Site.SourceLabel != "template:project" || c.Msg.Site.State != "draft" {
		t.Fatalf("copy = %+v, %v", c, err)
	}
	up := e.createSite("plain upload", librarySite(t, "plain"), "")
	cu, err := svc.CopyDecoySite(e.ctx, connect.NewRequest(&decoysitev1.CopyDecoySiteRequest{SiteId: up.Id}))
	if err != nil || strings.HasPrefix(cu.Msg.Site.SourceLabel, "template:") {
		t.Fatalf("copy of an upload = %+v, %v", cu, err)
	}
}
