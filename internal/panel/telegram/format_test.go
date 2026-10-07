package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// A re-opened alert keeps its row: first_seen is the first episode, opened_at the current one. The resolve message counts
// its duration from opened_at, and both messages say "again" with the time of the first episode; a plain alert says neither.
func TestReopenedAlertCountsFromItsEpisodeAndSaysAgain(t *testing.T) {
	first := time.Date(2026, 10, 7, 18, 21, 0, 0, time.UTC)
	reopened := store.HealthAlert{Kind: "check_failed", Severity: 3, TitleKey: "t", FirstSeen: first, OpenedAt: first.Add(60 * time.Minute)}
	reopened.ResolvedAt = reopened.OpenedAt.Add(2 * time.Minute)
	plain := reopened
	plain.OpenedAt = plain.FirstSeen
	plain.ResolvedAt = plain.OpenedAt.Add(2 * time.Minute)
	nextDay := reopened
	nextDay.OpenedAt = first.Add(30 * time.Hour)

	for _, tc := range []struct {
		lang                        L
		word, duration, again, date string
	}{
		{"en", "Again", "lasted 2 min", "Again, first at 18:21 UTC", "Again, first at 07.10 18:21 UTC"},
		{"ru", "Снова", "длилось 2 мин", "Снова, впервые в 18:21 UTC", "Снова, впервые в 07.10 18:21 UTC"},
	} {
		got := alertResolved(tc.lang, reopened, "nl")
		if !strings.Contains(got, tc.duration) || strings.Contains(got, "1 h") || strings.Contains(got, "1 ч") || !strings.Contains(got, tc.again) {
			t.Errorf("%s resolve of a re-open: %q, want %q and %q", tc.lang, got, tc.duration, tc.again)
		}
		if got := alertOpened(tc.lang, reopened, "nl", ""); !strings.Contains(got, tc.again) {
			t.Errorf("%s open of a re-open: %q lacks %q", tc.lang, got, tc.again)
		}
		if got := alertOpened(tc.lang, nextDay, "nl", ""); !strings.Contains(got, tc.date) {
			t.Errorf("%s open of a re-open on another day: %q lacks %q", tc.lang, got, tc.date)
		}
		for name, got := range map[string]string{"resolve": alertResolved(tc.lang, plain, "nl"), "open": alertOpened(tc.lang, plain, "nl", "")} {
			if strings.Contains(got, tc.word) {
				t.Errorf("%s %s of a first episode says again: %q", tc.lang, name, got)
			}
		}
		if got := alertResolved(tc.lang, plain, "nl"); !strings.Contains(got, tc.duration) {
			t.Errorf("%s resolve of a first episode: %q lacks %q", tc.lang, got, tc.duration)
		}
	}
}

func TestDoctorAlertLocalizesWARPWithoutInboundIDs(t *testing.T) {
	a := store.HealthAlert{
		Kind:     "doctor_fail",
		Severity: 3,
		Params: map[string]string{
			"check":       "warp_path",
			"detail_code": "warp_path.not_configured",
			"detail":      "inbound(s) with egress warp are not started, the node has no WARP account: inb_test",
			"inbounds":    "inb_test",
			"profiles":    "HY2 · WARP",
			"cred_id":     "cred_test",
		},
	}

	for _, tc := range []struct {
		lang L
		want string
	}{
		{lang: "en", want: "Profiles with WARP egress (“HY2 · WARP”) cannot start: the node has no WARP account"},
		{lang: "ru", want: "Профили с выходом через WARP («HY2 · WARP») не стартуют: у ноды нет аккаунта WARP"},
	} {
		got := alertOpened(tc.lang, a, "nl", "")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s message %q lacks %q", tc.lang, got, tc.want)
		}
		for _, forbidden := range []string{"inb_test", "cred_test", "egress warp:", "not configured on this node"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s message contains %q: %q", tc.lang, forbidden, got)
			}
		}
	}
}

func TestUnknownDoctorDetailUsesLocalizedGenericLine(t *testing.T) {
	a := store.HealthAlert{
		Kind:     "doctor_fail",
		Severity: 3,
		Params: map[string]string{
			"check":       "warp_path",
			"detail_code": "warp_path.future_failure",
			"detail":      "new English detail containing inb_test",
			"inbound_id":  "inb_test",
		},
	}

	for _, tc := range []struct {
		lang L
		want string
	}{
		{lang: "en", want: "Node check: WARP is down"},
		{lang: "ru", want: "Проверка узла: WARP не работает"},
	} {
		got := alertOpened(tc.lang, a, "nl", "")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s message %q lacks generic line %q", tc.lang, got, tc.want)
		}
		for _, forbidden := range []string{"inb_test", "new English detail"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s message contains %q: %q", tc.lang, forbidden, got)
			}
		}
	}
}
