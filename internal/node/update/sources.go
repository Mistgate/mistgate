package update

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"sync/atomic"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/release"
)

// Fetcher is one ordered way to fetch the signed file. Fetch always resumes at offset and appends to w.
type Fetcher struct {
	Kind  string
	Fetch func(context.Context, uint64, io.Writer) (total uint64, err error)
}

type fetchFailureClass uint8

const (
	failureTransient fetchFailureClass = iota
	failureBusy
	failurePermanent
)

// FetchFailure carries a short, safe-to-report code for one source failure.
type FetchFailure struct {
	Code  string
	Class fetchFailureClass
	Err   error
}

func (e *FetchFailure) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *FetchFailure) Unwrap() error { return e.Err }

func (e *FetchFailure) Is(target error) bool {
	return target == ErrRetryLater && e.Class == failureBusy || target == ErrPermanent && e.Class == failurePermanent
}

// TransientSourceFailure makes a network or transport failure eligible for the per-source retry budget.
func TransientSourceFailure(code string, err error) error {
	return newFetchFailure(code, failureTransient, err)
}

// BusySourceFailure switches immediately without consuming the source's transient retry budget.
func BusySourceFailure(code string, err error) error {
	return newFetchFailure(code, failureBusy, err)
}

// PermanentSourceFailure drops this source for the current update command.
func PermanentSourceFailure(code string, err error) error {
	return newFetchFailure(code, failurePermanent, err)
}

// IsOverflow reports that a source tried to append past the signed file size.
func IsOverflow(err error) bool { return errors.Is(err, errOverflow) }

func newFetchFailure(code string, class fetchFailureClass, err error) error {
	if !sourceCodeRE.MatchString(code) {
		code = "error"
	}
	return &FetchFailure{Code: code, Class: class, Err: err}
}

// sourceFailureCode returns the short code used in sources_failed. It never returns the underlying error text.
func sourceFailureCode(err error) string {
	var failure *FetchFailure
	if errors.As(err, &failure) && sourceCodeRE.MatchString(failure.Code) {
		return failure.Code
	}
	switch {
	case errors.Is(err, errOverflow):
		return "size"
	case errors.Is(err, ErrRetryLater):
		return "busy"
	case errors.Is(err, ErrPermanent):
		return "not_found"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	default:
		var step *stepErr
		if errors.As(err, &step) {
			switch step.code {
			case "size_mismatch":
				return "size"
			}
		}
		if errors.Is(err, errDownloadIdleTimeout) {
			return "stalled"
		}
		return "error"
	}
}

var sourceCodeRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

type selectedFetcher struct {
	Source
	fetcher Fetcher
	current func(string)
	used    func(string)
}

func (s selectedFetcher) Fetch(ctx context.Context, _ string, offset uint64, w io.Writer) (uint64, error) {
	if s.current != nil {
		s.current(s.fetcher.Kind)
	}
	counted := kindWriter{w: w, kind: s.fetcher.Kind, used: s.used}
	return s.fetcher.Fetch(ctx, offset, counted)
}

func (s selectedFetcher) SetActivity(fn func()) {
	if activity, ok := s.Source.(interface{ SetActivity(func()) }); ok {
		activity.SetActivity(fn)
	}
}

type kindWriter struct {
	w    io.Writer
	kind string
	used func(string)
}

func (w kindWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 && w.used != nil {
		w.used(w.kind)
	}
	return n, err
}

type countedWriter struct {
	w io.Writer
	n atomic.Uint64
}

func (w *countedWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		w.n.Add(uint64(n))
	}
	return n, err
}

type sourceState struct {
	fetcher    Fetcher
	transients int
	rate       float64
	slow       bool
	dropped    bool
	lastFail   string
}

type sourceSample struct {
	at time.Time
	n  uint64
}

