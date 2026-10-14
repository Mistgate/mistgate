<div align="center">

<img src="web/public/favicon.svg" width="96" height="96" alt="Логотип Mistgate">

<h1>Mistgate</h1>

<h3>Свой VPN для своих людей — без возни с серверами.</h3>

<p>Для семьи, друзей, сообщества или небольшой команды.<br>
Hysteria2, AmneziaWG и VLESS REALITY на ваших серверах: сервер добавляется из браузера, каждому человеку — одна ссылка, о сбое вы узнаёте первыми.</p>

<p>
<img alt="Статус: бета" src="https://img.shields.io/badge/%D1%81%D1%82%D0%B0%D1%82%D1%83%D1%81-%D0%B1%D0%B5%D1%82%D0%B0-f2c46b">
<a href="https://github.com/Mistgate/mistgate/releases/latest"><img alt="Последний релиз" src="https://img.shields.io/github/v/release/Mistgate/mistgate?sort=semver&color=b8acf2"></a>
<a href="LICENSE"><img alt="Лицензия AGPL-3.0" src="https://img.shields.io/github/license/Mistgate/mistgate?color=b8acf2"></a>
<a href="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml/badge.svg?branch=main"></a>
<a href="go.mod"><img alt="Версия Go" src="https://img.shields.io/github/go-mod/go-version/Mistgate/mistgate"></a>
</p>

<p><a href="https://mistgate.app/ru/"><b>Документация</b></a> · <a href="#быстрый-старт"><b>Установка</b></a> · <a href="#скриншоты"><b>Скриншоты</b></a> · <a href="#сравнение-с-другими-панелями"><b>Сравнение</b></a> · <a href="README.md">English</a> · <a href="README.zh.md">简体中文</a></p>

<p><sub><b>Бета.</b> Mistgate уже работает в продакшене, но до 1.0 возможны шероховатости, а API, хранимые настройки и протокол ноды ещё могут меняться. <a href="https://mistgate.app/ru/roadmap/status/">Статус и план</a></sub></p>

</div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/overview-dark-ru.webp">
  <source media="(prefers-color-scheme: light)" srcset="docs/images/readme/overview-light-ru.webp">
  <img alt="Админка Mistgate: шесть нод со статусом и нагрузкой, одна открытая проблема, последние события" src="docs/images/readme/overview-dark-ru.webp" width="100%">
</picture>

## Зачем Mistgate

Mistgate для тех, кто держит VPN для других людей: семьи, друзей, сообщества или небольшой команды. Вы ставите одну панель, добавляете в неё серверы и отправляете каждому человеку ссылку. Дальше панель сама держит серверы настроенными и проверяет их.

**Одна ссылка на человека, несколько протоколов.**
Приложения на ядре Clash/Mihomo (kl!ck, FlClash, Clash Verge Rev) получают по одной ссылке серверы Hysteria2, VLESS и AmneziaWG. Если в сети у человека заблокируют один протокол, второй уже есть в приложении. Happ получает серверы Hysteria2 и VLESS, а AmneziaVPN берёт ключ для каждого устройства со страницы человека. [Приложения-клиенты](docs/ru/guide/client-apps.md)

**VLESS REALITY за настоящим сайтом.**
VLESS идёт по TCP, через XHTTP или RAW с Vision, или через TLS по XHTTP или WebSocket, напрямую или за CDN, и работает в сетях, где режут UDP. Всякий, кто не клиент, видит настоящий сайт: по умолчанию собственный сайт ноды с её сертификатом Let's Encrypt или другой сайт, который вы укажете. Hysteria2 и VLESS могут жить на одной ноде, в том числе на общем TCP 443. **Проверить порты** на странице ноды с другой ноды проверяет, доходит ли TCP-порт именно до Mistgate. [VLESS REALITY](docs/ru/guide/vless.md) · [VLESS и Hysteria2 на одной ноде](docs/ru/guide/vless-and-hysteria2.md)

**Сервер добавляется из браузера.**
Введите IP и пароль root. Панель запомнит SSH-ключ сервера, проверит его, откроет порты во включённом UFW или firewalld, поставит подписанный агент и дождётся, пока он подключится. Сертификат Let's Encrypt нода получает сама, а кнопка **Включить WARP** даёт ей выход через Cloudflare. [Установка ноды по SSH](docs/ru/getting-started/ssh-install.md)

**Вы узнаёте первым.**
Каждые 5 минут панель подключается к каждому серверу так же, как приложение, и открывает через туннель тестовую страницу. На каждой ноде доктор делает 17 проверок хоста (диск, журнал, резолвер, часы, порты, BBR) и предлагает шесть исправлений. Ни одно не запускается без вашего подтверждения. Предупреждения приходят в Telegram. [Здоровье](docs/ru/operations/health.md)

