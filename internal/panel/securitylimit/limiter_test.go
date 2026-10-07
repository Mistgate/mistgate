package securitylimit

import (
	"context"
	_ "embed"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/vectors.json
var vectorJSON []byte

type vectorFile struct {
	Scenarios []vectorScenario `json:"scenarios"`
}

type vectorScenario struct {
	Name           string        `json:"name"`
	MaxKeysPerName int           `json:"max_keys_per_name,omitempty"`
	GoOnly         bool          `json:"go_only,omitempty"`
	Operations     []vectorEntry `json:"operations"`
}

type vectorEntry struct {
	Op     string `json:"op"`
	AtMS   int64  `json:"at_ms"`
	Name   string `json:"name"`
	Key    string `json:"key"`
	Bucket *struct {
		Name     string  `json:"name"`
		Burst    float64 `json:"burst"`
		RefillMS float64 `json:"refill_ms"`
	} `json:"bucket"`
	Window *struct {
		Name      string `json:"name"`
		Limit     int    `json:"limit"`
		SpanMS    int64  `json:"span_ms"`
		LockoutMS int64  `json:"lockout_ms"`
	} `json:"window"`
	Want vectorDecision `json:"want"`
}

type vectorDecision struct {
	Allowed      bool  `json:"allowed"`
	RetryAfterMS int64 `json:"retry_after_ms"`
	Remaining    int   `json:"remaining"`
	First        bool  `json:"first"`
}

func TestSharedVectors(t *testing.T) {
	var vectors vectorFile
	if err := json.Unmarshal(vectorJSON, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range vectors.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			clock := time.Unix(0, 0)
			limiter := NewMemory(func() time.Time { return clock }, scenario.MaxKeysPerName)
			for i, op := range scenario.Operations {
				clock = time.UnixMilli(op.AtMS)
				var got Decision
				var err error
				switch op.Op {
				case "take":
					if op.Bucket == nil {
						t.Fatalf("operation %d has no bucket", i)
					}
					got, err = limiter.Take(context.Background(), Bucket{
						Name: op.Bucket.Name, Burst: op.Bucket.Burst, Refill: time.Duration(op.Bucket.RefillMS * float64(time.Millisecond)),
					}, op.Key)
				case "peek", "record":
					if op.Window == nil {
						t.Fatalf("operation %d has no window", i)
					}
					spec := Window{
						Name: op.Window.Name, Limit: op.Window.Limit,
						Span:    time.Duration(op.Window.SpanMS) * time.Millisecond,
						Lockout: time.Duration(op.Window.LockoutMS) * time.Millisecond,
					}
					if op.Op == "peek" {
						got, err = limiter.Peek(context.Background(), spec, op.Key)
					} else {
						got, err = limiter.Record(context.Background(), spec, op.Key)
					}
				case "reset":
					err = limiter.Reset(context.Background(), Window{Name: op.Name}, op.Key)
					got.Allowed = err == nil
				default:
					t.Fatalf("operation %d has unknown op %q", i, op.Op)
				}
				if err != nil {
					t.Fatalf("operation %d: %v", i, err)
				}
				gotRetryAfterMS := int64(math.Ceil(float64(got.RetryAfter) / float64(time.Millisecond)))
				if got.Allowed != op.Want.Allowed || gotRetryAfterMS != op.Want.RetryAfterMS || got.Remaining != op.Want.Remaining || got.First != op.Want.First {
					t.Errorf("operation %d (%s): got %+v (retry_after_ms=%d), want %+v", i, op.Op, got, gotRetryAfterMS, op.Want)
				}
			}
		})
	}
}

func TestNewMemoryUsesDefaultsForMissingOptions(t *testing.T) {
	limiter := NewMemory(nil, 0)
	if limiter.now == nil {
		t.Fatal("default clock is nil")
	}
	if limiter.maxKeysPerName != DefaultMaxKeysPerName {
		t.Fatalf("default cap = %d, want %d", limiter.maxKeysPerName, DefaultMaxKeysPerName)
	}
}

