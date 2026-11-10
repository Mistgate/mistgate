//go:build !js

package assistant

import (
	"strings"
	"testing"
)

func TestRedactUserMessage(t *testing.T) {
	telegram := "123456:" + strings.Repeat("Ab3_-", 6)
	privateKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-test-data\n-----END OPENSSH PRIVATE KEY-----"
	key43 := "kJ8f2Lw0Zr5uPq1vXy3cNb7mHg4tDa6sEo9iUl2WxYk"
	hexKey := strings.Repeat("0123456789abcdef", 2)
	for _, tc := range []struct {
		name string
		text string
		gone []string
		keep []string
	}{
		{name: "password label", text: "SSH password: test-only-password", gone: []string{"test-only-password"}, keep: []string{"SSH password:", "[removed: looks like a password]"}},
		{name: "Russian token label", text: "Токен = test-only-token", gone: []string{"test-only-token"}, keep: []string{"Токен =", "[removed: looks like a password]"}},
		{name: "license label", text: "WARP license_key: LIC-SECRET-import", gone: []string{"LIC-SECRET-import"}, keep: []string{"license_key:", "[removed: looks like a password]"}},
		{name: "private key block", text: privateKey, gone: []string{"private-key-test-data", "BEGIN OPENSSH"}, keep: []string{"[private key]"}},
		{name: "Telegram token", text: "bot " + telegram, gone: []string{telegram}, keep: []string{"bot ", "[token]"}},
		{name: "base64 key", text: "key " + key43 + " end", gone: []string{key43}, keep: []string{"key ", " end"}},
		{name: "hex key", text: "key " + hexKey + " end", gone: []string{hexKey}, keep: []string{"key ", " end"}},
	} {
		got := redactUserMessage(tc.text)
		for _, value := range tc.gone {
			if strings.Contains(got, value) {
				t.Errorf("%s: secret %q survived in %q", tc.name, value, got)
			}
		}
		for _, value := range tc.keep {
			if !strings.Contains(got, value) {
				t.Errorf("%s: %q was lost from %q", tc.name, value, got)
			}
		}
	}

	ordinary := "The word password is harmless; https://example.com/550e8400-e29b-41d4-a716-446655440000 is for node de1."
	wantOrdinary := strings.Replace(ordinary, "550e8400-e29b-41d4-a716-446655440000", "[uuid]", 1)
	if got := redactUserMessage(ordinary); got != wantOrdinary {
		t.Errorf("ordinary text was changed: %q", got)
	}
}
