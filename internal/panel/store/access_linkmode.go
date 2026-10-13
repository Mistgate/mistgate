package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The device limit for the apps that take the subscription link: what a request that has no device yet is told before any
// write (LinkRefusal), the owner's block and unblock, the switch to STRICT, and the sweep of old revoked rows.

// ErrCannotBlock is returned when the device cannot be blocked: only a link device that reported a HWID has a hash a block
// can name (the shared device and an AmneziaVPN key have none).
var ErrCannotBlock = errors.New("store: this device cannot be blocked")

// LinkRefusal is what the snapshot of a subscription fetch already says about a request whose device does not exist yet,
// decided without a write: ErrAccessBlocked, ErrNoSharedDevice (STRICT, no HWID), or *AccessLimitError (ENFORCE and STRICT:
// the user is at the limit and no silent device could give its place away). nil: the store has to decide (a device is there,
// has room, or may take a silent one's place; TRACK always goes there). It is the first half of linkDeviceState.birth on the
// data SubscriptionData read, so a refused request costs the one read batch and nothing else. limit is the user's device
// limit. The rule of a place and of replacing one is birth's; a snapshot taken before a writer changed it is corrected there.
func (d SubscriptionData) LinkRefusal(limit int, hasHWID bool, now time.Time) error {
	if d.Device.ID != "" {
		return nil // it is there: only filled in
	}
	if d.Blocked {
		return ErrAccessBlocked
	}
	if d.LinkMode == LinkModeStrict && !hasHWID {
		return ErrNoSharedDevice
	}
	if d.LinkMode == LinkModeTrack {
		return nil
	}
	ceiling := linkCeiling(d.LinkMode, limit, hasHWID)
	if ceiling == 0 || len(d.Devices) < ceiling {
		return nil
	}
	if hasHWID {
		cutoff := now.Add(-LinkEvictAfter)
		sharedHere := false
		for _, dev := range d.Devices {
			if dev.Kind == DeviceKindLink && dev.LastSeenAt.Before(cutoff) {
				return nil // birth takes the oldest of them
			}
			sharedHere = sharedHere || dev.Kind == DeviceKindLink && dev.Implicit
		}
		if sharedHere && !hasHWIDDevice(d.Devices) {
			return nil // the first HWID takes the shared device over and spends no place
		}
	}
	return &AccessLimitError{Used: len(d.Devices), Limit: ceiling, Mode: d.LinkMode}
}

// hasHWIDDevice: some link device in the list reported a HWID (it is not the shared one).
func hasHWIDDevice(devs []AccessDevice) bool {
	for _, dev := range devs {
		if dev.Kind == DeviceKindLink && !dev.Implicit {
			return true
		}
	}
	return false
}

// BlockDevice revokes a live link device that reported a HWID, like RevokeDevice, and marks it blocked: the hash is not born
// again (ErrAccessBlocked from EnsureLinkDevice) until UnblockDevice. It returns the device's user. ErrNotFound when there is
// no live device, ErrCannotBlock for the shared device and for a key.
func (a Access) BlockDevice(ctx context.Context, id string, now time.Time) (string, error) {
	var kind string
	var shared, live bool
	err := a.s.R.QueryRowContext(ctx, `SELECT kind, hwid_hash IS NULL, revoked_at IS NULL FROM device WHERE id = ?`, id).Scan(&kind, &shared, &live)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !live) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if kind != DeviceKindLink || shared {
		return "", ErrCannotBlock
	}
	return a.revokeDevice(ctx, id, now,
		`EXISTS (SELECT 1 FROM device WHERE id = ? AND revoked_at IS NULL AND kind = 'link' AND NOT (hwid_hash IS NULL))`,
		Stmt{Query: `UPDATE device SET blocked_at = ? WHERE id = ?`, Args: []any{unix(now), id}})
}

// UnblockDevice lets the hash of a blocked device register again and returns its user; ErrNotFound when the device is not
// blocked. The device itself stays revoked: its app gets a device and keys of its own on the next fetch.
func (a Access) UnblockDevice(ctx context.Context, id string) (string, error) {
	results, err := a.s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM device WHERE id = ? AND blocked_at IS NOT NULL)`, id),
		Stmt{Query: `UPDATE device SET blocked_at = NULL WHERE id = ?`, Args: []any{id}},
		Stmt{Query: `SELECT user_id FROM device WHERE id = ?`, Args: []any{id}, Returning: true})
	if errors.Is(err, errGuard) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	rows := results[len(results)-1].Rows
	if len(rows) == 0 {
		return "", ErrNotFound
	}
	var userID string
	if err := batchRow(rows[0]).Scan(&userID); err != nil {
		return "", err
	}
	return userID, nil
}

// AccessBlockedDevice is a device the owner blocked. Fingerprint is the first 6 hex characters of its hash: the HWID itself
// is never stored.
type AccessBlockedDevice struct {
	ID, Platform, Model, Client, Fingerprint string
	BlockedAt                                time.Time
}

// BlockedDevices lists the blocked devices of a user, the newest block first.
func (a Access) BlockedDevices(ctx context.Context, userID string) ([]AccessBlockedDevice, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT id, platform, model, client, lower(hex(substr(hwid_hash, 1, 3))), blocked_at
		FROM device WHERE user_id = ? AND blocked_at IS NOT NULL ORDER BY blocked_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessBlockedDevice
	for rows.Next() {
		var d AccessBlockedDevice
		var at int64
		if err := rows.Scan(&d.ID, &d.Platform, &d.Model, &d.Client, &d.Fingerprint, &at); err != nil {
			return nil, err
		}
		d.BlockedAt = fromUnix(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Device returns a live (not revoked) device, with or without credentials; ErrNotFound when there is none.
func (a Access) Device(ctx context.Context, id string) (AccessDevice, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT d.id, d.user_id, CASE WHEN d.hwid_hash IS NULL THEN 1 ELSE 0 END AS implicit, d.kind, d.platform, d.model, d.os_version, d.client, d.first_seen_at, d.last_seen_at, d.created_at,
		   coalesce((SELECT group_concat(DISTINCT c.protocol) FROM device_credential c WHERE c.device_id = d.id AND c.revoked_at IS NULL), '')
		 FROM device d WHERE d.id = ? AND d.revoked_at IS NULL`, id)
	if err != nil {
		return AccessDevice{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return AccessDevice{}, errors.Join(rows.Err(), ErrNotFound)
	}
	return scanAccessDevice(rows)
}

