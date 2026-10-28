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
	for _, kind := range []string{"node_down", "no_traffic", "check_failed", "relay_down", "state_drift", "cert_expiry", "update_failed", "quota",
		"subscription_shared_suspect", "access_ended", "user_connection", "users_impacted", "torrent", "port_lossy", "device_limit", "country_blocked"} {
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

func TestRelayDownTelegramTextIsLocalized(t *testing.T) {
	a := store.HealthAlert{Kind: "relay_down", Subject: "rly_test", WhyKey: "health.alert.relay_down.why.freeze",
		Params: map[string]string{"relay": "fi1", "exit": "de1", "exit_profile": "files", "port": "8443", "stage": "freeze"}}
	wantTitle := "Relay via “fi1” is down"
	wantReason := "The 64 KB transfer through the relay stalls Exit: de1 · files · port 8443."
	if got := alertTitle("en", a); got != wantTitle {
		t.Errorf("English title = %q, want %q", got, wantTitle)
	}
	if got := alertReason("en", a); got != wantReason {
		t.Errorf("English reason = %q, want %q", got, wantReason)
	}
	for _, lang := range []L{"ru", "zh"} {
		title, reason := alertTitle(lang, a), alertReason(lang, a)
		if title == wantTitle || reason == wantReason || title == "" || reason == "" {
			t.Errorf("%s relay text was not translated: %q / %q", lang, title, reason)
		}
		if lang == "zh" && (!hasHan(title) || !hasHan(reason)) {
			t.Errorf("Chinese relay text has no Han characters: %q / %q", title, reason)
		}
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

func TestFlappingNodeAlertIsLocalized(t *testing.T) {
	a := store.HealthAlert{Kind: "host_blip", Subject: "flapping", WhyKey: "health.alert.host_blip.why.flapping", Params: map[string]string{"count": "4"}}
	for lang, want := range map[L][2]string{
		"en": {"The node keeps dropping", "4 link drops or agent restarts in the last hour."},
		"ru": {"Нода то и дело пропадает", "За последний час обрывов связи и перезапусков агента: 4."},
		"zh": {"节点反复掉线", "最近一小时内连接中断或 Agent 重启 4 次。"},
	} {
		if got := alertTitle(lang, a); got != want[0] {
			t.Errorf("%s title = %q, want %q", lang, got, want[0])
		}
		if got := alertReason(lang, a); got != want[1] {
			t.Errorf("%s reason = %q, want %q", lang, got, want[1])
		}
	}
}

func TestPanelLinkAlertIsLocalized(t *testing.T) {
	a := store.HealthAlert{Kind: "node_down", Subject: "panel_link", WhyKey: "health.alert.node_down.why.panel_link", Params: map[string]string{"nodes": "4", "total": "5"}}
	for lang, want := range map[L][2]string{
		"en": {"The panel lost contact with most nodes", "4 of 5 nodes went silent at once, at several hosters: likely the panel’s own connection. Per-node alerts are held."},
		"ru": {"Панель потеряла связь с большинством нод", "4 из 5 нод замолчали одновременно у нескольких хостеров: скорее всего, виновата связь самой панели. Алерты по нодам на паузе."},
		"zh": {"面板与大多数节点失去联系", "5 个节点中有 4 个同时失去响应，涉及多个服务商：多半是面板自身的连接问题。按节点的告警已暂停。"},
	} {
		if got := alertTitle(lang, a); got != want[0] {
			t.Errorf("%s title = %q, want %q", lang, got, want[0])
		}
		if got := alertReason(lang, a); got != want[1] {
			t.Errorf("%s reason = %q, want %q", lang, got, want[1])
		}
	}
}

func TestNotStartedAlertIsLocalized(t *testing.T) {
	a := store.HealthAlert{Kind: "check_failed", WhyKey: "health.alert.check_failed.why.not_started",
		Params: map[string]string{"profile": "HY2", "port": "8443", "error_detail": "bind: address already in use"}}
	for lang, want := range map[L]string{
		"en": "The profile did not start on the node · port 8443",
		"ru": "Профиль не запустился на ноде · порт 8443",
		"zh": "协议配置未能在节点上启动 · 端口 8443",
	} {
		if got := alertReason(lang, a); got != want {
			t.Errorf("%s reason = %q, want %q", lang, got, want)
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

func TestDeviceLimitAlertIsLocalized(t *testing.T) {
	alert := store.HealthAlert{
		Kind: "device_limit", Severity: 2, Subject: "usr_a", WhyKey: "health.alert.device_limit.why", FirstSeen: time.Now(), OpenedAt: time.Now(),
		Params: map[string]string{"user_id": "usr_a", "user_name": "alice <x>", "used": "5", "limit": "5", "count": "3"},
	}
	for lang, want := range map[L][]string{
		"en": {"An app was turned away at the device limit", "alice &lt;x&gt; is at the device limit (5 of 5): an app asking for a new place was turned away."},
		"ru": {"Приложению отказали: лимит устройств", "У alice &lt;x&gt; заняты все места для устройств (5 из 5)"},
		"zh": {"有应用因设备数上限被拒绝", "alice &lt;x&gt; 的设备数已达上限（5 / 5）：有应用申请新名额，被拒绝了。"},
	} {
		got := alertOpened(lang, alert, "", "")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s message %q lacks %q", lang, got, w)
			}
		}
	}
}

func TestCountryBlockedAlertTextsAreLocalized(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a := store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "nl1", WhyKey: "health.alert.country_blocked.why.tcp_blocked",
		Params:    map[string]string{"node": "nl1", "country": "RU", "verdict": "tcp_blocked", "networks": "Rostelecom, Beeline <x>", "ports": "443"},
		FirstSeen: now, OpenedAt: now, ResolvedAt: now.Add(time.Hour)}
	for _, tc := range []struct {
		lang  L
		parts []string
	}{
		{"en", []string{"Node is unreachable from Russia", "Node: <b>nl1</b>", "TCP is being cut (Rostelecom, Beeline &lt;x&gt;). ICMP passes."}},
		{"ru", []string{"Нода недоступна из России", "Нода: <b>nl1</b>", "TCP режут (Rostelecom, Beeline &lt;x&gt;). ICMP проходит."}},
		{"zh", []string{"节点从俄罗斯无法连接", "TCP 被阻断（Rostelecom, Beeline &lt;x&gt;），ICMP 仍可通过。"}},
	} {
		opened := alertOpened(tc.lang, a, "nl1", "")
		for _, part := range tc.parts {
			if !strings.Contains(opened, part) {
				t.Errorf("%s message %q lacks %q", tc.lang, opened, part)
			}
		}
		if resolved := alertResolved(tc.lang, a, "nl1"); !strings.Contains(resolved, tc.parts[0]) {
			t.Errorf("%s resolved message %q lacks the title", tc.lang, resolved)
		}
	}
	a.Params["verdict"] = "blocked"
	if got := alertOpened("en", a, "nl1", ""); !strings.Contains(got, "Neither ICMP nor TCP gets through (Rostelecom") {
		t.Errorf("blocked text = %q", got)
	}
	a.Params["country"] = "DE"
	if got := alertTitle("ru", a); got != "Нода недоступна из страны: Германия" {
		t.Errorf("country title = %q", got)
	}
}

func TestPartialCountryBlockedAlertTextsAreLocalized(t *testing.T) {
	a := store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "de1", Subject: "partial",
		TitleKey: "health.alert.country_blocked.title", WhyKey: "health.alert.country_blocked.why.partial",
		Params: map[string]string{"node": "de1", "country": "RU", "verdict": "partial", "confidence": "eyeball",
			"sources": "globalping", "networks": "MTS (AS8359, Novosibirsk)"}}
	for _, tc := range []struct {
		lang   L
		title  string
		reason string
		seen   string
	}{
		{"en", "Node is unreachable from some networks in Russia",
			"Probes on MTS (AS8359, Novosibirsk) get nothing from it while they reach your other nodes.",
			"Seen by: Globalping."},
		{"ru", "Нода недоступна из части сетей России",
			"Зонды в сетях MTS (AS8359, Novosibirsk) не получают от неё ответов, хотя доходят до других ваших нод.",
			"Видели: Globalping."},
		{"zh", "节点在俄罗斯的部分网络中无法连接",
			"位于MTS (AS8359, Novosibirsk)的探测点收不到它的响应，但能到达您的其他节点。",
			"发现方：Globalping。"},
	} {
		if got := alertTitle(tc.lang, a); got != tc.title {
			t.Errorf("%s title = %q, want %q", tc.lang, got, tc.title)
		}
		reason := alertReason(tc.lang, a)
		if !strings.HasPrefix(reason, tc.reason) || !strings.Contains(reason, tc.seen) {
			t.Errorf("%s reason = %q, want prefix %q and source %q", tc.lang, reason, tc.reason, tc.seen)
		}
	}
	a.Params["country"] = "DE"
	if got, want := alertTitle("ru", a), "Нода недоступна из части сетей страны: Германия"; got != want {
		t.Errorf("Russian fallback title = %q, want %q", got, want)
	}
}

func TestPartialCountryBlockedAlertIncludesMixedSourceNetworks(t *testing.T) {
	a := store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "de1", Subject: "partial",
		TitleKey: "health.alert.country_blocked.title", WhyKey: "health.alert.country_blocked.why.partial",
		Params: map[string]string{"node": "de1", "country": "RU", "verdict": "partial", "confidence": "eyeball",
			"sources": "globalping,bsbord", "networks": "Rostelecom (AS12389, Moscow), MTS <x> (Crimea, bsbord)"}}
	for _, lang := range []L{"en", "ru", "zh"} {
		got := alertReason(lang, a)
		if !strings.Contains(got, "Rostelecom (AS12389, Moscow)") || !strings.Contains(got, "MTS &lt;x&gt; (Crimea, bsbord)") ||
			strings.Contains(got, "MTS <x>") || !strings.Contains(got, "bsbord") {
			t.Errorf("%s mixed-source partial reason = %q", lang, got)
		}
	}
	if got := alertReason("en", a); !strings.Contains(got, "Seen by: Globalping, bsbord.") {
		t.Errorf("English mixed-source labels = %q", got)
	}
}

// The alert says which sources saw the block: the probe node alone (a data-centre view of TCP only), Globalping with it, and a
// disagreement between the two.
func TestCountryBlockedAlertNamesTheSources(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	alert := func(params map[string]string) store.HealthAlert {
		return store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "nl1", WhyKey: "health.alert.country_blocked.why." + params["verdict"],
			Params: params, FirstSeen: now, OpenedAt: now}
	}
	tspuOnly := alert(map[string]string{"node": "nl1", "country": "RU", "verdict": "tcp_blocked", "confidence": "dc_only", "sources": "tspu", "networks": "", "ports": "443"})
	both := alert(map[string]string{"node": "nl1", "country": "RU", "verdict": "tcp_blocked", "confidence": "eyeball", "sources": "globalping,tspu", "networks": "Rostelecom", "ports": "443"})
	conflict := alert(map[string]string{"node": "nl1", "country": "RU", "verdict": "tcp_blocked", "confidence": "conflict", "sources": "globalping,tspu", "networks": "Rostelecom", "ports": "443"})
	for _, tc := range []struct {
		name  string
		a     store.HealthAlert
		lang  L
		parts []string
	}{
		{"probe node alone", tspuOnly, "en", []string{"TCP to port 443 is being cut, seen from your probe node in the country (a data-centre view).", "Seen by: the TSPU probe on your node."}},
		{"probe node alone", tspuOnly, "ru", []string{"TCP на порт 443 режут", "Видели: зонд ТСПУ на вашей ноде."}},
		{"probe node alone", tspuOnly, "zh", []string{"TCP（端口 443） 被阻断", "发现方：您节点上的 TSPU 探测。"}},
		{"both", both, "en", []string{"TCP is being cut (Rostelecom). ICMP passes.", "Seen by: Globalping, the TSPU probe on your node."}},
		{"both", both, "ru", []string{"TCP режут (Rostelecom). ICMP проходит.", "Видели: Globalping, зонд ТСПУ на вашей ноде."}},
		{"conflict", conflict, "en", []string{"The sources disagree; the worse result is shown."}},
		{"conflict", conflict, "ru", []string{"Источники расходятся; показан худший результат."}},
		{"conflict", conflict, "zh", []string{"各来源的结果不一致，此处显示较差的一项。"}},
	} {
		got := alertOpened(tc.lang, tc.a, "nl1", "")
		for _, part := range tc.parts {
			if !strings.Contains(got, part) {
				t.Errorf("%s (%s): %q lacks %q", tc.name, tc.lang, got, part)
			}
		}
	}
	if got := alertOpened("en", tspuOnly, "nl1", ""); strings.Contains(got, "ICMP") {
		t.Errorf("the probe node tests TCP only, but the text mentions ICMP: %q", got)
	}
}

