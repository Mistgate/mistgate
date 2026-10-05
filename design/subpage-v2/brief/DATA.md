# Данные страницы подписки: что есть и что предлагается

Для разработчиков: контракт данных между панелью и публичной страницей. **Часть 1** — как устроено сейчас (по коду `internal/panel/subs/page.go`, `devices.go`, `unlock.go`, `subs.go`, `format.go`, `internal/panel/access/sub.go`, `web/src/sub/types.ts`, `logic.ts`). **Часть 2** — предложение: что добавить для новой страницы, новый вызов для выбора DNS, хранение и доставка DNS в каждый формат клиента. Примеры на `panel.example.com`, человек Alice. Секретный префикс подписки в примерах — `/k3xq8`.

---

## Часть 1. Как сейчас

### 1.1 Как страница получает данные

- `GET <prefix>/<token>`: один адрес для приложений и браузера. Что ответить, решает `subs.Choose(settings, User-Agent)`, первое совпадение:
  1. правила владельца `settings.rules[]`: `ua_contains` как подстрока без учёта регистра → формат. По умолчанию `mihomo` → Mihomo YAML и `clash` → Mihomo YAML;
  2. браузерный UA (`IsBrowser`: начинается с `Mozilla/`, содержит движок `AppleWebKit` / `Gecko/` / `Firefox/` / `Trident/` и **не** содержит имён VPN-клиентов `happ, v2ray, clash, mihomo, hiddify, sing-box, streisand, shadowrocket, karing, flclash, nekobox, okhttp, cfnetwork`) → **страница**;
  3. иначе — base64-список `hysteria2://…`.
- Страница — один файл `web/dist/sub.html` (JS, CSS и шрифты внутри). Сервер подставляет данные вместо `<!--MG_DATA-->` в виде `<script type="application/json" id="mg-data">…</script>` (`json.Marshal` экранирует `<`, `>` и `&`). Для единственного inline-скрипта сервер отдаёт CSP с его sha256:
  `default-src 'none'; script-src 'sha256-…'; style-src 'unsafe-inline'; img-src 'self' data:; font-src data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'` (в предпросмотре админки — `'self'`).
- Данные читает `normalize()` (`logic.ts`) защитно: поле, которого нет или у которого неверный тип, получает безопасное значение по умолчанию. Поэтому **новые поля можно добавлять без смены `v`**, старая страница их просто не видит.

### 1.2 `mg-data` — все поля (версия 1)

```json
{
  "v": 1,
  "lang": "ru",
  "brand": { "parts": ["mist", "gate"], "logo_svg": "", "accent": "#b8acf2" },
  "title": "Mistgate",
  "subscription_url": "https://panel.example.com/k3xq8/3f9k2v8q1x7m4c6d9b2n5h8j…",
  "server_count": 3,
  "server_loads": [
    { "name": "Германия · Frankfurt", "level": "high" },
    { "name": "Нидерланды", "level": "medium" },
    { "name": "Финляндия", "level": "low" }
  ],
  "user": {
    "name": "Alice",
    "status": "active",
    "expires_unix": 1766188800,
    "used_bytes": 12884901888,
    "quota_bytes": 107374182400,
    "quota_reset": "month",
    "next_reset_unix": 1760400000,
    "device_limit": 3,
    "devices_used": 2
  },
  "announcement": "В субботу с 02:00 до 03:00 обновляю серверы — может моргнуть на пару минут.",
  "support_url": "https://t.me/example_support",
  "options": { "show_announcement": true, "show_support": true, "show_qr": true },
  "apps": [
    { "platform": "ios", "kind": "happ", "name": "Happ",
      "download_url": "https://apps.apple.com/us/app/happ-proxy-utility/id6504287215",
      "add_url": "happ://add/https://panel.example.com/k3xq8/3f9k…", "description": "", "recommended": true },
    { "platform": "windows", "kind": "amnezia", "name": "AmneziaVPN",
      "download_url": "https://amnezia.org/downloads", "add_url": "", "description": "", "recommended": false }
  ],
  "access": { "happ": true, "amnezia": true },
  "devices": [
    { "id": "dev_a1", "platform": "", "model": "", "app": "happ", "last_seen_unix": 1759650000, "online": true },
    { "id": "dev_b2", "platform": "android", "model": "Pixel 7", "app": "amnezia", "last_seen_unix": 1759653000, "online": true }
  ],
  "amnezia": {
    "devices": [
      { "id": "dev_b2", "platform": "android", "label": "Pixel 7", "profile_id": "prf_31", "profile_name": "Main",
        "version": "3.1", "address": "10.66.4.9, fd66:66:0:1::9", "last_handshake_unix": 1759653000,
        "online": true, "stale": false,
        "min_clients": [{ "client": "amnezia", "app": "AmneziaVPN", "min": "5.0.1.5" }] }
    ],
    "profiles": [{ "id": "prf_31", "name": "Main", "version": "3.1", "egress": "direct", "countries": ["DE", "FI"] }],
    "can_add": true,
    "self_service": true,
    "endpoints": "https://panel.example.com/k3xq8/3f9k…/devices"
  },
  "locked": false,
  "unlock_url": ""
}
```

