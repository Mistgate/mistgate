---
title: API админки
description: Connect-RPC API, на котором работает админка, как звать его через curl и сгенерированный клиент, API-токены и их профили и что токену можно, а что нельзя.
---

Админка общается с панелью через Connect-RPC API. Скрипты пользуются тем же API — с теми же проверками ролей, валидацией и журналом аудита, — только вместо cookie сессии присылают API-токен. ИИ-агенты используют те же токены через [MCP-сервер](mcp.md).

## Где находится API

Каждая процедура — POST на адрес админки плюс `api/` и путь процедуры:

```text
<адрес админки>api/mistgate.admin.v1.<Service>/<Method>
https://panel.example.com/<prefix>/api/mistgate.admin.v1.UserService/ListUsers
```

Адрес админки — тот, что напечатал `mistgate setup` (владельцу его показывает **Настройки → Домены**): с секретным префиксом, на секретном хосте или на отдельном адресе. API доступен только там; на публичной стороне тот же путь отдаёт сайт-ширму.

Обработчики — это обработчики connect-go, поэтому работают протоколы Connect (JSON или бинарный protobuf), gRPC и gRPC-Web. Примеры здесь используют Connect с JSON.

## Сервисы

API описан в `proto/mistgate/admin/v1/`. Сгенерированный код лежит в репозитории: Go в `gen/mistgate/admin/v1` (пакеты `adminv1` и `adminv1connect`), TypeScript в `web/src/gen`.

| Сервис | Файл | Что охватывает |
|---|---|---|
| `AuthService` | `auth.proto` | Вход, passkey, пароль, сессии, повторное подтверждение, журнал аудита, настройки капчи |
| `InstanceService` | `instance.proto` | Оформление, адреса админки и подписок |
| `FleetService` | `fleet.proto` | Обзор и лента событий |
| `NodeService` | `node.proto` | Ноды: список, подробности, настройки, команды установки, перезапуски, логи, вывод из флота |
| `ProfileService` | `profile.proto` | Протоколы, профили и профили на нодах |
| `GroupService` | `group.proto` | Группы |
| `UserService` | `user.proto` | Пользователи, их трафик, устройства и ссылка подписки |
| `DeviceService` | `device.proto` | Устройства AmneziaWG и их ключи |
| `SubscriptionService` | `subscription.proto` | Настройки подписок и правила для приложений |
| `DnsService` | `dns.proto` | DNS-пресеты |
| `HealthService` | `health.proto` | Алерты, проверки глазами клиента, доктор и его исправления |
| `UpdateService` | `update.proto` | Пакет релиза, обновления нод, расписания и раскатки, обновления панели |
| `ProvisioningService` | `provisioning.proto` | Установка нод по SSH и сохранённый SSH-доступ к нодам |
| `WarpService` | `warp.proto` | Аккаунты WARP нод |
| `AwgService` | `awg.proto` | Помощники редактора профиля AmneziaWG |
| `BackupService` | `backup.proto` | Зашифрованные бэкапы панели |
| `ApiTokenService`, `ApprovalService` | `integrations.proto` | API-токены и одобрения владельца |

Справочник по каждому полю — комментарии в proto-файлах. Соглашения (`common.proto`):

- Идентификаторы — непрозрачные строки с префиксом: `nod_…`, `usr_…`, `prf_…`, `grp_…`, `dev_…`, `inb_…` (профиль на ноде).
- Время — Unix-секунды в полях с окончанием `_unix`; 0 — не задано. Счётчики байтов — беззнаковые 64-битные.
- В JSON имена полей в lowerCamelCase (`pageSize`), перечисления — по именам (`USER_FILTER_ONLINE`), 64-битные целые — строками (на входе числа тоже принимаются).
- Изменения используют optional-поля: отсутствующее поле не меняется.
- Ошибки — коды Connect с короткими сообщениями: `not_found`, `already_exists`, `invalid_argument`, `failed_precondition`, `aborted` (кто-то изменил раньше), `permission_denied`, `unauthenticated`, `resource_exhausted`.

## API-токены

Владелец создаёт токены в **Интеграции → API-токены → Новый токен** (нужно свежее повторное подтверждение входа):

