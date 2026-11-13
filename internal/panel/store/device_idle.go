package store

import (
	"strconv"
	"time"
)

// SettingDeviceIdleAfterDays is the subscription setting that controls when an unused device stops filling a place.
const SettingDeviceIdleAfterDays = "device_idle_after_days"

// DefaultDeviceIdleAfterDays is used for installations created before the setting was added.
const DefaultDeviceIdleAfterDays = 30

// deviceIdleSQL is the one definition of device idleness used by limit counters and device rows. The caller supplies
// the current time; the stored setting is read in the same query as the device snapshot.
func deviceIdleSQL(alias string, now time.Time) (idle, idleSince string) {
	days := `coalesce((SELECT CAST(v AS INTEGER) FROM setting WHERE k = '` + SettingDeviceIdleAfterDays + `'), ` +
		strconv.Itoa(DefaultDeviceIdleAfterDays) + `)`
	lastUse := `coalesce(nullif(` + alias + `.last_seen_at, 0), ` + alias + `.created_at)`
	since := `(` + lastUse + ` + (` + days + `) * 86400)`
	idle = `((` + days + `) > 0 AND ` + since + ` <= ` + strconv.FormatInt(unix(now), 10) + `)`
	idleSince = `CASE WHEN ` + idle + ` THEN ` + since + ` ELSE 0 END`
	return idle, idleSince
}
