//go:build !js

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// The rows behind client_debug (migration 00131): the logs people send for support. The owner asks a device with a key for them
// (an open request, 72 hours), or a person sends a bundle on their own; the bundle's lines are debug_capture_line rows under the
// row's id ("dbr_…"). The OpsService of the VPS edition and the client API's debug routes are the callers; the Cloudflare edition
// has none, so this file is not in the Workers' wasm.

const (
	// ClientDebugRequestTTL is how long a request of the owner waits for the person.
	ClientDebugRequestTTL = 72 * time.Hour
	// ClientDebugKeep is how long a row and its lines are kept, counted from the request (the bundle sent unasked: from its receipt).
	ClientDebugKeep = 7 * 24 * time.Hour
	// ClientDebugPerDevice is how many received bundles of one device are kept: an answer to a request drops the device's oldest beyond
	// it, with its lines. It is also how many bundles a person keeps that they sent on their own, whatever devices sent them.
	ClientDebugPerDevice = 3
	// ClientDebugMaxBundles is how many received bundles the panel keeps in all. A bundle sent unasked past it is refused
	// (ErrClientDebugFull); the owner's requests are never refused for it, but their bundles count.
	ClientDebugMaxBundles = 200
	// ClientDebugMaxBytes is the most the received bundles may weigh together (what their records cost, ClientDebugRow.Bytes): a bundle
	// sent unasked that would pass it is refused like one past ClientDebugMaxBundles.
	ClientDebugMaxBytes = 256 << 20
	// ClientDebugUnaskedEvery is the least time between two bundles a device sends unasked.
	ClientDebugUnaskedEvery = 10 * time.Minute
	// ClientDebugNoteRunes is the longest note of a request.
	ClientDebugNoteRunes = 200
	// ClientDebugIDPrefix starts every id; no node capture's id does.
	ClientDebugIDPrefix = "dbr_"
)

var (
	// ErrClientDebugOpen: the device already has an open request.
	ErrClientDebugOpen = errors.New("store: this device already has an open log request")
	// ErrClientDebugClosed: the request is not open any more (answered, cancelled, declined, expired).
	ErrClientDebugClosed = errors.New("store: the log request is not open")
	// ErrClientDebugNoRequest: no open request of this device has the id.
	ErrClientDebugNoRequest = errors.New("store: no open log request")
	// ErrClientDebugStillOpen: the request is open and has not run out; it is cancelled, not deleted.
	ErrClientDebugStillOpen = errors.New("store: the log request is still open")
	// ErrClientDebugFull: the panel keeps ClientDebugMaxBundles bundles already.
	ErrClientDebugFull = errors.New("store: the panel keeps as many logs as it takes")
)

// ClientDebugRateError: this device sent a bundle unasked less than ClientDebugUnaskedEvery ago; After is how long to wait.
type ClientDebugRateError struct{ After time.Duration }

func (e *ClientDebugRateError) Error() string {
	return "store: a device sends logs unasked at most once in ten minutes"
}

// NewClientDebugID is a new id: "dbr_" and 16 random bytes in base64url.
func NewClientDebugID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on a working system
	}
	return ClientDebugIDPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ClientDebugRow is one request or bundle. The names are resolved on read (empty when the person, the device or the admin is gone).
type ClientDebugRow struct {
	ID, UserID, DeviceID string
	Actor                string // as stored: the admin who asked ("" for a bundle sent unasked)
	ActorName            string // the admin's display name, a token's name, else Actor
	Unasked              bool   // the person sent it without a request
	Note, Level          string // "info" or "debug"
	RequestedAt          time.Time
	ExpiresAt            time.Time
	// State is open, received, declined, cancelled, or expired (a request of the owner whose time passed: set at read, and by the next request).
	State                 string
	ClosedAt              time.Time // zero while open
	Records, Bytes        int
	DroppedKV             int
	FirstMs, LastMs       int64
	App                   string
	ReadAt                time.Time // zero until the owner reads a bundle
	ReadBy                string
	UserName              string
	DevicePlatform        string
	DeviceModel, DeviceOS string
}

// ClientDebugLine is one record of a bundle, cleaned. Level: 0 debug, 1 info, 2 warn, 3 error.
type ClientDebugLine struct {
	AtMs            int64
	Level           int
	Source, Message string
	Attrs           map[string]string
}

