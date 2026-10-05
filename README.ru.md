<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./.github/assets/banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./.github/assets/banner-light.svg">
    <img alt="Mistgate — лёгкая панель для собственного VPN" src="./.github/assets/banner-dark.svg" width="100%">
  </picture>

  <p><a href="README.md">English</a> · <b>Русский</b> · <a href="https://mistgate.app/ru/">Документация</a></p>
</div>

**Mistgate** — лёгкая панель для собственного VPN-флота: один бинарь на Go для панели, один для агента ноды, без Docker. Hysteria2 и AmneziaWG живут рядом — в одной подписке и в одной админке. Панель хранит пользователей, профили и ноды; агент на каждой ноде запускает протоколы и держит хост в том состоянии, которое задала панель.

> **Установка с AI-агентом.** Claude Code, Codex CLI или любой агент, который умеет запускать `ssh` на вашем компьютере, может поставить панель за вас: скопируйте промпт установки из [инструкции для AI-агента](docs/ru/getting-started/ai-agents.md#установка-mistgate-с-ai-агентом). Он спрашивает перед каждым изменением и отдаёт вам ссылку настройки; аккаунт владельца вы создаёте сами.

## Зачем ещё одна панель

- **Лёгкая.** SQLite, systemd, два статических бинаря. Без Docker и без отдельного сервера БД. Цель по памяти для панели в простое — 80 МБ (пока цель, а не замер).
- **Пользователи AmneziaWG на виду.** Каждое устройство AmneziaWG — пир, о котором панель знает: кто онлайн, трафик по пользователям и по устройствам.
- **Доктор, который знает хостеров.** Заполненный диск и журналы, резолвер, который не резолвит, уход часов, занятые порты, сетевые настройки, отошедшие от базовых (fq, BBR). Находит, объясняет простыми словами и, где это безопасно, исправляет одним подтверждённым нажатием.
- **Взгляд со стороны клиента.** Панель подключается к каждому профилю на каждой ноде так же, как настоящий клиент (Hysteria2 и AmneziaWG), поэтому нода «зелёная», только если трафик действительно идёт.
- **Скрыта по умолчанию.** На публичном адресе — сайт-ширма (встроенный или ваш каталог). Админка живёт под секретным префиксом пути, на секретном хосте или на отдельном адресе, а неизвестные токены подписок получают ту же ширму.
- **Удобна и для ИИ-агентов.** API-токены и MCP-сервер: изменения по схеме «план → применить», всё рискованное ждёт одобрения владельца; готовые промпты для установки и работы с флотом.

## Что внутри

| Флот | Доступ и инструменты |
|:--|:--|
| <img src="./.github/assets/icons/zap.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Hysteria2](docs/ru/guide/hysteria2.md) и [AmneziaWG](docs/ru/guide/amneziawg.md)**<br>Hysteria2 на официальном ядре с Salamander; AmneziaWG 2.0 / 3.1 — по умолчанию в userspace, модулем ядра — по выбору для отдельной ноды. Несколько профилей на ноду. Протоколы — плагины с редактором по схеме. | <img src="./.github/assets/icons/qr.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Подписки](docs/ru/guide/subscriptions.md)**<br>Одна ссылка на человека. Каждое приложение получает формат, который умеет читать, по правилам User-Agent: список URI для приложений вроде Happ, профиль Mihomo YAML для [kl!ck](docs/ru/guide/client-apps.md#klck) (десктопное приложение, которое рекомендует Mistgate), Clash Verge или FlClash. Браузер получает страницу человека, AmneziaVPN — ключ `vpn://` на каждое устройство. См. [Приложения-клиенты](docs/ru/guide/client-apps.md). |
| <img src="./.github/assets/icons/globe.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Выход через WARP](docs/ru/guide/warp.md)**<br>Трафик профиля можно пустить через Cloudflare WARP. Если связь с WARP пропала, профиль закрывается, а не уходит напрямую. | <img src="./.github/assets/icons/users.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Пользователи и устройства](docs/ru/guide/users-and-groups.md)**<br>Группы, свой пир на каждое устройство, лимиты трафика, [DNS-пресеты](docs/ru/guide/dns.md) для пользователя или группы. [Страница пользователя](docs/ru/guide/user-page.md) с инструкциями под платформу, QR-кодом, трафиком и сроком, под своим паролем; если разрешить, человек сам добавляет там устройства AmneziaWG. |
| <img src="./.github/assets/icons/pulse.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Доктор флота](docs/ru/operations/health.md)**<br>Самопроверки нод с безопасными исправлениями, проверки глазами клиента, алерты с полным жизненным циклом и [защита от торрентов](docs/ru/guide/torrent-protection.md) на каждой ноде: распознанный BitTorrent блокируется в ядре, адреса не записываются. | <img src="./.github/assets/icons/window.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Админка](docs/ru/operations/security.md)**<br>Русский и английский, тёмная и светлая темы. Вход по passkey или паролю с кодом из приложения-аутентификатора, по желанию Cloudflare Turnstile, журнал аудита. |
| <img src="./.github/assets/icons/terminal.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Установка по SSH](docs/ru/getting-started/ssh-install.md)**<br>Подтвердите host key, введите пароль — и смотрите, как нода подключается. Панель сначала проверяет сервер, настраивает активный firewall и хранит SSH-доступ зашифрованным, с проверенной сменой пароля. | <img src="./.github/assets/icons/archive.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Зашифрованные бэкапы](docs/ru/operations/backups.md)**<br>Бэкапы по расписанию в ваш бакет Cloudflare R2, зашифрованные офлайн-ключом восстановления ещё до отправки, со сроком хранения и офлайн-восстановлением. |
| <img src="./.github/assets/icons/shield.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Подписанные обновления](docs/ru/operations/updates.md)**<br>Агенты нод и панель ставят только релизы, подписанные ключом релиза. Ноду можно обновить сейчас или по расписанию, с проверкой здоровья и автоматическим откатом. | <img src="./.github/assets/icons/spark.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Доступ для агентов](docs/ru/getting-started/ai-agents.md)**<br>Скрипты ходят в тот же [Connect API](docs/ru/reference/api.md), что и админка, с токенами readonly, operator и admin. ИИ-агенты работают через встроенный [MCP-сервер](docs/ru/reference/mcp.md); изменения идут через «план → применить», рискованные ждут владельца. |

