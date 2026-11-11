package store

import (
	"context"
	"sort"
	"time"
)

// HelloKexDevice is one device's ClientHello counts over a window (link_hour hello_pq, hello_classic, hello_unknown from
// migration 00127, summed over every node, inbound and key of the device).
type HelloKexDevice struct {
	DeviceID, UserID, UserName, Client, Platform string
	PQ, Classic, Unknown                         uint64
	LastHour                                     time.Time
}

// HelloKexFleet is the fleet's ClientHellos by device: how many devices sent only post-quantum hellos, only classic ones, both,
// or only ones that could not be read, and the devices with classic hellos, most first.
type HelloKexFleet struct {
	PQOnly, ClassicOnly, Mixed, UnknownOnly uint64
	Classic                                 []HelloKexDevice
}

// helloKexDevices reads the hour buckets that touch [since, now] of the nodes that are not retired, per device that still exists
// (a revoked device can not connect, so it can not break), of one person (userID) or of everyone (""), in no order.
func (s *Store) helloKexDevices(ctx context.Context, since time.Time, userID string) ([]HelloKexDevice, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT d.id, d.user_id, u.name, d.client, d.platform,
			SUM(l.hello_pq), SUM(l.hello_classic), SUM(l.hello_unknown), MAX(l.hour_start)
		FROM link_hour l
		JOIN node n ON n.id = l.node_id AND n.state <> 'retired'
		JOIN device_credential dc ON dc.id = l.cred_id
		JOIN device d ON d.id = dc.device_id AND d.revoked_at IS NULL
		JOIN user u ON u.id = d.user_id
		WHERE l.hour_start >= ? AND l.cred_id <> '' AND l.hello_pq + l.hello_classic + l.hello_unknown > 0 AND (? = '' OR d.user_id = ?)
		GROUP BY d.id`, linkHourOf(since), userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HelloKexDevice
	var last int64
	for rows.Next() {
		var d HelloKexDevice
		if err := rows.Scan(&d.DeviceID, &d.UserID, &d.UserName, &d.Client, &d.Platform, &d.PQ, &d.Classic, &d.Unknown, &last); err != nil {
			return nil, err
		}
		d.LastHour = fromUnix(last)
		out = append(out, d)
	}
	return out, rows.Err()
}

// UserHelloKex is one person's devices with ClientHello counts over the window, by device id.
func (s *Store) UserHelloKex(ctx context.Context, userID string, since time.Time) ([]HelloKexDevice, error) {
	out, err := s.helloKexDevices(ctx, since, userID)
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out, err
}

// HelloKex reads every person's devices (helloKexDevices) and sorts them into the fleet's classes. The list of classic devices is
// cut to limit.
func (s *Store) HelloKex(ctx context.Context, since time.Time, limit int) (HelloKexFleet, error) {
	devices, err := s.helloKexDevices(ctx, since, "")
	if err != nil {
		return HelloKexFleet{}, err
	}
	var out HelloKexFleet
	for _, d := range devices {
		switch {
		case d.Classic > 0 && d.PQ > 0:
			out.Mixed++
		case d.Classic > 0:
			out.ClassicOnly++
		case d.PQ > 0:
			out.PQOnly++
		default:
			out.UnknownOnly++
		}
		if d.Classic > 0 {
			out.Classic = append(out.Classic, d)
		}
	}
	sort.Slice(out.Classic, func(i, j int) bool {
		a, b := out.Classic[i], out.Classic[j]
		if a.Classic != b.Classic {
			return a.Classic > b.Classic
		}
		return a.DeviceID < b.DeviceID
	})
	if len(out.Classic) > limit {
		out.Classic = out.Classic[:limit]
	}
	return out, nil
}

// HelloKexSums is one device's ClientHello counts over a window.
type HelloKexSums struct{ PQ, Classic, Unknown uint64 }

// DevicesHelloKex reads, in one statement, the ClientHello counts of every device that has any over [since, now], summed over the
// credentials, nodes (not retired ones) and inbounds of the device, by device id. Only ids and sums, no joins to the device or the
// person: the caller keeps the devices it lists and ignores the rest. The answer has one small row per device with a hello in the
// window, however long the list the caller shows.
func (s *Store) DevicesHelloKex(ctx context.Context, since time.Time) (map[string]HelloKexSums, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT dc.device_id, SUM(l.hello_pq), SUM(l.hello_classic), SUM(l.hello_unknown)
		FROM link_hour l
		JOIN node n ON n.id = l.node_id AND n.state <> 'retired'
		JOIN device_credential dc ON dc.id = l.cred_id
		WHERE l.hour_start >= ? AND l.cred_id <> '' AND l.hello_pq + l.hello_classic + l.hello_unknown > 0
		GROUP BY dc.device_id`, linkHourOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HelloKexSums{}
	for rows.Next() {
		var id string
		var h HelloKexSums
		if err := rows.Scan(&id, &h.PQ, &h.Classic, &h.Unknown); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}
