// Package pagepass is the password of a user's page and the cookie that remembers it. Both are computed, never
// stored: HMAC-SHA256 under a key the panel derives from its master key (vault.Derive(KeyLabel)), over the user's
// current subscription token and a small number, the generation of the password. The database holds that number (and
// whether the password was removed) and nothing else: no password, no hash of one.
//
// Generation 0 is the password every person has had since the page was protected; "change" adds one, so the password is
// new, the link stays and the cookies of the old one stop working; a new link (a rotated token) is a new password too.
//
// The password only keeps the page and its self-service calls from whoever happens to hold the link; the
// subscription itself, which apps fetch from the same address, never asks for it.
package pagepass

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
)

// KeyLabel is the vault.Derive label of the key every function here takes.
const KeyLabel = "sub-page-password"

// alphabet has 32 characters (a byte masked with 31 picks one without bias): lower-case letters and digits
// without the look-alikes 0, 1, l and o, so a password can be read off a phone and typed in once.
const alphabet = "23456789abcdefghijkmnpqrstuvwxyz"

const length = 8

// MaxGen is the largest generation a cookie may claim; far beyond what changes by hand reach.
const MaxGen = 1_000_000

// mac is HMAC(key, purpose 0 token). Generation 0 uses the purpose words alone, as before generations existed (every password
// and cookie of an existing person stays what it was); a later generation appends "/<gen>".
func mac(key []byte, purpose string, gen int, token string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	if gen > 0 {
		m.Write([]byte{'/'})
		m.Write([]byte(strconv.Itoa(gen)))
	}
	m.Write([]byte{0})
	m.Write([]byte(token))
	return m.Sum(nil)
}

// Password is the page password of a token in a generation as it is shown and typed: "xxxx-xxxx".
func Password(key []byte, token string, gen int) string {
	sum := mac(key, "password", gen, token)
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

// normalize drops what a person adds when typing (blanks, dashes) and the capital letters a phone keyboard
// starts with.
func normalize(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer(" ", "", "-", "", "‐", "", "‑", "", "–", "", "—", "").Replace(s)
}

// Check reports whether typed is the password of the token in the generation (constant time).
func Check(key []byte, token string, gen int, typed string) bool {
	want := normalize(Password(key, token, gen))
	return hmac.Equal([]byte(want), []byte(normalize(typed)))
}

// Cookie is the value of the cookie that remembers an entered password: an HMAC over the token and the generation, so the
// server keeps no state, and under another purpose than the password, so it does not reveal it. Generation 0 is the bare
// HMAC (what browsers already hold); a later one is "<gen>.<HMAC>", so the server can tell which generation a cookie claims
// before it knows the current one.
func Cookie(key []byte, token string, gen int) string {
	v := base64.RawURLEncoding.EncodeToString(mac(key, "cookie", gen, token))
	if gen > 0 {
		return strconv.Itoa(gen) + "." + v
	}
	return v
}

// CheckCookie reports whether a cookie value is the cookie of the token in this generation (constant time).
func CheckCookie(key []byte, token string, gen int, value string) bool {
	return hmac.Equal([]byte(Cookie(key, token, gen)), []byte(value))
}

// CookieGen is the generation a cookie value claims and whether it is genuine for that generation (its HMAC is right for the
// token). It does not know the current generation: the caller compares the two. It is how a call that has not read the person's
// row yet tells a forged cookie (refused at once) from one that was once earned (judged when the row is read).
func CookieGen(key []byte, token, value string) (gen int, ok bool) {
	if head, _, found := strings.Cut(value, "."); found {
		n, err := strconv.Atoi(head)
		if err != nil || n < 1 || n > MaxGen || strconv.Itoa(n) != head {
			return 0, false
		}
		gen = n
	}
	return gen, CheckCookie(key, token, gen, value)
}
