//go:build !censorsim || js

package health

import "time"

func censorSimCountryControl(string) (string, uint16, bool)         { return "", 0, false }
func censorSimCountryInterval(interval time.Duration) time.Duration { return interval }
func censorSimCountryHourlyLimit(limit int) int                     { return limit }
func censorSimManualCheckGap(gap time.Duration) time.Duration       { return gap }
