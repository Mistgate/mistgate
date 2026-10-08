package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AccessProfile is a protocol preset. SettingsJSON holds no secrets; SecretsEnc is the vault blob (AAD =
// ID) of the x-secret values.
type AccessProfile struct {
	ID, Protocol, Name, SettingsJSON string
	SecretsEnc                       []byte
	Version                          uint32
	CreatedAt, UpdatedAt             time.Time
}

const accProfileCols = `p.id AS profile_id, p.protocol AS profile_protocol, p.name AS profile_name, p.settings_json AS profile_settings_json,
	p.secrets_enc AS profile_secrets_enc, p.version AS profile_version, p.created_at AS profile_created_at, p.updated_at AS profile_updated_at`

func scanAccessProfile(r rowScanner) (AccessProfile, error) {
	var p AccessProfile
	var id, protocol, name, settings sql.NullString
	var version, created, updated sql.NullInt64
	err := r.Scan(&id, &protocol, &name, &settings, &p.SecretsEnc, &version, &created, &updated)
	p.ID, p.Protocol, p.Name, p.SettingsJSON = id.String, protocol.String, name.String, settings.String
	p.Version = uint32(version.Int64)
	p.CreatedAt, p.UpdatedAt = accTimeFromUnix(created), accTimeFromUnix(updated)
	return p, err
}

// CreateProfile inserts a profile; ErrAccessExists when the name is taken.
func (a Access) CreateProfile(ctx context.Context, p AccessProfile) error {
	_, err := a.s.W.ExecContext(ctx,
		`INSERT INTO profile (id, protocol, name, settings_json, secrets_enc, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		p.ID, p.Protocol, p.Name, p.SettingsJSON, p.SecretsEnc, unix(p.CreatedAt), unix(p.CreatedAt))
	if accIsUnique(err) {
		return ErrAccessExists
	}
	return err
}

// Profile returns one profile, or ErrNotFound.
func (a Access) Profile(ctx context.Context, id string) (AccessProfile, error) {
	p, err := scanAccessProfile(a.s.R.QueryRowContext(ctx, `SELECT `+accProfileCols+` FROM profile p WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Profiles returns all profiles ordered by name.
func (a Access) Profiles(ctx context.Context) ([]AccessProfile, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT `+accProfileCols+` FROM profile p ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessProfile
	for rows.Next() {
		p, err := scanAccessProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProfile writes name, settings and secrets of p (matched by ID) when its stored version equals
// expectedVersion, bumps the version and, with bumpInbounds, the spec_version of the profile's inbounds. With
// bumpEpoch it also raises critical_epoch in the same transaction: every device that received its config before
// is then "stale" (an x-critical change of an AWG profile).
// ErrAccessVersion on mismatch, ErrAccessExists on a name clash. Returns the stored row.
func (a Access) UpdateProfile(ctx context.Context, p AccessProfile, expectedVersion uint32, bumpInbounds, bumpEpoch bool, now time.Time) (AccessProfile, error) {
	stmts := []Stmt{
		guard(`EXISTS (SELECT 1 FROM profile WHERE id = ? AND version = ?)`, p.ID, int64(expectedVersion)),
		{Query: `UPDATE profile SET name = ?, settings_json = ?, secrets_enc = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			Args: []any{p.Name, p.SettingsJSON, p.SecretsEnc, unix(now), p.ID}},
	}
	if bumpEpoch {
		stmts = append(stmts, Stmt{Query: `UPDATE profile SET critical_epoch = critical_epoch + 1 WHERE id = ?`, Args: []any{p.ID}})
	}
	if bumpInbounds {
		stmts = append(stmts, Stmt{Query: `UPDATE inbound SET spec_version = spec_version + 1, updated_at = ? WHERE profile_id = ?`, Args: []any{unix(now), p.ID}})
	}
	_, err := a.s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) {
		var version uint32
		readErr := a.s.R.QueryRowContext(ctx, `SELECT version FROM profile WHERE id = ?`, p.ID).Scan(&version)
		if errors.Is(readErr, sql.ErrNoRows) {
			return AccessProfile{}, ErrNotFound
		}
		if readErr != nil {
			return AccessProfile{}, readErr
		}
		return AccessProfile{}, ErrAccessVersion
	}
	if accIsUnique(err) {
		return AccessProfile{}, ErrAccessExists
	}
	if err != nil {
		return AccessProfile{}, err
	}
	return a.Profile(ctx, p.ID)
}

// DeleteProfile removes a profile that has no inbounds; ErrAccessInUse otherwise. The AWG credentials and peers
// of the profile go with it (foreign keys); the explicit AWG devices that held them are revoked, so none stays
// behind as an empty shell.
func (a Access) DeleteProfile(ctx context.Context, id string, now time.Time) error {
	_, err := a.s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM profile WHERE id = ?)
			AND NOT EXISTS (SELECT 1 FROM inbound WHERE profile_id = ?)`, id, id),
		Stmt{Query: `UPDATE device SET revoked_at = ? WHERE revoked_at IS NULL AND hwid_hash IS NOT NULL AND id IN
		   (SELECT device_id FROM device_credential WHERE profile_id = ?)`, Args: []any{unix(now), id}},
		Stmt{Query: `DELETE FROM profile WHERE id = ?`, Args: []any{id}},
	)
	if errors.Is(err, errGuard) {
		var inbounds int
		if readErr := a.s.R.QueryRowContext(ctx, `SELECT count(*) FROM inbound WHERE profile_id = ?`, id).Scan(&inbounds); readErr != nil {
			return readErr
		}
		if inbounds != 0 {
			return ErrAccessInUse
		}
		return ErrNotFound
	}
	return err
}

// AccessBrief is a user id and name.
type AccessBrief struct{ ID, Name string }

// ProfileUsers lists users whose group contains the profile and whose app toggles allow it: happ and
// amnezia say which toggles count (derived from the protocol's Clients()). Ordered by name.
func (a Access) ProfileUsers(ctx context.Context, profileID string, happ, amnezia bool) ([]AccessBrief, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT u.id, u.name FROM user u JOIN user_group_profile gp ON gp.group_id = u.group_id
		 WHERE gp.profile_id = ? AND ((? = 1 AND u.app_happ = 1) OR (? = 1 AND u.app_amnezia = 1))
		 ORDER BY u.name`, profileID, accBool(happ), accBool(amnezia))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessBrief
	for rows.Next() {
		var b AccessBrief
		if err := rows.Scan(&b.ID, &b.Name); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AccessInbound is a profile deployed on a node. The fleet module updates State, LastError and the
// certificate fields from the node's results.
type AccessInbound struct {
	ID, ProfileID, NodeID string
	PortOverride          uint16 // 0 = none
	TLSServerNameOverride string
	Enabled               bool
	SpecVersion           uint64
	State                 string // pending | active | failed | disabled
	LastError             string
	CertPinSHA256         string
	CertNotAfter          time.Time
	CreatedAt, UpdatedAt  time.Time
	// Plugin-neutral material of the inbound (AWG: the server key pair). PluginStateEnc is the vault blob (AAD =
	// inbound id) and nil for hysteria2; PluginPublicJSON is "{}" when there is none.
	PluginStateEnc   []byte
	PluginPublicJSON string
	// The last agent.v1.AwgHealth the node reported, as protojson ("" = never), and when.
	AwgHealthJSON string
	AwgHealthAt   time.Time
}

const accInboundCols = `i.id AS inbound_id, i.profile_id AS inbound_profile_id, i.node_id AS inbound_node_id,
	i.port_override AS inbound_port_override, i.tls_server_name_override AS inbound_tls_server_name_override, i.enabled AS inbound_enabled,
	i.spec_version AS inbound_spec_version, i.state AS inbound_state, i.last_error AS inbound_last_error,
	i.cert_pin_sha256 AS inbound_cert_pin_sha256, i.cert_not_after AS inbound_cert_not_after, i.created_at AS inbound_created_at,
	i.updated_at AS inbound_updated_at, i.plugin_state_enc AS inbound_plugin_state_enc, i.plugin_public_json AS inbound_plugin_public_json,
	i.awg_health_json AS inbound_awg_health_json, i.awg_health_at AS inbound_awg_health_at`

func scanAccessInbound(r rowScanner) (AccessInbound, error) {
	var i AccessInbound
	var id, profileID, nodeID, tlsName, state, lastError, certPin, pluginPublic, healthJSON sql.NullString
	var port, enabled, specVersion, notAfter, created, updated, healthAt sql.NullInt64
	err := r.Scan(&id, &profileID, &nodeID, &port, &tlsName, &enabled, &specVersion,
		&state, &lastError, &certPin, &notAfter, &created, &updated,
		&i.PluginStateEnc, &pluginPublic, &healthJSON, &healthAt)
	i.ID, i.ProfileID, i.NodeID = id.String, profileID.String, nodeID.String
	i.PortOverride, i.TLSServerNameOverride = uint16(port.Int64), tlsName.String
	i.Enabled, i.SpecVersion = enabled.Int64 == 1, uint64(specVersion.Int64)
	i.State, i.LastError, i.CertPinSHA256 = state.String, lastError.String, certPin.String
	i.PluginPublicJSON, i.AwgHealthJSON = pluginPublic.String, healthJSON.String
	i.CreatedAt, i.UpdatedAt = accTimeFromUnix(created), accTimeFromUnix(updated)
	if notAfter.Int64 != 0 {
		i.CertNotAfter = fromUnix(notAfter.Int64)
	}
	if healthAt.Int64 != 0 {
		i.AwgHealthAt = fromUnix(healthAt.Int64)
	}
	return i, err
}

func accNullPort(p uint16) any {
	if p == 0 {
		return nil
	}
	return int64(p)
}

// CreateInbound inserts an inbound; ErrAccessExists when the profile is already on the node, ErrNotFound
// when the profile or node does not exist.
//
// The inbound takes over the retained server key of its (profile, node), if there is one (RetainedKey): the row is
// deleted in the same transaction, so the key is never on both sides.
func (a Access) CreateInbound(ctx context.Context, i AccessInbound) error {
	_, err := a.s.batch(ctx,
		Stmt{Query: `INSERT INTO inbound (id, profile_id, node_id, port_override, tls_server_name_override, enabled, spec_version, state,
		   plugin_state_enc, plugin_public_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 1, 'pending', ?, ?, ?, ?)`,
			Args: []any{i.ID, i.ProfileID, i.NodeID, accNullPort(i.PortOverride), i.TLSServerNameOverride, int64(accBool(i.Enabled)),
				i.PluginStateEnc, accPublicJSON(i.PluginPublicJSON), unix(i.CreatedAt), unix(i.CreatedAt)}},
		Stmt{Query: `DELETE FROM awg_retained_key WHERE profile_id = ? AND node_id = ?`, Args: []any{i.ProfileID, i.NodeID}},
	)
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

// Inbound returns one inbound, or ErrNotFound.
func (a Access) Inbound(ctx context.Context, id string) (AccessInbound, error) {
	i, err := scanAccessInbound(a.s.R.QueryRowContext(ctx, `SELECT `+accInboundCols+` FROM inbound i WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

func (a Access) inbounds(ctx context.Context, where string, arg any) ([]AccessInbound, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT `+accInboundCols+` FROM inbound i `+where+` ORDER BY i.created_at, i.id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessInbound
	for rows.Next() {
		i, err := scanAccessInbound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// InboundsOfProfile returns the inbounds of a profile.
func (a Access) InboundsOfProfile(ctx context.Context, profileID string) ([]AccessInbound, error) {
	return a.inbounds(ctx, `WHERE i.profile_id = ?`, profileID)
}

// InboundsOfNode returns the inbounds of a node.
func (a Access) InboundsOfNode(ctx context.Context, nodeID string) ([]AccessInbound, error) {
	return a.inbounds(ctx, `WHERE i.node_id = ?`, nodeID)
}

// UpdateInbound writes the admin-editable fields and the spec version of i (matched by ID). When
// resetState is set the state goes back to pending (enabled) or disabled, until the node reports again.
func (a Access) UpdateInbound(ctx context.Context, i AccessInbound, resetState bool, now time.Time) error {
	q := `UPDATE inbound SET port_override = ?, tls_server_name_override = ?, enabled = ?, spec_version = ?, updated_at = ?`
	if resetState {
		q += `, state = CASE WHEN ? = 1 THEN 'pending' ELSE 'disabled' END, last_error = ''`
	}
	q += ` WHERE id = ?`
	args := []any{accNullPort(i.PortOverride), i.TLSServerNameOverride, accBool(i.Enabled), i.SpecVersion, unix(now)}
	if resetState {
		args = append(args, accBool(i.Enabled))
	}
	args = append(args, i.ID)
	res, err := a.s.W.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetInboundBuildError records that the inbound's desired spec could not be built (corrupt profile,
// unreadable secrets) so the node page shows it: state failed and last_error text. It writes only when
// that is news, because the desired state is recomputed on every change and stats tick.
func (a Access) SetInboundBuildError(ctx context.Context, id, msg string, now time.Time) error {
	msg = Clip(msg, 512)
	_, err := a.s.W.ExecContext(ctx,
		`UPDATE inbound SET state = 'failed', last_error = ?, updated_at = ?
		 WHERE id = ? AND enabled = 1 AND (state <> 'failed' OR last_error <> ?)`, msg, unix(now), id, msg)
	return err
}

// RetainedKeyAAD is the vault AAD of a retained server key: bound to the pair, not to the inbound that is gone.
func RetainedKeyAAD(profileID, nodeID string) string {
	return "awg_retained_key:" + profileID + ":" + nodeID
}

// AccessRetainedKey is the server key of a (profile, node) whose inbound was removed. StateEnc is the vault blob
// under RetainedKeyAAD.
type AccessRetainedKey struct {
	StateEnc   []byte
	PublicJSON string
}

// RetainedKey returns the retained server key of the pair, or ErrNotFound.
func (a Access) RetainedKey(ctx context.Context, profileID, nodeID string) (AccessRetainedKey, error) {
	var k AccessRetainedKey
	err := a.s.W.QueryRowContext(ctx, // the writer: a delete just before must be visible
		`SELECT state_enc, public_json FROM awg_retained_key WHERE profile_id = ? AND node_id = ?`, profileID, nodeID).
		Scan(&k.StateEnc, &k.PublicJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// DeleteInbound removes an inbound. With retain set and key material on the inbound, the key moves to
// awg_retained_key in the same transaction (replacing an older row of the pair, unless the node is retired):
// retain turns the inbound's vault blob into the blob for RetainedKeyAAD, and returns nil to keep nothing (an
// unreadable key must not make the inbound undeletable).
func (a Access) DeleteInbound(ctx context.Context, id string, now time.Time, retain func(stateEnc []byte, profileID, nodeID string) ([]byte, error)) error {
	for range 8 { // the guard pins what retain saw; a change in between starts over
		var profileID, nodeID, public string
		var enc []byte
		err := a.s.W.QueryRowContext(ctx, `SELECT profile_id, node_id, plugin_state_enc, plugin_public_json FROM inbound WHERE id = ?`, id).
			Scan(&profileID, &nodeID, &enc, &public)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		stmts := []Stmt{guard(`EXISTS (SELECT 1 FROM inbound WHERE id = ? AND profile_id = ? AND node_id = ?
			AND plugin_state_enc IS ? AND plugin_public_json = ?)`, id, profileID, nodeID, enc, public)}
		if retain != nil && len(enc) > 0 {
			kept, err := retain(enc, profileID, nodeID)
			if err != nil {
				return err
			}
			if len(kept) > 0 {
				stmts = append(stmts, Stmt{Query: `INSERT OR REPLACE INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at)
					 SELECT ?1, ?2, ?3, ?4, ?5 WHERE EXISTS (SELECT 1 FROM node WHERE id = ?2 AND state <> 'retired')`,
					Args: []any{profileID, nodeID, kept, accPublicJSON(public), unix(now)}})
			}
		}
		stmts = append(stmts, Stmt{Query: `DELETE FROM inbound WHERE id = ?`, Args: []any{id}})
		if _, err := a.s.batch(ctx, stmts...); errors.Is(err, errGuard) {
			continue
		} else if err != nil {
			return err
		}
		return nil
	}
	return ErrConflict
}

// AccessInboundFull is an inbound with its profile and node.
type AccessInboundFull struct {
	Inbound AccessInbound
	Profile AccessProfile
	Node    AccessNode
}

// InboundsFull returns every inbound joined with its profile and node, in the order they were created (rowid breaks a
// tie within a second); nodeID non-empty restricts it to one node. Retired nodes are excluded. This is the subscription's
// order, and the server names are numbered in it ("Germany", "Germany 2"): a server added later never takes the name
// of an older one. Deleting a server still renumbers the ones after it; a stored ordinal would keep them.
func (a Access) InboundsFull(ctx context.Context, nodeID string) ([]AccessInboundFull, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT `+accInboundCols+`, `+accProfileCols+`, `+accNodeCols+`
		 FROM inbound i JOIN profile p ON p.id = i.profile_id JOIN node n ON n.id = i.node_id
		 WHERE n.state <> 'retired' AND (? = '' OR n.id = ?)
		 ORDER BY i.created_at, i.rowid`, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessInboundFull
	for rows.Next() {
		f, err := scanAccessInboundFull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanAccessInboundFull(r rowScanner) (AccessInboundFull, error) {
	values, err := scanRowValues(r, 35)
	if err != nil {
		return AccessInboundFull{}, err
	}
	inbound, err := scanAccessInbound(batchRow(values[:17]))
	if err != nil {
		return AccessInboundFull{}, err
	}
	profile, err := scanAccessProfile(batchRow(values[17:25]))
	if err != nil {
		return AccessInboundFull{}, err
	}
	node, err := scanAccessNode(batchRow(values[25:]))
	if err != nil {
		return AccessInboundFull{}, err
	}
	return AccessInboundFull{Inbound: inbound, Profile: profile, Node: node}, nil
}

// AccessGroup is an access group: a named set of profiles.
type AccessGroup struct {
	ID, Name    string
	ProfileIDs  []string
	UserCount   int
	CreatedAt   time.Time
	DNSPresetID string // "" = the instance default
	// Color is a tone of GroupTones, "" = none picked. CreateGroup fills in the least used tone when it is empty.
	Color string
}

// GroupTones is the palette of a group's colour, in the order the admin offers it and the order a new group takes the least
// used tone in (ties go to the earlier one). Sky and mint come last: they are also the Link and Keys chips.
var GroupTones = []string{"lavender", "sand", "sage", "rose", "sky", "mint"}

func accJSON(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// CreateGroup inserts a group with its profile set. ErrAccessExists on a name clash, ErrNotFound when a
// profile id does not exist.
func (a Access) CreateGroup(ctx context.Context, g AccessGroup) error {
	insert := Stmt{Query: `INSERT INTO user_group (id, name, created_at, dns_preset_id, color) VALUES (?, ?, ?, ?, ?)`,
		Args: []any{g.ID, g.Name, unix(g.CreatedAt), accNullStr(g.DNSPresetID), g.Color}}
	if g.Color == "" {
		values := make([]string, len(GroupTones))
		args := make([]any, 0, len(GroupTones)*2+4)
		for i, tone := range GroupTones {
			values[i] = "(?, ?)"
			args = append(args, tone, int64(i))
		}
		args = append(args, g.ID, g.Name, unix(g.CreatedAt), accNullStr(g.DNSPresetID))
		insert = Stmt{Query: `WITH tones(color, position) AS (VALUES ` + strings.Join(values, ",") + `)
			INSERT INTO user_group (id, name, created_at, dns_preset_id, color)
			SELECT ?, ?, ?, ?, (SELECT tones.color FROM tones
				ORDER BY (SELECT count(*) FROM user_group existing WHERE existing.color = tones.color), tones.position LIMIT 1)`, Args: args}
	}
	stmts := []Stmt{insert}
	stmts = append(stmts, accSetGroupProfileStmts(g.ID, g.ProfileIDs)...)
	_, err := a.s.batch(ctx, stmts...)
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

func accSetGroupProfileStmts(groupID string, profileIDs []string) []Stmt {
	stmts := []Stmt{{Query: `DELETE FROM user_group_profile WHERE group_id = ?`, Args: []any{groupID}}}
	for _, pid := range profileIDs {
		stmts = append(stmts, Stmt{Query: `INSERT OR IGNORE INTO user_group_profile (group_id, profile_id) VALUES (?, ?)`, Args: []any{groupID, pid}})
	}
	return stmts
}

// Groups returns all groups with their profile ids and user counts, ordered by name.
func (a Access) Groups(ctx context.Context) ([]AccessGroup, error) {
	return a.groups(ctx, "")
}

// Group returns one group, or ErrNotFound.
func (a Access) Group(ctx context.Context, id string) (AccessGroup, error) {
	gs, err := a.groups(ctx, id)
	if err != nil {
		return AccessGroup{}, err
	}
	if len(gs) == 0 {
		return AccessGroup{}, ErrNotFound
	}
	return gs[0], nil
}

func (a Access) groups(ctx context.Context, id string) ([]AccessGroup, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT g.id, g.name, g.created_at, (SELECT count(*) FROM user u WHERE u.group_id = g.id), g.dns_preset_id, g.color
		 FROM user_group g WHERE (? = '' OR g.id = ?) ORDER BY g.name`, id, id)
	if err != nil {
		return nil, err
	}
	var out []AccessGroup
	idx := map[string]int{}
	for rows.Next() {
		g, err := scanAccessGroup(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		g.ProfileIDs = []string{}
		idx[g.ID] = len(out)
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prow, err := a.s.R.QueryContext(ctx,
		`SELECT group_id, profile_id FROM user_group_profile WHERE (? = '' OR group_id = ?) ORDER BY profile_id`, id, id)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		var gid, pid string
		if err := prow.Scan(&gid, &pid); err != nil {
			return nil, err
		}
		if i, ok := idx[gid]; ok {
			out[i].ProfileIDs = append(out[i].ProfileIDs, pid)
		}
	}
	return out, prow.Err()
}

func scanAccessGroup(r rowScanner) (AccessGroup, error) {
	var g AccessGroup
	var id, name, dnsPresetID, color sql.NullString
	var created, userCount sql.NullInt64
	err := r.Scan(&id, &name, &created, &userCount, &dnsPresetID, &color)
	if err != nil {
		return AccessGroup{}, err
	}
	g.ID, g.Name, g.DNSPresetID, g.Color = id.String, name.String, dnsPresetID.String, color.String
	g.UserCount = int(userCount.Int64)
	if created.Valid {
		g.CreatedAt = fromUnix(created.Int64)
	}
	return g, nil
}

// UpdateGroup renames a group, replaces its profile set and/or its DNS preset, sets its colour (nil = unchanged, "" = none).
func (a Access) UpdateGroup(ctx context.Context, id string, name *string, profileIDs *[]string, dnsPresetID, color *string) error {
	stmts := []Stmt{guard(`EXISTS (SELECT 1 FROM user_group WHERE id = ?)`, id)}
	if name != nil {
		stmts = append(stmts, Stmt{Query: `UPDATE user_group SET name = ? WHERE id = ?`, Args: []any{*name, id}})
	}
	if dnsPresetID != nil {
		stmts = append(stmts, Stmt{Query: `UPDATE user_group SET dns_preset_id = ? WHERE id = ?`, Args: []any{accNullStr(*dnsPresetID), id}})
	}
	if color != nil {
		stmts = append(stmts, Stmt{Query: `UPDATE user_group SET color = ? WHERE id = ?`, Args: []any{*color, id}})
	}
	if profileIDs != nil {
		stmts = append(stmts, accSetGroupProfileStmts(id, *profileIDs)...)
	}
	_, err := a.s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) {
		return ErrNotFound
	}
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

// DeleteGroup removes a group without users; ErrAccessInUse otherwise. A non-empty moveTo first moves the group's
// users there, in the same transaction (ErrNotFound when that group does not exist).
func (a Access) DeleteGroup(ctx context.Context, id, moveTo string) error {
	var err error
	if moveTo == "" || moveTo == id {
		_, err = a.s.batch(ctx,
			guard(`EXISTS (SELECT 1 FROM user_group WHERE id = ?)
				AND NOT EXISTS (SELECT 1 FROM user WHERE group_id = ?)`, id, id),
			Stmt{Query: `DELETE FROM user_group WHERE id = ?`, Args: []any{id}},
		)
		if errors.Is(err, errGuard) {
			var groupExists, hasUsers int
			if readErr := a.s.R.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_group WHERE id = ?),
				EXISTS (SELECT 1 FROM user WHERE group_id = ?)`, id, id).Scan(&groupExists, &hasUsers); readErr != nil {
				return readErr
			}
			if hasUsers != 0 {
				return ErrAccessInUse
			}
			return ErrNotFound
		}
		return err
	}
	_, err = a.s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM user_group WHERE id = ?)`, id),
		Stmt{Query: `UPDATE user SET group_id = ? WHERE group_id = ?`, Args: []any{moveTo, id}},
		Stmt{Query: `DELETE FROM user_group WHERE id = ?`, Args: []any{id}},
	)
	if errors.Is(err, errGuard) {
		return ErrNotFound
	}
	if accIsFK(err) {
		return ErrNotFound
	}
	return err
}
