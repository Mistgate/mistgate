//go:build censorsim && !js

package health

import "time"

// The control target is served inside the private censor lab. This tagged build must never probe the public control sites.
func censorSimCountryControl(string) (string, uint16, bool) { return "198.51.100.10", 443, true }
func censorSimCountryInterval(time.Duration) time.Duration  { return 5 * time.Second }
func censorSimCountryHourlyLimit(int) int                   { return 0 }
func censorSimManualCheckGap(time.Duration) time.Duration   { return time.Second }
