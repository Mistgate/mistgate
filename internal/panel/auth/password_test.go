package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	savedArgon := argon
	argon = argonParams{time: 1, memKiB: 8, threads: 1}
	t.Cleanup(func() { argon = savedArgon })
	pepper := []byte("test password pepper")
	h, err := hashPassword(context.Background(), pepper, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=8,t=1,p=1,keyid=pepper$") {
		t.Fatalf("not a PHC argon2id string: %s", h)
	}
	if ok, err := verifyPassword(context.Background(), pepper, h, "correct horse battery"); err != nil || !ok {
		t.Fatal("right password refused")
	}
	for _, bad := range []string{"", "correct horse batter", "Correct horse battery"} {
		if ok, err := verifyPassword(context.Background(), pepper, h, bad); err != nil || ok {
			t.Errorf("wrong password %q accepted", bad)
		}
	}
	if other, err := hashPassword(context.Background(), pepper, "correct horse battery"); err != nil || h == other {
		t.Error("salt is not random")
	}
	// A hash verifies under the parameters it was made with, not the current ones.
	argon = argonParams{time: 2, memKiB: 16, threads: 1}
	if ok, err := verifyPassword(context.Background(), pepper, h, "correct horse battery"); err != nil || !ok {
		t.Error("hash made under other parameters no longer verifies")
	}
	for _, broken := range []string{
		"", "plain", "$argon2id$v=19$m=8,t=1,p=1$c2FsdA", "$argon2i$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=18$m=8,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=8,t=99,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=0$c2FsdA$aGFzaA", "$argon2id$v=19$m=8,t=1,p=1,junk$c2FsdA$aGFzaA", "$argon2id$v=19$m=8,t=1,p=1$!!!$aGFzaA", "$argon2id$v=19$m=8,t=1,p=1$c2FsdA$",
	} {
		if ok, err := verifyPassword(context.Background(), pepper, broken, "x"); err != nil || ok {
			t.Errorf("accepted %q", broken)
		}
	}
	spoof, err := spoofHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := verifyPassword(context.Background(), nil, spoof, "anything"); err != nil || ok { // it exists to be verified against, never to match
		t.Error("spoof hash matched a guess")
	}
}

func TestProductionArgonIsADR0005(t *testing.T) {
	if productionArgon != (argonParams{time: 2, memKiB: 19456, threads: 1}) {
		t.Fatalf("production Argon2 profile = %+v", productionArgon)
	}
}

