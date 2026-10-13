---
title: 客户端应用
description: 各个平台适合推荐哪些应用、每个应用从订阅中获取什么内容、一键添加链接及其备用方式，以及如何在管理后台和通过 MCP 配置应用。Mistgate 推荐的桌面应用是 kl!ck。
---

Mistgate 没有自有客户端。用户使用现有应用连接，面板会向每个应用提供其支持的内容。本页介绍各个平台适用的应用、每个应用会获取什么内容、[用户页面](user-page.md)中的一键**添加**按钮如何工作，以及如何配置应用。

## 各个平台适用的应用

| 平台 | 推荐应用 | 也提供 |
|:--|:--|:--|
| Windows、macOS | [kl!ck](#klck)：在一个应用中支持 Hysteria2、VLESS 和 AmneziaWG | Happ；用于密钥的 AmneziaVPN |
| iOS、Android | Happ：Hysteria2 | 用于密钥的 AmneziaVPN |
| Linux | 默认不推荐应用 | 用于密钥的 AmneziaVPN；你添加的任何基于 mihomo 内核的应用 |

这是新面板默认提供的应用列表（参见[全新面板](#全新面板)）。你可以更改此列表：添加、移除、重新排序任何应用，也可以将其标记为推荐应用。

## 各应用会收到什么

所有应用都使用同一个订阅链接。面板根据应用的 User-Agent 选择格式（使用**订阅 → 下发规则**中的规则，参见[订阅](subscriptions.md)）：

| 应用 | 格式 | 内容 |
|:--|:--|:--|
| kl!ck | Mihomo YAML（kl!ck 发送 `User-Agent: mihomo/…`，会匹配默认规则 `mihomo`） | Hysteria2、VLESS 和 AmneziaWG 服务器、一个服务器组，以及根据用户 DNS 预设生成的 `dns:` 部分。 |
| Clash Verge Rev、FlClash、Clash Meta for Android | Mihomo YAML，应用的 User-Agent 包含 `clash` 或 `mihomo` 时（匹配两条默认规则之一） | 与 kl!ck 相同。没有匹配任何规则的应用会获取链接列表：添加包含其部分 User-Agent 的规则。 |
| Happ | 链接列表（base64） | Hysteria2 和 VLESS 服务器；Happ 不支持 AmneziaWG。Happ 还会收到包含 DNS 预设的 `Routing` 标头，以及服务器名称中的负载信息。 |
| Hiddify、v2rayNG 和同类应用 | 链接列表（base64），除非匹配你设置的规则 | Hysteria2 和 VLESS 服务器。应用能否读取 `hysteria2://` 和 `vless://`（REALITY、XHTTP）链接取决于其版本。 |
| AmneziaVPN、AmneziaWG 应用 | 不使用订阅：每台设备从用户页面获取一个密钥（`vpn://`、`.conf` 文件或二维码） | 仅 AmneziaWG。 |

只有在用户开启 AmneziaVPN 连接方式（在用户卡片或分组中的**订阅中的应用**设置），且用户所在分组有部署到节点的 AmneziaWG 协议配置时，基于 mihomo 内核的应用才会收到 AmneziaWG 服务器。首次获取订阅时，会为该用户的隐式设备发放 AmneziaWG 密钥。内核版本需要足够新：面板会在每个密钥旁标出 AmneziaWG 3.1 所需的 Mihomo 1.19.30 或更新版本，以及 2.0 所需的 1.19.14 或更新版本（参见[AmneziaWG](amneziawg.md)）。

## 一键添加链接

用户页面上的**添加**按钮会打开应用，并自动填入订阅链接。此按钮使用应用自己的链接方案，并将订阅链接填入应用的**「添加」链接模板**（占位符 `{url}`、`{url_enc}`、`{name_enc}`；参见[订阅](subscriptions.md)）。模板为空或应用无法识别链接时，页面会提供**复制链接**：在应用的添加订阅页面中粘贴链接。

以下是已知应用的模板。除 Happ 外，每种方案都能从应用自己的文档或源代码中确认（见下方说明）。

| 应用 | 平台 | 模板 |
|:--|:--|:--|
| kl!ck | Windows、macOS | `klick://add?url={url_enc}&name={name_enc}` |
| Happ | iOS、Android、Windows、macOS | `happ://add/{url}` |
| Clash Verge Rev | Windows、macOS、Linux | `clash-verge://install-config?url={url_enc}&name={name_enc}` |
| FlClash | Android、Windows、macOS、Linux | `flclash://install-config?url={url_enc}` |
| Clash Meta for Android | Android | `clashmeta://install-config?url={url_enc}&name={name_enc}` |
| Hiddify | Android、iOS、Windows、macOS、Linux | `hiddify://import/{url}#{name_enc}` |
| v2rayNG | Android | `v2rayng://install-sub?url={url_enc}` |
| AmneziaVPN | 所有平台 | 无：每台设备使用独立密钥，不使用订阅 |

- Happ 自己的文档没有说明 `happ://add/`。其他面板的订阅页面使用此格式，Mistgate 默认也采用这种格式。如果手机上点击按钮没有反应，用户可以复制链接。
- 暂不在选择器中提供以下应用，因为它们的链接方案无法确认：Streisand 和 Shadowrocket（将模板留空，让用户粘贴链接）。sing-box 完全不提供：它只能打开 Mistgate 不提供的 sing-box JSON 配置。
- 多种 Clash 应用都会注册 `clash://` 方案，因此系统可能会打开其他应用。使用上表中的应用专属方案。

## kl!ck

[kl!ck](https://github.com/vbu00/klick) 是由 vbu00 开发的 Windows 和 macOS 开源桌面客户端，使用 Rust 编写，以 Tauri 调用未修改的 Mihomo 内核。Mistgate 推荐在桌面端使用此应用，并注明其作者；此应用不属于 Mistgate。

**为什么推荐桌面用户使用此应用。**它能读取 Mihomo YAML，这是唯一包含全部三种协议的格式：同时使用 Hysteria2、VLESS 和 AmneziaWG 的用户，可以在同一个应用中通过同一订阅链接获取所有服务器，并使用其 DNS 预设。在 Windows 和 macOS 上，另一种选择是用 Happ 连接 Hysteria2 和 VLESS，再用 AmneziaVPN 管理密钥，需要两个应用和两种添加方式。

**面板需要进行哪些设置。**无需特别设置：kl!ck 获取订阅时使用 `mihomo/1.19.31`，因此默认规则 `mihomo` 会向它提供 Mihomo YAML。应用条目使用**通过订阅链接**类型。AmneziaWG 服务器的设置见上文。

**下载。**访问发布页面：<https://github.com/vbu00/klick/releases/latest>。Windows 和 macOS 版本分开发布，文件名中包含版本号，因此面板链接到发布页面，而不是具体文件。截至撰写本文时（2026 年 10 月），最新版本是 0.4.0，提供 Windows 安装程序；macOS 端口已合并到源代码，发布后 macOS 安装包会出现在同一页面。

**一键添加及其限制。**kl!ck 支持 `klick://add?url=<percent-encoded https link>&name=<optional name>`（也支持 `klick://add/<link>`）。它不会自动添加订阅：它会打开**添加**页面并填入订阅链接，只显示链接的域名，需要用户确认。链接必须使用 `https://`。kl!ck 在 0.4.0 之后才加入此处理方式，因此**一键添加需要使用比 0.4.0 更新的 kl!ck 版本**。旧版无法识别此方案：用户需要从页面复制链接，然后在 kl!ck 中手动添加。

## 在管理后台配置应用

**订阅 → 用户页面 → 用户页面中的应用**是页面显示的应用卡片列表（参见[订阅](subscriptions.md#用户页面中的应用)）。添加应用的步骤如下：

1. 在列表下方选择一个**平台**。
2. 选择一个**已知应用**：kl!ck、Happ、AmneziaVPN、Clash Verge Rev、FlClash、Clash Meta for Android、Hiddify 或 v2rayNG。列表只显示该平台上可用的应用。**自定义应用，手动填写**可保留原有方式：选择**接入方式**并自行填写字段。
3. 点击**添加应用**。卡片会自动填入应用名称、类型、该平台的下载链接和「添加」链接模板。你可以编辑任一字段，添加最多 80 个字符的说明，并开启**推荐**。
4. 使用箭头移动卡片：推荐应用会排在该平台的最前面；推荐状态相同时，列表中位置靠前的应用排在前面。点击**保存**。

要在现有面板中将 kl!ck 设为桌面推荐应用：为 Windows 和 macOS 添加 kl!ck，开启它的**推荐**，并在这两个平台上关闭 Happ 的该选项。在同一选项卡的预览中检查结果。

## 全新面板

尚未保存订阅设置的面板会显示以下默认值：

- Windows 和 macOS：先显示并推荐 kl!ck（发布页面和 `klick://add?url={url_enc}&name={name_enc}`），然后是 Happ。
- iOS 和 Android：Happ，并设为推荐应用，使用 `happ://add/{url}`。
- 所有五个平台：AmneziaVPN 密钥（手机端提供应用商店链接，其他平台提供 AmneziaVPN 网站链接）。
- 两条规则：`mihomo` 和 `clash`，都指向 Mihomo YAML。

以上默认值只会在尚未保存设置的面板中显示。设置保存后，面板会完整保留你保存的列表；更新不会重写它。

## 通过 MCP 配置

具有操作员权限或更高权限的 AI 智能体可以使用 `subscription_settings_get`、`subscription_app_upsert_plan` / `subscription_app_upsert_apply` 和 `subscription_app_remove_plan` / `subscription_app_remove_apply` 管理相同的列表。所有页面更改都需要所有者批准：所有者在**集成 → 待你审批**中批准，每张卡片都会完整显示各字段。[MCP](../reference/mcp.md#示例为-windows-和-macos-添加-klck) 中有一个为 Windows 和 macOS 添加 kl!ck 的完整示例。[AI 智能体指南](../getting-started/ai-agents.md)中的提示词也包含此步骤。