// ClientDebugIntake is a bundle to store. RequestID names the open request it answers; empty = sent unasked (NewID is then its id).
type ClientDebugIntake struct {
	RequestID, NewID string
	UserID, DeviceID string
	App              string
	Lines            []ClientDebugLine
	Bytes, DroppedKV int
	FirstMs, LastMs  int64
	Now              time.Time
}

const clientDebugColumns = `c.id, c.user_id, c.device_id, c.actor, COALESCE(NULLIF(a.display_name, ''), NULLIF(t.name, ''), c.actor), c.unasked, c.note, c.level,
	c.requested_at, c.expires_at, c.state, c.closed_at, c.records, c.bytes, c.dropped_kv, c.first_ms, c.last_ms, c.app, c.read_at, c.read_by,
	COALESCE(u.name, ''), COALESCE(d.platform, ''), COALESCE(d.model, ''), COALESCE(d.os_version, '')`

const clientDebugFrom = ` FROM client_debug c LEFT JOIN admin a ON a.id = c.actor LEFT JOIN api_token t ON c.actor IN ('mcp:' || t.id, 'token:' || t.id)
	LEFT JOIN user u ON u.id = c.user_id LEFT JOIN device d ON d.id = c.device_id`

func scanClientDebug(r rowScanner, now time.Time) (ClientDebugRow, error) {
	var c ClientDebugRow
	var unasked int
	var requested, expires, closed, read int64
	if err := r.Scan(&c.ID, &c.UserID, &c.DeviceID, &c.Actor, &c.ActorName, &unasked, &c.Note, &c.Level,
		&requested, &expires, &c.State, &closed, &c.Records, &c.Bytes, &c.DroppedKV, &c.FirstMs, &c.LastMs, &c.App, &read, &c.ReadBy,
		&c.UserName, &c.DevicePlatform, &c.DeviceModel, &c.DeviceOS); err != nil {
		return c, err
	}
	c.Unasked = unasked == 1
	c.RequestedAt, c.ExpiresAt = fromUnix(requested), fromUnix(expires)
	if closed != 0 {
		c.ClosedAt = fromUnix(closed)
	}
	if read != 0 {
		c.ReadAt = fromUnix(read)
	}
	if c.State == "open" && !now.IsZero() && !now.Before(c.ExpiresAt) {
		c.State = "expired"
	}
	return c, nil
}

