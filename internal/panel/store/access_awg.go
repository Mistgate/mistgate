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
// handed out by the IPAM below. Both backends read a candidate index, then use an atomic guarded write and retry if
// another writer took it first.

// awgQuarantine is how long a released peer index is kept out of circulation: an old client may still use the
// address it was given.
const awgQuarantine = 24 * time.Hour

// awgTakenSQL is the one definition of the peer indexes of profile ?1 that are not free at cutoff ?2: live and
// quarantined device peers, and the synthetic checker's credentials. Only live device peers have a unique index behind
// them, so every allocation reads its candidate from this and guards its write with "?3 NOT IN (awgTakenSQL)".
// A rotation keeps the device's own index and does not allocate.
const awgTakenSQL = `
	SELECT idx FROM awg_peer
	 WHERE profile_id = ?1 AND (released_at = 0 OR released_at > ?2)
	UNION
	SELECT c.awg_idx FROM health_probe_cred c JOIN inbound i ON i.id = c.inbound_id
	 WHERE i.profile_id = ?1 AND c.awg_idx > 0`

func awgPurgeExpiredStmt(profileID string, cutoff int64) Stmt {
	return Stmt{Query: `DELETE FROM awg_peer WHERE profile_id = ? AND released_at > 0 AND released_at <= ?`, Args: []any{profileID, cutoff}}
}

func awgInsertPeerStmt(credID, profileID string, idx int, publicKey string, createdAt time.Time) Stmt {
	return Stmt{Query: `INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at) VALUES (?, ?, ?, ?, ?)`,
		Args: []any{credID, profileID, int64(idx), publicKey, unix(createdAt)}}
}

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
// DataJSON (the store sets the owner, the profile and the epoch) and returns the peer's public key. It runs once per
// attempt between the candidate read and guarded write, so it must not access the database. Treat its result as
// provisional: do not keep it; only credentials returned by the store after commit or read back from the store are real.
type AWGIssue func(idx int) (cred AccessCred, publicKey string, err error)

// AWGDeviceAdd describes a new AWG device.
type AWGDeviceAdd struct {
	Device    AccessDevice // ID, UserID, Platform, Model, CreatedAt
	ProfileID string
	MaxIdx    int // the largest peer index of the profile's client network
	Limit     int // the user's device limit
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
// guarded batch. It returns the peer index. *AccessLimitError when the user has Limit devices already,
// ErrAccessSubnetFull when the network is used up, ErrNotFound when the user or profile is gone.
func (a Access) AddAWGDevice(ctx context.Context, add AWGDeviceAdd, now time.Time, issue AWGIssue) (int, error) {
	cutoff := unix(now.Add(-awgQuarantine))
	var idx int
	_, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		idx = 0
		var used int
		if err := a.s.R.QueryRowContext(ctx,
			`SELECT count(*) FROM device d WHERE d.user_id = ? AND d.revoked_at IS NULL AND `+accDeviceLive, add.Device.UserID).Scan(&used); err != nil {
			return nil, accMapErr(err)
		}
		if used >= add.Limit {
			return nil, &AccessLimitError{Used: used, Limit: add.Limit}
		}
		candidate, err := a.awgAllocIdxRead(ctx, add.ProfileID, add.MaxIdx, cutoff)
		if err != nil {
			return nil, accMapErr(err)
		}
		idx = candidate
		var epoch int64
		if err := a.s.R.QueryRowContext(ctx, `SELECT critical_epoch FROM profile WHERE id = ?`, add.ProfileID).Scan(&epoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, accMapErr(err)
		}
		cred, publicKey, err := issue(idx)
		if err != nil {
			return nil, accMapErr(err)
		}
		device := add.Device
		device.AWG, device.CreatedAt = true, now
		cred.DeviceID, cred.UserID, cred.ProfileID, cred.ConfigEpoch, cred.CreatedAt = device.ID, device.UserID, add.ProfileID, epoch, now
		guardSQL := `(
			(SELECT count(*) FROM device d WHERE d.user_id = ?4 AND d.revoked_at IS NULL AND ` + accDeviceLive + `) < ?5
			AND EXISTS (SELECT 1 FROM profile WHERE id = ?1 AND critical_epoch = ?6)
			AND ?3 NOT IN (` + awgTakenSQL + `)
		)`
		stmts := []Stmt{
			guard(guardSQL, add.ProfileID, cutoff, int64(idx), device.UserID, int64(add.Limit), epoch),
			awgPurgeExpiredStmt(add.ProfileID, cutoff),
			accInsertDeviceStmt(device),
		}
		stmts = append(stmts, accInsertCredStmts([]AccessCred{cred})...)
		stmts = append(stmts, awgInsertPeerStmt(cred.ID, add.ProfileID, idx, publicKey, now))
		return stmts, nil
	})
	if err != nil {
		return 0, accMapErr(err)
	}
	return idx, nil
}

