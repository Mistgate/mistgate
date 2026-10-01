package auth

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h := hashPassword("correct horse battery")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("not a PHC argon2id string: %s", h)
	}
	if !verifyPassword(h, "correct horse battery") {
		t.Fatal("right password refused")
	}
	for _, bad := range []string{"", "correct horse batter", "Correct horse battery"} {
		if verifyPassword(h, bad) {
			t.Errorf("wrong password %q accepted", bad)
		}
	}
	if h == hashPassword("correct horse battery") {
		t.Error("salt is not random")
	}
	// A hash verifies under the parameters it was made with, not the current ones.
	old := argon
	argon.time, argon.memKiB = 2, 16
	if !verifyPassword(h, "correct horse battery") {
		t.Error("hash made under other parameters no longer verifies")
	}
	argon = old
	for _, broken := range []string{
		"", "plain", "$argon2id$v=19$m=8,t=1,p=1$c2FsdA", "$argon2i$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=18$m=8,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=8,t=99,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=0$c2FsdA$aGFzaA", "$argon2id$v=19$m=8,t=1,p=1$!!!$aGFzaA", "$argon2id$v=19$m=8,t=1,p=1$c2FsdA$",
	} {
		if verifyPassword(broken, "x") {
			t.Errorf("accepted %q", broken)
		}
	}
	if verifyPassword(spoofHash(), "anything") { // it exists to be verified against, never to match
		t.Error("spoof hash matched a guess")
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
