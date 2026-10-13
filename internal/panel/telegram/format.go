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

// stop ends a sentence and gap is the space after it; list and comma separate items. Chinese has a full-width stop, no
// space after it and its own separators.
func (l L) stop() string { return l.pick(".", ".", "。") }
func (l L) gap() string {
	if l == "zh" {
		return "" // pick would fall back to English on an empty text
	}
	return " "
}
func (l L) list() string  { return l.pick(", ", ", ", "、") }
func (l L) comma() string { return l.pick(", ", ", ", "，") }

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
		return l.pick("under a minute", "меньше минуты", "不到 1 分钟")
	}
	m := int(d.Minutes())
	min, h, day := l.pick("min", "мин", "分钟"), l.pick("h", "ч", "小时"), l.pick("d", "д", "天")
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
	"disk_space":      {en: "Disk space", ru: "Место на диске", zh: "磁盘空间"},
	"journald_size":   {en: "System journal size", ru: "Размер журнала systemd", zh: "系统日志大小"},
	"dstate_tasks":    {en: "Hung processes", ru: "Зависшие процессы", zh: "挂起的进程"},
	"time_sync":       {en: "Clock", ru: "Часы", zh: "时钟"},
	"resolver":        {en: "Server resolver", ru: "Резолвер сервера", zh: "服务器 DNS 解析"},
	"ipv6":            {en: "IPv6", ru: "IPv6", zh: "IPv6"},
	"foreign_vpn":     {en: "Leftovers of other VPNs", ru: "Следы других VPN", zh: "其他 VPN 的残留"},
	"foreign_nft":     {en: "Foreign firewall rules", ru: "Чужие правила файрвола", zh: "非 Mistgate 防火墙规则"},
	"port_conflicts":  {en: "Ports in use", ru: "Занятые порты", zh: "端口占用"},
	"net_baseline":    {en: "Base network settings", ru: "Базовая настройка сети", zh: "网络基线设置"},
	"cert_expiry":     {en: "Certificates", ru: "Сертификаты", zh: "证书"},
	"memory_pressure": {en: "Memory", ru: "Память", zh: "内存"},
	"cpu_softirq":     {en: "CPU softirq", ru: "CPU softirq", zh: "CPU softirq"},
	"kernel_headers":  {en: "Kernel headers", ru: "Заголовки ядра", zh: "内核头文件"},
	"awg_backend":     {en: "AmneziaWG module", ru: "Модуль AmneziaWG", zh: "AmneziaWG 模块"},
	"warp_path":       {en: "WARP is down", ru: "WARP не работает", zh: "WARP 异常"},
}

