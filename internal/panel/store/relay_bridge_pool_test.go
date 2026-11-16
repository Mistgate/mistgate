package store

import (
	"testing"
	"time"
)

func TestBridgePool(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(ago time.Duration) int64 { return now.Add(-ago).Unix() }
	for _, tc := range []struct {
		name    string
		prev    uint32
		prevAt  int64
		clients uint32
		bps     uint64
		want    uint32
		wantAt  int64
	}{
		{"quiet stays at two", 0, 0, 10, 0, 2, 0},
		{"a busy relay (10 Mbit/s) gets four", 2, 0, 10, 10_000_000, 4, now.Unix()},
		{"just under the busy rate does not", 2, 0, 10, 9_999_999, 2, 0},
		{"80 % of two connections is near full: four", 2, 0, 194, 0, 4, now.Unix()},
		{"79 % is not", 2, 0, 193, 0, 2, 0},
		{"four stays while the reason holds", 4, at(time.Hour), 200, 0, 4, now.Unix()},
		{"four stays for 30 minutes after the reason went", 4, at(29 * time.Minute), 10, 0, 4, at(29 * time.Minute)},
		{"four goes back after 30 quiet minutes", 4, at(30 * time.Minute), 10, 0, 2, 0},
		{"but not while people are still above 40 % of two", 4, at(time.Hour), 100, 0, 4, at(time.Hour)},
		{"a time from the future does not hold four for ever", 4, now.Add(time.Hour).Unix(), 10, 0, 4, now.Add(time.Hour).Unix()},
		{"no time at all keeps four until the reason is seen again", 4, 0, 10, 0, 4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotAt := BridgePool(tc.prev, tc.prevAt, tc.clients, tc.bps, now)
			if got != tc.want || gotAt != tc.wantAt {
				t.Fatalf("pool = %d at %d, want %d at %d", got, gotAt, tc.want, tc.wantAt)
			}
		})
	}
}

func TestBridgeNearFull(t *testing.T) {
	// 121 people a connection: 242 on two, 484 on four; 80 % is 193.6 and 387.2
	for _, tc := range []struct {
		clients, pool uint32
		want          bool
	}{{0, 2, false}, {193, 2, false}, {194, 2, true}, {242, 2, true}, {387, 4, false}, {388, 4, true}, {10, 0, false}} {
		if got := BridgeNearFull(tc.clients, tc.pool); got != tc.want {
			t.Fatalf("near full(%d people, pool %d) = %v, want %v", tc.clients, tc.pool, got, tc.want)
		}
	}
	if BridgeCapacity(2) != 242 || BridgeCapacity(4) != 484 {
		t.Fatalf("capacity of two and four connections = %d and %d", BridgeCapacity(2), BridgeCapacity(4))
	}
}
