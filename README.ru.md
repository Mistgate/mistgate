<div align="center">

<img src="web/public/favicon.svg" width="96" height="96" alt="Логотип Mistgate">

<h1>Mistgate</h1>

<p>Панель для своего небольшого VPN: несколько своих серверов и люди, с которыми вы ими делитесь.<br>
Hysteria2, VLESS и AmneziaWG. Два бинарника на Go, SQLite и systemd.</p>

<p>
<img alt="Статус: бета" src="https://img.shields.io/badge/%D1%81%D1%82%D0%B0%D1%82%D1%83%D1%81-%D0%B1%D0%B5%D1%82%D0%B0-f2c46b">
<a href="https://github.com/Mistgate/mistgate/releases/latest"><img alt="Последний релиз" src="https://img.shields.io/github/v/release/Mistgate/mistgate?sort=semver&color=b8acf2"></a>
<a href="LICENSE"><img alt="Лицензия AGPL-3.0" src="https://img.shields.io/github/license/Mistgate/mistgate?color=b8acf2"></a>
<a href="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml/badge.svg?branch=main"></a>
<a href="go.mod"><img alt="Версия Go" src="https://img.shields.io/github/go-mod/go-version/Mistgate/mistgate"></a>
</p>

<p><a href="https://mistgate.app/ru/"><b>Документация</b></a> · <a href="#быстрый-старт"><b>Установка</b></a> · <a href="#протоколы"><b>Протоколы</b></a> · <a href="#насколько-это-стабильно"><b>Насколько это стабильно</b></a> · <a href="#сравнение-с-другими-панелями"><b>Сравнение</b></a> · <a href="README.md">English</a> · <a href="README.zh.md">简体中文</a></p>

<p><sub><b>Бета.</b> До 1.0 возможны изменения. <a href="https://mistgate.app/ru/roadmap/status/">Статус и план</a></sub></p>

</div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/overview-dark-ru.webp">
  <source media="(prefers-color-scheme: light)" srcset="docs/images/readme/overview-light-ru.webp">
  <img alt="Админка Mistgate: шесть нод со статусом и нагрузкой, одна открытая проблема, последние события" src="docs/images/readme/overview-dark-ru.webp" width="100%">
</picture>

## Что умеет

Панель ставится на один сервер, а VPN-серверы добавляются в неё как ноды. Каждый человек получает одну ссылку-подписку, и приложение само подтягивает все положенные ему серверы. Панель держит ноды настроенными и проверяет их.

- Ноду добавляют из браузера по IP и паролю root. Панель запоминает SSH-ключ сервера, проверяет его, открывает порты во включённом UFW или firewalld и ставит подписанный агент. Можно и одноразовой командой. [Добавление ноды](docs/ru/getting-started/add-node.md)
- Одна ссылка на человека. Приложения на ядре Mihomo (kl!ck, FlClash, Clash Verge Rev) получают по ней все три протокола, Happ — Hysteria2 и VLESS, а AmneziaVPN берёт ключ на каждое устройство со страницы человека. [Приложения-клиенты](docs/ru/guide/client-apps.md)
- Каждые 5 минут панель подключается к каждому серверу, как приложение. Доктор на каждой ноде делает 17 проверок хоста и предлагает шесть исправлений; без вашего подтверждения ни одно не запускается. Алерты видны в админке и приходят в Telegram. [Здоровье](docs/ru/operations/health.md)
- Без Docker и без сервера БД. В простое панель занимает 16-20 МиБ памяти на сервере с 1 vCPU и 1 ГБ. [Бенчмарки](docs/ru/reference/benchmarks.md), в том числе где Mistgate медленнее.
- Посторонний по адресу панели видит сайт-ширму; админка — за секретным путём или именем хоста.
- Встроенный MCP-сервер: ИИ-агент читает флот и планирует изменения, а рискованные ждут вашего одобрения в админке. [MCP-сервер](docs/ru/reference/mcp.md)

Чего пока нет: Telegram-бот только присылает алерты, а версия для Cloudflare Workers — [предварительная](docs/ru/getting-started/install-edge.md). Часть настроек по умолчанию рассчитана на пользователей в России (их можно поменять), а необязательная [проверка доступности](docs/ru/operations/reachability.md) по умолчанию смотрит из России, и большинство её источников работают только там.

## Протоколы

| Протокол | На ноде | Что настраивается |
|:--|:--|:--|
| [Hysteria2](docs/ru/guide/hysteria2.md) | Официальное ядро Hysteria2 | UDP (QUIC). Обфускация Salamander (Gecko — экспериментально), port hopping, сертификат Let's Encrypt или закреплённый самоподписанный. |
| [VLESS](docs/ru/guide/vless.md) | xray-core | REALITY поверх XHTTP или TCP (RAW + Vision); TLS поверх XHTTP или WebSocket, напрямую или за CDN. |
| [AmneziaWG](docs/ru/guide/amneziawg.md) | amneziawg-go в userspace или модуль ядра | Версии 3.1 (по умолчанию) и 2.0, отдельный ключ на каждое устройство. Версий 1.5 и 1.0 нет. |

