// Package pagepass is the password of a user's page and the cookie that remembers it. Both are computed, never
// stored: HMAC-SHA256 under a key the panel derives from its master key (vault.Derive(KeyLabel)), over the user's
// current subscription token. A new link (a rotated token) is therefore a new password and the old cookies stop
// working; nothing is added to the database.
//
// The password only keeps the page and its self-service calls from whoever happens to hold the link; the
// subscription itself, which apps fetch from the same address, never asks for it.
package pagepass

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// KeyLabel is the vault.Derive label of the key every function here takes.
const KeyLabel = "sub-page-password"

// alphabet has 32 characters (a byte masked with 31 picks one without bias): lower-case letters and digits
// without the look-alikes 0, 1, l and o, so a password can be read off a phone and typed in once.
const alphabet = "23456789abcdefghijkmnpqrstuvwxyz"

const length = 8

func mac(key []byte, purpose, token string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(token))
	return m.Sum(nil)
}

// Password is the page password of a token as it is shown and typed: "xxxx-xxxx".
func Password(key []byte, token string) string {
	sum := mac(key, "password", token)
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

// Check reports whether typed is the password of the token (constant time).
func Check(key []byte, token, typed string) bool {
	want := normalize(Password(key, token))
	return hmac.Equal([]byte(want), []byte(normalize(typed)))
}

// Cookie is the value of the cookie that remembers an entered password: an HMAC over the token, so the server keeps
// no state, and under another purpose than the password, so it does not reveal it.
func Cookie(key []byte, token string) string {
	return base64.RawURLEncoding.EncodeToString(mac(key, "cookie", token))
}

// CheckCookie reports whether a cookie value belongs to the token (constant time).
func CheckCookie(key []byte, token, value string) bool {
	return hmac.Equal([]byte(Cookie(key, token)), []byte(value))
}