func (a Access) awgAllocIdxRead(ctx context.Context, profileID string, maxIdx int, cutoff int64) (int, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT idx FROM (`+awgTakenSQL+`) taken ORDER BY idx`, profileID, cutoff)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	taken := map[int]bool{}
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			return 0, err
		}
		taken[idx] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	next := nextAWGIndex(taken, maxIdx)
	if next == 0 {
		return 0, ErrAccessSubnetFull
	}
	return next, nil
}

// RotateAWGDevice replaces a live AWG credential while keeping its peer index (and tunnel address). A guarded batch
// revokes the old credential and peer before inserting the new pair. It returns the device's user. ErrNotFound when
// there is no such live AWG device.
func (a Access) RotateAWGDevice(ctx context.Context, deviceID string, now time.Time, issue AWGIssue) (string, error) {
	var resultUserID string
	_, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		resultUserID = ""
		var userID, oldCred, profileID string
		var idx int
		err := a.s.R.QueryRowContext(ctx,
			`SELECT d.user_id, c.id, c.profile_id, ap.idx FROM device d
				 JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
				 JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
				 WHERE d.id = ? AND d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL ORDER BY c.created_at, c.id LIMIT 1`, deviceID).
			Scan(&userID, &oldCred, &profileID, &idx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, accMapErr(err)
		}
		resultUserID = userID

		var epoch int64
		if err := a.s.R.QueryRowContext(ctx, `SELECT critical_epoch FROM profile WHERE id = ?`, profileID).Scan(&epoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, accMapErr(err)
		}
		cred, publicKey, err := issue(idx)
		if err != nil {
			return nil, accMapErr(err)
		}
		cred.DeviceID, cred.UserID, cred.ProfileID, cred.ConfigEpoch, cred.CreatedAt = deviceID, userID, profileID, epoch, now

		// The device keeps its own index: the guard proves it still holds it live through the old credential. The index
		// is not checked against awgTakenSQL, which also lists this device's own quarantined peers of earlier rotations.
		guardSQL := `EXISTS (
			SELECT 1 FROM device d
			JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
			JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
			WHERE d.id = ? AND d.user_id = ? AND d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL
			AND c.id = ? AND c.profile_id = ? AND ap.idx = ?
			AND NOT EXISTS (
				SELECT 1 FROM device_credential c2
				JOIN awg_peer ap2 ON ap2.credential_id = c2.id AND ap2.released_at = 0
				WHERE c2.device_id = d.id AND c2.revoked_at IS NULL AND c2.profile_id IS NOT NULL
				AND (c2.created_at < c.created_at OR (c2.created_at = c.created_at AND c2.id < c.id))
			)
		) AND EXISTS (SELECT 1 FROM profile WHERE id = ? AND critical_epoch = ?)`
		stmts := []Stmt{
			guard(guardSQL, deviceID, userID, oldCred, profileID, int64(idx), profileID, epoch),
			{Query: `UPDATE device_credential SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, Args: []any{unix(now), oldCred}},
			{Query: `UPDATE awg_peer SET released_at = ? WHERE credential_id = ? AND released_at = 0`, Args: []any{unix(now), oldCred}},
		}
		stmts = append(stmts, accInsertCredStmts([]AccessCred{cred})...)
		stmts = append(stmts, awgInsertPeerStmt(cred.ID, profileID, idx, publicKey, now))
		return stmts, nil
	})
	if err != nil {
		return resultUserID, accMapErr(err)
	}
	return resultUserID, nil
}