// whyVariants say in a few words why a check failed (the "variant" of health.alert.<kind>.why.<variant>).
var whyVariants = map[string]localizedText{
	"via_cdn":          {en: "the check goes through the CDN; check the CDN and the origin", ru: "проверка идёт через CDN; проверьте CDN и источник", zh: "检测请求经过 CDN，请检查 CDN 和源站"},
	"udp_blocked":      {en: "the hoster seems to block UDP on this port", ru: "хостер, похоже, режет UDP на этом порту", zh: "服务商似乎封锁了这个端口的 UDP"},
	"udp_all_blocked":  {en: "the hoster seems to block UDP on the whole node", ru: "хостер, похоже, режет весь UDP на ноде", zh: "服务商似乎封锁了整个节点的 UDP"},
	"timeout":          {en: "the handshake does not complete", ru: "рукопожатие не завершается", zh: "握手无法完成"},
	"auth":             {en: "the panel’s check account is refused", ru: "проверочная учётка панели отклонена", zh: "面板的检测账户被拒绝"},
	"tls":              {en: "the certificate does not match", ru: "сертификат не подходит", zh: "证书不匹配"},
	"refused":          {en: "nothing listens on the port", ru: "на порту никто не слушает", zh: "端口上没有程序在监听"},
	"exit_unreachable": {en: "the node cannot reach the internet through the tunnel", ru: "нода не выходит в интернет через туннель", zh: "节点无法通过隧道访问互联网"},
	"http_status":      {en: "the test sites answer with errors", ru: "тестовые сайты отвечают ошибками", zh: "测试网站返回错误"},
	"warp_path":        {en: "the WARP exit is dead", ru: "выход через WARP не работает", zh: "WARP 出口已失效"},
	"mixed":            {en: "the profiles fail in different ways", ru: "профили падают по-разному", zh: "各协议配置的失败原因不同"},
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
		return l.pick("Node is unreachable", "Нода недоступна", "节点无法连接")
	case "no_traffic":
		return l.pick("Alive, but no traffic", "Жива, но трафик не идёт", "在线但无流量")
	case "check_failed":
		if a.Subject == "panel_egress" {
			return l.pick("The panel cannot reach many nodes", "Панель не достаёт до многих нод", "面板无法连接多个节点")
		}
		if a.Subject == "probe_path" {
			return l.pick("Panel probes fail across several nodes", "Проверки панели не проходят на нескольких нодах", "多个节点同时检测失败")
		}
		if p["profile"] == "" {
			return l.pick("A profile fails the check", "Профиль не проходит проверку", "协议配置未通过客户端视角检测")
		}
		return l.pick("Profile “"+data(p["profile"], 40)+"” fails the check", "Профиль «"+data(p["profile"], 40)+"» не проходит проверку", "「"+data(p["profile"], 40)+"」未通过客户端视角检测")
	case "doctor_warn", "doctor_fail":
		return doctorTitle(l, p["check"])
	case "state_drift":
		return l.pick("Node state differs from the panel", "Состояние ноды разошлось с панелью", "节点状态与面板不一致")
	case "cert_expiry":
		return l.pick("Certificate is about to expire", "Сертификат скоро истечёт", "证书即将到期")
	case "update_failed":
		return l.pick("The update is paused", "Обновление остановилось", "更新已暂停")
	case "quota":
		return l.pick("A user reached the quota", "Пользователь выбрал квоту", "有用户已达流量配额")
	case "subscription_shared_suspect":
		return l.pick("A subscription looks shared", "Подписка похожа на общую", "疑似共享订阅")
	case "access_ended":
		return l.pick("Access ended while the user is still trying", "Доступ закончился, но пользователь продолжает подключаться", "访问已到期，但用户仍在尝试连接")
	case "user_connection":
		return l.pick("A user may have a connection problem", "У пользователя может быть проблема с подключением", "可能有人无法连接")
	case "users_impacted":
		return l.pick("Connections dropped for some users", "У части пользователей пропало подключение", "部分用户的连接已中断")
	case "torrent":
		return l.pick("Torrent attempts", "Попытки качать торренты", "BT 下载尝试")
	case "port_lossy":
		return l.pick("UDP packets are being lost", "Теряются UDP-пакеты", "UDP 正在丢包")
	case "device_limit":
		return l.pick("An app was turned away at the device limit", "Приложению отказали: лимит устройств", "有应用因设备数上限被拒绝")
	}
	return data(strings.ReplaceAll(a.Kind, "_", " "), 40)
}