## Статус

Mistgate выпускается в ранних версиях; текущая — [`v0.1.15`](https://github.com/Mistgate/mistgate/releases/latest). Автор использует его в работе, но до 1.0 API, хранимые настройки и протокол ноды ещё могут меняться. Бинарники для Linux amd64 и arm64 лежат в [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest); для других платформ собирайте из исходников.

| Этап | Что |
|:--|:--|
| **Готово** | Панель и агент ноды по mTLS · Hysteria2 · AmneziaWG 2.0 / 3.1 · выход через WARP · подписки и страница пользователя · DNS-пресеты · доктор, проверки глазами клиента, алерты, алерты в Telegram · защита от торрентов на ноде · установка по SSH из админки и через MCP с одобрением владельца · сохранённый SSH-доступ с проверенной сменой пароля · зашифрованные бэкапы в Cloudflare R2 и восстановление · подписанные обновления нод сейчас или по расписанию, с проверкой здоровья и откатом · самообновление панели из подписанных релизов · воспроизводимые релизы с офлайн-подписью · API-токены и MCP-сервер · админка на русском и английском |
| **Сейчас** | Проверка `v0.1.15` в боевой работе: защита от торрентов, установка по SSH и восстановление, подписанные релизы панели и нод |
| **Дальше** | Telegram-бот сверх алертов (флот и пользователи) · новые форматы подписок (Xray JSON, sing-box) и зеркала подписок · установка одной командой |
| **Потом** | VLESS REALITY как первый внешний плагин протокола |

Подробности и известные ограничения — в разделе [Статус и план развития](docs/ru/roadmap/status.md). Часть настроек по умолчанию рассчитана на пользователей в России (Яндекс DNS для нод в России, контрольные домены доктора, пресеты split-DNS); всё это меняется в настройках.

## Быстрый старт

Нужен Linux-сервер с systemd и домен, который на него указывает (здесь `panel.example.com`), со свободными TCP 80 и 443. На сервере, от root:

```sh
ARCH=amd64    # arm64 на сервере с ARM
for f in "mistgate-linux-$ARCH" SHA256SUMS; do
  curl -fsSLO "https://github.com/Mistgate/mistgate/releases/latest/download/$f"
done
sha256sum --check --ignore-missing SHA256SUMS
install -m 0755 "mistgate-linux-$ARCH" /usr/local/bin/mistgate
mistgate setup --public-url https://panel.example.com
#   печатает адрес админки (https://panel.example.com/<секретный префикс>/) и одноразовую ссылку настройки
mistgate serve --listen :443 --acme-domain panel.example.com    # первый запуск — вручную
```

Затем запустите `serve` под systemd с юнитом из [Установки панели](docs/ru/getting-started/install-panel.md), откройте ссылку настройки и создайте владельца (passkey или пароль с кодом аутентификатора). На той же странице — что доказывает контрольная сумма и чего не доказывает, секретный хост админки или отдельный адрес (`setup --admin-host`, `--admin-listen`), свой сертификат (`--tls-cert`, `--tls-key`) и свой сайт-ширма (`--decoy-dir`). `mistgate serve -h` показывает все флаги, у каждого есть переменная окружения `MISTGATE_*`.

Ноды добавляются в **Ноды → Добавить ноду**:

- **Запустить автоустановку по SSH**: введите адрес сервера, подтвердите его host key, введите пароль; панель проверит сервер, поставит агента и дождётся его. См. [Установку ноды по SSH](docs/ru/getting-started/ssh-install.md).
- **Получить команду для ручной установки**: скопируйте `mistgate-node` на сервер и выполните одноразовую команду от root. См. [Добавление ноды](docs/ru/getting-started/add-node.md).

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <секретное имя> \
  --ca-sha256 <отпечаток> --token <одноразовый токен> && /root/mistgate-node install
```

Дальше — профиль, профиль на ноду, профиль в группу и пользователи; чек-лист первого запуска на «Обзоре» проводит по шагам ([Первые пользователи](docs/ru/getting-started/first-users.md)).

> **Важно:** в API способ «по подписке» — это значение enum `HAPP` (и `access.happ`, `users.apps.happ`). Оно означает «по ссылке подписки», каким бы приложением её ни открыли; `AMNEZIA` — «по ключу AmneziaWG».

## Документация

Полная документация — на [mistgate.app/ru](https://mistgate.app/ru/) (английская версия: [mistgate.app](https://mistgate.app/)); её исходники — в [`docs/`](docs/README.md), на GitHub они читаются так же. С чего начать:

- [Обзор](docs/ru/getting-started/overview.md) и [Требования](docs/ru/getting-started/requirements.md): основные понятия и что понадобится.
- [Установка панели](docs/ru/getting-started/install-panel.md), [установка ноды по SSH](docs/ru/getting-started/ssh-install.md), [первые пользователи](docs/ru/getting-started/first-users.md).
- [Инструкция для AI-агента](docs/ru/getting-started/ai-agents.md): промпты, чтобы установить панель, вести флот через MCP и дорабатывать код.
- [Здоровье](docs/ru/operations/health.md), [обновления](docs/ru/operations/updates.md), [зашифрованные бэкапы](docs/ru/operations/backups.md), [безопасность](docs/ru/operations/security.md), [решение проблем](docs/ru/operations/troubleshooting.md).
- [CLI](docs/ru/reference/cli.md), [конфигурация](docs/ru/reference/configuration.md), [API](docs/ru/reference/api.md), [MCP](docs/ru/reference/mcp.md), [архитектура](docs/ru/reference/architecture.md).

Coding-агентам — [`AGENTS.md`](AGENTS.md); опубликованный сайт отдаёт и [`llms.txt`](https://mistgate.app/llms.txt).

## Требования

- **Панель:** Linux (amd64 или arm64) с systemd, доменное имя, порты 443 (и 80 для Let's Encrypt). Данные — в SQLite в `/var/lib/mistgate`.
- **Ноды:** Linux с systemd (Ubuntu 22.04+ или Debian 12+), root или беспарольный `sudo`, публичный адрес, открытые UDP-порты ваших профилей. Агент сам подключается к панели, входящий порт управления на ноде не нужен.
- **Сборка:** Go 1.27 (строка `toolchain` в go.mod; `GOTOOLCHAIN=auto` скачает его), Node.js 22+, pnpm 10, make и POSIX-shell (Git Bash или WSL на Windows).

## Разработка

```
cmd/mistgate/            панель: serve, setup, backup, auth, mcp (stdio-прокси), release, version
cmd/mistgate-node/       агент ноды: enroll, install, run, cleanup-net, awg prepare-kernel, version
proto/mistgate/          admin API и API панель <-> агент (Connect-RPC)
gen/, web/src/gen/       сгенерировано из proto/ (не править вручную)
internal/panel/          модули панели: store, vault, auth, fleet, access, subs, protocols, health, update, provision, backup, warp, mcp, httpserver ...
internal/node/           модули агента: agent, engine, hysteria2, awg, warp, hostctl, doctor, torrentguard, update ...
web/                     SPA админки (Vite, React, TypeScript, TanStack Router/Query, Tailwind) и страница пользователя
docs/, site/             документация (en, ru) и статический сайт, который из неё собирается
scripts/                 сквозные тесты
```

Код только для Linux (nftables, netlink, AmneziaWG, systemd) закрыт `//go:build linux` и имеет заглушки, поэтому `go build ./...`, `go vet ./...` и `go test ./...` работают и на Windows, и на macOS.

```sh
make build                            # bin/mistgate-linux-{amd64,arm64}, bin/mistgate-node-linux-{amd64,arm64}
make dev                              # панель в режиме разработки: ширма :8080, админка :8081, агентам :8082, данные в ./.data
cd web && pnpm install && pnpm dev    # Vite на http://localhost:5173 с горячей перезагрузкой, прокси на админку :8081
make test                             # go vet, go test, затем pnpm typecheck, lint и vitest
make gen                              # buf lint + buf generate после правки proto/ (удалённые плагины: нужен интернет)
```

- При первом запуске dev-панель печатает одноразовую ссылку настройки (`http://localhost:8081/setup#...`). Сбросить всё: остановить панель и удалить `./.data`.
- Если порт 8081 занят, запустите панель с `--admin-listen 127.0.0.1:<порт>`, а Vite — с `MISTGATE_PANEL=http://127.0.0.1:<порт> pnpm dev`.
- Чтобы попробовать ноду против dev-панели, запустите обе в WSL (или в Linux-VM): `mistgate serve --dev`, добавьте ноду в админке, затем от root `mistgate-node enroll ... --state-dir /tmp/node` и `mistgate-node run --state-dir /tmp/node`. Агент не ходит на приватные адреса, поэтому проверяйте трафик на публичном сайте.
- Бинарник, собранный без `RELEASE_KEY`, не умеет обновлять ни себя, ни ноды; см. [Релизы и подпись](docs/ru/operations/releases.md).

Сквозные тесты:

- `scripts/e2e-wsl.sh [--keep] [--m3 | --awg | --warp | --mihomo | --m3-only | --old-node <путь>]`: панель, нода, настоящий клиент Hysteria2 и самообновление, по флагам — клиенты AmneziaWG, поддельный пир WARP и настоящий mihomo. Запускается от root в WSL в своём сетевом namespace (`wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh` из корня репозитория); нужны go, curl, jq, python3 (с yaml для шагов туннелей), openssl, nft, ip и интернет. От 3 до 10 минут.
- `scripts/e2e-mcp.sh [-mode listener|prefix|both] [-keep]`: API-токены и MCP против настоящей панели на loopback, любая ОС, без интернета, около минуты.

> **Внимание:** тесты, которым нужен root (`MG_ROOT_TESTS=1`), и e2e-скрипты меняют сетевое состояние хоста. Запускайте их в WSL или на одноразовой VM и никогда — на боевой ноде или панели.

Соглашения — в [CONTRIBUTING.md](CONTRIBUTING.md); если работаете с coding-агентом, возьмите [промпт для доработки кода](docs/ru/getting-started/ai-agents.md#доработка-кода-с-coding-агентом).

## Релизы

Каждый стабильный тег собирает черновик релиза в GitHub Actions. Мейнтейнер пересобирает бинарники из тега на машине с офлайн-ключом релиза, а `mistgate release sign` подписывает только бинарники, которые пересобираются байт в байт; подписанные манифесты загружаются, и черновик публикуется. После этого панели сами скачивают подписанный пакет нод и предлагают обновление панели на странице «Обновления». Вся процедура, сборка со своим ключом и смена ключа — в разделе [Релизы и подпись](docs/ru/operations/releases.md).

## Безопасность

- Делайте резервные копии каталога данных панели (`/var/lib/mistgate`): там база, мастер-ключ, которым зашифрованы хранимые секреты, и CA панели, которому доверяют ноды. Без него все ноды придётся подключать заново. [Зашифрованные бэкапы в R2](docs/ru/operations/backups.md) делают это по расписанию.
- Держите админку за секретным префиксом, на секретном хосте или на отдельном адресе и не публикуйте её адрес.
- API-токены и MCP никогда не получают ссылки подписок, ключи устройств, пароли страниц и пароли серверов; и всё же давайте агентам самый узкий профиль, которого хватает.
- Об уязвимостях сообщайте закрыто, см. [SECURITY.md](SECURITY.md).

## Лицензия

Mistgate — свободное ПО под [GNU Affero General Public License v3.0 only](LICENSE). Если вы запускаете изменённую панель для других, ссылка «Исходный код» в её админке (`serve --source-url`) должна вести на ваши исходники.