// downloadSources walks multiple sources serially. The legacy one-source loop remains in download.
func (u *Updater) downloadSources(ctx context.Context, src Source, f release.File, timeout time.Duration, fetchers []Fetcher,
	report func(*pb.UpdateProgress), setCurrent func(string),
) (used, failed []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fh, err := os.OpenFile(u.newPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return nil, nil, step("not_writable", err)
	}
	defer fh.Close()

	states := make([]sourceState, len(fetchers))
	for i, fetcher := range fetchers {
		states[i].fetcher = fetcher
	}
	tracker := newProgressTracker(report, f.Size, src.Now)
	cw := &capWriter{w: fh, left: f.Size}
	bytes := &countedWriter{w: &progressWriter{w: cw, onWrite: func(n int) { tracker.add(uint64(n)) }}}
	usedSet := make(map[string]bool, len(fetchers))
	markUsed := func(kind string) {
		if !usedSet[kind] {
			usedSet[kind] = true
			used = append(used, kind)
		}
	}
	current := firstLive(states)
	if current < 0 {
		return nil, nil, step("download_failed", errors.New("no update source is available"))
	}
	wraps := 0
	lastErr := error(nil)
	activity, _ := src.(interface{ SetActivity(func()) })

	for cw.n < f.Size {
		if ctx.Err() != nil {
			lastErr = errors.New("timeout")
			states[current].lastFail = "timeout"
			break
		}
		state := &states[current]
		if setCurrent != nil {
			setCurrent(state.fetcher.Kind)
		}
		offset := uint64(cw.n)
		before := bytes.n.Load()
		started := time.Now()
		skipSlowCheck := firstNotSlow(states, current) < 0
		total, fetchErr, reason, rate := u.runSource(ctx, state.fetcher, offset, bytes, before, started, skipSlowCheck, activity, func(rate float64) bool {
			if rate > state.rate {
				state.rate = rate
			}
			state.slow = true
			if faster := firstNotSlow(states, current); faster >= 0 {
				return true
			}
			best := fastestLive(states)
			return best >= 0 && best != current
		})
		delta := bytes.n.Load() - before
		if delta > 0 {
			markUsed(state.fetcher.Kind)
		}
		if rate > state.rate {
			state.rate = rate
		}

		if total != uint64(f.Size) && (fetchErr == nil || total != 0) {
			fetchErr = PermanentSourceFailure("size", errors.New("source reported a different file size"))
			reason = "size"
		}
		if errors.Is(fetchErr, errOverflow) {
			fetchErr = PermanentSourceFailure("size", errors.New("source sent more bytes than the signed file size"))
			reason = "size"
		}
		if ctx.Err() != nil {
			lastErr = errors.New("timeout")
			state.lastFail = "timeout"
			break
		}

		if fetchErr == nil && cw.n == f.Size {
			break
		}
		if fetchErr == nil {
			fetchErr = TransientSourceFailure("error", io.ErrUnexpectedEOF)
		}
		code := sourceFailureCode(fetchErr)
		lastErr = errors.New(code)
		if reason == "" {
			reason = code
		}
		state.lastFail = reason
		if errors.Is(fetchErr, ErrPermanent) {
			state.dropped = true
		} else if errors.Is(fetchErr, ErrRetryLater) {
			// Busy does not consume the transient attempt budget.
		} else if reason != "slow" {
			state.transients++
			if state.transients >= 3 {
				state.dropped = true
			}
		}

		// A complete capped file is still verified as a whole. A source that reported a bad total or overflow is
		// dropped, but the digest remains the only authority on whether these bytes can be installed.
		if cw.n == f.Size {
			break
		}
		if noneLive(states) {
			break
		}

		next := -1
		wrapped := false
		if reason == "slow" {
			next = nextNotSlow(states, current)
			if next < 0 {
				next = fastestLive(states)
			}
			wrapped = next >= 0 && next < current
		} else {
			next = nextNotSlow(states, current)
			if next < 0 {
				next, wrapped = nextLive(states, current)
			} else {
				wrapped = next < current
			}
			if wrapped && allLiveSlow(states) {
				next = fastestLive(states)
			}
		}
		if next < 0 {
			break
		}
		if wrapped {
			wraps++
			if !waitSourceRound(ctx, u, wraps-1) {
				lastErr = errors.New("timeout")
				break
			}
		}
		if next != current {
			u.log.Info("update source switched", "from", state.fetcher.Kind, "to", states[next].fetcher.Kind, "reason", reason)
		}
		current = next
	}

	for _, state := range states {
		if state.lastFail != "" {
			failed = append(failed, state.fetcher.Kind+":"+state.lastFail)
		}
	}
	if ctx.Err() != nil {
		return used, failed, step("download_failed", ctx.Err())
	}
	if cw.n != f.Size {
		if lastErr == nil {
			lastErr = errors.New("all update sources failed")
		}
		return used, failed, step("download_failed", lastErr)
	}
	tracker.flush()
	if err := fh.Sync(); err != nil {
		return used, failed, step("not_writable", err)
	}
	return used, failed, nil
}