// alertReason is one short line of why, "" when the headline says it all.
func alertReason(l L, a store.HealthAlert) string {
	p := a.Params
	switch a.Kind {
	case "node_down":
		if m := p["minutes"]; m != "" {
			return l.pick("No contact for "+data(m, 6)+" min.", "Нет связи уже "+data(m, 6)+" мин.", "已失联 "+data(m, 6)+" 分钟。")
		}
	case "no_traffic":
		line := l.pick(data(p["failed"], 4)+" of "+data(p["total"], 4)+" profiles fail the check.", data(p["failed"], 4)+" из "+data(p["total"], 4)+" профилей не проходят проверку.", data(p["total"], 4)+" 个协议配置中有 "+data(p["failed"], 4)+" 个未通过检测。")
		if v, ok := whyVariants[variantOf(a.WhyKey, "no_traffic")]; ok {
			line += l.gap() + upperFirst(v.forLanguage(l)) + l.stop()
		}
		return line
	case "check_failed":
		if a.WhyKey == "health.alert.check_failed.why.probe_path" {
			return l.pick("Probes from the panel failed on several nodes at once ("+data(p["nodes"], 200)+"), at "+data(p["providers"], 3)+
				" different hosters. Most likely the panel's own network, not the nodes. No per-node alerts and no port advice unless it lasts 20 minutes.",
				"Проверки с панели провалились сразу на нескольких нодах ("+data(p["nodes"], 200)+") у "+data(p["providers"], 3)+
					" разных хостеров. Скорее всего, сбой в сети самой панели, а не на нодах. Отдельных тревог по нодам и советов сменить порт не будет, если сбой не продлится 20 минут.",
				"面板的检测在 "+data(p["nodes"], 200)+" 上同时失败，涉及 "+data(p["providers"], 3)+" 个不同的服务商。这更可能是面板自身的网络问题，而不是节点：除非持续 20 分钟，否则不发出按节点的告警，也不建议更换端口。")
		}
		if a.Subject == "panel_egress" {
			return l.pick(data(p["failed"], 4)+" of "+data(p["total"], 4)+" checks fail across "+data(p["providers"], 3)+" providers: likely the panel’s own network.",
				data(p["failed"], 4)+" из "+data(p["total"], 4)+" проверок не проходят у "+data(p["providers"], 3)+" провайдеров: скорее всего, виновата сеть самой панели.",
				data(p["providers"], 3)+" 个服务商的 "+data(p["total"], 4)+" 次检测中有 "+data(p["failed"], 4)+" 次失败：多半是面板自身的网络问题。")
		}
		var parts []string
		if v, ok := whyVariants[variantOf(a.WhyKey, "check_failed")]; ok {
			parts = append(parts, upperFirst(v.forLanguage(l)))
		}
		if p["port"] != "" {
			parts = append(parts, l.pick("port ", "порт ", "端口 ")+data(p["port"], 6))
		}
		return strings.Join(parts, " · ")
	case "doctor_warn", "doctor_fail":
		if text, ok := doctorDetail(l, p); ok {
			return text
		}
		return l.pick("Node check: "+doctorTitle(l, p["check"]), "Проверка узла: "+doctorTitle(l, p["check"]), "节点检查："+doctorTitle(l, p["check"]))
	case "cert_expiry":
		who := data(p["profile"], 40)
		switch {
		case strings.HasSuffix(a.WhyKey, ".san_mismatch"):
			return l.pick("“"+who+"” does not match its domain "+data(p["server_name"], 80)+".", "«"+who+"» не подходит к домену "+data(p["server_name"], 80)+".", "「"+who+"」与域名 "+data(p["server_name"], 80)+" 不匹配。")
		case strings.HasSuffix(a.WhyKey, ".expired"):
			return l.pick("“"+who+"” has expired.", "«"+who+"» истёк.", "「"+who+"」的证书已过期。")
		case p["days_left"] != "":
			return l.pick("“"+who+"”: "+data(p["days_left"], 4)+" days left.", "«"+who+"»: осталось "+data(p["days_left"], 4)+" дн.", "「"+who+"」：剩余 "+data(p["days_left"], 4)+" 天。")
		}
	case "update_failed":
		return data(p["reason"], 160)
	case "access_ended":
		name := data(p["user_name"], 60)
		since, err := strconv.ParseInt(p["since"], 10, 64)
		if err != nil || since <= 0 {
			return l.pick(name+" keeps trying to connect, but the subscription has ended.", name+" продолжает подключаться, хотя подписка закончилась.", name+" 仍在尝试连接，但订阅已到期。")
		}
		opened := a.OpenedAt
		if opened.IsZero() {
			opened = a.FirstSeen
		}
		ago := l.duration(opened.Sub(time.Unix(since, 0)))
		if strings.HasSuffix(a.WhyKey, ".quota") {
			return l.pick(name+" keeps trying to connect, but the quota ran out "+ago+" ago.",
				name+" продолжает подключаться, хотя квота закончилась "+ago+" назад.",
				name+" 仍在尝试连接，但流量配额已在 "+ago+"前用完。")
		}
		return l.pick(name+" keeps trying to connect, but the subscription ended "+ago+" ago.",
			name+" продолжает подключаться, хотя подписка закончилась "+ago+" назад.",
			name+" 仍在尝试连接，但订阅已在 "+ago+"前到期。")
	case "torrent":
		return torrentReason(l, p)
	case "device_limit":
		return deviceLimitReason(l, p)
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
				" в этот час, а нода на связи. Похоже на блокировку на стороне пользователей.",
			protocol+" 的在线用户：现在 "+data(p["now"], 8)+" 人，此时段通常约 "+data(p["usual"], 8)+" 人，而节点在线。这看起来是用户侧网络封锁。")
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
		"Профиль «"+profile+"» на порту "+port+" потерял "+percent+"% UDP-пакетов при последней проверке.",
		"发往「"+profile+"」端口 "+port+" 的 UDP 测试包在最近一次检测中丢失了 "+percent+"%。")
}