// AWGImplicitWant asks for an AWG credential of one profile on the implicit device.
type AWGImplicitWant struct {
	ProfileID string
	MaxIdx    int
	Issue     AWGIssue
}

type accessProfileEpoch struct {
	ID    string
	Epoch int64
}

func scanAccessProfileEpoch(r rowScanner) (accessProfileEpoch, error) {
	var value accessProfileEpoch
	err := r.Scan(&value.ID, &value.Epoch)
	return value, err
}

type awgTakenIndex struct {
	ProfileID string
	Index     int
}

func scanAWGTakenIndex(r rowScanner) (awgTakenIndex, error) {
	var value awgTakenIndex
	err := r.Scan(&value.ProfileID, &value.Index)
	return value, err
}

type awgDeviceProfile struct {
	Device  AccessAWGDevice
	Profile AccessProfile
}

func scanAWGDeviceProfile(r rowScanner) (awgDeviceProfile, error) {
	device, profile, err := scanAWGDeviceWithProfile(r)
	return awgDeviceProfile{Device: device, Profile: profile}, err
}

// ImplicitAWGResult is the implicit device and its full live credential set from the read or write batch, plus the
// credentials this call added.
type ImplicitAWGResult struct {
	Device       AccessDevice
	Creds        []AccessCred
	Added        []AccessCred
	FullProfiles []string
}

