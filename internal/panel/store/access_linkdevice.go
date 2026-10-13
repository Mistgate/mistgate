package store

import (
	"context"
	"crypto/sha256"
	"time"
)

// Link devices: the apps that take the subscription link, one device per HWID they report, and one shared device for
// the apps that report none. A request finds its device by hwid_hash IS ? (NULL = the shared device); EnsureLinkDevice
// and EnsureLinkAWGCreds make it exist and hold its credentials, each in one guarded batch.

// HWIDHash is the hwid_hash of a device that reported a HWID: sha256 over a versioned label, the user id and the canonical
// HWID (access.CanonHWID). The user id salts it, so one phone on two subscriptions is not linked and a leaked database
// holds no raw HWID (a raw HWID plus the link is a device's keys). It never equals AWGHwidHash.
func HWIDHash(userID, canonHWID string) []byte {
	b := make([]byte, 0, 160)
	b = append(b, "mistgate hwid v1\x00"...)
	b = append(b, userID...)
	b = append(b, 0)
	b = append(b, canonHWID...)
	sum := sha256.Sum256(b)
	return sum[:]
}

// linkDeviceCreateGuard is the condition under which a link device may be born: ?1 the user, ?2 its hwid_hash (NULL for the
// shared device), ?3 the ceiling on the user's live devices (0 = none). The one-device-per-hash half is also held by the
// unique indexes device_hwid and device_implicit; the count is what two new HWIDs racing at the ceiling are decided by
// (the second batch sees the first one's device and fails its guard).
const linkDeviceCreateGuard = `
	NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?1 AND hwid_hash IS ?2 AND kind = 'link' AND revoked_at IS NULL)
	AND (?3 = 0 OR (SELECT count(*) FROM device d WHERE d.user_id = ?1 AND d.revoked_at IS NULL AND ` + accDeviceLive + `) < ?3)`

// linkAdoptGuard: ?1 the shared device, ?2 the user. The shared device is live and holds credentials, and the user has no
// link device with a HWID: the first app that reports one takes the shared device over, credentials and all.
// "NOT (hwid_hash IS NULL)" because a hwid_hash is no sign of a key (device.kind is).
const linkAdoptGuard = `
	EXISTS (SELECT 1 FROM device d WHERE d.id = ?1 AND d.user_id = ?2 AND d.hwid_hash IS NULL AND d.kind = 'link' AND d.revoked_at IS NULL AND ` + accDeviceLive + `)
	AND NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?2 AND kind = 'link' AND NOT (hwid_hash IS NULL) AND revoked_at IS NULL)`

// LinkIssue makes the credentials a link device is missing. deviceID is the device they will belong to and have the
// protocols it already holds. It runs once per attempt between the read and the guarded write, so it must not touch the
// database, and its result is provisional: only what the store returns after the commit is real.
type LinkIssue func(deviceID string, have map[string]bool) ([]AccessCred, error)

// LinkDeviceResult is the request's device and its live credentials as the batch (or the read that found nothing to do)
// left them, plus the credentials this call added: Added is what nodes have not heard of.
type LinkDeviceResult struct {
	Device       AccessDevice
	Creds        []AccessCred
	Added        []AccessCred
	FullProfiles []string // EnsureLinkAWGCreds: the profiles whose client network has no free address
}

// linkDeviceState is what one attempt decides on, read in one batch.
type linkDeviceState struct {
	own, shared []linkDeviceCredRow
	userIDs     []string
	used        int // live devices that count against the limit
	hwidDevices int // live link devices with a HWID, empty ones too
}

// add registers the reads of the state of user userID, whose device is the one with hash (nil: the shared one).
func (st *linkDeviceState) add(r *reads, userID string, hash []byte) {
	r.add(appendRows(&st.own, scanLinkDeviceCredRow), linkDeviceCredsSQL, userID, hwidArg(hash))
	if len(hash) > 0 { // the adoption candidate
		r.add(appendRows(&st.shared, scanLinkDeviceCredRow), linkDeviceCredsSQL, userID, nil)
	}
	r.add(oneRow(&st.used, scanCount), `SELECT count(*) AS n FROM device d WHERE d.user_id = ? AND d.revoked_at IS NULL AND `+accDeviceLive, userID)
	r.add(oneRow(&st.hwidDevices, scanCount), `SELECT count(*) AS n FROM device WHERE user_id = ? AND kind = 'link' AND NOT (hwid_hash IS NULL) AND revoked_at IS NULL`, userID)
	r.add(appendRows(&st.userIDs, scanString), `SELECT id FROM user WHERE id = ?`, userID)
}

func scanCount(r rowScanner) (int, error) {
	var n int
	err := r.Scan(&n)
	return n, err
}

// linkBirth is how the request's device comes to exist, decided on one snapshot: it already does; it is the shared device
// of the user, taken over by the first app that reports a HWID; or it is new.
type linkBirth struct {
	deviceID string
	creds    []AccessCred // the live credentials of the device that will be used (the shared one's, when adopted)
	stmts    []Stmt       // leave the device existing, with the request's hwid_hash, or fail the batch's guard
	exists   bool         // the device is there: nothing to write for it
	born     bool         // the device is new
}

