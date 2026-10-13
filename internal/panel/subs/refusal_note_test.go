package subs

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/access"
)

// The words of a refusal reach the apps as the announcement (200 characters at most) and as the name of the one entry:
// each in the three languages, with the numbers of the page, and the way out for the app without a device ID.
func TestRefusalNotes(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, lang := range []string{"en", "ru", "zh"} {
		seen := map[string]bool{}
		for _, refusal := range []string{access.RefusalDeviceLimit, access.RefusalDeviceBlocked, access.RefusalNoDeviceID} {
			v := access.SubView{Status: access.StatusActive, Refusal: refusal, DeviceLimit: 5, DevicesUsed: 5}
			note := stateNote(v, lang, now)
			if note == "" || seen[note] || utf8.RuneCountInString(note) > announceMax || strings.ContainsAny(note, "\r\n") {
				t.Errorf("%s %s: note %q (%d characters)", lang, refusal, note, utf8.RuneCountInString(note))
			}
			seen[note] = true
			if cutAnnounce(note, announceMax) != note {
				t.Errorf("%s %s: the announcement would be cut: %q", lang, refusal, note)
			}
			switch refusal {
			case access.RefusalDeviceLimit:
				if !strings.Contains(note, "5") {
					t.Errorf("%s: the limit note %q has no numbers", lang, note)
				}
			case access.RefusalNoDeviceID:
				if !strings.Contains(note, "kl!ck") || !strings.Contains(note, "HWID") {
					t.Errorf("%s: the note %q names no way out", lang, note)
				}
			}
		}
	}
	// zh follows the glossary of the user page: full-width marks, no spaces around 你, nothing in Latin punctuation but numbers.
	for _, refusal := range []string{access.RefusalDeviceLimit, access.RefusalDeviceBlocked, access.RefusalNoDeviceID} {
		note := stateNote(access.SubView{Status: access.StatusActive, Refusal: refusal, DeviceLimit: 5, DevicesUsed: 5}, "zh", now)
		if strings.ContainsAny(note, ",;:()") || strings.Contains(note, ". ") {
			t.Errorf("zh %s: Latin punctuation in %q", refusal, note)
		}
	}
	// A person without access gets the reason of the status, whatever else is set.
	if note := stateNote(access.SubView{Status: access.StatusDisabled}, "en", now); note != "Access paused" {
		t.Errorf("disabled = %q", note)
	}
}
