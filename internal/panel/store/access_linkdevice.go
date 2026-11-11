package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
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

// The mode of the device limit (SettingLinkDeviceLimit) is stored as one of these words. A panel that was never given one
// counts only: the step-2 behaviour, and what every installation starts as (MigrateLinkDeviceLimit picks the real one).
const (
	// SettingLinkDeviceLimit is the setting that holds the mode (see subsettings, which also exposes it as
	// SubscriptionSettings.link_device_limit).
	SettingLinkDeviceLimit = "link_device_limit"

	LinkModeTrack   = "track"
	LinkModeEnforce = "enforce"
	LinkModeStrict  = "strict"
)

// LinkModeOf is the mode a stored value names; anything else, and nothing, is LinkModeTrack.
func LinkModeOf(stored string) string {
	switch stored {
	case LinkModeEnforce, LinkModeStrict:
		return stored
	}
	return LinkModeTrack
}

// LinkEvictAfter is how long a link device must have been silent to give up its place to a new HWID (ENFORCE and STRICT).
const LinkEvictAfter = 30 * 24 * time.Hour

var (
	// ErrAccessBlocked is returned when the HWID of a new device was blocked by the owner (device.blocked_at).
	ErrAccessBlocked = errors.New("store: device blocked")
	// ErrDeviceRevoked is returned when a device key belongs to a revoked device.
	ErrDeviceRevoked = errors.New("store: device revoked")
	// ErrDeviceEnrolled is returned by the basic paths (EnsureLinkDevice, EnsureLinkAWGCreds) for the HWID of an enrolled device: its
	// credentials leave only with its key, never to whoever sends the HWID with the link.
	ErrDeviceEnrolled = errors.New("store: device is enrolled in the client API")
	// ErrNoSharedDevice is returned when the shared device would have to be made under STRICT, which has none.
	ErrNoSharedDevice = errors.New("store: no shared device in strict mode")
	// ErrNoLinkCredentials is returned by EnrolLinkDevice when the issuer has no credential to give: an enrolled device is never
	// left without any, and nothing was written.
	ErrNoLinkCredentials = errors.New("store: no credentials to issue to the enrolled device")
)

// LinkDeviceKeyOwner is the user state needed by a client API device lookup: the id and the hash of the current link, which is
// all that sync needs to tell a moved link (the status it computes itself). It deliberately omits the sealed subscription token.
type LinkDeviceKeyOwner struct {
	UserID       string
	SubTokenHash []byte
}

// linkCeiling is how many live devices a user may have before one more link device is refused (0 = no limit). TRACK counts
// only: a HWID device has twice the limit as a ceiling (what a client that sends a new HWID with every request runs into)
// and the shared device none. ENFORCE and STRICT hold both to the limit.
func linkCeiling(mode string, limit int, hasHWID bool) int {
	switch {
	case limit <= 0:
		return 0
	case mode == LinkModeTrack && !hasHWID:
		return 0
	case mode == LinkModeTrack:
		return 2 * limit
	}
	return limit
}

// linkDeviceCreateGuard is the condition under which a link device may be born: ?1 the user, ?2 its hwid_hash (NULL for the
// shared device), ?3 the ceiling on the user's live devices (0 = none). The one-device-per-hash half is also held by the
// unique indexes device_hwid and device_implicit; the count is what two new HWIDs racing at the ceiling are decided by
// (the second batch sees the first one's device and fails its guard). A hash the owner blocked is never born again; the
// block is a row of the same user and hash (revoked, with blocked_at), so a block that lands between the read and the write
// fails the guard and the repeat reads it.
const linkDeviceCreateGuard = `
	NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?1 AND hwid_hash IS ?2 AND kind = 'link' AND revoked_at IS NULL)
	AND NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?1 AND hwid_hash = ?2 AND blocked_at IS NOT NULL)
	AND (?3 = 0 OR (SELECT count(*) FROM device d WHERE d.user_id = ?1 AND d.revoked_at IS NULL AND ` + accLimitDevice + `) < ?3)`

