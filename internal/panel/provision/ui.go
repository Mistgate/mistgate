package provision

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// AdminPagePath is the exact path used by the authenticated panel wizard. The HTTP
// server also serves its slash-suffixed form without redirecting.
const AdminPagePath = "/nodes/install"

const (
	pageBodyLimit  = 32 << 10
	pageEventLimit = 100
)

type installPageData struct {
	Step        string
	Message     string
	HomeHref    string
	NodesHref   string
	Form        installForm
	Fingerprint string
	Preflight   *preflightView
	Job         *jobView
	Jobs        []jobView
	Access      []serverAccessView
	Events      []eventView
	Refresh     bool
}

type installForm struct {
	Host        string
	Port        string
	Username    string
	Name        string
	Address     string
	CountryCode string
	Location    string
	Provider    string
}

type serverAccessView struct {
	ID, Name, Host, Username, ConfiguredAt string
	Pending                                bool
}

type preflightView struct {
	Distribution   string
	Version        string
	Kernel         string
	Architecture   string
	CPUCount       uint32
	Memory         string
	Disk           string
	Systemd        bool
	PanelReachable bool
}

type jobView struct {
	ID         string
	Name       string
	Host       string
	State      string
	StateLabel string
	PhaseLabel string
	ErrorLabel string
	UpdatedAt  string
	CanCancel  bool
}

type eventView struct {
	Label     string
	CreatedAt string
}

