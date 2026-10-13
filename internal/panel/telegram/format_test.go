package telegram

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// The Chinese text must be a real translation: it exists, is not the English one, and carries Chinese characters.
func TestChineseTelegramMessagesAreTranslated(t *testing.T) {
	if got := L("zh").pick("Node is unreachable", "Нода недоступна", ""); got != "Node is unreachable" {
		t.Errorf("Chinese fallback = %q, want the English text when the Chinese one is empty", got)
	}
	if got := L("zh").pick("English", "Russian", "中文"); got != "中文" {
		t.Errorf("Chinese text = %q, want the Chinese variant", got)
	}
	for _, kind := range []string{"node_down", "no_traffic", "check_failed", "state_drift", "cert_expiry", "update_failed", "quota",
		"subscription_shared_suspect", "access_ended", "user_connection", "users_impacted", "torrent", "port_lossy"} {
		a := store.HealthAlert{Kind: kind, Params: map[string]string{"profile": "HY2"}}
		zh, en := alertTitle("zh", a), alertTitle("en", a)
		if zh == en || !hasHan(zh) {
			t.Errorf("alertTitle(%s): zh %q is not a translation of %q", kind, zh, en)
		}
	}
	if got := alertTitle("zh", store.HealthAlert{Kind: "node_down"}); got != "节点无法连接" {
		t.Errorf("Chinese node_down title = %q, want the admin wording", got)
	}
	if got := alertTitle("zh", store.HealthAlert{Kind: "check_failed", Params: map[string]string{"profile": "HY2"}}); got != "「HY2」未通过客户端视角检测" {
		t.Errorf("Chinese check_failed title = %q", got)
	}
}

// The Chinese sentences join with their own stop, separators and no space after the stop; the values stay in place.
func TestChineseTorrentAndDurationText(t *testing.T) {
	p := map[string]string{"user_name": "Alice", "nodes": "nl", "count": "3", "evidence": "dht_query", "ports": "443"}
	want := "Alice 正在 nl 上尝试使用 BT 下载：一天内 3 次尝试，均已被拦截。判定依据：DHT 查询，端口 443。"
	if got := torrentReason("zh", p); got != want {
		t.Errorf("torrentReason(zh) = %q, want %q", got, want)
	}
	for d, want := range map[time.Duration]string{
		30 * time.Second:  "不到 1 分钟",
		14 * time.Minute:  "14 分钟",
		125 * time.Minute: "2 小时 5 分钟",
		27 * time.Hour:    "1 天 3 小时",
		48 * time.Hour:    "2 天",
	} {
		if got := L("zh").duration(d); got != want {
			t.Errorf("duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestChineseSecurityMessages(t *testing.T) {
	until := time.Date(2026, 10, 7, 18, 21, 0, 0, time.UTC)
	got := lockoutText("zh", "root", "203.0.113.5", until)
	for _, want := range []string{"<b>多次登录失败，登录已锁定</b>", "用户名：<b>root</b>", "来源地址：203.0.113.5", "锁定至 18:21 UTC。"} {
		if !strings.Contains(got, want) {
			t.Errorf("lockoutText(zh) = %q, lacks %q", got, want)
		}
	}
	if got := signInText("zh", "Alice", "passkey", ""); !strings.Contains(got, "<b>Alice</b> 已登录（通行密钥）。") {
		t.Errorf("signInText(zh) = %q", got)
	}
	if got := methodChangedText("zh", "Alice", auth.EventPasskeyAdded, ""); !strings.Contains(got, "<b>Alice</b>：已添加通行密钥。") {
		t.Errorf("methodChangedText(zh) = %q", got)
	}
}

func TestTelegramMessageTablesHaveAllLanguageTexts(t *testing.T) {
	tables := []struct {
		name   string
		values map[string]localizedText
	}{
		{"checkTitles", checkTitles},
		{"whyVariants", whyVariants},
		{"torrentEvidence", torrentEvidence},
		{"backupReasons", backupReasons},
		{"toolTitles", toolTitles},
	}
	for _, table := range tables {
		for key, text := range table.values {
			if text.en == "" || text.ru == "" || text.zh == "" {
				t.Errorf("%s[%q] is missing a language text: %+v", table.name, key, text)
			}
			// Names that are the same in every language (IPv6, CPU softirq) are the only ones allowed to equal the English text.
			if text.zh == text.en && hasHan(text.zh) || text.zh != text.en && !hasHan(text.zh) {
				t.Errorf("%s[%q] zh text %q is not a translation of %q", table.name, key, text.zh, text.en)
			}
			if text.zh == text.en && text.en != "IPv6" && text.en != "CPU softirq" {
				t.Errorf("%s[%q] zh text is still the English placeholder %q", table.name, key, text.en)
			}
		}
	}
	if got := (localizedText{en: "English", ru: "Russian"}).forLanguage("zh"); got != "English" {
		t.Errorf("missing Chinese text fallback = %q, want English", got)
	}
}

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
		{"zh", "再次出现", "持续了 2 分钟", "再次出现，首次于 18:21 UTC", "再次出现，首次于 07.10 18:21 UTC"},
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
		{lang: "zh", want: "使用 WARP 出口的协议配置（「HY2 · WARP」）无法启动：节点没有 WARP 账户"},
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

func TestPortLossyAlertTextsAreLocalized(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	a := store.HealthAlert{
		Kind: "port_lossy", Severity: 2, NodeID: "node-a", Subject: "inb_1", WhyKey: "health.alert.port_lossy.why",
		Params:    map[string]string{"profile": "profile-a", "port": "8443", "sent": "300", "got": "286"},
		FirstSeen: now, OpenedAt: now, ResolvedAt: now.Add(5 * time.Minute),
	}
	for _, tc := range []struct {
		lang  L
		parts []string
	}{
		{"en", []string{"UDP packets are being lost", "Node: <b>node-a</b>", "Profile “profile-a” on port 8443 lost 5% of UDP packets in the latest check."}},
		{"ru", []string{"Теряются UDP-пакеты", "Нода: <b>node-a</b>", "Профиль «profile-a» на порту 8443 потерял 5% UDP-пакетов при последней проверке."}},
	} {
		for _, message := range []string{alertOpened(tc.lang, a, "node-a", ""), alertResolved(tc.lang, a, "node-a")} {
			for _, part := range tc.parts {
				if !strings.Contains(message, part) {
					t.Errorf("%s message %q lacks %q", tc.lang, message, part)
				}
			}
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