// linkDeviceEvictGuard is the condition under which a new HWID device takes the place of a silent one: ?1 the user, ?2 the new
// hwid_hash, ?3 the ceiling, ?4 the victim, ?5 the cutoff. The user is still at the ceiling (a place was freed by someone else
// otherwise, and nothing is to be evicted), the victim is still live, a link device holding credentials and still silent
// since the cutoff (it was not used again, nor evicted by the other writer), and the new hash is not there or blocked.
const linkDeviceEvictGuard = `
	NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?1 AND hwid_hash IS ?2 AND kind = 'link' AND revoked_at IS NULL)
	AND NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?1 AND hwid_hash = ?2 AND blocked_at IS NOT NULL)
	AND (SELECT count(*) FROM device d WHERE d.user_id = ?1 AND d.revoked_at IS NULL AND ` + accLimitDevice + `) >= ?3
	AND EXISTS (SELECT 1 FROM device d WHERE d.id = ?4 AND d.user_id = ?1 AND d.kind = 'link' AND d.revoked_at IS NULL AND d.last_seen_at < ?5 AND ` + accDeviceLive + `)`

// linkModeGuard: the mode the attempt decided under (?2, as stored: "" for none) is still the stored one (?1 the setting).
// Switching the mode and a fetch that decides on the old one are ordered by it: the fetch fails the guard and repeats on the
// new mode (a STRICT switch has revoked the shared devices in the same batch as the new mode).
const linkModeGuard = `coalesce((SELECT v FROM setting WHERE k = ?1), '') = ?2`

// revokeDeviceStmts soft-deletes the device and its credentials; the tunnel addresses of its AWG credentials go into
// quarantine (released_at > 0).
func revokeDeviceStmts(id string, now time.Time) []Stmt {
	stmts := []Stmt{{Query: `UPDATE device SET revoked_at = ? WHERE id = ?`, Args: []any{unix(now), id}}}
	return append(stmts, revokeDeviceCredentialStmts(id, now)...)
}

// revokeDeviceCredentialStmts revokes credentials and quarantines their AWG peers without revoking the owning device row.
func revokeDeviceCredentialStmts(id string, now time.Time) []Stmt {
	return []Stmt{
		{Query: `UPDATE awg_peer SET released_at = ? WHERE released_at = 0 AND credential_id IN
		   (SELECT id FROM device_credential WHERE device_id = ? AND revoked_at IS NULL)`, Args: []any{unix(now), id}},
		{Query: `UPDATE device_credential SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`, Args: []any{unix(now), id}},
	}
}

// linkAdoptGuard: ?1 the shared device, ?2 the user, ?3 the hwid_hash it is taken over by. The shared device is live and
// holds credentials, the user has no link device with a HWID, and the hash is not blocked: the first app that reports a
// HWID takes the shared device over, credentials and all.
// "NOT (hwid_hash IS NULL)" because a hwid_hash is no sign of a key (device.kind is).
const linkAdoptGuard = `
	EXISTS (SELECT 1 FROM device d WHERE d.id = ?1 AND d.user_id = ?2 AND d.hwid_hash IS NULL AND d.kind = 'link' AND d.revoked_at IS NULL AND ` + accDeviceLive + `)
	AND NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?2 AND kind = 'link' AND NOT (hwid_hash IS NULL) AND revoked_at IS NULL)
	AND NOT EXISTS (SELECT 1 FROM device WHERE user_id = ?2 AND hwid_hash = ?3 AND blocked_at IS NOT NULL)`

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
	// Replaced is the id of the silent device whose place the new one took (ENFORCE and STRICT), "" when none was.
	Replaced string
	// Born: the device is new (not found, not taken over from the shared one).
	Born bool
	// Used and Mode are what the attempt decided on: the user's live devices that count against the limit before this call, and the
	// mode of the limit. Callers that report a limit reached (access.placed) need them without a second read.
	Used int
	Mode string
}

// linkDeviceState is what one attempt decides on, read in one batch.
type linkDeviceState struct {
	own, shared []linkDeviceCredRow
	userIDs     []string
	used        int    // live devices that count against the limit
	hwidDevices int    // live link devices with a HWID, empty ones too
	mode        string // the stored mode of the device limit, as stored ("" = none)
	blocked     int    // rows of the user's that block the request's hash
	victims     []linkVictim
}

// linkVictim is a link device silent long enough to give its place away.
type linkVictim struct {
	ID, Platform, Model string
	LastSeen            int64
}