**Лёгкая и незаметная.**
Два статических бинарника на Go: панель и агент ноды. SQLite и systemd, без Docker и без сервера БД. На небольшом тестовом сервере (1 vCPU, 1 ГБ) одна панель в простое занимает 16-20 МиБ приватной памяти (3x-ui 68-74, PasarGuard 262-265, Remnawave 426-454); панель, нода и база вместе на одном сервере с 2 vCPU и 2 ГБ занимают 57 МиБ (у тех же трёх 74, 297 и 761). В этом тесте добавление и удаление пользователей и перезапуск панели не оборвали ни одного соединения, а 3x-ui при смене пользователей перезапускает своё ядро. Посторонний по адресу панели видит сайт-ширму, а админка спрятана за секретным путём или именем хоста. Релизы воспроизводимые и подписаны офлайн-ключом, ноды ставят только подписанные сборки. Защита от торрентов блокирует открытый BitTorrent на ноде; в событии остаются признак и порт назначения, а адреса с ноды не уходят. [Безопасность](docs/ru/operations/security.md) · [Релизы и подпись](docs/ru/operations/releases.md) · [Бенчмарки, в том числе где Mistgate медленнее](docs/ru/reference/benchmarks.md)

**ИИ-агент в помощь.**
В панель встроен MCP-сервер. Claude Code, Codex или другой агент видит флот и меняет его по схеме «план → применить». Установка ноды, исправление доктора и смена пароля сервера ждут, пока вы одобрите их в админке. Ссылки подписок, ключи устройств и пароли агент не получает. [Инструкция для AI-агента](docs/ru/getting-started/ai-agents.md) · [MCP-сервер](docs/ru/reference/mcp.md)

> **Скоро, в релизах этого пока нет:** подписки в форматах Xray JSON и sing-box и полноценный Telegram-бот для флота и пользователей. Версия панели на Cloudflare Workers входит в релизы с v0.1.36 как [предварительная](docs/ru/getting-started/install-edge.md) и на настоящем аккаунте Cloudflare ещё не запускалась.

## Скриншоты

<table>
<tr>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/node-dark-ru.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/node-light-ru.webp"><img alt="Страница ноды: статус, выход через WARP, CPU, память, диск и трафик за сутки" src="docs/images/readme/node-dark-ru.webp" width="100%"></picture><br><sub><b>Нода.</b> Статус, выход через WARP, нагрузка и трафик за сутки.</sub></td>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/health-dark-ru.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/health-light-ru.webp"><img alt="Проверки глазами клиента: таблица нод и протоколов с задержкой через каждый туннель" src="docs/images/readme/health-dark-ru.webp" width="100%"></picture><br><sub><b>Проверки глазами клиента.</b> Каждый протокол на каждой ноде, задержка через туннель.</sub></td>
</tr>
<tr>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/user-dark-ru.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/user-light-ru.webp"><img alt="Карточка пользователя: трафик, каждое устройство по своему идентификатору и ключ AmneziaVPN, а также квота, срок, лимит устройств и лимиты скорости" src="docs/images/readme/user-dark-ru.webp" width="100%"></picture><br><sub><b>Человек.</b> Устройства по ID, трафик в счёт квоты и лимиты.</sub></td>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/phone-dark-ru.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/phone-light-ru.webp"><img alt="Страница человека на телефоне: сколько дней осталось, трафик и его устройства с кнопкой «Добавить устройство»" src="docs/images/readme/phone-dark-ru.webp" width="100%"></picture><br><sub><b>Его ссылка в браузере.</b> Сколько дней осталось, трафик и его устройства.</sub></td>
</tr>
</table>

На скриншотах выдуманные демо-данные.

## Быстрый старт

Нужен чистый Linux-сервер amd64 или arm64 с systemd, доменом, который указывает прямо на него, свободными TCP-портами 80 и 443 и `openssh-client` с поддержкой `ssh-keygen -Y verify` либо OpenSSL версии 3.2 или новее. На сервере выполните:

```sh
curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
```

Установщик проверит подписанный манифест панели, выполнит настройку и запустит Mistgate под systemd. Он напечатает адрес админки и одноразовую ссылку, действующую 30 минут. Правила firewall не меняются. Добавьте --yes для запуска без вопросов или --dry-run, чтобы проверить релиз без изменений на сервере. Ручная установка и остальные параметры описаны в [руководстве по установке панели](docs/ru/getting-started/install-panel.md).

Затем:

1. Откройте ссылку настройки и создайте аккаунт владельца: с passkey или с паролем и кодом приложения-аутентификатора.
2. Добавьте серверы в **Ноды → Добавить ноду** — автоматически по SSH или одноразовой командой ([Добавление ноды](docs/ru/getting-started/add-node.md)).
3. Добавьте первых пользователей ([Первые пользователи](docs/ru/getting-started/first-users.md)).

