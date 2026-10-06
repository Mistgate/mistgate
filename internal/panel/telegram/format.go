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
// message needs live here, in the panel language (instance.Language: "ru" or anything else = English). A message is HTML
// (<b>, <a>) and nothing else; every value that came from data (a node, profile or token name, an agent's detail, a login)
// goes through esc() and clip(): it may hold "<" or a newline, and it is never trusted to be short.
//
// Never in a message: a token, a subscription link, a password. A message is built only from the fields named below.

// L is a language: ru or en.
type L string

func (l L) pick(en, ru string) string {
	if l == "ru" {
		return ru
	}
	return en
}

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
		return l.pick("under a minute", "меньше минуты")
	}
	m := int(d.Minutes())
	min, h, day := l.pick("min", "мин"), l.pick("h", "ч"), l.pick("d", "д")
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
var checkTitles = map[string][2]string{
	"disk_space":      {"Disk space", "Место на диске"},
	"journald_size":   {"System journal size", "Размер журнала systemd"},
	"dstate_tasks":    {"Hung processes", "Зависшие процессы"},
	"time_sync":       {"Clock", "Часы"},
	"resolver":        {"Server resolver", "Резолвер сервера"},
	"ipv6":            {"IPv6", "IPv6"},
	"foreign_vpn":     {"Leftovers of other VPNs", "Следы других VPN"},
	"foreign_nft":     {"Foreign firewall rules", "Чужие правила файрвола"},
	"port_conflicts":  {"Ports in use", "Занятые порты"},
	"net_baseline":    {"Base network settings", "Базовая настройка сети"},
	"cert_expiry":     {"Certificates", "Сертификаты"},
	"memory_pressure": {"Memory", "Память"},
	"cpu_softirq":     {"CPU softirq", "CPU softirq"},
	"kernel_headers":  {"Kernel headers", "Заголовки ядра"},
	"awg_backend":     {"AmneziaWG module", "Модуль AmneziaWG"},
	"warp_path":       {"WARP is down", "WARP не работает"},
}

// whyVariants say in a few words why a check failed (the "variant" of health.alert.<kind>.why.<variant>).
var whyVariants = map[string][2]string{
	"udp_blocked":      {"the hoster seems to block UDP on this port", "хостер, похоже, режет UDP на этом порту"},
	"udp_all_blocked":  {"the hoster seems to block UDP on the whole node", "хостер, похоже, режет весь UDP на ноде"},
	"timeout":          {"the handshake does not complete", "рукопожатие не завершается"},
	"auth":             {"the panel’s check account is refused", "проверочная учётка панели отклонена"},
	"tls":              {"the certificate does not match", "сертификат не подходит"},
	"refused":          {"nothing listens on the port", "на порту никто не слушает"},
	"exit_unreachable": {"the node cannot reach the internet through the tunnel", "нода не выходит в интернет через туннель"},
	"http_status":      {"the test sites answer with errors", "тестовые сайты отвечают ошибками"},
	"warp_path":        {"the WARP exit is dead", "выход через WARP не работает"},
	"mixed":            {"the profiles fail in different ways", "профили падают по-разному"},
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
		return l.pick("Node is unreachable", "Нода недоступна")
	case "no_traffic":
		return l.pick("Alive, but no traffic", "Жива, но трафик не идёт")
	case "check_failed":
		if a.Subject == "panel_egress" {
			return l.pick("The panel cannot reach many nodes", "Панель не достаёт до многих нод")
		}
		if p["profile"] == "" {
			return l.pick("A profile fails the check", "Профиль не проходит проверку")
		}
		return l.pick("Profile “"+data(p["profile"], 40)+"” fails the check", "Профиль «"+data(p["profile"], 40)+"» не проходит проверку")
	case "doctor_warn", "doctor_fail":
		return doctorTitle(l, p["check"])
	case "state_drift":
		return l.pick("Node state differs from the panel", "Состояние ноды разошлось с панелью")
	case "cert_expiry":
		return l.pick("Certificate is about to expire", "Сертификат скоро истечёт")
	case "update_failed":
		return l.pick("The update is paused", "Обновление остановилось")
	case "quota":
		return l.pick("A user reached the quota", "Пользователь выбрал квоту")
	case "subscription_shared_suspect":
		return l.pick("A subscription looks shared", "Подписка похожа на общую")
	}
	return data(strings.ReplaceAll(a.Kind, "_", " "), 40)
}

