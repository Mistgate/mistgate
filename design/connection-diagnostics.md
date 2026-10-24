# Счётчики соединений и сохраняемый журнал ошибок нод — дизайн (на согласование)

Статус: черновик на согласование владельцем, 2026-10-23. База — main 5386b244 (v0.1.52). Дизайн: opus-plan; сверка ключевых ссылок на код — основная сессия.

## 0. Решение коротко

- **Сбор.** Движки считают события соединений атомиками только в хуках «раз на соединение/рукопожатие»; путь данных (`LogTraffic`, `countWriter`) не меняется.
- **Доставка.** Агент шлёт счётчики полем `links` в существующем `StatsBatch` (возможность `links/1`). События (подключения, отказы, обрывы, застрявшие рукопожатия, неизвестные ключи) — в ближайшем батче; объёмные (время онлайн, рукопожатия) — раз в 10 минут.
- **Хранение.** Панель пишет одним `INSERT … FROM json_each` в часовые корзины `link_hour` (48 ч) внутри guarded-пачки `IngestStats`: на edge шаг статистики остаётся 2 вызова D1.
- **Журнал.** Агент берёт WARN+ из существующего тройника `ringHandler`, кладёт шаблон сообщения и атрибуты из белого списка, адреса режет разбором `net/netip`, дедуплицирует; везёт в том же `StatsBatch` полем `journal` (`journal/1`). Панель хранит часовые агрегаты 7 дней и чистит адреса повторно.
- **Алерты.** О людях — новые варианты `user_connection` (Info, без Telegram). Об инбаунде — новый вид `handshake_failing` (Warning, Telegram).

## 1. На что опираемся (проверено)

- Сбор статистики агента: `internal/node/agent/stats.go:40` (`buildBatch`), снимок сессий каждые 10 с; объединения батчей в outbox нет.
- Логи движков уже идут через агента (`Env.Log = a.log.With("source", proto)`), кольцо на 5000 записей и тройник — `internal/node/agent/logring.go:112`. Живой хвост — только владельцу (`policy.go`: «node logs carry client addresses»); на edge стрима логов нет вообще — сохраняемый журнал станет там единственным видом логов.
- Приём: `internal/panel/store/fleet_stats.go` — read-пачка 4 statement, write-пачка 9; форма закреплена `measure_d1_js_test.go:189` (`{4, 9}`). Шаг статистики = 2 вызова D1 (`edge/bridge/link_js_test.go`). Минута 17 cron: 25 (`edge/bridge/cron_js_test.go`) / 30 (`bridge.cjs`). Лимит edge `maxStatsDeltas = 10 000`.
- Алерты: сигналы одной пачкой (`store/health_signals.go`), варианты людей — `health/users.go`, базовая линия по `node_traffic_hour`, сверка — `health/eval.go`. Info в Telegram не уходит.

## 2. Где брать сигналы

Строка ключа = `cred_id` + `inbound_id`; строка с `cred_id = ""` — сам инбаунд (всё, что нельзя приписать ключу).

| Поле | Смысл |
|---|---|
| `connects` | Успешное подключение (новая сессия) |
| `rejects` | Ключ известен, но отказ: срок вышел (снятый или сменённый ключ нода уже не знает — он идёт в `unknown_keys`; к человеку его привяжет фаза 5) |
| `drops` | Подключился, поток почти сразу замёрз или оборвался |
| `stalls` | Попытка рукопожатия видна, завершения нет |
| `online_s` | Секунды онлайн (агент по снимку сессий) |
| `unknown_keys` | Только строка инбаунда: ключ ноде неизвестен |
| `hellos`, `handshakes` | Только строка инбаунда: начатые и завершённые рукопожатия |

### 2.1 Hysteria2 (`internal/node/hysteria2`)