// Only Globalping tells ICMP from TCP, and only a probe node alone is a data-centre view: sources that cannot say either keep
// the text to what they saw.
func TestCountryBlockedTCPTextClaimsOnlyWhatTheSourcesSaw(t *testing.T) {
	for _, tc := range []struct {
		sources, networks string
		icmp, dc          bool
	}{
		{"cheburcheck", "Rostelecom (AS12389, Moscow, cheburcheck)", false, false},
		{"tspu,cheburcheck", "probe1 (tspu), Rostelecom (AS12389, Moscow, cheburcheck)", false, false},
		{"bsbord", "MTS (Crimea, bsbord)", false, false},
		{"globalping,cheburcheck", "Rostelecom", true, false},
		{"", "Rostelecom", true, false}, // an alert from before the source list is a Globalping one
	} {
		a := store.HealthAlert{Kind: "country_blocked", Severity: 2, NodeID: "de1", TitleKey: "health.alert.country_blocked.title",
			WhyKey: "health.alert.country_blocked.why.tcp_blocked", Params: map[string]string{"node": "de1", "country": "RU",
				"verdict": "tcp_blocked", "confidence": "eyeball", "sources": tc.sources, "networks": tc.networks, "ports": "443"}}
		for _, lang := range []L{"en", "ru", "zh"} {
			got := alertReason(lang, a)
			if icmp := strings.Contains(got, "ICMP"); icmp != tc.icmp {
				t.Errorf("sources %q (%s): ICMP mentioned = %v, want %v: %q", tc.sources, lang, icmp, tc.icmp, got)
			}
			if dc := strings.Contains(got, "data-centre") || strings.Contains(got, "дата-центр") || strings.Contains(got, "机房"); dc != tc.dc {
				t.Errorf("sources %q (%s): data-centre wording = %v, want %v: %q", tc.sources, lang, dc, tc.dc, got)
			}
		}
	}
}

// A paused rollout says in words what the check after the update saw; a code it does not know stays as it is.
func TestUpdateFailedReasonIsASentence(t *testing.T) {
	for _, tc := range []struct{ lang, reason, want string }{
		{"en", "not_committed", "The new build did not confirm the update."},
		{"en", "state_not_applied", "The node did not apply its configuration."},
		{"en", "inbounds_unreadable", "The panel could not read the node’s profiles."},
		{"ru", "inbounds_unreadable", "Панель не смогла прочитать профили ноды."},
		{"zh", "not_committed", "新构建未确认此次更新。"},
		{"en", "warp_down", "WARP did not come up after the update."},
		{"en", "hash_mismatch", "hash_mismatch"},
	} {
		a := store.HealthAlert{Kind: "update_failed", Params: map[string]string{"node": "de1", "reason": tc.reason}}
		if got := alertReason(L(tc.lang), a); got != tc.want {
			t.Errorf("alertReason(%s, %s) = %q, want %q", tc.lang, tc.reason, got, tc.want)
		}
	}
}
