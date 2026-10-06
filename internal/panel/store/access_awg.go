package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Queries of the AWG devices: one device = one peer of ONE profile, its tunnel address
// handed out by the IPAM below. Everything that hands out an address runs in ONE write transaction, and the store
// has a single writer connection, so concurrent additions queue instead of racing: the smallest free index is
// read and used inside the same transaction.

// awgQuarantine is how long a released peer index is kept out of circulation: an old client may still use the
// address it was given.
const awgQuarantine = 24 * time.Hour

// AWGHwidHash is the hwid_hash of an AWG device: NULL belongs to the implicit per-user device (one per user, a
// unique index), so an explicit device gets a synthetic hash that never equals the hash of a real HWID header.
func AWGHwidHash(deviceID string) []byte {
	h := sha256.Sum256([]byte("awg:" + deviceID))
	return h[:]
}

func accPublicJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// AccessLimitError is returned when the user already has as many devices as the limit allows.
type AccessLimitError struct{ Used, Limit int }

func (e *AccessLimitError) Error() string { return fmt.Sprintf("device limit %d/%d", e.Used, e.Limit) }

// ErrAccessSubnetFull is returned when the client network of a profile has no free peer index.
var ErrAccessSubnetFull = errors.New("store: subnet full")

// AWGIssue makes the credential of one peer once its index is known: it fills ID, Protocol, SecretEnc and
// DataJSON (the store sets the owner, the profile and the epoch) and returns the peer's public key. It runs
// inside the write transaction: no database access of its own.
type AWGIssue func(idx int) (cred AccessCred, publicKey string, err error)

// AWGDeviceAdd describes a new AWG device.
type AWGDeviceAdd struct {
	Device    AccessDevice // ID, UserID, Platform, Model, CreatedAt
	ProfileID string
	MaxIdx    int // the largest peer index of the profile's client network
	Limit     int // the user's device limit
}

// awgAllocIdx returns the smallest free peer index in 2..maxIdx of a profile: one that no live peer holds and that
// was not released within the quarantine. Released rows older than the quarantine are dropped first. The system
// credentials of the profile's inbounds (the health checker's AWG client, health_probe_cred.awg_idx) hold indexes of
// the same network, so they count as taken.
func awgAllocIdx(ctx context.Context, tx *sql.Tx, profileID string, maxIdx int, now time.Time) (int, error) {
	cutoff := unix(now.Add(-awgQuarantine))
	if _, err := tx.ExecContext(ctx, `DELETE FROM awg_peer WHERE profile_id = ? AND released_at > 0 AND released_at <= ?`, profileID, cutoff); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT idx FROM awg_peer WHERE profile_id = ?
		 UNION SELECT c.awg_idx FROM health_probe_cred c JOIN inbound i ON i.id = c.inbound_id WHERE i.profile_id = ? AND c.awg_idx > 0
		 ORDER BY 1`, profileID, profileID)
	if err != nil {
		return 0, err
	}
	var taken []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			rows.Close()
			return 0, err
		}
		taken = append(taken, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	next := 2
	for _, i := range taken {
		if i == next {
			next++
		} else if i > next {
			break
		}
	}
	if next > maxIdx {
		return 0, ErrAccessSubnetFull
	}
	return next, nil
}

func awgInsertPeer(ctx context.Context, tx *sql.Tx, credID, profileID string, idx int, publicKey string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES (?, ?, ?, ?, ?)`,
		credID, profileID, idx, publicKey, unix(now))
	return err
}

