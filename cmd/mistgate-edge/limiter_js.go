//go:build js && wasm

package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/edge/d1driver"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
)

var errEdgeLimitUnavailable = errors.New("edge security limiter unavailable")

type edgeLimiter struct {
	callback js.Value
	log      *slog.Logger
	once     sync.Once
}

// The init limit callback receives {operation, name, key} plus operation fields:
// take adds burst/refillMs; peek and record add limit/spanMs/lockoutMs.
// Every call resolves to {ok, retryAfterMs, remaining, first}.
func newEdgeLimiter(callback js.Value, log *slog.Logger) securitylimit.Limiter {
	if log == nil {
		log = slog.Default()
	}
	return &edgeLimiter{callback: callback, log: log}
}

func (l *edgeLimiter) Take(ctx context.Context, spec securitylimit.Bucket, key string) (securitylimit.Decision, error) {
	request := l.request("take", spec.Name, key)
	request.Set("burst", spec.Burst)
	request.Set("refillMs", float64(spec.Refill)/float64(time.Millisecond))
	return l.call(ctx, request)
}

func (l *edgeLimiter) Peek(ctx context.Context, spec securitylimit.Window, key string) (securitylimit.Decision, error) {
	return l.call(ctx, l.windowRequest("peek", spec, key))
}

func (l *edgeLimiter) Record(ctx context.Context, spec securitylimit.Window, key string) (securitylimit.Decision, error) {
	return l.call(ctx, l.windowRequest("record", spec, key))
}

func (l *edgeLimiter) Reset(ctx context.Context, spec securitylimit.Window, key string) error {
	_, err := l.call(ctx, l.request("reset", spec.Name, key))
	return err
}

func (l *edgeLimiter) request(operation, name, key string) js.Value {
	request := js.Global().Get("Object").New()
	request.Set("operation", operation)
	request.Set("name", name)
	request.Set("key", key)
	return request
}

func (l *edgeLimiter) windowRequest(operation string, spec securitylimit.Window, key string) js.Value {
	request := l.request(operation, spec.Name, key)
	request.Set("limit", spec.Limit)
	request.Set("spanMs", float64(spec.Span)/float64(time.Millisecond))
	request.Set("lockoutMs", float64(spec.Lockout)/float64(time.Millisecond))
	return request
}

func (l *edgeLimiter) call(ctx context.Context, request js.Value) (securitylimit.Decision, error) {
	if l.callback.Type() != js.TypeFunction {
		return securitylimit.Decision{}, l.unavailable()
	}
	value, err := d1driver.Await(ctx, l.callback.Invoke(request))
	if err != nil || value.Type() != js.TypeObject || value.IsNull() {
		return securitylimit.Decision{}, l.unavailable()
	}
	ok := value.Get("ok")
	retry := value.Get("retryAfterMs")
	remaining := value.Get("remaining")
	first := value.Get("first")
	if ok.Type() != js.TypeBoolean || retry.Type() != js.TypeNumber || remaining.Type() != js.TypeNumber || first.Type() != js.TypeBoolean {
		return securitylimit.Decision{}, l.unavailable()
	}
	ms := retry.Float()
	left := remaining.Float()
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || math.IsNaN(left) || math.IsInf(left, 0) || left < 0 || left != math.Trunc(left) {
		return securitylimit.Decision{}, l.unavailable()
	}
	return securitylimit.Decision{
		Allowed: ok.Bool(), RetryAfter: time.Duration(ms * float64(time.Millisecond)),
		Remaining: remaining.Int(), First: first.Bool(),
	}, nil
}

func (l *edgeLimiter) unavailable() error {
	l.once.Do(func() {
		l.log.Warn("edge security limit callback unavailable; guarded requests will be refused")
	})
	return errEdgeLimitUnavailable
}
