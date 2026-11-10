package auth

import (
	"context"
	"database/sql/driver"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	sqlite "modernc.org/sqlite"
)

var authCleanupCancel atomic.Pointer[context.CancelFunc]

func init() {
	sqlite.MustRegisterScalarFunction("auth_test_cancel_request", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		if cancel := authCleanupCancel.Load(); cancel != nil {
			(*cancel)()
		}
		return int64(0), nil
	})
}

func useAuthCleanupCancel(cancel context.CancelFunc) func() {
	authCleanupCancel.Store(&cancel)
	return func() { authCleanupCancel.Store(nil) }
}

func TestClearLoginFailuresSurvivesRequestCancellation(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, _ := passwordAdmin(t, s, st, "ada")
	key := stepUpLockKey + adminID
	if _, err := st.RecordLoginFailure(context.Background(), key, *clock, MaxSignInFailures, FailureWindow, LockDuration); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(context.Background(), `CREATE TRIGGER cancel_failure_cleanup AFTER DELETE ON login_failure BEGIN SELECT auth_test_cancel_request(); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(useAuthCleanupCancel(cancel))
	if err := s.clearLoginFailures(ctx, key); err != nil {
		t.Fatalf("clear failure count after disconnect: %v", err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("the database trigger did not cancel the request")
	}
	assertNoLoginFailure(t, st, key, *clock)
}

func TestFinishStepUpReleasesReservationAfterRequestCancellation(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	if _, err := st.W.ExecContext(context.Background(), `CREATE TRIGGER cancel_stepup_totp BEFORE UPDATE OF totp_step ON admin_password BEGIN SELECT auth_test_cancel_request(); SELECT RAISE(ABORT, 'request cancelled'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(authed(t, s, cookie))
	t.Cleanup(useAuthCleanupCancel(cancel))
	good := testLoginCode(clock, secret)
	_, err := s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: good}, cookie))
	if codeOf(err) != connect.CodeInternal {
		t.Fatalf("cancelled step-up proof: %v", err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("the database trigger did not cancel the request")
	}
	assertNoLoginFailure(t, st, stepUpLockKey+adminID, *clock)
}

func TestSuccessfulSignInClearsPendingReservationsWithinSourceLimit(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	good := testLoginCode(clock, secret)

	ownerGate, pendingGate := make(chan struct{}), make(chan struct{})
	ownerStarted := make(chan struct{})
	var checks atomic.Int32
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = func() {
		if checks.Add(1) == 1 {
			close(ownerStarted)
			<-ownerGate
		} else {
			<-pendingGate
		}
	}
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })

	ownerDone := make(chan error, 1)
	go func() {
		_, err := login(s, "ada", testPassword, good)
		ownerDone <- err
	}()
	select {
	case <-ownerStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("owner password check did not reach argon2")
	}
	waitForFailureCount(t, st, "ada", *clock, 1)

	var pending sync.WaitGroup
	for range MaxSignInFailures - 1 {
		pending.Add(1)
		go func() {
			defer pending.Done()
			login(s, "ada", "a wrong password guess", "000000")
		}()
	}
	waitForFailureCount(t, st, "ada", *clock, MaxSignInFailures)
	close(ownerGate)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner sign-in: %v", err)
	}
	close(pendingGate)
	pending.Wait()

	assertNoLoginFailure(t, st, "ada", *clock)
	source, err := st.LoginFailures(context.Background(), sourceLockPrefix+SourceKey(netip.Addr{}), *clock, FailureWindow)
	if err != nil {
		t.Fatal(err)
	}
	if source.Failures != MaxSignInFailures-1 {
		t.Fatalf("pending failures against the source = %d, want %d", source.Failures, MaxSignInFailures-1)
	}
}

func waitForFailureCount(t *testing.T, st *store.Store, key string, now time.Time, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := st.LoginFailures(context.Background(), key, now, FailureWindow)
		if err != nil {
			t.Fatal(err)
		}
		if f.Failures == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("failure count for %q stayed at %d; want %d", key, f.Failures, want)
		}
		time.Sleep(time.Millisecond)
	}
}