var installPageTemplate = template.Must(template.New("node-install").Parse(`<!doctype html>
<html lang="ru">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<meta name="color-scheme" content="dark">
	{{if .Refresh}}<meta http-equiv="refresh" content="4">{{end}}
	<title>Установка ноды — Mistgate</title>
	<style>
		:root {
			color-scheme: dark;
			--bg: #0c0d10; --panel: #141519; --soft: #191b20; --line: #292b32;
			--text: #f3f4f6; --muted: #969ba8; --green: #90dfb3; --blue: #9ac8ff;
			--red: #ff7777; --yellow: #efc75e;
			font: 15px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif;
		}
		* { box-sizing: border-box }
		body { margin: 0; background: radial-gradient(ellipse at 50% -30%, #1a2022 0, transparent 55%), var(--bg); color: var(--text) }
		a { color: var(--green); text-underline-offset: 3px }
		main { width: min(100% - 32px, 900px); margin: 38px auto 72px }
		.top { display: flex; align-items: center; gap: 14px; margin-bottom: 24px }
		.back { display: grid; place-items: center; width: 38px; height: 38px; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); color: var(--text); font-size: 20px; text-decoration: none }
		.eyebrow { color: var(--muted); font-size: 12px; font-weight: 700; letter-spacing: .12em; text-transform: uppercase }
		h1 { margin: 3px 0 0; font-size: clamp(25px, 4vw, 34px); line-height: 1.15; letter-spacing: -.035em }
		h2 { margin: 0 0 6px; font-size: 19px; letter-spacing: -.02em }
		.lead { margin: 8px 0 0; color: var(--muted) }
		.card { margin: 16px 0; padding: 24px; border: 1px solid var(--line); border-radius: 19px; background: linear-gradient(145deg, #ffffff06, transparent 42%), var(--panel); box-shadow: 0 18px 50px #0003 }
		.steps { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 18px; padding: 0; list-style: none }
		.steps li { padding: 5px 10px; border: 1px solid var(--line); border-radius: 99px; color: var(--muted); font-size: 12px }
		.steps .current { border-color: #79caa080; background: #79caa013; color: var(--green) }
		.notice { margin: 0 0 16px; padding: 12px 14px; border: 1px solid #ddaa3860; border-radius: 12px; background: #ddaa3810; color: #f2d18b }
		.grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px }
		.field { display: grid; gap: 6px }
		.field.full { grid-column: 1 / -1 }
		label { color: #b8bbc4; font-size: 13px; font-weight: 600 }
		input { width: 100%; height: 44px; padding: 0 12px; border: 1px solid #343740; border-radius: 11px; outline: none; background: #0e0f13; color: var(--text); font: inherit }
		input:focus { border-color: #8bd8ac; box-shadow: 0 0 0 3px #8bd8ac22 }
		input::placeholder { color: #6f7480 }
		.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-top: 20px }
		.button { display: inline-flex; min-height: 42px; align-items: center; justify-content: center; padding: 0 16px; border: 1px solid #8bd8ac66; border-radius: 11px; background: linear-gradient(180deg, #a3edc3, #7ed9a6); color: #102018; font: inherit; font-size: 14px; font-weight: 700; text-decoration: none; cursor: pointer }
		.button.secondary { border-color: #3b3d45; background: #202228; color: var(--text) }
		.button:focus-visible, a:focus-visible { outline: 3px solid var(--blue); outline-offset: 3px }
		.key { overflow-wrap: anywhere; padding: 15px 16px; border: 1px solid #547b9d66; border-radius: 12px; background: #91c5ff0d; color: var(--blue); font: 600 14px/1.6 ui-monospace, SFMono-Regular, Consolas, monospace }
		.check { display: flex; align-items: flex-start; gap: 10px; margin-top: 15px; color: #c9cbd2; font-size: 13px }
		.check input { flex: none; width: 17px; height: 17px; margin: 2px 0 0; accent-color: var(--green) }
		.facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin: 17px 0 }
		.fact { padding: 12px; border: 1px solid var(--line); border-radius: 12px; background: var(--soft) }
		.fact b { display: block; overflow-wrap: anywhere; font-size: 14px }
		.fact span { display: block; margin-bottom: 4px; color: var(--muted); font-size: 11px; letter-spacing: .06em; text-transform: uppercase }
		.status { display: inline-flex; align-items: center; gap: 7px; padding: 5px 10px; border: 1px solid #70cc9980; border-radius: 99px; color: var(--green); font-size: 13px; font-weight: 700 }
		.status:before { width: 8px; height: 8px; border-radius: 50%; background: currentColor; content: "" }
		.status.failed { border-color: #e56d6d70; color: var(--red) }
		.status.queued { border-color: #dbb44f70; color: var(--yellow) }
		.status.running { border-color: #74b9ec70; color: var(--blue) }
		.status.cancel_requested { border-color: #dbb44f70; color: var(--yellow) }
		.status.cancelled { border-color: #858b9870; color: #b6bac3 }
		.job-head { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: space-between; gap: 16px }
		.job-meta { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 20px }
		.job-meta span { display: block; color: var(--muted); font-size: 12px }
		.job-meta b { display: block; overflow-wrap: anywhere; margin-top: 4px }
		.events { margin: 20px 0 0; padding: 0; border-top: 1px solid var(--line); list-style: none }
		.events li { display: flex; justify-content: space-between; gap: 12px; padding: 12px 2px; border-bottom: 1px solid var(--line) }
		.events span { color: #c7c9d0 }
		.events time { flex: none; color: var(--muted); font: 12px ui-monospace, SFMono-Regular, Consolas, monospace }
		.error { margin-top: 14px; color: var(--red); font-size: 13px }
		.table { width: 100%; margin-top: 12px; border-collapse: collapse }
		.table th, .table td { padding: 12px 8px; border-bottom: 1px solid var(--line); text-align: left; vertical-align: middle }
		.table th { color: var(--muted); font-size: 11px; letter-spacing: .06em; text-transform: uppercase }
		.table td { font-size: 13px }
		.mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace }
		.empty, .foot { color: var(--muted); font-size: 12px }
		.divider { height: 1px; margin: 20px 0; background: var(--line) }
		@media (max-width: 650px) {
			main { margin-top: 20px }
			.card { padding: 18px }
			.grid, .facts, .job-meta { grid-template-columns: 1fr }
			.table th:nth-child(3), .table td:nth-child(3) { display: none }
			.top { margin-bottom: 18px }
		}
	</style>
</head>
<body>
<main>
	<header class="top"><a class="back" href="{{.HomeHref}}" aria-label="Назад в панель">‹</a><div><div class="eyebrow">Mistgate · Управление нодами</div><h1>Установка ноды по SSH</h1></div></header>
	<p class="lead">Проверка сервера, подтверждение ключа SSH и установка агента Mistgate.</p>
	{{if .Message}}<div class="notice" role="alert">{{.Message}}</div>{{end}}
	{{if eq .Step "fingerprint"}}
	<section class="card">
		<ul class="steps"><li>1 · Сервер</li><li class="current">2 · Ключ SSH</li><li>3 · Проверка</li><li>4 · Установка</li></ul>
		<h2>Сверьте ключ SSH</h2><p class="lead">Mistgate не отправит пароль, пока вы не подтвердите, что этот ключ принадлежит вашему серверу.</p>
		<div class="key" aria-label="SHA-256 отпечаток SSH host key">{{.Fingerprint}}</div>
		<form method="post" action="" autocomplete="off">
			<input type="hidden" name="action" value="check"><input type="hidden" name="host" value="{{.Form.Host}}"><input type="hidden" name="port" value="{{.Form.Port}}"><input type="hidden" name="fingerprint" value="{{.Fingerprint}}">
			<div class="check"><input id="confirm-key" name="confirm_key" type="checkbox" value="yes" required><label for="confirm-key">Я сверил отпечаток с ключом моего сервера и подтверждаю его.</label></div>
			<div class="divider"></div><h2>Данные новой ноды</h2>
			<div class="grid" style="margin-top:14px">
				<div class="field"><label for="name">Имя ноды</label><input id="name" name="name" required minlength="2" maxlength="24" pattern="[a-zA-Z0-9-]+" value="{{.Form.Name}}" placeholder="edge-1"></div>
				<div class="field"><label for="address">Адрес ноды</label><input id="address" name="address" required maxlength="253" value="{{.Form.Address}}" placeholder="edge.example.com"></div>
				<div class="field"><label for="country">Код страны</label><input id="country" name="country_code" maxlength="2" pattern="[a-zA-Z]{2}" value="{{.Form.CountryCode}}" placeholder="DE"></div>
				<div class="field"><label for="location">Регион или город</label><input id="location" name="location" maxlength="100" value="{{.Form.Location}}" placeholder="Frankfurt"></div>
				<div class="field full"><label for="provider">Провайдер</label><input id="provider" name="provider" maxlength="100" value="{{.Form.Provider}}" placeholder="Название хостинга"></div>
				<div class="field"><label for="username">SSH-логин</label><input id="username" name="username" required maxlength="32" pattern="[A-Za-z_][A-Za-z0-9_.-]*" value="{{if .Form.Username}}{{.Form.Username}}{{else}}root{{end}}" autocomplete="username"></div>
				<div class="field"><label for="password">Пароль SSH</label><input id="password" name="password" type="password" required maxlength="1024" autocomplete="new-password"></div>
			</div>
			<div class="actions"><button class="button" type="submit">Проверить сервер</button><a class="button secondary" href="?">Начать заново</a></div>
		</form>
		<p class="foot">Подойдут root или отдельный SSH-пользователь с настроенным <span class="mono">sudo -n</span>. Пароль передаётся только в теле запроса и шифруется перед сохранением задания.</p>
	</section>
	{{else if eq .Step "preflight"}}
	<section class="card">
		<ul class="steps"><li>1 · Сервер</li><li>2 · Ключ SSH</li><li class="current">3 · Проверка</li><li>4 · Установка</li></ul>
		<h2>Требования выполнены</h2><p class="lead">SSH-аутентификация и предварительные проверки прошли. Для запуска установки введите пароль ещё раз.</p>
		{{with .Preflight}}<div class="facts">
			<div class="fact"><span>Система</span><b>{{.Distribution}} {{.Version}}</b></div><div class="fact"><span>Архитектура</span><b>{{.Architecture}}</b></div><div class="fact"><span>Ядро</span><b>{{.Kernel}}</b></div>
			<div class="fact"><span>Процессоры</span><b>{{.CPUCount}}</b></div><div class="fact"><span>Память</span><b>{{.Memory}}</b></div><div class="fact"><span>Свободное место</span><b>{{.Disk}}</b></div>
			<div class="fact"><span>Systemd</span><b>{{if .Systemd}}Доступен{{else}}Не найден{{end}}</b></div><div class="fact"><span>Связь с панелью</span><b>{{if .PanelReachable}}Есть{{else}}Нет{{end}}</b></div>
		</div>{{end}}
		<div class="divider"></div><h2>Подтверждение установки</h2>
		<form method="post" action="" autocomplete="off">
			<input type="hidden" name="action" value="start"><input type="hidden" name="host" value="{{.Form.Host}}"><input type="hidden" name="port" value="{{.Form.Port}}"><input type="hidden" name="username" value="{{if .Form.Username}}{{.Form.Username}}{{else}}root{{end}}"><input type="hidden" name="fingerprint" value="{{.Fingerprint}}">
			<input type="hidden" name="name" value="{{.Form.Name}}"><input type="hidden" name="address" value="{{.Form.Address}}"><input type="hidden" name="country_code" value="{{.Form.CountryCode}}"><input type="hidden" name="location" value="{{.Form.Location}}"><input type="hidden" name="provider" value="{{.Form.Provider}}">
			<div class="field"><label for="password">Пароль SSH пользователя {{if .Form.Username}}{{.Form.Username}}{{else}}root{{end}}</label><input id="password" name="password" type="password" required maxlength="1024" autocomplete="new-password"></div>
			<div class="check"><input id="confirm-key" name="confirm_key" type="checkbox" value="yes" required><label for="confirm-key">Подтверждаю отпечаток <span class="mono">{{.Fingerprint}}</span>.</label></div>
			<div class="check"><input id="confirm-install" name="confirm_install" type="checkbox" value="yes" required><label for="confirm-install">Установить агент Mistgate и создать новую ноду. Существующая система не будет переустановлена.</label></div>
			<div class="actions"><button class="button" type="submit">Запустить установку</button><a class="button secondary" href="?">Отмена</a></div>
		</form>
	</section>
	{{else if eq .Step "job"}}
	{{with .Job}}<section class="card">
		<div class="job-head"><div><div class="eyebrow">Задание установки</div><h2 style="margin-top:4px">{{.Name}}</h2></div><span class="status {{.State}}">{{.StateLabel}}</span></div>
		<div class="job-meta"><div><span>SSH-сервер</span><b class="mono">{{.Host}}</b></div><div><span>Этап</span><b>{{.PhaseLabel}}</b></div><div><span>Обновлено</span><b>{{.UpdatedAt}}</b></div></div>
		{{if .ErrorLabel}}<p class="error">{{.ErrorLabel}}</p>{{end}}
		{{if .CanCancel}}<div class="divider"></div><form method="post" action="" autocomplete="off">
			<input type="hidden" name="action" value="cancel"><input type="hidden" name="job_id" value="{{.ID}}">
			<div class="check"><input id="confirm-cancel" name="confirm_cancel" type="checkbox" value="yes" required><label for="confirm-cancel">Отменить установку. Если SSH-команды уже выполняются, сервер может быть изменён частично.</label></div>
			<div class="actions"><button class="button secondary" type="submit">Отменить установку</button><a class="button secondary" href="?">К списку заданий</a></div>
		</form>{{else if eq .State "cancel_requested"}}<p class="notice">Остановка запрошена. Панель завершит задание и удалит временные SSH-данные.</p>{{end}}
		{{end}}
		{{if .Events}}<ul class="events">{{range .Events}}<li><span>{{.Label}}</span><time>{{.CreatedAt}}</time></li>{{end}}</ul>{{else}}<p class="empty">События появятся после запуска задания.</p>{{end}}
		{{if and .Job (eq .Job.State "failed")}}<div class="divider"></div><h2>Повторить установку</h2><p class="lead">Проверьте причину ошибки. Для повтора требуется пароль root.</p>
		<form method="post" action="" autocomplete="off"><input type="hidden" name="action" value="retry"><input type="hidden" name="job_id" value="{{.Job.ID}}">
			<div class="grid" style="margin-top:14px"><div class="field"><label for="retry-username">SSH-логин</label><input id="retry-username" name="username" required maxlength="32" value="root" autocomplete="username"></div><div class="field"><label for="retry-password">Пароль SSH</label><input id="retry-password" name="password" type="password" required maxlength="1024" autocomplete="new-password"></div></div>
			<div class="check"><input id="confirm-retry" name="confirm_install" type="checkbox" value="yes" required><label for="confirm-retry">Повторно выполнить установку для этой ноды.</label></div>
			<div class="actions"><button class="button" type="submit">Повторить</button><a class="button secondary" href="?">К списку заданий</a></div>
		</form>{{end}}
		{{if and .Job (eq .Job.State "completed")}}<div class="actions"><a class="button secondary" href="{{.NodesHref}}">Открыть ноды</a><a class="button secondary" href="?">К списку заданий</a></div>{{end}}
	</section>
	{{if .Refresh}}<p class="foot">Статус обновляется автоматически каждые 4 секунды. <a href="?">Открыть список заданий</a></p>{{else}}<p class="foot"><a href="?">Вернуться к заданиям</a></p>{{end}}
	{{else}}
	<section class="card">
		<ul class="steps"><li class="current">1 · Сервер</li><li>2 · Ключ SSH</li><li>3 · Проверка</li><li>4 · Установка</li></ul>
		<h2>Новая нода</h2><p class="lead">Мастер установит Mistgate Agent на Ubuntu 22.04+ или Debian 12+ с systemd. Можно войти как root или как SSH-пользователь с passwordless sudo.</p>
		<form method="post" action="" autocomplete="off"><input type="hidden" name="action" value="fingerprint">
			<div class="grid" style="margin-top:18px"><div class="field"><label for="host">Адрес SSH-сервера</label><input id="host" name="host" required maxlength="253" value="{{.Form.Host}}" placeholder="node.example.com или публичный IP" autocomplete="off"></div><div class="field"><label for="port">Порт SSH</label><input id="port" name="port" type="number" min="1" max="65535" value="{{if .Form.Port}}{{.Form.Port}}{{else}}22{{end}}" required></div></div>
			<div class="actions"><button class="button" type="submit">Начать проверку</button></div>
		</form>
		<p class="foot">Пароль не запрашивается до подтверждения отпечатка SSH host key.</p>
	</section>
	{{end}}
	{{if and (not .Job) .Jobs}}<section class="card"><h2>Последние задания</h2><div style="overflow-x:auto"><table class="table"><thead><tr><th>Нода</th><th>Состояние</th><th>SSH-сервер</th><th></th></tr></thead><tbody>{{range .Jobs}}<tr><td>{{.Name}}</td><td><span class="status {{.State}}">{{.StateLabel}}</span></td><td class="mono">{{.Host}}</td><td><a href="?job={{.ID}}">Открыть</a></td></tr>{{end}}</tbody></table></div></section>{{end}}
	{{if and (not .Job) .Access}}<section class="card"><h2>Доступ к серверам</h2><p class="lead">Пароли хранятся в зашифрованном виде. При смене панель сначала проверит новый вход и только затем заменит сохранённый пароль.</p><div style="overflow-x:auto"><table class="table"><thead><tr><th>Нода</th><th>SSH</th><th>Подключение</th><th>Сменить пароль</th></tr></thead><tbody>{{range .Access}}<tr><td>{{.Name}}{{if .Pending}}<div class="error">Смена ожидает проверки; повторите её для восстановления.</div>{{end}}</td><td class="mono">{{.Host}}</td><td class="mono">{{.Username}}</td><td><form method="post" action="" autocomplete="off"><input type="hidden" name="action" value="rotate_password"><input type="hidden" name="node_id" value="{{.ID}}"><input name="new_password" type="password" required minlength="12" maxlength="1024" autocomplete="new-password" aria-label="Новый пароль SSH"><label class="check"><input name="confirm_rotation" type="checkbox" value="yes" required><span>Сменить пароль пользователя {{.Username}}</span></label><button class="button secondary" type="submit">Сменить</button></form></td></tr>{{end}}</tbody></table></div></section>{{end}}
	<p class="foot">Изменения на сервере начинаются только после успешной проверки требований, подтверждения ключа и нажатия «Запустить установку».</p>
</main>
</body>
</html>`))

