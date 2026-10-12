package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; every authenticator app speaks SHA-1
	"crypto/sha256"
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

	"github.com/mistgate/mistgate/internal/panel/vault"
)

type argonParams struct {
	time, memKiB uint32
	threads      uint8
}

// productionArgon is ADR 0005's profile (OWASP minimum), the same in both editions.
var productionArgon = argonParams{time: 2, memKiB: 19 << 10, threads: 1}

var argon = productionArgon // tests lower it

const pepperLabel = "admin-password-pepper"

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

// passwordPepper is the secret mixed into every new password hash, derived from the master key (ADR 0005).
func passwordPepper(v *vault.Vault) []byte {
	if v == nil {
		return nil
	}
	return v.Derive(pepperLabel)
}

// passwordInput is what argon2id hashes: HMAC-SHA256(pepper, pw), or the raw password for a hash without a pepper.
func passwordInput(pepper []byte, pw string) []byte {
	if len(pepper) == 0 {
		return []byte(pw)
	}
	m := hmac.New(sha256.New, pepper)
	_, _ = m.Write([]byte(pw))
	return m.Sum(nil)
}

// hashPassword returns an argon2id PHC string with a fresh random salt; with a pepper it carries keyid=pepper.
func hashPassword(ctx context.Context, pepper []byte, pw string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt) // never fails on supported platforms
	key, err := deriveArgon2IDKey(ctx, passwordInput(pepper, pw), salt, argon.time, argon.memKiB, argon.threads, 32)
	if err != nil {
		return "", err
	}
	params := fmt.Sprintf("m=%d,t=%d,p=%d", argon.memKiB, argon.time, argon.threads)
	if len(pepper) != 0 {
		params += ",keyid=pepper"
	}
	return fmt.Sprintf("$argon2id$v=%d$%s$%s$%s", argon2.Version, params,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

type parsedPasswordHash struct {
	params   argonParams
	peppered bool
	salt     []byte
	key      []byte
}

// parsePasswordHash accepts exactly what hashPassword writes, today's and older profiles; anything else is malformed.
func parsePasswordHash(encoded string) (parsedPasswordHash, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return parsedPasswordHash{}, false
	}
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 && len(fields) != 4 {
		return parsedPasswordHash{}, false
	}
	m, okM := parseArgonParam(fields[0], "m=", 32)
	t, okT := parseArgonParam(fields[1], "t=", 32)
	p, okP := parseArgonParam(fields[2], "p=", 8)
	if !okM || !okT || !okP {
		return parsedPasswordHash{}, false
	}
	peppered := len(fields) == 4
	if peppered && fields[3] != "keyid=pepper" {
		return parsedPasswordHash{}, false
	}
	if len(parts[4]) != base64.RawStdEncoding.EncodedLen(16) || len(parts[5]) != base64.RawStdEncoding.EncodedLen(32) {
		return parsedPasswordHash{}, false
	}
	salt, errSalt := base64.RawStdEncoding.DecodeString(parts[4])
	key, errKey := base64.RawStdEncoding.DecodeString(parts[5])
	if errSalt != nil || errKey != nil || len(salt) != 16 || len(key) != 32 {
		return parsedPasswordHash{}, false
	}
	return parsedPasswordHash{
		params:   argonParams{time: uint32(t), memKiB: uint32(m), threads: uint8(p)},
		peppered: peppered,
		salt:     salt,
		key:      key,
	}, true
}

func parseArgonParam(field, prefix string, bitSize int) (uint64, bool) {
	if !strings.HasPrefix(field, prefix) {
		return 0, false
	}
	value := strings.TrimPrefix(field, prefix)
	if value == "" {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(value, 10, bitSize)
	return n, err == nil
}

func validArgonParams(params argonParams) bool {
	return params.memKiB >= 1 && params.memKiB <= 65536 &&
		params.time >= 1 && params.time <= 4 &&
		params.threads >= 1 && params.threads <= 4
}

// verifyPassword checks pw against a hashPassword string in constant time. A malformed
// or absurdly expensive hash never matches.
func verifyPassword(ctx context.Context, pepper []byte, encoded, pw string) (bool, error) {
	parsed, ok := parsePasswordHash(encoded)
	if !ok || !validArgonParams(parsed.params) {
		return false, nil
	}
	if parsed.peppered && len(pepper) == 0 {
		return false, nil
	}
	var input []byte
	if parsed.peppered {
		input = passwordInput(pepper, pw)
	} else {
		input = []byte(pw)
	}
	got, err := deriveArgon2IDKey(ctx, input, parsed.salt, parsed.params.time, parsed.params.memKiB, parsed.params.threads, uint32(len(parsed.key)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, parsed.key) == 1, nil
}

// needsRehash reports whether a hash that just verified should be re-hashed with the current profile and the pepper.
func needsRehash(encoded string) bool {
	parsed, ok := parsePasswordHash(encoded)
	return !ok || !validArgonParams(parsed.params) || !parsed.peppered || parsed.params != argon
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
		entry.hash, entry.err = hashPassword(ctx, nil, string(b))
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
