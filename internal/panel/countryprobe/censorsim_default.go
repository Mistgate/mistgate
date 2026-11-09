//go:build !censorsim

package countryprobe

import "time"

const (
	censorSimManualOnly = false
	censorSimTestEvery  = 60 * time.Second
)
