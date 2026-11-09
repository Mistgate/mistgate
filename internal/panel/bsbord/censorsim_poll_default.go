//go:build !censorsim || js

package bsbord

import "time"

func censorSimBsbordPollInterval() time.Duration { return 30 * time.Second }