func scanLinkVictim(r rowScanner) (linkVictim, error) {
	var v linkVictim
	err := r.Scan(&v.ID, &v.Platform, &v.Model, &v.LastSeen)
	return v, err
}

// add registers the reads of the state of user userID, whose device is the one with hash (nil: the shared one). The
// oldest silent link device since now-LinkEvictAfter is read too: the place a new HWID device may take.
func (st *linkDeviceState) add(r *reads, userID string, hash []byte, now time.Time) {
	r.add(oneRow(&st.mode, scanString), `SELECT coalesce((SELECT v FROM setting WHERE k = ?), '') AS mode`, SettingLinkDeviceLimit)
	if len(hash) > 0 {
		r.add(oneRow(&st.blocked, scanCount), `SELECT count(*) AS n FROM device WHERE user_id = ? AND hwid_hash = ? AND blocked_at IS NOT NULL`, userID, hash)
	}
	r.add(appendRows(&st.victims, scanLinkVictim), `SELECT d.id, d.platform, d.model, d.last_seen_at FROM device d
		WHERE d.user_id = ? AND d.revoked_at IS NULL AND d.kind = 'link' AND d.last_seen_at < ? AND `+accDeviceLive+`
		ORDER BY d.last_seen_at, d.created_at, d.id LIMIT 1`, userID, unix(now.Add(-LinkEvictAfter)))
	r.add(appendRows(&st.own, scanLinkDeviceCredRow), linkDeviceCredsSQL, userID, hwidArg(hash))
	if len(hash) > 0 { // the adoption candidate
		r.add(appendRows(&st.shared, scanLinkDeviceCredRow), linkDeviceCredsSQL, userID, nil)
	}
	r.add(oneRow(&st.used, scanCount), `SELECT count(*) AS n FROM device d WHERE d.user_id = ? AND d.revoked_at IS NULL AND `+accLimitDevice, userID)
	r.add(oneRow(&st.hwidDevices, scanCount), `SELECT count(*) AS n FROM device WHERE user_id = ? AND kind = 'link' AND NOT (hwid_hash IS NULL) AND revoked_at IS NULL`, userID)
	r.add(appendRows(&st.userIDs, scanString), `SELECT id FROM user WHERE id = ?`, userID)
}

func scanCount(r rowScanner) (int, error) {
	var n int
	err := r.Scan(&n)
	return n, err
}

// linkBirth is how the request's device comes to exist, decided on one snapshot: it already does; it is the shared device
// of the user, taken over by the first app that reports a HWID; or it is new, perhaps in the place of a silent one.
type linkBirth struct {
	deviceID string
	creds    []AccessCred // the live credentials of the device that will be used (the shared one's, when adopted)
	stmts    []Stmt       // leave the device existing, with the request's hwid_hash, or fail the batch's guard
	exists   bool         // the device is there: nothing to write for it
	born     bool         // the device is new
	replaced string       // born in the place of this device (revoked by stmts)
	enrol    bool         // keyless existing device: credentials are replaced and the new hashes set in this batch
}

