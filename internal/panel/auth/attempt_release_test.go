package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func testLoginCode(clock *time.Time, secret []byte) string {
	*clock = clock.Add(time.Minute)
	return totpCode(secret, totpStep(*clock))
}

func assertNoLoginFailure(t *testing.T, st *store.Store, key string, now time.Time) {
	t.Helper()
	f, err := st.LoginFailures(context.Background(), key, now, FailureWindow)
	if err != nil {
		t.Fatal(err)
	}
	if f.Failures != 0 || f.Locked(now) {
		t.Fatalf("reservation remains for %q: %+v", key, f)
	}
}

func occupyArgon2Queue(t *testing.T) func() {
	t.Helper()
	select {
	case argon2Slots <- struct{}{}:
	default:
		t.Fatal("argon2 slot was already occupied")
	}
	for range maxArgon2Waiters {
		argon2Waiters <- struct{}{}
	}
	return func() {
		for range maxArgon2Waiters {
			<-argon2Waiters
		}
		<-argon2Slots
	}
}

func TestPasswordLoginReleasesReservationsAfterCancellation(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	good := testLoginCode(clock, secret)
	ctx, cancel := context.WithCancel(context.Background())
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = cancel
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })

	_, err := s.PasswordLogin(ctx, connect.NewRequest(&adminv1.PasswordLoginRequest{
		Login: "ada", Password: testPassword, TotpCode: good,
	}))
	if codeOf(err) != connect.CodeUnavailable {
		t.Fatalf("cancelled proof: %v", err)
	}
	assertNoLoginFailure(t, st, "ada", *clock)
	assertNoLoginFailure(t, st, sourceLockPrefix+"unknown", *clock)
	argon2BeforeAcquireForTest = originalHook
	if _, err := login(s, "ada", testPassword, good); err != nil {
		t.Fatalf("sign-in after cancellation: %v", err)
	}
}

func TestPasswordLoginReleasesReservationsWhenArgon2IsBusy(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	good := testLoginCode(clock, secret)
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = nil
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })
	releaseQueue := occupyArgon2Queue(t)
	_, err := login(s, "ada", testPassword, good)
	releaseQueue()
	if codeOf(err) != connect.CodeUnavailable {
		t.Fatalf("busy proof: %v", err)
	}
	assertNoLoginFailure(t, st, "ada", *clock)
	assertNoLoginFailure(t, st, sourceLockPrefix+"unknown", *clock)
}

func TestPasswordLoginReleasesReservationsAfterInternalError(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	good := testLoginCode(clock, secret)
	var dropErr error
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = func() {
		_, dropErr = st.W.ExecContext(context.Background(), `DROP TABLE admin_password`)
	}
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })

	_, err := login(s, "ada", testPassword, good)
	if codeOf(err) != connect.CodeInternal {
		t.Fatalf("internal error after proof: %v", err)
	}
	if dropErr != nil {
		t.Fatalf("prepare internal error: %v", dropErr)
	}
	assertNoLoginFailure(t, st, "ada", *clock)
	assertNoLoginFailure(t, st, sourceLockPrefix+"unknown", *clock)
}

func TestPasswordLoginReleasesSourceReservationWhenLoginLocksOnReservation(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	for range MaxSignInFailures {
		if _, err := st.RecordLoginFailure(context.Background(), "ada", *clock, MaxSignInFailures+reservationLockHeadroom, FailureWindow, LockDuration); err != nil {
			t.Fatal(err)
		}
	}
	good := testLoginCode(clock, secret)
	_, err := login(s, "ada", testPassword, good)
	if codeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("locked reservation: %v", err)
	}
	if f, err := st.LoginFailures(context.Background(), "ada", *clock, FailureWindow); err != nil || !f.Locked(*clock) {
		t.Fatalf("login should remain locked: %+v %v", f, err)
	}
	assertNoLoginFailure(t, st, sourceLockPrefix+"unknown", *clock)
}

func TestPasswordLoginReleasesSourceReservationWhenLoginLocksDuringSessionCreation(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	good := testLoginCode(clock, secret)
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = func() {
		for range MaxSignInFailures - 1 {
			if _, err := st.RecordLoginFailure(context.Background(), "ada", *clock, MaxSignInFailures, FailureWindow, LockDuration); err != nil {
				panic(err)
			}
		}
	}
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })

	_, err := login(s, "ada", testPassword, good)
	if codeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("raced lock during session creation: %v", err)
	}
	if f, err := st.LoginFailures(context.Background(), "ada", *clock, FailureWindow); err != nil || !f.Locked(*clock) {
		t.Fatalf("login should remain locked: %+v %v", f, err)
	}
	assertNoLoginFailure(t, st, sourceLockPrefix+"unknown", *clock)
}

