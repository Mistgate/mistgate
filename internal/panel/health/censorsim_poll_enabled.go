//go:build censorsim && !js

package health

import "time"

func censorSimCountryPollInterval() time.Duration { return time.Second }
