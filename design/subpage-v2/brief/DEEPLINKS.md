# Ссылки «Добавить одним нажатием» и форматы подписки по приложениям

Справочник для владельца и разработчиков. Здесь: какие приложения может предлагать страница, какой у каждого шаблон ссылки «добавить» в синтаксисе панели, какой формат подписки панель ему отдаёт и умеет ли оно сканировать QR. У каждой схемы стоит отметка:

- **ПРОВЕРЕНО** — подтверждено официальной документацией или исходным кодом приложения (источник указан);
- **НЕ ПРОВЕРЕНО** — официального подтверждения не нашёл. Источник указан, использовать на свой риск и проверять на устройстве.

Проверка — 05.10.2026.

---

## 1. Как панель собирает ссылку

Шаблон задаётся в админке: **Подписки → Страница пользователя → Приложения на странице → «Шаблон ссылки «добавить»»** (`PlatformApp.add_link_template`). При отдаче страницы сервер подставляет за один проход (`page.go: addURL`):

| Подстановка | Что вставляется | Пример |
|---|---|---|
| `{url}` | ссылка подписки как есть | `https://panel.example.com/k3xq8/3f9k2v8q…` |
| `{url_enc}` | она же, percent-encoded (`url.QueryEscape`, пробел → `%20`) | `https%3A%2F%2Fpanel.example.com%2Fk3xq8%2F3f9k2v8q…` |
| `{name_enc}` | название подписки, percent-encoded | `Mistgate` → `Mistgate`, `Мой VPN` → `%D0%9C%D0%BE%D0%B9%20VPN` |

Правила проверки при сохранении (`subsettings.Validate`): шаблон начинается со схемы (`[A-Za-z][A-Za-z0-9+.-]{1,31}:`), без пробелов, до 1000 символов, только эти три подстановки. Схемы `javascript data vbscript file blob about` запрещены. Пустой шаблон — страница вместо «Добавить» предлагает «Скопировать ссылку». **Подстановки base64 нет**, поэтому приложения, которым нужна ссылка в base64 (Shadowrocket), в один тап не добавляются (см. 3.11).

## 2. Какой формат подписки получает приложение

Одна ссылка для всех; формат выбирает сервер по `User-Agent` (`subs/format.go: Choose`):

1. правила владельца по подстроке UA (**Подписки → Правила**). По умолчанию `mihomo` → **Mihomo YAML** и `clash` → **Mihomo YAML**;
2. браузер → страница (UA с `Mozilla/` и движком, без имён VPN-клиентов `happ, v2ray, clash, mihomo, hiddify, sing-box, streisand, shadowrocket, karing, flclash, nekobox, okhttp, cfnetwork`);
3. остальные → **base64-список** ссылок `hysteria2://…`.

| Формат | Что внутри | DNS | Только Happ |
|---|---|---|---|
| base64 | серверы Hysteria2 (имена по шаблону владельца) | нет | заголовок `routing` (DNS-профиль Happ), процент загрузки узла в имени, имя до 30 символов UTF-16 |
| Mihomo YAML | все протоколы пользователя: Hysteria2 и прокси AmneziaWG (неявное устройство получает свои ключи AWG), группа выбора, секция `dns` | общая секция `dns` + свой `dns` у каждого AWG-прокси | — |

Во всех форматах приходят заголовки `Profile-Title`, `Subscription-Userinfo` (трафик и срок), `Profile-Update-Interval` (по умолчанию 12 ч, у неактивного — 1 ч), `Profile-Web-Page-Url` (эта страница), `Announce`, `Support-Url`.

Если UA приложения на базе Mihomo не совпал ни с одним правилом, оно получит base64. Ядро Mihomo обычно разбирает и такие списки, но тогда не будет секции DNS и прокси AmneziaWG. Решение — правило в **Подписки → Правила** с подстрокой UA этого приложения → Mihomo YAML.

---

## 3. Приложения

Сводка (подробности ниже):