func TestFinishStepUpReleasesReservationAfterInternalError(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	good := testLoginCode(clock, secret)
	ctx := authed(t, s, cookie)
	if _, err := st.W.ExecContext(context.Background(), `DROP TABLE admin_password`); err != nil {
		t.Fatal(err)
	}
	_, err := s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: good}, cookie))
	if codeOf(err) != connect.CodeInternal {
		t.Fatalf("step-up internal error: %v", err)
	}
	assertNoLoginFailure(t, st, stepUpLockKey+adminID, *clock)
}

func TestChangePasswordReleasesReservationAfterCancellation(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	ctx, cancel := context.WithCancel(authed(t, s, cookie))
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = cancel
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })

	_, err := s.ChangePassword(ctx, cookieReq(&adminv1.ChangePasswordRequest{
		CurrentPassword: testPassword, NewPassword: "a new long password 123",
	}, cookie))
	if codeOf(err) != connect.CodeUnavailable {
		t.Fatalf("cancelled password change: %v", err)
	}
	assertNoLoginFailure(t, st, stepUpLockKey+adminID, *clock)
}

func TestChangePasswordReleasesReservationWhenArgon2IsBusy(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	ctx := authed(t, s, cookie)
	originalHook := argon2BeforeAcquireForTest
	argon2BeforeAcquireForTest = nil
	t.Cleanup(func() { argon2BeforeAcquireForTest = originalHook })
	releaseQueue := occupyArgon2Queue(t)
	_, err := s.ChangePassword(ctx, cookieReq(&adminv1.ChangePasswordRequest{
		CurrentPassword: testPassword, NewPassword: "a new long password 123",
	}, cookie))
	releaseQueue()
	if codeOf(err) != connect.CodeUnavailable {
		t.Fatalf("busy password change: %v", err)
	}
	assertNoLoginFailure(t, st, stepUpLockKey+adminID, *clock)
}

func TestFinishStepUpReservationsBoundConcurrentWrongCodes(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	bad := "000000"
	for delta := int64(-1); delta <= 1; delta++ {
		if totpCode(secret, totpStep(*clock)+delta) == bad {
			bad = "000001"
		}
	}
	ctx := authed(t, s, cookie)

	const attempts = 40
	var evaluated, refused, other atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: bad}, cookie))
			switch codeOf(err) {
			case connect.CodeUnauthenticated:
				evaluated.Add(1)
			case connect.CodeResourceExhausted:
				refused.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	f, err := st.LoginFailures(context.Background(), stepUpLockKey+adminID, *clock, FailureWindow)
	if err != nil {
		t.Fatal(err)
	}
	if evaluated.Load() > MaxSignInFailures || !f.Locked(*clock) || other.Load() != 0 || refused.Load() == 0 {
		t.Fatalf("step-up burst evaluated=%d refused=%d other=%d failures=%+v", evaluated.Load(), refused.Load(), other.Load(), f)
	}
	_, err = s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: totpCode(secret, totpStep(*clock))}, cookie))
	if codeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("right code while locked: %v", err)
	}
}

func TestChangePasswordReservationsBoundConcurrentWrongPasswords(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	ctx := authed(t, s, cookie)

	const attempts = 40
	var wrong, refused, busy, other atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ChangePassword(ctx, cookieReq(&adminv1.ChangePasswordRequest{
				CurrentPassword: "not the password at all", NewPassword: "a new long password 123",
			}, cookie))
			switch codeOf(err) {
			case connect.CodeInvalidArgument:
				wrong.Add(1)
			case connect.CodeResourceExhausted:
				refused.Add(1)
			case connect.CodeUnavailable:
				busy.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	f, err := st.LoginFailures(context.Background(), stepUpLockKey+adminID, *clock, FailureWindow)
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Load() > MaxSignInFailures || !f.Locked(*clock) || other.Load() != 0 || refused.Load()+busy.Load() == 0 {
		t.Fatalf("password-change burst wrong=%d refused=%d busy=%d other=%d failures=%+v", wrong.Load(), refused.Load(), busy.Load(), other.Load(), f)
	}

	*clock = clock.Add(LockDuration + time.Minute)
	if _, err := s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{
		TotpCode: totpCode(secret, totpStep(*clock)),
	}, cookie)); err != nil {
		t.Fatalf("step-up after lock expiry: %v", err)
	}
	if _, err := changePassword(s, cookie, testPassword, "a new long password 123"); err != nil {
		t.Fatalf("right password after lock expiry: %v", err)
	}
	assertNoLoginFailure(t, st, stepUpLockKey+adminID, *clock)
}
