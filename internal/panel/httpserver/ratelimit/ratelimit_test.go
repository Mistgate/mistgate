package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestBucketBurstRefillAndRetryAfter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(2, 3, 100) // 2/s, burst 3
	l.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d inside the burst was refused", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok || retry != 500*time.Millisecond {
		t.Fatalf("4th request: ok=%v retry=%v, want refused with 500ms", ok, retry)
	}
	if ok, _ := l.Allow("b"); !ok { // keys are independent
		t.Fatal("another key was limited")
	}
	now = now.Add(500 * time.Millisecond)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("no token after the advertised Retry-After")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("a second token appeared from half a refill period")
	}
	now = now.Add(time.Hour) // never more than the burst
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("burst after a long pause: request %d refused", i)
		}
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("refill exceeded the burst")
	}
}

func TestDisabledAndBoundedMemory(t *testing.T) {
	off := New(0, 1, 10)
	for i := 0; i < 100; i++ {
		if ok, _ := off.Allow("x"); !ok {
			t.Fatal("a disabled limiter refused")
		}
	}
	l := New(1, 1, 50)
	for i := 0; i < 10_000; i++ { // a scan rotating its source address
		l.Allow(fmt.Sprintf("2001:db8:%x::/64", i))
	}
	if l.Keys() != 50 {
		t.Fatalf("%d keys tracked, want the cap of 50", l.Keys())
	}
}

func TestMapEvictsLeastRecentlyUsed(t *testing.T) {
	m := NewMap[int](2)
	m.GetOrCreate("a", func() int { return 1 })
	m.GetOrCreate("b", func() int { return 2 })
	m.Get("a") // b is now the oldest
	m.GetOrCreate("c", func() int { return 3 })
	if _, ok := m.Get("b"); ok {
		t.Fatal("the least recently used entry survived")
	}
	if v, ok := m.Get("a"); !ok || v != 1 {
		t.Fatal("a recently used entry was evicted")
	}
	m.Delete("a")
	if m.Len() != 1 {
		t.Fatalf("len %d after delete", m.Len())
	}
}