func TestPepper(t *testing.T) {
	savedArgon := argon
	argon = argonParams{time: 1, memKiB: 8, threads: 1}
	t.Cleanup(func() { argon = savedArgon })
	ctx := context.Background()
	pepper := []byte("pepper one")
	h, err := hashPassword(ctx, pepper, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	originalIDKey := argon2IDKey
	var calls int
	argon2IDKey = func(password, salt []byte, timeCost, memoryKiB uint32, threads uint8, keyLen uint32) []byte {
		calls++
		return originalIDKey(password, salt, timeCost, memoryKiB, threads, keyLen)
	}
	t.Cleanup(func() { argon2IDKey = originalIDKey })
	if ok, err := verifyPassword(ctx, pepper, h, "correct horse battery"); err != nil || !ok {
		t.Errorf("same pepper: matched=%v err=%v", ok, err)
	}
	if ok, err := verifyPassword(ctx, []byte("pepper two"), h, "correct horse battery"); err != nil || ok {
		t.Errorf("other pepper: matched=%v err=%v", ok, err)
	}
	callsBeforeNilPepper := calls
	if ok, err := verifyPassword(ctx, nil, h, "correct horse battery"); err != nil || ok || calls != callsBeforeNilPepper {
		t.Errorf("nil pepper: matched=%v calls=%d err=%v; want no Argon2 call", ok, calls, err)
	}

	legacy, err := hashPassword(ctx, nil, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range [][]byte{nil, pepper, []byte("pepper two")} {
		if ok, err := verifyPassword(ctx, candidate, legacy, "correct horse battery"); err != nil || !ok {
			t.Errorf("legacy hash with pepper %q: matched=%v err=%v", candidate, ok, err)
		}
	}

	for _, malformed := range []string{
		strings.Replace(h, ",keyid=pepper$", ",keyid=other$", 1),
		strings.Replace(h, ",keyid=pepper$", ",keyid=pepper,extra=x$", 1),
		strings.Replace(h, "m=8,t=1,p=1,keyid=pepper", "m=8,p=1,t=1,keyid=pepper", 1),
	} {
		if ok, err := verifyPassword(ctx, pepper, malformed, "correct horse battery"); err != nil || ok {
			t.Errorf("malformed hash matched: %v", malformed)
		}
	}
}

func TestVerifyPasswordRejectsTrailingParameter(t *testing.T) {
	fixedKey := make([]byte, 32)
	encoded := "$argon2id$v=19$m=8,t=1,p=1,junk$" + base64.RawStdEncoding.EncodeToString(make([]byte, 16)) + "$" + base64.RawStdEncoding.EncodeToString(fixedKey)
	originalIDKey := argon2IDKey
	var calls int
	argon2IDKey = func([]byte, []byte, uint32, uint32, uint8, uint32) []byte {
		calls++
		return fixedKey
	}
	t.Cleanup(func() { argon2IDKey = originalIDKey })

	ok, err := verifyPassword(context.Background(), nil, encoded, "password")
	if err != nil || ok || calls != 0 {
		t.Fatalf("trailing parameter: matched=%v, calls=%d, err=%v; want no match without hashing", ok, calls, err)
	}
}

func TestVerifyPasswordCeiling(t *testing.T) {
	fixedKey := make([]byte, 32)
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(fixedKey)
	encoded := func(m, timeCost, threads uint32) string {
		return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", m, timeCost, threads, salt, key)
	}
	originalIDKey := argon2IDKey
	var calls int
	var got argonParams
	argon2IDKey = func(_ []byte, _ []byte, timeCost, memoryKiB uint32, threads uint8, _ uint32) []byte {
		calls++
		got = argonParams{time: timeCost, memKiB: memoryKiB, threads: threads}
		return fixedKey
	}
	t.Cleanup(func() { argon2IDKey = originalIDKey })

	if ok, err := verifyPassword(context.Background(), nil, encoded(65536, 3, 2), "password"); err != nil || !ok {
		t.Fatalf("legacy VPS profile did not reach the key function: matched=%v err=%v", ok, err)
	}
	if got != (argonParams{time: 3, memKiB: 65536, threads: 2}) {
		t.Fatalf("key function received %+v", got)
	}
	for _, params := range [][3]uint32{{65537, 3, 2}, {8, 5, 1}, {8, 1, 5}, {8, 1, 0}} {
		before := calls
		if ok, err := verifyPassword(context.Background(), nil, encoded(params[0], params[1], params[2]), "password"); err != nil || ok || calls != before {
			t.Errorf("parameters m=%d,t=%d,p=%d: matched=%v calls=%d err=%v", params[0], params[1], params[2], ok, calls, err)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	savedArgon := argon
	argon = argonParams{time: 1, memKiB: 8, threads: 1}
	t.Cleanup(func() { argon = savedArgon })
	ctx := context.Background()
	legacy, err := hashPassword(ctx, nil, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !needsRehash(legacy) {
		t.Error("legacy hash did not need rehashing")
	}
	pepper := []byte("test password pepper")
	argon = argonParams{time: 1, memKiB: 16, threads: 1}
	olderPeppered, err := hashPassword(ctx, pepper, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	argon = argonParams{time: 1, memKiB: 8, threads: 1}
	if !needsRehash(olderPeppered) {
		t.Error("peppered hash with older parameters did not need rehashing")
	}
	current, err := hashPassword(ctx, pepper, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if needsRehash(current) {
		t.Error("current peppered hash needs rehashing")
	}
	if !needsRehash("not a password hash") {
		t.Error("malformed hash did not need rehashing")
	}
}

func TestSpoofHashUsesTheCurrentProfile(t *testing.T) {
	savedArgon := argon
	argon = argonParams{time: 2, memKiB: 13, threads: 1}
	savedCache := spoofHashCache.Swap(nil)
	t.Cleanup(func() {
		argon = savedArgon
		spoofHashCache.Store(savedCache)
	})
	h, err := spoofHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := parsePasswordHash(h)
	if !ok || parsed.peppered || parsed.params != argon {
		t.Fatalf("spoof hash profile = %+v peppered=%v parsed=%v", parsed.params, parsed.peppered, ok)
	}
}

func TestConcurrentPasswordVerificationsRespectArgonLimit(t *testing.T) {
	savedArgon := argon
	argon.time, argon.memKiB, argon.threads = 1, 8, 1
	t.Cleanup(func() { argon = savedArgon })
	encoded, err := hashPassword(context.Background(), nil, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	originalIDKey := argon2IDKey
	var active, maxActive atomic.Int32
	argon2IDKey = func(password, salt []byte, timeCost, memoryKiB uint32, threads uint8, keyLen uint32) []byte {
		current := active.Add(1)
		for previous := maxActive.Load(); current > previous; previous = maxActive.Load() {
			if maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		runtime.Gosched()
		key := originalIDKey(password, salt, timeCost, memoryKiB, threads, keyLen)
		active.Add(-1)
		return key
	}
	t.Cleanup(func() { argon2IDKey = originalIDKey })

	const requests = 16
	start := make(chan struct{})
	results := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := verifyPassword(context.Background(), nil, encoded, "correct horse battery")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("verify password: %v", err)
		}
	}
	if got := maxActive.Load(); got > maxConcurrentArgon2IDKeys {
		t.Fatalf("concurrent Argon2 calls = %d, limit = %d", got, maxConcurrentArgon2IDKeys)
	}
}

func TestVerifyPasswordQueueHonorsCancellation(t *testing.T) {
	savedArgon := argon
	argon.time, argon.memKiB, argon.threads = 1, 8, 1
	t.Cleanup(func() { argon = savedArgon })
	encoded, err := hashPassword(context.Background(), nil, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	originalIDKey := argon2IDKey
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	argon2IDKey = func(password, salt []byte, timeCost, memoryKiB uint32, threads uint8, keyLen uint32) []byte {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return originalIDKey(password, salt, timeCost, memoryKiB, threads, keyLen)
	}
	attempted := make(chan struct{}, 2)
	argon2BeforeAcquireForTest = func() { attempted <- struct{}{} }
	t.Cleanup(func() {
		unblock()
		argon2IDKey = originalIDKey
		argon2BeforeAcquireForTest = nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := verifyPassword(context.Background(), nil, encoded, "correct horse battery")
		firstDone <- err
	}()
	<-attempted
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := verifyPassword(ctx, nil, encoded, "correct horse battery")
		secondDone <- err
	}()
	<-attempted
	cancel()
	var secondErr error
	select {
	case secondErr = <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("queued verification did not return after cancellation")
	}
	unblock()
	firstErr := <-firstDone
	if !errors.Is(secondErr, context.Canceled) {
		t.Fatalf("queued verification error = %v, want context.Canceled", secondErr)
	}
	if firstErr != nil {
		t.Fatalf("first verification error = %v", firstErr)
	}
}

func TestPasswordPolicyAndLogin(t *testing.T) {
	for pw, ok := range map[string]bool{
		"": false, "short": false, "elevenchars": false, "twelvechars!": true, strings.Repeat("я", 12): true,
		strings.Repeat("a", 256): true, strings.Repeat("a", 257): false,
	} {
		if err := checkPasswordPolicy(pw); (err == nil) != ok {
			t.Errorf("%.20q: %v", pw, err)
		}
	}
	for in, want := range map[string]string{"Ada": "ada", "  Ada.Lovelace@Example.com ": "ada.lovelace@example.com", "a_b-c.d": "a_b-c.d"} {
		if got, err := normLogin(in); err != nil || got != want {
			t.Errorf("normLogin(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ab", strings.Repeat("a", 65), "sp ace", "ta\tb", "ü", "a/b", "a;b", "a'b", "a\x00b"} {
		if _, err := normLogin(bad); err == nil {
			t.Errorf("login %q accepted", bad)
		}
	}
}

// RFC 6238 appendix B, SHA-1 secret "12345678901234567890"; the RFC lists 8 digits, the
// 6-digit code is the last six of them.
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		if got := totpCode(secret, ts/30); got != want {
			t.Errorf("t=%d: %s, want %s", ts, got, want)
		}
	}
}

func TestMatchTOTP(t *testing.T) {
	secret := newTOTPSecret()
	if len(secret) != 20 {
		t.Fatal("secret length")
	}
	now := time.Unix(1_700_000_000, 0)
	cur := totpStep(now)
	for d, ok := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		step, got := matchTOTP(secret, totpCode(secret, cur+d), now)
		if got != ok || (ok && step != cur+d) {
			t.Errorf("step %+d: matched=%v step=%d", d, got, step)
		}
	}
	code := totpCode(secret, cur)
	if _, ok := matchTOTP(secret, code[:3]+" "+code[3:], now); !ok {
		t.Error("spaces in the code must be ignored")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "00000 00"} {
		if _, ok := matchTOTP(secret, bad, now); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := matchTOTP(newTOTPSecret(), code, now); ok {
		t.Error("code of another secret accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	u, err := url.Parse(totpURI("Mist gate", "ada@example.com", secret))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Mist gate:ada@example.com" ||
		q.Get("secret") != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" || q.Get("issuer") != "Mist gate" ||
		q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("uri %s", u)
	}
}
