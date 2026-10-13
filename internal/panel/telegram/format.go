package telegram

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The words of the messages. The admin screen words alerts in the browser from keys; a chat cannot, so the few sentences a
// message needs live here, in the panel language (instance.Language: "ru", "zh", or "en"). A message is HTML
// (<b>, <a>) and nothing else; every value that came from data (a node, profile or token name, an agent's detail, a login)
// goes through esc() and clip(): it may hold "<" or a newline, and it is never trusted to be short.
//
// Never in a message: a token, a subscription link, a password. A message is built only from the fields named below.

// L is a language: en, ru, or zh.
type L string

type localizedText struct{ en, ru, zh string }

func (t localizedText) forLanguage(l L) string {
	switch l {
	case "ru":
		if t.ru != "" {
			return t.ru
		}
	case "zh":
		if t.zh != "" {
			return t.zh
		}
	}
	return t.en
}

func (l L) pick(en, ru, zh string) string { return localizedText{en, ru, zh}.forLanguage(l) }

// esc makes untrusted text safe in Telegram HTML (only & < > matter there; quotes matter inside an attribute).
var htmlEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func esc(s string) string { return htmlEsc.Replace(s) }

// clip makes data a short single line: control characters and line breaks become spaces, at most n runes.
func clip(s string, n int) string {
	var b strings.Builder
	runes := 0
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			runes++
			space = false
		}
		if runes >= n {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

// data is clip + esc, for a value from data.
func data(s string, n int) string { return esc(clip(s, n)) }

// duration is "14 min", "2 h 5 min", "1 d 3 h" (ru: мин, ч, д): the coarse length of an alert.
func (l L) duration(d time.Duration) string {
	if d < time.Minute {
		return l.pick("under a minute", "меньше минуты", // zh: translation pass
			"under a minute",
		)
	}
	m := int(d.Minutes())
	min, h, day := l.pick("min", "мин", // zh: translation pass
		"min",
	), l.pick("h", "ч", // zh: translation pass
		"h",
	), l.pick("d", "д", // zh: translation pass
		"d",
	)
	switch {
	case m < 60:
		return strconv.Itoa(m) + " " + min
	case m < 24*60:
		if m%60 == 0 {
			return strconv.Itoa(m/60) + " " + h
		}
		return strconv.Itoa(m/60) + " " + h + " " + strconv.Itoa(m%60) + " " + min
	}
	d2, h2 := m/(24*60), (m%(24*60))/60
	if h2 == 0 {
		return strconv.Itoa(d2) + " " + day
	}
	return strconv.Itoa(d2) + " " + day + " " + strconv.Itoa(h2) + " " + h
}

// link is an <a> to a page of the admin, or "" when the panel does not know its public address.
func link(adminURL, page string, text string) string {
	if adminURL == "" {
		return ""
	}
	return `<a href="` + esc(strings.TrimRight(adminURL, "/")+"/"+page) + `">` + esc(text) + `</a>`
}

// join puts non-empty lines one under another.
func join(lines ...string) string {
	var out []string
	for _, s := range lines {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// ---- health alerts

// checkTitles are the titles of the doctor's checks (agent.proto "CHECK IDS").
var checkTitles = map[string]localizedText{
	"disk_space":      {en: "Disk space", ru: "Место на диске", zh: "Disk space"},                                 // zh: translation pass
	"journald_size":   {en: "System journal size", ru: "Размер журнала systemd", zh: "System journal size"},       // zh: translation pass
	"dstate_tasks":    {en: "Hung processes", ru: "Зависшие процессы", zh: "Hung processes"},                      // zh: translation pass
	"time_sync":       {en: "Clock", ru: "Часы", zh: "Clock"},                                                     // zh: translation pass
	"resolver":        {en: "Server resolver", ru: "Резолвер сервера", zh: "Server resolver"},                     // zh: translation pass
	"ipv6":            {en: "IPv6", ru: "IPv6", zh: "IPv6"},                                                       // zh: translation pass
	"foreign_vpn":     {en: "Leftovers of other VPNs", ru: "Следы других VPN", zh: "Leftovers of other VPNs"},     // zh: translation pass
	"foreign_nft":     {en: "Foreign firewall rules", ru: "Чужие правила файрвола", zh: "Foreign firewall rules"}, // zh: translation pass
	"port_conflicts":  {en: "Ports in use", ru: "Занятые порты", zh: "Ports in use"},                              // zh: translation pass
	"net_baseline":    {en: "Base network settings", ru: "Базовая настройка сети", zh: "Base network settings"},   // zh: translation pass
	"cert_expiry":     {en: "Certificates", ru: "Сертификаты", zh: "Certificates"},                                // zh: translation pass
	"memory_pressure": {en: "Memory", ru: "Память", zh: "Memory"},                                                 // zh: translation pass
	"cpu_softirq":     {en: "CPU softirq", ru: "CPU softirq", zh: "CPU softirq"},                                  // zh: translation pass
	"kernel_headers":  {en: "Kernel headers", ru: "Заголовки ядра", zh: "Kernel headers"},                         // zh: translation pass
	"awg_backend":     {en: "AmneziaWG module", ru: "Модуль AmneziaWG", zh: "AmneziaWG module"},                   // zh: translation pass
	"warp_path":       {en: "WARP is down", ru: "WARP не работает", zh: "WARP is down"},                           // zh: translation pass
}

// whyVariants say in a few words why a check failed (the "variant" of health.alert.<kind>.why.<variant>).
var whyVariants = map[string]localizedText{
	"via_cdn":          {en: "the check goes through the CDN; check the CDN and the origin", ru: "проверка идёт через CDN; проверьте CDN и источник", zh: "the check goes through the CDN; check the CDN and the origin"}, // zh: translation pass
	"udp_blocked":      {en: "the hoster seems to block UDP on this port", ru: "хостер, похоже, режет UDP на этом порту", zh: "the hoster seems to block UDP on this port"},                                               // zh: translation pass
	"udp_all_blocked":  {en: "the hoster seems to block UDP on the whole node", ru: "хостер, похоже, режет весь UDP на ноде", zh: "the hoster seems to block UDP on the whole node"},                                      // zh: translation pass
	"timeout":          {en: "the handshake does not complete", ru: "рукопожатие не завершается", zh: "the handshake does not complete"},                                                                                  // zh: translation pass
	"auth":             {en: "the panel’s check account is refused", ru: "проверочная учётка панели отклонена", zh: "the panel’s check account is refused"},                                                               // zh: translation pass
	"tls":              {en: "the certificate does not match", ru: "сертификат не подходит", zh: "the certificate does not match"},                                                                                        // zh: translation pass
	"refused":          {en: "nothing listens on the port", ru: "на порту никто не слушает", zh: "nothing listens on the port"},                                                                                           // zh: translation pass
	"exit_unreachable": {en: "the node cannot reach the internet through the tunnel", ru: "нода не выходит в интернет через туннель", zh: "the node cannot reach the internet through the tunnel"},                        // zh: translation pass
	"http_status":      {en: "the test sites answer with errors", ru: "тестовые сайты отвечают ошибками", zh: "the test sites answer with errors"},                                                                        // zh: translation pass
	"warp_path":        {en: "the WARP exit is dead", ru: "выход через WARP не работает", zh: "the WARP exit is dead"},                                                                                                    // zh: translation pass
	"mixed":            {en: "the profiles fail in different ways", ru: "профили падают по-разному", zh: "the profiles fail in different ways"},                                                                           // zh: translation pass
}

func variantOf(whyKey, kind string) string {
	_, v, _ := strings.Cut(whyKey, "health.alert."+kind+".why.")
	return v
}

// alertTitle is the headline of an alert.
func alertTitle(l L, a store.HealthAlert) string {
	p := a.Params
	switch a.Kind {
	case "node_down":
		return l.pick("Node is unreachable", "Нода недоступна", // zh: translation pass
			"Node is unreachable",
		)
	case "no_traffic":
		return l.pick("Alive, but no traffic", "Жива, но трафик не идёт", // zh: translation pass
			"Alive, but no traffic",
		)
	case "check_failed":
		if a.Subject == "panel_egress" {
			return l.pick("The panel cannot reach many nodes", "Панель не достаёт до многих нод", // zh: translation pass
				"The panel cannot reach many nodes",
			)
		}
		if a.Subject == "probe_path" {
			return l.pick("Panel probes fail across several nodes", "Проверки панели не проходят на нескольких нодах", // zh: translation pass
				"Panel probes fail across several nodes",
			)
		}
		if p["profile"] == "" {
			return l.pick("A profile fails the check", "Профиль не проходит проверку", // zh: translation pass
				"A profile fails the check",
			)
		}
		return l.pick("Profile “"+data(p["profile"], 40)+"” fails the check", "Профиль «"+data(p["profile"], 40)+"» не проходит проверку", // zh: translation pass
			"Profile “"+data(p["profile"], 40)+"” fails the check",
		)
	case "doctor_warn", "doctor_fail":
		return doctorTitle(l, p["check"])
	case "state_drift":
		return l.pick("Node state differs from the panel", "Состояние ноды разошлось с панелью", // zh: translation pass
			"Node state differs from the panel",
		)
	case "cert_expiry":
		return l.pick("Certificate is about to expire", "Сертификат скоро истечёт", // zh: translation pass
			"Certificate is about to expire",
		)
	case "update_failed":
		return l.pick("The update is paused", "Обновление остановилось", // zh: translation pass
			"The update is paused",
		)
	case "quota":
		return l.pick("A user reached the quota", "Пользователь выбрал квоту", // zh: translation pass
			"A user reached the quota",
		)
	case "subscription_shared_suspect":
		return l.pick("A subscription looks shared", "Подписка похожа на общую", // zh: translation pass
			"A subscription looks shared",
		)
	case "access_ended":
		return l.pick("Access ended while the user is still trying", "Доступ закончился, но пользователь продолжает подключаться", // zh: translation pass
			"Access ended while the user is still trying",
		)
	case "user_connection":
		return l.pick("A user may have a connection problem", "У пользователя может быть проблема с подключением", // zh: translation pass
			"A user may have a connection problem",
		)
	case "users_impacted":
		return l.pick("Connections dropped for some users", "У части пользователей пропало подключение", // zh: translation pass
			"Connections dropped for some users",
		)
	case "torrent":
		return l.pick("Torrent attempts", "Попытки качать торренты", // zh: translation pass
			"Torrent attempts",
		)
	case "port_lossy":
		return l.pick("UDP packets are being lost", "Теряются UDP-пакеты", // zh: translation pass
			"UDP packets are being lost",
		)
	}
	return data(strings.ReplaceAll(a.Kind, "_", " "), 40)
}

// alertReason is one short line of why, "" when the headline says it all.
func alertReason(l L, a store.HealthAlert) string {
	p := a.Params
	switch a.Kind {
	case "node_down":
		if m := p["minutes"]; m != "" {
			return l.pick("No contact for "+data(m, 6)+" min.", "Нет связи уже "+data(m, 6)+" мин.", // zh: translation pass
				"No contact for "+data(m, 6)+" min.",
			)
		}
	case "no_traffic":
		line := l.pick(data(p["failed"], 4)+" of "+data(p["total"], 4)+" profiles fail the check.", data(p["failed"], 4)+" из "+data(p["total"], 4)+" профилей не проходят проверку.", // zh: translation pass
			data(p["failed"], 4)+" of "+data(p["total"], 4)+" profiles fail the check.",
		)
		if v, ok := whyVariants[variantOf(a.WhyKey, "no_traffic")]; ok {
			line += " " + upperFirst(v.forLanguage(l)) + "."
		}
		return line
	case "check_failed":
		if a.WhyKey == "health.alert.check_failed.why.probe_path" {
			return l.pick("Probes from the panel failed on several nodes at once ("+data(p["nodes"], 200)+"), at "+data(p["providers"], 3)+
				" different hosters. Most likely the panel's own network, not the nodes. No per-node alerts and no port advice unless it lasts 20 minutes.",
				"Проверки с панели провалились сразу на нескольких нодах ("+data(p["nodes"], 200)+") у "+data(p["providers"], 3)+
					" разных хостеров. Скорее всего, сбой в сети самой панели, а не на нодах. Отдельных тревог по нодам и советов сменить порт не будет, если сбой не продлится 20 минут.", // zh: translation pass
				"Probes from the panel failed on several nodes at once ("+data(p["nodes"], 200)+"), at "+data(p["providers"], 3)+
					" different hosters. Most likely the panel's own network, not the nodes. No per-node alerts and no port advice unless it lasts 20 minutes.",
			)
		}
		if a.Subject == "panel_egress" {
			return l.pick(data(p["failed"], 4)+" of "+data(p["total"], 4)+" checks fail across "+data(p["providers"], 3)+" providers: likely the panel’s own network.",
				data(p["failed"], 4)+" из "+data(p["total"], 4)+" проверок не проходят у "+data(p["providers"], 3)+" провайдеров: скорее всего, виновата сеть самой панели.", // zh: translation pass
				data(p["failed"], 4)+" of "+data(p["total"], 4)+" checks fail across "+data(p["providers"], 3)+" providers: likely the panel’s own network.",
			)
		}
		var parts []string
		if v, ok := whyVariants[variantOf(a.WhyKey, "check_failed")]; ok {
			parts = append(parts, upperFirst(v.forLanguage(l)))
		}
		if p["port"] != "" {
			parts = append(parts, l.pick("port ", "порт ", // zh: translation pass
				"port ",
			)+data(p["port"], 6))
		}
		return strings.Join(parts, " · ")
	case "doctor_warn", "doctor_fail":
		if text, ok := doctorDetail(l, p); ok {
			return text
		}
		return l.pick("Node check: "+doctorTitle(l, p["check"]), "Проверка узла: "+doctorTitle(l, p["check"]), // zh: translation pass
			"Node check: "+doctorTitle(l, p["check"]),
		)
	case "cert_expiry":
		who := data(p["profile"], 40)
		switch {
		case strings.HasSuffix(a.WhyKey, ".san_mismatch"):
			return l.pick("“"+who+"” does not match its domain "+data(p["server_name"], 80)+".", "«"+who+"» не подходит к домену "+data(p["server_name"], 80)+".", // zh: translation pass
				"“"+who+"” does not match its domain "+data(p["server_name"], 80)+".",
			)
		case strings.HasSuffix(a.WhyKey, ".expired"):
			return l.pick("“"+who+"” has expired.", "«"+who+"» истёк.", // zh: translation pass
				"“"+who+"” has expired.",
			)
		case p["days_left"] != "":
			return l.pick("“"+who+"”: "+data(p["days_left"], 4)+" days left.", "«"+who+"»: осталось "+data(p["days_left"], 4)+" дн.", // zh: translation pass
				"“"+who+"”: "+data(p["days_left"], 4)+" days left.",
			)
		}
	case "update_failed":
		return data(p["reason"], 160)
	case "access_ended":
		name := data(p["user_name"], 60)
		since, err := strconv.ParseInt(p["since"], 10, 64)
		if err != nil || since <= 0 {
			return l.pick(name+" keeps trying to connect, but the subscription has ended.", name+" продолжает подключаться, хотя подписка закончилась.", // zh: translation pass
				name+" keeps trying to connect, but the subscription has ended.",
			)
		}
		opened := a.OpenedAt
		if opened.IsZero() {
			opened = a.FirstSeen
		}
		ago := l.duration(opened.Sub(time.Unix(since, 0)))
		if strings.HasSuffix(a.WhyKey, ".quota") {
			return l.pick(name+" keeps trying to connect, but the quota ran out "+ago+" ago.",
				name+" продолжает подключаться, хотя квота закончилась "+ago+" назад.", // zh: translation pass
				name+" keeps trying to connect, but the quota ran out "+ago+" ago.",
			)
		}
		return l.pick(name+" keeps trying to connect, but the subscription ended "+ago+" ago.",
			name+" продолжает подключаться, хотя подписка закончилась "+ago+" назад.", // zh: translation pass
			name+" keeps trying to connect, but the subscription ended "+ago+" ago.",
		)
	case "torrent":
		return torrentReason(l, p)
	case "port_lossy":
		return portLossReason(l, p)
	case "users_impacted":
		protocol := a.Subject
		switch protocol {
		case "awg":
			protocol = "AmneziaWG"
		case "hysteria2":
			protocol = "Hysteria2"
		case "vless":
			protocol = "VLESS"
		default:
			protocol = data(protocol, 32)
		}
		return l.pick("People on "+protocol+": now "+data(p["now"], 8)+", usually about "+data(p["usual"], 8)+
			" at this hour, while the node is online. This looks like a block on the users' side.",
			"Людей на "+protocol+": сейчас "+data(p["now"], 8)+", обычно около "+data(p["usual"], 8)+
				" в этот час, а нода на связи. Похоже на блокировку на стороне пользователей.", // zh: translation pass
			"People on "+protocol+": now "+data(p["now"], 8)+", usually about "+data(p["usual"], 8)+
				" at this hour, while the node is online. This looks like a block on the users' side.",
		)
	}
	return ""
}

func portLossReason(l L, p map[string]string) string {
	profile, port := data(p["profile"], 40), data(p["port"], 6)
	percent := "?"
	sent, sentErr := strconv.ParseUint(p["sent"], 10, 32)
	got, gotErr := strconv.ParseUint(p["got"], 10, 32)
	if sentErr == nil && gotErr == nil && sent > 0 && got <= sent {
		lost := sent - got
		percent = strconv.FormatUint((lost*100+sent/2)/sent, 10)
	}
	return l.pick("Profile “"+profile+"” on port "+port+" lost "+percent+"% of UDP packets in the latest check.",
		"Профиль «"+profile+"» на порту "+port+" потерял "+percent+"% UDP-пакетов при последней проверке.", // zh: translation pass
		"Profile “"+profile+"” on port "+port+" lost "+percent+"% of UDP packets in the latest check.",
	)
}

// torrentEvidence say in a few words what the node's torrent guard matched (the "evidence" of a torrent_attempt event).
var torrentEvidence = map[string]localizedText{
	"tracker_connect": {en: "tracker connect request (the protocol’s magic number)", ru: "запрос подключения к трекеру (магическое число протокола)", zh: "tracker connect request (the protocol’s magic number)"}, // zh: translation pass
	"dht_query":       {en: "DHT query", ru: "запрос DHT", zh: "DHT query"},                                                                                                                                        // zh: translation pass
	"utp_syn":         {en: "uTP connection start", ru: "начало соединения uTP", zh: "uTP connection start"},                                                                                                       // zh: translation pass
	"tcp_handshake":   {en: "BitTorrent handshake over TCP", ru: "рукопожатие BitTorrent по TCP", zh: "BitTorrent handshake over TCP"},                                                                             // zh: translation pass
}

// torrentReason is the sentence of a TORRENT alert: who, where, how many attempts, and the evidence and port of them when
// the node said so (an older agent does not). Values from data go through data().
func torrentReason(l L, p map[string]string) string {
	name, nodes := data(p["user_name"], 60), data(p["nodes"], 120)
	count, _ := strconv.Atoi(p["count"])
	where := ""
	if nodes != "" {
		where = l.pick(" on ", " на ", // zh: translation pass
			" on ",
		) + nodes
	}
	var attempts string
	if l == "ru" {
		attempts = strconv.Itoa(count) + " " + ruPlural(count, "попытка", "попытки", "попыток")
	} else {
		attempts = strconv.Itoa(count) + " attempt"
		if count != 1 {
			attempts += "s"
		}
	}
	line := l.pick(name+" is trying to use torrents"+where+": "+attempts+" in a day, blocked.",
		name+" пытается качать торренты"+where+": "+attempts+" за сутки, торренты заблокированы.", // zh: translation pass
		name+" is trying to use torrents"+where+": "+attempts+" in a day, blocked.",
	)
	var proof []string
	if text, ok := torrentEvidence[p["evidence"]]; ok {
		proof = append(proof, text.forLanguage(l))
	} else if p["evidence"] != "" {
		proof = append(proof, data(p["evidence"], 40))
	}
	if ports := data(p["ports"], 60); ports != "" {
		word := l.pick("port ", "порт ", // zh: translation pass
			"port ",
		)
		if strings.Contains(ports, ",") {
			word = l.pick("ports ", "порты ", // zh: translation pass
				"ports ",
			)
		}
		proof = append(proof, word+ports)
	}
	if len(proof) == 0 {
		return line
	}
	lead := l.pick("Evidence: ", "Доказательство: ", // zh: translation pass
		"Evidence: ",
	)
	if p["evidence"] == "" {
		lead = ""
	}
	return line + " " + upperFirst(lead+strings.Join(proof, ", ")) + "."
}

// ruPlural picks the Russian form after a number: 1 попытка, 2 попытки, 5 попыток.
func ruPlural(n int, one, few, many string) string {
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	}
	return many
}

func doctorTitle(l L, check string) string {
	if title, ok := checkTitles[check]; ok {
		return title.forLanguage(l)
	}
	return l.pick("Node check", "Проверка узла", // zh: translation pass
		"Node check",
	)
}

// doctorDetail words the WARP doctor rows that can open alerts. The agent's free-form detail is deliberately never a
// fallback: it can contain English, inbound ids or other node-local identifiers. Unknown codes use the check title.
func doctorDetail(l L, p map[string]string) (string, bool) {
	switch p["detail_code"] {
	case "warp_path.not_configured":
		profiles := quotedProfiles(l, p["profiles"])
		if profiles == "" {
			return l.pick("Profiles with WARP egress cannot start: the node has no WARP account",
				"Профили с выходом через WARP не стартуют: у ноды нет аккаунта WARP", // zh: translation pass
				"Profiles with WARP egress cannot start: the node has no WARP account",
			), true
		}
		return l.pick("Profiles with WARP egress ("+profiles+") cannot start: the node has no WARP account",
			"Профили с выходом через WARP ("+profiles+") не стартуют: у ноды нет аккаунта WARP", // zh: translation pass
			"Profiles with WARP egress ("+profiles+") cannot start: the node has no WARP account",
		), true
	case "warp_path.host_clash":
		return l.pick("WARP cannot use this host", "WARP не может работать на этом хосте", // zh: translation pass
			"WARP cannot use this host",
		), true
	case "warp_path.check_failed":
		state := warpStateWord(l, p["state"])
		if state == "" {
			return l.pick("The latest WARP check failed", "Последняя проверка WARP не прошла", // zh: translation pass
				"The latest WARP check failed",
			), true
		}
		return l.pick("WARP is "+state+", but its latest check failed", "WARP "+state+", но последняя проверка не прошла", // zh: translation pass
			"WARP is "+state+", but its latest check failed",
		), true
	case "warp_path.no_backend":
		return l.pick("No working WireGuard backend", "Нет рабочего бэкенда WireGuard", // zh: translation pass
			"No working WireGuard backend",
		), true
	case "warp_path.paused_used":
		profiles := quotedProfiles(l, p["profiles"])
		if profiles == "" {
			return l.pick("WARP is paused and dependent profiles fail closed", "WARP на паузе, профили с ним не пропускают трафик", // zh: translation pass
				"WARP is paused and dependent profiles fail closed",
			), true
		}
		return l.pick("WARP is paused; profiles that use it fail closed: "+profiles,
			"WARP на паузе; профили с ним не пропускают трафик: "+profiles, // zh: translation pass
			"WARP is paused; profiles that use it fail closed: "+profiles,
		), true
	case "warp_path.down":
		return l.pick("WARP is down", "WARP не работает", // zh: translation pass
			"WARP is down",
		), true
	case "warp_path.unknown_state":
		return l.pick("Unknown WARP state", "Неизвестное состояние WARP", // zh: translation pass
			"Unknown WARP state",
		), true
	default:
		return "", false
	}
}

func warpStateWord(l L, state string) string {
	switch state {
	case "up":
		return l.pick("up", "работает", // zh: translation pass
			"up",
		)
	case "starting":
		return l.pick("starting", "запускается", // zh: translation pass
			"starting",
		)
	case "unavailable":
		return l.pick("unavailable", "недоступен", // zh: translation pass
			"unavailable",
		)
	case "disabled":
		return l.pick("paused", "на паузе", // zh: translation pass
			"paused",
		)
	case "down":
		return l.pick("down", "не работает", // zh: translation pass
			"down",
		)
	default:
		return ""
	}
}

func quotedProfiles(l L, profiles string) string {
	var names []string
	for _, name := range strings.Split(profiles, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if l == "ru" {
			names = append(names, "«"+data(name, 60)+"»")
		} else {
			names = append(names, "“"+data(name, 60)+"”")
		}
	}
	return strings.Join(names, ", ")
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// alertOpened is the message of an alert that began. node is the node's name ("" for a panel-wide alert).
func alertOpened(l L, a store.HealthAlert, node, adminURL string) string {
	sev := ""
	if a.Severity >= 3 {
		sev = l.pick(" · critical", " · критично", // zh: translation pass
			" · critical",
		)
	}
	return join(
		"<b>"+alertTitle(l, a)+"</b>"+sev,
		nodeLine(l, node),
		againLine(l, a),
		alertReason(l, a),
		link(adminURL, "health", l.pick("Open Health", "Открыть «Здоровье»", // zh: translation pass
			"Open Health",
		)),
	)
}

// againLine marks an alert that fired again within the re-open window: its row is kept, so first_seen is the first
// episode and opened_at the current one. UTC like lockoutText; the date is added when the first episode was not today.
func againLine(l L, a store.HealthAlert) string {
	if !a.OpenedAt.After(a.FirstSeen) {
		return ""
	}
	first, layout := a.FirstSeen.UTC(), "15:04"
	if a.OpenedAt.UTC().Format(time.DateOnly) != first.Format(time.DateOnly) {
		layout = "02.01 15:04"
	}
	return l.pick("Again, first at ", "Снова, впервые в ", // zh: translation pass
		"Again, first at ",
	) + first.Format(layout) + " UTC"
}

// alertResolved is the message of an alert that ended, with how long its current episode lasted: counted from
// opened_at, because a re-opened alert keeps first_seen and would include the gap between its episodes.
func alertResolved(l L, a store.HealthAlert, node string) string {
	tail := ""
	if !a.OpenedAt.IsZero() && a.ResolvedAt.After(a.OpenedAt) {
		tail = l.pick("lasted ", "длилось ", // zh: translation pass
			"lasted ",
		) + l.duration(a.ResolvedAt.Sub(a.OpenedAt))
	}
	line := nodeLine(l, node)
	switch {
	case line != "" && tail != "":
		line += " · " + tail
	case tail != "":
		line = upperFirst(tail)
	}
	reason := ""
	if a.Kind == "port_lossy" {
		reason = alertReason(l, a)
	}
	return join("<b>"+l.pick("Resolved: ", "Решено: ", // zh: translation pass
		"Resolved: ",
	)+alertTitle(l, a)+"</b>", line, reason, againLine(l, a))
}

func nodeLine(l L, node string) string {
	if node == "" {
		return ""
	}
	return l.pick("Node: ", "Нода: ", // zh: translation pass
		"Node: ",
	) + "<b>" + data(node, 60) + "</b>"
}

// ---- the rest

func releaseText(l L, version, adminURL string) string {
	return join(
		"<b>"+l.pick("A new panel release is available: ", "Вышла новая версия панели: ", // zh: translation pass
			"A new panel release is available: ",
		)+data(version, 30)+"</b>",
		l.pick("It is signed; install it from the Updates page.", "Она подписана; установить можно на странице «Обновления».", // zh: translation pass
			"It is signed; install it from the Updates page.",
		),
		link(adminURL, "updates", l.pick("Open Updates", "Открыть «Обновления»", // zh: translation pass
			"Open Updates",
		)),
	)
}

func agentsOutdatedText(l L, version string, nodes []string, adminURL string) string {
	names := make([]string, 0, 5)
	for i, n := range nodes {
		if i == 5 {
			names = append(names, l.pick("and "+strconv.Itoa(len(nodes)-5)+" more", "и ещё "+strconv.Itoa(len(nodes)-5), // zh: translation pass
				"and "+strconv.Itoa(len(nodes)-5)+" more",
			))
			break
		}
		names = append(names, data(n, 40))
	}
	return join(
		"<b>"+l.pick("Node agents are behind the release ", "Агенты нод отстают от релиза ", // zh: translation pass
			"Node agents are behind the release ",
		)+data(version, 30)+"</b>",
		l.pick("Not updated: ", "Не обновлены: ", // zh: translation pass
			"Not updated: ",
		)+strings.Join(names, ", ")+".",
		link(adminURL, "updates", l.pick("Open Updates", "Открыть «Обновления»", // zh: translation pass
			"Open Updates",
		)),
	)
}

var backupReasons = map[string]localizedText{
	"backup_storage_failed":        {en: "the storage did not answer", ru: "хранилище не отвечает", zh: "the storage did not answer"},                                                    // zh: translation pass
	"backup_storage_delete_failed": {en: "the storage refused to delete an old backup", ru: "хранилище не дало удалить старую копию", zh: "the storage refused to delete an old backup"}, // zh: translation pass
	"backup_create_failed":         {en: "the archive could not be made", ru: "не удалось собрать архив", zh: "the archive could not be made"},                                           // zh: translation pass
	"backup_settings_invalid":      {en: "the backup settings are not valid", ru: "настройки бэкапа неверны", zh: "the backup settings are not valid"},                                   // zh: translation pass
}

func backupFailedText(l L, code, adminURL string) string {
	reason := data(code, 40)
	if r, ok := backupReasons[code]; ok {
		reason = r.forLanguage(l)
	}
	return join(
		"<b>"+l.pick("The panel backup failed", "Бэкап панели не удался", // zh: translation pass
			"The panel backup failed",
		)+"</b>",
		upperFirst(reason)+".",
		link(adminURL, "settings/backups", l.pick("Open backup settings", "Открыть настройки бэкапа", // zh: translation pass
			"Open backup settings",
		)),
	)
}

func backupRecoveredText(l L) string {
	return "<b>" + l.pick("Resolved: the panel backup works again", "Решено: бэкап панели снова работает", // zh: translation pass
		"Resolved: the panel backup works again",
	) + "</b>"
}

// toolTitles are the MCP tools that can wait for the owner (the admin screen's "approval.tool.*").
var toolTitles = map[string]localizedText{
	"user_create":                 {en: "Create a user", ru: "Создать пользователя", zh: "Create a user"},                                                                                      // zh: translation pass
	"user_update":                 {en: "Change a user", ru: "Изменить пользователя", zh: "Change a user"},                                                                                     // zh: translation pass
	"user_disable":                {en: "Disable users", ru: "Отключить пользователей", zh: "Disable users"},                                                                                   // zh: translation pass
	"user_enable":                 {en: "Enable users", ru: "Включить пользователей", zh: "Enable users"},                                                                                      // zh: translation pass
	"user_reset_traffic":          {en: "Reset users’ traffic", ru: "Сбросить трафик пользователей", zh: "Reset users’ traffic"},                                                               // zh: translation pass
	"device_revoke":               {en: "Remove a device", ru: "Удалить устройство", zh: "Remove a device"},                                                                                    // zh: translation pass
	"alert_mute":                  {en: "Mute an alert", ru: "Заглушить алерт", zh: "Mute an alert"},                                                                                           // zh: translation pass
	"node_fix":                    {en: "Fix a node", ru: "Исправить ноду", zh: "Fix a node"},                                                                                                  // zh: translation pass
	"rollout_start":               {en: "Update a node now", ru: "Обновить ноду сейчас", zh: "Update a node now"},                                                                              // zh: translation pass
	"rollout_pause":               {en: "Pause the rollout", ru: "Поставить раскатку на паузу", zh: "Pause the rollout"},                                                                       // zh: translation pass
	"rollout_resume":              {en: "Resume the rollout", ru: "Продолжить раскатку", zh: "Resume the rollout"},                                                                             // zh: translation pass
	"rollout_cancel":              {en: "Cancel the rollout", ru: "Отменить раскатку", zh: "Cancel the rollout"},                                                                               // zh: translation pass
	"node_update_schedule":        {en: "Schedule a node update", ru: "Запланировать обновление ноды", zh: "Schedule a node update"},                                                           // zh: translation pass
	"node_update_schedule_cancel": {en: "Cancel a scheduled node update", ru: "Отменить запланированное обновление ноды", zh: "Cancel a scheduled node update"},                                // zh: translation pass
	"update_timezone":             {en: "Change the time zone of update schedules", ru: "Сменить часовой пояс расписания обновлений", zh: "Change the time zone of update schedules"},          // zh: translation pass
	"node_rollback":               {en: "Roll a node back", ru: "Откатить ноду", zh: "Roll a node back"},                                                                                       // zh: translation pass
	"node_install":                {en: "Install a node over SSH", ru: "Установить ноду по SSH", zh: "Install a node over SSH"},                                                                // zh: translation pass
	"node_server_password_rotate": {en: "Change a node’s SSH password", ru: "Сменить SSH-пароль ноды", zh: "Change a node’s SSH password"},                                                     // zh: translation pass
	"subscription_app_upsert":     {en: "Add or change an app on the user page", ru: "Добавить или изменить приложение на странице пользователя", zh: "Add or change an app on the user page"}, // zh: translation pass
	"subscription_app_remove":     {en: "Remove an app from the user page", ru: "Убрать приложение со страницы пользователя", zh: "Remove an app from the user page"},                          // zh: translation pass
}

// planWaitingText says an agent's plan waits for the owner. It never says what the plan contains beyond the tool's title:
// the owner reads the rest on the approval screen, behind a step-up.
func planWaitingText(l L, tool, token, adminURL string) string {
	title := data(tool, 40)
	if t, ok := toolTitles[tool]; ok {
		title = t.forLanguage(l)
	}
	return join(
		"<b>"+l.pick("An agent’s plan waits for your approval", "План агента ждёт твоего решения", // zh: translation pass
			"An agent’s plan waits for your approval",
		)+"</b>",
		l.pick("“"+data(token, 40)+"” wants to: "+title+".", "«"+data(token, 40)+"» хочет: "+title+".", // zh: translation pass
			"“"+data(token, 40)+"” wants to: "+title+".",
		),
		l.pick("It expires in 10 minutes.", "Он истечёт через 10 минут.", // zh: translation pass
			"It expires in 10 minutes.",
		),
		link(adminURL, "integrations", l.pick("Open Integrations → Waiting for you", "Открыть «Интеграции» → «Ждут тебя»", // zh: translation pass
			"Open Integrations → Waiting for you",
		)),
	)
}

func awgPrepareFailedText(l L, node, code, adminURL string) string {
	return join(
		"<b>"+l.pick("The AmneziaWG kernel module could not be built", "Не удалось собрать модуль ядра AmneziaWG", // zh: translation pass
			"The AmneziaWG kernel module could not be built",
		)+"</b>",
		nodeLine(l, node),
		l.pick("The node keeps running the userspace version. Reason code: ", "Нода продолжает работать на userspace-версии. Код причины: ", // zh: translation pass
			"The node keeps running the userspace version. Reason code: ",
		)+data(code, 32)+".",
		link(adminURL, "health", l.pick("Open Health", "Открыть «Здоровье»", // zh: translation pass
			"Open Health",
		)),
	)
}

func testText(l L, brand string) string {
	return "<b>" + l.pick("Test message", "Проверка связи", // zh: translation pass
		"Test message",
	) + "</b>\n" +
		l.pick("Alerts from "+data(brand, 40)+" will arrive in this chat.", "Алерты от «"+data(brand, 40)+"» будут приходить в этот чат.", // zh: translation pass
			"Alerts from "+data(brand, 40)+" will arrive in this chat.",
		)
}

func linkedText(l L, name, brand string) string {
	return l.pick("Linked to "+data(brand, 40)+" as "+data(name, 60)+". Alerts are on; switch them off or unlink in the panel.",
		"Чат привязан к «"+data(brand, 40)+"» как "+data(name, 60)+". Алерты включены; выключить или отвязать можно в панели.", // zh: translation pass
		"Linked to "+data(brand, 40)+" as "+data(name, 60)+". Alerts are on; switch them off or unlink in the panel.",
	)
}

func alreadyLinkedText(l L, name, brand string) string {
	return l.pick("This chat gets alerts from "+data(brand, 40)+" for "+data(name, 60)+". Manage it in the panel.",
		"Этот чат получает алерты «"+data(brand, 40)+"» для "+data(name, 60)+". Управлять можно в панели.", // zh: translation pass
		"This chat gets alerts from "+data(brand, 40)+" for "+data(name, 60)+". Manage it in the panel.",
	)
}
