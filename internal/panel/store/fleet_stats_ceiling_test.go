package store

import (
	"math"
	"testing"
	"time"
)

func TestSpeedCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate uint64
		span time.Duration
		want uint64
	}{
		{"no limit", 0, 10 * time.Second, 0},
		{"no span", 1_000_000, 0, 0},
		{"1 Mbps over 10 s: twice the 1.25 MB plus 16 MiB", 1_000_000, 10 * time.Second, 2*1_250_000 + 16<<20},
		{"100 Mbps over an hour", 100_000_000, time.Hour, 2*100_000_000/8*3600 + 16<<20},
		{"a limit no link carries is no ceiling", math.MaxUint64, time.Hour, 0},
	} {
		if got := speedCeiling(tc.rate, tc.span); got != tc.want {
			t.Errorf("%s: speedCeiling = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestCapDelta(t *testing.T) {
	if up, down, capped := capDelta(10, 20, 100); up != 10 || down != 20 || capped {
		t.Fatalf("under the ceiling: %d %d %t", up, down, capped)
	}
	if up, down, capped := capDelta(10, 20, 0); up != 10 || down != 20 || capped {
		t.Fatalf("no ceiling: %d %d %t", up, down, capped)
	}
	up, down, capped := capDelta(300, 100, 100)
	if !capped || up+down != 100 || up != 75 {
		t.Fatalf("over the ceiling: up=%d down=%d capped=%t, want the same proportion summing to 100", up, down, capped)
	}
	up, down, capped = capDelta(math.MaxUint64, math.MaxUint64, 1<<30)
	if !capped || up+down != 1<<30 {
		t.Fatalf("saturated: up=%d down=%d capped=%t", up, down, capped)
	}
	if up, down, _ := capDelta(0, 500, 100); up != 0 || down != 100 {
		t.Fatalf("one direction only: up=%d down=%d", up, down)
	}
}

func TestStrictestLimit(t *testing.T) {
	for _, tc := range [][3]uint64{{0, 0, 0}, {5, 0, 5}, {0, 7, 7}, {5, 7, 5}, {9, 7, 7}} {
		if got := strictestLimit(tc[0], tc[1]); got != tc[2] {
			t.Errorf("strictestLimit(%d, %d) = %d, want %d", tc[0], tc[1], got, tc[2])
		}
	}
}
