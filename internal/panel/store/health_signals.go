package store

import (
	"context"
	"errors"
	"time"
)

// HealthUserSignal holds the stored activity timestamps used by user alerts.
type HealthUserSignal struct {
	ID, Name, Status                   string
	LastTrafficAt, SubscriptionFetchAt time.Time
	AccessEndedAt                      time.Time // expiry time, or the last traffic time for a quota limit
}

// HealthAWGSignal holds the live device data used by never_connected and stale_key.
type HealthAWGSignal struct {
	DeviceID, UserID, UserName, Status string
	CreatedAt, LastHandshakeAt         time.Time
	ConfigEpoch, CriticalEpoch         int64
}

// HealthNodeHourSignal is one peak_users value for the current hour or the same hour on a prior day.
type HealthNodeHourSignal struct {
	NodeID, Protocol string
	Hour             int64
	PeakUsers        uint64
}

// HealthSignalBatch contains the stored inputs for user and fleet impact alerts.
type HealthSignalBatch struct {
	Users      []HealthUserSignal
	AWGDevices []HealthAWGSignal
	NodeHours  []HealthNodeHourSignal
}

func scanHealthUserSignal(r rowScanner) (HealthUserSignal, error) {
	var signal HealthUserSignal
	var trafficAt, fetchAt, endedAt int64
	err := r.Scan(&signal.ID, &signal.Name, &signal.Status, &trafficAt, &fetchAt, &endedAt)
	if err != nil {
		return HealthUserSignal{}, err
	}
	signal.LastTrafficAt = fleetTime(trafficAt)
	signal.SubscriptionFetchAt = fleetTime(fetchAt)
	signal.AccessEndedAt = fleetTime(endedAt)
	return signal, nil
}

func scanHealthAWGSignal(r rowScanner) (HealthAWGSignal, error) {
	var signal HealthAWGSignal
	var createdAt, handshakeAt int64
	err := r.Scan(&signal.DeviceID, &signal.UserID, &signal.UserName, &signal.Status,
		&createdAt, &handshakeAt, &signal.ConfigEpoch, &signal.CriticalEpoch)
	if err != nil {
		return HealthAWGSignal{}, err
	}
	signal.CreatedAt = fleetTime(createdAt)
	signal.LastHandshakeAt = fleetTime(handshakeAt)
	return signal, nil
}

func scanHealthNodeHour(r rowScanner) (HealthNodeHourSignal, error) {
	var signal HealthNodeHourSignal
	var peak int64
	err := r.Scan(&signal.NodeID, &signal.Protocol, &signal.Hour, &peak)
	if err != nil {
		return HealthNodeHourSignal{}, err
	}
	if peak < 0 {
		return HealthNodeHourSignal{}, errors.New("store: negative health node-hour peak")
	}
	signal.PeakUsers = uint64(peak)
	return signal, nil
}

// HealthSignals reads the user, AWG and hourly fleet inputs in one read batch.
func (s *Store) HealthSignals(ctx context.Context, now time.Time) (HealthSignalBatch, error) {
	var out HealthSignalBatch
	currentHour := now.UTC().Truncate(time.Hour).Unix()
	daySeconds := int64((24 * time.Hour) / time.Second)
	hourStarts := make([]any, 0, 8)
	for daysAgo := int64(0); daysAgo <= 7; daysAgo++ {
		hourStarts = append(hourStarts, currentHour-daysAgo*daySeconds)
	}
	var reads reads
	reads.add(appendRows(&out.Users, scanHealthUserSignal), `
		SELECT u.id AS user_id, u.name AS user_name, u.status AS user_status,
		       u.last_seen_at AS last_traffic_at, COALESCE(d.last_seen_at, 0) AS subscription_fetch_at,
		       CASE WHEN u.status = 'expired' THEN COALESCE(u.expires_at, 0) ELSE u.last_seen_at END AS access_ended_at
		FROM user u
		LEFT JOIN device d ON d.user_id = u.id AND d.hwid_hash IS NULL AND d.revoked_at IS NULL
		WHERE u.status IN ('active', 'expired', 'limited')`)
	reads.add(appendRows(&out.AWGDevices, scanHealthAWGSignal), `
		SELECT d.id AS device_id, u.id AS user_id, u.name AS user_name, u.status AS user_status,
		       d.created_at AS created_at, d.last_seen_at AS last_handshake_at,
		       c.config_epoch AS config_epoch, p.critical_epoch AS critical_epoch
		FROM device d
		JOIN user u ON u.id = d.user_id
		JOIN device_credential c ON c.device_id = d.id AND c.protocol = 'awg' AND c.revoked_at IS NULL AND c.profile_id IS NOT NULL
		JOIN profile p ON p.id = c.profile_id
		JOIN awg_peer ap ON ap.credential_id = c.id AND ap.released_at = 0
		WHERE d.revoked_at IS NULL AND d.hwid_hash IS NOT NULL`)
	reads.add(appendRows(&out.NodeHours, scanHealthNodeHour), `
		SELECT node_id AS node_id, protocol AS protocol, hour_start AS hour_start, peak_users AS peak_users
		FROM node_traffic_hour
		WHERE hour_start IN (?, ?, ?, ?, ?, ?, ?, ?)`, hourStarts...)
	if err := reads.run(ctx, s); err != nil {
		return HealthSignalBatch{}, err
	}
	return out, nil
}
