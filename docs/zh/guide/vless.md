---
title: VLESS REALITY
description: "VLESS 协议配置：通过 TCP 和 XHTTP 使用 REALITY，或通过 XHTTP 和 WebSocket 使用 TLS，可直连或经 CDN 连接。了解何时选择它而不是 Hysteria2、各项设置、REALITY 目标、端口，以及应用会收到和需要什么。"
---

VLESS 是一种 TCP 协议。使用 REALITY 时，节点会向非客户端访问者显示一个真实网站，即**目标**：默认是节点自身的站点，也可以是你指定的其他站点，因此探测方会看到一个真实存在的网站所做的 TLS 握手。持有正确密钥的用户会在同一握手过程中被识别，并获得隧道。本页列出 VLESS 协议配置的设置及其作用；如何创建协议配置并部署到节点，请参阅[协议配置](profiles.md)；如何让它与 Hysteria2 共用节点，请参阅[在同一节点上运行 VLESS 和 Hysteria2](vless-and-hysteria2.md)。

> **注意：** VLESS 支持通过 TCP 和 XHTTP 使用 REALITY，也支持使用 XHTTP + TLS 和 WebSocket + TLS。TLS 可通过 ACME 域名证书直连，也可经 CDN 连接。

## 何时选择 VLESS

Hysteria2 和 VLESS 可以同时放在一个订阅中；应用（或用户）会使用能够连接的协议。

| | Hysteria2 | VLESS REALITY |
|:--|:--|:--|
| 传输方式 | UDP（QUIC） | TCP |
| 适用场景 | 丢包和远距离链路：QUIC 恢复速度更快 | UDP 和 QUIC 被限速或封锁的网络；许多移动网络和办公网络都如此 |
| 证书和域名 | Let’s Encrypt 需要域名；自签名证书可用于 IP 地址 | 目标为节点自身站点（默认值）时，需要带 Let’s Encrypt 证书的域名。目标为其他站点时，节点无需配置证书：只有 IP 地址的节点也可使用 |
| 端口 80 | 用于 Let’s Encrypt | 不使用 |
| 节点开销 | 低 | 较高：节点会在用户态复制每个字节；VLESS 传输方式中，使用 Vision 的 TCP 开销较低 |

