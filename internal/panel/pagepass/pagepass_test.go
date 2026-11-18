package pagepass

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

var key = bytes.Repeat([]byte{9}, 32)

func TestPasswordIsEightUnambiguousCharsAsTwoGroups(t *testing.T) {
	re := regexp.MustCompile(`^[2-9a-km-np-z]{4}-[2-9a-km-np-z]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		pw := Password(key, strings.Repeat("t", 40)+string(rune('A'+i%26))+string(rune('a'+i/26)), 0)
		if !re.MatchString(pw) {
			t.Fatalf("%q", pw)
		}
		seen[pw] = true
	}
	if len(seen) < 495 { // 31^8 values: a clash in 500 is a broken derivation
		t.Errorf("only %d distinct passwords in 500", len(seen))
	}
	for _, bad := range "01lo" {
		if strings.ContainsRune(alphabet, bad) {
			t.Errorf("the alphabet has the look-alike %q", bad)
		}
	}
	if len(alphabet) != 32 { // a byte masked with 31 must pick without bias
		t.Errorf("alphabet of %d characters", len(alphabet))
	}
}

func TestPasswordFollowsTokenAndKey(t *testing.T) {
	a := Password(key, "token-a", 0)
	if a != Password(key, "token-a", 0) {
		t.Error("not deterministic")
	}
	if a == Password(key, "token-b", 0) {
		t.Error("two tokens, one password")
	}
	if a == Password(bytes.Repeat([]byte{8}, 32), "token-a", 0) {
		t.Error("two keys, one password")
	}
}

func TestCheckForgivesWhatPeopleTypeAndNothingElse(t *testing.T) {
	pw := Password(key, "token-a", 0)
	raw := strings.ReplaceAll(pw, "-", "")
	for _, ok := range []string{pw, strings.ToUpper(pw), raw, " " + raw[:4] + " " + raw[4:] + " ", raw[:4] + "‑" + raw[4:]} {
		if !Check(key, "token-a", 0, ok) {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{"", raw[:7], raw + "a", "aaaa-bbbb", Password(key, "token-b", 0), strings.Repeat(raw, 20)} {
		if Check(key, "token-a", 0, bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestCookieIsBoundToTheTokenAndRevealsNothing(t *testing.T) {
	c := Cookie(key, "token-a", 0)
	if !CheckCookie(key, "token-a", 0, c) {
		t.Fatal("own cookie refused")
	}
	if CheckCookie(key, "token-b", 0, c) || CheckCookie(key, "token-a", 0, "") || CheckCookie(key, "token-a", 0, c+"x") || CheckCookie(bytes.Repeat([]byte{8}, 32), "token-a", 0, c) {
		t.Error("a foreign or damaged cookie was accepted")
	}
	pw := strings.ReplaceAll(Password(key, "token-a", 0), "-", "")
	if strings.Contains(c, pw) || strings.Contains(c, "token-a") {
		t.Error("the cookie shows the password or the token")
	}
}

// The algorithm as it was before generations existed, kept here so that generation 0 is checked byte for byte against it
// (nobody's password or cookie may change with the migration).
func legacyMac(key []byte, purpose, token string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(token))
	return m.Sum(nil)
}

func legacyPassword(key []byte, token string) string {
	sum := legacyMac(key, "password", token)
	var b [length + 1]byte
	for i := 0; i < length; i++ {
		j := i
		if i >= length/2 {
			j++
		}
		b[j] = alphabet[sum[i]&31]
	}
	b[length/2] = '-'
	return string(b[:])
}

func TestGenerationZeroIsTheOldPasswordAndCookieByteForByte(t *testing.T) {
	for i := 0; i < 300; i++ {
		token := strings.Repeat("k", 30) + string(rune('A'+i%26)) + string(rune('a'+i/26))
		if got, want := Password(key, token, 0), legacyPassword(key, token); got != want {
			t.Fatalf("password of %q: %q, was %q", token, got, want)
		}
		if got, want := Cookie(key, token, 0), base64.RawURLEncoding.EncodeToString(legacyMac(key, "cookie", token)); got != want {
			t.Fatalf("cookie of %q: %q, was %q", token, got, want)
		}
	}
	// Values worked out apart from this code (HMAC-SHA256, key of 32 bytes of 9, purpose, a zero byte, the token), so a change of the
	// derivation fails here even if legacyPassword above were changed with it.
	for _, c := range []struct{ token, password, cookie string }{
		{"token-a", "2yz9-h6a7", "Tb170rktjHTa_y0NYn2tTzXcKXTAQuVWoNQGO4Gc4q8"},
		{"alice-link-token-0123456789", "c9ry-9txc", "EYbE8JMQk4kgJHYJS5o8V70IcFzLrUahq8XH8R42i1Y"},
	} {
		if got := Password(key, c.token, 0); got != c.password {
			t.Errorf("password of %q is %q, pinned %q", c.token, got, c.password)
		}
		if got := Cookie(key, c.token, 0); got != c.cookie {
			t.Errorf("cookie of %q is %q, pinned %q", c.token, got, c.cookie)
		}
	}
}

func TestEachGenerationIsANewPasswordAndANewCookie(t *testing.T) {
	seenPw, seenCk := map[string]int{}, map[string]int{}
	for gen := 0; gen < 200; gen++ {
		pw, ck := Password(key, "token-a", gen), Cookie(key, "token-a", gen)
		if prev, dup := seenPw[pw]; dup {
			t.Fatalf("generations %d and %d have the password %q", prev, gen, pw)
		}
		if prev, dup := seenCk[ck]; dup {
			t.Fatalf("generations %d and %d have the cookie %q", prev, gen, ck)
		}
		seenPw[pw], seenCk[ck] = gen, gen
		if !Check(key, "token-a", gen, pw) || Check(key, "token-a", gen+1, pw) {
			t.Fatalf("generation %d: the password is checked against the wrong generation", gen)
		}
		if !CheckCookie(key, "token-a", gen, ck) || CheckCookie(key, "token-a", gen+1, ck) {
			t.Fatalf("generation %d: the cookie is checked against the wrong generation", gen)
		}
	}
	// pinned, worked out apart from this code
	if got := Password(key, "token-a", 1); got != "7avc-5b5j" {
		t.Errorf("generation 1: %q", got)
	}
	if got := Password(key, "token-a", 2); got != "qct2-avbv" {
		t.Errorf("generation 2: %q", got)
	}
	if got := Cookie(key, "token-a", 1); got != "1.AOc91KKnOa6hamPPSnJS2dAbVA0UWSq-WQn35cW4rs4" {
		t.Errorf("cookie of generation 1: %q", got)
	}
}

func TestCookieGenNamesTheClaimedGenerationOnlyForAGenuineCookie(t *testing.T) {
	for _, gen := range []int{0, 1, 2, 17, MaxGen} {
		if got, ok := CookieGen(key, "token-a", Cookie(key, "token-a", gen)); !ok || got != gen {
			t.Errorf("generation %d: got %d, %v", gen, got, ok)
		}
	}
	good := Cookie(key, "token-a", 3)
	for _, bad := range []string{
		"", ".", "3.", ".x", "x", good + "x", "4." + strings.TrimPrefix(good, "3."), "03." + strings.TrimPrefix(good, "3."), "-3." + strings.TrimPrefix(good, "3."),
		"0." + strings.TrimPrefix(good, "3."), "1000001." + strings.TrimPrefix(good, "3."), "99999999999999999999." + strings.TrimPrefix(good, "3."),
		Cookie(key, "token-b", 3), Cookie(bytes.Repeat([]byte{8}, 32), "token-a", 3),
	} {
		if gen, ok := CookieGen(key, "token-a", bad); ok {
			t.Errorf("%q was taken for a cookie of generation %d", bad, gen)
		}
	}
	// a cookie of an earlier generation is genuine for that generation: the caller judges it against the current one
	if gen, ok := CookieGen(key, "token-a", Cookie(key, "token-a", 1)); !ok || gen != 1 {
		t.Errorf("an earlier cookie: %d, %v", gen, ok)
	}
}
