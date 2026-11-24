---
title: Сравнение с другими панелями
seoTitle: "Mistgate или 3x-ui, Marzban, Remnawave: сравнение"
description: Чем Mistgate отличается от 3x-ui, Marzban, PasarGuard, Remnawave, Hiddify Manager и s-ui, в чём другие впереди и откуда данные; проверено 9 октября 2026 года и частично 24 ноября 2026 года.
---

Здесь Mistgate сравнивается с панелями, которые чаще всего ставят для той же задачи. Каждая клетка проверена 9 октября 2026 года по README, документации и заметкам к релизам самого проекта; «?» значит, что там об этом не сказано. 24 ноября 2026 года заново проверены клетки, где в таблице README стояло «?» или «частично», и добавленные в неё строки. Проекты быстро меняются: перед выбором загляните в источники, а если клетка устарела, откройте issue.

## Чем Mistgate отличается

- **Ноды добавляются из панели.** По SSH с паролем: панель закрепляет ключ хоста, проверяет сервер, готовит включённый firewall и ставит подписанный агент. В других панелях нода добавляется вручную: отдельная панель на каждую ноду, пакет ноды или сгенерированный compose-файл.
- **Без Docker и без сервера БД.** Два статических бинарника на Go, SQLite и systemd.
- **AmneziaWG на равных с остальными.** Версии 2.0 и 3.1, отдельный ключ на каждое устройство (потерянный телефон отключается, остальные не трогаются), ключи `vpn://` для AmneziaVPN. В 3x-ui тоже есть AmneziaWG 3.1 в userspace.
- **Проверки со стороны клиента.** Панель подключается к каждому профилю на каждой ноде так же, как приложение; на каждой ноде работает доктор с исправлениями, которые вы подтверждаете; алерты открываются, закрываются и приходят в Telegram.
- **Подписанные релизы.** Воспроизводимые сборки с подписью офлайн-ключом; панель и ноды ставят только подписанные сборки.
- **Скрыта по умолчанию.** На публичном адресе сайт-ширма, админка за секретным путём, хостом или на отдельном адресе.
- **Встроенный MCP-сервер** для ИИ-агентов со схемой «план → применить» и одобрениями владельца.

## В чём другие впереди

