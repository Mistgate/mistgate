//go:build !js

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/decoy"
)

// The library of decoy sites (migration 00143). The files stay in node_decoy_bundle, addressed by SHA-256; a site is a name, a
// state and up to DecoyVersionLimit versions. A node in mode "own" points at a site and keeps decoy_bundle_hash equal to the
// site's current hash. Only the VPS edition keeps bundles, so none of this is in the js build.

const (
	DecoySiteLimit    = 100
	DecoyVersionLimit = 5
	// PanelDecoySourceKey is the setting that says what the panel's own public site is: "builtin", "dir" or "site:<id>".
	PanelDecoySourceKey = "decoy_panel_source"

	DecoyStateDraft = "draft"
	DecoyStateReady = "ready"

	DecoySourceUpload    = "upload"
	DecoySourceAssistant = "assistant"
	DecoySourceMCP       = "mcp"
	// DecoyLabelTemplate starts the source_label of a site the panel generated from a built-in template ("template:<kind>").
	DecoyLabelTemplate = "template:"

	MaxDecoyNameRunes = 60
	MaxDecoyNoteRunes = 200
)

// DecoySiteRow is a site without its files.
type DecoySiteRow struct {
	ID, Name, Note, Source, SourceLabel, State, BundleHash string
	CreatedAt, UpdatedAt                                   time.Time
	CreatedBy, UpdatedBy                                   string
}

// DecoyFileRef is a file of a version with its size, as kept in decoy_site_version.files_json.
type DecoyFileRef struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

// DecoyVersionRow is one saved version of a site. Computed is false for a version made by the migration whose bundle has not
// been read yet (BackfillDecoyVersions).
type DecoyVersionRow struct {
	SiteID     string
	Seq        int
	BundleHash string
	CreatedAt  time.Time
	CreatedBy  string
	Size       int
	FileCount  int
	Files      []DecoyFileRef
	Title      string
	Warnings   []decoy.Warning
	Computed   bool
}

// DecoySiteInfo is a site with its current version and how many nodes serve it.
type DecoySiteInfo struct {
	DecoySiteRow
	Version   DecoyVersionRow
	NodeCount int
}