- `connects` — хук `inbound.Connect` (`index.go:233`), ядро вызывает раз на QUIC-соединение после авторизации (hysteria core v2.12.3 `server/server.go:213-215`).
- `rejects` — `Authenticate` (`index.go:137`): ключ найден, `validUntil` вышел.
- `unknown_keys` — `Authenticate` (`index.go:134`): `cs == nil`. Только клиенты Hysteria2 и зонды, знающие протокол; прочий HTTP/3 уходит в маскарад.
- `hellos` — обёртка `cert.GetCertificate` в `server.TLSConfig` (`hysteria2.go:313`): +1 на ClientHello QUIC-слушателя. `handshakes` — вызовы `Authenticate` с любым исходом; `hellos − handshakes` — рукопожатия без auth-запроса (DPI режет ответы или сканер). При TLS 1.3 PSK-возобновлении `GetCertificate` не вызывается → `hellos` занижен, алерт осторожный. Session cache у sing-box/mihomo — **требует проверки**.
- `drops` — хук `Disconnect(addr, id, err)` (`index.go:245`): `errors.As(err, *quic.IdleTimeoutError)` **и** сессия < 60 с или < 64 КиБ. Байты сессии без горячего пути: в `Collect` копим `cs.flushed`, в `Connect` запоминаем базу, в `Disconnect` — разность (точно при одном соединении на ключ).

### 2.2 VLESS (`internal/node/vless`, xray-core v1.260327.0)

- `connects` — `credState.addLink` (`egress.go:184`), переход 0→1 открытых ссылок.
- `rejects` — `inbound.dispatch` (`egress.go:419-425`): ключ `removed` или срок вышел.
- `unknown_keys` — неизвестный UUID отклоняет сам xray (`proxy/vless/encoding/encoding.go:94-96`). Точный хук: свой тип конфига прокси через `common.RegisterConfig`, создатель повторяет штатный и передаёт `countingValidator{*vless.MemoryValidator}` (nil от `Get` → `unknown` инбаунда). **Требует спайка.** Запасной путь: `*xlog.AccessMessage` со статусом `AccessRejected` в `xrayLogHandler.Handle` (по ноде целиком).
- `hellos` — принятые TCP-соединения: `WrapConnServer` (`tcpmask.go:145`) для REALITY/RAW/XHTTP/WS, в режиме TLS — `tlsFront.accept` (`front.go:155`).
- `stalls`/`drops` по TCP — один `getsockopt(TCP_INFO)` перед `Close` (`trackedTCPConn.Close`, конец `tlsFront.serve`): `stall` = `Bytes_received > 0 && Bytes_acked == 0 && Unacked > 0`; `drop` = `Unacked > 0 && 0 < Bytes_acked ≤ 64 KiB && Last_ack_recv ≥ 5000 мс` (типичные 15–20 КБ ТСПУ). Только Linux.
- Приписка к ключу: развернуть `inboundSession.Conn` через `NetConn()` до `*trackedTCPConn` и положить `cs` — работает для RAW (REALITY); XHTTP, WS и TLS-front — только строка инбаунда. Порядок обёрток — **требует проверки** тестом.

### 2.3 AmneziaWG (ядро и userspace)

Всё в `Engine.Collect` (`awg/engine.go`), который и так читает пиров раз в 10 с.

- `connects` — `LastHS` продвинулся, а пир был не в сети (> 190 с).
- `stalls` — дельта rx > 0, а `LastHS` старше 180 с: приходят только инициации, рукопожатие не завершается (ответ сервера не доходит до клиента). userspace проверено (`device/receive.go:426`), ядро — **требует проверки** на стенде.
- `unknown_keys` — ядро: подписка на multicast «auth» уже есть (`kernel_linux.go:38`), сейчас уходит снимком в `AwgHealth.unknown_peer_events`; добавляем в строку инбаунда. userspace: `Verbosef` сейчас пустышка — сверяем формат `"Received invalid initiation message from %s"` и считаем, аргументы с адресом не трогаем.
- `rejects`, `drops`, `hellos`, `handshakes` — нет: WireGuard молча роняет чужое.

### 2.4 Что узнать нельзя

Попытки, не дошедшие до ноды (их видят только kl!ck и TSPU-проба); AWG с неверной обфускацией (только `udp_rx_packets` инбаунда); Hysteria2 Initial до TLS и занижение при PSK; чей неизвестный ключ (до фазы 5); устройство и адрес клиента (не собираем по решению 07.10).