// deviceLimitReason is the sentence of a DEVICE_LIMIT alert: who is at the limit and what they can do about it. The owner is
// the reader, so it says what the person can do too. Values from data go through data().
func deviceLimitReason(l L, p map[string]string) string {
	name, used, limit := data(p["user_name"], 60), data(p["used"], 6), data(p["limit"], 6)
	return l.pick(name+" is at the device limit ("+used+" of "+limit+"): an app asking for a new place was turned away. "+
		"They can remove an old device on their subscription page, or you can raise the limit.",
		"У "+name+" заняты все места для устройств ("+used+" из "+limit+"): приложению, которое просило новое место, отказали. "+
			"Можно удалить старое устройство на странице подписки или поднять лимит.",
		name+" 的设备数已达上限（"+used+" / "+limit+"）：有应用申请新名额，被拒绝了。"+
			"可以在订阅页面删除旧设备，或提高上限。")
}

// torrentEvidence say in a few words what the node's torrent guard matched (the "evidence" of a torrent_attempt event).
var torrentEvidence = map[string]localizedText{
	"tracker_connect": {en: "tracker connect request (the protocol’s magic number)", ru: "запрос подключения к трекеру (магическое число протокола)", zh: "Tracker 连接请求（协议的魔数）"},
	"dht_query":       {en: "DHT query", ru: "запрос DHT", zh: "DHT 查询"},
	"utp_syn":         {en: "uTP connection start", ru: "начало соединения uTP", zh: "uTP 连接开始"},
	"tcp_handshake":   {en: "BitTorrent handshake over TCP", ru: "рукопожатие BitTorrent по TCP", zh: "基于 TCP 的 BitTorrent 握手"},
}

// torrentReason is the sentence of a TORRENT alert: who, where, how many attempts, and the evidence and port of them when
// the node said so (an older agent does not). Values from data go through data().
func torrentReason(l L, p map[string]string) string {
	name, nodes := data(p["user_name"], 60), data(p["nodes"], 120)
	count, _ := strconv.Atoi(p["count"])
	where := ""
	if nodes != "" {
		where = l.pick(" on "+nodes, " на "+nodes, " "+nodes+" 上")
	}
	var attempts string
	switch l {
	case "ru":
		attempts = strconv.Itoa(count) + " " + ruPlural(count, "попытка", "попытки", "попыток")
	case "zh":
		attempts = strconv.Itoa(count) + " 次尝试"
	default:
		attempts = strconv.Itoa(count) + " attempt"
		if count != 1 {
			attempts += "s"
		}
	}
	line := l.pick(name+" is trying to use torrents"+where+": "+attempts+" in a day, blocked.",
		name+" пытается качать торренты"+where+": "+attempts+" за сутки, торренты заблокированы.",
		name+" 正在"+where+"尝试使用 BT 下载：一天内 "+attempts+"，均已被拦截。")
	var proof []string
	if text, ok := torrentEvidence[p["evidence"]]; ok {
		proof = append(proof, text.forLanguage(l))
	} else if p["evidence"] != "" {
		proof = append(proof, data(p["evidence"], 40))
	}
	if ports := data(p["ports"], 60); ports != "" {
		word := l.pick("port ", "порт ", "端口 ")
		if strings.Contains(ports, ",") {
			word = l.pick("ports ", "порты ", "端口 ")
		}
		proof = append(proof, word+ports)
	}
	if len(proof) == 0 {
		return line
	}
	lead := l.pick("Evidence: ", "Доказательство: ", "判定依据：")
	if p["evidence"] == "" {
		lead = ""
	}
	return line + l.gap() + upperFirst(lead+strings.Join(proof, l.comma())) + l.stop()
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
	return l.pick("Node check", "Проверка узла", "节点检查")
}

