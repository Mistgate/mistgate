package asndb

import (
	"fmt"
	"time"
)

const (
	MinReleaseIPv4Ranges = 300_000
	MinReleaseASNs       = 50_000
	MaxReleaseAge        = 45 * 24 * time.Hour
)

// ReleaseLimits makes the release sanity policy explicit and lets tests exercise its boundaries with small tables.
type ReleaseLimits struct {
	MinIPv4Ranges int
	MinASNs       int
	MaxAge        time.Duration
}

// ValidateRelease applies the minimum-size and freshness checks used by generation and release signing.
func ValidateRelease(db *DB, now time.Time) error {
	return ValidateReleaseWith(db, now, DefaultReleaseLimits())
}

// DefaultReleaseLimits returns the minimum production quality policy.
func DefaultReleaseLimits() ReleaseLimits {
	return ReleaseLimits{
		MinIPv4Ranges: MinReleaseIPv4Ranges,
		MinASNs:       MinReleaseASNs,
		MaxAge:        MaxReleaseAge,
	}
}

// ValidateReleaseWith applies the same checks with an explicit policy.
func ValidateReleaseWith(db *DB, now time.Time, limits ReleaseLimits) error {
	if db == nil {
		return fmt.Errorf("asndb: no table to validate")
	}
	if limits.MinIPv4Ranges < 0 || limits.MinASNs < 0 || limits.MaxAge <= 0 {
		return fmt.Errorf("asndb: invalid release limits")
	}
	if count := db.IPv4RangeCount(); count < limits.MinIPv4Ranges {
		return fmt.Errorf("asndb: only %d routed IPv4 ranges (want at least %d)", count, limits.MinIPv4Ranges)
	}
	if count := db.ASNCount(); count < limits.MinASNs {
		return fmt.Errorf("asndb: only %d ASNs (want at least %d)", count, limits.MinASNs)
	}
	built := db.Built()
	if built.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("asndb: source timestamp %s is in the future", built.Format(time.RFC3339))
	}
	if age := now.Sub(built); age > limits.MaxAge {
		return fmt.Errorf("asndb: source timestamp %s is older than %s", built.Format(time.RFC3339), limits.MaxAge)
	}
	return nil
}