// alertReason is one short line of why, "" when the headline says it all.
func alertReason(l L, a store.HealthAlert) string {
	p := a.Params
	switch a.Kind {
	case "node_down":
		if m := p["minutes"]; m != "" {
			return l.pick("No contact for "+data(m, 6)+" min.", "Нет связи уже "+data(m, 6)+" мин.")
		}
	case "no_traffic":
		line := l.pick(data(p["failed"], 4)+" of "+data(p["total"], 4)+" profiles fail the check.", data(p["failed"], 4)+" из "+data(p["total"], 4)+" профилей не проходят проверку.")
		if v, ok := whyVariants[variantOf(a.WhyKey, "no_traffic")]; ok {
			line += " " + upperFirst(l.pick(v[0], v[1])) + "."
		}
		return line
	case "check_failed":
		if a.Subject == "panel_egress" {
			return l.pick(data(p["failed"], 4)+" of "+data(p["total"], 4)+" checks fail across "+data(p["providers"], 3)+" providers: likely the panel’s own network.",
				data(p["failed"], 4)+" из "+data(p["total"], 4)+" проверок не проходят у "+data(p["providers"], 3)+" провайдеров: скорее всего, виновата сеть самой панели.")
		}
		var parts []string
		if v, ok := whyVariants[variantOf(a.WhyKey, "check_failed")]; ok {
			parts = append(parts, upperFirst(l.pick(v[0], v[1])))
		}
		if p["port"] != "" {
			parts = append(parts, l.pick("port ", "порт ")+data(p["port"], 6))
		}
		return strings.Join(parts, " · ")
	case "doctor_warn", "doctor_fail":
		if text, ok := doctorDetail(l, p); ok {
			return text
		}
		return l.pick("Node check: "+doctorTitle(l, p["check"]), "Проверка узла: "+doctorTitle(l, p["check"]))
	case "cert_expiry":
		who := data(p["profile"], 40)
		switch {
		case strings.HasSuffix(a.WhyKey, ".san_mismatch"):
			return l.pick("“"+who+"” does not match its domain "+data(p["server_name"], 80)+".", "«"+who+"» не подходит к домену "+data(p["server_name"], 80)+".")
		case strings.HasSuffix(a.WhyKey, ".expired"):
			return l.pick("“"+who+"” has expired.", "«"+who+"» истёк.")
		case p["days_left"] != "":
			return l.pick("“"+who+"”: "+data(p["days_left"], 4)+" days left.", "«"+who+"»: осталось "+data(p["days_left"], 4)+" дн.")
		}
	case "update_failed":
		return data(p["reason"], 160)
	}
	return ""
}

func doctorTitle(l L, check string) string {
	if title, ok := checkTitles[check]; ok {
		return l.pick(title[0], title[1])
	}
	return l.pick("Node check", "Проверка узла")
}

// doctorDetail words the WARP doctor rows that can open alerts. The agent's free-form detail is deliberately never a
// fallback: it can contain English, inbound ids or other node-local identifiers. Unknown codes use the check title.
func doctorDetail(l L, p map[string]string) (string, bool) {
	switch p["detail_code"] {
	case "warp_path.not_configured":
		profiles := quotedProfiles(l, p["profiles"])
		if profiles == "" {
			return l.pick("Profiles with WARP egress cannot start: the node has no WARP account",
				"Профили с выходом через WARP не стартуют: у ноды нет аккаунта WARP"), true
		}
		return l.pick("Profiles with WARP egress ("+profiles+") cannot start: the node has no WARP account",
			"Профили с выходом через WARP ("+profiles+") не стартуют: у ноды нет аккаунта WARP"), true
	case "warp_path.host_clash":
		return l.pick("WARP cannot use this host", "WARP не может работать на этом хосте"), true
	case "warp_path.check_failed":
		state := warpStateWord(l, p["state"])
		if state == "" {
			return l.pick("The latest WARP check failed", "Последняя проверка WARP не прошла"), true
		}
		return l.pick("WARP is "+state+", but its latest check failed", "WARP "+state+", но последняя проверка не прошла"), true
	case "warp_path.no_backend":
		return l.pick("No working WireGuard backend", "Нет рабочего бэкенда WireGuard"), true
	case "warp_path.paused_used":
		profiles := quotedProfiles(l, p["profiles"])
		if profiles == "" {
			return l.pick("WARP is paused and dependent profiles fail closed", "WARP на паузе, профили с ним не пропускают трафик"), true
		}
		return l.pick("WARP is paused; profiles that use it fail closed: "+profiles,
			"WARP на паузе; профили с ним не пропускают трафик: "+profiles), true
	case "warp_path.down":
		return l.pick("WARP is down", "WARP не работает"), true
	case "warp_path.unknown_state":
		return l.pick("Unknown WARP state", "Неизвестное состояние WARP"), true
	default:
		return "", false
	}
}

