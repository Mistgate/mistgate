package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
)

// The Telegram key of a person: one device of kind 'key' that holds ONE live credential of protocol 'mtproto' (no profile:
// the same secret works on every MTProto profile the person may use). Like an AmneziaVPN key it is made on request and is
// revoked with RevokeDevice, and it is listed among the person's devices. Unlike any other device it does NOT count against
// the device limit (the owner's decision): making it never fails for want of room, and it is left out of every count that
// the limit is enforced or shown by (accLimitDevice, SubscriptionData.LimitCount). Nothing else holds an mtproto credential,
// so a link device never carries one.

// TelegramProtocol is the protocol id of the MTProto credential.
const TelegramProtocol = "mtproto"

// accNotTelegramKey is the SQL condition "device d is not a Telegram key": it holds no live mtproto credential.
const accNotTelegramKey = `NOT EXISTS (SELECT 1 FROM device_credential tk WHERE tk.device_id = d.id AND tk.protocol = '` + TelegramProtocol + `' AND tk.revoked_at IS NULL)`

// accLimitDevice is the SQL condition "device d fills the device limit": it holds a live credential and is not the Telegram key.
// It is the one definition every count of the limit uses (the AmneziaVPN key and link-device checks, the counts of the user
// views and lists); to change how the Telegram key counts, change it here and in SubscriptionData.LimitCount.
const accLimitDevice = accDeviceLive + ` AND ` + accNotTelegramKey

// accLimitDevices is the SQL that counts the devices of the user (the placeholder user) that fill the device limit.
func accLimitDevices(user string) string {
	return `SELECT count(*) FROM device d WHERE d.user_id = ` + user + ` AND d.revoked_at IS NULL AND ` + accLimitDevice
}

// IsTelegramKey: the device is the person's Telegram key, which the device limit does not count.
func (d AccessDevice) IsTelegramKey() bool {
	return d.Kind == DeviceKindKey && slices.Contains(d.Protocols, TelegramProtocol)
}

// accLiveTelegramSQL is the condition "user ?1 holds a Telegram key".
const accLiveTelegramSQL = `EXISTS (SELECT 1 FROM device_credential c WHERE c.user_id = ?1 AND c.protocol = '` + TelegramProtocol + `' AND c.revoked_at IS NULL)`

// TelegramKeyAdd describes a new Telegram key.
type TelegramKeyAdd struct {
	Device AccessDevice // ID, UserID, Platform, Model, Client; the store sets the kind and CreatedAt
	Cred   AccessCred   // ID, Protocol, SecretEnc, DataJSON; the store sets the owner, the device and CreatedAt
}

// AddTelegramKey inserts the key device with its credential in one guarded batch. The device limit does not apply to it.
// ErrAccessExists when the user holds a key already, ErrNotFound when the user is gone.
func (a Access) AddTelegramKey(ctx context.Context, add TelegramKeyAdd, now time.Time) error {
	_, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		var held int
		if err := a.s.R.QueryRowContext(ctx, `SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = ? AND revoked_at IS NULL`,
			add.Device.UserID, TelegramProtocol).Scan(&held); err != nil {
			return nil, accMapErr(err)
		}
		if held > 0 {
			return nil, ErrAccessExists
		}
		device, cred := add.Device, add.Cred
		device.Key, device.CreatedAt = true, now
		cred.DeviceID, cred.UserID, cred.ProfileID, cred.CreatedAt = device.ID, device.UserID, "", now
		stmts := []Stmt{
			guard(`NOT `+accLiveTelegramSQL, device.UserID),
			accInsertDeviceStmt(device),
		}
		return append(stmts, accInsertCredStmts([]AccessCred{cred})...), nil
	})
	return accMapErr(err)
}

// TelegramKey returns the user's live Telegram key: its device and credential. ErrNotFound when there is none.
func (a Access) TelegramKey(ctx context.Context, userID string) (AccessDevice, AccessCred, error) {
	var cred AccessCred
	var created, deviceCreated int64
	var model, platform, client string
	err := a.s.R.QueryRowContext(ctx,
		`SELECT c.id, c.device_id, c.user_id, c.protocol, c.secret_enc, c.data_json, c.config_epoch, c.created_at,
		        d.created_at, d.model, d.platform, d.client
		 FROM device_credential c JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL AND d.kind = 'key'
		 WHERE c.user_id = ? AND c.protocol = ? AND c.revoked_at IS NULL`, userID, TelegramProtocol).
		Scan(&cred.ID, &cred.DeviceID, &cred.UserID, &cred.Protocol, &cred.SecretEnc, &cred.DataJSON, &cred.ConfigEpoch, &created,
			&deviceCreated, &model, &platform, &client)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessDevice{}, AccessCred{}, ErrNotFound
	}
	if err != nil {
		return AccessDevice{}, AccessCred{}, err
	}
	cred.CreatedAt = fromUnix(created)
	return AccessDevice{ID: cred.DeviceID, UserID: userID, Kind: DeviceKindKey, Model: model, Platform: platform, Client: client,
		CreatedAt: fromUnix(deviceCreated), Protocols: []string{TelegramProtocol}}, cred, nil
}

// TelegramKeyUsers maps the given users to the device of their Telegram key; users without one are absent.
func (a Access) TelegramKeyUsers(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT c.user_id, c.device_id FROM device_credential c JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL
		 WHERE c.protocol = ? AND c.revoked_at IS NULL AND c.user_id IN (SELECT value FROM json_each(?))`, TelegramProtocol, accJSON(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var user, device string
		if err := rows.Scan(&user, &device); err != nil {
			return nil, err
		}
		out[user] = device
	}
	return out, rows.Err()
}

// RotateTelegramKey replaces the credential of the user's key (the device stays, so its place and label do): the old one is
// revoked and cred, with the new secret, takes its place in one guarded batch. It returns the key's device.
// ErrNotFound when the user has no key.
func (a Access) RotateTelegramKey(ctx context.Context, userID string, cred AccessCred, now time.Time) (string, error) {
	var deviceID string
	_, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		var oldID string
		err := a.s.R.QueryRowContext(ctx,
			`SELECT c.id, c.device_id FROM device_credential c JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL AND d.kind = 'key'
			 WHERE c.user_id = ? AND c.protocol = ? AND c.revoked_at IS NULL`, userID, TelegramProtocol).Scan(&oldID, &deviceID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, accMapErr(err)
		}
		next := cred
		next.DeviceID, next.UserID, next.ProfileID, next.CreatedAt = deviceID, userID, "", now
		stmts := []Stmt{
			guard(`EXISTS (SELECT 1 FROM device_credential WHERE id = ? AND revoked_at IS NULL)`, oldID),
			{Query: `UPDATE device_credential SET revoked_at = ? WHERE id = ?`, Args: []any{unix(now), oldID}},
		}
		return append(stmts, accInsertCredStmts([]AccessCred{next})...), nil
	})
	if err != nil {
		return "", accMapErr(err)
	}
	return deviceID, nil
}

// RevokeTelegramKey revokes the user's Telegram key (see RevokeDevice) and reports whether there was one.
func (a Access) RevokeTelegramKey(ctx context.Context, userID string, now time.Time) (bool, error) {
	dev, _, err := a.TelegramKey(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := a.RevokeDevice(ctx, dev.ID, now); err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	return true, nil
}