// EnsureImplicitAWGCreds gives the user's implicit device (created with a fresh ID when none is live) an AWG
// credential for every wanted profile that it has none for, and returns the device's full live credential set plus
// the credentials it added. A subscription in the Mihomo format has no device identity, so every Mihomo client of the
// user shares this one peer per profile.
// The device limit is not checked: a subscription fetch must not fail because of it (the implicit device is one
// per user, so the limit is exceeded by at most one).
func (a Access) EnsureImplicitAWGCreds(ctx context.Context, userID string, dev AccessDevice, now time.Time, want []AWGImplicitWant) (ImplicitAWGResult, error) {
	if len(want) == 0 {
		return ImplicitAWGResult{}, nil
	}
	profileIDs := make([]string, 0, len(want))
	seen := map[string]bool{}
	for _, w := range want {
		if w.ProfileID != "" && !seen[w.ProfileID] {
			seen[w.ProfileID] = true
			profileIDs = append(profileIDs, w.ProfileID)
		}
	}
	if len(profileIDs) == 0 {
		return ImplicitAWGResult{}, nil
	}
	cutoff := unix(now.Add(-awgQuarantine))
	var result ImplicitAWGResult
	results, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		result = ImplicitAWGResult{}
		var implicitRows []implicitDeviceCredRow
		var profileEpochs []accessProfileEpoch
		var userIDs []string
		var taken []awgTakenIndex
		r := reads{}
		r.add(appendRows(&implicitRows, scanImplicitDeviceCredRow), implicitDeviceCredsSQL, userID)
		r.add(appendRows(&profileEpochs, scanAccessProfileEpoch), `SELECT id, critical_epoch FROM profile WHERE id IN (SELECT value FROM json_each(?))`, accJSON(profileIDs))
		r.add(appendRows(&userIDs, scanString), `SELECT id FROM user WHERE id = ?`, userID)
		for _, profileID := range profileIDs {
			r.add(appendRows(&taken, scanAWGTakenIndex), `SELECT ?1 AS profile_id, taken.idx FROM (`+awgTakenSQL+`) taken`, profileID, cutoff)
		}
		if err := r.run(ctx, a.s); err != nil {
			return nil, accMapErr(err)
		}
		if len(userIDs) == 0 {
			return nil, ErrNotFound
		}
		snapshot := implicitDeviceSnapshot(implicitRows)
		epochs := make(map[string]int64, len(profileEpochs))
		for _, profile := range profileEpochs {
			epochs[profile.ID] = profile.Epoch
		}
		occupied := make(map[string]map[int]bool, len(profileIDs))
		for _, profileID := range profileIDs {
			occupied[profileID] = map[int]bool{}
		}
		for _, row := range taken {
			occupied[row.ProfileID][row.Index] = true
		}
		result.Device, result.Creds = snapshot.Device, snapshot.Creds
		deviceID := snapshot.Device.ID
		have := map[string]bool{}
		for _, cred := range snapshot.Creds {
			if cred.ProfileID != "" {
				have[cred.ProfileID] = true
			}
		}
		if deviceID == "" {
			dev.UserID, dev.Implicit, dev.CreatedAt = userID, true, now
			dev.ID = NewID("dev_")
			deviceID = dev.ID
		}
		var issues []awgBatchIssue
		for _, w := range want {
			if w.ProfileID == "" || have[w.ProfileID] {
				continue
			}
			idx := nextAWGIndex(occupied[w.ProfileID], w.MaxIdx)
			if idx == 0 {
				result.FullProfiles = append(result.FullProfiles, w.ProfileID)
				continue
			}
			epoch, ok := epochs[w.ProfileID]
			if !ok {
				return nil, ErrNotFound
			}
			cred, publicKey, err := w.Issue(idx)
			if err != nil {
				return nil, accMapErr(err)
			}
			cred.DeviceID, cred.UserID, cred.ProfileID, cred.ConfigEpoch, cred.CreatedAt = deviceID, userID, w.ProfileID, epoch, now
			issues = append(issues, awgBatchIssue{profileID: w.ProfileID, idx: idx, epoch: epoch, cred: cred, publicKey: publicKey})
			occupied[w.ProfileID][idx] = true
			have[w.ProfileID] = true
		}
		if len(issues) == 0 {
			return nil, nil
		}
		stmts := make([]Stmt, 0, 2+len(issues)*4)
		if snapshot.Device.ID == "" {
			stmts = append(stmts, guard(`NOT EXISTS (SELECT 1 FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL)`, userID))
			stmts = append(stmts, accInsertDeviceStmt(dev))
		} else {
			stmts = append(stmts, guard(`EXISTS (SELECT 1 FROM device WHERE id = ? AND user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL)`, deviceID, userID))
		}
		for _, issue := range issues {
			stmts = append(stmts, guard(`
				EXISTS (SELECT 1 FROM device WHERE id = ?4 AND user_id = ?5 AND hwid_hash IS NULL AND revoked_at IS NULL)
				AND EXISTS (SELECT 1 FROM profile WHERE id = ?1 AND critical_epoch = ?6)
				AND NOT EXISTS (SELECT 1 FROM device_credential WHERE device_id = ?4 AND profile_id = ?1 AND revoked_at IS NULL)
				AND ?3 NOT IN (`+awgTakenSQL+`)`,
				issue.profileID, cutoff, int64(issue.idx), deviceID, userID, issue.epoch))
			stmts = append(stmts, awgPurgeExpiredStmt(issue.profileID, cutoff))
			stmts = append(stmts, accInsertCredStmts([]AccessCred{issue.cred})...)
			stmts = append(stmts, awgInsertPeerStmt(issue.cred.ID, issue.profileID, issue.idx, issue.publicKey, now))
		}
		result.Added = make([]AccessCred, len(issues))
		for i, issue := range issues {
			result.Added[i] = issue.cred
		}
		stmts = append(stmts, Stmt{Query: implicitDeviceCredsSQL, Args: []any{userID}, Returning: true})
		return stmts, nil
	})
	if err != nil {
		return ImplicitAWGResult{}, accMapErr(err)
	}
	if len(results) > 0 {
		var snapshot implicitDeviceCredsSnapshot
		for _, row := range results[len(results)-1].Rows {
			if err := snapshot.scan(batchRow(row)); err != nil {
				return ImplicitAWGResult{}, err
			}
		}
		result.Device, result.Creds = snapshot.Device, snapshot.Creds
	}
	return result, nil
}

type awgBatchIssue struct {
	profileID string
	idx       int
	epoch     int64
	cred      AccessCred
	publicKey string
}

func nextAWGIndex(taken map[int]bool, maxIdx int) int {
	for idx := 2; idx <= maxIdx; idx++ {
		if !taken[idx] {
			return idx
		}
	}
	return 0
}

