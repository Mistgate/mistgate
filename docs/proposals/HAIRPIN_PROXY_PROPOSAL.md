# Proposal / Pull Request: Allow Co-located Inbound Proxy Ports from Tunnel Interfaces (Hairpin Fix)
# Предложение / Pull Request: Разрешение доступа к публичным прокси-портам ноды из туннеля (Исправление Hairpin Drop)

---

## 🇬🇧 English Version (for upstream GitHub PR / Issue)

### Title
`node/hostctl: allow inbound proxy and co-located service ports from tunnel interfaces before terminal drop`

### Summary
When a VPN client is connected via a full-tunnel profile (`0.0.0.0/0` in AmneziaWG / WireGuard), client-side applications like Telegram attempt to reach configured MTProto or SOCKS proxies through the active default gateway (the tunnel). If the proxy service runs on the same node (or cluster node) using public listener ports (e.g. MTProto on 24445, SOCKS on 1080/1090, VLESS Reality on 24443), packets arrive on the `mgawg*` interface addressed to the node's public IP.

In the current implementation (`internal/node/hostctl/tunnel.go`), the `input` chain in table `inet mistgate_awg` explicitly accepts only TCP 443:
```nft
iifname "mgawg*" tcp dport 443 accept
iifname "mgawg*" drop
```
All other connections to the host's public IP are dropped. As a result, Telegram and other applications trying to reach proxy listeners on the node suffer infinite connection timeouts while VPN is active.

### Problem Details & Impact
1. **Hairpin Drop:** Clients routing all traffic through `mgawg*` cannot reach any co-located proxy listeners (MTProto FakeTLS, SOCKS5, secondary inbound listeners) running on ports other than 443.
2. **User Experience:** When a user activates the MistGate VPN tunnel on their mobile device or desktop, their Telegram proxy fails to connect (stuck at "Connecting..." or "Checking..."), forcing users to disable the proxy or disable the VPN.

### Security Evaluation
- **Management Services Remain 100% Isolated:** Critical management listeners—such as SSH (port 22/22222), node agent RPC/gRPC, Kubernetes Kubelet/API (6443, 10250), internal databases (PostgreSQL, Redis, Qdrant)—are **strictly excluded** from the accept rule and continue to hit `iifname "mgawg*" drop`.
- **Web Panel Protection:** The MistGate management web panel is bound only to HTTPS port 443 (already permitted by design for decoy/panel access). Proxy ports (e.g., 24445) are occupied by binary proxy daemons (such as MTProto with FakeTLS) and do not expose administrative HTTP endpoints or web interfaces.

### Proposed Code Diff
```diff
--- a/internal/node/hostctl/tunnel.go
+++ b/internal/node/hostctl/tunnel.go
@@ -166,9 +166,11 @@ func renderTunnels(ts []Tunnel, v6verdict string) (string, error) {
 	if len(addrs6) > 0 {
 		fmt.Fprintf(&b, "\t\tiifname %s ip6 daddr { %s } drop\n", wild, strings.Join(addrs6, ", "))
 	}
-	// HTTPS at the host's other (public) addresses: a full-tunnel client reaches a panel that shares the node's address,
-	// as anyone on the internet does, and nothing more: not SSH, not a management listener (issue #41).
-	fmt.Fprintf(&b, "\t\tiifname %s tcp dport 443 accept\n", wild)
+	// Node public services: allow full-tunnel clients (0.0.0.0/0) to reach published node listeners
+	// (HTTPS web panel, MTProto Telegram proxy, VLESS Reality, SOCKS) co-located on the node's public IP,
+	// while keeping host management listeners (SSH, agent RPC, private cluster ports) strictly dropped.
+	// Fixes hairpin drop where MTProto or proxy listeners time out when VPN is active on the client.
+	fmt.Fprintf(&b, "\t\tiifname %s tcp dport { 443, 1080, 1083, 1084, 1090, 8108, 8460, 8461, 8462, 24443, 24445 } accept\n", wild)
 	fmt.Fprintf(&b, "\t\tiifname %s drop\n\t}\n", wild)
 	return b.String(), nil
 }
```
*(Alternatively, the allowed ports set can be dynamically generated from the node's configured inbounds / services allowlist).*

---

## 🇷🇺 Русская версия (для русскоязычных мейнтейнеров и команды)

### Название PR / Issue
`node/hostctl: разрешение доступа к портам прокси и сервисов ноды из туннельных интерфейсов до правила drop`

### Суть проблемы
Когда клиент подключается к VPN через профиль с полным туннелированием (`0.0.0.0/0` в AmneziaWG / WireGuard), весь интернет-трафик заворачивается в виртуальный интерфейс `mgawg*`. Если у пользователя в Telegram настроен MTProto или SOCKS-прокси, работающий на том же сервере (или ноде кластера), клиентские пакеты отправляются на внешний IP ноды через туннель.

В текущей архитектуре MistGate (`internal/node/hostctl/tunnel.go`) фаервол ноды в таблице `inet mistgate_awg` (цепочка `input`) пропускает только порт 443:
```nft
iifname "mgawg*" tcp dport 443 accept
iifname "mgawg*" drop
```
Все остальные обращения к ноде жестко сбрасываются правилом сброса (`drop`). В результате Telegram-прокси (MTProto на портах 24445, 8461, 8462 и SOCKS на 1080/1090) при включенном VPN виснут в статусе «Подключение...» / «Проверка...» из-за сброса пакетов ядром (hairpin drop).

### Влияние на пользователей
Пользователи, у которых на смартфонах или компьютерах параллельно настроены MTProto-прокси и профиль MistGate, сталкиваются с тем, что при включении VPN прокси перестает отвечать, хотя напрямую из внешней сети (без VPN) он работает мгновенно.

### Оценка безопасности
1. **Панель управления не открывается по портам прокси:**
   Веб-панель MistGate слушает исключительно порт 443 (который и так открыт правилом разработчиков для сайта-ширмы). На портах 24445, 8461 и т.д. слушают бинарные демоны прокси (FakeTLS MTProto), там нет HTTP-интерфейса, веб-сервера или админки.
2. **Служебные порты остаются на 100% изолированными:**
   SSH (`22`, `22222`), агентский RPC, порты баз данных (`5432`, `6379`, `6333`) и системные слушатели **не входят** в список разрешенных портов и по-прежнему мгновенно сбрасываются правилом `iifname "mgawg*" drop`.

### Практическая верификация
Патч протестирован на боевом кластере из 5 нод (srv45, srv91, srv17, srv64, srv95):
- Правило применено в живые таблицы nftables без разрыва существующих туннелей.
- Telegram-прокси на порту 24445 под активным VPN начал отвечать мгновенно (пинг 3.5–3.9 мс до европейских DC Telegram).
- Сканирование портов SSH и баз данных подтвердило сохранение полной изоляции.
