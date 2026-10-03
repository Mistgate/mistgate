<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./.github/assets/banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./.github/assets/banner-light.svg">
    <img alt="Mistgate — лёгкая панель для собственного VPN" src="./.github/assets/banner-dark.svg" width="100%">
  </picture>

  <p><a href="README.md">English</a> · <b>Русский</b></p>
</div>

**Mistgate** — лёгкая панель для собственного VPN-флота: один бинарь на Go для панели, один для агента ноды, без Docker. Hysteria2 и AmneziaWG живут рядом — в одной подписке и в одной админке. Панель хранит пользователей, профили и ноды; агент на каждой ноде запускает протоколы и держит хост в том состоянии, которое задала панель.

## Зачем ещё одна панель

- **Лёгкая.** SQLite, systemd, два статических бинаря. Без Docker и без отдельного сервера БД. Цель по памяти для панели в простое — 80 МБ (пока цель, а не замер).
- **Пользователи AmneziaWG на виду.** Каждое устройство AmneziaWG — пир, о котором панель знает: кто онлайн, трафик по пользователям и по устройствам.
- **Доктор, который знает хостеров.** Заполненный диск и журналы, резолвер, который не резолвит, уход часов, занятые порты, сетевые настройки, отошедшие от базовых (fq, BBR). Находит, объясняет простыми словами и, где это безопасно, исправляет одним подтверждённым нажатием.
- **Взгляд со стороны клиента.** Панель подключается к каждому профилю на каждой ноде так же, как настоящий клиент (Hysteria2 и AmneziaWG), поэтому нода «зелёная», только если трафик действительно идёт.
- **Скрыта по умолчанию.** На публичном адресе — сайт-ширма (встроенный или ваш каталог). Админка живёт под секретным префиксом пути, на секретном хосте или на отдельном адресе, а неизвестные токены подписок получают ту же ширму.
- **Удобна и для ИИ-агентов.** API-токены и MCP-сервер: изменения по схеме «план → применить», всё рискованное ждёт одобрения владельца.

## Что внутри

| Флот | Доступ и инструменты |
|:--|:--|
| <img src="./.github/assets/icons/zap.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Hysteria2 и AmneziaWG**<br>Hysteria2 на официальном ядре с Salamander; AmneziaWG 2.0 / 3.1 — по умолчанию в userspace, модулем ядра — по выбору для отдельной ноды. Несколько профилей на ноду. Протоколы — плагины с редактором по схеме. | <img src="./.github/assets/icons/qr.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Подписки**<br>Одна ссылка на человека. Каждое приложение получает формат, который умеет читать, по правилам User-Agent: список URI для приложений вроде Happ, профиль Mihomo YAML для Clash Verge или FlClash. Браузер получает страницу человека, AmneziaVPN — ключ `vpn://` на каждое устройство. |
| <img src="./.github/assets/icons/globe.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Выход через WARP**<br>Трафик профиля можно пустить через Cloudflare WARP. Если связь с WARP пропала, профиль закрывается, а не уходит напрямую. | <img src="./.github/assets/icons/users.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Пользователи и устройства**<br>Группы, свой пир на каждое устройство, лимиты трафика, DNS-пресеты для пользователя или группы. Страница пользователя с инструкциями под платформу, QR-кодом, трафиком и сроком, под своим паролем; если разрешить, человек сам добавляет там устройства AmneziaWG. |
| <img src="./.github/assets/icons/pulse.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Доктор флота**<br>Самопроверки нод с безопасными исправлениями, проверки глазами клиента, алерты с полным жизненным циклом. | <img src="./.github/assets/icons/window.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Админка**<br>Русский и английский, тёмная и светлая темы. Вход по passkey или паролю с кодом из приложения-аутентификатора, по желанию Cloudflare Turnstile, журнал аудита. |
| <img src="./.github/assets/icons/shield.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Подписанные обновления**<br>Агенты нод обновляются только из пакетов, подписанных вашим ключом релиза ed25519. Раскатка партиями с канарейкой, проверка здоровья, автоматический откат. | <img src="./.github/assets/icons/spark.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Доступ для агентов**<br>Скрипты ходят в тот же Connect API, что и админка, с токенами readonly, operator и admin. ИИ-агенты работают через встроенный MCP-сервер; изменения идут через «план → применить», рискованные ждут владельца. |

