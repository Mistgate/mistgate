//go:build !linux

package agent

import "context"

// defaultExitProbe: no service manager to ask on this platform.
func defaultExitProbe(context.Context) exitFacts { return exitFacts{Restarts: -1} }
