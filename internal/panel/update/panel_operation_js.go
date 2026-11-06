//go:build js

package update

import "time"

func PanelOperationRunning(string, time.Time) bool { return false }

func PanelHelperRequestPending(string, time.Time) bool { return false }