**Или поручите установку ИИ-агенту.** Claude Code, Codex CLI или любой агент, который умеет запускать `ssh` на вашем компьютере, пройдёт эти шаги сам: скопируйте промпт установки из [инструкции для AI-агента](docs/ru/getting-started/ai-agents.md#установка-mistgate-с-ai-агентом). Он спрашивает перед каждым изменением, а аккаунт владельца вы создаёте сами.

## Сравнение с другими панелями

| | Mistgate | 3x-ui | Remnawave | Hiddify | PasarGuard | s-ui |
|:--|:--|:--|:--|:--|:--|:--|
| Работает без Docker | ✅ | ✅ | ❌ | ✅ | частично | ✅ |
| База данных | SQLite | SQLite или PostgreSQL | PostgreSQL + Redis | MySQL + Redis | SQLite, MySQL, MariaDB или PostgreSQL | SQLite |
| Память панели в простое | 16-20 МиБ¹ | 68-74 МиБ¹ | 426-454 МиБ¹ | не измеряли | 262-265 МиБ¹ | не измеряли |
| Установка одной командой | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ |
| Ноду можно добавить из панели (SSH) | ✅ | ❌ | ❌ | ❌ | ❌ | ? |
| Hysteria2 | ✅ | ✅ | частично | ✅ | ✅ | ✅ |
| AmneziaWG | ✅ 2.0 и 3.1 | ✅ 3.1 | ? | ? | ? | ? |
| VLESS REALITY | ✅ | ✅ | ✅ | ✅ | ✅ | ? |
| VMess, Trojan, Shadowsocks | ❌ | ✅ | частично | ✅ | ✅ | ✅ |
| Подписки Xray JSON или sing-box | скоро | Xray JSON | ✅ обе | ✅ обе | sing-box | sing-box |
| Проверки и алерты | ✅ проверки глазами клиента, доктор с исправлениями | ✅ монитор туннеля (включается отдельно) | ✅ статус, Prometheus | частично | частично | ? |
| Telegram-бот | только алерты, полный бот скоро | ✅ | частично | ✅ | ✅ | ? |
| Лимит устройств по HWID | ✅ | ✅ | ✅ | ? | ✅ | ? |
| Встроенный MCP-сервер для ИИ-агентов | ✅ | ? | ❌ | ? | ? | ? |

¹ Приватная память всей группы панели в простое, 0–200 пользователей, без подключённой ноды, на тестовом сервере с 1 vCPU и 1 ГБ (у 3x-ui в группе есть его ядро Xray и fail2ban). Методика, версии и ограничения: [Бенчмарки](docs/ru/reference/benchmarks.md). «?» значит, что в README и документации проекта об этом не сказано.

Другие панели впереди по числу протоколов, размеру сообщества и полноценным Telegram-ботам. Полная таблица с источниками, проверенная 9 октября 2026 года, лежит на странице [Сравнение с другими панелями](docs/ru/reference/comparison.md).

## Статус и планы

Проект в бете. [Последний релиз](https://github.com/Mistgate/mistgate/releases/latest) уже работает в продакшене. До версии 1.0 ещё могут меняться API, хранимые настройки и протокол ноды. Бинарники для Linux amd64 и arm64 лежат в [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest).

Дальше: первый запуск версии для Cloudflare Workers ([предварительная](docs/ru/getting-started/install-edge.md)) на настоящем аккаунте и перенос в неё того, что есть на VPS, затем подписки Xray JSON и sing-box; позже — Telegram-бот для флота и пользователей. Часть настроек по умолчанию рассчитана на пользователей в России (Яндекс DNS для нод в России, контрольные домены доктора, пресеты split-DNS); всё это меняется в настройках. Подробности и известные ограничения: [Статус и план развития](docs/ru/roadmap/status.md).

## Документация

Вся документация на [mistgate.app/ru](https://mistgate.app/ru/), те же страницы лежат в [`docs/`](docs/README.md). С чего начать:

- [Обзор](docs/ru/getting-started/overview.md) и [Требования](docs/ru/getting-started/requirements.md)
- [Установка панели](docs/ru/getting-started/install-panel.md), [Установка ноды по SSH](docs/ru/getting-started/ssh-install.md), [Первые пользователи](docs/ru/getting-started/first-users.md)
- [Здоровье](docs/ru/operations/health.md), [Обновления](docs/ru/operations/updates.md), [Зашифрованные бэкапы](docs/ru/operations/backups.md), [Решение проблем](docs/ru/operations/troubleshooting.md)
- [CLI](docs/ru/reference/cli.md), [API](docs/ru/reference/api.md), [MCP-сервер](docs/ru/reference/mcp.md), [Архитектура](docs/ru/reference/architecture.md)

## Участие в разработке

Баг-репорты, исправления и правки документации приветствуются. В [CONTRIBUTING.md](CONTRIBUTING.md) (на английском) описаны сборка, устройство кода, проверки CI и соглашения. Coding-агентам начинать с [`AGENTS.md`](AGENTS.md), а в [инструкции для AI-агента](docs/ru/getting-started/ai-agents.md#доработка-кода-с-coding-агентом) есть готовый промпт для доработки кода.

## Безопасность

Об уязвимостях сообщайте закрыто, см. [SECURITY.md](SECURITY.md). Делайте резервные копии каталога данных панели (`/var/lib/mistgate`): там база, мастер-ключ и CA, которому доверяют ноды. [Зашифрованные бэкапы](docs/ru/operations/backups.md) в Cloudflare R2 делают это по расписанию.

## Лицензия

Mistgate распространяется как свободное ПО под [GNU Affero General Public License v3.0 only](LICENSE). Если вы запускаете изменённую панель для других, ссылка «Исходный код» в её админке (`serve --source-url`) должна вести на ваши исходники.
