//go:build !js

package assistant

import "testing"

func TestCompletePlanTextAllowsJoinersAndRefusesOtherFormatCharacters(t *testing.T) {
	for name, text := range map[string]string{
		"Persian ZWNJ": "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645",
		"emoji ZWJ":    "check \U0001f469\u200d\U0001f4bb daily",
	} {
		got, err := completePlanText("fact value", text, 1000)
		if err != nil || got != text {
			t.Errorf("%s was not preserved: %q, %v", name, got, err)
		}
	}
	for name, text := range map[string]string{
		"bidi control": "abc\u202edef",
		"soft hyphen":  "co\u00adoperate",
	} {
		if _, err := completePlanText("fact value", text, 1000); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