func (u *Updater) runSource(ctx context.Context, fetcher Fetcher, offset uint64, w *countedWriter, before uint64,
	started time.Time, skipSlowCheck bool, activity interface{ SetActivity(func()) }, onSlow func(float64) bool,
) (total uint64, err error, reason string, rate float64) {
	var activityAt atomic.Int64
	activityAt.Store(started.UnixNano())
	if activity != nil {
		activity.SetActivity(func() { activityAt.Store(time.Now().UnixNano()) })
		defer activity.SetActivity(nil)
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	type result struct {
		total uint64
		err   error
	}
	done := make(chan result, 1)
	go func() {
		total, err := fetcher.Fetch(attemptCtx, offset, w)
		done <- result{total: total, err: err}
	}()
	defer cancel()

	stall := u.cfg.SourceStallTimeout
	interval := u.cfg.SourceSlowInterval
	ticker := time.NewTicker(min(interval, stall))
	defer ticker.Stop()
	lastByteAt := started
	lastCount := before
	lastSlowCheck := started
	samples := []sourceSample{{at: started, n: before}}
	slowTriggered := false

	finish := func(r result, finishAt time.Time) (uint64, error, string, float64) {
		delta := w.n.Load() - before
		elapsed := finishAt.Sub(started).Seconds()
		if elapsed > 0 {
			rate = float64(delta) / elapsed
		}
		return r.total, r.err, "", rate
	}

	for {
		select {
		case r := <-done:
			return finish(r, time.Now())
		case <-ctx.Done():
			cancel()
			r := <-done
			return finish(r, time.Now())
		case now := <-ticker.C:
			current := w.n.Load()
			if current > lastCount {
				lastCount, lastByteAt = current, now
			}
			if active := time.Unix(0, activityAt.Load()); active.After(lastByteAt) {
				lastByteAt = active
			}
			if now.Sub(lastByteAt) >= stall {
				cancel()
				<-done
				delta := w.n.Load() - before
				elapsed := now.Sub(started).Seconds()
				if elapsed > 0 {
					rate = float64(delta) / elapsed
				}
				return 0, TransientSourceFailure("stalled", errors.New("source stopped sending bytes")), "stalled", rate
			}
			if !skipSlowCheck && !slowTriggered {
				samples = append(samples, sourceSample{at: now, n: current})
			}
			if skipSlowCheck || slowTriggered || now.Sub(started) < u.cfg.SourceSlowAfter || now.Sub(lastSlowCheck) < interval {
				continue
			}
			lastSlowCheck = now
			cutoff := now.Add(-u.cfg.SourceSlowWindow)
			base := samples[0]
			for len(samples) > 1 && !samples[1].at.After(cutoff) {
				samples = samples[1:]
				base = samples[0]
			}
			windowBytes := current - base.n
			windowSecs := now.Sub(base.at).Seconds()
			if windowSecs > 0 {
				rate = float64(windowBytes) / windowSecs
			}
			if now.Sub(started) >= u.cfg.SourceSlowAfter && windowBytes < u.cfg.SourceSlowBytes {
				slowTriggered = true
				if onSlow(rate) {
					cancel()
					<-done
					return 0, TransientSourceFailure("slow", errors.New("source transfer rate is below the minimum")), "slow", rate
				}
			}
		}
	}
}

func firstLive(states []sourceState) int {
	for i := range states {
		if !states[i].dropped {
			return i
		}
	}
	return -1
}

func firstNotSlow(states []sourceState, current int) int {
	for i := range states {
		if i != current && !states[i].dropped && !states[i].slow {
			return i
		}
	}
	return -1
}

func nextNotSlow(states []sourceState, current int) int {
	for i := current + 1; i < len(states); i++ {
		if !states[i].dropped && !states[i].slow {
			return i
		}
	}
	for i := 0; i < current; i++ {
		if !states[i].dropped && !states[i].slow {
			return i
		}
	}
	return -1
}

func fastestLive(states []sourceState) int {
	best := -1
	for i := range states {
		if states[i].dropped {
			continue
		}
		if best < 0 || states[i].rate > states[best].rate {
			best = i
		}
	}
	return best
}

func nextLive(states []sourceState, current int) (next int, wrapped bool) {
	for i := current + 1; i < len(states); i++ {
		if !states[i].dropped {
			return i, false
		}
	}
	for i := range states {
		if !states[i].dropped {
			return i, true
		}
	}
	return -1, false
}

func noneLive(states []sourceState) bool { return firstLive(states) < 0 }

func allLiveSlow(states []sourceState) bool {
	live := false
	for _, state := range states {
		if state.dropped {
			continue
		}
		live = true
		if !state.slow {
			return false
		}
	}
	return live
}

func waitSourceRound(ctx context.Context, u *Updater, round int) bool {
	delays := u.cfg.SourceBackoff
	if len(delays) == 0 {
		return true
	}
	if round >= len(delays) {
		round = len(delays) - 1
	}
	return sleep(ctx, delays[round])
}
