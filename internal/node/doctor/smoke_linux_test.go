//go:build linux

package doctor

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRealHostSmoke runs every check against the machine the tests run on (read-only). It proves the real
// readers and commands work end to end and that nothing times out or panics; it asserts no particular health.
func TestRealHostSmoke(t *testing.T) {
	e := DefaultEnv()
	e.StateDir = os.TempDir()
	e.Virt = "unknown"
	rep, err := New(e).Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != len(CheckIDs()) {
		t.Fatalf("%d results", len(rep.Results))
	}
	for _, r := range rep.Results {
		t.Logf("%-16s %-4s %s", r.ID, r.Status, r.Detail)
		if strings.Contains(r.Detail, "timed out") || strings.Contains(r.Detail, "internal error") {
			t.Errorf("%s: %s", r.ID, r.Detail)
		}
		if len(r.Detail) > 200 {
			t.Errorf("%s detail is %d bytes", r.ID, len(r.Detail))
		}
	}
}
