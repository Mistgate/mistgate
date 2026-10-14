package provision

import (
	"io"
	"strconv"
	"strings"
)

// installPageStyle is the style sheet of the install page, from <style> to </style>.
const installPageStyle = `		:root {
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
`

// renderInstallPage writes the install page of d in language l. The page's words are written in Russian and pass through
// tr, which gives the English ones (pageEnglish) on an English page; values from d (names, hosts, fingerprints) never
// pass through tr. Every value is escaped for the place it lands in: esc for text and quoted attributes, href for a
// link's whole address, query for a value inside a query string.
//
// The page was an html/template; it is Go code now because text/template looks methods up by name, and a program that
// can do that cannot drop the exported methods nothing calls (the linker keeps them all): with the template out,
// panel.wasm is 7.5 MB smaller. The output is the same, byte for byte.
func renderInstallPage(out io.Writer, l pageLang, d installPageData) error {
	var b strings.Builder
	w := func(s string) { b.WriteString(s) }
	tr := func(s string) string { return esc(l.tr(s)) }
	w(`<!doctype html>
<html lang="` + esc(string(d.Lang)) + `">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<meta name="color-scheme" content="dark">
	`)
	if d.Refresh {
		w(`<meta http-equiv="refresh" content="4">`)
	}
	w(`
	<title>` + tr("Установка ноды — Mistgate") + `</title>
	<style>
` + installPageStyle + `	</style>
</head>
<body>
<main>
	<header class="top"><a class="back" href="` + href(d.HomeHref) + `" aria-label="` + tr("Назад в панель") + `">‹</a><div><div class="eyebrow">` + tr("Mistgate · Управление нодами") + `</div><h1>` + tr("Установка ноды по SSH") + `</h1></div></header>
	<p class="lead">` + tr("Проверка сервера, подтверждение ключа SSH и установка агента Mistgate.") + `</p>
	`)
	if d.Message != "" {
		w(`<div class="notice" role="alert">` + tr(d.Message) + `</div>`)
	}
	w(`
	`)
	if d.Step == "fingerprint" {
		w(`
	<section class="card">
		<ul class="steps"><li>` + tr("1 · Сервер") + `</li><li class="current">` + tr("2 · Ключ SSH") + `</li><li>` + tr("3 · Проверка") + `</li><li>` + tr("4 · Установка") + `</li></ul>
		<h2>` + tr("Сверьте ключ SSH") + `</h2><p class="lead">` + tr("Mistgate не отправит пароль, пока вы не подтвердите, что этот ключ принадлежит вашему серверу.") + `</p>
		<div class="key" aria-label="` + tr("SHA-256 отпечаток SSH host key") + `">` + esc(d.Fingerprint) + `</div>
		<form method="post" action="" autocomplete="off">
			<input type="hidden" name="action" value="check"><input type="hidden" name="host" value="` + esc(d.Form.Host) + `"><input type="hidden" name="port" value="` + esc(d.Form.Port) + `"><input type="hidden" name="fingerprint" value="` + esc(d.Fingerprint) + `">
			<div class="check"><input id="confirm-key" name="confirm_key" type="checkbox" value="yes" required><label for="confirm-key">` + tr("Я сверил отпечаток с ключом моего сервера и подтверждаю его.") + `</label></div>
			<div class="divider"></div><h2>` + tr("Данные новой ноды") + `</h2>
			<div class="grid" style="margin-top:14px">
				<div class="field"><label for="name">` + tr("Имя ноды") + `</label><input id="name" name="name" required minlength="2" maxlength="24" pattern="[a-zA-Z0-9-]+" value="` + esc(d.Form.Name) + `" placeholder="edge-1"></div>
				<div class="field"><label for="address">` + tr("Адрес ноды") + `</label><input id="address" name="address" required maxlength="253" value="` + esc(d.Form.Address) + `" placeholder="edge.example.com"></div>
				<div class="field"><label for="country">` + tr("Код страны") + `</label><input id="country" name="country_code" maxlength="2" pattern="[a-zA-Z]{2}" value="` + esc(d.Form.CountryCode) + `" placeholder="DE"></div>
				<div class="field"><label for="location">` + tr("Регион или город") + `</label><input id="location" name="location" maxlength="100" value="` + esc(d.Form.Location) + `" placeholder="Frankfurt"></div>
				<div class="field full"><label for="provider">` + tr("Провайдер") + `</label><input id="provider" name="provider" maxlength="100" value="` + esc(d.Form.Provider) + `" placeholder="` + tr("Название хостинга") + `"></div>
				<div class="field"><label for="username">` + tr("SSH-логин") + `</label><input id="username" name="username" required maxlength="32" pattern="[A-Za-z_][A-Za-z0-9_.-]*" value="`)
		if d.Form.Username != "" {
			w(esc(d.Form.Username))
		} else {
			w(`root`)
		}
		w(`" autocomplete="username"></div>
				<div class="field"><label for="password">` + tr("Пароль SSH") + `</label><input id="password" name="password" type="password" required maxlength="1024" autocomplete="new-password"></div>
			</div>
			<div class="actions"><button class="button" type="submit">` + tr("Проверить сервер") + `</button><a class="button secondary" href="?lang=` + query(string(d.Lang)) + `">` + tr("Начать заново") + `</a></div>
		</form>
		<p class="foot">` + tr("Подойдут root или отдельный SSH-пользователь с настроенным") + ` <span class="mono">sudo -n</span>. ` + tr("Пароль передаётся только в теле запроса и шифруется перед сохранением задания.") + `</p>
	</section>
	`)
	} else if d.Step == "preflight" {
		w(`
	<section class="card">
		<ul class="steps"><li>` + tr("1 · Сервер") + `</li><li>` + tr("2 · Ключ SSH") + `</li><li class="current">` + tr("3 · Проверка") + `</li><li>` + tr("4 · Установка") + `</li></ul>
		<h2>` + tr("Требования выполнены") + `</h2><p class="lead">` + tr("SSH-аутентификация и предварительные проверки прошли. Для запуска установки введите пароль ещё раз.") + `</p>
		`)
		if pf := d.Preflight; pf != nil {
			w(`<div class="facts">
			<div class="fact"><span>` + tr("Система") + `</span><b>` + esc(pf.Distribution) + ` ` + esc(pf.Version) + `</b></div><div class="fact"><span>` + tr("Архитектура") + `</span><b>` + esc(pf.Architecture) + `</b></div><div class="fact"><span>` + tr("Ядро") + `</span><b>` + esc(pf.Kernel) + `</b></div>
			<div class="fact"><span>` + tr("Процессоры") + `</span><b>` + strconv.FormatUint(uint64(pf.CPUCount), 10) + `</b></div><div class="fact"><span>` + tr("Память") + `</span><b>` + esc(l.bytes(pf.Memory)) + `</b></div><div class="fact"><span>` + tr("Свободное место") + `</span><b>` + esc(l.bytes(pf.Disk)) + `</b></div>
			<div class="fact"><span>Systemd</span><b>`)
			if pf.Systemd {
				w(tr("Доступен"))
			} else {
				w(tr("Не найден"))
			}
			w(`</b></div><div class="fact"><span>` + tr("Связь с панелью") + `</span><b>`)
			if pf.PanelReachable {
				w(tr("Есть"))
			} else {
				w(tr("Нет"))
			}
			w(`</b></div>
		</div>`)
		}
		w(`
		<div class="divider"></div><h2>` + tr("Подтверждение установки") + `</h2>
		<form method="post" action="" autocomplete="off">
			<input type="hidden" name="action" value="start"><input type="hidden" name="host" value="` + esc(d.Form.Host) + `"><input type="hidden" name="port" value="` + esc(d.Form.Port) + `"><input type="hidden" name="username" value="`)
		if d.Form.Username != "" {
			w(esc(d.Form.Username))
		} else {
			w(`root`)
		}
		w(`"><input type="hidden" name="fingerprint" value="` + esc(d.Fingerprint) + `">
			<input type="hidden" name="name" value="` + esc(d.Form.Name) + `"><input type="hidden" name="address" value="` + esc(d.Form.Address) + `"><input type="hidden" name="country_code" value="` + esc(d.Form.CountryCode) + `"><input type="hidden" name="location" value="` + esc(d.Form.Location) + `"><input type="hidden" name="provider" value="` + esc(d.Form.Provider) + `">
			<div class="field"><label for="password">` + tr("Пароль SSH пользователя") + ` `)
		if d.Form.Username != "" {
			w(esc(d.Form.Username))
		} else {
			w(`root`)
		}
		w(`</label><input id="password" name="password" type="password" required maxlength="1024" autocomplete="new-password"></div>
			<div class="check"><input id="confirm-key" name="confirm_key" type="checkbox" value="yes" required><label for="confirm-key">` + tr("Подтверждаю отпечаток") + ` <span class="mono">` + esc(d.Fingerprint) + `</span>.</label></div>
			<div class="check"><input id="confirm-install" name="confirm_install" type="checkbox" value="yes" required><label for="confirm-install">` + tr("Установить агент Mistgate и создать новую ноду. Существующая система не будет переустановлена.") + `</label></div>
			<div class="actions"><button class="button" type="submit">` + tr("Запустить установку") + `</button><a class="button secondary" href="?lang=` + query(string(d.Lang)) + `">` + tr("Отмена") + `</a></div>
		</form>
	</section>
	`)
	} else if d.Step == "job" {
		w(`
	`)
		if job := d.Job; job != nil {
			w(`<section class="card">
		<div class="job-head"><div><div class="eyebrow">` + tr("Задание установки") + `</div><h2 style="margin-top:4px">` + esc(job.Name) + `</h2></div><span class="status ` + esc(job.State) + `">` + tr(job.StateLabel) + `</span></div>
		<div class="job-meta"><div><span>` + tr("SSH-сервер") + `</span><b class="mono">` + esc(job.Host) + `</b></div><div><span>` + tr("Этап") + `</span><b>` + tr(job.PhaseLabel) + `</b></div><div><span>` + tr("Обновлено") + `</span><b>` + esc(job.UpdatedAt) + `</b></div></div>
		`)
			if job.ErrorLabel != "" {
				w(`<p class="error">` + tr(job.ErrorLabel) + `</p>`)
			}
			w(`
		`)
			if job.CanCancel {
				w(`<div class="divider"></div><form method="post" action="" autocomplete="off">
			<input type="hidden" name="action" value="cancel"><input type="hidden" name="job_id" value="` + esc(job.ID) + `">
			<div class="check"><input id="confirm-cancel" name="confirm_cancel" type="checkbox" value="yes" required><label for="confirm-cancel">` + tr("Отменить установку. Если SSH-команды уже выполняются, сервер может быть изменён частично.") + `</label></div>
			<div class="actions"><button class="button secondary" type="submit">` + tr("Отменить установку") + `</button><a class="button secondary" href="?lang=` + query(string(d.Lang)) + `">` + tr("К списку заданий") + `</a></div>
		</form>`)
			} else if job.State == "cancel_requested" {
				w(`<p class="notice">` + tr("Остановка запрошена. Панель завершит задание и удалит временные SSH-данные.") + `</p>`)
			}
			w(`
		`)
		}
		w(`
		`)
		if len(d.Events) > 0 {
			w(`<ul class="events">`)
			for _, ev := range d.Events {
				w(`<li><span>` + tr(ev.Label) + `</span><time>` + esc(ev.CreatedAt) + `</time></li>`)
			}
			w(`</ul>`)
		} else {
			w(`<p class="empty">` + tr("События появятся после запуска задания.") + `</p>`)
		}
		w(`
		`)
		if d.Job != nil && d.Job.CanRetry {
			w(`<div class="divider"></div><h2>` + tr("Повторить установку") + `</h2><p class="lead">` + tr("Проверьте причину ошибки. Для повтора введите SSH-логин и его пароль: root или пользователь с passwordless sudo.") + `</p>
		<form method="post" action="" autocomplete="off"><input type="hidden" name="action" value="retry"><input type="hidden" name="job_id" value="` + esc(d.Job.ID) + `">
			<div class="grid" style="margin-top:14px"><div class="field"><label for="retry-username">` + tr("SSH-логин") + `</label><input id="retry-username" name="username" required maxlength="32" value="root" autocomplete="username"></div><div class="field"><label for="retry-password">` + tr("Пароль SSH") + `</label><input id="retry-password" name="password" type="password" required maxlength="1024" autocomplete="new-password"></div></div>
			<div class="check"><input id="confirm-retry" name="confirm_install" type="checkbox" value="yes" required><label for="confirm-retry">` + tr("Повторно выполнить установку для этой ноды.") + `</label></div>
			<div class="actions"><button class="button" type="submit">` + tr("Повторить") + `</button><a class="button secondary" href="?lang=` + query(string(d.Lang)) + `">` + tr("К списку заданий") + `</a></div>
		</form>`)
		}
		w(`
		`)
		if d.Job != nil && d.Job.State == "completed" {
			w(`<div class="actions"><a class="button secondary" href="` + href(d.NodesHref) + `">` + tr("Открыть ноды") + `</a><a class="button secondary" href="?lang=` + query(string(d.Lang)) + `">` + tr("К списку заданий") + `</a></div>`)
		}
		w(`
	</section>
	`)
		if d.Refresh {
			w(`<p class="foot">` + tr("Статус обновляется автоматически каждые 4 секунды.") + ` <a href="?lang=` + query(string(d.Lang)) + `">` + tr("Открыть список заданий") + `</a></p>`)
		} else {
			w(`<p class="foot"><a href="?lang=` + query(string(d.Lang)) + `">` + tr("Вернуться к заданиям") + `</a></p>`)
		}
		w(`
	`)
	} else {
		w(`
	<section class="card">
		<ul class="steps"><li class="current">` + tr("1 · Сервер") + `</li><li>` + tr("2 · Ключ SSH") + `</li><li>` + tr("3 · Проверка") + `</li><li>` + tr("4 · Установка") + `</li></ul>
		<h2>` + tr("Новая нода") + `</h2><p class="lead">` + tr("Мастер установит Mistgate Agent на Ubuntu 22.04+ или Debian 12+ с systemd. Можно войти как root или как SSH-пользователь с passwordless sudo.") + `</p>
		<form method="post" action="" autocomplete="off"><input type="hidden" name="action" value="fingerprint">
			<div class="grid" style="margin-top:18px"><div class="field"><label for="host">` + tr("Адрес SSH-сервера") + `</label><input id="host" name="host" required maxlength="253" value="` + esc(d.Form.Host) + `" placeholder="` + tr("node.example.com или публичный IP") + `" autocomplete="off"></div><div class="field"><label for="port">` + tr("Порт SSH") + `</label><input id="port" name="port" type="number" min="1" max="65535" value="`)
		if d.Form.Port != "" {
			w(esc(d.Form.Port))
		} else {
			w(`22`)
		}
		w(`" required></div></div>
			<div class="actions"><button class="button" type="submit">` + tr("Начать проверку") + `</button></div>
		</form>
		<p class="foot">` + tr("Пароль не запрашивается до подтверждения отпечатка SSH host key.") + `</p>
	</section>
	`)
	}
	w(`
	`)
	if !(d.Job != nil) && len(d.Jobs) > 0 {
		w(`<section class="card"><h2>` + tr("Последние задания") + `</h2><div style="overflow-x:auto"><table class="table"><thead><tr><th>` + tr("Нода") + `</th><th>` + tr("Состояние") + `</th><th>` + tr("SSH-сервер") + `</th><th></th></tr></thead><tbody>`)
		for _, j := range d.Jobs {
			w(`<tr><td>` + esc(j.Name) + `</td><td><span class="status ` + esc(j.State) + `">` + tr(j.StateLabel) + `</span></td><td class="mono">` + esc(j.Host) + `</td><td><a href="?lang=` + query(string(d.Lang)) + `&job=` + query(j.ID) + `">` + tr("Открыть") + `</a></td></tr>`)
		}
		w(`</tbody></table></div></section>`)
	}
	w(`
	`)
	if !(d.Job != nil) && len(d.Access) > 0 {
		w(`<section class="card"><h2>` + tr("Доступ к серверам") + `</h2><p class="lead">` + tr("Пароли хранятся в зашифрованном виде. При смене панель сначала проверит новый вход и только затем заменит сохранённый пароль.") + `</p><div style="overflow-x:auto"><table class="table"><thead><tr><th>` + tr("Нода") + `</th><th>SSH</th><th>` + tr("Подключение") + `</th><th>` + tr("Сменить пароль") + `</th></tr></thead><tbody>`)
		for _, ac := range d.Access {
			w(`<tr><td>` + esc(ac.Name))
			if ac.Pending {
				w(`<div class="error">` + tr("Смена ожидает проверки; повторите её для восстановления.") + `</div>`)
			}
			w(`</td><td class="mono">` + esc(ac.Host) + `</td><td class="mono">` + esc(ac.Username) + `</td><td>`)
			if ac.Retired {
				w(tr("Нода выведена из флота: панель больше не меняет этот сервер. Пароль можно показать или забыть в настройках ноды."))
			} else {
				w(`<form method="post" action="" autocomplete="off"><input type="hidden" name="action" value="rotate_password"><input type="hidden" name="node_id" value="` + esc(ac.ID) + `"><input name="new_password" type="password" required minlength="12" maxlength="1024" autocomplete="new-password" aria-label="` + tr("Новый пароль SSH") + `"><label class="check"><input name="confirm_rotation" type="checkbox" value="yes" required><span>` + tr("Сменить пароль пользователя") + ` ` + esc(ac.Username) + `</span></label><button class="button secondary" type="submit">` + tr("Сменить") + `</button></form>`)
			}
			w(`</td></tr>`)
		}
		w(`</tbody></table></div></section>`)
	}
	w(`
	<p class="foot">` + tr("Изменения на сервере начинаются только после успешной проверки требований, подтверждения ключа и нажатия «Запустить установку».") + `</p>
</main>
</body>
</html>`)
	_, err := io.WriteString(out, b.String())
	return err
}

