package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; every authenticator app speaks SHA-1
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id cost (RFC 9106 second recommended option: 64 MiB, 3 passes). Tests lower it.
var argon = struct {
	time, memKiB uint32
	threads      uint8
}{3, 64 << 10, 2}

const (
	minPasswordRunes          = 12
	maxPasswordBytes          = 256 // bounds the hashing work an unauthenticated caller can ask for
	minLogin                  = 3
	maxLogin                  = 64
	maxConcurrentArgon2IDKeys = 1

	totpDigits = 6
	totpPeriod = 30 // seconds
	totpWindow = 1  // steps accepted on either side of now (clock skew)
)

var (
	argon2Slots = make(chan struct{}, maxConcurrentArgon2IDKeys)
	argon2IDKey = argon2.IDKey
	// argon2BeforeAcquireForTest lets tests know a verification reached the cancellable queue.
	argon2BeforeAcquireForTest func()
)

func deriveArgon2IDKey(ctx context.Context, password, salt []byte, timeCost, memoryKiB uint32, threads uint8, keyLen uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hook := argon2BeforeAcquireForTest; hook != nil {
		hook()
	}
	select {
	case argon2Slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-argon2Slots
		return nil, err
	}
	defer func() {
		<-argon2Slots
		scheduleMemoryRelease()
	}()
	return argon2IDKey(password, salt, timeCost, memoryKiB, threads, keyLen), nil
}

// hashPassword returns an argon2id PHC string with a fresh random salt.
func hashPassword(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt) // never fails on supported platforms
	key, err := deriveArgon2IDKey(ctx, []byte(pw), salt, argon.time, argon.memKiB, argon.threads, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argon.memKiB, argon.time, argon.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword checks pw against a hashPassword string in constant time. A malformed
// or absurdly expensive hash never matches.
func verifyPassword(ctx context.Context, encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, nil
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 16 || p == 0 {
		return false, nil
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false, nil
	}
	got, err := deriveArgon2IDKey(ctx, []byte(pw), salt, t, m, p, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type spoofHashEntry struct {
	ready chan struct{}
	hash  string
	err   error
}

var spoofHashCache atomic.Pointer[spoofHashEntry]

// spoofHash is a valid hash of a random password, verified against when the login does
// not exist so that unknown and known logins cost the same time.

func spoofHash(ctx context.Context) (string, error) {
	for {
		if cached := spoofHashCache.Load(); cached != nil {
			select {
			case <-cached.ready:
				return cached.hash, cached.err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		entry := &spoofHashEntry{ready: make(chan struct{})}
		if !spoofHashCache.CompareAndSwap(nil, entry) {
			continue
		}
		b := make([]byte, 16)
		rand.Read(b)
		entry.hash, entry.err = hashPassword(ctx, string(b))
		if entry.err != nil {
			spoofHashCache.CompareAndSwap(entry, nil)
		}
		close(entry.ready)
		return entry.hash, entry.err
	}
}

// checkPasswordPolicy and normLogin validate what a new admin typed.
func checkPasswordPolicy(pw string) error {
	if len(pw) > maxPasswordBytes {
		return errors.New("password is too long")
	}
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Errorf("password must be at least %d characters", minPasswordRunes)
	}
	return nil
}

// normLogin lower-cases and validates a login name: a-z 0-9 . _ @ -, 3 to 64 characters.
func normLogin(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < minLogin || len(s) > maxLogin {
		return "", fmt.Errorf("login must be %d-%d characters", minLogin, maxLogin)
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '@' || r == '-') {
			return "", errors.New("login may contain a-z 0-9 . _ @ - only")
		}
	}
	return s, nil
}

// --- TOTP (RFC 6238, SHA-1, 6 digits, 30 s) ---

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() []byte {
	s := make([]byte, 20) // 160 bits, the SHA-1 block-friendly size RFC 4226 recommends
	rand.Read(s)
	return s
}

func totpURI(issuer, login string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", b32.EncodeToString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(totpDigits))
	q.Set("period", strconv.Itoa(totpPeriod))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+login) + "?" + q.Encode()
}

// totpCode is HOTP(secret, step) truncated to 6 digits (RFC 4226 §5.3).
func totpCode(secret []byte, step int64) string {
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(c[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000)
}

func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// matchTOTP checks code against the steps around now and returns the step that matched.
// All candidate steps are compared, so timing does not depend on which one matched.
func matchTOTP(secret []byte, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	var matched int64
	ok := 0
	for d := int64(-totpWindow); d <= totpWindow; d++ {
		step := totpStep(now) + d
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			matched, ok = step, 1
		}
	}
	return matched, ok == 1
}