// awgIssueInto fills the store-owned fields of an issued credential and inserts it with its peer.
func awgIssueInto(ctx context.Context, tx *sql.Tx, deviceID, userID, profileID string, idx int, now time.Time, issue AWGIssue) error {
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT critical_epoch FROM profile WHERE id = ?`, profileID).Scan(&epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	cred, pub, err := issue(idx)
	if err != nil {
		return err
	}
	cred.DeviceID, cred.UserID, cred.ProfileID, cred.ConfigEpoch, cred.CreatedAt = deviceID, userID, profileID, epoch, now
	if err := accInsertCreds(ctx, tx, []AccessCred{cred}); err != nil {
		return err
	}
	return awgInsertPeer(ctx, tx, cred.ID, profileID, idx, pub, now)
}

func accMapErr(err error) error {
	switch {
	case accIsUnique(err):
		return ErrAccessExists
	case accIsFK(err):
		return ErrNotFound
	}
	return err
}

// AddAWGDevice inserts an AWG device with its credential and peer: limit check, index, credential and rows in one
// transaction. It returns the peer index. *AccessLimitError when the user has Limit devices already,
// ErrAccessSubnetFull when the network is used up, ErrNotFound when the user or profile is gone.
func (a Access) AddAWGDevice(ctx context.Context, add AWGDeviceAdd, now time.Time, issue AWGIssue) (int, error) {
	tx, err := a.s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, accMapErr(err)
	}
	defer tx.Rollback()
	var used int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM device d WHERE d.user_id = ? AND d.revoked_at IS NULL AND `+accDeviceLive, add.Device.UserID).Scan(&used); err != nil {
		return 0, accMapErr(err)
	}
	if used >= add.Limit {
		return 0, &AccessLimitError{Used: used, Limit: add.Limit}
	}
	idx, err := awgAllocIdx(ctx, tx, add.ProfileID, add.MaxIdx, now)
	if err != nil {
		return 0, accMapErr(err)
	}
	d := add.Device
	d.AWG, d.CreatedAt = true, now
	if err := accInsertDevice(ctx, tx, d, nil); err != nil {
		return idx, accMapErr(err)
	}
	if err := awgIssueInto(ctx, tx, d.ID, d.UserID, add.ProfileID, idx, now, issue); err != nil {
		return idx, accMapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return idx, accMapErr(err)
	}
	return idx, nil
}

// RotateAWGDevice replaces the credential of a live AWG device by a new one with the SAME peer index (the same
// tunnel address): the old credential is revoked and its peer released in the same transaction, so one desired
// state removes the old public key and adds the new one. It returns the device's user. ErrNotFound when there is
// no such live AWG device.
func (a Access) RotateAWGDevice(ctx context.Context, deviceID string, now time.Time, issue AWGIssue) (string, error) {
	tx, err := a.s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", accMapErr(err)
	}
	defer tx.Rollback()
	var userID, oldCred, profileID string
	var idx int
	err = tx.QueryRowContext(ctx,
		`SELECT d.user_id, c.id, c.profile_id, ap.idx FROM device d
			 JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
			 JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
			 WHERE d.id = ? AND d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL ORDER BY c.created_at, c.id LIMIT 1`, deviceID).
		Scan(&userID, &oldCred, &profileID, &idx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", accMapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_credential SET revoked_at = ? WHERE id = ?`, unix(now), oldCred); err != nil {
		return userID, accMapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE awg_peer SET released_at = ? WHERE credential_id = ?`, unix(now), oldCred); err != nil {
		return userID, accMapErr(err)
	}
	if err := awgIssueInto(ctx, tx, deviceID, userID, profileID, idx, now, issue); err != nil {
		return userID, accMapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return userID, accMapErr(err)
	}
	return userID, nil
}

// AWGImplicitWant asks for an AWG credential of one profile on the implicit device.
type AWGImplicitWant struct {
	ProfileID string
	MaxIdx    int
	Issue     AWGIssue
}

// EnsureImplicitAWGCreds gives the user's implicit device (created from dev when there is none) an AWG
// credential for every wanted profile that it has none for, and returns how many it added. A subscription in the
// Mihomo format has no device identity, so every Mihomo client of the user shares this one peer per profile.
// The device limit is not checked: a subscription fetch must not fail because of it (the implicit device is one
// per user, so the limit is exceeded by at most one).
func (a Access) EnsureImplicitAWGCreds(ctx context.Context, userID string, dev AccessDevice, now time.Time, want []AWGImplicitWant) (int, error) {
	added := 0
	tx, err := a.s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, accMapErr(err)
	}
	defer tx.Rollback()
	devID := ""
	err = tx.QueryRowContext(ctx, `SELECT id FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, userID).Scan(&devID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, accMapErr(err)
	}
	have := map[string]bool{}
	if devID != "" {
		rows, err := tx.QueryContext(ctx,
			`SELECT profile_id FROM device_credential WHERE device_id = ? AND revoked_at IS NULL AND profile_id IS NOT NULL`, devID)
		if err != nil {
			return 0, accMapErr(err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return 0, accMapErr(err)
			}
			have[p] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, accMapErr(err)
		}
	}
	for _, w := range want {
		if have[w.ProfileID] {
			continue
		}
		if devID == "" {
			dev.UserID, dev.Implicit, dev.CreatedAt = userID, true, now
			if err := accInsertDevice(ctx, tx, dev, nil); err != nil {
				return 0, accMapErr(err)
			}
			devID = dev.ID
		}
		idx, err := awgAllocIdx(ctx, tx, w.ProfileID, w.MaxIdx, now)
		if err != nil {
			return 0, accMapErr(err)
		}
		if err := awgIssueInto(ctx, tx, devID, userID, w.ProfileID, idx, now, w.Issue); err != nil {
			return 0, accMapErr(err)
		}
		have[w.ProfileID] = true
		added++
	}
	if err := tx.Commit(); err != nil {
		return 0, accMapErr(err)
	}
	return added, nil
}