| Приложение | Платформы | Шаблон в синтаксисе панели | Формат | QR подписки | Схема |
|---|---|---|---|---|---|
| Happ | iOS, Android, Windows, macOS | `happ://add/{url}` | base64 + заголовки Happ | ? (на странице подсказка «+» → «Сканировать QR») | НЕ ПРОВЕРЕНО |
| **kl!ck** | Windows, macOS | `klick://add?url={url_enc}&name={name_enc}` | Mihomo YAML | — (компьютер) | **ПРОВЕРЕНО** (исходники) |
| AmneziaVPN | iOS, Android, Windows, macOS, Linux | — (ключ на устройство) | `vpn://`, `.conf`, QR | да (QR ключа) | не схема, см. 3.3 |
| Clash Verge Rev | Windows, macOS, Linux | `clash-verge://install-config?url={url_enc}&name={name_enc}` | Mihomo YAML | — | **ПРОВЕРЕНО** (исходники) |
| FlClash | Android, Windows, macOS, Linux | `flclash://install-config?url={url_enc}` | Mihomo YAML | ? | **ПРОВЕРЕНО** (README) |
| Clash Meta for Android | Android | `clashmeta://install-config?url={url_enc}&name={name_enc}` | Mihomo YAML | ? | **ПРОВЕРЕНО** (исходники) |
| Hiddify | Android, iOS, Windows, macOS, Linux | `hiddify://import/{url}#{name_enc}` | base64 или Mihomo YAML (по UA) | ? | **ПРОВЕРЕНО** (документация) |
| v2rayNG | Android | `v2rayng://install-sub?url={url_enc}` | base64 | ? | **ПРОВЕРЕНО** (исходники) |
| Streisand | iOS | `streisand://import/{url}#{name_enc}` | base64 | ? | НЕ ПРОВЕРЕНО |
| Shadowrocket | iOS | — (нужен base64, подстановки нет) | base64 | ? | НЕ ПРОВЕРЕНО |
| sing-box (SFI/SFA/SFM) | iOS, Android, macOS | `sing-box://import-remote-profile?url={url_enc}#{name_enc}` | **не подходит** | — | ПРОВЕРЕНО, но **не предлагать** |

«?» в колонке QR: сканер в приложении есть, но что он примет ссылку на подписку, не подтверждено. Камера телефона в любом случае откроет по QR **эту страницу**, а с неё сработает «Добавить одним нажатием».

### 3.1 Happ

- **Платформы**: iOS, Android, Windows, macOS (есть и Linux/TV, в настройках по умолчанию не заведены).
- **Шаблон**: `happ://add/{url}` — это шаблон по умолчанию у панели (`subsettings.Defaults`).
- **Отметка: НЕ ПРОВЕРЕНО.** На страницах документации Happ (`happ.su/main/dev-docs`: app-management, crypto-link, examples-of-links-and-parameters) `happ://add/` не описан: там есть только `happ://crypt4/` и `happ://crypt5/` и параметры подписки в заголовках. Схема `happ://add/` используется в открытом каталоге приложений страницы подписки другой панели (`app-config.json`) для iOS, Android и macOS, и на ней стоит текущий шаблон панели по умолчанию.
- **Формат**: base64. Узнаётся по подстроке `happ` в UA (`isHapp`). Только Happ получает заголовок `routing` (`happ://routing/onadd/<base64 JSON>`, DNS-профиль: один Remote и один Domestic DNS на всё устройство, `dns/happ.go`) и процент загрузки узла в имени сервера (« · 64%»).
- **QR**: на текущей странице подсказка «В Happ: „+“ → „Сканировать QR“». По документации не подтверждено.
- **Скачать** (`subsettings.Defaults`): iOS `https://apps.apple.com/us/app/happ-proxy-utility/id6504287215`, Android `https://play.google.com/store/apps/details?id=com.happproxy`, Windows `https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe`, macOS `https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.macOS.universal.dmg`.

### 3.2 kl!ck (наш клиент для Windows и macOS)

