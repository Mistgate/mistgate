package vless

import "testing"

// A generated short id is never one a YAML reader could take for a number (see numberLike).
func TestGeneratedShortIDsAreNeverNumberLike(t *testing.T) {
	for range 20000 {
		ids, err := generatedShortIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if numberLike(id) {
				t.Fatalf("generated a number-like short id %q", id)
			}
		}
	}
	for _, id := range []string{"12345678", "123e4567", "00000000"} {
		if !numberLike(id) {
			t.Errorf("%q should be number-like", id)
		}
	}
	for _, id := range []string{"1234567a", "e2e4b678", "deadbeef"} {
		if numberLike(id) {
			t.Errorf("%q should not be number-like", id)
		}
	}
}