// PageHandler serves the Go-rendered SSH installation wizard. The HTTP server wraps it
// in the same owner-only session and cross-origin protections as the admin API.
func (s *Service) PageHandler() http.Handler {
	return http.HandlerFunc(s.servePage)
}

func (s *Service) servePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.AdminFrom(r.Context()); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.getPage(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := parseInstallForm(w, r); err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Форма повреждена или слишком велика. Начните проверку заново."})
		return
	}
	s.postPage(w, r)
}

func parseInstallForm(w http.ResponseWriter, r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return errors.New("unsupported form encoding")
	}
	r.Body = http.MaxBytesReader(w, r.Body, pageBodyLimit)
	return r.ParseForm()
}

func (s *Service) getPage(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	if jobID != "" {
		job, err := s.GetNodeProvision(r.Context(), connect.NewRequest(&adminv1.GetNodeProvisionRequest{JobId: jobID}))
		if err != nil {
			status := http.StatusInternalServerError
			message := "Не удалось загрузить задание. Попробуйте ещё раз."
			if connect.CodeOf(err) == connect.CodeNotFound {
				status, message = http.StatusNotFound, "Задание не найдено. Возможно, оно уже удалено."
			}
			s.renderForRequest(w, r, status, installPageData{Step: "host", Message: message})
			return
		}
		events, err := s.ListNodeProvisionEvents(r.Context(), connect.NewRequest(&adminv1.ListNodeProvisionEventsRequest{
			JobId: jobID, Limit: pageEventLimit,
		}))
		if err != nil {
			s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить события задания."})
			return
		}
		view := makeJobView(job.Msg.Job)
		page := installPageData{Step: "job", Job: &view, Refresh: view.State == "queued" || view.State == "running" || view.State == "cancel_requested"}
		if r.URL.Query().Get("cancelled") == "1" {
			if view.State == "cancelled" && job.Msg.Job.ErrorCode == "cancelled_before_start" {
				page.Message = "Задание отменено до подключения к серверу; временные SSH-данные удалены."
			} else {
				page.Message = "Установка остановлена. Проверьте сервер: часть SSH-команд могла уже выполниться."
			}
		}
		for _, event := range events.Msg.Events {
			page.Events = append(page.Events, makeEventView(event))
		}
		s.renderForRequest(w, r, http.StatusOK, page)
		return
	}
	jobs, err := s.ListNodeProvisions(r.Context(), connect.NewRequest(&adminv1.ListNodeProvisionsRequest{}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить задания установки."})
		return
	}
	page := installPageData{Step: "host"}
	if r.URL.Query().Get("password_rotated") == "1" {
		page.Message = "Пароль изменён, новый SSH-вход проверен."
	}
	for _, job := range jobs.Msg.Jobs {
		page.Jobs = append(page.Jobs, makeJobView(job))
	}
	access, err := s.ListNodeServerAccess(r.Context(), connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить список SSH-доступов."})
		return
	}
	for _, item := range access.Msg.Access {
		page.Access = append(page.Access, serverAccessView{
			ID: item.NodeId, Name: item.NodeName, Host: net.JoinHostPort(item.Host, strconv.FormatUint(uint64(item.Port), 10)),
			Username: item.Username, ConfiguredAt: time.Unix(item.ConfiguredUnix, 0).Local().Format("2006-01-02 15:04"),
			Pending: item.RotationPending,
		})
	}
	s.renderForRequest(w, r, http.StatusOK, page)
}

func (s *Service) postPage(w http.ResponseWriter, r *http.Request) {
	values := r.PostForm
	form := formValues(values)
	switch values.Get("action") {
	case "fingerprint":
		host, port, ok := formTarget(form)
		if !ok {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Укажите корректный адрес и порт SSH.", Form: form})
			return
		}
		result, err := s.GetSSHFingerprint(r.Context(), connect.NewRequest(&adminv1.GetSSHFingerprintRequest{Host: host, Port: port}))
		if err != nil {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
				Step: "host", Message: userError(err, "Не удалось получить SSH-отпечаток. Проверьте публичный адрес и доступность порта."), Form: form,
			})
			return
		}
		form.Host, form.Port = result.Msg.Host, strconv.FormatUint(uint64(result.Msg.Port), 10)
		s.renderForRequest(w, r, http.StatusOK, installPageData{Step: "fingerprint", Form: form, Fingerprint: result.Msg.Fingerprint})
	case "check":
		if !confirmed(values.Get("confirm_key")) {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Сначала получите и подтвердите отпечаток SSH host key."})
			return
		}
		host, port, ok := formTarget(form)
		fingerprint := values.Get("fingerprint")
		if !ok || !validFingerprint(fingerprint) {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Данные SSH-сервера недействительны. Начните проверку заново."})
			return
		}
		facts, err := s.checkSSH(r.Context(), host, port, fingerprint, form.Username, values.Get("password"))
		if err != nil {
			s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
				Step: "fingerprint", Form: form, Fingerprint: fingerprint,
				Message: userError(err, "SSH-проверка не прошла. Проверьте пароль, ключ сервера и требования к системе."),
			})
			return
		}
		s.renderForRequest(w, r, http.StatusOK, installPageData{Step: "preflight", Form: form, Fingerprint: fingerprint, Preflight: makePreflightView(facts)})
	case "start":
		s.startPageJob(w, r, values, form)
	case "retry":
		s.retryPageJob(w, r, values)
	case "cancel":
		s.cancelPageJob(w, r, values)
	case "rotate_password":
		s.rotatePagePassword(w, r, values)
	default:
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Неизвестное действие. Начните установку заново."})
	}
}

