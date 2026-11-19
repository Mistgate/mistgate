//go:build !js

package globalping

import (
	"context"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/health"
)

// A round asked by hand reads what is left of the hourly window (GET /v1/limits, which counts for nothing) and nothing else.
func TestSourceRemainingReadsTheWindow(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	b, ok := e.source().(health.CountryBudget)
	if !ok {
		t.Fatal("the source does not say what is left of its window")
	}
	left, reset, err := b.Remaining(context.Background())
	if err != nil || left != 245 || reset != 3000*time.Second {
		t.Fatalf("remaining = %d, %v, %v", left, reset, err)
	}
	reqs := e.fake.requests()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].Path != "/v1/limits" {
		t.Fatalf("requests = %+v", reqs)
	}
	e.fake.limitsCode = 500
	if _, _, err := b.Remaining(context.Background()); err == nil {
		t.Fatal("a failed read was taken for a budget")
	}
}