// AccessAWGDevice is a live AWG device with its one live credential and peer.
type AccessAWGDevice struct {
	AccessDevice
	ProfileID, ProfileName, ProfileSettingsJSON string // the settings without secrets: enough for the version
	CredID, DataJSON, PublicKey                 string
	SecretEnc                                   []byte // vault, AAD = CredID
	Idx                                         int
	ConfigEpoch, CriticalEpoch                  int64
	// DNSSig is the DNS the key was issued with, per node id (the pair its config holds); empty when nothing was recorded.
	DNSSig map[string]string
	// DNSStale lists the nodes where the DNS that applies to the person is not the one the key holds. The store leaves it
	// empty: only the access module knows what applies.
	DNSStale []string
}

// Stale reports whether the profile changed in a way that breaks the config the device last received.
func (d AccessAWGDevice) Stale() bool { return d.ConfigEpoch < d.CriticalEpoch }

func (a Access) awgDevices(ctx context.Context, where string, arg any) ([]AccessAWGDevice, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT d.id, d.user_id, d.platform, d.model, d.os_version, d.first_seen_at, d.last_seen_at, d.created_at,
		   p.id, p.name, p.settings_json, c.id, c.data_json, c.secret_enc, ap.public_key, ap.idx, c.config_epoch, p.critical_epoch, c.dns_sig
		 FROM device d
		 JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
		 JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
		 JOIN profile p ON p.id = c.profile_id
		 WHERE d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL AND `+where+` ORDER BY d.created_at, d.id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessAWGDevice
	for rows.Next() {
		var d AccessAWGDevice
		var first, last, created int64
		var sig string
		if err := rows.Scan(&d.ID, &d.UserID, &d.Platform, &d.Model, &d.OSVersion, &first, &last, &created,
			&d.ProfileID, &d.ProfileName, &d.ProfileSettingsJSON, &d.CredID, &d.DataJSON, &d.SecretEnc, &d.PublicKey, &d.Idx,
			&d.ConfigEpoch, &d.CriticalEpoch, &sig); err != nil {
			return nil, err
		}
		if sig != "" { // written by SetDNSSig; anything else reads as "nothing recorded"
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
		out = append(out, d)
	}
	return out, rows.Err()
}

// AWGDevices returns the live AWG devices of a user, oldest first. FirstSeenAt and LastSeenAt are zero while the
// device has never connected.
func (a Access) AWGDevices(ctx context.Context, userID string) ([]AccessAWGDevice, error) {
	return a.awgDevices(ctx, `d.user_id = ?`, userID)
}

// AWGDevice returns one live AWG device, or ErrNotFound.
func (a Access) AWGDevice(ctx context.Context, deviceID string) (AccessAWGDevice, error) {
	ds, err := a.awgDevices(ctx, `d.id = ?`, deviceID)
	if err != nil {
		return AccessAWGDevice{}, err
	}
	if len(ds) == 0 {
		return AccessAWGDevice{}, ErrNotFound
	}
	return ds[0], nil
}

// SetConfigEpoch records that the credential's device received a config of the given profile epoch (never lowers it).
func (a Access) SetConfigEpoch(ctx context.Context, credID string, epoch int64) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE device_credential SET config_epoch = max(config_epoch, ?) WHERE id = ?`, epoch, credID)
	return err
}

// SetDNSSig records the DNS the credential's key was issued with, per node id (replacing what was there): what a person's
// device now holds, the one thing "stale DNS" is measured against.
func (a Access) SetDNSSig(ctx context.Context, credID string, sig map[string]string) error {
	b, err := json.Marshal(sig)
	if err != nil {
		return err
	}
	_, err = a.s.W.ExecContext(ctx, `UPDATE device_credential SET dns_sig = ? WHERE id = ?`, string(b), credID)
	return err
}

// RenameDevice sets the label (device.model) of a live AWG device; ErrNotFound when there is none.
func (a Access) RenameDevice(ctx context.Context, deviceID, label string) error {
	res, err := a.s.W.ExecContext(ctx,
		`UPDATE device SET model = ? WHERE id = ? AND revoked_at IS NULL AND hwid_hash IS NOT NULL`, label, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpProfileEpoch marks every device of the profile that already received a config as stale (something on the
// nodes changed that an issued config cannot survive).
func (a Access) BumpProfileEpoch(ctx context.Context, profileID string) error {
	_, err := a.s.W.ExecContext(ctx, `UPDATE profile SET critical_epoch = critical_epoch + 1 WHERE id = ?`, profileID)
	return err
}

// ProfileLiveAWG counts the live credentials of explicit AWG devices of a profile (the configs an x-critical
// change breaks) and lists the names of their owners, at most limit, ordered.
func (a Access) ProfileLiveAWG(ctx context.Context, profileID string, limit int) (n int, users []string, err error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT u.name FROM device_credential c
		 JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL
		 JOIN user u ON u.id = c.user_id
		 WHERE c.profile_id = ? AND c.revoked_at IS NULL ORDER BY u.name, c.id`, profileID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, nil, err
		}
		n++
		if len(users) < limit && (len(users) == 0 || users[len(users)-1] != name) {
			users = append(users, name)
		}
	}
	return n, users, rows.Err()
}

// AWGDevicesPerProfile counts the live explicit AWG devices (AmneziaVPN keys) per profile, of the users of a group
// (groupID) or of one user (userID); the other argument is "".
func (a Access) AWGDevicesPerProfile(ctx context.Context, groupID, userID string) (map[string]int, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT c.profile_id, count(*) FROM device_credential c
		 JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL
		 JOIN user u ON u.id = c.user_id
		 WHERE c.revoked_at IS NULL AND c.profile_id IS NOT NULL AND (? = '' OR u.group_id = ?) AND (? = '' OR u.id = ?)
		 GROUP BY c.profile_id`, groupID, groupID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var pid string
		var n int
		if err := rows.Scan(&pid, &n); err != nil {
			return nil, err
		}
		out[pid] = n
	}
	return out, rows.Err()
}

// ProfileHasPeers reports whether the profile ever handed out a tunnel address (live or in quarantine): its client
// networks are then fixed.
func (a Access) ProfileHasPeers(ctx context.Context, profileID string) (bool, error) {
	var one int
	err := a.s.R.QueryRowContext(ctx, `SELECT 1 FROM awg_peer WHERE profile_id = ? LIMIT 1`, profileID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