func (s *Service) cancelPageJob(w http.ResponseWriter, r *http.Request, values url.Values) {
	jobID := strings.TrimSpace(values.Get("job_id"))
	if len(jobID) < 5 || len(jobID) > 64 || !confirmed(values.Get("confirm_cancel")) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Выберите задание и подтвердите отмену."})
		return
	}
	if _, err := s.CancelNodeProvision(r.Context(), jobID); err != nil {
		status := http.StatusBadRequest
		if connect.CodeOf(err) == connect.CodeNotFound {
			status = http.StatusNotFound
		}
		s.renderForRequest(w, r, status, installPageData{Step: "host", Message: userError(err, "Не удалось отменить установку. Обновите страницу и проверьте состояние задания.")})
		return
	}
	query := url.Values{"job": []string{jobID}, "cancelled": []string{"1"}}
	w.Header().Set("Location", "?"+query.Encode())
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Service) startPageJob(w http.ResponseWriter, r *http.Request, values url.Values, form installForm) {
	if !confirmed(values.Get("confirm_key")) || !confirmed(values.Get("confirm_install")) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Перед запуском подтвердите ключ сервера и установку."})
		return
	}
	host, port, ok := formTarget(form)
	fingerprint := values.Get("fingerprint")
	if !ok || !validFingerprint(fingerprint) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Данные SSH-сервера недействительны. Начните проверку заново."})
		return
	}
	password := values.Get("password")
	facts, err := s.checkSSH(r.Context(), host, port, fingerprint, form.Username, password)
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
			Step: "fingerprint", Form: form, Fingerprint: fingerprint,
			Message: userError(err, "Повторная SSH-проверка не прошла. Проверьте пароль и попробуйте снова."),
		})
		return
	}
	result, err := s.StartNodeProvision(r.Context(), connect.NewRequest(&adminv1.StartNodeProvisionRequest{
		ConfirmInstall: true, Name: form.Name, Address: form.Address, CountryCode: form.CountryCode,
		Location: form.Location, Provider: form.Provider, SshHost: host, SshPort: port, Fingerprint: fingerprint, Password: password, SshUsername: form.Username,
	}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
			Step: "preflight", Form: form, Fingerprint: fingerprint, Preflight: makePreflightView(facts),
			Message: userError(err, "Не удалось создать задание. Проверьте имя ноды и повторите действие."),
		})
		return
	}
	redirectToJob(w, result.Msg.Job.Id)
}

