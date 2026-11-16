//go:build js

package health

import (
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The Cloudflare edition has no own probes (ownprobe_signal.go): a row of their source is ignored.
func currentProbeDetail(row store.ExtCheck, _ time.Duration, _ time.Time) (countryreach.ExtDetail, string, string, time.Time) {
	return countryreach.ExtDetail{}, verdictUnknown, "", row.At
}

func probeMeasurement(countryAlertSignal, countryreach.Vantage) string { return "" }
