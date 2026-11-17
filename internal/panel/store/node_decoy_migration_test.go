//go:build !js

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/decoy"
)

func TestNodeDecoyMigrationKeepsExistingNodesBuiltin(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 58); err != nil {
		t.Fatalf("up to 58: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_existing', 'de1', '203.0.113.10', 'active', 1)`); err != nil {
		t.Fatal(err)
	}

	if _, err := p.UpTo(ctx, 59); err != nil {
		t.Fatalf("up to 59: %v", err)
	}
	// read the columns themselves: Node() follows the newest schema, and later migrations add node columns
	decoy := func() (mode, hash, agentMode, agentHash string, err error) {
		err = s.R.QueryRowContext(ctx, `SELECT decoy_mode, decoy_bundle_hash, decoy_agent_mode, decoy_agent_hash FROM node WHERE id = 'nod_existing'`).Scan(&mode, &hash, &agentMode, &agentHash)
		return
	}
	mode, hash, agentMode, agentHash, err := decoy()
	if err != nil {
		t.Fatal(err)
	}
	if mode != "builtin" || hash != "" || agentMode != "" || agentHash != "" {
		t.Fatalf("existing node decoy state = %q %q %q %q", mode, hash, agentMode, agentHash)
	}

	if _, err := p.DownTo(ctx, 58); err != nil {
		t.Fatalf("down to 58: %v", err)
	}
	if _, err := p.UpTo(ctx, 59); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if mode, _, _, _, err = decoy(); err != nil || mode != "builtin" {
		t.Fatalf("node after migration rerun = %q, %v", mode, err)
	}
}

func TestDecoyLibraryMigrationMakesSitesOfNodeBundles(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 142); err != nil {
		t.Fatal(err)
	}
	bundleA, bundleB, bundleC := siteBundle(t, "bakery"), siteBundle(t, "garage"), siteBundle(t, "florist")
	for _, b := range []*decoy.Bundle{bundleA, bundleB, bundleC} {
		execT(t, s, `INSERT INTO node_decoy_bundle (sha256, content) VALUES (?, ?)`, b.Hash(), b.Archive())
	}
	node := func(id, name, state, mode, hash string) {
		execT(t, s, `INSERT INTO node (id, name, address, state, created_at, decoy_mode, decoy_bundle_hash) VALUES (?, ?, '203.0.113.10', ?, 1, ?, ?)`, id, name, state, mode, hash)
	}
	node("nod_1", "de1", "active", "own", bundleA.Hash())
	node("nod_2", "nl1", "active", "own", bundleA.Hash())     // the same bundle: the same site
	node("nod_3", "fr1", "active", "builtin", bundleB.Hash()) // a leftover of an earlier own site
	node("nod_4", "se1", "active", "builtin", "")
	node("nod_5", "DE1", "retired", "own", bundleC.Hash()) // the name is taken
	if _, err := p.UpTo(ctx, 143); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	rows, err := s.R.QueryContext(ctx, `SELECT name, bundle_hash FROM decoy_site WHERE state = 'ready' AND source = 'upload'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, hash string
		if err := rows.Scan(&name, &hash); err != nil {
			t.Fatal(err)
		}
		names[name] = hash
	}
	rows.Close()
	want := map[string]string{"de1": bundleA.Hash(), "fr1": bundleB.Hash(), "DE1 (2)": bundleC.Hash()}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("sites = %v, want %v", names, want)
	}
	site := func(id string) string {
		var siteID string
		if err := s.R.QueryRowContext(ctx, `SELECT decoy_site_id FROM node WHERE id = ?`, id).Scan(&siteID); err != nil {
			t.Fatal(err)
		}
		return siteID
	}
	if site("nod_1") == "" || site("nod_1") != site("nod_2") {
		t.Fatalf("nodes with one bundle: %q and %q", site("nod_1"), site("nod_2"))
	}
	if site("nod_3") == "" || site("nod_3") == site("nod_1") || site("nod_4") != "" {
		t.Fatalf("leftover %q, none %q", site("nod_3"), site("nod_4"))
	}
	if got := countT(t, s, `SELECT count(*) FROM decoy_site_version WHERE seq = 1 AND checks_json = ''`); got != 3 {
		t.Fatalf("versions waiting for their meta = %d, want 3", got)
	}
	if err := s.BackfillDecoyVersions(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countT(t, s, `SELECT count(*) FROM decoy_site_version WHERE checks_json <> '' AND file_count = 1 AND title <> ''`); got != 3 {
		t.Fatalf("versions with their meta = %d, want 3", got)
	}
}
