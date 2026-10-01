package buildinfo

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
)

func TestBuiltUnix(t *testing.T) {
	defer func(old string) { Built = old }(Built)
	for in, want := range map[string]int64{"": 0, "abc": 0, "-5": 0, "1790000000": 1790000000} {
		Built = in
		if got := BuiltUnix(); got != want {
			t.Errorf("Built %q: %d, want %d", in, got, want)
		}
	}
}

func TestReleasePublicKey(t *testing.T) {
	defer func(old string) { ReleaseKey = old }(ReleaseKey)
	ReleaseKey = ""
	if _, err := ReleasePublicKey(); !errors.Is(err, ErrUnsignedBuild) {
		t.Fatalf("no key: %v", err)
	}
	ReleaseKey = "not base64!"
	if _, err := ReleasePublicKey(); err == nil || errors.Is(err, ErrUnsignedBuild) {
		t.Fatalf("a broken key must not look unsigned: %v", err)
	}
	pub := make([]byte, ed25519.PublicKeySize)
	pub[0] = 7
	ReleaseKey = base64.StdEncoding.EncodeToString(pub)
	got, err := ReleasePublicKey()
	if err != nil || got[0] != 7 || len(KeyFingerprint(got)) != 16 {
		t.Fatalf("got %v %v", got, err)
	}
}
