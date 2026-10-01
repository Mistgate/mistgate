package pagepass

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var key = bytes.Repeat([]byte{9}, 32)

func TestPasswordIsEightUnambiguousCharsAsTwoGroups(t *testing.T) {
	re := regexp.MustCompile(`^[2-9a-km-np-z]{4}-[2-9a-km-np-z]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		pw := Password(key, strings.Repeat("t", 40)+string(rune('A'+i%26))+string(rune('a'+i/26)))
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
	a := Password(key, "token-a")
	if a != Password(key, "token-a") {
		t.Error("not deterministic")
	}
	if a == Password(key, "token-b") {
		t.Error("two tokens, one password")
	}
	if a == Password(bytes.Repeat([]byte{8}, 32), "token-a") {
		t.Error("two keys, one password")
	}
}

func TestCheckForgivesWhatPeopleTypeAndNothingElse(t *testing.T) {
	pw := Password(key, "token-a")
	raw := strings.ReplaceAll(pw, "-", "")
	for _, ok := range []string{pw, strings.ToUpper(pw), raw, " " + raw[:4] + " " + raw[4:] + " ", raw[:4] + "‑" + raw[4:]} {
		if !Check(key, "token-a", ok) {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{"", raw[:7], raw + "a", "aaaa-bbbb", Password(key, "token-b"), strings.Repeat(raw, 20)} {
		if Check(key, "token-a", bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestCookieIsBoundToTheTokenAndRevealsNothing(t *testing.T) {
	c := Cookie(key, "token-a")
	if !CheckCookie(key, "token-a", c) {
		t.Fatal("own cookie refused")
	}
	if CheckCookie(key, "token-b", c) || CheckCookie(key, "token-a", "") || CheckCookie(key, "token-a", c+"x") || CheckCookie(bytes.Repeat([]byte{8}, 32), "token-a", c) {
		t.Error("a foreign or damaged cookie was accepted")
	}
	pw := strings.ReplaceAll(Password(key, "token-a"), "-", "")
	if strings.Contains(c, pw) || strings.Contains(c, "token-a") {
		t.Error("the cookie shows the password or the token")
	}
}