func TestWindowEvictionIsPerNameAndLeastRecentlyUsed(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 3)
	ctx := context.Background()
	alpha := Window{Name: "alpha", Limit: 1, Span: time.Hour}
	beta := Window{Name: "beta", Limit: 1, Span: time.Hour}
	for _, key := range []string{"a", "b", "c"} {
		if d, err := limiter.Record(ctx, alpha, key); err != nil || !d.Allowed {
			t.Fatalf("record alpha/%s: %+v, %v", key, d, err)
		}
	}
	if d, err := limiter.Record(ctx, beta, "preserved"); err != nil || !d.Allowed {
		t.Fatalf("record beta: %+v, %v", d, err)
	}
	if d, err := limiter.Peek(ctx, alpha, "a"); err != nil || d.Allowed {
		t.Fatalf("peek alpha/a: %+v, %v", d, err)
	}
	if d, err := limiter.Record(ctx, alpha, "d"); err != nil || !d.Allowed {
		t.Fatalf("record alpha/d: %+v, %v", d, err)
	}
	for _, key := range []string{"a", "c", "d", "preserved"} {
		name, spec := "alpha", alpha
		if key == "preserved" {
			name, spec = "beta", beta
		}
		d, err := limiter.Peek(ctx, spec, key)
		if err != nil || d.Allowed {
			t.Errorf("recent key %s/%s was evicted: %+v, %v", name, key, d, err)
		}
	}
	if d, err := limiter.Peek(ctx, alpha, "b"); err != nil || !d.Allowed {
		t.Errorf("least-recent alpha/b was not evicted: %+v, %v", d, err)
	}
	for i := 0; i < 10_000; i++ {
		if _, err := limiter.Record(ctx, alpha, string(rune(i+0x1000))); err != nil {
			t.Fatalf("flood record %d: %v", i, err)
		}
	}
	if got := limiter.Size("alpha"); got != 3 {
		t.Errorf("alpha size = %d, want cap 3", got)
	}
	if got := limiter.Size("beta"); got != 1 {
		t.Errorf("beta size = %d, want 1", got)
	}
	if d, err := limiter.Peek(ctx, beta, "preserved"); err != nil || d.Allowed {
		t.Errorf("alpha flood evicted beta key: %+v, %v", d, err)
	}
}

func TestMemoryCanEvictWithOneKeyPerName(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 1)
	window := Window{Name: "single", Limit: 1, Span: time.Hour}
	if _, err := limiter.Record(context.Background(), window, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Record(context.Background(), window, "new"); err != nil {
		t.Fatal(err)
	}
	if got := limiter.Size("single"); got != 1 {
		t.Fatalf("size = %d, want 1", got)
	}
	if d, err := limiter.Peek(context.Background(), window, "new"); err != nil || d.Allowed {
		t.Fatalf("new key was lost after eviction: %+v, %v", d, err)
	}
	if d, err := limiter.Peek(context.Background(), window, "old"); err != nil || !d.Allowed {
		t.Fatalf("old key survived eviction: %+v, %v", d, err)
	}
}

func TestRecordLockoutIsAtomic(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 100)
	spec := Window{Name: "subscription-miss", Limit: 20, Span: time.Minute, Lockout: 15 * time.Minute}
	const attempts = 100
	results := make(chan Decision, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision, err := limiter.Record(context.Background(), spec, "client")
			if err != nil {
				errs <- err
				return
			}
			results <- decision
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	allowed, first := 0, 0
	for decision := range results {
		if decision.Allowed {
			allowed++
		}
		if decision.First {
			first++
			if decision.Allowed || decision.RetryAfter != spec.Lockout {
				t.Errorf("first lockout decision = %+v", decision)
			}
		}
	}
	if allowed != spec.Limit-1 {
		t.Errorf("allowed %d misses, want %d before the lockout", allowed, spec.Limit-1)
	}
	if first != 1 {
		t.Errorf("first lockout decisions = %d, want exactly one", first)
	}
}