// AccessAWGDevice is a live AWG device with its one live credential and peer.
type AccessAWGDevice struct {
	AccessDevice
	ProfileID, ProfileName, ProfileSettingsJSON string // the settings without secrets: enough for the version
	CredID, DataJSON, PublicKey                 string
	SecretEnc                                   []byte // vault, AAD = CredID
	Idx                                         int
	ConfigEpoch, CriticalEpoch                  int64
	// DNSSig is written by RecordDeviceConfig for each node config the device receives; anything else reads as nothing recorded.
	DNSSig map[string]string
	// DNSStale lists the nodes where the DNS that applies to the person is not the one the key holds. The store leaves it
	// empty: only the access module knows what applies.
	DNSStale []string
}

// AccessAWGDeviceScope is the read-only state needed to render one device's configs.
type AccessAWGDeviceScope struct {
	Device   AccessAWGDevice
	User     AccessUser
	Group    AccessGroup
	Profile  AccessProfile
	Inbounds []AccessInboundFull
}

// Stale reports whether the profile changed in a way that breaks the config the device last received.
func (d AccessAWGDevice) Stale() bool { return d.ConfigEpoch < d.CriticalEpoch }

func scanAWGDevice(r rowScanner) (AccessAWGDevice, error) {
	var d AccessAWGDevice
	var id, userID, platform, model, osVersion sql.NullString
	var first, last, created sql.NullInt64
	var profileID, profileName, profileSettings sql.NullString
	var credID, dataJSON, publicKey, dnsSig sql.NullString
	var secretEnc []byte
	var idx, configEpoch, criticalEpoch sql.NullInt64
	if err := r.Scan(&id, &userID, &platform, &model, &osVersion, &first, &last, &created,
		&profileID, &profileName, &profileSettings, &credID, &dataJSON, &secretEnc, &publicKey, &idx, &configEpoch, &criticalEpoch, &dnsSig); err != nil {
		return AccessAWGDevice{}, err
	}
	d.ID, d.UserID, d.Platform, d.Model, d.OSVersion = id.String, userID.String, platform.String, model.String, osVersion.String
	d.ProfileID, d.ProfileName, d.ProfileSettingsJSON = profileID.String, profileName.String, profileSettings.String
	d.CredID, d.DataJSON, d.SecretEnc, d.PublicKey = credID.String, dataJSON.String, secretEnc, publicKey.String
	d.Idx, d.ConfigEpoch, d.CriticalEpoch = int(idx.Int64), configEpoch.Int64, criticalEpoch.Int64
	d.AWG, d.Protocols = true, []string{"awg"}
	if first.Valid && first.Int64 != 0 {
		d.FirstSeenAt = fromUnix(first.Int64)
	}
	if last.Valid && last.Int64 != 0 {
		d.LastSeenAt = fromUnix(last.Int64)
	}
	if created.Valid {
		d.CreatedAt = fromUnix(created.Int64)
	}
	if dnsSig.String != "" {
		_ = json.Unmarshal([]byte(dnsSig.String), &d.DNSSig)
	}
	return d, nil
}

func scanAWGDeviceWithProfile(r rowScanner) (AccessAWGDevice, AccessProfile, error) {
	values, err := scanRowValues(r, 27)
	if err != nil {
		return AccessAWGDevice{}, AccessProfile{}, err
	}
	device, err := scanAWGDevice(batchRow(values[:19]))
	if err != nil {
		return AccessAWGDevice{}, AccessProfile{}, err
	}
	profile, err := scanAccessProfile(batchRow(values[19:]))
	if err != nil {
		return AccessAWGDevice{}, AccessProfile{}, err
	}
	return device, profile, nil
}

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
		d, err := scanAWGDevice(rows)
		if err != nil {
			return nil, err
		}
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