// birth decides it. dev is the request's device (its ID is the id a new one gets, "" = generated); limit is the user's
// device limit (0 = none) and the stored mode turns it into the ceiling for a new device (linkCeiling). Refusals, all made
// without a write: *AccessLimitError (at the ceiling, and no silent device to replace), ErrAccessBlocked (the owner blocked
// the HWID) and ErrNoSharedDevice (STRICT has no shared device). Every statement that rests on the mode carries a guard on it.
// enrol (EnrolLinkDevice) changes three things: the shared device is never adopted, an existing device with a key is refused
// (ErrAccessExists), and an existing keyless device keeps its row while its credentials are revoked and the hashes written.
// Without enrol an existing device with a key is ErrDeviceEnrolled.
func (st *linkDeviceState) birth(dev AccessDevice, limit int, now time.Time, enrol bool) (linkBirth, error) {
	own := linkDeviceSnapshotOf(st.own)
	if own.Device.ID != "" {
		if enrol {
			if len(own.Device.KeyHash) != 0 {
				return linkBirth{}, ErrAccessExists
			}
			stmts := []Stmt{guard(`EXISTS (SELECT 1 FROM device WHERE id = ?1 AND user_id = ?2
				AND hwid_hash = ?3 AND kind = 'link' AND revoked_at IS NULL AND key_hash IS NULL)`,
				own.Device.ID, dev.UserID, dev.HWIDHash)}
			stmts = append(stmts, revokeDeviceCredentialStmts(own.Device.ID, now)...)
			// The enrolling client says more about itself than the headers of a basic fetch did: what it says replaces what the row held.
			stmts = append(stmts, Stmt{Query: `UPDATE device SET key_hash = ?4, enrol_token_hash = ?5,
					platform = coalesce(nullif(?6, ''), platform), model = coalesce(nullif(?7, ''), model),
					os_version = coalesce(nullif(?8, ''), os_version), client = coalesce(nullif(?9, ''), client),
					first_seen_at = CASE WHEN first_seen_at = 0 THEN ?10 ELSE first_seen_at END, last_seen_at = max(last_seen_at, ?10)
				WHERE id = ?1 AND user_id = ?2 AND hwid_hash = ?3 AND kind = 'link' AND revoked_at IS NULL AND key_hash IS NULL`,
				Args: []any{own.Device.ID, dev.UserID, dev.HWIDHash, dev.KeyHash, dev.EnrolTokenHash,
					dev.Platform, dev.Model, dev.OSVersion, dev.Client, unix(now)}})
			return linkBirth{deviceID: own.Device.ID, exists: true, enrol: true, stmts: stmts}, nil
		}
		if len(own.Device.KeyHash) != 0 {
			return linkBirth{}, ErrDeviceEnrolled
		}
		// key_hash IS NULL: a device enrolled since the read (the snapshot is older than the enrolment) is neither filled nor returned.
		return linkBirth{deviceID: own.Device.ID, creds: own.Creds, exists: true, stmts: []Stmt{
			guard(`EXISTS (SELECT 1 FROM device WHERE id = ?1 AND user_id = ?2 AND hwid_hash IS ?3 AND kind = 'link' AND revoked_at IS NULL AND key_hash IS NULL)`,
				own.Device.ID, dev.UserID, hwidArg(dev.HWIDHash))}}, nil
	}
	mode, hasHWID := LinkModeOf(st.mode), len(dev.HWIDHash) > 0
	if st.blocked > 0 {
		return linkBirth{}, ErrAccessBlocked
	}
	if mode == LinkModeStrict && !hasHWID {
		return linkBirth{}, ErrNoSharedDevice
	}
	modeGuard := guard(linkModeGuard, SettingLinkDeviceLimit, st.mode)
	if shared := linkDeviceSnapshotOf(st.shared); !enrol && hasHWID && shared.Device.ID != "" && len(shared.Creds) > 0 && st.hwidDevices == 0 {
		return linkBirth{deviceID: shared.Device.ID, creds: shared.Creds, stmts: []Stmt{
			modeGuard,
			guard(linkAdoptGuard, shared.Device.ID, dev.UserID, dev.HWIDHash),
			{Query: `UPDATE device SET hwid_hash = ?3,
					platform = coalesce(nullif(?4, ''), platform), model = coalesce(nullif(?5, ''), model),
					os_version = coalesce(nullif(?6, ''), os_version), client = coalesce(nullif(?7, ''), client),
					first_seen_at = CASE WHEN first_seen_at = 0 THEN ?8 ELSE first_seen_at END, last_seen_at = max(last_seen_at, ?8)
				WHERE id = ?1 AND user_id = ?2 AND hwid_hash IS NULL AND kind = 'link' AND revoked_at IS NULL`,
				Args: []any{shared.Device.ID, dev.UserID, dev.HWIDHash, dev.Platform, dev.Model, dev.OSVersion, dev.Client, unix(now)}},
		}}, nil
	}
	ceiling := linkCeiling(mode, limit, hasHWID)
	var victim *linkVictim
	if ceiling > 0 && st.used >= ceiling {
		// Only ENFORCE and STRICT replace a device, and only for a HWID: TRACK has its ceiling instead, and the shared device
		// is not worth a place that someone else holds.
		if mode == LinkModeTrack || !hasHWID || len(st.victims) == 0 {
			return linkBirth{}, &AccessLimitError{Used: st.used, Limit: ceiling, Mode: mode}
		}
		victim = &st.victims[0]
	}
	if dev.ID == "" {
		dev.ID = NewID("dev_")
	}
	dev.Kind, dev.Implicit, dev.AWG, dev.CreatedAt = DeviceKindLink, !hasHWID, false, now
	b := linkBirth{deviceID: dev.ID, born: true}
	if victim == nil {
		b.stmts = []Stmt{modeGuard, guard(linkDeviceCreateGuard, dev.UserID, hwidArg(dev.HWIDHash), int64(ceiling)), accInsertDeviceStmt(dev)}
		return b, nil
	}
	b.replaced = victim.ID
	// The silent device is revoked with its credentials and peers, the event says so, and the new device takes its place.
	b.stmts = []Stmt{modeGuard, guard(linkDeviceEvictGuard, dev.UserID, hwidArg(dev.HWIDHash), int64(ceiling), victim.ID, unix(now.Add(-LinkEvictAfter)))}
	b.stmts = append(b.stmts, revokeDeviceStmts(victim.ID, now)...)
	b.stmts = append(b.stmts, Stmt{Query: `INSERT INTO event (ts, severity, code, source, user_id, device_id, params_json) VALUES (?, 1, 'device_replaced', 'panel', ?, ?, ?)`,
		Args: []any{unix(now), dev.UserID, dev.ID, deviceReplacedParams(*victim)}})
	b.stmts = append(b.stmts, accInsertDeviceStmt(dev))
	return b, nil
}