| Поле | Тип | Откуда (`page.go` / `access.SubView`) | Смысл и правила |
|---|---|---|---|
| `v` | int | константа 1 | версия формата |
| `lang` | `"ru"\|"en"` | язык инстанса | язык по умолчанию. Выбор человека хранится в `localStorage["lang"]` |
| `brand.parts` | [string, string] | `instance.BrandHead`, `BrandTail` | словесный знак: вторая часть рисуется акцентом |
| `brand.logo_svg` | string | логотип владельца, очищенный сервером | `""` = встроенный значок. Только в `<img src="data:…">` |
| `brand.accent` | hex | акцент инстанса | `normalize` пропускает только `#rrggbb` |
| `title` | string | действующее название подписки (настройки → бренд) | заголовок вкладки и шапки |
| `subscription_url` | string | `BaseURL + "/" + token` | **секрет**: копируется и кодируется в QR, на экран целиком не выводится |
| `server_count` | int | `len(v.Lines)` | сколько серверов получит приложение по ссылке |
| `server_loads[]` | `{name, level}` | `serverLoads(v.Servers)` | один элемент на **узел** с заданной пропускной способностью **и** свежим замером (≤ 90 с). `name` = страна на языке страницы + `" · " + Location`, иначе «Сервер», с номером при повторе. `level`: `high` ≥ 80 %, `medium` ≥ 50 %, иначе `low`. Проценты и скорости на страницу не уходят |
| `user.name` | string | `SubscriptionName`, иначе `UserName` | приветствие |
| `user.status` | `active\|expired\|limited\|disabled` | `ComputeStatus` в момент запроса | |
| `user.expires_unix` | int | `Expires` | 0 = бессрочно |
| `user.used_bytes` | int | `Up + Down` за текущий период | |
| `user.quota_bytes` | int | `Total` | 0 = без лимита |
| `user.quota_reset` | `none\|day\|week\|month\|rolling_month` | `QuotaReset` | |
| `user.next_reset_unix` | int | `NextReset` | 0 = нет |
| `user.device_limit` | int | `DeviceLimit` | 0 = без лимита |
| `user.devices_used` | int | `len(v.Devices)` | неявное устройство ссылки + каждый ключ AWG |
| `announcement` | string ≤ 1000 | `settings.announcement` | обычный текст, `\n` сохраняется |
| `support_url` | http(s) или `tg://` | `settings.support_url` | страница открывает только `http(s)`, `tg`, `mailto` (`safeUrl`) |
| `options.*` | bool | `settings.user_page` | `show_announcement`, `show_support`, `show_qr` |
| `apps[]` | см. ниже | `settings.apps[]` в порядке владельца | до 30 на инстанс |
| `apps[].platform` | `ios\|android\|windows\|macos\|linux` | | |
| `apps[].kind` | `happ\|amnezia` | `APP_HAPP` = «по ссылке подписки», `APP_AMNEZIA` = «ключ AmneziaWG» | |
| `apps[].name` | string ≤ 100 | | |
| `apps[].download_url` | http(s) | | подпись магазина страница выводит по домену: App Store / Google Play / «с сайта» |
| `apps[].add_url` | string | `addURL(add_link_template, link, title)`: подстановки `{url}`, `{url_enc}`, `{name_enc}` за один проход | `""`, если шаблона нет. Схемы `javascript: data: vbscript: file: blob: about:` запрещены и при сохранении, и на странице |
| `apps[].description` | string ≤ 80 | | одна строка обычного текста |
| `apps[].recommended` | bool | | значок, идёт первым на своей платформе |
| `access.happ` / `access.amnezia` | bool | переключатель приложения у пользователя **и** живой inbound нужного протокола | страница показывает только работающее |
| `devices[]` | `{id, platform, model, app, last_seen_unix, online}` | `v.Devices` | неявное устройство ссылки: `platform = model = ""`, одно на все приложения по ссылке. `last_seen_unix` обновляется не чаще раза в час. `online` = пользователь сейчас онлайн. Устройства AWG здесь тоже есть (`app = "amnezia"`), страница показывает их один раз |
| `amnezia` | object \| null | `amneziaData()` | `null`, если у пользователя нет AmneziaVPN |
| `amnezia.devices[]` | `pageAWGDevice` | `v.Devices` с `AWG` | `label` (≤ 40), `platform`, `profile_*`, `version` `3.1\|2.0`, `address` (**на экран не выводится**), `last_handshake_unix`, `online` = рукопожатие моложе 3 мин, `stale` = профиль изменился, `min_clients[]` |
| `amnezia.profiles[]` | `{id, name, version, egress, countries[]}` | `v.AWGProfiles` | на странице называются странами, WARP — «запасным выходом», 2.0 — «для старых версий», но не по `name` |
| `amnezia.can_add` | bool | есть адрес вызовов, статус active, есть профиль, есть место | |
| `amnezia.self_service` | bool | `settings.user_page.allow_device_self_service` (нет значения = вкл.) | |
| `amnezia.endpoints` | string | `link + "/devices"`; `""` в предпросмотре или при выключенном самообслуживании | страница берёт из адреса только путь и шлёт на свой origin |
| `locked` | bool | пароль включён и нет cookie | тогда настоящие только `v, lang, brand, title, unlock_url`; `apps`, `devices`, `server_loads` пустые |
| `unlock_url` | string | `link + "/unlock"` | |

