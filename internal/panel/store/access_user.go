package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// AccessUser is a user row with its group name and explicit node list.
type AccessUser struct {
	ID, Name, SubscriptionName, GroupID, GroupName string
	Disabled                                       bool
	Status                                         string // active | disabled | expired | limited (derived, see access.ComputeStatus)
	AppHapp, AppAmnezia                            bool
	AllNodes                                       bool
	QuotaBytes                                     uint64 // 0 = unlimited
	QuotaReset                                     string // none | day | week | month | rolling_month
	PeriodStart                                    time.Time
	UsedBytes                                      uint64
	ExpiresAt                                      time.Time // zero = never
	DeviceLimit                                    int
	SpeedLimitBps                                  uint64
	SubTokenHash, SubTokenEnc                      []byte
	LastSeenAt                                     time.Time
	LastNodeID                                     string
	CreatedAt                                      time.Time
	NodeIDs                                        []string // rows of user_node; meaningful while AllNodes is false
	DNSPresetID                                    string   // "" = inherit (group, then the instance default)
	GroupDNSPresetID                               string   // the user's group's preset, read-only here ("" = none)
}

const accUserCols = `u.id, u.name, u.subscription_name, u.group_id, g.name AS group_name, u.disabled, u.status, u.app_happ, u.app_amnezia, u.all_nodes,
	u.quota_bytes, u.quota_reset, u.period_start, u.used_bytes, u.expires_at, u.device_limit, u.speed_limit_bps,
	u.sub_token_hash, u.sub_token_enc, u.last_seen_at, u.last_node_id, u.created_at, u.dns_preset_id, g.dns_preset_id AS group_dns_preset_id`

const accUserFrom = ` FROM user u JOIN user_group g ON g.id = u.group_id `

func scanAccessUser(r interface{ Scan(...any) error }) (AccessUser, error) {
	var u AccessUser
	var dis, happ, amn, all int
	var quota, used, speed, period, seen, created int64
	var expires sql.NullInt64
	var lastNode, dns, gdns sql.NullString
	err := r.Scan(&u.ID, &u.Name, &u.SubscriptionName, &u.GroupID, &u.GroupName, &dis, &u.Status, &happ, &amn, &all,
		&quota, &u.QuotaReset, &period, &used, &expires, &u.DeviceLimit, &speed,
		&u.SubTokenHash, &u.SubTokenEnc, &seen, &lastNode, &created, &dns, &gdns)
	u.DNSPresetID, u.GroupDNSPresetID = dns.String, gdns.String
	u.Disabled, u.AppHapp, u.AppAmnezia, u.AllNodes = dis == 1, happ == 1, amn == 1, all == 1
	u.QuotaBytes, u.UsedBytes, u.SpeedLimitBps = uint64(quota), uint64(used), uint64(speed)
	u.PeriodStart = fromUnix(period)
	u.ExpiresAt = accTime(expires)
	u.LastSeenAt = accTime(sql.NullInt64{Int64: seen, Valid: true})
	u.LastNodeID = lastNode.String
	u.CreatedAt = fromUnix(created)
	return u, err
}

// AccessDevice is one device of a user: the implicit per-user device of subscription fetches (no HWID) or an
// AWG device that holds its own keys.
type AccessDevice struct {
	ID, UserID, Platform, Model, OSVersion string
	Implicit                               bool // hwid_hash IS NULL
	// AWG marks a device created by AddAWGDevice: a synthetic hwid_hash (AWGHwidHash) and first/last seen 0 =
	// "never connected" until the fleet records a handshake.
	AWG                                bool
	NoInitialSeen                      bool // credentials were created by a no-touch subscription view
	FirstSeenAt, LastSeenAt, CreatedAt time.Time
	Protocols                          []string // protocols with a live credential
}

// AccessCred is one device x protocol credential. ProfileID binds an AWG credential to its profile ("" for
// hysteria2); ConfigEpoch is the profile critical_epoch the device last received a config for.
type AccessCred struct {
	ID, DeviceID, UserID, Protocol string
	ProfileID                      string
	SecretEnc                      []byte // vault, AAD = ID
	DataJSON                       string
	ConfigEpoch                    int64
	CreatedAt                      time.Time
}

