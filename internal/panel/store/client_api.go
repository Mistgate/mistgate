package store

import "context"

// SettingClientAPI is the owner's switch of the client API; absent = off. It opens the discovery header on subscription app
// answers and enrolment by the link (POST <link>/client/enrol), nothing else: a device that is already enrolled does not depend
// on it (the refusal of a basic fetch that carries its HWID is made whatever the switch says, and the device's own calls will
// be too).
const SettingClientAPI = "client_api.enabled"

// ClientAPIStats is who uses the Mistgate app: everybody, the people who have it on (the person's switch, and the subscription
// link on too, because the app rides on it) and the devices that are connected with a key of their own and not removed.
type ClientAPIStats struct{ People, PeopleOn, Devices int }

// ClientAPIStats reads the three numbers in one query (the VPS and the edge editions alike).
func (a Access) ClientAPIStats(ctx context.Context) (ClientAPIStats, error) {
	var s ClientAPIStats
	err := a.s.R.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(app_happ = 1 AND app_mistgate_off = 0), 0),
		        (SELECT count(*) FROM device WHERE key_hash IS NOT NULL AND revoked_at IS NULL)
		 FROM user`).Scan(&s.People, &s.PeopleOn, &s.Devices)
	return s, err
}