func (s *Service) retryPageJob(w http.ResponseWriter, r *http.Request, values url.Values) {
	jobID := strings.TrimSpace(values.Get("job_id"))
	if len(jobID) > 64 || jobID == "" || !confirmed(values.Get("confirm_install")) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Для повтора выберите задание и подтвердите запуск."})
		return
	}
	result, err := s.RetryNodeProvision(r.Context(), connect.NewRequest(&adminv1.RetryNodeProvisionRequest{
		JobId: jobID, ConfirmInstall: true, Password: values.Get("password"), SshUsername: sshUsername(values.Get("username")),
	}))
	if err != nil {
		s.getPageWithMessage(w, r, jobID, userError(err, "Не удалось повторить установку. Проверьте пароль и состояние задания."))
		return
	}
	redirectToJob(w, result.Msg.Job.Id)
}

func (s *Service) getPageWithMessage(w http.ResponseWriter, r *http.Request, jobID, message string) {
	job, err := s.GetNodeProvision(r.Context(), connect.NewRequest(&adminv1.GetNodeProvisionRequest{JobId: jobID}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: message})
		return
	}
	events, err := s.ListNodeProvisionEvents(r.Context(), connect.NewRequest(&adminv1.ListNodeProvisionEventsRequest{
		JobId: jobID, Limit: pageEventLimit,
	}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusInternalServerError, installPageData{Step: "host", Message: "Не удалось загрузить события задания."})
		return
	}
	view := makeJobView(job.Msg.Job)
	page := installPageData{Step: "job", Job: &view, Message: message, Refresh: view.State == "queued" || view.State == "running"}
	for _, event := range events.Msg.Events {
		page.Events = append(page.Events, makeEventView(event))
	}
	s.renderForRequest(w, r, http.StatusBadRequest, page)
}