- **Платформы**: Windows, macOS.
- **Шаблон**: `klick://add?url={url_enc}&name={name_enc}` (основная форма, её строит панель). Вторая форма — `klick://add/{url}`, «как у Happ».
- **Отметка: ПРОВЕРЕНО** — исходники `crates/klick-core/src/deeplink.rs`:
  - `klick://add?url=<https, percent-encoded>&name=<необязательно>`, параметры в любом порядке, неизвестные пропускаются, первое значение побеждает;
  - `klick://add/<url>` — ссылка как есть или percent-encoded;
  - ссылка подписки **только `https://`** (`http` — только в отладочной сборке для `127.0.0.1`/`localhost`), до 2048 символов, без пробелов;
  - `name` — обычный текст до 64 символов, `+` в названии считается пробелом;
  - kl!ck **ничего не добавляет сам**: открывает экран «Добавить» с уже вставленной подпиской, человек нажимает одну кнопку. На экране показан только домен ссылки, токен скрыт;
  - схема `klick` зарегистрирована в `crates/klick-ui/tauri.conf.json` (`plugins.deep-link`).
- **Формат**: kl!ck скачивает подписку с `User-Agent: mihomo/1.19.31` (`crates/klick-service/src/subs.rs`, **проверено**) → правило панели `mihomo` → **Mihomo YAML** (Hysteria2 + AmneziaWG, секция DNS).
- **QR**: не нужен (компьютер). На компьютере страница показывает QR, чтобы открыть её на телефоне.
- **Скачать**: Windows `https://github.com/vbu00/klick/releases/latest` (страница релиза), macOS `https://github.com/vbu00/klick/releases/latest/download/klick-macos.pkg`.

#### Готовые записи для **Подписки → Страница пользователя → Добавить приложение**

**Windows**

| Поле в админке | Значение |
|---|---|
| Платформа | Windows |
| Принимает | По ссылке подписки |
| Название приложения | `kl!ck` |
| Ссылка на скачивание | `https://github.com/vbu00/klick/releases/latest` |
| Шаблон ссылки «добавить» | `klick://add?url={url_enc}&name={name_enc}` |
| Описание на карточке | `Все ваши серверы в одном приложении, подписка обновляется сама` |
| Рекомендуем | да |

**macOS**

| Поле в админке | Значение |
|---|---|
| Платформа | macOS |
| Принимает | По ссылке подписки |
| Название приложения | `kl!ck` |
| Ссылка на скачивание | `https://github.com/vbu00/klick/releases/latest/download/klick-macos.pkg` |
| Шаблон ссылки «добавить» | `klick://add?url={url_enc}&name={name_enc}` |
| Описание на карточке | `Все ваши серверы в одном приложении, подписка обновляется сама` |
| Рекомендуем | да |

Если kl!ck рекомендуется на компьютерах, снимите «Рекомендуем» с Happ на Windows и macOS и поднимите kl!ck выше Happ (стрелки ↑↓). Тогда kl!ck станет первым и главным, а Happ уйдёт в «Другие приложения».

То же в виде документа настроек (protojson, `SubscriptionSettings.apps[]`, например для API или MCP):

```json
[
  { "platform": "PLATFORM_WINDOWS", "kind": "APP_HAPP", "name": "kl!ck",
    "download_url": "https://github.com/vbu00/klick/releases/latest",
    "add_link_template": "klick://add?url={url_enc}&name={name_enc}",
    "description": "Все ваши серверы в одном приложении, подписка обновляется сама",
    "recommended": true },
  { "platform": "PLATFORM_MACOS", "kind": "APP_HAPP", "name": "kl!ck",
    "download_url": "https://github.com/vbu00/klick/releases/latest/download/klick-macos.pkg",
    "add_link_template": "klick://add?url={url_enc}&name={name_enc}",
    "description": "Все ваши серверы в одном приложении, подписка обновляется сама",
    "recommended": true }
]
```

(`APP_HAPP` — это вид «по ссылке подписки», имя перечисления осталось с тех пор, как такое приложение было одно. Описание — 62 символа при лимите 80.)

Проверка после сохранения: в предпросмотре страницы как пользователь на Windows кнопка «Добавить в kl!ck» должна вести на `klick://add?url=https%3A%2F%2F…&name=…`, а `name` равен названию подписки.

### 3.3 AmneziaVPN (ключ на устройство)

