package store

import (
	"context"
	"database/sql"
	"time"
)

// SettingClientCompat is the owner's changes to the client catalogue (access.ClientCompat): a JSON list of rules that differ
// from the built-in table, or are the owner's own. Absent = the built-in table as it is.
const SettingClientCompat = "client_compat.rules"

// ClientCompatDevice is a subscription-link device as the client catalogue needs it: who, which app, when.
type ClientCompatDevice struct {
	ID, UserID, Platform, Model, OSVersion, Client string
	Shared                                         bool // hwid_hash IS NULL: the shared place of a link
	LastSeenAt                                     time.Time
}

// ClientCompatDevices are the live, not blocked link devices that hold a live VLESS credential, with the user they belong to;
// userID non-empty restricts it to one person. A device without a VLESS credential is never handed a REALITY server, whatever
// its person's group holds. Revoked and blocked devices are gone from the subscription.
func (a Access) ClientCompatDevices(ctx context.Context, userID string) ([]ClientCompatDevice, error) {
	rows, err := a.s.R.QueryContext(ctx,
		`SELECT d.id, d.user_id, coalesce(d.platform, ''), coalesce(d.model, ''), coalesce(d.os_version, ''), coalesce(d.client, ''),
		   CASE WHEN d.hwid_hash IS NULL THEN 1 ELSE 0 END, coalesce(d.last_seen_at, 0)
		 FROM device d
		 WHERE d.kind = 'link' AND d.revoked_at IS NULL AND d.blocked_at IS NULL AND (? = '' OR d.user_id = ?)
		   AND EXISTS (SELECT 1 FROM device_credential c WHERE c.device_id = d.id AND c.protocol = 'vless' AND c.revoked_at IS NULL)
		 ORDER BY d.user_id, d.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientCompatDevice
	for rows.Next() {
		var d ClientCompatDevice
		var shared int64
		var last sql.NullInt64
		if err := rows.Scan(&d.ID, &d.UserID, &d.Platform, &d.Model, &d.OSVersion, &d.Client, &shared, &last); err != nil {
			return nil, err
		}
		d.Shared, d.LastSeenAt = shared != 0, accTimeIfNonzero(last)
		out = append(out, d)
	}
	return out, rows.Err()
}