### 2.5 Цена на горячем пути

Атомики только в хуках раз на соединение/рукопожатие (`Authenticate`, `Connect`, `Disconnect`, `GetCertificate`, `validator.Get`, `addLink` под уже взятым мьютексом, TCP accept/close + один `getsockopt`). AWG — в `Collect`. Сброс через `Swap(0)`, как трафик. Бенчмарк `Authenticate` до/после.

## 3. Протокол агента (аддитивно, `api_version` = 1)

```proto
// StatsBatch
  repeated LinkCounters links = 9;    // agents with "links/1"
  repeated JournalEntry journal = 10; // agents with "journal/1"

message LinkCounters {
  string cred_id = 1;   // "" = the inbound itself
  string inbound_id = 2;
  uint32 connects = 3; uint32 rejects = 4; uint32 drops = 5; uint32 stalls = 6; uint32 online_s = 7;
  uint32 unknown_keys = 8; uint32 hellos = 9; uint32 handshakes = 10;
}
message JournalEntry {
  Severity level = 1;        // WARNING | ERROR
  string source = 2;         // agent | hysteria2 | vless | awg | xray | warp ...
  string message = 3;        // the template, never formatted with client data
  string inbound_id = 4;
  map<string, string> attrs = 5; // allow-listed keys, addresses replaced
  uint32 count = 6; int64 first_unix = 7; int64 last_unix = 8;
}
```

- Движок → агент: поле `engine.Collected.Links []plugin.LinkStat`, без нового интерфейса.
- Событийные поля — в ближайшем батче (только ненулевые); `online_s`, `hellos`, `handshakes` копятся и уходят на границе 10 минут UTC. Корзина часа — как у трафика.
- Совместимость: новые caps `links/1`, `journal/1`; старый агент — пометка «обновите агент»; старая панель отбрасывает неизвестные поля.
- Надёжность: поля `StatsBatch` → seq/ack/дедупликация, учёт ровно один раз; рестарт агента теряет до 10 минут объёмных счётчиков.
- Лимиты в `guardStats`: `links` ≤ `maxStatsDeltas`, `journal` ≤ 64, `message` ≤ 256 Б, `attrs` ≤ 8 × 160 Б.

## 4. Хранение в панели

```sql
CREATE TABLE link_hour (
    node_id TEXT NOT NULL, hour_start INTEGER NOT NULL, inbound_id TEXT NOT NULL,
    cred_id TEXT NOT NULL,                 -- '' = the inbound row
    user_id TEXT NOT NULL DEFAULT '',      -- resolved from device_credential at write time
    connects INTEGER NOT NULL DEFAULT 0, rejects INTEGER NOT NULL DEFAULT 0, drops INTEGER NOT NULL DEFAULT 0,
    stalls INTEGER NOT NULL DEFAULT 0, online_s INTEGER NOT NULL DEFAULT 0, unknown_keys INTEGER NOT NULL DEFAULT 0,
    hellos INTEGER NOT NULL DEFAULT 0, handshakes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, hour_start, inbound_id, cred_id)) STRICT;
CREATE INDEX link_hour_user ON link_hour (user_id, hour_start) WHERE user_id <> '';
CREATE INDEX link_hour_hour ON link_hour (hour_start);

CREATE TABLE node_journal (
    node_id TEXT NOT NULL, hour_start INTEGER NOT NULL, key TEXT NOT NULL,
    level INTEGER NOT NULL CHECK (level IN (2, 3)), source TEXT NOT NULL, message TEXT NOT NULL,
    inbound_id TEXT NOT NULL DEFAULT '', attrs_json TEXT NOT NULL DEFAULT '{}',
    count INTEGER NOT NULL, first_at INTEGER NOT NULL, last_at INTEGER NOT NULL,
    PRIMARY KEY (node_id, hour_start, key)) STRICT;
CREATE INDEX node_journal_hour ON node_journal (hour_start);
```