// ClientDebug returns one row, or ErrNotFound. An open request whose time passed reads as expired at now.
func (s *Store) ClientDebug(ctx context.Context, id string, now time.Time) (ClientDebugRow, error) {
	row, err := scanClientDebug(s.R.QueryRowContext(ctx, `SELECT `+clientDebugColumns+clientDebugFrom+` WHERE c.id = ?`, id), now)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// OpenClientDebug is the device's open request that has not run out at now.
func (s *Store) OpenClientDebug(ctx context.Context, deviceID string, now time.Time) (ClientDebugRow, bool, error) {
	row, err := scanClientDebug(s.R.QueryRowContext(ctx, `SELECT `+clientDebugColumns+clientDebugFrom+` WHERE c.device_id = ? AND c.state = 'open' AND c.expires_at > ?`,
		deviceID, unix(now)), now)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	return row, err == nil, err
}

// ClientDebugList lists the requests and bundles of a person (everyone when userID is empty), newest first, at most 100.
func (s *Store) ClientDebugList(ctx context.Context, userID string, now time.Time) ([]ClientDebugRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+clientDebugColumns+clientDebugFrom+` WHERE (? = '' OR c.user_id = ?)
		ORDER BY c.requested_at DESC, c.id DESC LIMIT 100`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientDebugRow
	for rows.Next() {
		c, err := scanClientDebug(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateClientDebug opens a request of the owner for a device with a key. ErrNotFound: the device is none, revoked or has no key;
// ErrClientDebugOpen: it has an open request that has not run out (one that has is expired by this call, in the same batch).
// row carries ID, DeviceID, Actor, Note and Level; the person and the times are set here.
func (s *Store) CreateClientDebug(ctx context.Context, row ClientDebugRow, now time.Time) (ClientDebugRow, error) {
	const liveDevice = `SELECT 1 FROM device WHERE id = ? AND key_hash IS NOT NULL AND revoked_at IS NULL`
	_, err := s.batch(ctx,
		Stmt{Query: `UPDATE client_debug SET state = 'expired', closed_at = expires_at WHERE device_id = ? AND state = 'open' AND expires_at <= ?`,
			Args: []any{row.DeviceID, unix(now)}},
		guard(liveDevice, row.DeviceID),
		guard(`SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM client_debug WHERE device_id = ? AND state = 'open')`, row.DeviceID),
		Stmt{Query: `INSERT INTO client_debug (id, user_id, device_id, actor, note, level, requested_at, expires_at, state)
			SELECT ?, user_id, id, ?, ?, ?, ?, ?, 'open' FROM device WHERE id = ?`,
			Args: []any{row.ID, row.Actor, row.Note, row.Level, unix(now), unix(now.Add(ClientDebugRequestTTL)), row.DeviceID}})
	if errors.Is(err, errGuard) {
		var live int
		if err := s.R.QueryRowContext(ctx, liveDevice, row.DeviceID).Scan(&live); errors.Is(err, sql.ErrNoRows) {
			return row, ErrNotFound
		} else if err != nil {
			return row, err
		}
		return row, ErrClientDebugOpen
	}
	if err != nil {
		return row, err
	}
	return s.ClientDebug(ctx, row.ID, now)
}

// CancelClientDebug withdraws an open request that has not run out. ErrNotFound: no such row; ErrClientDebugClosed: it is not open.
func (s *Store) CancelClientDebug(ctx context.Context, id string, now time.Time) (ClientDebugRow, error) {
	return s.closeClientDebug(ctx, id, "", "cancelled", now)
}

// DeclineClientDebug is the person's refusal of the open request of this device. ErrClientDebugNoRequest: no such request is open.
func (s *Store) DeclineClientDebug(ctx context.Context, deviceID, id string, now time.Time) (ClientDebugRow, error) {
	row, err := s.closeClientDebug(ctx, id, deviceID, "declined", now)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrClientDebugClosed) {
		return row, ErrClientDebugNoRequest
	}
	return row, err
}

func (s *Store) closeClientDebug(ctx context.Context, id, deviceID, state string, now time.Time) (ClientDebugRow, error) {
	res, err := s.batch(ctx, Stmt{Query: `UPDATE client_debug SET state = ?, closed_at = ? WHERE id = ? AND (? = '' OR device_id = ?) AND state = 'open' AND expires_at > ?`,
		Args: []any{state, unix(now), id, deviceID, deviceID, unix(now)}})
	if err != nil {
		return ClientDebugRow{}, err
	}
	row, err := s.ClientDebug(ctx, id, now)
	if err != nil {
		return row, err
	}
	if deviceID != "" && row.DeviceID != deviceID {
		return ClientDebugRow{}, ErrNotFound
	}
	if len(res) != 1 || res[0].RowsAffected != 1 {
		return row, ErrClientDebugClosed
	}
	return row, nil
}

// ClientDebugAdmitUnasked says whether a device may send a bundle on its own now: nil, a *ClientDebugRateError with the wait, or
// ErrClientDebugFull. It is the cheap check made before the body is read; ReceiveClientDebug makes it again in the write.
func (s *Store) ClientDebugAdmitUnasked(ctx context.Context, deviceID string, now time.Time) error {
	var last int64
	if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(requested_at), 0) FROM client_debug WHERE device_id = ? AND unasked = 1`, deviceID).Scan(&last); err != nil {
		return err
	}
	if wait := time.Unix(last, 0).Add(ClientDebugUnaskedEvery).Sub(now); last != 0 && wait > 0 {
		return &ClientDebugRateError{After: wait}
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM client_debug WHERE state = 'received'`).Scan(&n); err != nil {
		return err
	}
	if n >= ClientDebugMaxBundles {
		return ErrClientDebugFull
	}
	var weight int64
	if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM client_debug WHERE state = 'received'`).Scan(&weight); err != nil {
		return err
	}
	if weight >= ClientDebugMaxBytes {
		return ErrClientDebugFull
	}
	return nil
}

// ReceiveClientDebug stores a bundle in one batch: the lines, the counters, the state, and the drop of the device's bundles beyond
// ClientDebugPerDevice (the oldest, with their lines). With RequestID the bundle answers that open request of the device; if it
// is answered already (by this bundle, a moment before) the stored row comes back with replay true and nothing is written; any
// other state is ErrClientDebugNoRequest. Without one it is a bundle the person sent on their own: refused with a
// *ClientDebugRateError within ClientDebugUnaskedEvery of the device's last, and with ErrClientDebugFull past ClientDebugMaxBundles
// or ClientDebugMaxBytes; it pushes out only the person's own unasked bundles beyond ClientDebugPerDevice.
func (s *Store) ReceiveClientDebug(ctx context.Context, in ClientDebugIntake) (row ClientDebugRow, replay bool, err error) {
	id, now := in.RequestID, unix(in.Now)
	unasked := id == ""
	if unasked {
		id = in.NewID
	}
	var stmts []Stmt
	if unasked {
		stmts = append(stmts,
			guard(`SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM client_debug WHERE device_id = ? AND unasked = 1 AND requested_at > ?)`, in.DeviceID, now-int64(ClientDebugUnaskedEvery/time.Second)),
			guard(`SELECT COUNT(*) < ? FROM client_debug WHERE state = 'received'`, ClientDebugMaxBundles),
			guard(`SELECT COALESCE(SUM(bytes), 0) + ? <= ? FROM client_debug WHERE state = 'received'`, in.Bytes, ClientDebugMaxBytes),
			Stmt{Query: `INSERT INTO client_debug (id, user_id, device_id, unasked, requested_at, expires_at, state, closed_at, records, bytes, dropped_kv, first_ms, last_ms, app)
				VALUES (?, ?, ?, 1, ?, ?, 'received', ?, ?, ?, ?, ?, ?, ?)`,
				Args: []any{id, in.UserID, in.DeviceID, now, now, now, len(in.Lines), in.Bytes, in.DroppedKV, in.FirstMs, in.LastMs, in.App}})
	} else {
		stmts = append(stmts, guard(`SELECT 1 FROM client_debug WHERE id = ? AND device_id = ? AND state = 'open' AND expires_at > ?`, id, in.DeviceID, now))
	}
	for i, l := range in.Lines {
		attrs, err := plainJSON(l.Attrs)
		if err != nil {
			return row, false, err
		}
		if l.Attrs == nil {
			attrs = "{}"
		}
		stmts = append(stmts, Stmt{Query: `INSERT INTO debug_capture_line (capture_id, seq, at_ms, level, source, message, attrs_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{id, i + 1, l.AtMs, l.Level, l.Source, l.Message, attrs}})
	}
	if !unasked {
		stmts = append(stmts, Stmt{Query: `UPDATE client_debug SET state = 'received', closed_at = ?, records = ?, bytes = ?, dropped_kv = ?, first_ms = ?, last_ms = ?, app = ?
			WHERE id = ? AND state = 'open'`, Args: []any{now, len(in.Lines), in.Bytes, in.DroppedKV, in.FirstMs, in.LastMs, in.App, id}})
	}
	// What goes to make room, this bundle always kept. A bundle the person sent on their own pushes out only the person's own bundles sent
	// on their own, the oldest beyond ClientDebugPerDevice whatever device sent them (a person who leaves and comes back as a new device
	// has the same three); a bundle the owner asked for is never pushed out by them. An answer to a request pushes out the device's
	// oldest bundles beyond ClientDebugPerDevice, of either kind.
	scope, owner := `device_id = ? AND state = 'received'`, in.DeviceID
	if unasked {
		scope, owner = `user_id = ? AND unasked = 1 AND state = 'received'`, in.UserID
	}
	older := `SELECT id FROM client_debug WHERE ` + scope + ` AND id <> ? AND id NOT IN
		(SELECT id FROM client_debug WHERE ` + scope + ` AND id <> ? ORDER BY closed_at DESC, rowid DESC LIMIT ?)`
	args := []any{owner, id, owner, id, ClientDebugPerDevice - 1}
	stmts = append(stmts,
		Stmt{Query: `DELETE FROM debug_capture_line WHERE capture_id IN (` + older + `)`, Args: args},
		Stmt{Query: `DELETE FROM client_debug WHERE id IN (` + older + `)`, Args: args})
	_, err = s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) {
		if unasked {
			if err := s.ClientDebugAdmitUnasked(ctx, in.DeviceID, in.Now); err != nil {
				return row, false, err
			}
			var weight int64
			if err := s.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM client_debug WHERE state = 'received'`).Scan(&weight); err != nil {
				return row, false, err
			} else if weight+int64(in.Bytes) > ClientDebugMaxBytes {
				return row, false, ErrClientDebugFull
			}
			return row, false, ErrConflict // a writer changed the picture between the guard and the read: the person tries again
		}
		fresh, readErr := s.ClientDebug(ctx, id, in.Now)
		if readErr != nil && !errors.Is(readErr, ErrNotFound) {
			return row, false, readErr
		}
		if readErr == nil && fresh.DeviceID == in.DeviceID && fresh.State == "received" {
			return fresh, true, nil
		}
		return row, false, ErrClientDebugNoRequest
	}
	if err != nil {
		return row, false, err
	}
	row, err = s.ClientDebug(ctx, id, in.Now)
	return row, false, err
}

// PruneClientDebug deletes the rows requested before the cut, those whose person or device is gone, and the lines of all of them
// (lines are only ever written and deleted together with their row, in one batch, so none is left behind: the sweep does not scan
// the line table for strays). It returns how many rows went.
func (s *Store) PruneClientDebug(ctx context.Context, before time.Time) (int64, error) {
	gone := `requested_at < ? OR device_id NOT IN (SELECT id FROM device) OR user_id NOT IN (SELECT id FROM user)`
	cut := unix(before)
	res, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM debug_capture_line WHERE capture_id IN (SELECT id FROM client_debug WHERE ` + gone + `)`, Args: []any{cut}},
		Stmt{Query: `DELETE FROM client_debug WHERE ` + gone, Args: []any{cut}})
	if err != nil {
		return 0, err
	}
	if len(res) != 2 {
		return 0, nil
	}
	return res[1].RowsAffected, nil
}

