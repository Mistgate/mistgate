//go:build js

package app

import "github.com/mistgate/mistgate/internal/panel/health"

// The Cloudflare edition has no checks "now" and no background work: nothing is asked of the country sources by hand.
func countryNowSources(*core) []health.CountryNowSource { return nil }