// enrolled finds the existing link device held by the supplied key and returns the guard that keeps credential
// issuance tied to that same live, unblocked key, and to the link it was enrolled under, through the write batch. linkCurrent is
// the read that says the device's enrol_token_hash is still the hash of the user's link: a rotation takes the device's
// credentials' right to grow away until it enrols again with the new link.
func (st *linkDeviceState) enrolled(dev AccessDevice, linkCurrent bool) (linkBirth, error) {
	own := linkDeviceSnapshotOf(st.own)
	if !linkCurrent || own.Device.ID == "" || own.Device.ID != dev.ID || len(dev.KeyHash) != sha256.Size || !bytes.Equal(own.Device.KeyHash, dev.KeyHash) {
		return linkBirth{}, ErrAccessExists
	}
	return linkBirth{deviceID: own.Device.ID, creds: own.Creds, exists: true, stmts: []Stmt{
		guard(`EXISTS (SELECT 1 FROM device WHERE id = ?1 AND user_id = ?2 AND hwid_hash = ?3
			AND kind = 'link' AND revoked_at IS NULL AND blocked_at IS NULL AND key_hash = ?4
			AND enrol_token_hash = (SELECT sub_token_hash FROM user WHERE id = ?2))`,
			dev.ID, dev.UserID, dev.HWIDHash, dev.KeyHash),
	}}, nil
}

// deviceReplacedParams is the params_json of the device_replaced event: the device that gave its place away.
func deviceReplacedParams(v linkVictim) string {
	b, _ := json.Marshal(map[string]string{"old_device": v.ID, "platform": v.Platform, "model": v.Model, "last_seen": strconv.FormatInt(v.LastSeen, 10)})
	return string(b)
}

// EnsureLinkDevice makes the request's link device (dev.HWIDHash, nil for the shared device) exist and hold every
// credential issue returns for it, in one guarded batch, and reads the device back. A device that exists is only filled;
// the first HWID of a user whose shared device holds credentials takes the shared device over, credentials unchanged;
// otherwise a device is born, unless the user already has as many live devices as the stored mode allows for it
// (linkCeiling of limit, the user's device limit): *AccessLimitError, or, for a HWID in ENFORCE and STRICT, the oldest link
// device silent for LinkEvictAfter is revoked in the same batch and its place taken (LinkDeviceResult.Replaced). A device
// is never born empty: with nothing to issue and no device the result is empty and nothing is written.
// Concurrent calls decide through the batch's guards and retry on what the winner left (see retryGuarded): two new HWIDs at
// the ceiling make one device, the same HWID twice makes one, two adoptions make one, two evictions of one silent device make
// one, a block or a mode switch between the read and the write is read by the repeat. ErrNotFound when the user is gone;
// ErrAccessBlocked and ErrNoSharedDevice as in birth.
func (a Access) EnsureLinkDevice(ctx context.Context, dev AccessDevice, limit int, now time.Time, issue LinkIssue) (LinkDeviceResult, error) {
	return a.ensureLinkDevice(ctx, dev, limit, now, issue, false)
}

