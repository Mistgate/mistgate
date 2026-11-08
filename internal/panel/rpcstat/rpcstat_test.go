package rpcstat

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestWindowCountsAndForgets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	w := New(func() time.Time { return now })
	if s := w.Snapshot(); s.Requests != 0 || s.P95ms != 0 || s.WindowS != 300 {
		t.Fatalf("empty: %+v", s)
	}
	for i := range 100 {
		status := 200
		switch {
		case i < 3:
			status = 500
		case i < 10:
			status = 403
		}
		w.Record(status, time.Duration(i+1)*time.Millisecond, false) // 1..100 ms
	}
	w.Record(200, time.Hour, true) // a stream: counted, not timed
	s := w.Snapshot()
	if s.Requests != 101 || s.ServerErrors != 3 || s.ClientErrors != 7 {
		t.Fatalf("counts: %+v", s)
	}
	// 95 of 100 calls took 95 ms or less; the bucket 75..100 holds 76..100 ms, so the estimate sits inside it.
	if s.P95ms < 75 || s.P95ms > 100 {
		t.Fatalf("p95 = %v", s.P95ms)
	}

	now = now.Add(3 * time.Minute)
	w.Record(200, 2*time.Second, false)
	if s := w.Snapshot(); s.Requests != 102 {
		t.Fatalf("3 minutes later: %+v", s)
	}
	now = now.Add(3 * time.Minute) // the first minute left the window; the slot it used is reused later
	if s := w.Snapshot(); s.Requests != 1 || s.P95ms != 1500+(2500-1500)*0.95 {
		t.Fatalf("6 minutes later: %+v", s)
	}
	now = now.Add(10 * time.Minute)
	if s := w.Snapshot(); s.Requests != 0 || s.P95ms != 0 {
		t.Fatalf("all gone: %+v", s)
	}
	w.Record(200, time.Minute, false) // slower than the last edge
	if s := w.Snapshot(); s.P95ms != 10000 {
		t.Fatalf("slow: %+v", s)
	}
}

func TestNilWindowAndConcurrency(t *testing.T) {
	var w *Window
	w.Record(200, time.Millisecond, false)
	if s := w.Snapshot(); s.Requests != 0 {
		t.Fatal("nil window counted")
	}
	w = New(nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				w.Record(200, time.Millisecond, false)
			}
		})
	}
	wg.Wait()
	if s := w.Snapshot(); s.Requests != 8000 || math.IsNaN(s.P95ms) {
		t.Fatalf("%+v", s)
	}
}

func TestRecordDoesNotAllocate(t *testing.T) {
	w := New(nil)
	if n := testing.AllocsPerRun(1000, func() { w.Record(200, 3*time.Millisecond, false) }); n != 0 {
		t.Fatalf("Record allocates %v times", n)
	}
}
