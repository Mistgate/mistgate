//go:build censorsim && !js

package bsbord

import "time"

func censorSimBsbordPollInterval() time.Duration { return time.Second }