| Поле | Правило |
|---|---|
| Название | От 1 до 64 символов, без повторов среди действующих токенов без учёта регистра. Именно его показывает журнал аудита. |
| Доступ | Профиль: **Только чтение**, **Оператор** или **Админ**. |
| Срок жизни | По умолчанию 90 дней, не больше 365. Токен всегда истекает; чтобы продлить, создайте новый и отзовите старый. |
| Запросов в минуту | По умолчанию 120, от 1 до 600. |

Секрет выглядит как `tk1_` и 43 символа, безопасных для URL. Его показывают один раз; панель хранит только его SHA-256 и последние четыре символа (чтобы различать токены в списке). Одновременно может быть не больше 50 действующих токенов. Список показывает, когда и откуда каждый токен использовался последний раз и через что (API или MCP).

**Отозвать** останавливает токен сразу: следующий его запрос отклоняется, а изменения, которые он запланировал через MCP и ещё не применил, отменяются. Отзыв требует повторного подтверждения.

### Аутентификация

Передавайте токен в одном заголовке `Authorization` со схемой Bearer:

```text
Authorization: Bearer tk1_...
```

Запрос с заголовком Bearer оценивается только по токену; cookie не смотрятся. Отказы:

| Статус | `code` в ответе | Сообщение |
|---|---|---|
| 401 | `unauthenticated` | `not signed in` (токена нет, он искажён или неизвестен), `token revoked`, `token expired` |
| 403 | `permission_denied` | `this call is not available to API tokens`, `this token's profile cannot do this`, `this needs the owner's approval; it is not available over the API`, `this is used only while the MCP server makes a plan; it is not available over the API` |
| 429 | `resource_exhausted` | `too many requests for this token, slow down`, с `Retry-After` |

## Вызов API

### Через curl

```sh
TOKEN=$(head -n1 ~/.config/mistgate/token)
ADMIN=https://panel.example.com/<prefix>/

curl -sS "${ADMIN}api/mistgate.admin.v1.UserService/ListUsers" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"filter": "USER_FILTER_ONLINE", "pageSize": 20}'
```

Изменение выглядит так же. Продлить двух пользователей на 30 дней (профиль «Оператор» и выше):

```sh
curl -sS "${ADMIN}api/mistgate.admin.v1.UserService/ExtendUsers" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"userIds": ["usr_...", "usr_..."], "days": 30}'
```

Ошибка приходит в JSON с HTTP-статусом ошибки:

```json
{"code": "permission_denied", "message": "this token's profile cannot do this"}
```

### Через сгенерированный клиент