- **Протоколы.** VMess, Trojan, Shadowsocks, TUIC и обычный WireGuard есть в большинстве других панелей. В Mistgate есть Hysteria2, VLESS (REALITY по XHTTP или TCP с Vision; TLS по XHTTP или WebSocket, напрямую или за CDN), AmneziaWG и, экспериментально, MTProto только для приложения Telegram и TrustTunnel (VPN поверх HTTPS с официальными приложениями).
- **Форматы подписок.** Другие отдают Xray JSON, sing-box, Clash, Stash или Outline. Mistgate отдаёт список URI, Mihomo YAML, Xray JSON, sing-box JSON и ключи AmneziaVPN; не каждый профиль подходит каждому формату, см. [что где работает](../guide/subscriptions.md#что-где-работает).
- **Сообщество и зрелость.** У 3x-ui около 47,7 тыс. звёзд на GitHub, у s-ui около 10,0 тыс., у Hiddify Manager около 9,3 тыс. Mistgate новый проект в ранних версиях.
- **Лимит устройств.** 3x-ui, PasarGuard и Remnawave ограничивают устройства по HWID. Mistgate тоже, и даёт каждому устройству свои ключи, состояние «в сети» и трафик, так что одно устройство можно удалить или заблокировать, не трогая остальные.
- **Роли админов.** У PasarGuard и Hiddify Manager несколько админов с ролями; в документации Remnawave сказано, что нескольких админов там нет. В Mistgate сегодня один админ; три роли (владелец, помощник, только чтение) действуют для API-токенов, см. [Безопасность](../operations/security.md#роли-админов).
- **Telegram.** У 3x-ui, Marzban, PasarGuard и Hiddify Manager полноценные Telegram-боты. Mistgate отправляет алерты. **Планируется**: полноценный бот.
- **Установка.** Установщик Mistgate одной командой проверяет подписанный манифест панели, прежде чем её ставить.

## Полная таблица

| | Mistgate | 3x-ui | Marzban | PasarGuard | Remnawave | Hiddify Manager | s-ui |
|:--|:--|:--|:--|:--|:--|:--|:--|
| Бэкенд | Go | Go | Python, React | Python, React | NestJS (TypeScript) | Python | Go |
| База данных | SQLite | SQLite или PostgreSQL | SQLite, MySQL или MariaDB | SQLite, MySQL, MariaDB, PostgreSQL или TimescaleDB | PostgreSQL, Redis | MySQL, Redis | SQLite |
| Лицензия | AGPL-3.0 | GPL-3.0 | AGPL-3.0 | AGPL-3.0 | AGPL-3.0 | GPL-3.0 | GPL-3.0 |
| Docker | не нужен: systemd, статические бинарники | по желанию | частично: установщик использует Compose, ручная установка описана | частично: Compose | нужен и панели, и ноде | по желанию | по желанию |
| Установка одной командой | да | да | да | да | нет: Compose, скрипты сообщества | да | да |
| Память | панель: 39-44 МиБ в простое, замер¹; нода: установщику по SSH нужно 256 МБ | 73-76 МиБ в простое, замер¹ | ? | 273-277 МиБ в простое, замер¹; по документации: минимум 1 ГБ, рекомендуется 2 ГБ | 436-464 МиБ в простое, замер¹; по документации: панель минимум 2 ГБ, рекомендуется 4 ГБ; нода минимум 1 ГБ | ? | ? |
| Ядро | Hysteria2; xray-core для VLESS; AmneziaWG в userspace или в ядре | Xray, TUIC внутри процесса, AmneziaWG | Xray | Xray, ядро WireGuard; нода на sing-box анонсирована | Xray | Xray и sing-box | sing-box |
| VLESS REALITY / XHTTP | да / да | да / да | да / ? | да / ? | да / да | да / да | да / ? |
| VMess, Trojan, Shadowsocks | нет | да | да | да | частично: Trojan и Shadowsocks, VMess не указан | да | да |
| Hysteria2 | да | да | ? | да | частично: только в клиентских конфигах Xray JSON | да | да |
| TUIC | нет | да (v5) | ? | ? | ? | да | да |
| WireGuard | нет | да | ? | да | частично: только через конфиг ядра | да | ? |
| AmneziaWG | да: 2.0 и 3.1, ключ на каждое устройство | да: 3.1 в userspace | ? | ? | ? | ? | ? |
| MTProto (прокси для Telegram) | экспериментально: Fake TLS, ключ на человека, только для приложения Telegram | да: встроенный процесс `mtg`, секрет FakeTLS на клиента | ? | ? | ? | да: MTProxy FakeTLS и веб-прокси | ? |
| TrustTunnel | экспериментально: VPN поверх HTTPS, ключи `tt://` для официальных приложений | ? | ? | ? | ? | ? | ? |
| Несколько нод | да: агент по взаимному TLS | да: главная панель управляет другими панелями 3x-ui | да: marzban-node | да | да | да: родительская и дочерние панели | ? |
| Как добавляется нода | из панели по SSH или одноразовой командой | вручную: полная 3x-ui на каждой ноде | вручную | вручную | вручную: сгенерированный compose-файл | вручную: дочерняя панель | ? |
| Ретрансляция через ноду входа | да: пересылка с ноды входа на ноду выхода, настраивается в панели, проверяется до включения и каждые 5 минут | частично: цепочка исходящих, вручную | ? | ? | частично: описанный в документации мост, вручную | да: домены-реле; сервер-реле настраивается скриптом | ? |
| Форматы подписок | список URI, Mihomo YAML, ключи AmneziaVPN `vpn://`, Xray JSON, sing-box JSON, веб-страница | base64, Xray JSON, Clash/Mihomo | V2ray, Clash, ClashMeta | V2ray, Clash, ClashMeta, Xray JSON, sing-box, Outline | Xray JSON, base64, Mihomo, Stash, Clash, sing-box | sub, sub64, Xray, sing-box, Clash, ClashMeta, WireGuard | ссылки, sing-box, Clash.Meta |
| Трафик и срок на пользователя | да | да | да | да | да | да | да |
| Лимит устройств | да: HWID, место и ключи на устройство | да: HWID и IP | ? | да: HWID | да: HWID | ? | ? |
| Несколько админов с ролями | нет: один админ; роли действуют для API-токенов | ? | частично: несколько админов, в разработке | да: роли с областями доступа (RBAC) | нет | да: суперадмин, админ, агент | ? |
| Telegram-бот | частично: алерты; полный бот **Планируется** | да (и Discord) | да | да | частично: уведомления, вход через Telegram | да | частично: только сторонний бот |
| API | да: Connect, токены readonly / operator / admin | да: REST, токены с правами | да: REST, вебхуки | да: REST, вебхуки | да: OpenAPI, токены с правами, вебхуки | да | да |
| ИИ-агенты (MCP) | да: встроен, «план → применить», одобрения владельца | ? | ? | ? | своего сервера нет, есть от сообщества | ? | ? |
| Выход через WARP | да: на профиль, при обрыве закрывается | да | ? | частично: инструкция | частично: от сообщества | да | да (в исходниках) |
| Здоровье и алерты | да: проверки глазами клиента, доктор нод с исправлениями, жизненный цикл алертов, Telegram | да: алерты в Telegram, монитор туннеля (включается отдельно) | частично | частично | да: статус, Prometheus, алерты по трафику | частично | частично: только статус |
| Блокировка торрентов | да: в ядре; в событии остаются признак и порт назначения, адресов нет | ? | ? | ? | да: плагин | ? | ? |
| Сайт-ширма | да | частично: только маскировка Hysteria2 | да: шаблон главной страницы | ? | ? | да: настройка Decoy Site | ? |
| Подписанные релизы | да: офлайн-ключ, воспроизводимые сборки, ноды ставят только подписанные | частично: файлы `.sha256` | ? | ? | ? | ? | ? |
| Версия для Cloudflare Workers | предварительная: [ещё не запускалась на настоящем аккаунте Cloudflare](../getting-started/install-edge.md) | нет | нет | нет | нет | нет | нет |
| Звёзды на GitHub | 1 | около 47,7 тыс. | около 7,4 тыс. | около 2,7 тыс. | около 5,2 тыс. | около 9,3 тыс. | около 10,0 тыс. |
| Последний релиз | v0.2.1 | v3.11.0 (25.10.2026) | v0.8.4 (09.01.2025) | v5.6.0 (03.11.2026) | 3.4.11 (14.11.2026) | v13.0.3 (26.09.2026) | v1.6.4 (07.10.2026) |

¹ Замер проекта на одной тестовой машине: приватная память (anon) всей группы панели в простое, при 0–200 пользователях и без подключённой ноды, на сервере с 1 vCPU и 1 ГБ (у 3x-ui в группе есть его ядро Xray и fail2ban). Marzban, Hiddify Manager и s-ui не измеряли.

На сервере с 2 vCPU и 2 ГБ, где работали ядро VPN, база и трафик Hysteria2, Mistgate (панель и нода) занимал 92 МиБ в простое и 123 МиБ в «плохой день» (64 потока в туннеле, 300 запросов подписки в секунду и по одному пользователю, добавляемому и удаляемому каждую секунду); у 3x-ui те же показатели 81 и 118 МиБ, у PasarGuard 312 и 374 МиБ, у Remnawave 815 и 1 159 МиБ. На этом сервере 3x-ui занимает меньше памяти, чем Mistgate; одна панель меньше у Mistgate (39–44 МиБ против 73–76). Методика, версии и места, где Mistgate медленнее, описаны на странице [Бенчмарки](benchmarks.md). Сквозной тест сверяет память панели в простое с 80 МБ, а агента ноды со 100 МБ.

Marzban только поддерживается (последний релиз вышел в январе 2025 года), и его пользователи переходят на PasarGuard, преемника, которого развивают в отдельном репозитории, а не в форке на GitHub. Marzneshin заброшен.

## Источники

Проверено 9 октября 2026 года. README, документация и заметки к релизам каждого проекта; звёзды и последние релизы взяты из GitHub API в тот же день. 24 ноября 2026 года заново проверены последние релизы Mistgate, 3x-ui, PasarGuard и Remnawave (релизы на GitHub, для [бенчмарка](benchmarks.md)), а также клетки, где в таблице README стояло «?» или «частично», и добавленные в неё строки (TrustTunnel, прокси для Telegram, WireGuard, TUIC, ретрансляция, сайт-ширма, лимит устройств, админы) по страницам из раздела [ответы от 24 ноября 2026 года](#ответы-от-24-ноября-2026-года); остальное в таблице заново не проверялось.

| Проект | Репозиторий | Документация |
|:--|:--|:--|
| Mistgate | [Mistgate/mistgate](https://github.com/Mistgate/mistgate) | эта документация |
| 3x-ui | [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) | [docs.sanaei.dev](https://docs.sanaei.dev/) |
| Marzban | [Gozargah/Marzban](https://github.com/Gozargah/Marzban) | [gozargah.github.io/marzban](https://gozargah.github.io/marzban/) |
| PasarGuard | [PasarGuard/panel](https://github.com/PasarGuard/panel) | [docs.pasarguard.org](https://docs.pasarguard.org) |
| Remnawave | [remnawave/panel](https://github.com/remnawave/panel) | [docs.rw](https://docs.rw) |
| Hiddify Manager | [hiddify/Hiddify-Manager](https://github.com/hiddify/Hiddify-Manager) | [hiddify.com](https://hiddify.com) |
| s-ui | [alireza0/s-ui](https://github.com/alireza0/s-ui) | [вики](https://github.com/alireza0/s-ui/wiki) |

## Ответы от 24 ноября 2026 года

Страницы, на которых держатся ответы от 24 ноября 2026 года. Клетка говорит «да», «нет» или «частично» только если об этом сказано на одной из них; иначе в ней «?».

- **3x-ui:** [README](https://github.com/MHSanaei/3x-ui#readme) (список протоколов, TUIC, WireGuard, лимиты устройств по HWID, цепочки исходящих); [Inbounds](https://docs.sanaei.dev/docs/config/inbounds) (MTProto отдаёт встроенный процесс `mtg`); [Clients](https://docs.sanaei.dev/docs/config/clients) (устройства HWID); [Transports](https://docs.sanaei.dev/docs/config/transports) (`masquerade` у Hysteria2, `dialerProxy`); [TUIC](https://docs.sanaei.dev/docs/config/tuic).
- **Marzban:** [README](https://github.com/Gozargah/Marzban#readme) («Multi-admin support (WIP)», `HOME_PAGE_TEMPLATE`, «Decoy page template»).
- **PasarGuard:** [README](https://github.com/PasarGuard/panel#readme) (список протоколов, лимиты устройств по HWID, несколько админов с RBAC); [Admin roles](https://docs.pasarguard.org/en/panel/admin_roles); [Settings](https://docs.pasarguard.org/en/panel/settings) (форматы подписок, среди них `xray`).
- **Remnawave:** [Comparison of functions](https://docs.rw/overview/comparison-of-functions) («Multi-admin support: No», хосты WireGuard только через ядро); [Server-side routing](https://docs.rw/learn-en/server-routing) («мост», настраиваемый вручную); [HWID device limit](https://docs.rw/features/hwid-device-limit); [Awesome Remnawave](https://docs.rw/awesome-remnawave) (проекты сообщества).
- **Hiddify Manager:** [README](https://github.com/hiddify/Hiddify-Manager#readme) (список протоколов, прокси для Telegram, три роли админов); [All settings](https://hiddify.com/manager/configuration-and-advanced-settings/Settings-reference/) («Decoy Site»); [Telegram proxy](https://hiddify.com/manager/configuration-and-advanced-settings/How-to-set-up-Telegram-proxy-on-Hiddify/); [My account and admin security](https://hiddify.com/manager/configuration-and-advanced-settings/My-account-and-admin-security-in-HiddifyManager/); [Relay domains](https://hiddify.com/manager/domain-worker-cdn-and-tunneling/How-to-add-relay-domain-to-Hiddify-manager/).
- **s-ui:** [README](https://github.com/alireza0/s-ui#readme) (список протоколов, «Client & Traffic & System Status», Telegram-бот от третьей стороны) и страница вики [Configuration objects](https://github.com/alireza0/s-ui/wiki/Configuration-Objects) (`reality` в объекте TLS).