Неактивный пользователь (`status ≠ active`): `access.SubView` возвращается до перебора inbound'ов, поэтому `server_count = 0`, `server_loads = []`, `access.* = false`, `amnezia = null`. Приложения вместо серверов получают одну строку-причину (`stateNote`).

### 1.3 Существующие вызовы страницы

Все под ссылкой подписки, только `POST`, JSON на входе и выходе, тело не больше 4 КБ, `Content-Type: application/json`, неизвестные поля отклоняются, в ответе `Cache-Control: no-store`.

| Вызов | Тело | Ответ 200 |
|---|---|---|
| `POST <link>/unlock` | `{"password":"abcd2345"}` | `{"ok":true}` + `Set-Cookie: mg_page=…` |
| `POST <link>/devices` | `{"profile_id","platform","label"}` | `{"device":{…},"configs":[AwgConfig…]}` |
| `POST <link>/devices/<id>/configs` | — | `{"device","configs"}`; конфиги отмечаются полученными (снимает `stale`) |
| `POST <link>/devices/<id>/rotate` | — | `{"device","configs"}` (новая пара ключей, тот же адрес) |
| `POST <link>/devices/<id>/revoke` | — | `{"ok":true}` |
| `POST <link>/devices/<id>/rename` | `{"label"}` | `{"device"}`. **Страница пока не использует** |

`AwgConfig`: `{node_id, node_name, country_code, version, conf, vpn_key, filename, stale, warnings[], min_clients[]}`. `warnings`: `amnezia_desktop_mtu`, `dns_fallback`, `dns_no_split`. `filename` вида `mistgate-de.conf`. Внимание: `node_name` — **внутреннее имя узла** (`de1`), страница выводит его в двух местах: при повторе страны («Германия · de1») и в диалоге нового ключа («старое подключение «de1 · AWG 3.1»»). См. 2.6.

### 1.4 Как вызовы защищены (`serveDevices`, `serveUnlock`)

Порядок проверок для `/devices…`:

1. **Пароль страницы**: если он включён (`PageKey` задан и `settings.user_page.require_page_password`, нет значения = вкл.) и нет cookie `mg_page` → `401 {"error":"locked"}`. Проверка идёт до базы. Cookie — HMAC от токена (сменили ссылку — cookie недействителен), `Path` = путь ссылки, `HttpOnly`, `Secure` (кроме http-инстанса), `SameSite=Lax`, около 180 дней.
2. **Токен**: неизвестный → ответ-приманка (decoy) и учёт промаха. 20 промахов за минуту с одного клиента (`auth.SourceKey`: адрес IPv4 или /64 IPv6) → клиент 15 минут получает только приманку.
3. Источник не умеет устройства → приманка.
4. **`http.CrossOriginProtection`** (Sec-Fetch-Site / Origin; доверенный origin — `BaseURL`) → `403 cross_origin`.
5. **Выключатель владельца** (`allow_device_self_service`) → `403 self_service_disabled`.
6. Разбор пути → `404 not_found`.
7. **Статус**: выдать или показать ключ (`devices`, `configs`, `rotate`) можно только при `active` → `409 user_inactive`. `revoke` и `rename` разрешены всегда.
8. **Бюджет записей**: 20 вызовов в час на токен (`MaxWritesPerHour`, отдельно от бюджета скачиваний подписки) → `429 too_many_requests` + `Retry-After`.
9. Работа. Ошибки модуля доступа: `FailedPrecondition` → `409 <код из начала сообщения>` (`device_limit`, `subnet_full`, …), `NotFound` → 404, `InvalidArgument` → `400 invalid`, остальное → `500 internal`.

Формат ошибки: `{"error":"<code>","message"?: "…"}`. Состояние идентификации всегда берётся из источника, а не из кэша скачивания: только что отключённый пользователь или сменённая ссылка перестают работать сразу.

---

## Часть 2. Предложение для новой страницы

Все добавления **аддитивные**: новые поля появляются рядом со старыми, `v` остаётся 1, `normalize()` даёт им значения по умолчанию. `server_loads` оставляем на переходный период (он выводится из `servers[]`).

### 2.1 Новые поля `mg-data`

```json
{
  "servers": [
    {
      "id": "nod_7k2m",
      "country_code": "DE",
      "place": "Frankfurt",
      "label": "Германия · Frankfurt",
      "app_names": ["🇩🇪 DE · Hysteria2", "🇩🇪 DE · Hysteria2 WARP"],
      "connections": [
        { "way": "link", "exit": "direct", "app_name": "🇩🇪 DE · Hysteria2" },
        { "way": "link", "exit": "warp",   "app_name": "🇩🇪 DE · Hysteria2 WARP" },
        { "way": "key",  "exit": "direct", "profile_id": "prf_31" }
      ],
      "online": true,
      "load": "high",
      "dns": {
        "choice": "",
        "effective": "dns_builtin_adblock",
        "options": ["dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_family"],
        "keys_to_refresh": []
      }
    },
    {
      "id": "nod_9q4f",
      "country_code": "FI",
      "place": "",
      "label": "Финляндия",
      "app_names": ["🇫🇮 FI · Hysteria2"],
      "connections": [{ "way": "link", "exit": "direct", "app_name": "🇫🇮 FI · Hysteria2" }],
      "online": false,
      "load": null,
      "dns": null
    }
  ],
  "dns": {
    "enabled": true,
    "endpoint": "https://panel.example.com/k3xq8/3f9k…/dns",
    "refresh_hours": 12,
    "link": { "per_server": false, "effective": "dns_builtin_adblock" }
  },
  "dns_presets": [
    { "id": "dns_builtin_adblock",  "name": "AdGuard — без рекламы", "description": "Блокирует рекламу и трекеры.", "category": "no_ads" },
    { "id": "dns_builtin_standard", "name": "Обычный",               "description": "Cloudflare и Google, без фильтров.", "category": "regular" },
    { "id": "dns_builtin_family",   "name": "Семейный",              "description": "Без рекламы и сайтов для взрослых.", "category": "family" }
  ],
  "platform_hint": "ios"
}
```