// doctorDetail words the WARP doctor rows that can open alerts. The agent's free-form detail is deliberately never a
// fallback: it can contain English, inbound ids or other node-local identifiers. Unknown codes use the check title.
func doctorDetail(l L, p map[string]string) (string, bool) {
	switch p["detail_code"] {
	case "warp_path.not_configured":
		profiles := quotedProfiles(l, p["profiles"])
		if profiles == "" {
			return l.pick("Profiles with WARP egress cannot start: the node has no WARP account",
				"Профили с выходом через WARP не стартуют: у ноды нет аккаунта WARP",
				"使用 WARP 出口的协议配置无法启动：节点没有 WARP 账户"), true
		}
		return l.pick("Profiles with WARP egress ("+profiles+") cannot start: the node has no WARP account",
			"Профили с выходом через WARP ("+profiles+") не стартуют: у ноды нет аккаунта WARP",
			"使用 WARP 出口的协议配置（"+profiles+"）无法启动：节点没有 WARP 账户"), true
	case "warp_path.host_clash":
		return l.pick("WARP cannot use this host", "WARP не может работать на этом хосте", "WARP 无法在此主机上运行"), true
	case "warp_path.check_failed":
		state := warpStateWord(l, p["state"])
		if state == "" {
			return l.pick("The latest WARP check failed", "Последняя проверка WARP не прошла", "WARP 最近一次检测失败"), true
		}
		return l.pick("WARP is "+state+", but its latest check failed", "WARP "+state+", но последняя проверка не прошла", "WARP 状态为"+state+"，但最近一次检测失败"), true
	case "warp_path.no_backend":
		return l.pick("No working WireGuard backend", "Нет рабочего бэкенда WireGuard", "没有可用的 WireGuard 后端"), true
	case "warp_path.paused_used":
		profiles := quotedProfiles(l, p["profiles"])
		if profiles == "" {
			return l.pick("WARP is paused and dependent profiles fail closed", "WARP на паузе, профили с ним не пропускают трафик", "WARP 已暂停，依赖它的协议配置会停止转发"), true
		}
		return l.pick("WARP is paused; profiles that use it fail closed: "+profiles,
			"WARP на паузе; профили с ним не пропускают трафик: "+profiles,
			"WARP 已暂停；使用它的协议配置会停止转发："+profiles), true
	case "warp_path.down":
		return l.pick("WARP is down", "WARP не работает", "WARP 异常"), true
	case "warp_path.unknown_state":
		return l.pick("Unknown WARP state", "Неизвестное состояние WARP", "未知的 WARP 状态"), true
	default:
		return "", false
	}
}

func warpStateWord(l L, state string) string {
	switch state {
	case "up":
		return l.pick("up", "работает", "正常")
	case "starting":
		return l.pick("starting", "запускается", "启动中")
	case "unavailable":
		return l.pick("unavailable", "недоступен", "不可用")
	case "disabled":
		return l.pick("paused", "на паузе", "已暂停")
	case "down":
		return l.pick("down", "не работает", "异常")
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
		switch l {
		case "ru":
			names = append(names, "«"+data(name, 60)+"»")
		case "zh":
			names = append(names, "「"+data(name, 60)+"」")
		default:
			names = append(names, "“"+data(name, 60)+"”")
		}
	}
	return strings.Join(names, l.list())
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
		sev = l.pick(" · critical", " · критично", " · 严重")
	}
	return join(
		"<b>"+alertTitle(l, a)+"</b>"+sev,
		nodeLine(l, node),
		againLine(l, a),
		alertReason(l, a),
		link(adminURL, "health", l.pick("Open Health", "Открыть «Здоровье»", "打开「运行状况」")),
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
	return l.pick("Again, first at ", "Снова, впервые в ", "再次出现，首次于 ") + first.Format(layout) + " UTC"
}