func warpStateWord(l L, state string) string {
	switch state {
	case "up":
		return l.pick("up", "работает")
	case "starting":
		return l.pick("starting", "запускается")
	case "unavailable":
		return l.pick("unavailable", "недоступен")
	case "disabled":
		return l.pick("paused", "на паузе")
	case "down":
		return l.pick("down", "не работает")
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
		sev = l.pick(" · critical", " · критично")
	}
	return join(
		"<b>"+alertTitle(l, a)+"</b>"+sev,
		nodeLine(l, node),
		alertReason(l, a),
		link(adminURL, "health", l.pick("Open Health", "Открыть «Здоровье»")),
	)
}

// alertResolved is the message of an alert that ended, with how long it lasted.
func alertResolved(l L, a store.HealthAlert, node string) string {
	tail := ""
	if !a.FirstSeen.IsZero() && a.ResolvedAt.After(a.FirstSeen) {
		tail = l.pick("lasted ", "длилось ") + l.duration(a.ResolvedAt.Sub(a.FirstSeen))
	}
	line := nodeLine(l, node)
	switch {
	case line != "" && tail != "":
		line += " · " + tail
	case tail != "":
		line = upperFirst(tail)
	}
	return join("<b>"+l.pick("Resolved: ", "Решено: ")+alertTitle(l, a)+"</b>", line)
}

func nodeLine(l L, node string) string {
	if node == "" {
		return ""
	}
	return l.pick("Node: ", "Нода: ") + "<b>" + data(node, 60) + "</b>"
}

// ---- the rest

func releaseText(l L, version, adminURL string) string {
	return join(
		"<b>"+l.pick("A new panel release is available: ", "Вышла новая версия панели: ")+data(version, 30)+"</b>",
		l.pick("It is signed; install it from the Updates page.", "Она подписана; установить можно на странице «Обновления»."),
		link(adminURL, "updates", l.pick("Open Updates", "Открыть «Обновления»")),
	)
}

func agentsOutdatedText(l L, version string, nodes []string, adminURL string) string {
	names := make([]string, 0, 5)
	for i, n := range nodes {
		if i == 5 {
			names = append(names, l.pick("and "+strconv.Itoa(len(nodes)-5)+" more", "и ещё "+strconv.Itoa(len(nodes)-5)))
			break
		}
		names = append(names, data(n, 40))
	}
	return join(
		"<b>"+l.pick("Node agents are behind the release ", "Агенты нод отстают от релиза ")+data(version, 30)+"</b>",
		l.pick("Not updated: ", "Не обновлены: ")+strings.Join(names, ", ")+".",
		link(adminURL, "updates", l.pick("Open Updates", "Открыть «Обновления»")),
	)
}

var backupReasons = map[string][2]string{
	"backup_storage_failed":        {"the storage did not answer", "хранилище не отвечает"},
	"backup_storage_delete_failed": {"the storage refused to delete an old backup", "хранилище не дало удалить старую копию"},
	"backup_create_failed":         {"the archive could not be made", "не удалось собрать архив"},
	"backup_settings_invalid":      {"the backup settings are not valid", "настройки бэкапа неверны"},
}

func backupFailedText(l L, code, adminURL string) string {
	reason := data(code, 40)
	if r, ok := backupReasons[code]; ok {
		reason = l.pick(r[0], r[1])
	}
	return join(
		"<b>"+l.pick("The panel backup failed", "Бэкап панели не удался")+"</b>",
		upperFirst(reason)+".",
		link(adminURL, "settings/backups", l.pick("Open backup settings", "Открыть настройки бэкапа")),
	)
}

