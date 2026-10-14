//go:build js

package hysteria2

import (
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

// The cores are checked in the native tests only (xray-core is not built for the edge).
func checkWithCores(*testing.T, plugin.ClientFormat, []byte) {}