// alertResolved is the message of an alert that ended, with how long its current episode lasted: counted from
// opened_at, because a re-opened alert keeps first_seen and would include the gap between its episodes.
func alertResolved(l L, a store.HealthAlert, node string) string {
	tail := ""
	if !a.OpenedAt.IsZero() && a.ResolvedAt.After(a.OpenedAt) {
		tail = l.pick("lasted ", "длилось ", "持续了 ") + l.duration(a.ResolvedAt.Sub(a.OpenedAt))
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
	return join("<b>"+l.pick("Resolved: ", "Решено: ", "已解决：")+alertTitle(l, a)+"</b>", line, reason, againLine(l, a))
}

func nodeLine(l L, node string) string {
	if node == "" {
		return ""
	}
	return l.pick("Node: ", "Нода: ", "节点：") + "<b>" + data(node, 60) + "</b>"
}

// ---- the rest

func releaseText(l L, version, adminURL string) string {
	return join(
		"<b>"+l.pick("A new panel release is available: ", "Вышла новая версия панели: ", "面板有新版本：")+data(version, 30)+"</b>",
		l.pick("It is signed; install it from the Updates page.", "Она подписана; установить можно на странице «Обновления».", "此版本已签名，可在「更新」页面安装。"),
		link(adminURL, "updates", l.pick("Open Updates", "Открыть «Обновления»", "打开「更新」")),
	)
}

func agentsOutdatedText(l L, version string, nodes []string, adminURL string) string {
	names := make([]string, 0, 5)
	for i, n := range nodes {
		if i == 5 {
			names = append(names, l.pick("and "+strconv.Itoa(len(nodes)-5)+" more", "и ещё "+strconv.Itoa(len(nodes)-5), "另外 "+strconv.Itoa(len(nodes)-5)+" 个"))
			break
		}
		names = append(names, data(n, 40))
	}
	return join(
		"<b>"+l.pick("Node agents are behind the release ", "Агенты нод отстают от релиза ", "节点 Agent 落后于新版本 ")+data(version, 30)+"</b>",
		l.pick("Not updated: ", "Не обновлены: ", "尚未更新：")+strings.Join(names, l.list())+l.stop(),
		link(adminURL, "updates", l.pick("Open Updates", "Открыть «Обновления»", "打开「更新」")),
	)
}

var backupReasons = map[string]localizedText{
	"backup_storage_failed":        {en: "the storage did not answer", ru: "хранилище не отвечает", zh: "存储未响应"},
	"backup_storage_delete_failed": {en: "the storage refused to delete an old backup", ru: "хранилище не дало удалить старую копию", zh: "存储拒绝删除旧备份"},
	"backup_create_failed":         {en: "the archive could not be made", ru: "не удалось собрать архив", zh: "无法创建备份归档"},
	"backup_settings_invalid":      {en: "the backup settings are not valid", ru: "настройки бэкапа неверны", zh: "备份设置无效"},
}

func backupFailedText(l L, code, adminURL string) string {
	reason := data(code, 40)
	if r, ok := backupReasons[code]; ok {
		reason = r.forLanguage(l)
	}
	return join(
		"<b>"+l.pick("The panel backup failed", "Бэкап панели не удался", "面板备份失败")+"</b>",
		upperFirst(reason)+l.stop(),
		link(adminURL, "settings/backups", l.pick("Open backup settings", "Открыть настройки бэкапа", "打开备份设置")),
	)
}

func backupRecoveredText(l L) string {
	return "<b>" + l.pick("Resolved: the panel backup works again", "Решено: бэкап панели снова работает", "已解决：面板备份已恢复正常") + "</b>"
}

// toolTitles are the MCP tools that can wait for the owner (the admin screen's "approval.tool.*").
var toolTitles = map[string]localizedText{
	"user_create":                 {en: "Create a user", ru: "Создать пользователя", zh: "创建用户"},
	"user_update":                 {en: "Change a user", ru: "Изменить пользователя", zh: "更改用户"},
	"user_disable":                {en: "Disable users", ru: "Отключить пользователей", zh: "停用用户"},
	"user_enable":                 {en: "Enable users", ru: "Включить пользователей", zh: "启用用户"},
	"user_reset_traffic":          {en: "Reset users’ traffic", ru: "Сбросить трафик пользователей", zh: "重置用户流量"},
	"device_revoke":               {en: "Remove a device", ru: "Удалить устройство", zh: "移除设备"},
	"alert_mute":                  {en: "Mute an alert", ru: "Заглушить алерт", zh: "静默告警"},
	"node_fix":                    {en: "Fix a node", ru: "Исправить ноду", zh: "修复节点"},
	"rollout_start":               {en: "Update a node now", ru: "Обновить ноду сейчас", zh: "立即更新节点"},
	"rollout_pause":               {en: "Pause the rollout", ru: "Поставить раскатку на паузу", zh: "暂停滚动更新"},
	"rollout_resume":              {en: "Resume the rollout", ru: "Продолжить раскатку", zh: "继续滚动更新"},
	"rollout_cancel":              {en: "Cancel the rollout", ru: "Отменить раскатку", zh: "取消滚动更新"},
	"node_update_schedule":        {en: "Schedule a node update", ru: "Запланировать обновление ноды", zh: "定时更新节点"},
	"node_update_schedule_cancel": {en: "Cancel a scheduled node update", ru: "Отменить запланированное обновление ноды", zh: "取消节点的定时更新"},
	"update_timezone":             {en: "Change the time zone of update schedules", ru: "Сменить часовой пояс расписания обновлений", zh: "更改更新计划的时区"},
	"node_rollback":               {en: "Roll a node back", ru: "Откатить ноду", zh: "回滚节点"},
	"node_install":                {en: "Install a node over SSH", ru: "Установить ноду по SSH", zh: "通过 SSH 安装节点"},
	"node_server_password_rotate": {en: "Change a node’s SSH password", ru: "Сменить SSH-пароль ноды", zh: "更改节点的 SSH 密码"},
	"subscription_app_upsert":     {en: "Add or change an app on the user page", ru: "Добавить или изменить приложение на странице пользователя", zh: "添加或更改用户页面上的应用"},
	"subscription_app_remove":     {en: "Remove an app from the user page", ru: "Убрать приложение со страницы пользователя", zh: "移除用户页面上的应用"},
}

// planWaitingText says an agent's plan waits for the owner. It never says what the plan contains beyond the tool's title:
// the owner reads the rest on the approval screen, behind a step-up.
func planWaitingText(l L, tool, token, adminURL string) string {
	title := data(tool, 40)
	if t, ok := toolTitles[tool]; ok {
		title = t.forLanguage(l)
	}
	return join(
		"<b>"+l.pick("An agent’s plan waits for your approval", "План агента ждёт твоего решения", "AI 智能体的计划正在等待你审批")+"</b>",
		l.pick("“"+data(token, 40)+"” wants to: "+title+".", "«"+data(token, 40)+"» хочет: "+title+".", "「"+data(token, 40)+"」请求："+title+"。"),
		l.pick("It expires in 10 minutes.", "Он истечёт через 10 минут.", "10 分钟后过期。"),
		link(adminURL, "integrations", l.pick("Open Integrations → Waiting for you", "Открыть «Интеграции» → «Ждут тебя»", "打开「集成 → 待你审批」")),
	)
}

func awgPrepareFailedText(l L, node, code, adminURL string) string {
	return join(
		"<b>"+l.pick("The AmneziaWG kernel module could not be built", "Не удалось собрать модуль ядра AmneziaWG", "无法构建 AmneziaWG 内核模块")+"</b>",
		nodeLine(l, node),
		l.pick("The node keeps running the userspace version. Reason code: ", "Нода продолжает работать на userspace-версии. Код причины: ", "节点继续使用用户态版本运行。原因代码：")+data(code, 32)+l.stop(),
		link(adminURL, "health", l.pick("Open Health", "Открыть «Здоровье»", "打开「运行状况」")),
	)
}

func testText(l L, brand string) string {
	return "<b>" + l.pick("Test message", "Проверка связи", "测试消息") + "</b>\n" +
		l.pick("Alerts from "+data(brand, 40)+" will arrive in this chat.", "Алерты от «"+data(brand, 40)+"» будут приходить в этот чат.", "来自 "+data(brand, 40)+" 的告警会发送到此聊天。")
}

func linkedText(l L, name, brand string) string {
	return l.pick("Linked to "+data(brand, 40)+" as "+data(name, 60)+". Alerts are on; switch them off or unlink in the panel.",
		"Чат привязан к «"+data(brand, 40)+"» как "+data(name, 60)+". Алерты включены; выключить или отвязать можно в панели.",
		"已绑定到 "+data(brand, 40)+"，对应用户 "+data(name, 60)+"。告警已开启，可在面板中关闭或解绑。")
}

func alreadyLinkedText(l L, name, brand string) string {
	return l.pick("This chat gets alerts from "+data(brand, 40)+" for "+data(name, 60)+". Manage it in the panel.",
		"Этот чат получает алерты «"+data(brand, 40)+"» для "+data(name, 60)+". Управлять можно в панели.",
		"此聊天正在接收来自 "+data(brand, 40)+" 的告警（用户 "+data(name, 60)+"）。可在面板中管理。")
}
