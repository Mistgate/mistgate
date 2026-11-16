//go:build !js

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// accLiveTrustTunnelSQL is "user <user> holds a live tt:// key", user being a placeholder. Only a device of kind key counts: the
// person's Mistgate app devices carry TrustTunnel credentials of their own (protocol trusttunnel too) and must not stand in the
// key's way.
func accLiveTrustTunnelSQL(user string) string {
	return `EXISTS (SELECT 1 FROM device_credential c JOIN device kd ON kd.id = c.device_id AND kd.kind = 'key' AND kd.revoked_at IS NULL
		WHERE c.user_id = ` + user + ` AND c.protocol = '` + TrustTunnelProtocol + `' AND c.revoked_at IS NULL)`
}

// TrustTunnelKeyAdd describes a single key device and its credential.
type TrustTunnelKeyAdd struct {
	Device AccessDevice
	Cred   AccessCred
	Limit  int
}

// AddTrustTunnelKey adds the person's one live TrustTunnel key. Unlike the Telegram key, it takes a device-limit place.
func (a Access) AddTrustTunnelKey(ctx context.Context, add TrustTunnelKeyAdd, now time.Time) error {
	_, err := a.s.retryGuarded(ctx, func() ([]Stmt, error) {
		var held, used int
		if err := a.s.R.QueryRowContext(ctx, `SELECT CAST(`+accLiveTrustTunnelSQL("?")+` AS INTEGER)`, add.Device.UserID).Scan(&held); err != nil {
			return nil, accMapErr(err)
		}
		if held > 0 {
			return nil, ErrAccessExists
		}
		if err := a.s.R.QueryRowContext(ctx, accLimitDevices("?", now), add.Device.UserID).Scan(&used); err != nil {
			return nil, accMapErr(err)
		}
		if used >= add.Limit {
			return nil, &AccessLimitError{Used: used, Limit: add.Limit}
		}
		device, cred := add.Device, add.Cred
		device.Key, device.CreatedAt = true, now
		cred.DeviceID, cred.UserID, cred.ProfileID, cred.Path, cred.CreatedAt = device.ID, device.UserID, "", "", now
		// One guarded batch, like AddAWGDevice: the place is counted where the key is written, so two issues (or an issue and a
		// device registering) that both saw a free place cannot both take it.
		guardSQL := `NOT ` + accLiveTrustTunnelSQL("?1") + ` AND (` + accLimitDevices("?2", now) + `) < ?3`
		stmts := []Stmt{guard(guardSQL, device.UserID, device.UserID, int64(add.Limit)), accInsertDeviceStmt(device)}
		return append(stmts, accInsertCredStmts([]AccessCred{cred})...), nil
	})
	if errors.Is(err, errGuard) {
		var used int
		if readErr := a.s.R.QueryRowContext(ctx, accLimitDevices("?", now), add.Device.UserID).Scan(&used); readErr != nil {
			return accMapErr(readErr)
		}
		if used >= add.Limit {
			return &AccessLimitError{Used: used, Limit: add.Limit}
		}
	}
	return accMapErr(err)
}

// TrustTunnelKey returns the person's live TrustTunnel key, or ErrNotFound.
func (a Access) TrustTunnelKey(ctx context.Context, userID string) (AccessDevice, AccessCred, error) {
	var cred AccessCred
	var created, deviceCreated int64
	var model, platform, client string
	err := a.s.R.QueryRowContext(ctx,
		`SELECT c.id, c.device_id, c.user_id, c.protocol, c.secret_enc, c.data_json, c.config_epoch, c.created_at,
		        d.created_at, d.model, d.platform, d.client
		 FROM device_credential c JOIN device d ON d.id = c.device_id AND d.revoked_at IS NULL AND d.kind = 'key'
		 WHERE c.user_id = ? AND c.protocol = ? AND c.revoked_at IS NULL`, userID, TrustTunnelProtocol).
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
		CreatedAt: fromUnix(deviceCreated), Protocols: []string{TrustTunnelProtocol}}, cred, nil
}

// RevokeTrustTunnelKey revokes the key's device and credential, freeing its device-limit place.
func (a Access) RevokeTrustTunnelKey(ctx context.Context, userID string, now time.Time) (bool, error) {
	dev, _, err := a.TrustTunnelKey(ctx, userID)
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