| Поле | Как получить | Правила |
|---|---|---|
| `servers[]` | по одному на **узел** из `v.Servers` (ссылка) и из узлов профилей AWG пользователя (ключ), в порядке подписки | только у активного пользователя, иначе `[]` |
| `servers[].id` | `node.ID` | уже уходит наружу в `AwgConfig.node_id`, нового раскрытия нет. Нужен вызову DNS. Если не хочется светить ID — HMAC(node_id), сервер сопоставляет обратно по узлам пользователя |
| `country_code`, `place` | `node.CountryCode`, `node.Location` | `place` — текст владельца, может быть пустым |
| `label` | правило `serverLoads()`: страна на языке страницы + `" · " + place`, иначе «Сервер», с номером при повторе | **никогда** не `node.Name` |
| `app_names[]` | `remarks(servers, settings.server_name_template, lang)`: имена Mihomo, **без** процента загрузки, который Happ добавляет к своим | если шаблон содержит `{node}`, поле **не заполняется** (`[]`): имя узла не должно попасть на страницу |
| `connections[]` | ссылка: каждая запись `v.Servers` этого узла. Ключ: каждый профиль AWG, у которого есть inbound на этом узле | `exit` = `egressOf(merged)` (`direct` / `warp`) |
| `online` | агент узла на связи: есть свежий замер сети (`CurrentNetworkUtilization`, ≤ 90 с) или живая сессия агента | сегодня узел с выпавшим агентом остаётся в подписке, и страница честно скажет «не отвечает». Узлы не в состоянии `active` в подписку не попадают вовсе |
| `load` | `loadLevel(LoadPercent)` или `null` | `null`: у узла нет `bandwidth_mbps` или нет свежего замера. Проценты не отдаём |
| `servers[].dns` | 2.3 | `null`, если выбора DNS нет (выключен или узел не настроен) |
| `dns.enabled` | `settings.user_page.allow_dns_choice` (новое, по умолчанию выкл.) | |
| `dns.endpoint` | `link + "/dns"`; `""` в предпросмотре, при выключенном выборе или у неактивного пользователя | |
| `dns.refresh_hours` | `settings.update_interval_hours` (0 → 12) | для текста «приложения получат DNS в течение N ч» |
| `dns.link.per_server` | `false`, пока DNS для Hysteria2 нельзя задать по узлам (2.4) | при `false` страница показывает одну пометку над списком серверов |
| `dns.link.effective` | действующий для приложений по ссылке пресет: сегодня `dns.Service.Effective(user)` | |
| `dns_presets[]` | только пресеты из `options` всех серверов. `name`/`description` на языке страницы | у встроенных пресетов `description` хранится как «ru\nen» в одном поле — делить по языку. Имена встроенных пресетов только русские: для `lang=en` нужен перевод (например, из `NameEN` каталога провайдеров) |
| `platform_hint` | тот же разбор, что `detectPlatform`, по `User-Agent` / `Sec-CH-UA-Platform` на сервере | **необязательно**: JS угадывает и сам. Полезно для предпросмотра «как iPhone» в админке. Можно не делать |

**Что новых полей не требует** (уже есть в данных, достаточно логики страницы):
- «Первый / повторный визит»: неявное устройство ссылки в `devices[]` (`platform = model = ""`) с `last_seen_unix > 0` означает, что приложение уже забирало подписку. Плюс `amnezia.devices.length > 0` и отметка в `localStorage`.
- «Скоро закончится» (≤ 7 дней) и «трафик почти исчерпан» (≥ 90 %) — пороги на странице.
- «Переименовать» — вызов `/devices/<id>/rename` уже есть.
- Запомненная платформа и тема — `localStorage` (ключи `platform`, `theme`). Значение только для удобства, страница работает и без него.

### 2.2 Новый вызов: выбор DNS

```
POST <link>/dns
Content-Type: application/json

{"server":"nod_7k2m","preset":"dns_builtin_yandex"}     // "" = вернуть вариант по умолчанию для узла
```

Ответ 200:

```json
{
  "server": { "...": "элемент servers[] целиком, с новым dns" },
  "stale_devices": ["dev_b2"]
}
```

`stale_devices` — устройства AWG, у которых есть конфиг на этом узле: им нужен свежий конфиг (2.4). Страница отмечает их `stale` и показывает «Получить ключ заново».

**Защита — та же цепочка, что у `/devices`** (1.4), в том же порядке, тем же кодом:

| Шаг | Ответ |
|---|---|
| пароль страницы, нет cookie | `401 locked` |
| неизвестный токен | приманка + учёт промаха |
| `CrossOriginProtection` | `403 cross_origin` |
| `allow_dns_choice` выключен | `403 dns_disabled` |
| пользователь не `active` | `409 user_inactive` (у неактивного нет серверов) |
| узел не входит в серверы пользователя | `404 not_found` |
| пресет не в `options` узла | `409 not_allowed` |
| бюджет записей (общий с `/devices`, 20 в час) | `429 too_many_requests` + `Retry-After` |
| тело не JSON, лишние поля, > 4 КБ | `400 bad_request` / `415` / `413` |