func backupRecoveredText(l L) string {
	return "<b>" + l.pick("Resolved: the panel backup works again", "Решено: бэкап панели снова работает") + "</b>"
}

// toolTitles are the MCP tools that can wait for the owner (the admin screen's "approval.tool.*").
var toolTitles = map[string][2]string{
	"user_create": {"Create a user", "Создать пользователя"}, "user_update": {"Change a user", "Изменить пользователя"},
	"user_disable": {"Disable users", "Отключить пользователей"}, "user_enable": {"Enable users", "Включить пользователей"},
	"user_reset_traffic": {"Reset users’ traffic", "Сбросить трафик пользователей"}, "device_revoke": {"Remove a device", "Удалить устройство"},
	"alert_mute": {"Mute an alert", "Заглушить алерт"}, "node_fix": {"Fix a node", "Исправить ноду"},
	"rollout_start": {"Update a node now", "Обновить ноду сейчас"}, "rollout_pause": {"Pause the rollout", "Поставить раскатку на паузу"},
	"rollout_resume": {"Resume the rollout", "Продолжить раскатку"}, "rollout_cancel": {"Cancel the rollout", "Отменить раскатку"},
	"node_update_schedule":        {"Schedule a node update", "Запланировать обновление ноды"},
	"node_update_schedule_cancel": {"Cancel a scheduled node update", "Отменить запланированное обновление ноды"},
	"update_timezone":             {"Change the time zone of update schedules", "Сменить часовой пояс расписания обновлений"},
	"node_rollback":               {"Roll a node back", "Откатить ноду"}, "node_install": {"Install a node over SSH", "Установить ноду по SSH"},
	"node_server_password_rotate": {"Change a node’s SSH password", "Сменить SSH-пароль ноды"},
	"subscription_app_upsert":     {"Add or change an app on the user page", "Добавить или изменить приложение на странице пользователя"},
	"subscription_app_remove":     {"Remove an app from the user page", "Убрать приложение со страницы пользователя"},
}

// planWaitingText says an agent's plan waits for the owner. It never says what the plan contains beyond the tool's title:
// the owner reads the rest on the approval screen, behind a step-up.
func planWaitingText(l L, tool, token, adminURL string) string {
	title := data(tool, 40)
	if t, ok := toolTitles[tool]; ok {
		title = l.pick(t[0], t[1])
	}
	return join(
		"<b>"+l.pick("An agent’s plan waits for your approval", "План агента ждёт твоего решения")+"</b>",
		l.pick("“"+data(token, 40)+"” wants to: "+title+".", "«"+data(token, 40)+"» хочет: "+title+"."),
		l.pick("It expires in 10 minutes.", "Он истечёт через 10 минут."),
		link(adminURL, "integrations", l.pick("Open Integrations → Waiting for you", "Открыть «Интеграции» → «Ждут тебя»")),
	)
}

func awgPrepareFailedText(l L, node, code, adminURL string) string {
	return join(
		"<b>"+l.pick("The AmneziaWG kernel module could not be built", "Не удалось собрать модуль ядра AmneziaWG")+"</b>",
		nodeLine(l, node),
		l.pick("The node keeps running the userspace version. Reason code: ", "Нода продолжает работать на userspace-версии. Код причины: ")+data(code, 32)+".",
		link(adminURL, "health", l.pick("Open Health", "Открыть «Здоровье»")),
	)
}

func testText(l L, brand string) string {
	return "<b>" + l.pick("Test message", "Проверка связи") + "</b>\n" +
		l.pick("Alerts from "+data(brand, 40)+" will arrive in this chat.", "Алерты от «"+data(brand, 40)+"» будут приходить в этот чат.")
}

func linkedText(l L, name, brand string) string {
	return l.pick("Linked to "+data(brand, 40)+" as "+data(name, 60)+". Alerts are on; switch them off or unlink in the panel.",
		"Чат привязан к «"+data(brand, 40)+"» как "+data(name, 60)+". Алерты включены; выключить или отвязать можно в панели.")
}

func alreadyLinkedText(l L, name, brand string) string {
	return l.pick("This chat gets alerts from "+data(brand, 40)+" for "+data(name, 60)+". Manage it in the panel.",
		"Этот чат получает алерты «"+data(brand, 40)+"» для "+data(name, 60)+". Управлять можно в панели.")
}