func (s *Service) checkSSH(ctx context.Context, host string, port uint32, fingerprint, username, password string) (*adminv1.NodePreflight, error) {
	result, err := s.CheckSSH(ctx, connect.NewRequest(&adminv1.CheckSSHRequest{
		Host: host, Port: port, Fingerprint: fingerprint, Password: password, Username: sshUsername(username),
	}))
	if err != nil {
		return nil, err
	}
	return result.Msg.Preflight, nil
}

func (s *Service) rotatePagePassword(w http.ResponseWriter, r *http.Request, values url.Values) {
	nodeID := strings.TrimSpace(values.Get("node_id"))
	password := values.Get("new_password")
	if !confirmed(values.Get("confirm_rotation")) || nodeID == "" || len(nodeID) > 64 || len(password) < 12 || !validPassword(password) {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{Step: "host", Message: "Подтвердите смену пароля и укажите новый пароль не короче 12 символов."})
		return
	}
	_, err := s.RotateNodeServerPassword(r.Context(), connect.NewRequest(&adminv1.RotateNodeServerPasswordRequest{
		NodeId: nodeID, NewPassword: password, Confirm: true,
	}))
	if err != nil {
		s.renderForRequest(w, r, http.StatusBadRequest, installPageData{
			Step: "host", Message: userError(err, "Не удалось подтвердить смену пароля. Доступ сохранён для восстановления."),
		})
		return
	}
	http.Redirect(w, r, "?password_rotated=1", http.StatusSeeOther)
}