// htmlEscaper escapes text and quoted attribute values; it is the table of html/template (which also escapes "+").
var htmlEscaper = strings.NewReplacer("\x00", "\uFFFD", `"`, "&#34;", "&", "&amp;", "'", "&#39;", "+", "&#43;", "<", "&lt;", ">", "&gt;")

func esc(s string) string { return htmlEscaper.Replace(s) }

// query escapes s as a value inside a query string: every byte but a letter, a digit and "-._~" becomes %xx.
func query(s string) string { return esc(escapeURL(s, false)) }

// href escapes the address of a link. An address with a scheme other than http, https and mailto is replaced by
// "#ZgotmplZ"; the rest keeps its reserved characters and its valid %xx escapes, and every other byte becomes %xx.
func href(s string) string {
	if scheme, _, ok := strings.Cut(s, ":"); ok && !strings.Contains(scheme, "/") &&
		!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https") && !strings.EqualFold(scheme, "mailto") {
		return "#ZgotmplZ"
	}
	return esc(escapeURL(s, true))
}

// escapeURL percent-encodes s byte by byte (lower-case hex) except for the unreserved characters of RFC 3986; with
// norm it also keeps the reserved ones and the valid %xx escapes.
func escapeURL(s string, norm bool) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		keep := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0
		if norm && !keep {
			keep = strings.IndexByte("!#$&*+,/:;=?@[]", c) >= 0 ||
				c == '%' && i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2])
		}
		if keep {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}
