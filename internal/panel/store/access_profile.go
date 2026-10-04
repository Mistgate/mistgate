package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

const accProfileCols = `p.id, p.protocol, p.name, p.settings_json, p.secrets_enc, p.version, p.created_at, p.updated_at`

func scanAccessProfile(r interface{ Scan(...any) error }) (AccessProfile, error) {
	var p AccessProfile
	var c, u int64
	err := r.Scan(&p.ID, &p.Protocol, &p.Name, &p.SettingsJSON, &p.SecretsEnc, &p.Version, &c, &u)
	p.CreatedAt, p.UpdatedAt = fromUnix(c), fromUnix(u)
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
	err := a.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE profile SET name = ?, settings_json = ?, secrets_enc = ?, version = version + 1, updated_at = ?
			 WHERE id = ? AND version = ?`,
			p.Name, p.SettingsJSON, p.SecretsEnc, unix(now), p.ID, expectedVersion)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if tx.QueryRowContext(ctx, `SELECT 1 FROM profile WHERE id = ?`, p.ID).Scan(&exists) != nil {
				return ErrNotFound
			}
			return ErrAccessVersion
		}
		if bumpEpoch {
			if _, err = tx.ExecContext(ctx, `UPDATE profile SET critical_epoch = critical_epoch + 1 WHERE id = ?`, p.ID); err != nil {
				return err
			}
		}
		if bumpInbounds {
			_, err = tx.ExecContext(ctx, `UPDATE inbound SET spec_version = spec_version + 1, updated_at = ? WHERE profile_id = ?`, unix(now), p.ID)
		}
		return err
	})
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
	return a.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM inbound WHERE profile_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAccessInUse
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE device SET revoked_at = ? WHERE revoked_at IS NULL AND hwid_hash IS NOT NULL AND id IN
			   (SELECT device_id FROM device_credential WHERE profile_id = ?)`, unix(now), id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM profile WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		return nil
	})
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

const accInboundCols = `i.id, i.profile_id, i.node_id, i.port_override, i.tls_server_name_override, i.enabled, i.spec_version,
	i.state, i.last_error, i.cert_pin_sha256, i.cert_not_after, i.created_at, i.updated_at,
	i.plugin_state_enc, i.plugin_public_json, i.awg_health_json, i.awg_health_at`

