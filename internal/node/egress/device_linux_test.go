//go:build linux

package egress

import (
	"strings"
	"testing"
)

// Binding to an existing device works (here the loopback device; the WARP lab in internal/node/warp proves the
// real tunnel device).
func TestWithDeviceBound(t *testing.T) {
	addr, _ := countingServer(t, "tcp", "127.0.0.1:0")
	e := New(nil, AllowPrivate(), WithDevice("lo"))
	c, err := e.TCP(addr)
	if err != nil {
		if strings.Contains(err.Error(), "not permitted") {
			t.Skipf("SO_BINDTODEVICE needs privileges here: %v", err)
		}
		t.Fatal(err)
	}
	c.Close()
}