func TestWindowEvictionKeepsLockedKeysBeforeUnlockedKeys(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 2)
	ctx := context.Background()
	spec := Window{Name: "shared", Limit: 2, Span: time.Hour, Lockout: time.Minute}
	if d, err := limiter.Record(ctx, spec, "locked"); err != nil || !d.Allowed {
		t.Fatalf("first record for locked key: %+v, %v", d, err)
	}
	if d, err := limiter.Record(ctx, spec, "locked"); err != nil || d.Allowed || !d.First {
		t.Fatalf("second record should lock key: %+v, %v", d, err)
	}
	for i := 0; i < 10; i++ {
		if d, err := limiter.Record(ctx, spec, string(rune('a'+i))); err != nil || !d.Allowed {
			t.Fatalf("record one-miss key %d: %+v, %v", i, d, err)
		}
	}
	if d, err := limiter.Peek(ctx, spec, "locked"); err != nil || d.Allowed || d.RetryAfter != time.Minute {
		t.Fatalf("locked key was evicted during the unlocked-key flood: %+v, %v", d, err)
	}
}

func TestPeekExpiredWindowDoesNotEvictLiveWindow(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 2)
	ctx := context.Background()
	expired := Window{Name: "shared", Limit: 1, Span: time.Millisecond}
	live := Window{Name: "shared", Limit: 1, Span: time.Hour}
	if d, err := limiter.Record(ctx, expired, "expired"); err != nil || !d.Allowed {
		t.Fatalf("record expired key: %+v, %v", d, err)
	}
	clock = clock.Add(time.Millisecond)
	if d, err := limiter.Record(ctx, live, "live"); err != nil || !d.Allowed {
		t.Fatalf("record live key: %+v, %v", d, err)
	}
	if d, err := limiter.Peek(ctx, expired, "expired"); err != nil || !d.Allowed {
		t.Fatalf("peek expired key: %+v, %v", d, err)
	}
	if d, err := limiter.Record(ctx, live, "new"); err != nil || !d.Allowed {
		t.Fatalf("record replacement key: %+v, %v", d, err)
	}
	if d, err := limiter.Peek(ctx, live, "live"); err != nil || d.Allowed {
		t.Fatalf("peek touched away live key: %+v, %v", d, err)
	}
}

func TestBucketLRUIsBoundedPerName(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 64)
	ctx := context.Background()
	spec := Bucket{Name: "bounded", Burst: 1, Refill: time.Minute}
	for i := 0; i < 20_000; i++ {
		if _, err := limiter.Take(ctx, spec, string(rune(i+0x1000))); err != nil {
			t.Fatalf("take key %d: %v", i, err)
		}
	}
	if got := len(limiter.buckets); got != 64 {
		t.Errorf("bucket count = %d, want cap 64", got)
	}
	if got := limiter.bucketOrderByName[spec.Name].Len(); got != 64 {
		t.Errorf("per-name bucket count = %d, want cap 64", got)
	}
	other := Bucket{Name: "other", Burst: 1, Refill: time.Minute}
	if _, err := limiter.Take(ctx, other, "key"); err != nil {
		t.Fatal(err)
	}
	if got := limiter.bucketOrderByName[other.Name].Len(); got != 1 {
		t.Errorf("other-name bucket count = %d, want 1", got)
	}
}

func TestBucketEvictionDropsTheOldestState(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	limiter := NewMemory(func() time.Time { return clock }, 2)
	ctx := context.Background()
	spec := Bucket{Name: "bucket-lru", Burst: 1, Refill: time.Minute}
	for _, key := range []string{"old", "recent"} {
		if d, err := limiter.Take(ctx, spec, key); err != nil || !d.Allowed {
			t.Fatalf("take %s: %+v, %v", key, d, err)
		}
	}
	clock = clock.Add(time.Minute)
	if d, err := limiter.Take(ctx, spec, "old"); err != nil || !d.Allowed {
		t.Fatalf("refill old bucket: %+v, %v", d, err)
	}
	if d, err := limiter.Take(ctx, spec, "new"); err != nil || !d.Allowed {
		t.Fatalf("insert new bucket: %+v, %v", d, err)
	}
	if d, err := limiter.Take(ctx, spec, "recent"); err != nil || !d.Allowed {
		t.Fatalf("evicted bucket did not start full: %+v, %v", d, err)
	}
	if got := limiter.bucketOrderByName[spec.Name].Len(); got != 2 {
		t.Fatalf("bucket count = %d, want cap 2", got)
	}
}