func accInsertUserStmt(u AccessUser) Stmt {
	return Stmt{Query: `INSERT INTO user (id, name, subscription_name, group_id, disabled, status, app_happ, app_amnezia, all_nodes, quota_bytes, quota_reset,
		   period_start, used_bytes, expires_at, device_limit, speed_limit_bps, sub_token_hash, sub_token_enc, created_at, dns_preset_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{u.ID, u.Name, u.SubscriptionName, u.GroupID, int64(accBool(u.Disabled)), u.Status, int64(accBool(u.AppHapp)), int64(accBool(u.AppAmnezia)), int64(accBool(u.AllNodes)),
			int64(u.QuotaBytes), u.QuotaReset, unix(u.PeriodStart), accNullUnix(u.ExpiresAt), int64(u.DeviceLimit), int64(u.SpeedLimitBps),
			u.SubTokenHash, u.SubTokenEnc, unix(u.CreatedAt), accNullStr(u.DNSPresetID)}}
}

func accSetUserNodeStmts(userID string, nodeIDs []string) []Stmt {
	stmts := []Stmt{{Query: `DELETE FROM user_node WHERE user_id = ?`, Args: []any{userID}}}
	for _, nodeID := range nodeIDs {
		stmts = append(stmts, Stmt{Query: `INSERT OR IGNORE INTO user_node (user_id, node_id) VALUES (?, ?)`, Args: []any{userID, nodeID}})
	}
	return stmts
}

func accInsertDeviceStmt(d AccessDevice) Stmt {
	var hwid any // NULL: the implicit device
	seen := unix(d.CreatedAt)
	if d.NoInitialSeen {
		seen = 0
	}
	switch {
	case d.AWG:
		hwid, seen = AWGHwidHash(d.ID), 0
	case !d.Implicit:
		hwid = []byte(d.ID)
	}
	return Stmt{Query: `INSERT INTO device (id, user_id, hwid_hash, platform, model, os_version, first_seen_at, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{d.ID, d.UserID, hwid, d.Platform, d.Model, d.OSVersion, seen, seen, unix(d.CreatedAt)}}
}

func accInsertCredStmts(creds []AccessCred) []Stmt {
	stmts := make([]Stmt, 0, len(creds))
	for _, c := range creds {
		stmts = append(stmts, Stmt{Query: `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, secret_enc, data_json, config_epoch, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{c.ID, c.DeviceID, c.UserID, c.Protocol, accNullStr(c.ProfileID), c.SecretEnc, c.DataJSON, c.ConfigEpoch, unix(c.CreatedAt)}})
	}
	return stmts
}

func accInsertDeviceStmts(d AccessDevice, creds []AccessCred) []Stmt {
	stmts := []Stmt{accInsertDeviceStmt(d)}
	return append(stmts, accInsertCredStmts(creds)...)
}

func accInsertDevice(ctx context.Context, tx *sql.Tx, d AccessDevice, creds []AccessCred) error {
	stmt := accInsertDeviceStmt(d)
	if _, err := tx.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
		return err
	}
	return accInsertCreds(ctx, tx, creds)
}

func accInsertCreds(ctx context.Context, tx *sql.Tx, creds []AccessCred) error {
	for _, stmt := range accInsertCredStmts(creds) {
		if _, err := tx.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
			return err
		}
	}
	return nil
}

// CreateUser inserts a user with its node list, implicit device and credentials in one transaction. The
// implicit device exists only to hold credentials: with none (a user of the Amnezia app only) it is not created
// and not counted against the device limit.
// ErrAccessExists on a name (or token) clash, ErrNotFound when the group or a node does not exist.
func (a Access) CreateUser(ctx context.Context, u AccessUser, dev AccessDevice, creds []AccessCred) error {
	stmts := []Stmt{accInsertUserStmt(u)}
	stmts = append(stmts, accSetUserNodeStmts(u.ID, u.NodeIDs)...)
	if len(creds) != 0 {
		stmts = append(stmts, accInsertDeviceStmts(dev, creds)...)
	}
	_, err := a.s.batch(ctx, stmts...)
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

// User returns one user, or ErrNotFound.
func (a Access) User(ctx context.Context, id string) (AccessUser, error) {
	us, err := a.UsersByIDs(ctx, []string{id})
	if err != nil {
		return AccessUser{}, err
	}
	if len(us) == 0 {
		return AccessUser{}, ErrNotFound
	}
	return us[0], nil
}

// UsersByIDs returns the existing users among ids, ordered by name.
func (a Access) UsersByIDs(ctx context.Context, ids []string) ([]AccessUser, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT `+accUserCols+accUserFrom+`WHERE u.id IN (SELECT value FROM json_each(?)) ORDER BY u.name`, accJSON(ids))
	if err != nil {
		return nil, err
	}
	return a.collectUsers(ctx, rows)
}

// UserByTokenHash finds the user of a subscription token hash (sha256), or ErrNotFound.
func (a Access) UserByTokenHash(ctx context.Context, hash []byte) (AccessUser, error) {
	results, err := a.s.batch(ctx,
		Stmt{Query: `SELECT ` + accUserCols + accUserFrom + `WHERE u.sub_token_hash = ?`, Args: []any{hash}, Returning: true},
		Stmt{Query: `SELECT node_id FROM user_node WHERE user_id = (SELECT id FROM user WHERE sub_token_hash = ?) ORDER BY node_id`, Args: []any{hash}, Returning: true},
	)
	if err != nil {
		return AccessUser{}, err
	}
	if len(results[0].Rows) == 0 {
		return AccessUser{}, ErrNotFound
	}
	u, err := scanAccessUser(batchRow(results[0].Rows[0]))
	if err != nil {
		return AccessUser{}, err
	}
	for _, row := range results[1].Rows {
		var nodeID string
		if err := batchRow(row).Scan(&nodeID); err != nil {
			return AccessUser{}, err
		}
		u.NodeIDs = append(u.NodeIDs, nodeID)
	}
	return u, nil
}

// HasUserWithTokenHash checks a subscription token without loading the user's view.
func (a Access) HasUserWithTokenHash(ctx context.Context, hash []byte) (bool, error) {
	var found int
	err := a.s.R.QueryRowContext(ctx, `SELECT 1 FROM user WHERE sub_token_hash = ?`, hash).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// collectUsers drains rows and attaches the explicit node lists.
func (a Access) collectUsers(ctx context.Context, rows *sql.Rows) ([]AccessUser, error) {
	var out []AccessUser
	idx := map[string]int{}
	var ids []string
	for rows.Next() {
		u, err := scanAccessUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[u.ID] = len(out)
		ids = append(ids, u.ID)
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	nrows, err := a.s.R.QueryContext(ctx,
		`SELECT user_id, node_id FROM user_node WHERE user_id IN (SELECT value FROM json_each(?)) ORDER BY node_id`, accJSON(ids))
	if err != nil {
		return nil, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var uid, nid string
		if err := nrows.Scan(&uid, &nid); err != nil {
			return nil, err
		}
		out[idx[uid]].NodeIDs = append(out[idx[uid]].NodeIDs, nid)
	}
	return out, nrows.Err()
}

// AccessUserQuery is a list request. Filter is "", "online", "expiring" or "over_quota".
type AccessUserQuery struct {
	Filter    string
	Query     string
	GroupID   string
	After     string // name after which the page starts ("" = first page)
	Offset    int    // rows skipped after After, for numbered pages
	Limit     int
	OnlineIDs []string // users currently online (fleet), for the "online" filter and its count
	Now       time.Time
}

// AccessUserCounts are the filter chip badges for one query, ignoring the filter itself.
type AccessUserCounts struct{ All, Online, Expiring, OverQuota int }

const accExpiringWindow = 7 * 24 * time.Hour

func accLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// ListUsers returns one page ordered by name (up to Limit users plus a flag telling whether more follow)
// and the chip counts. LIKE is case-insensitive for ASCII only.
func (a Access) ListUsers(ctx context.Context, q AccessUserQuery) ([]AccessUser, bool, AccessUserCounts, error) {
	conds, args := []string{"1 = 1"}, []any{}
	if q.GroupID != "" {
		conds = append(conds, "u.group_id = ?")
		args = append(args, q.GroupID)
	}
	if q.Query != "" {
		conds = append(conds, `(u.name LIKE ? ESCAPE '\' OR g.name LIKE ? ESCAPE '\')`)
		like := accLike(q.Query)
		args = append(args, like, like)
	}
	base := strings.Join(conds, " AND ")
	now, soon := unix(q.Now), unix(q.Now.Add(accExpiringWindow))
	online := accJSON(q.OnlineIDs)

	var counts AccessUserCounts
	err := a.s.R.QueryRowContext(ctx,
		`SELECT count(*),
		   coalesce(sum(u.id IN (SELECT value FROM json_each(?))), 0),
		   coalesce(sum(u.disabled = 0 AND u.expires_at IS NOT NULL AND u.expires_at > ? AND u.expires_at <= ?), 0),
		   coalesce(sum(u.quota_bytes > 0 AND u.used_bytes >= u.quota_bytes), 0)`+accUserFrom+`WHERE `+base,
		append([]any{online, now, soon}, args...)...).
		Scan(&counts.All, &counts.Online, &counts.Expiring, &counts.OverQuota)
	if err != nil {
		return nil, false, counts, err
	}

	lconds, largs := base, append([]any{}, args...)
	switch q.Filter {
	case "online":
		lconds += ` AND u.id IN (SELECT value FROM json_each(?))`
		largs = append(largs, online)
	case "expiring":
		lconds += ` AND u.disabled = 0 AND u.expires_at IS NOT NULL AND u.expires_at > ? AND u.expires_at <= ?`
		largs = append(largs, now, soon)
	case "over_quota":
		lconds += ` AND u.quota_bytes > 0 AND u.used_bytes >= u.quota_bytes`
	}
	if q.After != "" {
		lconds += ` AND u.name > ?`
		largs = append(largs, q.After)
	}
	limit := q.Limit
	if limit < 1 {
		limit = 50
	}
	largs = append(largs, limit+1, max(q.Offset, 0))
	rows, err := a.s.R.QueryContext(ctx, `SELECT `+accUserCols+accUserFrom+`WHERE `+lconds+` ORDER BY u.name LIMIT ? OFFSET ?`, largs...)
	if err != nil {
		return nil, false, counts, err
	}
	users, err := a.collectUsers(ctx, rows)
	if err != nil {
		return nil, false, counts, err
	}
	more := len(users) > limit
	if more {
		users = users[:limit]
	}
	return users, more, counts, nil
}

// UpdateUser writes every admin-editable column of u (matched by ID) and its status. setNodes also
// replaces the explicit node list with u.NodeIDs. Usage counters are left alone (the fleet increments them).
// ErrAccessExists on a name clash, ErrNotFound when the user, group or a node does not exist.
func (a Access) UpdateUser(ctx context.Context, u AccessUser, setNodes bool) error {
	stmts := []Stmt{
		guard(`EXISTS (SELECT 1 FROM user WHERE id = ?)`, u.ID),
		{Query: `UPDATE user SET name = ?, subscription_name = ?, group_id = ?, disabled = ?, status = ?, app_happ = ?, app_amnezia = ?, all_nodes = ?,
			   quota_bytes = ?, quota_reset = ?, period_start = ?, expires_at = ?, device_limit = ?, speed_limit_bps = ?,
			   dns_preset_id = ?
			 WHERE id = ?`,
			Args: []any{u.Name, u.SubscriptionName, u.GroupID, int64(accBool(u.Disabled)), u.Status, int64(accBool(u.AppHapp)), int64(accBool(u.AppAmnezia)), int64(accBool(u.AllNodes)),
				int64(u.QuotaBytes), u.QuotaReset, unix(u.PeriodStart), accNullUnix(u.ExpiresAt), int64(u.DeviceLimit), int64(u.SpeedLimitBps), accNullStr(u.DNSPresetID), u.ID}},
	}
	if setNodes {
		stmts = append(stmts, accSetUserNodeStmts(u.ID, u.NodeIDs)...)
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

// SetUsersDisabled flips the admin switch of several users.
func (a Access) SetUsersDisabled(ctx context.Context, ids []string, disabled bool) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE user SET disabled = ? WHERE id IN (SELECT value FROM json_each(?))`, accBool(disabled), accJSON(ids))
	return err
}

// SetUserStatus stores a recomputed status.
func (a Access) SetUserStatus(ctx context.Context, id, status string) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE user SET status = ? WHERE id = ?`, status, id)
	return err
}

// ResetUserPeriod starts a new quota period: usage back to zero, status as given.
func (a Access) ResetUserPeriod(ctx context.Context, id string, periodStart time.Time, status string) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE user SET period_start = ?, used_bytes = 0, status = ? WHERE id = ?`, unix(periodStart), status, id)
	return err
}

// SetSubToken replaces the subscription token of a user.
func (a Access) SetSubToken(ctx context.Context, id string, hash, enc []byte) error {
	res, err := a.s.W.ExecContext(ctx, `UPDATE user SET sub_token_hash = ?, sub_token_enc = ? WHERE id = ?`, hash, enc, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddUsage adds bytes to a user's usage in the current period. The fleet module does the same inside its
// stats transaction; this exists for callers that have no transaction of their own.
func (a Access) AddUsage(ctx context.Context, id string, bytes uint64) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE user SET used_bytes = used_bytes + ? WHERE id = ?`, int64(bytes), id)
	return err
}

// DeleteUsers deletes users (devices, credentials, node list and traffic rows cascade) and returns the
// number deleted.
func (a Access) DeleteUsers(ctx context.Context, ids []string) (int, error) {
	res, err := a.s.W.ExecContext(ctx, `DELETE FROM user WHERE id IN (SELECT value FROM json_each(?))`, accJSON(ids))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AccessUserState is what the status sweep needs about a user.
type AccessUserState struct {
	ID, Status, QuotaReset string
	Disabled               bool
	QuotaBytes, UsedBytes  uint64
	ExpiresAt, PeriodStart time.Time
}

// UserStates returns the state columns of the given users, or of all users when ids is nil.
func (a Access) UserStates(ctx context.Context, ids []string) ([]AccessUserState, error) {
	q := `SELECT id, status, quota_reset, disabled, quota_bytes, used_bytes, expires_at, period_start FROM user`
	var args []any
	if ids != nil {
		q += ` WHERE id IN (SELECT value FROM json_each(?))`
		args = append(args, accJSON(ids))
	}
	rows, err := a.s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessUserState
	for rows.Next() {
		var s AccessUserState
		var dis int
		var quota, used, period int64
		var exp sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Status, &s.QuotaReset, &dis, &quota, &used, &exp, &period); err != nil {
			return nil, err
		}
		s.Disabled, s.QuotaBytes, s.UsedBytes = dis == 1, uint64(quota), uint64(used)
		s.ExpiresAt, s.PeriodStart = accTime(exp), fromUnix(period)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---- usage and derived user data (reads of tables the fleet module writes) ----

// SubscriptionData reads the independent rows a subscription view needs in one store batch. The access
// rows are omitted for a user who is not active, because the view returns before it reads them.
type SubscriptionData struct {
	Up, Down       uint64
	Devices        []AccessDevice
	AWG            []AccessAWGDevice
	ImplicitDevice AccessDevice
	ImplicitCreds  []AccessCred
	ImplicitFound  bool
	Group          AccessGroup
	Inbounds       []AccessInboundFull
}

// implicitDeviceCredsSQL reads one live implicit device and all its live credentials. Its aliases are unique so the
// query can also be the trailing SELECT of a Returning batch.
const implicitDeviceCredsSQL = `
	SELECT d.id AS device_id, d.user_id AS user_id, coalesce(d.platform, '') AS platform, coalesce(d.model, '') AS model,
		coalesce(d.os_version, '') AS os_version, coalesce(d.first_seen_at, 0) AS first_seen_at, coalesce(d.last_seen_at, 0) AS last_seen_at,
		d.created_at AS device_created_at, coalesce(c.id, '') AS credential_id, coalesce(c.protocol, '') AS protocol,
		coalesce(c.profile_id, '') AS profile_id, coalesce(c.secret_enc, X'') AS secret_enc, coalesce(c.data_json, '') AS data_json,
		coalesce(c.config_epoch, 0) AS config_epoch, coalesce(c.created_at, 0) AS credential_created_at
	FROM device d LEFT JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL
	WHERE d.user_id = ? AND d.revoked_at IS NULL AND d.hwid_hash IS NULL
	ORDER BY c.protocol, c.profile_id, c.id`

type implicitDeviceCredRowScanner interface {
	Scan(dest ...any) error
}

type implicitDeviceCredsSnapshot struct {
	Device AccessDevice
	Creds  []AccessCred
	Found  bool
}

func (s *implicitDeviceCredsSnapshot) scan(row implicitDeviceCredRowScanner) error {
	device, cred, hasCred, err := scanImplicitDeviceCred(row)
	if err != nil {
		return err
	}
	if !s.Found {
		s.Device, s.Found = device, true
	}
	if hasCred {
		if !slices.Contains(s.Device.Protocols, cred.Protocol) {
			s.Device.Protocols = append(s.Device.Protocols, cred.Protocol)
		}
		s.Creds = append(s.Creds, cred)
	}
	return nil
}

func scanImplicitDeviceCred(row implicitDeviceCredRowScanner) (AccessDevice, AccessCred, bool, error) {
	var device AccessDevice
	var first, last, deviceCreated, configEpoch, credCreated int64
	var id, protocol, profile, data string
	var secret []byte
	if err := row.Scan(&device.ID, &device.UserID, &device.Platform, &device.Model, &device.OSVersion,
		&first, &last, &deviceCreated, &id, &protocol, &profile, &secret, &data, &configEpoch, &credCreated); err != nil {
		return AccessDevice{}, AccessCred{}, false, err
	}
	device.Implicit = true
	device.FirstSeenAt, device.LastSeenAt, device.CreatedAt = fromUnix(first), fromUnix(last), fromUnix(deviceCreated)
	if id == "" {
		return device, AccessCred{}, false, nil
	}
	cred := AccessCred{ID: id, DeviceID: device.ID, UserID: device.UserID, Protocol: protocol, ProfileID: profile,
		SecretEnc: secret, DataJSON: data, ConfigEpoch: configEpoch, CreatedAt: fromUnix(credCreated)}
	return device, cred, true, nil
}

func (a Access) SubscriptionData(ctx context.Context, userID, groupID string, since time.Time, active bool) (SubscriptionData, error) {
	stmts := []Stmt{
		{Query: `SELECT coalesce(sum(bytes_up), 0) AS bytes_up, coalesce(sum(bytes_down), 0) AS bytes_down FROM traffic_bucket WHERE user_id = ? AND hour_start >= ?`, Args: []any{userID, unix(since)}, Returning: true},
		{Query: `SELECT d.id, d.user_id, CASE WHEN d.hwid_hash IS NULL THEN 1 ELSE 0 END AS implicit, d.platform, d.model, d.os_version,
			coalesce(d.first_seen_at, 0) AS first_seen_at, coalesce(d.last_seen_at, 0) AS last_seen_at, d.created_at,
			coalesce((SELECT group_concat(DISTINCT c.protocol) FROM device_credential c WHERE c.device_id = d.id AND c.revoked_at IS NULL), '') AS protocols
			FROM device d WHERE d.user_id = ? AND d.revoked_at IS NULL AND ` + accDeviceLive + ` ORDER BY d.created_at, d.id`, Args: []any{userID}, Returning: true},
		{Query: `SELECT d.id, d.user_id, d.platform, d.model, d.os_version, coalesce(d.first_seen_at, 0) AS first_seen_at, coalesce(d.last_seen_at, 0) AS last_seen_at, d.created_at,
			p.id AS profile_id, p.name AS profile_name, p.settings_json AS profile_settings_json, c.id AS credential_id, c.data_json, c.secret_enc, ap.public_key, ap.idx, c.config_epoch, p.critical_epoch, coalesce(c.dns_sig, '') AS dns_sig
			FROM device d
			JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
			JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
			JOIN profile p ON p.id = c.profile_id
			WHERE d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL AND d.user_id = ? ORDER BY d.created_at, d.id`, Args: []any{userID}, Returning: true},
	}
	if active {
		stmts = append(stmts,
			Stmt{Query: implicitDeviceCredsSQL, Args: []any{userID}, Returning: true},
			Stmt{Query: `SELECT g.id, g.name, g.created_at, (SELECT count(*) FROM user u WHERE u.group_id = g.id) AS user_count, coalesce(g.dns_preset_id, '') AS dns_preset_id, g.color FROM user_group g WHERE g.id = ?`, Args: []any{groupID}, Returning: true},
			Stmt{Query: `SELECT profile_id FROM user_group_profile WHERE group_id = ? ORDER BY profile_id`, Args: []any{groupID}, Returning: true},
			Stmt{Query: `SELECT i.id AS inbound_id, i.profile_id AS inbound_profile_id, i.node_id AS inbound_node_id, coalesce(i.port_override, 0) AS port_override,
				i.tls_server_name_override, i.enabled, i.spec_version, i.state, i.last_error, i.cert_pin_sha256,
				coalesce(i.cert_not_after, 0) AS cert_not_after, coalesce(i.created_at, 0) AS inbound_created_at, coalesce(i.updated_at, 0) AS inbound_updated_at,
				i.plugin_state_enc, i.plugin_public_json, i.awg_health_json, coalesce(i.awg_health_at, 0) AS awg_health_at,
				p.id AS profile_id, p.protocol, p.name AS profile_name, p.settings_json AS profile_settings_json, p.secrets_enc AS profile_secrets_enc,
				p.version AS profile_version, p.created_at AS profile_created_at, p.updated_at AS profile_updated_at,
				n.id AS node_id, n.name AS node_name, n.address AS node_address, n.country_code, n.location, n.provider, n.bandwidth_mbps, n.state AS node_state
				FROM inbound i JOIN profile p ON p.id = i.profile_id JOIN node n ON n.id = i.node_id
				WHERE n.state <> 'retired' ORDER BY i.created_at, i.rowid`, Returning: true},
		)
	}
	results, err := a.s.batch(ctx, stmts...)
	if err != nil {
		return SubscriptionData{}, err
	}
	var out SubscriptionData
	if len(results[0].Rows) != 1 {
		return out, errors.New("store: subscription usage batch returned no row")
	}
	var up, down int64
	if err := (batchRow(results[0].Rows[0])).Scan(&up, &down); err != nil {
		return out, err
	}
	out.Up, out.Down = uint64(up), uint64(down)
	for _, row := range results[1].Rows {
		var d AccessDevice
		var implicit int
		var first, last, created int64
		var protocols string
		if err := (batchRow(row)).Scan(&d.ID, &d.UserID, &implicit, &d.Platform, &d.Model, &d.OSVersion, &first, &last, &created, &protocols); err != nil {
			return SubscriptionData{}, err
		}
		d.Implicit = implicit == 1
		d.FirstSeenAt, d.LastSeenAt, d.CreatedAt = fromUnix(first), fromUnix(last), fromUnix(created)
		if protocols != "" {
			d.Protocols = strings.Split(protocols, ",")
		}
		out.Devices = append(out.Devices, d)
	}
	for _, row := range results[2].Rows {
		var d AccessAWGDevice
		var first, last, created int64
		var sig string
		if err := (batchRow(row)).Scan(&d.ID, &d.UserID, &d.Platform, &d.Model, &d.OSVersion, &first, &last, &created,
			&d.ProfileID, &d.ProfileName, &d.ProfileSettingsJSON, &d.CredID, &d.DataJSON, &d.SecretEnc, &d.PublicKey, &d.Idx,
			&d.ConfigEpoch, &d.CriticalEpoch, &sig); err != nil {
			return SubscriptionData{}, err
		}
		if sig != "" {
			_ = json.Unmarshal([]byte(sig), &d.DNSSig)
		}
		d.AWG, d.Protocols = true, []string{"awg"}
		if first != 0 {
			d.FirstSeenAt = fromUnix(first)
		}
		if last != 0 {
			d.LastSeenAt = fromUnix(last)
		}
		d.CreatedAt = fromUnix(created)
		out.AWG = append(out.AWG, d)
	}
	if !active {
		return out, nil
	}
	var implicit implicitDeviceCredsSnapshot
	for _, row := range results[3].Rows {
		if err := implicit.scan(batchRow(row)); err != nil {
			return SubscriptionData{}, err
		}
	}
	out.ImplicitDevice, out.ImplicitCreds, out.ImplicitFound = implicit.Device, implicit.Creds, implicit.Found
	if len(results[4].Rows) != 1 {
		return SubscriptionData{}, ErrNotFound
	}
	var groupCreated int64
	if err := (batchRow(results[4].Rows[0])).Scan(&out.Group.ID, &out.Group.Name, &groupCreated, &out.Group.UserCount, &out.Group.DNSPresetID, &out.Group.Color); err != nil {
		return SubscriptionData{}, err
	}
	out.Group.CreatedAt = fromUnix(groupCreated)
	for _, row := range results[5].Rows {
		var profileID string
		if err := (batchRow(row)).Scan(&profileID); err != nil {
			return SubscriptionData{}, err
		}
		out.Group.ProfileIDs = append(out.Group.ProfileIDs, profileID)
	}
	for _, row := range results[6].Rows {
		var f AccessInboundFull
		var port, enabled int
		var notAfter, ic, iu, healthAt, pc, pu int64
		i, p, n := &f.Inbound, &f.Profile, &f.Node
		if err := (batchRow(row)).Scan(&i.ID, &i.ProfileID, &i.NodeID, &port, &i.TLSServerNameOverride, &enabled, &i.SpecVersion,
			&i.State, &i.LastError, &i.CertPinSHA256, &notAfter, &ic, &iu, &i.PluginStateEnc, &i.PluginPublicJSON, &i.AwgHealthJSON, &healthAt,
			&p.ID, &p.Protocol, &p.Name, &p.SettingsJSON, &p.SecretsEnc, &p.Version, &pc, &pu,
			&n.ID, &n.Name, &n.Address, &n.CountryCode, &n.Location, &n.Provider, &n.BandwidthMbps, &n.State); err != nil {
			return SubscriptionData{}, err
		}
		i.PortOverride, i.Enabled = uint16(port), enabled == 1
		if notAfter != 0 {
			i.CertNotAfter = fromUnix(notAfter)
		}
		i.CreatedAt, i.UpdatedAt = fromUnix(ic), fromUnix(iu)
		if healthAt != 0 {
			i.AwgHealthAt = fromUnix(healthAt)
		}
		p.CreatedAt, p.UpdatedAt = fromUnix(pc), fromUnix(pu)
		out.Inbounds = append(out.Inbounds, f)
	}
	return out, nil
}

// ImplicitDeviceCreds returns the live implicit device and its live credentials in one read.
func (a Access) ImplicitDeviceCreds(ctx context.Context, userID string) (AccessDevice, []AccessCred, error) {
	rows, err := a.s.R.QueryContext(ctx, implicitDeviceCredsSQL, userID)
	if err != nil {
		return AccessDevice{}, nil, err
	}
	defer rows.Close()
	var snapshot implicitDeviceCredsSnapshot
	for rows.Next() {
		if err := snapshot.scan(rows); err != nil {
			return AccessDevice{}, nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return AccessDevice{}, nil, err
	}
	if !snapshot.Found {
		return AccessDevice{}, nil, ErrNotFound
	}
	return snapshot.Device, snapshot.Creds, nil
}

// UsersProtocolsSince returns, per user, the protocols with traffic since the given time.
func (a Access) UsersProtocolsSince(ctx context.Context, ids []string, since time.Time) (map[string][]string, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT DISTINCT user_id, protocol FROM traffic_bucket
		 WHERE user_id IN (SELECT value FROM json_each(?)) AND hour_start >= ? ORDER BY user_id, protocol`, accJSON(ids), unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var uid, proto string
		if err := rows.Scan(&uid, &proto); err != nil {
			return nil, err
		}
		out[uid] = append(out[uid], proto)
	}
	return out, rows.Err()
}

// DeviceCounts returns the number of devices per user that count against the device limit: not revoked and
// holding at least one live credential (a device without any is an empty shell, e.g. the implicit device of a
// user of the Amnezia app only, created by an older version).
func (a Access) DeviceCounts(ctx context.Context, ids []string) (map[string]int, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT d.user_id, count(*) FROM device d
		 WHERE d.revoked_at IS NULL AND `+accDeviceLive+` AND d.user_id IN (SELECT value FROM json_each(?)) GROUP BY d.user_id`, accJSON(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var uid string
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			return nil, err
		}
		out[uid] = n
	}
	return out, rows.Err()
}

// DailyTraffic returns bytes (up + down) per UTC day since `from`, keyed by the day's Unix start.
func (a Access) DailyTraffic(ctx context.Context, userID string, from time.Time) (map[int64]uint64, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT hour_start / 86400 * 86400, sum(bytes_up + bytes_down) FROM traffic_bucket
		 WHERE user_id = ? AND hour_start >= ? GROUP BY 1`, userID, unix(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]uint64{}
	for rows.Next() {
		var day, b int64
		if err := rows.Scan(&day, &b); err != nil {
			return nil, err
		}
		out[day] = uint64(b)
	}
	return out, rows.Err()
}

// AccessNodeTraffic is a user's traffic on one node and protocol.
type AccessNodeTraffic struct {
	NodeID, NodeName, Protocol string
	Bytes                      uint64
}

// NodeTraffic returns a user's traffic per node and protocol since the given time.
func (a Access) NodeTraffic(ctx context.Context, userID string, since time.Time) ([]AccessNodeTraffic, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT b.node_id, n.name, b.protocol, sum(b.bytes_up + b.bytes_down)
		 FROM traffic_bucket b JOIN node n ON n.id = b.node_id
		 WHERE b.user_id = ? AND b.hour_start >= ? GROUP BY b.node_id, b.protocol ORDER BY 4 DESC`, userID, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessNodeTraffic
	for rows.Next() {
		var t AccessNodeTraffic
		var b int64
		if err := rows.Scan(&t.NodeID, &t.NodeName, &t.Protocol, &b); err != nil {
			return nil, err
		}
		t.Bytes = uint64(b)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- devices and credentials ----

// accDeviceLive is the SQL condition "device d holds a live credential".
const accDeviceLive = `EXISTS (SELECT 1 FROM device_credential c WHERE c.device_id = d.id AND c.revoked_at IS NULL)`

// Devices returns the non-revoked devices of a user that hold a live credential, with their credential
// protocols.
func (a Access) Devices(ctx context.Context, userID string) ([]AccessDevice, error) {
	return a.devices(ctx, userID, true)
}

func (a Access) devices(ctx context.Context, userID string, withCredsOnly bool) ([]AccessDevice, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT d.id, d.user_id, d.hwid_hash IS NULL, d.platform, d.model, d.os_version, d.first_seen_at, d.last_seen_at, d.created_at,
		   coalesce((SELECT group_concat(DISTINCT c.protocol) FROM device_credential c WHERE c.device_id = d.id AND c.revoked_at IS NULL), '')
		 FROM device d WHERE d.user_id = ? AND d.revoked_at IS NULL AND (? = 0 OR `+accDeviceLive+`) ORDER BY d.created_at, d.id`,
		userID, accBool(withCredsOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessDevice
	for rows.Next() {
		var d AccessDevice
		var impl int
		var first, last, created int64
		var protos string
		if err := rows.Scan(&d.ID, &d.UserID, &impl, &d.Platform, &d.Model, &d.OSVersion, &first, &last, &created, &protos); err != nil {
			return nil, err
		}
		d.Implicit = impl == 1
		d.FirstSeenAt, d.LastSeenAt, d.CreatedAt = fromUnix(first), fromUnix(last), fromUnix(created)
		if protos != "" {
			d.Protocols = strings.Split(protos, ",")
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ImplicitDevice returns the live implicit device of a user, or ErrNotFound.
func (a Access) ImplicitDevice(ctx context.Context, userID string) (AccessDevice, error) {
	ds, err := a.devices(ctx, userID, false) // also an empty one: a second implicit device cannot be inserted
	if err != nil {
		return AccessDevice{}, err
	}
	for _, d := range ds {
		if d.Implicit {
			return d, nil
		}
	}
	return AccessDevice{}, ErrNotFound
}

// TouchDevice sets last_seen_at unless it is already newer than `not`: a throttle for write-on-read.
func (a Access) TouchDevice(ctx context.Context, id string, now, not time.Time) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE device SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?`, unix(now), id, unix(not))
	return err
}

// AddDevice inserts a device with its credentials (ErrAccessExists on a live implicit device race).
func (a Access) AddDevice(ctx context.Context, d AccessDevice, creds []AccessCred) error {
	_, err := a.s.batch(ctx, accInsertDeviceStmts(d, creds)...)
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

// AddCreds inserts credentials for an existing device (ErrAccessExists when one is already live).
func (a Access) AddCreds(ctx context.Context, creds []AccessCred) error {
	_, err := a.s.batch(ctx, accInsertCredStmts(creds)...)
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

// RevokeDevice soft-deletes a live device and its credentials and returns its user id; ErrNotFound when
// the device does not exist or is already revoked.
func (a Access) RevokeDevice(ctx context.Context, id string, now time.Time) (string, error) {
	results, err := a.s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM device WHERE id = ? AND revoked_at IS NULL)`, id),
		Stmt{Query: `UPDATE device SET revoked_at = ? WHERE id = ?`, Args: []any{unix(now), id}},
		// The tunnel addresses of its AWG credentials go into quarantine (released_at > 0).
		Stmt{Query: `UPDATE awg_peer SET released_at = ? WHERE released_at = 0 AND credential_id IN
		   (SELECT id FROM device_credential WHERE device_id = ? AND revoked_at IS NULL)`, Args: []any{unix(now), id}},
		Stmt{Query: `UPDATE device_credential SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`, Args: []any{unix(now), id}},
		Stmt{Query: `SELECT user_id FROM device WHERE id = ?`, Args: []any{id}, Returning: true},
	)
	if errors.Is(err, errGuard) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if len(results[4].Rows) == 0 {
		return "", ErrNotFound
	}
	var userID string
	if err := batchRow(results[4].Rows[0]).Scan(&userID); err != nil {
		return "", err
	}
	return userID, nil
}

// AccessDesiredCred is a live credential that may go to a node: its user is active, belongs to a group
// with the inbound's profile and has the node selected. The caller still checks the app toggles against
// the protocol's Clients().
type AccessDesiredCred struct {
	InboundID, CredID, UserID, DeviceID, Protocol, DataJSON string
	SpeedLimitBps                                           uint64
	ExpiresAt                                               time.Time
	AppHapp, AppAmnezia                                     bool
}

// DesiredCreds returns the candidate credentials of every enabled inbound of a node.
func (a Access) DesiredCreds(ctx context.Context, nodeID string) ([]AccessDesiredCred, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT i.id, c.id, c.user_id, c.device_id, c.protocol, c.data_json, u.speed_limit_bps, u.expires_at, u.app_happ, u.app_amnezia
		 FROM inbound i
		 JOIN profile p ON p.id = i.profile_id
		 JOIN user_group_profile gp ON gp.profile_id = p.id
		 JOIN user u ON u.group_id = gp.group_id AND u.status = 'active'
		      AND (u.all_nodes = 1 OR EXISTS (SELECT 1 FROM user_node un WHERE un.user_id = u.id AND un.node_id = i.node_id))
		 JOIN device_credential c ON c.user_id = u.id AND c.protocol = p.protocol AND c.revoked_at IS NULL
		      AND (c.profile_id IS NULL OR c.profile_id = i.profile_id)
		 JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL
		 WHERE i.node_id = ? AND i.enabled = 1
		 ORDER BY i.id, c.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessDesiredCred
	for rows.Next() {
		var c AccessDesiredCred
		var speed int64
		var exp sql.NullInt64
		var happ, amn int
		if err := rows.Scan(&c.InboundID, &c.CredID, &c.UserID, &c.DeviceID, &c.Protocol, &c.DataJSON, &speed, &exp, &happ, &amn); err != nil {
			return nil, err
		}
		c.SpeedLimitBps, c.ExpiresAt, c.AppHapp, c.AppAmnezia = uint64(speed), accTime(exp), happ == 1, amn == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetUserExpiry sets a user's term end (zero = never).
func (a Access) SetUserExpiry(ctx context.Context, id string, t time.Time) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE user SET expires_at = ? WHERE id = ?`, accNullUnix(t), id)
	return err
}