Реализация: в `routeOf` добавить `sub == "dns"` для `POST`. `serveDNS` повторяет начало `serveDevices` (стоит вынести общую часть: пароль → `identify` → приманка → `cop.Check` → выключатель → статус → `writeAdmit`). После записи — `st.drop()` (сбросить кэш вида токена), чтобы следующее скачивание подписки уже несло новый DNS. Аудит: одна строка `page_dns_choice` с именем пользователя, **именем узла** (для владельца это нормально) и пресетом, без токена и без адреса.

### 2.3 Хранение и правило выбора

Новые таблицы (миграция):

```sql
-- что владелец разрешил на узле
CREATE TABLE node_dns_option (
  node_id    TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  preset_id  TEXT NOT NULL REFERENCES dns_preset(id) ON DELETE CASCADE,
  position   INTEGER NOT NULL,
  is_default INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (node_id, preset_id)
);
-- что выбрал человек
CREATE TABLE user_node_dns (
  user_id    TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  node_id    TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  preset_id  TEXT NOT NULL REFERENCES dns_preset(id) ON DELETE CASCADE,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, node_id)
);
```

Действующий DNS для пары (пользователь, узел):
1. выбор человека — если пресет всё ещё в `node_dns_option` этого узла;
2. иначе вариант по умолчанию узла (`is_default`);
3. иначе сегодняшнее правило `dns.Service.Effective(user)`: свой пресет → группы → инстанса → встроенный.

У узла без строк в `node_dns_option` выбора нет (`servers[].dns = null`), действует пункт 3, всё как сегодня.

`servers[].dns`: `choice` — то, что выбрал человек (`""` = не выбирал), `effective` — результат правила, `options` — разрешённое владельцем в его порядке, `keys_to_refresh` — устройства AWG этого узла, ещё не получившие конфиг с новым DNS.

### 2.4 Как DNS доходит до каждого формата (и где по узлам нельзя)