- Миграции по ADR 0002, без внешних ключей (как `client_probe_report`).
- `IngestStats`: `cred_id` из `links` разрешаются теми же чтениями (read остаётся 4); фильтр как у трафика (ключ разрешился, протокол совпал, probe-ключи пропускаются, строки `""` — только для инбаундов этой ноды); write +1 statement на счётчики (upsert по корзине) и +1 на журнал (с приблизительным лимитом 500 строк на ноду в час).
- Ретенция: `link_hour` 48 ч, `node_journal` 7 дней — одна пачка из двух `DELETE` в health sweep.

| Путь | Сейчас | После |
|---|---|---|
| Шаг статистики, вызовов D1 | 2 | 2 |
| Statement `IngestStats` (read/write) | 4/9 | 4/10 (журнал), 4/11 (счётчики) |
| Минута 17 cron | 25 / 30 | 26 / 31 |
| `HealthSignals` | 1 пачка | 1 пачка, +2 statement |

## 5. Журнал ошибок нод

- Что: slog WARN и ERROR агента и движков (включая отфильтрованные WARN/ERROR xray и `Errorf` amneziawg-go). INFO — нет (для важного есть Event). journald не читаем.
- Откуда: `a.journal.note(rec)` в `ringHandler.Handle`.
- Адреса — четыре слоя: (1) сообщение — шаблон: amneziawg-go `Errorf(f, a…)` пишет шаблон `f`, а форматированный текст — только в живой хвост (в `"%v - Failed to send handshake response: %v"` ошибка sendmsg несёт адрес клиента); (2) атрибуты по белому списку (`inbound, protocol, profile, port, state, reason, code, backend, mode, version, kernel, iface, check, fix, count, err, error`); (3) общий пакет `logscrub.Addrs` (~80 строк): токенизация, `netip.ParseAddrPort`/`ParseAddr` → `<ip>`/`<ip>:port`, домены остаются, длина ≤ 160 рун; (4) повтор чистки на панели плюс замена секретных имён (agent SNI, link/admin prefix).
- Дедупликация: на агенте по (level, source, message, inbound, code) за интервал батча, ≤ 64 ключа + запись `journal_overflow`; на панели upsert по (нода, час, ключ) — шторм одной ошибки = одна строка в час.
- Поиск: `GetNodeJournal(node?, since, until, min_level, source, inbound, query)`, `LIKE`, ≤ 200 строк.
- Edge: журнал приходит тем же `IngestStats` — на edge вкладка «Логи» показывает его вместо «недоступно».

## 6. Алерты

Окно W — две последние часовые корзины. Пороги стартовые, калибруются на реальной ноде.

**О людях** — `user_connection`, Info, вкладка «Люди» и блок «Соединение»:

| Вариант | Открывается (W) | Держится |
|---|---|---|
| `drops` «подключается, но сразу рвётся» | connects ≥ 3, drops ≥ 3, online_s < 600 | drops ≥ 1, online_s < 1200 |
| `rejected` «ключ отклоняется» | rejects ≥ 3, connects = 0 | rejects ≥ 1, connects = 0 |
| `handshake` «пакеты доходят, рукопожатие нет» (AWG) | stalls ≥ 6, connects = 0 | stalls ≥ 1 |

Только активные пользователи; главнее `silent`; не поднимается, если на инбаунде открыт `handshake_failing`; `never_connected` получает `packets_seen`.

**Об инбаунде** — новый вид `handshake_failing`:

| Протокол | Открывается (W) | Держится | Закрывается |
|---|---|---|---|
| Hysteria2 | hellos ≥ 30, handshakes/hellos < 0.3 | < 0.5 | ≥ 0.5 |
| VLESS | hellos ≥ 30, (stalls + drops)/hellos ≥ 0.5 | ≥ 0.25 | < 0.25 |
| AWG | ≥ 3 ключа со stalls и ≥ 50 % пытавшихся | ≥ 25 % | < 25 % |