// SaveSubscriptionSettings stores settings (kv) and the mode of the device limit ("" = leave it) in one batch. Switching to
// LinkModeStrict revokes every live shared link device in the same batch, like RevokeDevice does for one (the device, the
// quarantine of its AmneziaWG addresses, its credentials), so that no fetch can find the mode and a shared device together:
// one that decided on the old mode fails linkModeGuard and repeats. revoked is how many people lost a shared device (STRICT only).
func (s *Store) SaveSubscriptionSettings(ctx context.Context, kv map[string]string, mode string, now time.Time) (revoked int, err error) {
	stmts := make([]Stmt, 0, len(kv)+6)
	strict := mode == LinkModeStrict
	if strict { // read before the revoke below
		stmts = append(stmts, Stmt{Query: `SELECT count(DISTINCT d.user_id) AS n FROM device d WHERE d.hwid_hash IS NULL AND d.kind = 'link' AND d.revoked_at IS NULL AND ` + accDeviceLive, Returning: true})
	}
	for k, v := range kv {
		stmts = append(stmts, Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, Args: []any{k, v}})
	}
	if mode != "" {
		stmts = append(stmts, Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, Args: []any{SettingLinkDeviceLimit, mode}})
	}
	if strict {
		// Peers and credentials first: they find their device by the device still being live.
		const shared = `(SELECT id FROM device WHERE hwid_hash IS NULL AND kind = 'link' AND revoked_at IS NULL)`
		stmts = append(stmts,
			Stmt{Query: `UPDATE awg_peer SET released_at = ? WHERE released_at = 0 AND credential_id IN
				(SELECT id FROM device_credential WHERE revoked_at IS NULL AND device_id IN ` + shared + `)`, Args: []any{unix(now)}},
			Stmt{Query: `UPDATE device_credential SET revoked_at = ? WHERE revoked_at IS NULL AND device_id IN ` + shared, Args: []any{unix(now)}},
			Stmt{Query: `UPDATE device SET revoked_at = ? WHERE hwid_hash IS NULL AND kind = 'link' AND revoked_at IS NULL`, Args: []any{unix(now)}})
	}
	results, err := s.batch(ctx, stmts...)
	if err != nil {
		return 0, err
	}
	if !strict {
		return 0, nil
	}
	if rows := results[0].Rows; len(rows) == 1 {
		err = batchRow(rows[0]).Scan(&revoked)
	}
	return revoked, err
}

// SharedLinkUsers counts the people who have a live shared link device with credentials: the ones STRICT turns away.
func (a Access) SharedLinkUsers(ctx context.Context) (int, error) {
	var n int
	err := a.s.R.QueryRowContext(ctx, `SELECT count(DISTINCT d.user_id) FROM device d
		WHERE d.hwid_hash IS NULL AND d.kind = 'link' AND d.revoked_at IS NULL AND `+accDeviceLive).Scan(&n)
	return n, err
}

// HasUsers reports whether the panel has any user.
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var found bool
	err := s.R.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user)`).Scan(&found)
	return found, err
}

// PruneRevokedDevices deletes devices revoked before cutoff and not blocked (a block is kept for good), at most batch rows
// at a time, until none is left or ctx ends. A device row is referenced by its credentials, which cascade (and their AmneziaWG
// peers with them); a device that still holds a live credential, which a revoke never leaves, is skipped. It returns how many
// rows it removed.
func (s *Store) PruneRevokedDevices(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for {
		res, err := s.W.ExecContext(ctx, `DELETE FROM device WHERE id IN (
			SELECT d.id FROM device d WHERE d.revoked_at IS NOT NULL AND d.revoked_at < ? AND d.blocked_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM device_credential c WHERE c.device_id = d.id AND c.revoked_at IS NULL) LIMIT ?)`, unix(cutoff), batch)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < int64(batch) {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(20 * time.Millisecond): // let the stats writer in between batches
		}
	}
}