| Клиент / формат | Чем DNS несётся сегодня (код) | По узлам? | Когда применится |
|---|---|---|---|
| **AmneziaVPN / AmneziaWG**: `.conf`, `vpn://`, QR | `[Interface] DNS =` в каждом конфиге. `access.awgDNS(user)` один раз на все узлы → `renderDeviceConfigs` (`device.go`) | **Да, точно**: конфиг и так отдельный на каждый узел. Нужно `awgDNS(user, node)` | Только после **нового получения конфига** (`/devices/<id>/configs`): DNS внутри ключа. Пара ключей не меняется, `rotate` не нужен |
| **Mihomo YAML, прокси AWG** (kl!ck, Clash Verge Rev, FlClash, CMFA) | у каждого прокси `remote-dns-resolve: true` + `dns: [a, b]` (`protocols/awg/render.go:437`), `in.DNS` из того же `awgDNS(user)` (`access/sub.go`) | **Да, точно**: поле у каждого прокси своё | При следующем обновлении подписки в приложении |
| **Mihomo YAML, прокси Hysteria2** | одна секция `dns:` на профиль (`dns/mihomo.go`, `Effective(user)`) | **Нет**: у hysteria2-прокси в Mihomo нет своего резолвера (проверено по https://wiki.metacubex.one/en/config/proxies/hysteria2/ — полей `dns`/`remote-dns-resolve` нет; они есть только у wireguard) | При обновлении, но один DNS на все узлы |
| **Happ** (base64 + заголовок `routing`) | профиль маршрутизации Happ: один Remote DNS и один Domestic DNS (`dns/happ.go`) | **Нет**: в формате Happ одно значение на всё устройство | При обновлении, один на все узлы |
| Прочие base64-клиенты (v2rayNG, Hiddify, Streisand, Shadowrocket) | панель не передаёт им DNS вообще | — | — |

**Решение, которое нужно принять.** Владелец хочет DNS по узлам и для подписок Hysteria2 («при следующем обновлении подписки»). Форматы клиентов этого не позволяют: у Hysteria2 резолвер один на профиль. Варианты:

- **(А) Сейчас, дёшево — рекомендуется.** Выбор по узлам точно работает для ключей AmneziaVPN и для прокси AWG в Mihomo. Для Hysteria2-приложений действует `dns.link.effective`: один DNS на все серверы, сегодняшнее правило пользователь → группа → инстанс. Страница честно пишет об этом одной строкой (`dns.link.per_server = false`). При желании можно дать человеку выбрать и его — из пересечения вариантов узлов.
- **(Б) Позже, точно для всех.** Резолвить на узле: агент узла резолвит имена назначения для сессий этого пользователя выбранным резолвером. Сработает для любых Hysteria2-клиентов, которые отдают серверу имя хоста, а не IP (Happ/Xray со sniffing, Mihomo в режиме fake-ip). Нужна поддержка в агенте: резолвер по пользователю вместо общего на сервер. Это заметная работа и отдельное ТЗ. Тогда `dns.link.per_server = true`, и пометка исчезает.
- (В) Отвергнуто: `nameserver-policy` в Mihomo работает по доменам, а не по выбранному прокси. `"#<proxy>"` у nameserver меняет маршрут запроса, а не резолвер.

**«Получить ключ заново» без смены ключа.** Сегодня `stale` = «эпоха профиля новее эпохи, которую получило устройство» (`AccessAWGDevice.Stale()`). Для DNS предлагается отметка `user_node_dns.updated_at` новее времени, когда устройство последний раз получило конфиги этого узла. Тогда `stale = true`, и в данных устройства **(новое)** `stale_reason: "profile" | "dns"` — страница меняет текст («DNS сервера изменился»). Вызов `/configs` снимает отметку, как и сейчас.

### 2.5 Что настраивает владелец (админка)

- **Подписки → Страница пользователя**: переключатель «Выбор DNS на странице» → `UserPageOptions.allow_dns_choice` (optional bool; нет значения = **выкл.**: новая функция, по умолчанию не включается).
- **Узлы → узел → «DNS для пользователей»** (или блок на той же странице «Страница пользователя» со списком узлов): разрешённые пресеты (порядок перетаскиванием) и один «по умолчанию». В proto: `Node.dns_option_preset_ids` (repeated string) и `Node.dns_default_preset_id`, либо отдельный `SetNodeDnsOptions`.
- Пример настройки из задачи (ID встроенных пресетов): российский узел — `dns_builtin_yandex` (по умолчанию) и `dns_builtin_standard`; остальные узлы — `dns_builtin_adblock` (по умолчанию), `dns_builtin_standard`, `dns_builtin_family`.
- В карточке пользователя в админке — выбор человека по узлам (только чтение) и «Сбросить».
- Предупреждение в админке: «В Happ и для Hysteria2 в Mihomo DNS один на все узлы — по узлам работает только для ключей AmneziaWG» (пока не сделан вариант Б).

### 2.6 Прочие изменения данных

- **`AwgConfig.node_name` → `label`**: страна + место, как `servers[].label`. Внутреннее имя оставить только как `legacy_name`, и только для одного случая — назвать старое подключение, которое человек должен удалить в приложении. Ключи, выданные до того, как имена стали по стране, назывались «de1 · AWG 3.1». Тогда `nodeLabels()` берёт `label`, а не `node_name`.
- **`apps[].add_url` для Shadowrocket**: схема `sub://` требует base64 от ссылки, а подстановки base64 нет. Если нужен Shadowrocket в один тап — добавить `{url_b64}` в `addKnown` (`subsettings.go`) и в `addURL` (`page.go`). См. `DEEPLINKS.md`.
- **`amnezia.devices[].stale_reason`**: см. 2.4.
- **Устройства у неактивного пользователя.** Сейчас `SubView` возвращается до перебора inbound'ов, поэтому `access.amnezia = false` и `amnezia = null`: неактивный человек не видит свои ключи и не может удалить ненужный, хотя сервер `revoke` и `rename` при неактивной подписке разрешает. Для новой страницы (SPEC 3.3) — отдавать `amnezia` с `devices[]` и `endpoints` и при `status ≠ active`, с `can_add = false` и пустыми `profiles`.

### 2.7 Что не меняется

- Страница остаётся одним файлом без внешних ресурсов. CSP прежняя: `connect-src 'self'` покрывает и `/dns`.
- Ключи никогда не встраиваются в страницу: только по запросу, по одному устройству, только в памяти.
- Предпросмотр админки (`preview = true`) не получает адресов вызовов: `amnezia.endpoints = ""`, `dns.endpoint = ""`.
