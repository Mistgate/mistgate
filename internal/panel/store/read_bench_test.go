//go:build !js

package store

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

const subscriptionReadWorkers = 50

func benchmarkSubscriptionStore(b *testing.B) (*Store, time.Time) {
	b.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "subscription.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if _, err := s.W.ExecContext(ctx, `INSERT INTO user_group (id, name, created_at) VALUES (?, ?, ?)`, "bench-group", "Benchmark", 1); err != nil {
		b.Fatal(err)
	}
	return s, time.Unix(1_700_000_000, 0).UTC()
}

func warmSubscriptionBenchmark(b *testing.B, s *Store, now time.Time) {
	b.Helper()
	if _, err := s.Access().SubscriptionData(context.Background(), "bench-user", "bench-group", now, true, now); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkSubscriptionData(b *testing.B) {
	s, now := benchmarkSubscriptionStore(b)
	warmSubscriptionBenchmark(b, s, now)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Access().SubscriptionData(ctx, "bench-user", "bench-group", now, true, now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSubscriptionDataContention(b *testing.B) {
	s, now := benchmarkSubscriptionStore(b)
	warmSubscriptionBenchmark(b, s, now)
	ctx := context.Background()
	workers := subscriptionReadWorkers
	perWorker := (b.N + workers - 1) / workers
	if perWorker < 1 {
		perWorker = 1
	}
	latencies := make([]int64, workers*perWorker)
	start := make(chan struct{})
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.StopTimer()
	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for j := range perWorker {
				i := worker*perWorker + j
				began := time.Now()
				if _, err := s.Access().SubscriptionData(ctx, "bench-user", "bench-group", now, true, now); err != nil {
					b.Errorf("SubscriptionData: %v", err)
					return
				}
				latencies[i] = time.Since(began).Nanoseconds()
			}
		}(worker)
	}
	b.ResetTimer()
	b.StartTimer()
	close(start)
	wg.Wait()
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	reads := len(latencies)
	if reads == 0 {
		b.Fatal("contention benchmark ran no reads")
	}
	b.ReportMetric(float64(latencies[(reads-1)/2]), "p50-read-ns")
	b.ReportMetric(float64(latencies[(reads-1)*99/100]), "p99-read-ns")
	b.ReportMetric(float64(reads)/b.Elapsed().Seconds(), "reads/s")
}