Hysteria2 и VLESS уживаются на одной ноде, даже на общем TCP 443. Не каждый формат подписки передаёт любой профиль: [Что где работает](docs/ru/guide/subscriptions.md#что-где-работает).

Не поддерживаются: VMess, Trojan, Shadowsocks, TUIC и обычный WireGuard.

Протокол — это две половины, вкомпилированные в бинарники. На панели это один Go-интерфейс: JSON Schema настроек (по ней админка строит форму), проверка, спецификация сервера для ноды, выдача ключей и кусок каждого формата подписки. На ноде это движок: запускает спецификацию, меняет ключи без перезапуска и отдаёт трафик и состояние. Новый протокол получают только агенты, которые его заявили, так что старые продолжают работать. Подробнее: [Контракт протокольного плагина](docs/ru/reference/architecture.md#контракт-протокольного-плагина).

## Насколько это стабильно

Это бета, и проект молодой: тег v0.1.0 поставлен 3 октября 2026 года, а v0.1.63 (27 октября) — 64-й. До 1.0 ещё могут меняться API, хранимые настройки и протокол ноды. Автоматического теста обновления со старых версий релизы пока не проходят, это одно из [условий для 1.0](docs/ru/roadmap/status.md).

Сам я держу на Mistgate шесть своих серверов для семьи и друзей.

Туннели не зависят от панели: пока она лежит, нода работает на последней применённой конфигурации; изменение пользователя не перезапускает сервер; старые агенты работают с новой панелью. В [бенчмарке](docs/ru/reference/benchmarks.md) добавление и удаление пользователей и перезапуск панели не оборвали ни одного соединения.

Проверки:

- CI на каждый push и pull request ([ci.yml](.github/workflows/ci.yml)): тесты и vet на Go, race-детектор на сессии панели с агентом, `govulncheck`, линт и тесты админки, сборка документации, установщик на Debian и Ubuntu.
- Сквозной прогон ([scripts/e2e-wsl.sh](scripts/e2e-wsl.sh)): настоящие панель, нода и клиенты Hysteria2, AmneziaWG, Xray, mihomo и sing-box, плюс обновление агента, откаты и установка по SSH. Запускается вручную перед каждым релизом, в WSL или на виртуалке, не в CI.
- GitHub Actions собирает каждый тег в черновик релиза. Я пересобираю его из тега на машине с офлайн-ключом релизов и подписываю только бинарники, совпавшие байт в байт; ноды и панель ставят только подписанные сборки. [Релизы и подпись](docs/ru/operations/releases.md)

Обновление ([Обновления](docs/ru/operations/updates.md)):

- Ноды сами не обновляются. Раскатка сначала обновляет одну ноду-канарейку и даёт ей 5 минут пройти проверку, потом идёт пачками по одной-две. Нода, не прошедшая её, получает прежнюю сборку, а раскатка встаёт на паузу.
- Агент откатывается и сам, если новая сборка не подключилась за 5 минут или падает при запуске. Любую ноду можно откатить вручную.
- Панель обновляет себя только с вашего подтверждения: сначала сохраняет копию данных, а если новая версия не продержалась 45 секунд, возвращает старый бинарник и данные.
- [Зашифрованные бэкапы](docs/ru/operations/backups.md) `/var/lib/mistgate` всё равно делайте.

Mistgate пишется с помощью ИИ-агентов. Я читаю то, что они пишут, и решаю, что попадёт в main и в релиз; проверки выше для их кода те же, что для любого другого. Коммиты, написанные с агентом, помечены строкой `Co-Authored-By`, а правила для агентов в этом репозитории — в [AGENTS.md](AGENTS.md).

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

Нужен чистый Linux-сервер amd64 или arm64 с systemd, доменом, который указывает прямо на него (здесь `panel.example.com`), свободными TCP-портами 80 и 443 и `openssh-client` с поддержкой `ssh-keygen -Y verify` либо OpenSSL версии 3.2 или новее. На сервере выполните:

```sh
curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
```

Установщик проверит подписанный манифест панели, запустит Mistgate под systemd и напечатает адрес админки с одноразовой ссылкой настройки на 30 минут. Firewall он не трогает. `--yes` убирает вопросы, `--dry-run` только проверяет релиз; ручная установка и остальные параметры: [Установка панели](docs/ru/getting-started/install-panel.md).

Затем:

1. Откройте ссылку настройки и создайте аккаунт владельца: с passkey или с паролем и кодом приложения-аутентификатора.
2. Добавьте серверы в **Ноды → Добавить ноду**, по SSH или одноразовой командой ([Добавление ноды](docs/ru/getting-started/add-node.md)).
3. Добавьте людей: [Первые пользователи](docs/ru/getting-started/first-users.md).

ИИ-агент, который умеет запускать `ssh`, пройдёт эти шаги по промпту из [инструкции для AI-агента](docs/ru/getting-started/ai-agents.md#установка-mistgate-с-ai-агентом); аккаунт владельца создаёте вы сами.

## Сравнение с другими панелями

| | Mistgate | 3x-ui | Remnawave | Hiddify | PasarGuard | s-ui |
|:--|:--|:--|:--|:--|:--|:--|
| Работает без Docker | ✅ | ✅ | ❌ | ✅ | 🟡 частично | ✅ |
| База данных | SQLite | SQLite или PostgreSQL | PostgreSQL + Redis | MySQL + Redis | SQLite, MySQL, MariaDB или PostgreSQL | SQLite |
| Память панели в простое | 16-20 МиБ¹ | 68-74 МиБ¹ | 426-454 МиБ¹ | не измеряли | 262-265 МиБ¹ | не измеряли |
| Добавить ноду из панели (SSH) | ✅ | ❌ | ❌ | ❌ | ❌ | ? |
| Hysteria2 | ✅ | ✅ | 🟡 частично | ✅ | ✅ | ✅ |
| VLESS REALITY | ✅ | ✅ | ✅ | ✅ | ✅ | ? |
| AmneziaWG | ✅ 2.0 и 3.1 | ✅ 3.1 | ? | ? | ? | ? |
| VMess, Trojan, Shadowsocks | ❌ | ✅ | 🟡 частично | ✅ | ✅ | ✅ |
| Подписки Xray JSON или sing-box | ✅ обе | Xray JSON | ✅ обе | ✅ обе | sing-box | sing-box |
| Проверки и алерты | ✅ проверки глазами клиента, доктор, доступность из страны | ✅ монитор туннеля (включается отдельно) | ✅ статус, Prometheus | 🟡 частично | 🟡 частично | ? |
| Telegram-бот | 🟡 только алерты | ✅ | 🟡 частично | ✅ | ✅ | ? |
| Встроенный MCP-сервер для ИИ-агентов | ✅ | ? | ❌ | ? | ? | ? |

¹ Приватная память всей группы панели в простое, 0–200 пользователей, без подключённой ноды, на тестовом сервере с 1 vCPU и 1 ГБ (у 3x-ui в группе есть его ядро Xray и fail2ban). Методика, версии и ограничения: [Бенчмарки](docs/ru/reference/benchmarks.md). «?» значит, что в README и документации проекта об этом не сказано.

Другие панели впереди по числу протоколов, размеру сообщества и полноценным Telegram-ботам. Полная таблица с источниками, проверенная 9 октября 2026 года: [Сравнение с другими панелями](docs/ru/reference/comparison.md).

## Документация

Вся документация на [mistgate.app/ru](https://mistgate.app/ru/) и в [`docs/`](docs/README.md). С чего начать:

- [Обзор](docs/ru/getting-started/overview.md) и [Требования](docs/ru/getting-started/requirements.md)
- [Установка панели](docs/ru/getting-started/install-panel.md), [Установка ноды по SSH](docs/ru/getting-started/ssh-install.md), [Первые пользователи](docs/ru/getting-started/first-users.md)
- [Здоровье](docs/ru/operations/health.md), [Обновления](docs/ru/operations/updates.md), [Зашифрованные бэкапы](docs/ru/operations/backups.md), [Решение проблем](docs/ru/operations/troubleshooting.md)
- [CLI](docs/ru/reference/cli.md), [API](docs/ru/reference/api.md), [MCP-сервер](docs/ru/reference/mcp.md), [Архитектура](docs/ru/reference/architecture.md)

## Участие в разработке

Баг-репорты, исправления и правки документации приветствуются. В [CONTRIBUTING.md](CONTRIBUTING.md) (на английском) описаны сборка, устройство кода, проверки CI и соглашения. Coding-агентам начинать с [`AGENTS.md`](AGENTS.md), а в [инструкции для AI-агента](docs/ru/getting-started/ai-agents.md#доработка-кода-с-coding-агентом) есть готовый промпт для доработки кода.

## Безопасность

Об уязвимостях сообщайте закрыто, см. [SECURITY.md](SECURITY.md). Делайте резервные копии каталога данных панели (`/var/lib/mistgate`): там база, мастер-ключ и CA, которому доверяют ноды.

## Лицензия

Mistgate распространяется как свободное ПО под [GNU Affero General Public License v3.0 only](LICENSE). Если вы запускаете изменённую панель для других, ссылка «Исходный код» в её админке (`serve --source-url`) должна вести на ваши исходники.

## История звёзд

<a href="https://www.star-history.com/?repos=mistgate%2Fmistgate&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&legend=top-left" />
   <img alt="График звёзд на GitHub" src="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&legend=top-left" />
 </picture>
</a>