// EnrolLinkDevice enrols one request device with credentials in the same guarded batch. It never adopts a shared device. A
// matching keyless device keeps its row while its old credentials and AWG peers are revoked/quarantined and fresh credentials
// and hashes are written. An already enrolled row is not replaced.
func (a Access) EnrolLinkDevice(ctx context.Context, dev AccessDevice, limit int, now time.Time, issue LinkIssue) (LinkDeviceResult, error) {
	if dev.UserID == "" || len(dev.HWIDHash) != sha256.Size || len(dev.KeyHash) != sha256.Size || len(dev.EnrolTokenHash) != sha256.Size {
		return LinkDeviceResult{}, errors.New("store: invalid device key hash")
	}
	dev.Kind, dev.Implicit, dev.AWG, dev.Key, dev.CreatedAt = DeviceKindLink, false, false, false, now
	dev.KeyHash = append([]byte(nil), dev.KeyHash...)
	dev.EnrolTokenHash = append([]byte(nil), dev.EnrolTokenHash...)
	return a.ensureLinkDevice(ctx, dev, limit, now, issue, true)
}

func (a Access) ensureLinkDevice(ctx context.Context, dev AccessDevice, limit int, now time.Time, issue LinkIssue, enrol bool) (LinkDeviceResult, error) {
	var result LinkDeviceResult
	results, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		result = LinkDeviceResult{}
		var st linkDeviceState
		r := reads{}
		st.add(&r, dev.UserID, dev.HWIDHash, now)
		if err := r.run(ctx, a.s); err != nil {
			return nil, accMapErr(err)
		}
		if len(st.userIDs) == 0 {
			return nil, ErrNotFound
		}
		own := linkDeviceSnapshotOf(st.own)
		result.Device, result.Creds = own.Device, own.Creds
		b, err := st.birth(dev, limit, now, enrol)
		if err != nil {
			return nil, err
		}
		have := make(map[string]bool, len(b.creds))
		for _, c := range b.creds {
			have[c.Protocol] = true
		}
		if b.enrol {
			have = make(map[string]bool)
		}
		added, err := issue(b.deviceID, have)
		if err != nil {
			return nil, accMapErr(err)
		}
		if enrol && len(added) == 0 {
			return nil, ErrNoLinkCredentials
		}
		if len(added) == 0 && (b.exists || b.born) {
			return nil, nil // nothing to fill, and no device is born empty
		}
		result.Replaced, result.Born, result.Used, result.Mode = b.replaced, b.born, st.used, LinkModeOf(st.mode)
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

// linkDeviceByKeyHashSQL is an index search on device_key_hash (the partial unique index; "key_hash = ?" implies its
// "IS NOT NULL"), then the owner by primary key. TestLinkDeviceByKeyHashUsesTheKeyIndex holds the plan.
const linkDeviceByKeyHashSQL = `SELECT d.id, d.user_id, d.hwid_hash, d.key_hash, d.enrol_token_hash, d.kind,
	coalesce(d.platform, ''), coalesce(d.model, ''), coalesce(d.os_version, ''), coalesce(d.client, ''),
	coalesce(d.first_seen_at, 0), coalesce(d.last_seen_at, 0), d.created_at, d.revoked_at, d.blocked_at, u.id, u.sub_token_hash
	FROM device d JOIN user u ON u.id = d.user_id
	WHERE d.key_hash = ? AND d.kind = 'link'`

// LinkDeviceByKeyHash finds a link device by its globally unique key hash and the owner's small projection (what sync needs).
// A live device comes back with its owner; a key of a removed device is ErrDeviceRevoked, of a blocked one ErrAccessBlocked, and
// an unknown key ErrNotFound. For the first two the device is empty (a caller cannot serve it by mistake) and only the owner
// is returned, so a caller can answer "removed" or "blocked" to the one who holds the key. Revoked rows keep their hashes: a key
// is never reusable.
func (a Access) LinkDeviceByKeyHash(ctx context.Context, keyHash []byte) (AccessDevice, LinkDeviceKeyOwner, error) {
	if len(keyHash) != sha256.Size {
		return AccessDevice{}, LinkDeviceKeyOwner{}, errors.New("store: invalid device key hash")
	}
	var d AccessDevice
	var user LinkDeviceKeyOwner
	var first, last, created int64
	var revoked, blocked sql.NullInt64
	err := a.s.R.QueryRowContext(ctx, linkDeviceByKeyHashSQL, keyHash).Scan(
		&d.ID, &d.UserID, &d.HWIDHash, &d.KeyHash, &d.EnrolTokenHash, &d.Kind,
		&d.Platform, &d.Model, &d.OSVersion, &d.Client, &first, &last, &created,
		&revoked, &blocked, &user.UserID, &user.SubTokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessDevice{}, LinkDeviceKeyOwner{}, ErrNotFound
	}
	if err != nil {
		return AccessDevice{}, LinkDeviceKeyOwner{}, err
	}
	user.SubTokenHash = append([]byte(nil), user.SubTokenHash...)
	if blocked.Valid {
		return AccessDevice{}, user, ErrAccessBlocked
	}
	if revoked.Valid {
		return AccessDevice{}, user, ErrDeviceRevoked
	}
	d.Implicit = d.HWIDHash == nil
	d.FirstSeenAt, d.LastSeenAt, d.CreatedAt = accTimeIfNonzero(sql.NullInt64{Int64: first, Valid: true}), accTimeIfNonzero(sql.NullInt64{Int64: last, Valid: true}), fromUnix(created)
	return d, user, nil
}

// SetLinkDeviceKeyHash changes the key and enrol-token hashes only while the user's live link device has the expected current
// key hash (nil for a first set). The one-statement guard is atomic on both SQLite and D1.
func (a Access) SetLinkDeviceKeyHash(ctx context.Context, userID, deviceID string, expectedKeyHash, keyHash, enrolTokenHash []byte) (bool, error) {
	if userID == "" || deviceID == "" || len(keyHash) != sha256.Size || len(enrolTokenHash) != sha256.Size || (len(expectedKeyHash) != 0 && len(expectedKeyHash) != sha256.Size) {
		return false, errors.New("store: invalid device key hash")
	}
	var expected any
	if len(expectedKeyHash) > 0 {
		expected = expectedKeyHash
	}
	results, err := a.s.batch(ctx, Stmt{Query: `UPDATE device SET key_hash = ?, enrol_token_hash = ?
		WHERE id = ? AND user_id = ? AND kind = 'link' AND revoked_at IS NULL AND key_hash IS ?`,
		Args: []any{keyHash, enrolTokenHash, deviceID, userID, expected}})
	if accIsUnique(err) {
		return false, ErrAccessExists
	}
	if err != nil {
		return false, err
	}
	return len(results) == 1 && results[0].RowsAffected == 1, nil
}

// RevokeEnrolledLinkDevice removes a link device only while the key that authorized the request is still current. It uses
// the same revoke statements as the user's device page, so credentials are removed from desired node state and AWG peers are
// quarantined. A concurrent key rotation, block or removal makes this a no-op.
func (a Access) RevokeEnrolledLinkDevice(ctx context.Context, dev AccessDevice, now time.Time) error {
	if dev.ID == "" || dev.UserID == "" || len(dev.KeyHash) != sha256.Size {
		return errors.New("store: invalid enrolled device revocation")
	}
	stmts := []Stmt{guard(`EXISTS (SELECT 1 FROM device WHERE id = ?1 AND user_id = ?2 AND kind = 'link'
		AND revoked_at IS NULL AND blocked_at IS NULL AND key_hash = ?3)`, dev.ID, dev.UserID, dev.KeyHash)}
	stmts = append(stmts, revokeDeviceStmts(dev.ID, now)...)
	stmts = append(stmts, Stmt{Query: `SELECT user_id FROM device WHERE id = ?`, Args: []any{dev.ID}, Returning: true})
	results, err := a.s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) {
		return ErrNotFound
	}
	if err != nil {
		return accMapErr(err)
	}
	rows := results[len(results)-1].Rows
	if len(rows) == 0 {
		return ErrNotFound
	}
	var userID string
	if err := batchRow(rows[0]).Scan(&userID); err != nil {
		return err
	}
	if userID != dev.UserID {
		return ErrNotFound
	}
	return nil
}
