//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestTakeLeaseSerializesAcrossTakersAndExpires(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()

	type result struct {
		holder string
		taken  bool
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, holder := range []string{"alice", "de1.example.com"} {
		holder := holder
		go func() {
			<-start
			taken, err := st.TakeLease(ctx, "country", holder, time.Minute, now)
			results <- result{holder: holder, taken: taken, err: err}
		}()
	}
	close(start)

	var winner, loser string
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.taken {
			if winner != "" {
				t.Fatalf("both takers won: %q and %q", winner, got.holder)
			}
			winner = got.holder
		} else {
			loser = got.holder
		}
	}
	if winner == "" || loser == "" {
		t.Fatalf("winner = %q, loser = %q; want one winner", winner, loser)
	}

	if err := st.ReleaseLease(ctx, "country", loser); err != nil {
		t.Fatal(err)
	}
	if taken, err := st.TakeLease(ctx, "country", loser, time.Minute, now.Add(time.Minute-time.Second)); err != nil || taken {
		t.Fatalf("stranger release freed the lease before expiry: taken=%v, err=%v", taken, err)
	}
	if taken, err := st.TakeLease(ctx, "country", loser, time.Minute, now.Add(time.Minute)); err != nil || !taken {
		t.Fatalf("lease did not pass to the next taker at expiry: taken=%v, err=%v", taken, err)
	}

	if err := st.ReleaseLease(ctx, "country", winner); err != nil {
		t.Fatal(err)
	}
	if taken, err := st.TakeLease(ctx, "country", winner, time.Minute, now.Add(time.Minute)); err != nil || taken {
		t.Fatalf("a former holder released the current lease: taken=%v, err=%v", taken, err)
	}
	if err := st.ReleaseLease(ctx, "country", loser); err != nil {
		t.Fatal(err)
	}
	if taken, err := st.TakeLease(ctx, "country", winner, time.Minute, now.Add(time.Minute)); err != nil || !taken {
		t.Fatalf("current holder release did not free the lease: taken=%v, err=%v", taken, err)
	}
}