// cleanDecoyText checks a name or a note: trimmed, at most max characters, no control characters.
func cleanDecoyText(s string, max int, required bool) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max || (required && s == "") {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// gcDecoyBundles deletes the bundles that no version and no node holds.
const gcDecoyBundles = `DELETE FROM node_decoy_bundle WHERE sha256 NOT IN (SELECT bundle_hash FROM decoy_site_version)
	AND sha256 NOT IN (SELECT decoy_bundle_hash FROM node WHERE decoy_bundle_hash <> '')`

const decoySiteCols = `id, name, note, source, source_label, state, bundle_hash, created_at, created_by, updated_at, updated_by`

func scanDecoySite(r rowScanner) (DecoySiteRow, error) {
	var d DecoySiteRow
	var created, updated int64
	err := r.Scan(&d.ID, &d.Name, &d.Note, &d.Source, &d.SourceLabel, &d.State, &d.BundleHash, &created, &d.CreatedBy, &updated, &d.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return DecoySiteRow{}, ErrDecoySiteNotFound
	}
	d.CreatedAt, d.UpdatedAt = fromUnix(created), fromUnix(updated)
	return d, err
}

const decoyVersionCols = `site_id, seq, bundle_hash, created_at, created_by, size_bytes, file_count, files_json, title, checks_json`

func scanDecoyVersion(r rowScanner) (DecoyVersionRow, error) {
	var v DecoyVersionRow
	var created int64
	var files, checks string
	err := r.Scan(&v.SiteID, &v.Seq, &v.BundleHash, &created, &v.CreatedBy, &v.Size, &v.FileCount, &files, &v.Title, &checks)
	if errors.Is(err, sql.ErrNoRows) {
		return DecoyVersionRow{}, ErrDecoyVersionNotFound
	}
	if err != nil {
		return DecoyVersionRow{}, err
	}
	v.CreatedAt = fromUnix(created)
	if checks != "" {
		v.Computed = true
		_ = json.Unmarshal([]byte(files), &v.Files)
		_ = json.Unmarshal([]byte(checks), &v.Warnings)
	}
	return v, nil
}

// decoyMeta is what is worked out from a bundle when a version is saved.
type decoyMeta struct {
	size, files              int
	filesJSON, title, checks string
}

// versionMeta runs the checks of a bundle. selfID is the site the version is for ("" for a new site): its own title is not a
// duplicate.
func (s *Store) versionMeta(ctx context.Context, b *decoy.Bundle, source, selfID string) (decoyMeta, error) {
	report := decoy.Analyze(b, source == DecoySourceAssistant)
	if report.Title != "" {
		var n int
		err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM decoy_site s JOIN decoy_site_version v ON v.site_id = s.id
			WHERE s.id <> ? AND v.title = ? AND v.seq = (SELECT max(seq) FROM decoy_site_version WHERE site_id = s.id)`, selfID, report.Title).Scan(&n)
		if err != nil {
			return decoyMeta{}, err
		}
		if n > 0 {
			report.Warnings = append(report.Warnings, decoy.Warning{Code: decoy.WarnDuplicateName})
		}
	}
	infos := b.Infos()
	refs := make([]DecoyFileRef, len(infos))
	for i, f := range infos {
		refs[i] = DecoyFileRef{Path: f.Path, Size: f.Size}
	}
	if report.Warnings == nil {
		report.Warnings = []decoy.Warning{}
	}
	files, _ := json.Marshal(refs)
	checks, _ := json.Marshal(report.Warnings)
	return decoyMeta{size: b.Size(), files: len(infos), filesJSON: string(files), title: report.Title, checks: string(checks)}, nil
}

func versionInsert(siteID, hash string, now int64, by string, m decoyMeta) Stmt {
	return Stmt{Query: `INSERT INTO decoy_site_version (` + decoyVersionCols + `)
		SELECT ?, coalesce(max(seq), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ? FROM decoy_site_version WHERE site_id = ?`,
		Args: []any{siteID, hash, now, by, m.size, m.files, m.filesJSON, m.title, m.checks, siteID}}
}

func versionPrune(siteID string) Stmt {
	return Stmt{Query: `DELETE FROM decoy_site_version WHERE site_id = ? AND seq <= (SELECT max(seq) FROM decoy_site_version WHERE site_id = ?) - ?`,
		Args: []any{siteID, siteID, DecoyVersionLimit}}
}

func bundleUpsert(b *decoy.Bundle) Stmt {
	return Stmt{Query: `INSERT INTO node_decoy_bundle (sha256, content) VALUES (?, ?) ON CONFLICT (sha256) DO UPDATE SET content = excluded.content`,
		Args: []any{b.Hash(), b.Archive()}}
}

// NewDecoySite is the input of CreateDecoySite. Label is the site's source_label; empty = By (who made it).
type NewDecoySite struct {
	Name, Note, Source, State, By, Label string
	Bundle                               *decoy.Bundle
}

// CreateDecoySite saves a bundle as a new site with one version. Refused with ErrDecoyNameTaken, ErrDecoyLibraryFull or the
// invalid-text errors.
func (s *Store) CreateDecoySite(ctx context.Context, now time.Time, in NewDecoySite) (DecoySiteRow, error) {
	name, ok := cleanDecoyText(in.Name, MaxDecoyNameRunes, true)
	if !ok {
		return DecoySiteRow{}, ErrDecoyNameInvalid
	}
	note, ok := cleanDecoyText(in.Note, MaxDecoyNoteRunes, false)
	if !ok {
		return DecoySiteRow{}, ErrDecoyNoteInvalid
	}
	if in.State != DecoyStateDraft && in.State != DecoyStateReady {
		in.State = DecoyStateDraft
	}
	if in.Source != DecoySourceAssistant && in.Source != DecoySourceMCP {
		in.Source = DecoySourceUpload
	}
	if taken, err := s.decoyNameTaken(ctx, name, ""); err != nil || taken {
		return DecoySiteRow{}, firstErr(err, ErrDecoyNameTaken)
	}
	meta, err := s.versionMeta(ctx, in.Bundle, in.Source, "")
	if err != nil {
		return DecoySiteRow{}, err
	}
	id, at := NewID("dsi_"), unix(now)
	if in.Label == "" {
		in.Label = in.By
	}
	_, err = s.batch(ctx,
		guard(`SELECT (SELECT count(*) FROM decoy_site) < ?`, DecoySiteLimit),
		bundleUpsert(in.Bundle),
		Stmt{Query: `INSERT INTO decoy_site (` + decoySiteCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{id, name, note, in.Source, in.Label, in.State, in.Bundle.Hash(), at, in.By, at, in.By}},
		versionInsert(id, in.Bundle.Hash(), at, in.By, meta),
	)
	if errors.Is(err, errGuard) {
		return DecoySiteRow{}, ErrDecoyLibraryFull
	}
	if err != nil {
		if taken, _ := s.decoyNameTaken(ctx, name, ""); taken {
			return DecoySiteRow{}, ErrDecoyNameTaken
		}
		return DecoySiteRow{}, err
	}
	return s.DecoySite(ctx, id)
}

func firstErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// decoyNameTaken says whether another site (not exceptID) has the name (compared ignoring case).
func (s *Store) decoyNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM decoy_site WHERE name = ? AND id <> ?`, name, exceptID).Scan(&n)
	return n > 0, err
}

// DecoySite reads one site.
func (s *Store) DecoySite(ctx context.Context, id string) (DecoySiteRow, error) {
	return scanDecoySite(s.R.QueryRowContext(ctx, `SELECT `+decoySiteCols+` FROM decoy_site WHERE id = ?`, id))
}

// DecoySites lists every site with its current version and the number of nodes that serve it, most recently changed first.
func (s *Store) DecoySites(ctx context.Context) ([]DecoySiteInfo, error) {
	return s.decoyInfos(ctx, "")
}

// DecoySiteInfoOf is DecoySites for one site.
func (s *Store) DecoySiteInfoOf(ctx context.Context, id string) (DecoySiteInfo, error) {
	out, err := s.decoyInfos(ctx, id)
	if err == nil && len(out) == 0 {
		err = ErrDecoySiteNotFound
	}
	if err != nil {
		return DecoySiteInfo{}, err
	}
	return out[0], nil
}

func (s *Store) decoyInfos(ctx context.Context, id string) ([]DecoySiteInfo, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT s.id, s.name, s.note, s.source, s.source_label, s.state, s.bundle_hash, s.created_at, s.created_by,
			s.updated_at, s.updated_by, v.site_id, v.seq, v.bundle_hash, v.created_at, v.created_by, v.size_bytes, v.file_count, v.files_json, v.title, v.checks_json,
			(SELECT count(*) FROM node n WHERE n.decoy_site_id = s.id AND n.decoy_mode = 'own' AND n.state <> 'retired')
		FROM decoy_site s JOIN decoy_site_version v ON v.site_id = s.id AND v.seq = (SELECT max(seq) FROM decoy_site_version WHERE site_id = s.id)
		WHERE (? = '' OR s.id = ?) ORDER BY s.updated_at DESC, s.name`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecoySiteInfo
	for rows.Next() {
		var info DecoySiteInfo
		var created, updated int64
		var vCreated int64
		var files, checks string
		v := &info.Version
		if err := rows.Scan(&info.ID, &info.Name, &info.Note, &info.Source, &info.SourceLabel, &info.State, &info.BundleHash, &created, &info.CreatedBy,
			&updated, &info.UpdatedBy, &v.SiteID, &v.Seq, &v.BundleHash, &vCreated, &v.CreatedBy, &v.Size, &v.FileCount, &files, &v.Title, &checks, &info.NodeCount); err != nil {
			return nil, err
		}
		info.CreatedAt, info.UpdatedAt, v.CreatedAt = fromUnix(created), fromUnix(updated), fromUnix(vCreated)
		if checks != "" {
			v.Computed = true
			_ = json.Unmarshal([]byte(files), &v.Files)
			_ = json.Unmarshal([]byte(checks), &v.Warnings)
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// DecoySiteVersions lists the versions of a site, the newest first.
func (s *Store) DecoySiteVersions(ctx context.Context, id string) ([]DecoyVersionRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+decoyVersionCols+` FROM decoy_site_version WHERE site_id = ? ORDER BY seq DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecoyVersionRow
	for rows.Next() {
		v, err := scanDecoyVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DecoySiteBundle reads the files of a version (seq 0 = the current one).
func (s *Store) DecoySiteBundle(ctx context.Context, id string, seq int) (*decoy.Bundle, DecoyVersionRow, error) {
	q := `SELECT ` + decoyVersionCols + ` FROM decoy_site_version WHERE site_id = ? AND seq = (SELECT max(seq) FROM decoy_site_version WHERE site_id = ?)`
	args := []any{id, id}
	if seq > 0 {
		q, args = `SELECT `+decoyVersionCols+` FROM decoy_site_version WHERE site_id = ? AND seq = ?`, []any{id, seq}
	}
	v, err := scanDecoyVersion(s.R.QueryRowContext(ctx, q, args...))
	if errors.Is(err, ErrDecoyVersionNotFound) {
		if _, serr := s.DecoySite(ctx, id); serr != nil {
			return nil, v, serr
		}
		return nil, v, ErrDecoyVersionNotFound
	}
	if err != nil {
		return nil, v, err
	}
	archive, err := s.NodeDecoyBundle(ctx, v.BundleHash)
	if err != nil {
		return nil, v, err
	}
	b, err := decoy.ParseStoredArchive(archive)
	return b, v, err
}

// DecoySiteNodes lists the nodes (not retired) that serve the site.
func (s *Store) DecoySiteNodes(ctx context.Context, id string) ([]NodeRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+nodeCols+` FROM node WHERE decoy_site_id = ? AND decoy_mode = 'own' AND state <> 'retired' ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NodeDecoyModeCounts counts the nodes (not retired) in each decoy mode.
func (s *Store) NodeDecoyModeCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT decoy_mode, count(*) FROM node WHERE state <> 'retired' GROUP BY decoy_mode`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var mode string
		var n int
		if err := rows.Scan(&mode, &n); err != nil {
			return nil, err
		}
		out[mode] = n
	}
	return out, rows.Err()
}

// PanelDecoySource reads the panel's own site setting: "builtin", "dir", "site:<id>", or "" when it was never set.
func (s *Store) PanelDecoySource(ctx context.Context) (string, error) {
	v, err := s.Setting(ctx, PanelDecoySourceKey)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return v, err
}

// SetPanelDecoySource stores "builtin" or "dir", or "site:<id>" for a ready site.
func (s *Store) SetPanelDecoySource(ctx context.Context, source string) error {
	id, isSite := strings.CutPrefix(source, "site:")
	if !isSite {
		return s.SetSettingIfChanged(ctx, PanelDecoySourceKey, source)
	}
	_, err := s.batch(ctx,
		guard(`SELECT EXISTS (SELECT 1 FROM decoy_site WHERE id = ? AND state = 'ready')`, id),
		Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, Args: []any{PanelDecoySourceKey, source}},
	)
	if errors.Is(err, errGuard) {
		if _, serr := s.DecoySite(ctx, id); serr != nil {
			return serr
		}
		return ErrDecoySiteDraft
	}
	return err
}

// decoySiteUse says where a site stands: the nodes (not retired) in mode own and the panel.
func (s *Store) decoySiteUse(ctx context.Context, id string) (*DecoySiteInUseError, error) {
	nodes, err := s.DecoySiteNodes(ctx, id)
	if err != nil {
		return nil, err
	}
	use := &DecoySiteInUseError{}
	for _, n := range nodes {
		use.NodeIDs = append(use.NodeIDs, n.ID)
	}
	source, err := s.PanelDecoySource(ctx)
	use.Panel = source == "site:"+id
	return use, err
}

// notInUse is the guard of the changes a site that stands somewhere does not allow.
const notInUse = `SELECT NOT EXISTS (SELECT 1 FROM node WHERE decoy_site_id = ? AND decoy_mode = 'own' AND state <> 'retired')
	AND coalesce((SELECT v FROM setting WHERE k = ?), '') <> ?`

func notInUseGuard(id string) Stmt {
	return guard(notInUse, id, PanelDecoySourceKey, "site:"+id)
}

// inUseOr turns a lost not-in-use guard into the error that says where the site stands.
func (s *Store) inUseOr(ctx context.Context, id string, err error) error {
	if !errors.Is(err, errGuard) {
		return err
	}
	if _, serr := s.DecoySite(ctx, id); serr != nil {
		return serr
	}
	use, uerr := s.decoySiteUse(ctx, id)
	if uerr != nil {
		return uerr
	}
	return use
}

// UpdateDecoySite renames a site and changes its note.
func (s *Store) UpdateDecoySite(ctx context.Context, now time.Time, id, name, note, by string) (DecoySiteRow, error) {
	name, ok := cleanDecoyText(name, MaxDecoyNameRunes, true)
	if !ok {
		return DecoySiteRow{}, ErrDecoyNameInvalid
	}
	note, ok = cleanDecoyText(note, MaxDecoyNoteRunes, false)
	if !ok {
		return DecoySiteRow{}, ErrDecoyNoteInvalid
	}
	res, err := s.W.ExecContext(ctx, `UPDATE decoy_site SET name = ?, note = ?, updated_at = ?, updated_by = ? WHERE id = ?`, name, note, unix(now), by, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return DecoySiteRow{}, ErrDecoyNameTaken
		}
		return DecoySiteRow{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return DecoySiteRow{}, ErrDecoySiteNotFound
	}
	return s.DecoySite(ctx, id)
}

// SetDecoySiteReady marks a draft ready, or puts a site that stands nowhere back to a draft.
func (s *Store) SetDecoySiteReady(ctx context.Context, now time.Time, id string, ready bool, by string) (DecoySiteRow, error) {
	if _, err := s.DecoySite(ctx, id); err != nil {
		return DecoySiteRow{}, err
	}
	state := DecoyStateDraft
	if ready {
		state = DecoyStateReady
	}
	stmts := []Stmt{
		guard(`SELECT EXISTS (SELECT 1 FROM decoy_site WHERE id = ?)`, id),
		{Query: `UPDATE decoy_site SET state = ?, updated_at = ?, updated_by = ? WHERE id = ?`, Args: []any{state, unix(now), by, id}},
	}
	if !ready {
		// nodes that kept the site only as a leftover from a switch to another mode lose it, so none of them can be switched back to a draft
		stmts = append([]Stmt{notInUseGuard(id)}, stmts...)
		stmts = append(stmts, Stmt{Query: `UPDATE node SET decoy_site_id = '', decoy_bundle_hash = '' WHERE decoy_site_id = ?`, Args: []any{id}}, Stmt{Query: gcDecoyBundles})
	}
	if _, err := s.batch(ctx, stmts...); err != nil {
		if errors.Is(err, errGuard) {
			return DecoySiteRow{}, s.inUseOr(ctx, id, err)
		}
		return DecoySiteRow{}, err
	}
	return s.DecoySite(ctx, id)
}

// AddDecoySiteVersion saves a bundle as the newest version of a site and returns the nodes that serve the site (they hold the new
// hash now). changed is false when the bundle is the current version already.
func (s *Store) AddDecoySiteVersion(ctx context.Context, now time.Time, id string, b *decoy.Bundle, by string) (site DecoySiteRow, nodeIDs []string, changed bool, err error) {
	site, err = s.DecoySite(ctx, id)
	if err != nil || site.BundleHash == b.Hash() {
		return site, nil, false, err
	}
	meta, err := s.versionMeta(ctx, b, site.Source, id)
	if err != nil {
		return site, nil, false, err
	}
	at := unix(now)
	_, err = s.batch(ctx,
		guard(`SELECT EXISTS (SELECT 1 FROM decoy_site WHERE id = ?)`, id),
		bundleUpsert(b),
		versionInsert(id, b.Hash(), at, by, meta),
		Stmt{Query: `UPDATE decoy_site SET bundle_hash = ?, updated_at = ?, updated_by = ? WHERE id = ?`, Args: []any{b.Hash(), at, by, id}},
		Stmt{Query: `UPDATE node SET decoy_bundle_hash = ? WHERE decoy_site_id = ?`, Args: []any{b.Hash(), id}},
		versionPrune(id),
		Stmt{Query: gcDecoyBundles},
	)
	return s.afterVersion(ctx, id, err)
}

// RevertDecoySite saves an earlier version again as the newest one.
func (s *Store) RevertDecoySite(ctx context.Context, now time.Time, id string, seq int, by string) (site DecoySiteRow, nodeIDs []string, changed bool, err error) {
	site, err = s.DecoySite(ctx, id)
	if err != nil {
		return site, nil, false, err
	}
	v, err := scanDecoyVersion(s.R.QueryRowContext(ctx, `SELECT `+decoyVersionCols+` FROM decoy_site_version WHERE site_id = ? AND seq = ?`, id, seq))
	if err != nil || v.BundleHash == site.BundleHash {
		return site, nil, false, err
	}
	at := unix(now)
	_, err = s.batch(ctx,
		guard(`SELECT EXISTS (SELECT 1 FROM decoy_site_version WHERE site_id = ? AND seq = ?)`, id, seq),
		Stmt{Query: `INSERT INTO decoy_site_version (` + decoyVersionCols + `)
			SELECT site_id, (SELECT max(seq) + 1 FROM decoy_site_version WHERE site_id = ?), bundle_hash, ?, ?, size_bytes, file_count, files_json, title, checks_json
			FROM decoy_site_version WHERE site_id = ? AND seq = ?`, Args: []any{id, at, by, id, seq}},
		Stmt{Query: `UPDATE decoy_site SET bundle_hash = ?, updated_at = ?, updated_by = ? WHERE id = ?`, Args: []any{v.BundleHash, at, by, id}},
		Stmt{Query: `UPDATE node SET decoy_bundle_hash = ? WHERE decoy_site_id = ?`, Args: []any{v.BundleHash, id}},
		versionPrune(id),
		Stmt{Query: gcDecoyBundles},
	)
	return s.afterVersion(ctx, id, err)
}

func (s *Store) afterVersion(ctx context.Context, id string, err error) (DecoySiteRow, []string, bool, error) {
	if errors.Is(err, errGuard) {
		return DecoySiteRow{}, nil, false, ErrDecoySiteNotFound
	}
	if err != nil {
		return DecoySiteRow{}, nil, false, err
	}
	site, err := s.DecoySite(ctx, id)
	if err != nil {
		return site, nil, true, err
	}
	nodes, err := s.DecoySiteNodes(ctx, id)
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return site, ids, true, err
}

// DeleteDecoySite deletes a site that stands nowhere, with its versions and the bundles nothing else holds.
func (s *Store) DeleteDecoySite(ctx context.Context, id string) error {
	if _, err := s.DecoySite(ctx, id); err != nil {
		return err
	}
	_, err := s.batch(ctx,
		notInUseGuard(id),
		Stmt{Query: `UPDATE node SET decoy_site_id = '', decoy_bundle_hash = '' WHERE decoy_site_id = ?`, Args: []any{id}},
		Stmt{Query: `DELETE FROM decoy_site_version WHERE site_id = ?`, Args: []any{id}},
		Stmt{Query: `DELETE FROM decoy_site WHERE id = ?`, Args: []any{id}},
		Stmt{Query: gcDecoyBundles},
	)
	if err != nil {
		return s.inUseOr(ctx, id, err)
	}
	return nil
}

// AssignDecoySite puts a ready site on nodes: their mode becomes "own" and they hold the site's current hash.
func (s *Store) AssignDecoySite(ctx context.Context, id string, nodeIDs []string) ([]NodeRow, error) {
	if len(nodeIDs) == 0 || len(nodeIDs) > 50 {
		return nil, errors.New("assigning a decoy site needs 1 to 50 nodes")
	}
	site, err := s.DecoySite(ctx, id)
	if err != nil {
		return nil, err
	}
	if site.State != DecoyStateReady {
		return nil, ErrDecoySiteDraft
	}
	stmts := []Stmt{guard(`SELECT EXISTS (SELECT 1 FROM decoy_site WHERE id = ? AND state = 'ready')`, id)}
	seen := map[string]bool{}
	for _, nid := range nodeIDs {
		if seen[nid] {
			return nil, errors.New("decoy site assignment has a duplicate node id")
		}
		seen[nid] = true
		n, err := s.Node(ctx, nid)
		if err != nil {
			return nil, err
		}
		if n.State == "retired" {
			return nil, ErrNodeRetired
		}
		stmts = append(stmts,
			guard(`SELECT EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, nid),
			Stmt{Query: `UPDATE node SET decoy_mode = 'own', decoy_site_id = ?, decoy_bundle_hash = (SELECT bundle_hash FROM decoy_site WHERE id = ?) WHERE id = ?`, Args: []any{id, id, nid}},
		)
	}
	stmts = append(stmts, Stmt{Query: gcDecoyBundles})
	if _, err := s.batch(ctx, stmts...); err != nil {
		if errors.Is(err, errGuard) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return s.nodesByID(ctx, nodeIDs)
}

// UnassignDecoySite takes a site off nodes that serve it; they go to mode (builtin or panel) and forget the site.
func (s *Store) UnassignDecoySite(ctx context.Context, id string, nodeIDs []string, mode string) ([]NodeRow, error) {
	if mode != decoy.ModeBuiltin && mode != decoy.ModePanel {
		return nil, errors.New("a node taken off a decoy site gets builtin or panel")
	}
	if len(nodeIDs) == 0 || len(nodeIDs) > 50 {
		return nil, errors.New("taking a decoy site off needs 1 to 50 nodes")
	}
	stmts := make([]Stmt, 0, 2*len(nodeIDs)+1)
	for _, nid := range nodeIDs {
		stmts = append(stmts,
			guard(`SELECT EXISTS (SELECT 1 FROM node WHERE id = ? AND decoy_site_id = ? AND decoy_mode = 'own' AND state <> 'retired')`, nid, id),
			Stmt{Query: `UPDATE node SET decoy_mode = ?, decoy_site_id = '', decoy_bundle_hash = '' WHERE id = ?`, Args: []any{mode, nid}},
		)
	}
	stmts = append(stmts, Stmt{Query: gcDecoyBundles})
	if _, err := s.batch(ctx, stmts...); err != nil {
		if errors.Is(err, errGuard) {
			return nil, ErrDecoyNotAssigned
		}
		return nil, err
	}
	return s.nodesByID(ctx, nodeIDs)
}

func (s *Store) nodesByID(ctx context.Context, ids []string) ([]NodeRow, error) {
	out := make([]NodeRow, 0, len(ids))
	for _, id := range ids {
		n, err := s.Node(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// BackfillDecoyVersions works out the files, title and checks of the versions the migration created (they have none yet) from
// their bundles. A bundle that is missing or no longer valid leaves its version without checks, which is how a page shows it.
func (s *Store) BackfillDecoyVersions(ctx context.Context) error {
	rows, err := s.R.QueryContext(ctx, `SELECT v.site_id, v.seq, v.bundle_hash, s.source FROM decoy_site_version v
		JOIN decoy_site s ON s.id = v.site_id WHERE v.checks_json = ''`)
	if err != nil {
		return err
	}
	type todo struct {
		id, hash, source string
		seq              int
	}
	var list []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.id, &t.seq, &t.hash, &t.source); err != nil {
			rows.Close()
			return err
		}
		list = append(list, t)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, t := range list {
		archive, err := s.NodeDecoyBundle(ctx, t.hash)
		if err != nil {
			continue
		}
		b, err := decoy.ParseStoredArchive(archive)
		if err != nil {
			continue
		}
		meta, err := s.versionMeta(ctx, b, t.source, t.id)
		if err != nil {
			return err
		}
		if _, err := s.W.ExecContext(ctx, `UPDATE decoy_site_version SET size_bytes = ?, file_count = ?, files_json = ?, title = ?, checks_json = ? WHERE site_id = ? AND seq = ?`,
			meta.size, meta.files, meta.filesJSON, meta.title, meta.checks, t.id, t.seq); err != nil {
			return err
		}
	}
	return nil
}