Пакеты Go импортируются из модуля `github.com/mistgate/mistgate`; закрепите тег релиза или коммит, потому что до 1.0 API ещё может меняться. Базовый адрес клиента — адрес админки плюс `api`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// bearer adds the API token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func main() {
	raw, err := os.ReadFile(os.Getenv("MISTGATE_TOKEN_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	token := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	hc := &http.Client{Transport: bearer{token}}

	// The admin URL that `mistgate setup` printed, plus "api".
	base := "https://panel.example.com/<prefix>/api"
	users := adminv1connect.NewUserServiceClient(hc, base)

	resp, err := users.ListUsers(context.Background(), connect.NewRequest(&adminv1.ListUsersRequest{PageSize: 10}))
	if err != nil {
		log.Fatal(connect.CodeOf(err), err)
	}
	for _, u := range resp.Msg.GetUsers() {
		fmt.Println(u.GetId(), u.GetName(), u.GetStatus())
	}
}
```

Для другого языка сгенерируйте клиент из `proto/` командой `buf generate` с плагином Connect или gRPC для этого языка.

## Профили токенов

Профиль работает как роль админа: **Только чтение** — как «Только чтение», **Оператор** — как «Помощник», **Админ** — как «Владелец». Сверх роли токену доступны только процедуры из фиксированного списка разрешений (`internal/panel/auth/policy_tokens.go`); всё остальное отклоняется при любом профиле. Ни одна процедура из списка не возвращает учётных данных.

| Процедура | Только чтение | Оператор | Админ |
|---|---|---|---|
| `FleetService.Overview`, `FleetService.ListEvents` | да | да | да |
| `NodeService.ListNodes`, `NodeService.GetNode` | да | да | да |
| `HealthService.ListAlerts`, `GetChecks`, `GetDoctor` | да | да | да |
| `UserService.ListUsers`, `UserService.GetUser` | да | да | да |
| `GroupService.ListGroups` | да | да | да |
| `ProfileService.ListProfiles`, `ProfileService.GetProfile` (секреты скрыты) | да | да | да |
| `SubscriptionService.ListClients`, `SubscriptionService.TestUserAgent` | да | да | да |
| `UpdateService.GetUpdates` | да | да | да |
| `UserService.CreateUser`, `UpdateUser`, `SetUsersEnabled`, `ExtendUsers`, `ResetUserTraffic`, `RevokeDevice` | нет | да | да |
| `HealthService.MuteAlert`, `RunChecksNow`, `RunDoctor` | нет | да | да |
| `AuthService.ListAudit` | нет | нет | да |
| `HealthService.ApplyFix` | нет | нет | только через одобренный план MCP |
| `UpdateService.StartRollout`, `PauseRollout`, `ResumeRollout`, `CancelRollout`, `RollbackNode` | нет | нет | только через одобренный план MCP |
| `UpdateService.ScheduleNodeUpdate`, `CancelNodeUpdateSchedule`, `SetUpdateTimezone` | нет | нет | только через одобренный план MCP |
| `ProvisioningService.ListNodeServerAccess` (данные подключения, без пароля) | нет | нет | да |
| `ProvisioningService.GetSSHFingerprint` | нет | нет | только пока MCP-сервер составляет план `node_install` |
| `ProvisioningService.StartNodeProvision`, `RotateNodeServerPassword` | нет | нет | только через одобренный план MCP |

Закрыто для любого токена: удаление пользователей, ссылка подписки, устройства и их ключи, подключение нод, их настройки, перезапуски, логи и вывод из флота, подготовка модуля ядра AmneziaWG, шаги установки по SSH из админки (проверка сервера, повтор задания, показ и удаление сохранённого доступа), любые изменения профилей, групп, DNS-пресетов и настроек подписок, WARP, бэкапы, обновления панели и пересмотр пакета, оформление, токены, одобрения, собственный аккаунт админа (passkey, пароль, сессии, повторное подтверждение) и настройки капчи.

## Изменения, которым нужен владелец

Токен никогда не проходит повторное подтверждение. Процедуры, которым оно нужно или которые меняют то, что работает на нодах, напрямую токену недоступны; через обычный API они отвечают 403 «this needs the owner's approval; it is not available over the API».

Добраться до них можно только через [MCP-сервер](mcp.md) с токеном профиля «Админ»: агент планирует изменение, владелец читает план в **Интеграции → Ждут тебя** и одобряет его (со своим повторным подтверждением), и только после этого применение агента делает этот один вызов. Одобрение проверяется в базе при каждом использовании и открывает только процедуру этого плана. План истекает через 10 минут после создания.

## Ограничения

- **Запросов в минуту на токен**: сколько задано у токена (от 1 до 600, по умолчанию 120), подряд — до 30. Сверх лимита — 429 с `Retry-After`.
- **На сеть клиента** на стороне админки: 30 запросов в секунду, подряд до 200 — одинаково для cookie и токенов.
- **Размер запроса**: от 64 КиБ до 1 МиБ в зависимости от сервиса.
- **Срок жизни**: каждый токен истекает (не позже чем через 365 дней).

## Аудит

Каждый вызов токеном пишет строку в журнал аудита: «вызвал(а) <процедура>», HTTP-статус, адрес клиента и автор «API-токен <имя>» (или «токен MCP <имя>» через MCP). Удачные чтения пишутся не чаще раза в минуту на процедуру; изменения и отказы — всегда. Строки самой процедуры (например, «создал(а) пользователя …») пишутся так же, как для админа.

## Чего никогда нет в ответах

- `CreateUser`, вызванный токеном, возвращает пользователя без ссылки подписки и пароля страницы. `GetSubscriptionLink` для токенов закрыт.
- Секреты профилей приходят скрытыми (`••••`) для любого вызывающего.
- Ключи и конфигурации устройств (`DeviceService`) и аккаунты WARP для токенов закрыты.
- Секрет токена после создания не возвращается никогда, даже владельцу.
- В параметрах аудита нет секретов.
