//go:build !censorsim || js

package health

import "time"

func censorSimCountryPollInterval() time.Duration { return 30 * time.Second }