Защиты: нода на связи, инбаунд работает, обычный пик ≥ 3 пользователей (медиана 7 дней) — сканеры на пустом инбаунде не поднимут; вытесняется `check_failed`/`no_traffic`/`node_down`; мало данных — состояние не меняется, 2 часа без данных — закрытие. Текст: «С ноды видно N попыток за час, X % рукопожатий не завершается — признак DPI на пути клиентов; проверки панели из-за рубежа этого не видят».

## 7. UI, API, MCP, приватность

- Карточка человека, блок «Соединение»: устройство × нода × протокол за 24 ч (подключений, обрывов, отказов, «рукопожатие не проходит», онлайн) + подсказка из алерта + пометка о старом агенте.
- Страница ноды: у инбаунда — попытки рукопожатий и % незавершённых, неизвестные ключи, замёрзшие соединения, для AWG «не прошли рукопожатие: N устройств»; вкладка «Журнал» с фильтрами.
- Admin API: `UserService.GetUserConnection`, `NodeService.GetNodeConnections`, `NodeService.GetNodeJournal`.
- MCP (ProfileReadonly): `user_connection`, `node_connections`, `node_journal`.
- Приватность: адресов клиентов и назначения нет нигде; гранулярность — ключ × час; карта сессий Hysteria2 по адресу не покидает агент.

## 8. Фазы (каждая — отдельный релиз)

| Фаза | Что | Доказательства | Объём |
|---|---|---|---|
| 1. Журнал | proto `journal`, агрегатор, `logscrub`, шаблоны amneziawg-go, миграция, +1 statement, ретенция, `GetNodeJournal`, вкладка (VPS и edge), MCP `node_journal`, docs | fuzz/таблица `logscrub` (IPv4/IPv6/`[v6]:port`/зоны, версии и время не трогаются); дедупликация и overflow; тройник не блокирует кольцо; форма `{4, 10}`; e2e: ошибка запуска инбаунда попадает в журнал без адресов (VPS и `--edge`) | 2–3 дня |
| 2. Счётчики Hysteria2 и AWG + хранение, API, UI, MCP | proto `links`, хуки H2, `Collect` AWG, окно и `online_s`, миграция, +1 statement, guard, RPC, UI, MCP | интеграция H2 (неверный токен, истёкший ключ, подключение, idle-обрыв); AWG на фейке и стенде; форма `{4, 11}`; e2e m3: неверный пароль H2 → `unknown_keys`, AWG с отрезанными ответами → `stalls` у человека | 3–4 дня |
| 3. VLESS | спайк валидатора, счётчики, TCP_INFO (Linux), приписка через unwrap | настоящий xray: неверный UUID при двух инбаундах, истёкший ключ; классификатор TCP_INFO таблицей; netns-тест «заморозка после 16 КиБ» | 2–3 дня |
| 4. Алерты | +2 чтения `HealthSignals`, варианты, `handshake_failing`, docs, Telegram | пороги/гистерезис/вытеснение; D1-бюджет оценщика; e2e: обрывы → вариант на «Людях» | ~2 дня |
| 5. Приписка неизвестных ключей (по желанию) | усечённые хеши неизвестных ключей → сверка с отозванными | совпадение/несовпадение, несовпавшее не хранится | 1–2 дня |

## 9. Риски и что не делаем

Риски: спайк xray (есть запасной путь); пороги TCP_INFO и алертов стартовые; rx на инициации и payload «auth» в ядре AWG не проверены (стенд); `hellos` H2 занижены при PSK; число строк `link_hour` на большой ноде в D1.

Не делаем: построчные логи соединений, адреса и ASN клиентов, адреса назначения; семейство/версию клиента из User-Agent (отдельной задачей); чтение journald; полнотекстовый индекс; объединение батчей; алерты на стороне агента; Telegram для алертов о людях.

## 10. Решения после ревью фазы 2 (Codex)

- Приём доверяет ключу и инбаунду, которые назвала нода, так же, как приём трафика: проверяется, что инбаунд этой ноды и протокол ключа совпадает, но не то, что человек имеет доступ к профилю инбаунда. У Hysteria2 один ключ на устройство для всех профилей протокола; подделать счётчики может только нода с действующим mTLS, а такая нода может подделать и трафик.
- Снятый или сменённый ключ нода уже не знает: его попытки идут в `unknown_keys` инбаунда, а не в `rejects` человека. Привязка — фаза 5.
- Поздний обрыв уже отозванного ключа Hysteria2 может не попасть в счёт (его состояние выброшено после последнего сбора). Ключ уже недействителен, вреда нет.

