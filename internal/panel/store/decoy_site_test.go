//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/decoy"
)

func siteBundle(t *testing.T, body string, extra ...decoy.File) *decoy.Bundle {
	t.Helper()
	b, err := decoy.FromFiles(append([]decoy.File{{Path: "index.html", Content: []byte("<title>" + body + "</title><p>" + body)}}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newSite(t *testing.T, s *Store, name string, b *decoy.Bundle, state string) DecoySiteRow {
	t.Helper()
	site, err := s.CreateDecoySite(context.Background(), time.Now(), NewDecoySite{Name: name, Source: DecoySourceUpload, State: state, By: "admin", Bundle: b})
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return site
}

func bundleRows(t *testing.T, s *Store) int {
	t.Helper()
	return countT(t, s, `SELECT count(*) FROM node_decoy_bundle`)
}

func TestDecoySiteKeepsFiveVersionsAndCollectsBundles(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	site := newSite(t, s, "bakery", siteBundle(t, "v1"), DecoyStateReady)
	for i := 2; i <= 7; i++ {
		var err error
		if site, _, _, err = s.AddDecoySiteVersion(ctx, time.Now(), site.ID, siteBundle(t, fmt.Sprintf("v%d", i)), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := s.DecoySiteVersions(ctx, site.ID)
	if err != nil || len(versions) != DecoyVersionLimit {
		t.Fatalf("versions = %d, %v; want %d", len(versions), err, DecoyVersionLimit)
	}
	if versions[0].Seq != 7 || versions[len(versions)-1].Seq != 3 || versions[0].BundleHash != site.BundleHash {
		t.Fatalf("kept versions %d..%d, current hash match %v", versions[len(versions)-1].Seq, versions[0].Seq, versions[0].BundleHash == site.BundleHash)
	}
	if got := bundleRows(t, s); got != DecoyVersionLimit {
		t.Fatalf("bundles = %d, want the %d of the kept versions", got, DecoyVersionLimit)
	}
	// the same content again is not a new version
	if _, _, changed, err := s.AddDecoySiteVersion(ctx, time.Now(), site.ID, siteBundle(t, "v7"), "admin"); err != nil || changed {
		t.Fatalf("re-adding the current version: changed=%v err=%v", changed, err)
	}
	if v := versions[0]; !v.Computed || v.Title != "v7" || v.FileCount != 1 || len(v.Files) != 1 || v.Files[0].Path != "index.html" {
		t.Fatalf("version meta = %+v", v)
	}
}

func TestDecoySiteBundleStaysWhileANodeHoldsIt(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	node, _ := fixtureInbound(t, s, "a")
	site := newSite(t, s, "bakery", siteBundle(t, "v1"), DecoyStateReady)
	if _, err := s.AssignDecoySite(ctx, site.ID, []string{node}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 3; i++ {
		var ids []string
		var err error
		if site, ids, _, err = s.AddDecoySiteVersion(ctx, time.Now(), site.ID, siteBundle(t, fmt.Sprintf("v%d", i)), "admin"); err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != node {
			t.Fatalf("nodes to update = %v", ids)
		}
	}
	n, err := s.Node(ctx, node)
	if err != nil || n.DecoyMode != decoy.ModeOwn || n.DecoySiteID != site.ID || n.DecoyBundleHash != site.BundleHash {
		t.Fatalf("node after new versions = %+v, %v", n, err)
	}
	if _, err := s.NodeDecoyBundle(ctx, n.DecoyBundleHash); err != nil {
		t.Fatal(err)
	}
	// taking the site off collects nothing that a version still holds, deleting the site collects every bundle
	if _, err := s.UnassignDecoySite(ctx, site.ID, []string{node}, decoy.ModeBuiltin); err != nil {
		t.Fatal(err)
	}
	if got := bundleRows(t, s); got != 3 {
		t.Fatalf("bundles after unassign = %d, want 3", got)
	}
	if err := s.DeleteDecoySite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if got := bundleRows(t, s); got != 0 {
		t.Fatalf("bundles after delete = %d, want 0", got)
	}
	if n, _ := s.Node(ctx, node); n.DecoyBundleHash != "" || n.DecoySiteID != "" || n.DecoyMode != decoy.ModeBuiltin {
		t.Fatalf("node after unassign = %+v", n)
	}
}

func TestDecoySiteNamesAndLimit(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	newSite(t, s, "Bakery", siteBundle(t, "one"), DecoyStateDraft)
	if _, err := s.CreateDecoySite(ctx, time.Now(), NewDecoySite{Name: " bakery ", Bundle: siteBundle(t, "two")}); !errors.Is(err, ErrDecoyNameTaken) {
		t.Fatalf("same name ignoring case = %v", err)
	}
	for _, name := range []string{"", "   ", "a\nb"} {
		if _, err := s.CreateDecoySite(ctx, time.Now(), NewDecoySite{Name: name, Bundle: siteBundle(t, "x")}); !errors.Is(err, ErrDecoyNameInvalid) {
			t.Fatalf("name %q = %v", name, err)
		}
	}
	long := strings.Repeat("x", MaxDecoyNameRunes+1)
	if _, err := s.CreateDecoySite(ctx, time.Now(), NewDecoySite{Name: long, Bundle: siteBundle(t, "x")}); !errors.Is(err, ErrDecoyNameInvalid) {
		t.Fatalf("61-character name = %v", err)
	}
	if _, err := s.CreateDecoySite(ctx, time.Now(), NewDecoySite{Name: "n", Note: strings.Repeat("x", MaxDecoyNoteRunes+1), Bundle: siteBundle(t, "x")}); !errors.Is(err, ErrDecoyNoteInvalid) {
		t.Fatalf("201-character note = %v", err)
	}
	for i := 1; i < DecoySiteLimit; i++ {
		newSite(t, s, fmt.Sprintf("site %d", i), siteBundle(t, fmt.Sprintf("body %d", i)), DecoyStateDraft)
	}
	if _, err := s.CreateDecoySite(ctx, time.Now(), NewDecoySite{Name: "one too many", Bundle: siteBundle(t, "last")}); !errors.Is(err, ErrDecoyLibraryFull) {
		t.Fatalf("101st site = %v", err)
	}
	if got := bundleRows(t, s); got != DecoySiteLimit {
		t.Fatalf("a refused site left a bundle behind: %d rows", got)
	}
	if _, err := s.UpdateDecoySite(ctx, time.Now(), mustSiteID(t, s, "site 1"), "BAKERY", "note", "admin"); !errors.Is(err, ErrDecoyNameTaken) {
		t.Fatalf("rename onto a taken name = %v", err)
	}
	renamed, err := s.UpdateDecoySite(ctx, time.Now(), mustSiteID(t, s, "site 1"), "Corner shop", "a note", "admin")
	if err != nil || renamed.Name != "Corner shop" || renamed.Note != "a note" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
}

func mustSiteID(t *testing.T, s *Store, name string) string {
	t.Helper()
	sites, err := s.DecoySites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range sites {
		if site.Name == name {
			return site.ID
		}
	}
	t.Fatalf("no site %q", name)
	return ""
}

func TestDecoySiteStatesAndUse(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeA, _ := fixtureInbound(t, s, "a")
	nodeB, _ := fixtureInbound(t, s, "b")
	draft := newSite(t, s, "draft", siteBundle(t, "d"), DecoyStateDraft)
	if _, err := s.AssignDecoySite(ctx, draft.ID, []string{nodeA}); !errors.Is(err, ErrDecoySiteDraft) {
		t.Fatalf("assigning a draft = %v", err)
	}
	if err := s.SetPanelDecoySource(ctx, "site:"+draft.ID); !errors.Is(err, ErrDecoySiteDraft) {
		t.Fatalf("a draft for the panel = %v", err)
	}
	site, err := s.SetDecoySiteReady(ctx, time.Now(), draft.ID, true, "admin")
	if err != nil || site.State != DecoyStateReady {
		t.Fatalf("mark ready = %+v, %v", site, err)
	}
	if _, err := s.AssignDecoySite(ctx, site.ID, []string{nodeA, nodeB}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPanelDecoySource(ctx, "site:"+site.ID); err != nil {
		t.Fatal(err)
	}
	var use *DecoySiteInUseError
	if err := s.DeleteDecoySite(ctx, site.ID); !errors.As(err, &use) || len(use.NodeIDs) != 2 || !use.Panel {
		t.Fatalf("delete of a used site = %v (%+v)", err, use)
	}
	if _, err := s.SetDecoySiteReady(ctx, time.Now(), site.ID, false, "admin"); !errors.As(err, &use) {
		t.Fatalf("back to a draft while used = %v", err)
	}
	infos, err := s.DecoySites(ctx)
	if err != nil || len(infos) != 1 || infos[0].NodeCount != 2 || infos[0].State != DecoyStateReady {
		t.Fatalf("list = %+v, %v", infos, err)
	}
	// taking one node off leaves the other, taking the panel off lets the last node's removal free the site
	if _, err := s.UnassignDecoySite(ctx, site.ID, []string{nodeA}, decoy.ModePanel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnassignDecoySite(ctx, site.ID, []string{nodeA}, decoy.ModePanel); !errors.Is(err, ErrDecoyNotAssigned) {
		t.Fatalf("taking a node off twice = %v", err)
	}
	if n, _ := s.Node(ctx, nodeA); n.DecoyMode != decoy.ModePanel || n.DecoySiteID != "" {
		t.Fatalf("node A = %+v", n)
	}
	if _, err := s.UnassignDecoySite(ctx, site.ID, []string{nodeB}, decoy.ModeBuiltin); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPanelDecoySource(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDecoySiteReady(ctx, time.Now(), site.ID, false, "admin"); err != nil {
		t.Fatalf("back to a draft once free = %v", err)
	}
	if err := s.DeleteDecoySite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDecoySite(ctx, site.ID); !errors.Is(err, ErrDecoySiteNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestDecoySiteRevertAndBackfill(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	site := newSite(t, s, "bakery", siteBundle(t, "first"), DecoyStateReady)
	first := site.BundleHash
	site, _, _, err := s.AddDecoySiteVersion(ctx, time.Now(), site.ID, siteBundle(t, "second"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	site, _, changed, err := s.RevertDecoySite(ctx, time.Now(), site.ID, 1, "admin")
	if err != nil || !changed || site.BundleHash != first {
		t.Fatalf("revert = %+v changed=%v err=%v", site, changed, err)
	}
	versions, _ := s.DecoySiteVersions(ctx, site.ID)
	if len(versions) != 3 || versions[0].Seq != 3 || versions[0].BundleHash != first || versions[0].Title != "first" {
		t.Fatalf("versions after revert = %+v", versions)
	}
	if _, _, _, err := s.RevertDecoySite(ctx, time.Now(), site.ID, 9, "admin"); !errors.Is(err, ErrDecoyVersionNotFound) {
		t.Fatalf("revert to a version that is not there = %v", err)
	}
	// a version whose meta was never worked out (as the migration leaves it) is filled in from its bundle
	execT(t, s, `UPDATE decoy_site_version SET checks_json = '', files_json = '', title = '', size_bytes = 0, file_count = 0 WHERE site_id = ?`, site.ID)
	if err := s.BackfillDecoyVersions(ctx); err != nil {
		t.Fatal(err)
	}
	b, v, err := s.DecoySiteBundle(ctx, site.ID, 0)
	if err != nil || !v.Computed || v.Title != "first" || v.FileCount != 1 || b.Hash() != first {
		t.Fatalf("backfilled = %+v, %v", v, err)
	}
}

func TestDecoySiteTitleDuplicateIsAWarning(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	newSite(t, s, "one", siteBundle(t, "Same shop"), DecoyStateReady)
	second := newSite(t, s, "two", siteBundle(t, "Same shop", decoy.File{Path: "404.html", Content: []byte("<p>no")}), DecoyStateReady)
	infos, err := s.DecoySites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID != second.ID {
			continue
		}
		var codes []string
		for _, w := range info.Version.Warnings {
			codes = append(codes, w.Code)
		}
		if len(codes) != 1 || codes[0] != decoy.WarnDuplicateName {
			t.Fatalf("warnings of the second site = %v", codes)
		}
	}
}
