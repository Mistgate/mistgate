package subs

import (
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
)

// Persian dates are in the Persian calendar the page's Intl writes (ICU, fa-u-nu-latn): the same day on the page and in the app.
// The dates are ICU's, Nowruz of the leap years included.
func TestPersianDatesAreThePagesOnes(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		g    time.Time
		want [3]int
	}{
		{day(2026, 12, 1), [3]int{1405, 9, 10}},
		{day(2027, 2, 10), [3]int{1405, 11, 21}},
		{day(2027, 3, 20), [3]int{1405, 12, 29}},
		{day(2027, 3, 21), [3]int{1406, 1, 1}},
		{day(2026, 1, 15), [3]int{1404, 10, 25}},
		{day(2024, 3, 19), [3]int{1402, 12, 29}},
		{day(2024, 3, 20), [3]int{1403, 1, 1}},
		{day(2025, 3, 20), [3]int{1403, 12, 30}}, // 1403 is a leap year
		{day(2025, 3, 21), [3]int{1404, 1, 1}},
		{day(2028, 3, 20), [3]int{1407, 1, 1}},
		{day(2029, 3, 20), [3]int{1408, 1, 1}},
		{day(2026, 9, 22), [3]int{1405, 6, 31}},
		{day(2026, 9, 23), [3]int{1405, 7, 1}},
	} {
		if y, m, d := persianDate(c.g); [3]int{y, m, d} != c.want {
			t.Errorf("persianDate(%s) = %d-%d-%d, want %v", c.g.Format(time.DateOnly), y, m, d, c.want)
		}
	}
	now := day(2026, 12, 1)
	for _, c := range []struct {
		date time.Time
		lang string
		want string
	}{
		{day(2026, 12, 1), "fa", "10 آذر"},
		{day(2027, 2, 10), "fa", "21 بهمن"},        // another Gregorian year, the same Persian one
		{day(2026, 1, 15), "fa", "25 دی 1404"},     // the same Gregorian year, the Persian one before
		{day(2027, 3, 25), "fa", "5 فروردین 1406"}, // the next one
		{day(2026, 12, 1), "tr", "1 Aralık"},
		{day(2027, 8, 30), "tr", "30 Ağustos 2027"},
	} {
		if got := dayText(c.date, now, c.lang); got != c.want {
			t.Errorf("dayText(%s, %s) = %q, want %q", c.date.Format(time.DateOnly), c.lang, got, c.want)
		}
	}
}

// What the apps show a person whose subscription does not work, in Persian and Turkish: translations that carry the date the
// page writes, the app names in Latin, nothing left of the format.
func TestPersianAndTurkishStateNotes(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sep29, oct10 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		view access.SubView
		fa   string
		tr   string
	}{
		{access.SubView{Status: access.StatusExpired, Expires: sep29}, "اشتراک در 7 مهر تمام شد. پیام بدهید تا تمدیدش کنیم", "Abonelik 29 Eylül tarihinde sona erdi. Uzatmak için bize yazın"},
		{access.SubView{Status: access.StatusLimited, QuotaReset: access.ResetMonth, NextReset: oct10}, "حجم تمام شده، 18 مهر دوباره شارژ می‌شود", "Trafik bitti, 10 Ekim tarihinde sıfırlanır"},
		{access.SubView{Status: access.StatusDisabled}, "دسترسی موقتاً قطع شده", "Erişim durduruldu"},
	} {
		if got := stateNote(c.view, "fa", now); got != c.fa {
			t.Errorf("fa = %q, want %q", got, c.fa)
		}
		if got := stateNote(c.view, "tr", now); got != c.tr {
			t.Errorf("tr = %q, want %q", got, c.tr)
		}
	}
	mihomo := access.SubView{Status: access.StatusActive, Nodes: []access.SubNode{{Conns: []access.SubConn{{MihomoOnly: true}}}}}
	for _, lang := range []string{"fa", "tr"} {
		got, en := mihomoOnlyNote(mihomo, lang), mihomoOnlyNote(mihomo, "en")
		if got == en || !strings.Contains(got, "kl!ck") || !strings.Contains(got, "Mihomo") {
			t.Errorf("%s: mihomo note %q", lang, got)
		}
		if n := noServersNote(lang); n == noServersNote("en") || len([]rune(n)) > announceMax {
			t.Errorf("%s: no-servers note %q", lang, n)
		}
	}
	if got := access.CountryName("DE", "fa"); got != "آلمان" {
		t.Errorf("CountryName(DE, fa) = %q", got)
	}
	if got := access.CountryName("DE", "tr"); got != "Almanya" {
		t.Errorf("CountryName(DE, tr) = %q", got)
	}
}