## Статус

Mistgate выпущен в ранней версии `v0.1.0`. Автор использует его в работе, но до 1.0 API, хранимые настройки и протокол ноды ещё могут меняться. Готовые бинарники панели для Linux amd64 и arm64 доступны в [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest); для других платформ собирайте из исходников.

| Этап | Что |
|:--|:--|
| **Готово** | Панель и агент ноды по mTLS · Hysteria2 · AmneziaWG 2.0 / 3.1 · выход через WARP · подписки и страница пользователя · DNS-пресеты · доктор, проверки глазами клиента, алерты · подписанные обновления агента с канарейкой и откатом · самообновление панели через GitHub Releases с проверкой контрольной суммы и откатом · API-токены и MCP-сервер · админка на русском и английском |
| **M2 — выпущен в v0.1.0** | SSH-установка из админки и одобренный MCP-процесс · root или логин с passwordless sudo · проверка SSH host key · сохранённый зашифрованный доступ и проверенная смена пароля · отмена и восстановление заданий · зашифрованные бэкапы в Cloudflare R2 и восстановление · [подробности M2](docs/ru/roadmap/m2-ssh-provisioning.md) |
| **Дальше** | Telegram-бот на весь флот · новые форматы подписок (Xray JSON, sing-box) и зеркала подписок · установка одной командой |
| **Потом** | VLESS REALITY как первый внешний плагин протокола |

Часть настроек по умолчанию рассчитана на пользователей в России (Яндекс DNS для нод в России, контрольные домены доктора, пресеты split-DNS); всё это меняется в настройках.

## Документация

