//go:build !js

package health

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The own probes' share of the country signal. It is a file of its own, with a stub for the Cloudflare edition
// (ownprobe_signal_js.go): the edition has no probes, so a row of the "probes" source never exists there, and this code would
// only weigh down the Workers, which have a size limit (scripts/check-edge-sizes.sh).

// currentProbeDetail keeps the points of the row that are young enough, and judges them: a probe is one narrow point, so
// whatever it sees, it raises "partial" at most.
func currentProbeDetail(row store.ExtCheck, maxAge time.Duration, now time.Time) (countryreach.ExtDetail, string, string, time.Time) {
	var detail countryreach.ExtDetail
	if json.Unmarshal([]byte(row.DetailJSON), &detail) != nil {
		return countryreach.ExtDetail{}, verdictUnknown, countryreach.ReasonTooFewProbes, row.At
	}
	cut := now.Add(-maxAge).Unix()
	filtered := make([]countryreach.Vantage, 0, len(detail.Vantages))
	at := time.Time{}
	for _, vantage := range detail.Vantages {
		if !strings.HasPrefix(vantage.Key, "probe:") {
			continue
		}
		unixAt := vantage.At
		if unixAt == 0 {
			unixAt = row.At.Unix()
		}
		if unixAt < cut || unixAt > now.Add(5*time.Minute).Unix() {
			continue
		}
		vantage.At = unixAt
		filtered = append(filtered, vantage)
		vAt := time.Unix(unixAt, 0).UTC()
		if vAt.After(at) {
			at = vAt
		}
	}
	detail.Vantages = filtered
	if at.IsZero() {
		at = row.At
	}
	verdict, reason := countryreach.JudgeVantages(filtered, 1, countryreach.VerdictTCPBlocked)
	if verdict == verdictTCPBlocked || verdict == verdictBlocked {
		verdict = verdictPartial // a probe is one narrow point: whatever it sees, it raises "partial" at most
	}
	return detail, verdict, reason, at
}

// probeMeasurement names one measurement of an own probe: the probe and the time it was made.
func probeMeasurement(signal countryAlertSignal, v countryreach.Vantage) string {
	at := v.At
	if at == 0 {
		at = signal.at.Unix()
	}
	return v.Key + "|" + strconv.FormatInt(at, 10)
}
