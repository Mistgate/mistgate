package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Queries of the dns module (DNS presets) and the preset columns of users and groups
// (access_user.go, access_profile.go read and write those).

// DNSBuiltinDefaultID is the preset every install starts with as its default (Russia split).
const DNSBuiltinDefaultID = "dns_builtin_ru_split"

// dnsDefaultKey is the setting that holds the instance default preset id ("" or missing = the built-in one).
const dnsDefaultKey = "dns.default_preset"

// ErrDNSExists is returned when a preset name is already taken.
var ErrDNSExists = errors.New("store: dns preset name taken")

// DNS source names of the effective preset.
const (
	DNSSourceUser    = "user"
	DNSSourceGroup   = "group"
	DNSSourceDefault = "default"
)

// DNSPreset is a preset row. ServersJSON and SplitJSON are validated by the dns module before they get here.
type DNSPreset struct {
	ID, Name, Description  string
	Builtin, IPv4Only      bool
	SplitDirect            bool   // the split rules' domains bypass the VPN (Happ)
	Transport              string // preferred transport: plain, dot or doh
	ServersJSON, SplitJSON string
	CreatedAt, UpdatedAt   time.Time
}

// DNS is the query set of the dns module.
type DNS struct{ s *Store }

// DNS returns the dns module's queries.
func (s *Store) DNS() DNS { return DNS{s} }

const dnsCols = `id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, created_at, updated_at`

func scanDNSPreset(r rowScanner) (DNSPreset, error) {
	var p DNSPreset
	var b, v4, sd, c, u int64
	err := r.Scan(&p.ID, &p.Name, &p.Description, &b, &p.ServersJSON, &p.SplitJSON, &v4, &sd, &p.Transport, &c, &u)
	p.Builtin, p.IPv4Only, p.SplitDirect = b == 1, v4 == 1, sd == 1
	p.CreatedAt, p.UpdatedAt = fromUnix(c), fromUnix(u)
	return p, err
}