// ClientDebugLines returns the records of a bundle, oldest first after AfterSeq, with the filter applied, and the sources the bundle
// holds. The text comes back as it was stored: the cleaning of a bundle happened on intake, and the node journal's scrubber (which
// turns state.json into <host> and a long path into <key>) is not applied to it. The caller filters by attribute key and passes the
// text through the panel's current secret names. A node capture's id finds no lines here: the ids do not meet.
func (s *Store) ClientDebugLines(ctx context.Context, id string, f DebugLineFilter) ([]DebugLineRow, []string, error) {
	return s.debugLines(ctx, id, f, nil)
}

// MarkClientDebugRead records the owner's first read of a received bundle: read_at and read_by are set only while read_at is 0, so
// first is true for one caller only (the one that writes the audit row).
func (s *Store) MarkClientDebugRead(ctx context.Context, id, by string, now time.Time) (first bool, err error) {
	res, err := s.batch(ctx, Stmt{Query: `UPDATE client_debug SET read_at = ?, read_by = ? WHERE id = ? AND read_at = 0 AND state = 'received'`,
		Args: []any{unix(now), by, id}})
	if err != nil {
		return false, err
	}
	return len(res) == 1 && res[0].RowsAffected == 1, nil
}

// DeleteClientDebug deletes a closed request or a bundle, with its lines, in one batch. ErrNotFound: no such row;
// ErrClientDebugStillOpen: it is open and has not run out. It returns the row as it was.
func (s *Store) DeleteClientDebug(ctx context.Context, id string, now time.Time) (ClientDebugRow, error) {
	row, err := s.ClientDebug(ctx, id, now)
	if err != nil {
		return row, err
	}
	if row.State == "open" {
		return row, ErrClientDebugStillOpen
	}
	_, err = s.batch(ctx,
		guard(`SELECT 1 FROM client_debug WHERE id = ? AND NOT (state = 'open' AND expires_at > ?)`, id, unix(now)),
		Stmt{Query: `DELETE FROM debug_capture_line WHERE capture_id = ?`, Args: []any{id}},
		Stmt{Query: `DELETE FROM client_debug WHERE id = ?`, Args: []any{id}})
	if errors.Is(err, errGuard) {
		if _, err := s.ClientDebug(ctx, id, now); errors.Is(err, ErrNotFound) {
			return row, ErrNotFound
		}
		return row, ErrClientDebugStillOpen
	}
	return row, err
}
