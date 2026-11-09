//go:build !censorsim || js

package app

import "time"

func censorSimGlobalping() (string, time.Duration) { return "", 0 }
func censorSimBsbord() string                      { return "" }