// birth decides it. dev is the request's device (its ID is the id a new one gets, "" = generated); ceiling limits the
// live devices of the user for a new one (0 = no limit). A new device at or above the ceiling is *AccessLimitError.
func (st *linkDeviceState) birth(dev AccessDevice, ceiling int, now time.Time) (linkBirth, error) {
	own := linkDeviceSnapshotOf(st.own)
	if own.Device.ID != "" {
		return linkBirth{deviceID: own.Device.ID, creds: own.Creds, exists: true, stmts: []Stmt{
			guard(`EXISTS (SELECT 1 FROM device WHERE id = ?1 AND user_id = ?2 AND hwid_hash IS ?3 AND kind = 'link' AND revoked_at IS NULL)`,
				own.Device.ID, dev.UserID, hwidArg(dev.HWIDHash))}}, nil
	}
	if shared := linkDeviceSnapshotOf(st.shared); len(dev.HWIDHash) > 0 && shared.Device.ID != "" && len(shared.Creds) > 0 && st.hwidDevices == 0 {
		return linkBirth{deviceID: shared.Device.ID, creds: shared.Creds, stmts: []Stmt{
			guard(linkAdoptGuard, shared.Device.ID, dev.UserID),
			{Query: `UPDATE device SET hwid_hash = ?3,
					platform = coalesce(nullif(?4, ''), platform), model = coalesce(nullif(?5, ''), model),
					os_version = coalesce(nullif(?6, ''), os_version), client = coalesce(nullif(?7, ''), client),
					first_seen_at = CASE WHEN first_seen_at = 0 THEN ?8 ELSE first_seen_at END, last_seen_at = max(last_seen_at, ?8)
				WHERE id = ?1 AND user_id = ?2 AND hwid_hash IS NULL AND kind = 'link' AND revoked_at IS NULL`,
				Args: []any{shared.Device.ID, dev.UserID, dev.HWIDHash, dev.Platform, dev.Model, dev.OSVersion, dev.Client, unix(now)}},
		}}, nil
	}
	if ceiling > 0 && st.used >= ceiling {
		return linkBirth{}, &AccessLimitError{Used: st.used, Limit: ceiling}
	}
	if dev.ID == "" {
		dev.ID = NewID("dev_")
	}
	dev.Kind, dev.Implicit, dev.AWG, dev.CreatedAt = DeviceKindLink, len(dev.HWIDHash) == 0, false, now
	return linkBirth{deviceID: dev.ID, born: true, stmts: []Stmt{
		guard(linkDeviceCreateGuard, dev.UserID, hwidArg(dev.HWIDHash), int64(ceiling)),
		accInsertDeviceStmt(dev),
	}}, nil
}

// EnsureLinkDevice makes the request's link device (dev.HWIDHash, nil for the shared device) exist and hold every
// credential issue returns for it, in one guarded batch, and reads the device back. A device that exists is only filled;
// the first HWID of a user whose shared device holds credentials takes the shared device over, credentials unchanged;
// otherwise a device is born, unless the user already has ceiling live devices (*AccessLimitError). A device is never
// born empty: with nothing to issue and no device the result is empty and nothing is written.
// Concurrent calls decide through the batch's guards and retry on what the winner left (see retryGuarded): two new HWIDs at
// the ceiling make one device, the same HWID twice makes one, two adoptions make one. ErrNotFound when the user is gone.
func (a Access) EnsureLinkDevice(ctx context.Context, dev AccessDevice, ceiling int, now time.Time, issue LinkIssue) (LinkDeviceResult, error) {
	var result LinkDeviceResult
	results, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		result = LinkDeviceResult{}
		var st linkDeviceState
		r := reads{}
		st.add(&r, dev.UserID, dev.HWIDHash)
		if err := r.run(ctx, a.s); err != nil {
			return nil, accMapErr(err)
		}
		if len(st.userIDs) == 0 {
			return nil, ErrNotFound
		}
		own := linkDeviceSnapshotOf(st.own)
		result.Device, result.Creds = own.Device, own.Creds
		b, err := st.birth(dev, ceiling, now)
		if err != nil {
			return nil, err
		}
		have := make(map[string]bool, len(b.creds))
		for _, c := range b.creds {
			have[c.Protocol] = true
		}
		added, err := issue(b.deviceID, have)
		if err != nil {
			return nil, accMapErr(err)
		}
		if len(added) == 0 && (b.exists || b.born) {
			return nil, nil // nothing to fill, and no device is born empty
		}
		stmts := append([]Stmt{}, b.stmts...)
		result.Added = make([]AccessCred, len(added))
		for i, cred := range added {
			cred.DeviceID, cred.UserID, cred.CreatedAt = b.deviceID, dev.UserID, now
			result.Added[i] = cred
			stmts = append(stmts, guard(`NOT EXISTS (SELECT 1 FROM device_credential WHERE device_id = ?1 AND protocol = ?2 AND coalesce(profile_id, '') = ?3 AND revoked_at IS NULL)`,
				b.deviceID, cred.Protocol, cred.ProfileID))
			stmts = append(stmts, accInsertCredStmts([]AccessCred{cred})...)
		}
		return append(stmts, Stmt{Query: linkDeviceCredsSQL, Args: []any{dev.UserID, hwidArg(dev.HWIDHash)}, Returning: true}), nil
	})
	if err != nil {
		return LinkDeviceResult{}, accMapErr(err)
	}
	if len(results) > 0 {
		var snapshot linkDeviceSnapshot
		for _, row := range results[len(results)-1].Rows {
			if err := snapshot.scan(batchRow(row)); err != nil {
				return LinkDeviceResult{}, err
			}
		}
		result.Device, result.Creds = snapshot.Device, snapshot.Creds
	}
	return result, nil
}
