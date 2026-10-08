package telegram

import (
	"strconv"
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

func TestProbePathAlertIsLocalized(t *testing.T) {
	a := store.HealthAlert{
		Kind: "check_failed", Subject: "probe_path", WhyKey: "health.alert.check_failed.why.probe_path",
		Params: map[string]string{"nodes": "alpha, beta", "providers": "2", "failed": "2", "total": "2"},
	}
	for _, tc := range []struct {
		lang   L
		title  string
		reason string
	}{
		{
			lang:   "en",
			title:  "Panel probes fail across several nodes",
			reason: "Probes from the panel failed on several nodes at once (alpha, beta), at 2 different hosters. Most likely the panel's own network, not the nodes. No per-node alerts and no port advice unless it lasts 20 minutes.",
		},
		{
			lang:   "ru",
			title:  "Проверки панели не проходят на нескольких нодах",
			reason: "Проверки с панели провалились сразу на нескольких нодах (alpha, beta) у 2 разных хостеров. Скорее всего, сбой в сети самой панели, а не на нодах. Отдельных тревог по нодам и советов сменить порт не будет, если сбой не продлится 20 минут.",
		},
	} {
		if got := alertTitle(tc.lang, a); got != tc.title {
			t.Errorf("%s title = %q, want %q", tc.lang, got, tc.title)
		}
		if got := alertReason(tc.lang, a); got != tc.reason {
			t.Errorf("%s reason = %q, want %q", tc.lang, got, tc.reason)
		}
	}
}

func TestUserHealthAlertsAreLocalized(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	accessEnded := store.HealthAlert{
		Kind: "access_ended", Severity: 2, WhyKey: "health.alert.access_ended.why.expired",
		Params:    map[string]string{"user_name": "Alice <VIP>", "since": strconv.FormatInt(now.Add(-72*time.Hour).Unix(), 10)},
		FirstSeen: now, OpenedAt: now,
	}
	impacted := store.HealthAlert{
		Kind: "users_impacted", Severity: 2, NodeID: "nod_de", Subject: "hysteria2", WhyKey: "health.alert.users_impacted.why.gone",
		Params: map[string]string{"now": "0", "usual": "5", "users": "5"}, FirstSeen: now, OpenedAt: now,
	}

	for _, tc := range []struct {
		lang  L
		alert store.HealthAlert
		want  []string
	}{
		{lang: "en", alert: accessEnded, want: []string{"Alice &lt;VIP&gt;", "subscription ended 3 d ago"}},
		{lang: "ru", alert: func() store.HealthAlert { a := accessEnded; a.WhyKey = "health.alert.access_ended.why.quota"; return a }(), want: []string{"Alice &lt;VIP&gt;", "квота закончилась 3 д назад"}},
		{lang: "en", alert: impacted, want: []string{"People on Hysteria2", "now 0", "usually about 5", "users' side"}},
		{lang: "ru", alert: impacted, want: []string{"Людей на Hysteria2", "сейчас 0", "обычно около 5", "на стороне пользователей"}},
	} {
		node := ""
		if tc.alert.Kind == "users_impacted" {
			node = "de1"
		}
		got := alertOpened(tc.lang, tc.alert, node, "")
		if tc.alert.Kind == "users_impacted" {
			wantReason := "People on Hysteria2: now 0, usually about 5 at this hour, while the node is online. This looks like a block on the users' side."
			if tc.lang == "ru" {
				wantReason = "Людей на Hysteria2: сейчас 0, обычно около 5 в этот час, а нода на связи. Похоже на блокировку на стороне пользователей."
			}
			if reason := alertReason(tc.lang, tc.alert); reason != wantReason {
				t.Errorf("%s users_impacted reason = %q, want %q", tc.lang, reason, wantReason)
			}
		}
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s %s message %q lacks %q", tc.lang, tc.alert.Kind, got, want)
			}
		}
	}
}

func TestTorrentAlertIsLocalizedWithEvidenceAndPorts(t *testing.T) {
	alert := store.HealthAlert{
		Kind: "torrent", Severity: 2, Subject: "usr_a", WhyKey: "health.alert.torrent.why", FirstSeen: time.Now(), OpenedAt: time.Now(),
		Params: map[string]string{"user_id": "usr_a", "user_name": "alice <x>", "nodes": "EE, DE", "count": "7",
			"evidence": "tracker_connect", "ports": "6881, 6969"},
	}
	for _, tc := range []struct {
		lang   L
		mutate func(map[string]string)
		want   string
	}{
		{"en", nil, "alice &lt;x&gt; is trying to use torrents on EE, DE: 7 attempts in a day, blocked. Evidence: tracker connect request (the protocol’s magic number), ports 6881, 6969."},
		{"ru", nil, "alice &lt;x&gt; пытается качать торренты на EE, DE: 7 попыток за сутки, торренты заблокированы. Доказательство: запрос подключения к трекеру (магическое число протокола), порты 6881, 6969."},
		{"ru", func(p map[string]string) { p["count"], p["ports"], p["evidence"] = "1", "6969", "dht_query" },
			"1 попытка за сутки, торренты заблокированы. Доказательство: запрос DHT, порт 6969."},
		{"ru", func(p map[string]string) { p["count"], p["evidence"] = "22", "utp_syn" }, "22 попытки за сутки"},
		{"en", func(p map[string]string) { p["count"], p["ports"], p["evidence"] = "1", "80", "tcp_handshake" },
			"1 attempt in a day, blocked. Evidence: BitTorrent handshake over TCP, port 80."},
		// an agent that predates the evidence says nothing of it
		{"en", func(p map[string]string) { delete(p, "evidence"); delete(p, "ports") }, "7 attempts in a day, blocked."},
		{"ru", func(p map[string]string) { delete(p, "evidence") }, "заблокированы. Порты 6881, 6969."},
		// an unknown code from a newer agent is shown as data, not dropped
		{"en", func(p map[string]string) { p["evidence"] = "new_thing<" }, "Evidence: new_thing&lt;, ports"},
	} {
		a := alert
		a.Params = map[string]string{}
		for k, v := range alert.Params {
			a.Params[k] = v
		}
		if tc.mutate != nil {
			tc.mutate(a.Params)
		}
		got := alertOpened(tc.lang, a, "", "")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s message %q lacks %q", tc.lang, got, tc.want)
		}
		if strings.Contains(got, "198.51.100") {
			t.Errorf("message has an address: %q", got)
		}
	}
	if got := alertTitle("ru", alert); got != "Попытки качать торренты" {
		t.Errorf("ru title = %q", got)
	}
	if got := alertTitle("en", alert); got != "Torrent attempts" {
		t.Errorf("en title = %q", got)
	}
}