- **Платформы**: iOS, Android, Windows, macOS, Linux.
- **Вид**: «Ключ AmneziaWG» (`APP_AMNEZIA`). Шаблон не нужен и не используется: подписки у этого вида нет, ключ выдаётся на каждое устройство через «Мои устройства».
- **Что выдаёт панель** (`/devices/<id>/configs`, по одному на каждый сервер профиля), **проверено** по коду панели:
  - `vpn_key` — строка `vpn://…` для AmneziaVPN («+» → вставить ключ → «Продолжить»);
  - `conf` — текст `.conf` AmneziaWG (`[Interface]` с `PrivateKey`, `Address`, `DNS`, параметрами маскировки `Jc/Jmin/Jmax/S1–S4/H1–H4/I1`; `[Peer]`), файл `filename` вида `mistgate-de.conf`. Его импортируют AmneziaVPN («+» → «Файл с настройками подключения») и приложения AmneziaWG;
  - **QR** — тот же текст `conf` как QR-код (AmneziaVPN: «+» → «QR-код»). Если не помещается, страница предлагает файл или ключ;
  - предупреждение `amnezia_desktop_mtu`: на компьютере давать файл, а не ключ;
  - минимальные версии (`min_clients`), для AWG 3.1 по образцу: AmneziaVPN 5.0.1.5, AmneziaWG Android 3.1.20260814.
- **Скачать** (`subsettings.Defaults`): iOS `https://apps.apple.com/us/app/amneziavpn/id1600529900`, Android `https://play.google.com/store/apps/details?id=org.amnezia.vpn`, компьютеры `https://amnezia.org/downloads`.

### 3.4 Clash Verge Rev

- **Платформы**: Windows, macOS, Linux.
- **Шаблон**: `clash-verge://install-config?url={url_enc}&name={name_enc}`. Работает и `clash://install-config?url={url_enc}&name={name_enc}`, но `clash://` регистрируют несколько Clash-приложений, и система откроет любое из них. Своя схема надёжнее.
- **Отметка: ПРОВЕРЕНО** — исходники `src-tauri/src/utils/resolve/scheme.rs` (ветка `dev`): принимаются схемы `clash` и `clash-verge`, путь не проверяется, берутся параметры `url` (до двух раундов percent-декодирования) и `name`.
- **Формат**: Mihomo YAML, если UA содержит `clash`. Обычно это так (`clash-verge/v…`), но строку UA в исходниках я **не проверял**.
- **Скачать**: `https://github.com/clash-verge-rev/clash-verge-rev/releases/latest`.

### 3.5 FlClash

- **Платформы**: Android, Windows, macOS, Linux.
- **Шаблон**: `flclash://install-config?url={url_enc}`.
- **Отметка: ПРОВЕРЕНО** — README `https://github.com/chen08209/FlClash`: «`clash://install-config?url=<URL-encoded subscription link>` … схемы `clashmeta://` и `flclash://` работают так же». Параметр `name` в README не упомянут, поэтому в шаблон не включён.
- **Формат**: Mihomo YAML, если UA содержит `clash` или `mihomo` (имя `FlClash` подстроку `clash` содержит, но UA **не проверял**).
- **Скачать**: `https://github.com/chen08209/FlClash/releases/latest`.

### 3.6 Clash Meta for Android (CMFA)

- **Платформы**: Android.
- **Шаблон**: `clashmeta://install-config?url={url_enc}&name={name_enc}` (работает и `clash://…`).
- **Отметка: ПРОВЕРЕНО** — исходники `MetaCubeX/ClashMetaForAndroid`: `AndroidManifest.xml` (схемы `clash`, `clashmeta`, host `install-config`) и `ExternalControlActivity.kt` (параметры `url`, `name`, `type`; по умолчанию профиль-ссылка, который обновляется сам; открывается экран свойств профиля).
- **Формат**: Mihomo YAML, если UA содержит `clash` (UA **не проверял**).
- **Скачать**: `https://github.com/MetaCubeX/ClashMetaForAndroid/releases/latest`.

### 3.7 Hiddify

- **Платформы**: Android, iOS, Windows, macOS, Linux.
- **Шаблон**: `hiddify://import/{url}#{name_enc}`.
- **Отметка: ПРОВЕРЕНО** — документация `https://hiddify.com/app/URL-Scheme/`: `hiddify://import/<sublink>#name`, принимает подписки Clash, sing-box, V2ray (base64) и одиночные ссылки.
- **Формат**: UA содержит `hiddify` → не браузер. Если в UA есть ещё и `clash` (у части версий так), правило отдаст Mihomo YAML, иначе base64. Hiddify читает оба. UA **не проверял**.