// List returns every preset: the built-ins in their fixed order (the sort column), then the custom ones by name.
func (d DNS) List(ctx context.Context) ([]DNSPreset, error) {
	rows, err := d.s.R.QueryContext(ctx, `SELECT `+dnsCols+` FROM dns_preset ORDER BY builtin DESC, sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSPreset
	for rows.Next() {
		p, err := scanDNSPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one preset, or ErrNotFound.
func (d DNS) Get(ctx context.Context, id string) (DNSPreset, error) {
	p, err := scanDNSPreset(d.s.R.QueryRowContext(ctx, `SELECT `+dnsCols+` FROM dns_preset WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Exists reports whether a preset id exists.
func (d DNS) Exists(ctx context.Context, id string) (bool, error) {
	var one int
	err := d.s.R.QueryRowContext(ctx, `SELECT 1 FROM dns_preset WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Create inserts a custom preset. ErrDNSExists on a name clash.
func (d DNS) Create(ctx context.Context, p DNSPreset) error {
	_, err := d.s.W.ExecContext(ctx,
		`INSERT INTO dns_preset (`+dnsCols+`) VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.ServersJSON, p.SplitJSON, accBool(p.IPv4Only), accBool(p.SplitDirect), p.Transport, unix(p.CreatedAt), unix(p.UpdatedAt))
	if accIsUnique(err) {
		return ErrDNSExists
	}
	return err
}

// Update replaces the editable fields of a preset (the built-ins too). ErrNotFound, ErrDNSExists.
func (d DNS) Update(ctx context.Context, p DNSPreset) error {
	res, err := d.s.W.ExecContext(ctx,
		`UPDATE dns_preset SET name = ?, description = ?, servers = ?, split = ?, ipv4_only = ?, split_direct = ?, preferred_transport = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, p.ServersJSON, p.SplitJSON, accBool(p.IPv4Only), accBool(p.SplitDirect), p.Transport, unix(p.UpdatedAt), p.ID)
	if accIsUnique(err) {
		return ErrDNSExists
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a preset and clears it from every user and group that used it, in one transaction.
// Refusing built-ins and the instance default is the caller's job. ErrNotFound when the id is unknown.
func (d DNS) Delete(ctx context.Context, id string) error {
	_, err := d.s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM dns_preset WHERE id = ?)`, id),
		Stmt{Query: `DELETE FROM dns_preset WHERE id = ?`, Args: []any{id}},
		Stmt{Query: `UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id = ?`, Args: []any{id}},
		Stmt{Query: `UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id = ?`, Args: []any{id}},
	)
	if errors.Is(err, errGuard) {
		return ErrNotFound
	}
	return err
}

// DefaultID returns the instance default preset id as stored ("" = the built-in default).
func (d DNS) DefaultID(ctx context.Context) (string, error) {
	v, err := d.s.Setting(ctx, dnsDefaultKey)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return strings.TrimSpace(v), err
}

// SetDefaultID stores the instance default ("" = the built-in default). The caller checks that it exists.
func (d DNS) SetDefaultID(ctx context.Context, id string) error {
	return d.s.SetSettings(ctx, map[string]string{dnsDefaultKey: id})
}

// UserCounts returns, per preset id, how many users it is effective for (direct, via group or default).
func (d DNS) UserCounts(ctx context.Context) (map[string]int, error) {
	l, err := d.Lookup(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.s.R.QueryContext(ctx,
		`SELECT coalesce(u.dns_preset_id, ''), coalesce(g.dns_preset_id, ''), count(*)
		 FROM user u JOIN user_group g ON g.id = u.group_id GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var own, grp string
		var n int
		if err := rows.Scan(&own, &grp, &n); err != nil {
			return nil, err
		}
		id, _, _ := l.Resolve(own, grp)
		out[id] += n
	}
	return out, rows.Err()
}

// UserRefs returns the preset ids set on a user and on the user's group ("" = none), or ErrNotFound.
func (d DNS) UserRefs(ctx context.Context, userID string) (own, group string, err error) {
	var o, g sql.NullString
	err = d.s.R.QueryRowContext(ctx,
		`SELECT u.dns_preset_id, g.dns_preset_id FROM user u JOIN user_group g ON g.id = u.group_id WHERE u.id = ?`, userID).Scan(&o, &g)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return o.String, g.String, err
}

// DNSLookup is what resolving an effective preset needs: the names of the existing presets and the
// instance default.
type DNSLookup struct {
	Names     map[string]string // preset id -> name
	DefaultID string            // the instance default that exists, else the built-in default
}

// Lookup reads the preset names and the instance default.
func (d DNS) Lookup(ctx context.Context) (DNSLookup, error) {
	rows, err := d.s.R.QueryContext(ctx, `SELECT id, name FROM dns_preset`)
	if err != nil {
		return DNSLookup{}, err
	}
	defer rows.Close()
	l := DNSLookup{Names: map[string]string{}}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return DNSLookup{}, err
		}
		l.Names[id] = name
	}
	if err := rows.Err(); err != nil {
		return DNSLookup{}, err
	}
	def, err := d.DefaultID(ctx)
	if err != nil {
		return DNSLookup{}, err
	}
	l.DefaultID = DNSBuiltinDefaultID
	if _, ok := l.Names[def]; ok {
		l.DefaultID = def
	}
	return l, nil
}

// DNSEffectiveData is the independent state needed to resolve a person's effective preset.
type DNSEffectiveData struct {
	OwnPresetID, GroupPresetID string
	DefaultID                  string
	Presets                    []DNSPreset
}

// DNSSubscriptionData also carries the per-node offers and this person's picks for a subscription page.
type DNSSubscriptionData struct {
	DNSEffectiveData
	NodeOptions map[string][]NodeDNSOption
	UserChoices map[string]UserNodeDNS
}

type dnsUserPresetIDs struct {
	Own, Group string
	Found      bool
}

func scanDNSUserPresetIDs(r rowScanner) (dnsUserPresetIDs, error) {
	var value dnsUserPresetIDs
	if err := r.Scan(&value.Own, &value.Group); err != nil {
		return dnsUserPresetIDs{}, err
	}
	value.Found = true
	return value, nil
}

func scanNodeDNSOffer(r rowScanner) (NodeDNSOption, error) {
	var value NodeDNSOption
	var isDefault int
	err := r.Scan(&value.NodeID, &value.PresetID, &value.Position, &isDefault)
	value.Default = isDefault == 1
	return value, err
}

func scanUserNodeDNS(r rowScanner) (UserNodeDNS, error) {
	var value UserNodeDNS
	err := r.Scan(&value.NodeID, &value.PresetID, &value.UpdatedMs)
	return value, err
}

func (d DNS) scanEffectiveData(ctx context.Context, userID string, withNode bool) (DNSEffectiveData, map[string][]NodeDNSOption, map[string]UserNodeDNS, error) {
	data := DNSEffectiveData{Presets: []DNSPreset{}, DefaultID: DNSBuiltinDefaultID}
	var presets []DNSPreset
	var presetIDs dnsUserPresetIDs
	var defaultID string
	var offerRows []NodeDNSOption
	var choiceRows []UserNodeDNS
	r := reads{}
	r.add(appendRows(&presets, scanDNSPreset), `SELECT `+dnsCols+` FROM dns_preset ORDER BY builtin DESC, sort, name`)
	r.add(maybeOneRow(&presetIDs, scanDNSUserPresetIDs), `SELECT coalesce(u.dns_preset_id, '') AS own_preset_id, coalesce(g.dns_preset_id, '') AS group_preset_id
		FROM user u JOIN user_group g ON g.id = u.group_id WHERE u.id = ?`, userID)
	r.add(maybeOneRow(&defaultID, scanString), `SELECT v FROM setting WHERE k = ?`, dnsDefaultKey)
	if withNode {
		r.add(appendRows(&offerRows, scanNodeDNSOffer), `SELECT node_id, preset_id, position, is_default FROM node_dns_option ORDER BY node_id, position, preset_id`)
		r.add(appendRows(&choiceRows, scanUserNodeDNS), `SELECT node_id, preset_id, updated_at FROM user_node_dns WHERE user_id = ?`, userID)
	}
	if err := r.run(ctx, d.s); err != nil {
		return DNSEffectiveData{}, nil, nil, err
	}
	if !presetIDs.Found {
		return DNSEffectiveData{}, nil, nil, ErrNotFound
	}
	data.Presets, data.OwnPresetID, data.GroupPresetID = presets, presetIDs.Own, presetIDs.Group
	if defaultID != "" {
		data.DefaultID = strings.TrimSpace(defaultID)
	}
	if data.DefaultID == "" {
		data.DefaultID = DNSBuiltinDefaultID
	}
	var offers map[string][]NodeDNSOption
	var choices map[string]UserNodeDNS
	if withNode {
		offers, choices = map[string][]NodeDNSOption{}, map[string]UserNodeDNS{}
		for _, row := range offerRows {
			offers[row.NodeID] = append(offers[row.NodeID], row)
		}
		for _, choice := range choiceRows {
			choice.UserID = userID
			choices[choice.NodeID] = choice
		}
	}
	return data, offers, choices, nil
}

// EffectiveData reads a person's preset and the instance default in one batch.
func (d DNS) EffectiveData(ctx context.Context, userID string) (DNSEffectiveData, error) {
	data, _, _, err := d.scanEffectiveData(ctx, userID, false)
	return data, err
}

// SubscriptionData reads a person's effective preset, node offers and choices in one batch.
func (d DNS) SubscriptionData(ctx context.Context, userID string) (DNSSubscriptionData, error) {
	data, options, choices, err := d.scanEffectiveData(ctx, userID, true)
	if err != nil {
		return DNSSubscriptionData{}, err
	}
	return DNSSubscriptionData{DNSEffectiveData: data, NodeOptions: options, UserChoices: choices}, nil
}

// Resolve applies user -> group -> instance default. A reference to a preset that no longer exists
// counts as unset.
func (l DNSLookup) Resolve(own, group string) (id, name, source string) {
	if _, ok := l.Names[own]; ok && own != "" {
		return own, l.Names[own], DNSSourceUser
	}
	if _, ok := l.Names[group]; ok && group != "" {
		return group, l.Names[group], DNSSourceGroup
	}
	return l.DefaultID, l.Names[l.DefaultID], DNSSourceDefault
}