先使用 Hysteria2，再为网络连接有问题的用户添加 VLESS；也可以先让少数用户试用：请参阅[让少数用户试用 VLESS](vless-and-hysteria2.md#让少数用户试用-vless)。

## 设置

编辑器将设置分为**基础**、**混淆**和**高级**。更改关键设置后，用户现有的服务器会失效，直到应用更新订阅；编辑器保存前会先询问（请参阅[协议配置](profiles.md)）。

| 设置 | 默认值 | 是否关键 | 作用 |
|:--|:--|:--|:--|
| **TCP 端口** | 443 | 是 | 节点监听的 TCP 端口。范围为 1 至 65535，但不能使用 80。 |
| **传输方式** | XHTTP | 是 | **XHTTP**、**TCP（RAW + Vision）**，或搭配 TLS 的 **WebSocket**。TCP 始终启用 Vision。 |
| **传输层安全** | REALITY | 是 | **REALITY**或**TLS**。使用 Vision 的 TCP 只能搭配 REALITY；WebSocket 只能搭配 TLS。 |
| **源站证书** | Let’s Encrypt 域名 | 是 | 仅适用于 TLS：直连时使用 ACME 域名证书。仅当 CDN 接受自签名证书时才可使用该证书。 |
| **源站服务器名称** | 节点域名 | 是 | 仅适用于 TLS：节点证书中的名称。入站的域名（SNI）覆盖值优先。 |
| **CDN 主机名和端口** | 留空，443 | 是 | 仅适用于 TLS：客户端连接此端点，并将 CDN 主机名同时用作 Host 和 SNI。源站连接仍使用 TLS。 |
| **向其他访问者展示的站点** | 本节点站点 | 是 | **本节点站点**：使用节点自身 Let’s Encrypt 证书的站点。**其他站点**：你指定的真实站点。见下文。 |
| **目标（主机:端口）** | 无 | 是 | 仅适用于其他站点：REALITY 向其他访问者显示的站点，例如 `www.example.org:443`。选择其他站点时必填；选择本节点站点时不显示。 |
| **服务器名称** | 留空 | 是 | 仅适用于其他站点：最多填写 8 个、且目标证书对其有效的主机名。留空时使用目标主机名。目标是 IP 地址时必填。不能使用通配符。 |
| **Short ID** | 两个，已生成 | 是 | 填写 1 至 8 个偶数长度的十六进制值，每个值最多 16 个字符。每台设备会使用其中一个值。 |
| **客户端指纹** | Chrome | 否 | 客户端 TLS 握手所模拟的浏览器：Chrome、Firefox、Safari、iOS、Edge 或随机。 |
| **XHTTP 路径** | 已生成 | 是 | 以 `/` 开头，最多 64 个字符，只能包含字母、数字和 `/ _ -`。新建协议配置时会随机生成路径。 |
| **XHTTP Host 请求头** | 留空 | 是 | 留空表示不设置。 |
| **XHTTP 模式** | 自动 | 否 | **自动**、**Packet up**、**Stream up**或**Stream one**。由客户端选择；节点接受所有模式。 |
| **XHTTP 填充** | 100–1000 | 是 | 每个请求添加的随机填充，格式为 `min-max`，范围为 1 至 4096 字节。 |
| **WebSocket 路径和主机** | 已生成，留空 | 是 | 仅适用于 TLS。服务器主机名留空；客户端配置了 CDN 主机名时使用该名称，否则使用可选的 WebSocket Host 值。 |
| **出口** | 直连 | 否 | **直连**或**WARP**。请参阅 [WARP](warp.md)。 |

XHTTP 设置仅适用于 XHTTP 传输方式。设备使用的服务器名称和 Short ID 固定不变，因此每次更新订阅时，其链接都不会改变。

## TLS 和 CDN

如需直连或经 CDN 连接，请选择使用 XHTTP 或 WebSocket 的 TLS。直连需要 ACME 域名证书。端口可用时，节点可以通过 TCP 443 响应 ACME TLS-ALPN-01 检查，也可以通过 TCP 80 响应 HTTP-01 检查。证书会按需签发和续期，不需要重启入站。

使用 CDN 时，请设置其主机名和端口。客户端连接到此端点，并将 CDN 主机名同时作为 Host 和 SNI 发送。CDN 会通过 TLS 连接节点；Mistgate 不会将源站配置为普通 HTTP。由于部分 CDN 会缓存完整请求正文，XHTTP 经 CDN 连接时默认使用 **Packet up**。只有 CDN 会流式传输请求正文时，才使用 **Stream up**。XHTTP 无法与 CDN 配合使用时，可改用 WebSocket + TLS。

自签名源站证书只适用于已配置为接受未经验证源站证书的 CDN，不适用于客户端直连节点。

每台设备只有一个 VLESS 身份（UUID），该设备的所有 VLESS 服务器共用这个 UUID。用户下次更新订阅时会生成 UUID。节点会按原样保存 UUID，因为 VLESS 会自行比对它；Hysteria2 则不同，节点只保存其哈希值。

## VLESS 经 CDN 连接

CDN 会在客户端和节点之间使用自己的地址：客户端连接 CDN，CDN 再连接节点。节点地址被封锁、CDN 地址未被封锁时，这种方式会有帮助。

在协议配置中选择**传输层安全：TLS**和一种传输方式（XHTTP 或 WebSocket），然后填写客户端连接所用的**CDN 主机名**，例如 `cdn.example.org`。所有链接都会使用该名称作为地址、服务器名称（SNI）和 Host 请求头；XHTTP 会以 **Packet up** 模式启动。节点仍使用自身的证书名称；客户端不会使用该名称。

CDN 设置：

1. **源站**：使用节点自身的名称（协议配置的**服务器名称**（SNI）或节点域名）和协议配置的 TCP 端口。将此名称的 DNS 记录设为**仅 DNS**（DNS only），不要通过 CDN 代理：CDN 和 Let’s Encrypt 都需要连接节点。
2. **允许的请求**：在协议配置路径上允许 GET 和 POST。XHTTP 会通过 POST 请求上传数据，并通过一个长 GET 请求接收下载数据。只允许 GET 的 CDN 无法正常工作。
3. **缓存**：对此路径关闭缓存。CDN 不得保存或改写响应。
4. **WebSocket**：协议配置使用 WebSocket 传输方式时，在 CDN 上启用此功能。
5. **源站证书**：源站始终使用 TLS，而不是普通 HTTP，因为 VLESS 请求头会以明文经过网络。CDN 验证源站证书时可使用 Let’s Encrypt。只有将 CDN 设置为不验证证书时才能使用自签名证书；Mistgate 也只允许在配置了 CDN 主机名的协议配置中使用该证书。
6. **源站端口**：有些 CDN 只会转发到固定端口列表。请选择 CDN 支持的端口（443 是稳妥的选择）。

XHTTP 停滞时，CDN 可能正在缓存完整请求正文：请保留 **Packet up**。如果仍无法使用，可新建一个 WebSocket 协议配置；这样用户会同时看到两者。CDN 在空闲连接 60 至 100 秒后会将其断开时，节点会在空闲下载期间发送保持连接的填充数据；目前也尚未对实际 CDN 验证此行为。

> **注意：** 尚未使用真实 CDN 测试。项目的端到端测试覆盖了从客户端、经节点 TLS 前端到互联网的完整路径，但由节点自身模拟 CDN。只有实际尝试后，才能确定特定 CDN 如何处理长时间下载、请求大小和空闲连接。

## 节点自身的站点

这是默认选项。节点会在 REALITY 后方提供自己的站点，与其 Hysteria2 协议配置显示的站点相同（请参阅[伪装站点](decoy-site.md)）。访问节点域名的 TCP 443 时会看到该站点及节点自身的 Let’s Encrypt 证书；通过 QUIC 连接的访问者也会看到它。因此，节点不需要指向借用的站点。无需选择第三方站点、探测第三方站点，也不会将陌生访问者转交给第三方站点。

要求：

- **域名。** Let’s Encrypt 只为域名签发证书。节点地址必须是 A 记录指向该节点的主机名；或者，在将协议配置添加到节点时，为该节点填写域名（**域名（SNI）**）。只有 IP 地址的节点无法使用此协议配置，面板会说明原因；请在此类节点上使用目标为其他站点的协议配置。
- **Agent 版本至少为 v0.1.35。** 旧版 Agent 不识别此目标。面板不会向它们发送此设置：该节点上的协议配置会显示为启动失败，原因是「Agent 过旧」，节点的其他部分不受影响。请在**更新**页面更新节点。目标为其他站点的协议配置只需要支持 VLESS 的 Agent。
- 如果需要续期证书，请开放 **TCP 443**。

证书。在 Let’s Encrypt 签发证书之前，站点会显示同名的自签名证书，因此客户端和访问者不会因等待签发而受阻；签发后会切换为 Let’s Encrypt 证书。续期不需要 TCP 80：Let’s Encrypt 会通过 TCP 443（TLS-ALPN-01）发起请求，REALITY 会将请求转发到节点自身的站点，由该站点响应。端口 80 空闲时仍会照常工作。节点页面会显示证书到期时间，并通过**证书即将到期**告警监控。

此类目标无需节点探测（因为没有外部目标），而且首次启动后，首位客户端无需等待。

## 选择其他站点作为 REALITY 目标

选择**其他站点**，让节点将访问者转发到你指定的真实站点。目标就是节点模拟的站点。选择不当会让节点容易与真实站点区分，或导致所有握手失败。

- 目标必须支持 **TLS 1.3**，并提供后量子密钥交换 **X25519MLKEM768**。大多数大型现代网站都支持。可使用 OpenSSL 3.5 或更新版本查询网站：`openssl s_client -connect www.example.org:443 -tls1_3 -groups X25519MLKEM768`。
- 优先选择与节点处于**同一网络**（ASN）的站点，或至少选择同一国家且距离较近的站点：这样更符合访问者连接的地址，也能缩短握手时间。
- 选择持续稳定且用户所在地区不会封锁的站点。节点会为每位访问者连接该站点。
- 不要使用共享 CDN 或大型匿名代理后的站点：陌生人可能会将节点当作转发器。不要使用自己的面板、其他节点或自己运营的站点。
- 在目标字段中填写准确的主机名，并在**服务器名称**中填写其证书涵盖的名称。留空时会使用目标的主机名；目标是 IP 地址时，编辑器会要求填写服务器名称。

服务器启动时，节点会对目标执行一次 TLS 1.3 握手并提供 X25519MLKEM768，然后在服务器的健康状况详情中记录检测结果：目标无响应、不支持 TLS 1.3，或不支持该密钥交换方式。目标有问题时只会显示警告，不会阻止服务器运行。

> **注意：** Agent 启动后，或首次出现新目标或服务器名称时，第一个客户端可能需要等待 5 至 10 秒才能连接。节点会先测量目标的一次响应情况，之后的连接都会很快。

## 端口

默认端口为 TCP 443。节点会自行在 UFW 中开放该端口，并添加注释 `mistgate-node-managed-tcp-v1-<port>`；使用 firewalld 或服务商防火墙时，需要手动开放端口。TCP 80 不允许使用：该端口保留给 Let’s Encrypt。

TCP 443 也用于 Hysteria2 协议配置提供网站。同一节点上的 VLESS 协议配置若使用 TCP 443，且目标为其他站点，会暂停该站点；目标为节点自身站点时，REALITY 仍会显示同一站点，不会造成损失。两种情况都不会影响通过 UDP 运行的 Hysteria2。操作前请阅读[在同一节点上运行 VLESS 和 Hysteria2](vless-and-hysteria2.md)。

节点上的两个协议配置不能监听同一 TCP 端口。TCP 和 UDP 使用相同编号的端口不会冲突。

## 应用收到的内容

VLESS 有四种格式（下面的两种，以及 Xray JSON 和 sing-box JSON）；应用会收到哪种格式，由[订阅](subscriptions.md)中的规则决定。VLESS 与 Hysteria2 共用**订阅中的应用**开关：能获得 Hysteria2 服务器的用户也会获得其分组中的 VLESS 服务器。

**链接列表。** 每台服务器对应一个分享链接，供 Happ 和其他应用读取。以下示例使用 REALITY 的 XHTTP：

```ini
vless://<uuid>@de1.example.com:443?encryption=none&fp=chrome&mode=auto&path=%2Fq8x2kd7w&pbk=<public key>&security=reality&sid=6ba85179&sni=www.example.org&spx=%2F57999bb8&type=xhttp#de1%20%C2%B7%20vless%20%C2%B7%20443
```

使用 Vision 的 TCP 会包含 `flow=xtls-rprx-vision` 和 `type=tcp`，不包含 `mode`、`path` 或 `extra`。设置 XHTTP Host 请求头时会包含 `host`；填充范围不是 100–1000 时会包含 `extra`。链接只包含与应用默认设置不同的值：如果将默认值写出，就会暴露客户端指纹。

**Mihomo YAML。** 基于 mihomo 内核的应用（kl!ck、Clash Verge、FlClash 等）会为每台服务器收到一个代理配置：

```yaml
- name: "🇩🇪 DE · VLESS"
  type: vless
  server: "de1.example.com"
  port: 443
  uuid: "<uuid>"
  udp: true
  packet-encoding: xudp
  tls: true
  servername: "www.example.org"
  client-fingerprint: chrome
  skip-cert-verify: false
  network: xhttp
  xhttp-opts:
    path: "/q8x2kd7w"
    mode: auto
  reality-opts:
    public-key: "<public key>"
    short-id: "6ba85179"
```

使用 TCP 时，以 `network: tcp` 和 `flow: xtls-rprx-vision` 替代 `xhttp-opts`。

节点地址不是普通主机名或 IP 地址时，不会生成链接。

## 应用要求

- 使用 VLESS REALITY 链接的应用（Happ 和大多数 Xray 系应用），或基于 mihomo 内核的应用。Mistgate 会根据 Mihomo 1.19.32 检查其 Mihomo 输出。
- XHTTP 比使用 Vision 的 TCP 更新。如果旧版应用无法连接 XHTTP 服务器，请新建一个使用 TCP 传输方式的协议配置；之后用户会同时看到两种配置。
- TCP 端口必须能连接到节点。如果服务商过滤该端口，应用连接会超时。

## 当前限制

- 面板的客户端视角检测会使用与订阅相同的解析后客户端设置测试 VLESS，包括握手和隧道。请参阅[运行状况](../operations/health.md)。
- 节点页面的**检测端口**会从另一台节点检查 TCP 连接能否抵达 Mistgate，详见[节点](nodes.md)。这只是一个观测点，不会执行完整的 REALITY 握手。要查看客户端实际能看到的内容，请从其他网络测试（`nc -vz de1.example.com 8443`），或使用手机连接。
- sing-box JSON 只承载基于 TCP（RAW + Vision）和 WebSocket 的 VLESS，不支持 XHTTP；Xray JSON 和 Mihomo YAML 支持所有类型。参见[各格式支持哪些协议配置](subscriptions.md#各格式支持哪些协议配置)。
- 尚未使用真实 CDN 测试经 CDN 连接的 VLESS，详见[VLESS 经 CDN 连接](#vless-经-cdn-连接)。