## 11. После выкладки v0.1.53 (живой флот)

- `hellos`/`handshakes` инбаунда включали синтетические проверки панели (служебный ключ `health_probe_cred`, раз в 300 с, при сбоях раз в 60 с): на пустом инбаунде видны рукопожатия без подключений. Агент не знает служебный ключ, а ClientHello приходит до авторизации, поэтому вычитание на приёме: подключения и отказы строки служебного ключа (по одному на рукопожатие, дошедшее до авторизации) пишутся в `link_hour.probe_handshakes` строки инбаунда (миграция 00077), чтение вычитает их из обоих счётчиков с нулём снизу. Агент сбрасывает рукопожатия раз в 10 минут, а подключения сразу, поэтому на краях окна расхождение в одну-две проверки; доля незавершённых не страдает, вычитается одно и то же. Агенты обновлять не нужно.
- `drops` Hysteria2 считают и короткие проверочные соединения клиентов (проверка всех серверов, автовыбор): соединение, меньше 64 КиБ, таймаут простоя. Сами по себе обрывы не признак ТСПУ; признак уточняется до алертов (фаза 4).

## 12. Фаза 1 (журнал): решения для реализации, 2026-10-24

Владелец: журнал видят все админы (адреса вырезаны), хранится 7 дней. Контракт — `agent.proto` JournalEntry (комментарий над ним — спецификация агента), `node.proto` GetNodeJournal, миграция 00078.

- Шаблон вместо текста: движок, который форматирует сообщение с данными клиента (логгер amneziawg-go в `awg/userspace_linux.go`), кладёт шаблон в атрибут `msg_template`; журнал берёт его вместо сообщения и не отправляет как атрибут. Живой хвост по-прежнему получает отформатированный текст.
- Агент: `journal.note` из `ringHandler.Handle` для WARN+, без канала и без ожидания (короткий мьютекс), чтобы кольцо никогда не блокировалось. Ключ дедупликации — level, source, message, inbound, `code`; атрибуты — первой записи интервала. Забирается в `buildBatch` в `StatsBatch.journal`. Возможность `journal/1`.
- Чистка адресов — общий пакет `internal/logscrub` (агент и панель): токенизация, `netip.ParseAddrPort`/`ParseAddr`, `<ip>`/`<ip>:port`; версии, время и имена не трогаются.
- Панель: `guardStats` режет журнал до 64 записей, сообщение до 256 байт, атрибуты до 8 × 160 символов; `IngestStats` +1 statement (upsert по node, hour, key; key — хеш панели; count складывается, first/last расширяются, attrs остаются от первой строки часа; ≈ 500 строк на ноду в час, остальное отбрасывается); повторная чистка адресов и замена собственных секретных имён ноды и панели. Форма пачки `{4, 11}`. Ретенция 7 дней в health sweep (cron минуты 17 — обновить бюджеты).
- `GetNodeJournal`: см. `node.proto`; `nodes_without_journal` — ноды без `journal/1`. MCP `node_journal` (ProfileReadonly).
- UI: вкладка «Журнал» на странице ноды (VPS и edge); на edge, где живого хвоста нет, вкладка «Логи» ведёт в журнал.

## Вопросы владельцу

1. Кто видит журнал: все админы (адреса вырезаны) или только владелец, как живые логи?
2. Сроки: счётчики 48 часов, журнал 7 дней — достаточно?
3. `handshake_failing` в Telegram сразу как Warning или один релиз на Info для калибровки?
4. Нужна ли фаза 5 (приписка отклонённых неизвестных ключей к людям)?
5. VLESS по человеку только на RAW (REALITY), XHTTP/WS/TLS-front — по инбаунду. Устраивает?
6. Порядок фаз: журнал первым или счётчики соединений первыми?