func scanAccessInbound(r interface{ Scan(...any) error }) (AccessInbound, error) {
	var i AccessInbound
	var port sql.NullInt64
	var en int
	var notAfter, c, u, healthAt int64
	err := r.Scan(&i.ID, &i.ProfileID, &i.NodeID, &port, &i.TLSServerNameOverride, &en, &i.SpecVersion,
		&i.State, &i.LastError, &i.CertPinSHA256, &notAfter, &c, &u,
		&i.PluginStateEnc, &i.PluginPublicJSON, &i.AwgHealthJSON, &healthAt)
	i.PortOverride = uint16(port.Int64)
	i.Enabled = en == 1
	if notAfter != 0 {
		i.CertNotAfter = fromUnix(notAfter)
	}
	i.CreatedAt, i.UpdatedAt = fromUnix(c), fromUnix(u)
	if healthAt != 0 {
		i.AwgHealthAt = fromUnix(healthAt)
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
	err := a.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO inbound (id, profile_id, node_id, port_override, tls_server_name_override, enabled, spec_version, state,
			   plugin_state_enc, plugin_public_json, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, 1, 'pending', ?, ?, ?, ?)`,
			i.ID, i.ProfileID, i.NodeID, accNullPort(i.PortOverride), i.TLSServerNameOverride, accBool(i.Enabled),
			i.PluginStateEnc, accPublicJSON(i.PluginPublicJSON), unix(i.CreatedAt), unix(i.CreatedAt)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM awg_retained_key WHERE profile_id = ? AND node_id = ?`, i.ProfileID, i.NodeID)
		return err
	})
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
	return a.tx(ctx, func(tx *sql.Tx) error {
		var profileID, nodeID, public string
		var enc []byte
		err := tx.QueryRowContext(ctx, `SELECT profile_id, node_id, plugin_state_enc, plugin_public_json FROM inbound WHERE id = ?`, id).
			Scan(&profileID, &nodeID, &enc, &public)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if retain != nil && len(enc) > 0 {
			kept, err := retain(enc, profileID, nodeID)
			if err != nil {
				return err
			}
			if len(kept) > 0 {
				if _, err := tx.ExecContext(ctx,
					`INSERT OR REPLACE INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at)
					 SELECT ?1, ?2, ?3, ?4, ?5 WHERE EXISTS (SELECT 1 FROM node WHERE id = ?2 AND state <> 'retired')`,
					profileID, nodeID, kept, accPublicJSON(public), unix(now)); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM inbound WHERE id = ?`, id)
		return err
	})
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
		var f AccessInboundFull
		var port sql.NullInt64
		var en int
		var notAfter, ic, iu, pc, pu, healthAt int64
		i, p, n := &f.Inbound, &f.Profile, &f.Node
		if err := rows.Scan(&i.ID, &i.ProfileID, &i.NodeID, &port, &i.TLSServerNameOverride, &en, &i.SpecVersion,
			&i.State, &i.LastError, &i.CertPinSHA256, &notAfter, &ic, &iu,
			&i.PluginStateEnc, &i.PluginPublicJSON, &i.AwgHealthJSON, &healthAt,
			&p.ID, &p.Protocol, &p.Name, &p.SettingsJSON, &p.SecretsEnc, &p.Version, &pc, &pu,
			&n.ID, &n.Name, &n.Address, &n.CountryCode, &n.Location, &n.Provider, &n.BandwidthMbps, &n.State); err != nil {
			return nil, err
		}
		i.PortOverride = uint16(port.Int64)
		i.Enabled = en == 1
		if notAfter != 0 {
			i.CertNotAfter = fromUnix(notAfter)
		}
		i.CreatedAt, i.UpdatedAt = fromUnix(ic), fromUnix(iu)
		if healthAt != 0 {
			i.AwgHealthAt = fromUnix(healthAt)
		}
		p.CreatedAt, p.UpdatedAt = fromUnix(pc), fromUnix(pu)
		out = append(out, f)
	}
	return out, rows.Err()
}

// AccessGroup is an access group: a named set of profiles.
type AccessGroup struct {
	ID, Name    string
	ProfileIDs  []string
	UserCount   int
	CreatedAt   time.Time
	DNSPresetID string // "" = the instance default
}

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
	err := a.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES (?, ?, ?, ?)`, g.ID, g.Name, unix(g.CreatedAt), accNullStr(g.DNSPresetID)); err != nil {
			return err
		}
		return accSetGroupProfiles(ctx, tx, g.ID, g.ProfileIDs)
	})
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

func accSetGroupProfiles(ctx context.Context, tx *sql.Tx, groupID string, profileIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_profile WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, pid := range profileIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_group_profile (group_id, profile_id) VALUES (?, ?)`, groupID, pid); err != nil {
			return err
		}
	}
	return nil
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
		`SELECT g.id, g.name, g.created_at, (SELECT count(*) FROM user u WHERE u.group_id = g.id), g.dns_preset_id
		 FROM user_group g WHERE (? = '' OR g.id = ?) ORDER BY g.name`, id, id)
	if err != nil {
		return nil, err
	}
	var out []AccessGroup
	idx := map[string]int{}
	for rows.Next() {
		var g AccessGroup
		var c int64
		var dns sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &c, &g.UserCount, &dns); err != nil {
			rows.Close()
			return nil, err
		}
		g.DNSPresetID = dns.String
		g.CreatedAt = fromUnix(c)
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

// UpdateGroup renames a group, replaces its profile set and/or its DNS preset (nil = unchanged, "" = none).
func (a Access) UpdateGroup(ctx context.Context, id string, name *string, profileIDs *[]string, dnsPresetID *string) error {
	err := a.tx(ctx, func(tx *sql.Tx) error {
		if name != nil {
			res, err := tx.ExecContext(ctx, `UPDATE user_group SET name = ? WHERE id = ?`, *name, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNotFound
			}
		} else {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_group WHERE id = ?`, id).Scan(&one); err != nil {
				return ErrNotFound
			}
		}
		if dnsPresetID != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE user_group SET dns_preset_id = ? WHERE id = ?`, accNullStr(*dnsPresetID), id); err != nil {
				return err
			}
		}
		if profileIDs != nil {
			return accSetGroupProfiles(ctx, tx, id, *profileIDs)
		}
		return nil
	})
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
	err := a.tx(ctx, func(tx *sql.Tx) error {
		if moveTo != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE user SET group_id = ? WHERE group_id = ?`, moveTo, id); err != nil {
				return err
			}
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE group_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAccessInUse
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM user_group WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		return nil
	})
	if accIsFK(err) {
		return ErrNotFound
	}
	return err
}