func redirectToJob(w http.ResponseWriter, jobID string) {
	query := url.Values{"job": []string{jobID}}
	w.Header().Set("Location", "?"+query.Encode())
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Service) renderForRequest(w http.ResponseWriter, r *http.Request, status int, page installPageData) {
	page.HomeHref, page.NodesHref = pageLinks(r.URL.Path)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := installPageTemplate.Execute(w, page); err != nil {
		if s.cfg.Log != nil {
			s.cfg.Log.Error("render node provisioning page", "err", err)
		}
	}
}

func pageLinks(path string) (home, nodes string) {
	up := "../"
	if strings.HasSuffix(path, "/") {
		up = "../../"
	}
	return up, up + "nodes"
}

func formValues(values url.Values) installForm {
	return installForm{
		Host: strings.TrimSpace(values.Get("host")), Port: strings.TrimSpace(values.Get("port")),
		Username: sshUsername(strings.TrimSpace(values.Get("username"))),
		Name:     strings.TrimSpace(values.Get("name")), Address: strings.TrimSpace(values.Get("address")),
		CountryCode: strings.TrimSpace(values.Get("country_code")), Location: strings.TrimSpace(values.Get("location")),
		Provider: strings.TrimSpace(values.Get("provider")),
	}
}

func formTarget(form installForm) (string, uint32, bool) {
	if form.Host == "" {
		return "", 0, false
	}
	port := uint64(22)
	if form.Port != "" {
		parsed, err := strconv.ParseUint(form.Port, 10, 16)
		if err != nil || parsed == 0 {
			return "", 0, false
		}
		port = parsed
	}
	return form.Host, uint32(port), true
}

func confirmed(value string) bool { return value == "yes" }

func makePreflightView(facts *adminv1.NodePreflight) *preflightView {
	if facts == nil {
		return nil
	}
	return &preflightView{
		Distribution: facts.Distribution, Version: facts.Version, Kernel: facts.Kernel, Architecture: facts.Architecture,
		CPUCount: facts.CpuCount, Memory: formatBytes(facts.MemoryBytes), Disk: formatBytes(facts.DiskAvailableBytes),
		Systemd: facts.Systemd, PanelReachable: facts.PanelReachable,
	}
}

func makeJobView(job *adminv1.NodeProvisionJob) jobView {
	if job == nil {
		return jobView{}
	}
	updated := time.Unix(job.UpdatedUnix, 0).Local().Format("2006-01-02 15:04:05")
	return jobView{
		ID: job.Id, Name: job.Name, Host: net.JoinHostPort(job.SshHost, strconv.FormatUint(uint64(job.SshPort), 10)),
		State: job.State, StateLabel: stateLabel(job.State), PhaseLabel: phaseLabel(job.Phase),
		ErrorLabel: errorLabel(job.ErrorCode), UpdatedAt: updated, CanCancel: job.State == "queued" || job.State == "running",
	}
}

func makeEventView(event *adminv1.NodeProvisionEvent) eventView {
	if event == nil {
		return eventView{}
	}
	return eventView{
		Label:     eventLabel(event.Phase, event.Code),
		CreatedAt: time.Unix(event.CreatedUnix, 0).Local().Format("15:04:05"),
	}
}

func stateLabel(state string) string {
	switch state {
	case "queued":
		return "В очереди"
	case "running":
		return "Установка"
	case "cancel_requested":
		return "Остановка"
	case "cancelled":
		return "Отменено"
	case "completed":
		return "Нода подключена"
	case "failed":
		return "Ошибка"
	default:
		return "Состояние неизвестно"
	}
}

func phaseLabel(phase string) string {
	switch phase {
	case "queued":
		return "Ожидает запуска"
	case "preflight":
		return "Проверка сервера"
	case "firewall":
		return "Настройка firewall сервера"
	case "transfer":
		return "Передача агента"
	case "enrollment":
		return "Регистрация ноды"
	case "install":
		return "Установка службы"
	case "waiting_node":
		return "Ожидание подключения"
	case "completed":
		return "Завершено"
	case "cancelling":
		return "Остановка SSH-задания"
	case "cancelled":
		return "Отменено"
	case "failed":
		return "Завершилось с ошибкой"
	default:
		return "Подготовка"
	}
}