// AWGDeviceScope reads the device and the related user, group, profile and live inbounds in one fixed batch.
func (a Access) AWGDeviceScope(ctx context.Context, deviceID string) (AccessAWGDeviceScope, error) {
	var device awgDeviceProfile
	var user AccessUser
	var userNodeIDs []string
	var group AccessGroup
	groupProfileIDs := []string{}
	var inbounds []AccessInboundFull
	r := reads{}
	r.add(oneRow(&device, scanAWGDeviceProfile), `SELECT d.id AS device_id, d.user_id AS device_user_id, d.platform, d.model, d.os_version,
			coalesce(d.first_seen_at, 0) AS first_seen_at, coalesce(d.last_seen_at, 0) AS last_seen_at, d.created_at AS device_created_at,
			p.id AS device_profile_id, p.name AS device_profile_name, p.settings_json AS device_profile_settings,
			c.id AS credential_id, c.data_json AS credential_data, c.secret_enc AS credential_secret, ap.public_key, ap.idx,
			c.config_epoch, p.critical_epoch, coalesce(c.dns_sig, '') AS dns_sig,
			p.id AS profile_id, p.protocol AS profile_protocol, p.name AS profile_name, p.settings_json AS profile_settings_json,
			p.secrets_enc AS profile_secrets_enc, p.version AS profile_version, p.created_at AS profile_created_at, p.updated_at AS profile_updated_at
			FROM device d
			JOIN device_credential c ON c.device_id = d.id AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
			JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
			JOIN profile p ON p.id = c.profile_id
			WHERE d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL AND d.id = ?`, deviceID)
	r.add(oneRow(&user, scanAccessUser), `SELECT `+accUserCols+accUserFrom+`WHERE u.id = (SELECT user_id FROM device WHERE id = ?)`, deviceID)
	r.add(appendRows(&userNodeIDs, scanString), `SELECT node_id FROM user_node WHERE user_id = (SELECT user_id FROM device WHERE id = ?) ORDER BY node_id`, deviceID)
	r.add(oneRow(&group, scanAccessGroup), `SELECT g.id, g.name, g.created_at, (SELECT count(*) FROM user u WHERE u.group_id = g.id) AS user_count,
			coalesce(g.dns_preset_id, '') AS dns_preset_id, g.color
			FROM user_group g WHERE g.id = (SELECT u.group_id FROM user u WHERE u.id = (SELECT user_id FROM device WHERE id = ?))`, deviceID)
	r.add(appendRows(&groupProfileIDs, scanString), `SELECT profile_id FROM user_group_profile WHERE group_id = (SELECT u.group_id FROM user u WHERE u.id = (SELECT user_id FROM device WHERE id = ?)) ORDER BY profile_id`, deviceID)
	r.add(appendRows(&inbounds, scanAccessInboundFull), `SELECT `+accInboundCols+`, `+accProfileCols+`, `+accNodeCols+`
			FROM inbound i JOIN profile p ON p.id = i.profile_id JOIN node n ON n.id = i.node_id
			WHERE n.state <> 'retired' AND i.profile_id = (
				SELECT c.profile_id FROM device_credential c WHERE c.device_id = ? AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
			)
			ORDER BY i.created_at, i.rowid`, deviceID)
	if err := r.run(ctx, a.s); err != nil {
		return AccessAWGDeviceScope{}, err
	}
	user.NodeIDs = userNodeIDs
	group.ProfileIDs = groupProfileIDs
	return AccessAWGDeviceScope{Device: device.Device, User: user, Group: group, Profile: device.Profile, Inbounds: inbounds}, nil
}

// RecordDeviceConfig records what the credential's device received: the profile epoch of the config (never lowered; nil
// leaves it) and the DNS the key now holds per node id, replacing the stored one (nil leaves it), which is what "stale
// DNS" is measured against.
func (a Access) RecordDeviceConfig(ctx context.Context, credID string, epoch *int64, sig map[string]string) error {
	var epochValue any
	if epoch != nil {
		epochValue = *epoch
	}
	var sigValue any
	if sig != nil {
		b, err := json.Marshal(sig)
		if err != nil {
			return err
		}
		sigValue = string(b)
	}
	_, err := a.s.W.ExecContext(ctx, `UPDATE device_credential
		SET config_epoch = CASE WHEN ?1 IS NULL THEN config_epoch ELSE max(config_epoch, ?1) END,
		    dns_sig = coalesce(?2, dns_sig)
		WHERE id = ?3`, epochValue, sigValue, credID)
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