Полная документация доступна на [mistgate.app/ru](https://mistgate.app/ru/) (английская версия: [mistgate.app](https://mistgate.app/)). Исходники лежат в [`docs/ru`](docs/ru/index.md) и [`docs/en`](docs/en/index.md). Coding-агентам — [`AGENTS.md`](AGENTS.md), установка нод через MCP описана в [инструкции для AI-агента](docs/ru/getting-started/ai-agents.md), опубликованный сайт отдаёт [`llms.txt`](llms.txt). С чего начать:

- [Обзор](docs/ru/getting-started/overview.md): основные понятия (панель, нода, профиль, пользователь, группа, устройство, подписка).
- [Установка панели](docs/ru/getting-started/install-panel.md), [добавление ноды](docs/ru/getting-started/add-node.md), [первые пользователи](docs/ru/getting-started/first-users.md).
- [Установка с AI-агентом](docs/ru/getting-started/ai-agents.md): подключение ноды, смена пароля и раскатка обновлений через MCP с одобрением владельца.
- [Здоровье](docs/ru/operations/health.md), [обновления](docs/ru/operations/updates.md), [зашифрованные бэкапы](docs/ru/operations/backups.md), [безопасность](docs/ru/operations/security.md), [решение проблем](docs/ru/operations/troubleshooting.md).
- [CLI](docs/ru/reference/cli.md), [конфигурация](docs/ru/reference/configuration.md), [API](docs/ru/reference/api.md), [MCP](docs/ru/reference/mcp.md).

## Требования

- **Панель:** Linux (amd64 или arm64) с systemd, доменное имя, порты 443 (и 80 для Let's Encrypt). Данные — в SQLite в `/var/lib/mistgate`.
- **Ноды:** Linux с systemd (Ubuntu 22.04+ или Debian 12+), root, публичный адрес, открытые UDP-порты ваших профилей. Агент сам подключается к панели, входящий порт управления на ноде не нужен.
- **Сборка:** Go 1.27 (строка `toolchain` в go.mod; `GOTOOLCHAIN=auto` скачает его), Node.js 22+, pnpm 10, make и POSIX-shell (Git Bash или WSL на Windows).

## Быстрый старт

Соберите статические бинарники для Linux (SPA админки встраивается в панель):

```sh
make build            # bin/mistgate-linux-{amd64,arm64}, bin/mistgate-node-linux-{amd64,arm64}
```

На сервере панели (здесь `panel.example.com`), от root:

```sh
install -m 0755 mistgate-linux-amd64 /usr/local/bin/mistgate
mistgate setup --public-url https://panel.example.com
#   печатает адрес админки (https://panel.example.com/<секретный префикс>/) и одноразовую ссылку настройки
mistgate serve --listen :443 --acme-domain panel.example.com
```

Откройте ссылку настройки и создайте владельца (passkey или пароль с кодом аутентификатора). Запускайте `serve` под systemd или другим супервизором; `mistgate serve -h` показывает все флаги, у каждого есть переменная окружения `MISTGATE_*`.

- `--tls-cert` / `--tls-key` — свой сертификат вместо Let's Encrypt.
- `setup --admin-host` или `--admin-listen` переносят админку на секретный хост или на отдельный адрес вместо префикса пути.
- Всё, что не админка, отвечает сайтом-ширмой; `--decoy-dir` — свой сайт.

Для автоматической установки ноды откройте **Ноды → Добавить ноду → Настроить установку по SSH**. Введите адрес и SSH-логин, проверьте отпечаток host key и результаты предварительной проверки, затем подтвердите установку. Панель поставит доверенного агента, запустит службу и дождётся подключения.

Для ручной установки выберите **Получить команду для ручной установки**. Положите бинарь агента на сервер как `/root/mistgate-node` (строку `scp` панель печатает, когда у неё есть подписанный пакет) и выполните команду от root:

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <секретное имя> \
  --ca-sha256 <отпечаток> --token <одноразовый токен> && /root/mistgate-node install
```

`enroll` обменивает токен на сертификат ноды, `install` пишет защищённый unit systemd и запускает агента. Нода сама станет онлайн в админке. Дальше — профиль, профиль на ноду, профиль в группу и пользователи; чек-лист первого запуска на «Обзоре» проводит по шагам.

> **Важно:** в API способ «по подписке» — это значение enum `HAPP` (и `access.happ`, `users.apps.happ`). Оно означает «по ссылке подписки», каким бы приложением её ни открыли; `AMNEZIA` — «по ключу AmneziaWG».

## Разработка

```
cmd/mistgate/            панель: serve, setup, auth, mcp (stdio-прокси), release, version
cmd/mistgate-node/       агент ноды: enroll, install, run, cleanup-net, awg prepare-kernel, version
proto/mistgate/          admin API и API панель <-> агент (Connect-RPC)
gen/, web/src/gen/       сгенерировано из proto/ (не править вручную)
internal/panel/          модули панели: store, vault, auth, fleet, access, subs, protocols, health, update, warp, mcp, httpserver ...
internal/node/           модули агента: agent, engine, hysteria2, awg, warp, hostctl, doctor, update ...
web/                     SPA админки (Vite, React, TypeScript, TanStack Router/Query, Tailwind) и страница пользователя
scripts/                 сквозные тесты
```

Код только для Linux (nftables, netlink, AmneziaWG, systemd) закрыт `//go:build linux` и имеет заглушки, поэтому `go build ./...`, `go vet ./...` и `go test ./...` работают и на Windows, и на macOS.

```sh
make dev                              # панель в режиме разработки: ширма :8080, админка :8081, агентам :8082, данные в ./.data
cd web && pnpm install && pnpm dev    # Vite на http://localhost:5173 с горячей перезагрузкой, прокси на админку :8081
make test                             # go vet, go test, затем pnpm typecheck, lint и vitest
make gen                              # buf lint + buf generate после правки proto/ (удалённые плагины: нужен интернет)
```

- При первом запуске dev-панель печатает одноразовую ссылку настройки (`http://localhost:8081/setup#...`). Сбросить всё: остановить панель и удалить `./.data`.
- Если порт 8081 занят, запустите панель с `--admin-listen 127.0.0.1:<порт>`, а Vite — с `MISTGATE_PANEL=http://127.0.0.1:<порт> pnpm dev`.
- Чтобы попробовать ноду против dev-панели, запустите обе в WSL (или в Linux-VM): `mistgate serve --dev`, добавьте ноду в админке, затем от root `mistgate-node enroll ... --state-dir /tmp/node` и `mistgate-node run --state-dir /tmp/node`. Агент не ходит на приватные адреса, поэтому проверяйте трафик на публичном сайте.

Сквозные тесты:

- `scripts/e2e-wsl.sh [--keep] [--m3 | --awg | --warp | --mihomo | --m3-only | --old-node <путь>]`: панель, нода, настоящий клиент Hysteria2 и самообновление, по флагам — клиенты AmneziaWG, поддельный пир WARP и настоящий mihomo. Запускается от root в WSL в своём сетевом namespace (`wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh` из корня репозитория); нужны go, curl, jq, python3 (с yaml для шагов туннелей), openssl, nft, ip и интернет. От 3 до 10 минут.
- `scripts/e2e-mcp.sh [-mode listener|prefix|both] [-keep]`: API-токены и MCP против настоящей панели на loopback, любая ОС, без интернета, около минуты.

> **Внимание:** тесты, которым нужен root (`MG_ROOT_TESTS=1`), и e2e-скрипты меняют сетевое состояние хоста. Запускайте их в WSL или на одноразовой VM и никогда — на боевой ноде или панели.

Соглашения — в [CONTRIBUTING.md](CONTRIBUTING.md).

## Выпуск обновлений нод

```sh
mistgate release keygen --out ~/mistgate-release.key      # один раз; печатает публичный ключ, файл храните офлайн
RELEASE_KEY=<публичный ключ> make build                   # вшивает ключ и время сборки в оба бинарника
mistgate release sign --key ~/mistgate-release.key --version "$(git describe --tags --always)" \
  --built "$(git log -1 --format=%ct)" --expires 30d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
scp dist/* panel.example.com:/var/lib/mistgate/dist/      # панель заметит пакет в течение минуты
```

Затем запустите раскатку на странице **Обновления** (только владелец, с повторным подтверждением входа).

- Агенты нод, собранные без `RELEASE_KEY`, не обновляются сами. Панель сохраняет ключ проверки пакетов в `release.pub`, пока сама обновляется из GitHub.
- Панель проверяет официальные GitHub Releases и устанавливает проверенный бинарник на root-установках под systemd. Для отката сохраняются предыдущий бинарник и копия данных, сделанная при остановленной панели.
- Ноды, установленные до появления самообновления, один раз обновляются вручную (`mistgate-node install` с новым бинарём).

## Безопасность

- Делайте резервные копии каталога данных панели (`/var/lib/mistgate`): там база, мастер-ключ, которым зашифрованы хранимые секреты, и CA панели, которому доверяют ноды. Без него все ноды придётся подключать заново.
- Держите админку за секретным префиксом, на секретном хосте или на отдельном адресе и не публикуйте её адрес.
- API-токены и MCP никогда не получают ссылки подписок, ключи устройств и пароли страниц; и всё же давайте агентам самый узкий профиль, которого хватает.
- Об уязвимостях сообщайте закрыто, см. [SECURITY.md](SECURITY.md).

## Лицензия

Mistgate — свободное ПО под [GNU Affero General Public License v3.0 only](LICENSE). Если вы запускаете изменённую панель для других, ссылка «Исходный код» в её админке (`serve --source-url`) должна вести на ваши исходники.