func eventLabel(phase, code string) string {
	switch code {
	case "queued":
		return "Задание поставлено в очередь"
	case "retry_requested":
		return "Запрошен повтор установки"
	case "cancel_requested":
		return "Владелец запросил отмену установки"
	case "cancelled_before_start":
		return "Задание отменено до подключения к серверу"
	case "remote_outcome_unknown":
		return "SSH-работа остановлена; состояние сервера нужно проверить"
	case "checking_host":
		return "Проверка сервера и SSH-ключа"
	case "preparing_host_firewall":
		return "Настройка активного firewall сервера"
	case "uploading_agent":
		return "Передача агента на сервер"
	case "enrolling_node":
		return "Регистрация ноды в панели"
	case "starting_agent":
		return "Настройка и запуск systemd-службы"
	case "waiting_for_agent":
		return "Ожидание подключения агента"
	case "agent_connected":
		return "Нода подключилась к панели"
	default:
		if code == "" {
			return phaseLabel(phase)
		}
		return phaseLabel(phase) + " · " + strings.ReplaceAll(code, "_", " ")
	}
}

func errorLabel(code string) string {
	switch code {
	case "remote_outcome_unknown":
		return "SSH-задание остановлено. Проверьте сервер: часть команд установки могла уже выполниться."
	case "ssh_target_invalid", "ssh_target_not_public":
		return "Укажите публичный адрес сервера, доступный по SSH."
	case "ssh_host_key_changed":
		return "Ключ SSH изменился после подтверждения. Установка остановлена."
	case "ssh_authentication_failed":
		return "Сервер отклонил пароль root. Проверьте пароль и повторите установку."
	case "ssh_connection_timeout", "ssh_preflight_timeout":
		return "Сервер не ответил вовремя. Проверьте сеть и порт SSH."
	case "unsupported_os", "unsupported_os_version":
		return "Поддерживаются Ubuntu 22.04+ и Debian 12+."
	case "unsupported_architecture":
		return "Поддерживаются архитектуры amd64 и arm64."
	case "systemd_required":
		return "На сервере не найден systemd."
	case "panel_unreachable":
		return "Сервер не может подключиться к панели. Проверьте адрес панели и исходящий доступ."
	case "insufficient_resources":
		return "На сервере недостаточно процессорных ресурсов или памяти."
	case "insufficient_disk_space":
		return "На сервере недостаточно свободного места для агента."
	case "node_not_connected":
		return "Агент установлен, но не подключился к панели вовремя."
	case "ssh_connection_failed", "ssh_connection_canceled":
		return "SSH-подключение прервано. Проверьте сервер и повторите установку."
	case "ssh_connection_refused", "ssh_connection_unavailable":
		return "Не удалось подключиться к SSH-серверу. Проверьте адрес и порт."
	case "ssh_preflight_failed", "ssh_preflight_canceled":
		return "Не удалось завершить предварительную проверку сервера."
	case "node_identity_mismatch", "node_identity_unreadable":
		return "На сервере обнаружена другая или повреждённая регистрация Mistgate."
	case "node_state_unavailable", "credentials_unavailable", "job_state_unavailable":
		return "Панели не удалось безопасно продолжить это задание. Проверьте журналы панели."
	case "node_retired":
		return "Эта нода была выведена из эксплуатации и не может быть зарегистрирована повторно."
	case "host_firewall_configuration_failed":
		return "Не удалось настроить активный firewall на сервере. Проверьте права SSH-пользователя и правила UFW/firewalld, затем повторите установку. Firewall хостера настраивается отдельно."
	case "agent_bundle_unavailable":
		return "В доверенном наборе релиза не найден агент для этой архитектуры."
	case "agent_transfer_failed":
		return "Не удалось передать агент на сервер."
	case "node_enrollment_failed", "node_enrollment_unavailable", "panel_ca_unavailable":
		return "Не удалось безопасно зарегистрировать ноду в панели."
	case "node_name_taken", "name_taken":
		return "Имя уже используется. Укажите другое имя ноды."
	case "systemd_install_failed", "agent_install_failed":
		return "Не удалось установить или запустить службу агента."
	case "":
		return ""
	default:
		return "Установка остановлена: " + strings.ReplaceAll(code, "_", " ")
	}
}

func userError(err error, fallback string) string {
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied:
		return "Требуется свежая проверка passkey. Войдите в панель и повторите действие."
	case connect.CodeUnauthenticated:
		return "Сессия панели завершилась. Войдите снова и повторите действие."
	case connect.CodeAlreadyExists:
		return "Имя ноды уже используется. Выберите другое имя."
	case connect.CodeDeadlineExceeded:
		return "Панель не получила ответ от SSH-сервера вовремя. Проверьте порт и firewall сервера или хостера, разрешив TCP-подключение с сервера панели."
	case connect.CodeFailedPrecondition:
		return "Сервер не подходит для установки или настройка панели не завершена. Проверьте требования к системе."
	case connect.CodeInvalidArgument:
		return "Проверьте адрес, порт, пароль и данные новой ноды."
	default:
		return fallback
	}
}

func formatBytes(value uint64) string {
	const unit = uint64(1024)
	if value < unit {
		return fmt.Sprintf("%d Б", value)
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	units := [...]string{"КБ", "МБ", "ГБ", "ТБ", "ПБ"}
	return fmt.Sprintf("%.1f %s", float64(value)/float64(div), units[exp])
}
