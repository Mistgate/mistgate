//go:build censorsim

package countryprobe

import "time"

// Only the tagged lab binary disables scheduled runs and shortens the manual API test limiter. The actual API command,
// probe algorithm, address checks, result storage and classifier inputs stay on the production path.
const (
	censorSimManualOnly = true
	censorSimTestEvery  = time.Second
)
