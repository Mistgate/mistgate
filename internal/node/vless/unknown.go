package vless

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const unknownKeyWarningInterval = 10 * time.Minute

type unknownKeyTracker struct {
	inboundID string
	log       *slog.Logger
	count     atomic.Uint32
	warnings  unknownKeyWarnings
}

func newUnknownKeyTracker(inboundID string, logger *slog.Logger) *unknownKeyTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &unknownKeyTracker{inboundID: inboundID, log: logger}
}

func (c *unknownKeyTracker) record(now time.Time) {
	c.count.Add(1)
	if count, ok := c.warnings.record(now); ok {
		inboundID := redactXrayIdentifiers(c.inboundID)
		c.log.Warn(fmt.Sprintf("vless: %d connections with an unknown key on %s in the last 10 min: an app is using a revoked or stale subscription", count, inboundID))
	}
}

// unknownKeyWarnings keeps bounded ten-second buckets so each warning can report the recent ten-minute count without
// retaining one timestamp per rejected connection. The bucket granularity can include or omit at most ten seconds.
type unknownKeyWarnings struct {
	mu       sync.Mutex
	lastWarn time.Time
	buckets  [60]unknownKeyWarningBucket
}

type unknownKeyWarningBucket struct {
	window int64
	count  uint32
}

func (w *unknownKeyWarnings) record(now time.Time) (uint32, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	window := now.Unix() / 10
	index := window % int64(len(w.buckets))
	if index < 0 {
		index += int64(len(w.buckets))
	}
	bucket := &w.buckets[index]
	if bucket.window != window {
		bucket.window, bucket.count = window, 0
	}
	bucket.count++

	var recent uint32
	for _, bucket := range w.buckets {
		age := window - bucket.window
		if age >= 0 && age < int64(len(w.buckets)) {
			recent += bucket.count
		}
	}
	warn := w.lastWarn.IsZero() || now.Sub(w.lastWarn) >= unknownKeyWarningInterval
	if warn {
		w.lastWarn = now
	}
	return recent, warn
}