### 3.8 v2rayNG

- **Платформы**: Android.
- **Шаблон**: `v2rayng://install-sub?url={url_enc}`.
- **Отметка: ПРОВЕРЕНО** — исходники `2dust/v2rayNG`: `AndroidManifest.xml` (схема `v2rayng`, host `install-config` и `install-sub`), `UrlSchemeActivity.kt` (параметр `url`; оба host обрабатываются одинаково), `AngConfigManager.kt` (http(s)-ссылка → `importUrlAsSubscription`, имя подписки берётся из `#фрагмента` самой ссылки, иначе «import sub»). Отдельного параметра `name` нет. Вариант `?url={url_enc}%23{name_enc}` (имя во фрагменте ссылки) теоретически даст имя, но **не проверен**.
- **Формат**: base64 (`v2ray` в UA → не браузер, правил нет). Поддержку `hysteria2://` в конкретной версии v2rayNG **не проверял**.

### 3.9 Streisand

- **Платформы**: iOS.
- **Шаблон**: `streisand://import/{url}#{name_enc}`.
- **Отметка: НЕ ПРОВЕРЕНО.** Официальной документации не нашёл. `streisand://import/` + ссылка используется в каталоге приложений другой панели. Форма `streisand://import/URL#NAME` описана в пересказе документации Marzban (`deepwiki.com/sm1ky/marzban-docs`, раздел URL schemes).
- **Формат**: base64 (`streisand` в UA).
- **Скачать**: `https://apps.apple.com/app/streisand/id6450534064`.

### 3.10 sing-box (SFI, SFA, SFM)

- **Схема ПРОВЕРЕНА** (`https://sing-box.sagernet.org/clients/general/`): `sing-box://import-remote-profile?url=urlEncodedURL#urlEncodedName`, в синтаксисе панели `sing-box://import-remote-profile?url={url_enc}#{name_enc}`.
- **Не предлагать.** Удалённый профиль sing-box — это **конфигурация sing-box в JSON**, а панель такой формат не отдаёт (форматы: base64, страница, приманка, Mihomo YAML; `subscription.proto: SubFormat`). sing-box получит base64-список (`sing-box` в UA) и не сможет его открыть. Чтобы поддержать, нужен новый формат `SUB_FORMAT_SINGBOX_JSON`.

### 3.11 Shadowrocket

- **Платформы**: iOS.
- **Шаблон**: оставить **пустым**. Страница предложит «Скопировать ссылку», а в Shadowrocket ссылку вставляют через «+» (тип «Subscribe»).
- **Отметка: НЕ ПРОВЕРЕНО.** В каталоге приложений другой панели для Shadowrocket указано `"urlScheme": "sub://"` с `"isNeedBase64Encoding": true`, то есть ссылка вида `sub://<base64 ссылки подписки>`. Официальной документации не нашёл. В синтаксисе панели это **невыразимо**: нет подстановки base64. Если нужно, добавить `{url_b64}` (DATA.md, 2.6), тогда шаблон будет `sub://{url_b64}` — тоже непроверенный.
- **Формат**: base64 (`shadowrocket` в UA).

---

## 4. Что не проверено и как проверить

| Что | Как проверить |
|---|---|
| `happ://add/{url}` (Happ) | на iPhone и Android: нажать «Добавить в Happ» на странице и убедиться, что подписка добавилась |
| `streisand://import/{url}#{name_enc}` | на iPhone со Streisand |
| `sub://<base64>` (Shadowrocket) | только после добавления `{url_b64}` |
| UA Clash Verge Rev, FlClash, CMFA, Hiddify, v2rayNG, Shadowrocket | добавить подписку и посмотреть, что пришло: если в профиле Clash-приложения есть серверы AmneziaWG и своя секция DNS, это Mihomo YAML; если только Hysteria2, это base64 и UA не совпал. Тогда добавить правило в **Подписки → Правила** с подстрокой UA этого приложения |
| Сканер QR в приложениях для ссылки подписки (не страницы) | сканером внутри приложения навести на QR страницы на компьютере |
| Поддержка `hysteria2://` в v2rayNG и Streisand | добавить подписку и проверить, что серверы появились |
