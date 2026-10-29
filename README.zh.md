<div align="center">

<img src="web/public/favicon.svg" width="96" height="96" alt="Mistgate 徽标">

<h1>Mistgate</h1>

<p>一个自托管面板，用来管理你自己的几台 VPN 服务器，以及和你一起用它们的人。<br>
支持 Hysteria2、VLESS 和 AmneziaWG。两个 Go 二进制文件，SQLite 加 systemd。</p>

<p>
<img alt="状态：测试版" src="https://img.shields.io/badge/status-beta-f2c46b">
<a href="https://github.com/Mistgate/mistgate/releases/latest"><img alt="最新发布版本" src="https://img.shields.io/github/v/release/Mistgate/mistgate?sort=semver&color=b8acf2"></a>
<a href="LICENSE"><img alt="许可证：AGPL-3.0" src="https://img.shields.io/github/license/Mistgate/mistgate?color=b8acf2"></a>
<a href="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Mistgate/mistgate/actions/workflows/ci.yml/badge.svg?branch=main"></a>
<a href="go.mod"><img alt="Go 版本" src="https://img.shields.io/github/go-mod/go-version/Mistgate/mistgate"></a>
</p>

<p><a href="https://mistgate.app/zh/"><b>文档</b></a> · <a href="#快速开始"><b>安装</b></a> · <a href="#协议"><b>协议</b></a> · <a href="#稳定性如何"><b>稳定性如何</b></a> · <a href="#与其他面板的对比"><b>对比</b></a> · <a href="README.md">English</a> · <a href="README.ru.md">Русский</a></p>

<p><sub><b>测试版。</b>1.0 之前仍会有变化。<a href="https://mistgate.app/zh/roadmap/status/">状态和路线图</a></sub></p>

</div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/overview-dark-zh.webp">
  <source media="(prefers-color-scheme: light)" srcset="docs/images/readme/overview-light-zh.webp">
  <img alt="Mistgate 管理后台：节点及其运行状况和吞吐量、一个未解决的问题和最近的事件" src="docs/images/readme/overview-dark-zh.webp" width="100%">
</picture>

## 功能

面板装在一台服务器上，你的 VPN 服务器作为节点加进来。每个人拿到一个订阅链接，应用会自动获取他可以用的所有服务器。面板负责让节点保持正确配置，并持续检测它们。

- 在浏览器里用 IP 和 root 密码添加节点。面板会固定 SSH 主机密钥、检查服务器、在已启用的 UFW 或 firewalld 中开放端口，并安装已签名的 Agent。也可以用一条一次性命令安装。[添加节点](docs/zh/getting-started/add-node.md)
- 每人一个链接。基于 Mihomo 内核的应用（kl!ck、FlClash、Clash Verge Rev）从中获取全部三种协议，Happ 获取 Hysteria2 和 VLESS，AmneziaVPN 则在用户页面上为每台设备取一个密钥。[客户端应用](docs/zh/guide/client-apps.md)
- 面板每 5 分钟像应用一样连接一次每台服务器。每个节点上的诊断运行 17 项主机检查，并提供七种修复，只有你确认后才会执行。告警显示在管理后台，也会发到 Telegram。[运行状况](docs/zh/operations/health.md)
- 某些网络无法访问某台服务器时，用户可以经由你的另一个节点连接，该节点只转发加密流量。面板在启用前检查路径，之后持续监测；节点页面把每个入口画成一条从用户到服务器的线路。[中转](docs/zh/guide/relays.md)
- 可选的检测：按网络查看你的服务器在某国境内能否打开——Globalping 在家庭和移动网络中通过 IPv4 和 IPv6 的探测、志愿者和 SIM 探测点、封锁名单，以及从你自己节点发起的 TSPU 探测。大多数来源只在俄罗斯可用。[可达性](docs/zh/operations/reachability.md)
- 不需要 Docker，也不需要数据库服务器。在 1 vCPU / 1 GB 的服务器上，面板空闲时占用 16–20 MiB 内存。[基准测试](docs/zh/reference/benchmarks.md)里也写了 Mistgate 较慢的地方。
- 陌生人打开面板地址只会看到伪装站点；管理后台位于私密路径或私密主机名之后。
- 内置 MCP 服务器：AI 智能体可以读取集群状态并规划修改，有风险的操作要等你在管理后台批准。[MCP](docs/zh/reference/mcp.md)

目前还没有的：Telegram 机器人只能发告警；Cloudflare Workers 版仍是[预览版](docs/zh/getting-started/install-edge.md)。部分默认值偏向俄罗斯用户，都可以在设置里修改。

## 协议

| 协议 | 节点上的实现 | 可以设置什么 |
|:--|:--|:--|
| [Hysteria2](docs/zh/guide/hysteria2.md) | 官方 Hysteria2 内核 | UDP（QUIC）。Salamander 混淆（Gecko 为实验性）、端口跳跃、Let's Encrypt 证书或固定指纹的自签名证书。 |
| [VLESS](docs/zh/guide/vless.md) | xray-core | REALITY 搭配 XHTTP 或 TCP（RAW + Vision）；TLS 搭配 XHTTP 或 WebSocket，可以直连，也可以放在 CDN 之后。 |
| [AmneziaWG](docs/zh/guide/amneziawg.md) | 用户态的 amneziawg-go，或内核模块 | 3.1 版（默认）和 2.0 版，每台设备一个独立密钥。不提供 1.5 和 1.0。 |

Hysteria2 和 VLESS 可以共用一个节点，包括 TCP 443。并非每种订阅格式都能承载每种协议配置：见[各格式支持哪些协议配置](docs/zh/guide/subscriptions.md#各格式支持哪些协议配置)。

暂不支持：VMess、Trojan、Shadowsocks、TUIC 和普通 WireGuard。我们计划加入更多协议，每个新协议都通过下面的统一接口接入同一个订阅链接。

一个协议分为两半，都编译进二进制文件。面板这一半是一个 Go 接口：设置的 JSON Schema（管理后台据此生成表单）、校验、交给节点运行的服务器配置、设备凭据的签发，以及它在每种订阅格式中的那一部分。节点这一半是一个引擎：运行这份配置，无需重启即可替换凭据，并上报流量和运行状况。新协议只会下发给声明支持它的 Agent，所以旧版 Agent 照常工作。详见[协议插件契约](docs/zh/reference/architecture.md#协议插件契约)。

## 稳定性如何

还是测试版，而且很新：v0.1.0 的标签打于 2026-10-03，v0.1.63（2026-10-27）已是第 64 个标签。1.0 之前，API、已存储的设置和节点协议仍可能变化。发布版本目前还没有经过从旧版本升级的自动化测试，这是 [1.0 的条件](docs/zh/roadmap/status.md)之一。

我自己就用它管理六台服务器，给家人和朋友用。

隧道不依赖面板：面板停机时，节点继续按最后一次应用的配置提供服务；增删用户不会重启服务器；旧版 Agent 可以配合新版面板工作。在[基准测试](docs/zh/reference/benchmarks.md)中，增删用户和重启面板都没有断开任何连接。

检查：

- 每次 push 和 pull request 都运行 CI（[ci.yml](.github/workflows/ci.yml)）：Go 测试和 vet、面板与 Agent 会话的竞态检测、`govulncheck`、管理后台的 lint 和测试、文档构建，以及在 Debian 和 Ubuntu 上测试安装脚本。
- 端到端测试（[scripts/e2e-wsl.sh](scripts/e2e-wsl.sh)）：真实的面板和节点，配合真实的 Hysteria2、AmneziaWG、Xray、mihomo 和 sing-box 客户端，还覆盖 Agent 更新、回滚和通过 SSH 安装。每次发布前在 WSL 或虚拟机里手动运行，不在 CI 中。
- GitHub Actions 把每个标签构建成草稿版本。我在存有离线发布密钥的机器上从标签重新构建，只签名逐字节一致的二进制文件；节点和面板只安装已签名的构建。[发布版本和签名](docs/zh/operations/releases.md)

更新（[更新](docs/zh/operations/updates.md)）：

- 节点不会自行更新。滚动更新先更新一个金丝雀节点，给它 5 分钟通过健康检查，然后每批一到两个节点继续。没有通过的节点会换回之前的构建，滚动更新随之暂停。
- 新构建 5 分钟内没有连上，或启动时反复崩溃，Agent 也会自己回滚。任何节点都可以手动回滚。
- 面板只有在你确认后才会更新自己。它会先备份数据；新版本撑不过 45 秒，就恢复旧的二进制文件和数据。
- 即便如此，也请为 `/var/lib/mistgate` 做[加密备份](docs/zh/operations/backups.md)。

Mistgate 是借助 AI 编码智能体开发的。它们写的代码由我审阅，合并什么、发布什么由我决定；上面这些检查对它们的代码和对其他人的代码一视同仁。借助智能体完成的提交带有 `Co-Authored-By` 行，智能体在这个仓库里遵守的规则写在 [AGENTS.md](AGENTS.md) 中。

## 截图

<table>
<tr>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/node-dark-zh.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/node-light-zh.webp"><img alt="节点页面：状态、WARP 出口、CPU、RAM、磁盘和 24 小时流量" src="docs/images/readme/node-dark-zh.webp" width="100%"></picture><br><sub><b>节点。</b>状态、WARP 出口、负载和 24 小时流量。</sub></td>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/health-dark-zh.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/health-light-zh.webp"><img alt="客户端视角检测：按协议列出各节点，并显示通过每条隧道的延迟" src="docs/images/readme/health-dark-zh.webp" width="100%"></picture><br><sub><b>客户端视角检测。</b>每个节点上的每种协议，以及通过隧道的延迟。</sub></td>
</tr>
<tr>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/user-dark-zh.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/user-light-zh.webp"><img alt="用户页面：流量、按设备 ID 区分的每台设备和 AmneziaVPN 密钥，以及流量配额、有效期、设备上限和限速" src="docs/images/readme/user-dark-zh.webp" width="100%"></picture><br><sub><b>用户。</b>按设备 ID 区分的设备、计入配额的流量和各项限制。</sub></td>
<td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/readme/phone-dark-zh.webp"><source media="(prefers-color-scheme: light)" srcset="docs/images/readme/phone-light-zh.webp"><img alt="手机上的用户页面：剩余天数、流量，以及他的设备和「添加设备」按钮" src="docs/images/readme/phone-dark-zh.webp" width="100%"></picture><br><sub><b>用户在浏览器中打开自己的链接。</b>剩余天数、流量和他的设备。</sub></td>
</tr>
</table>

截图中的数据均为虚构的演示数据。

## 快速开始

你需要一台全新的 Linux 服务器（amd64 或 arm64，带 systemd），一个直接指向它的域名（这里用 `panel.example.com`），空闲的 TCP 80 和 443 端口，以及支持 `ssh-keygen -Y verify` 的 `openssh-client`，或者 OpenSSL 3.2 及以上版本。在服务器上运行：

```sh
curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
```

安装程序会验证已签名的面板清单，通过 systemd 启动 Mistgate，并打印管理后台地址和一次性初始设置链接（30 分钟内有效）。它不会改动防火墙。`--yes` 跳过所有提问，`--dry-run` 只验证发布版本；手动安装和其他选项见[安装面板](docs/zh/getting-started/install-panel.md)。

然后：

1. 打开初始设置链接并创建所有者：使用通行密钥，或使用密码加身份验证器验证码。
2. 在**节点 → 添加节点**中添加服务器，可以通过 SSH，也可以用一条一次性命令（[添加节点](docs/zh/getting-started/add-node.md)）。
3. 添加用户：[首批用户](docs/zh/getting-started/first-users.md)。

能运行 `ssh` 的 AI 智能体可以按 [AI 智能体指南](docs/zh/getting-started/ai-agents.md#使用-ai-智能体安装-mistgate)里的提示词完成这些步骤；所有者账户仍由你自己创建。

## 与其他面板的对比

| | Mistgate | 3x-ui | Remnawave | Hiddify | PasarGuard | s-ui |
|:--|:--|:--|:--|:--|:--|:--|
| 无需 Docker 即可运行 | ✅ | ✅ | ❌ | ✅ | 🟡 部分 | ✅ |
| 数据库 | SQLite | SQLite 或 PostgreSQL | PostgreSQL + Redis | MySQL + Redis | SQLite、MySQL、MariaDB 或 PostgreSQL | SQLite |
| 面板空闲时占用 RAM | 16–20 MiB¹ | 68–74 MiB¹ | 426–454 MiB¹ | 未测量 | 262–265 MiB¹ | 未测量 |
| 在面板中添加节点（SSH） | ✅ | ❌ | ❌ | ❌ | ❌ | ? |
| Hysteria2 | ✅ | ✅ | 🟡 部分 | ✅ | ✅ | ✅ |
| VLESS REALITY | ✅ | ✅ | ✅ | ✅ | ✅ | ? |
| AmneziaWG | ✅ 2.0 和 3.1 | ✅ 3.1 | ? | ? | ? | ? |
| VMess、Trojan、Shadowsocks | ❌ | ✅ | 🟡 部分 | ✅ | ✅ | ✅ |
| Xray JSON 或 sing-box 订阅 | ✅ 两者都有 | Xray JSON | ✅ 两者都有 | ✅ 两者都有 | sing-box | sing-box |
| 健康检查和告警 | ✅ 客户端视角检测、节点诊断、从国家视角检测可达性 | ✅ 隧道监控（需手动开启） | ✅ 状态、Prometheus | 🟡 部分 | 🟡 部分 | ? |
| Telegram 机器人 | 🟡 仅告警 | ✅ | 🟡 部分 | ✅ | ✅ | ? |
| 面向 AI 智能体的内置 MCP 服务器 | ✅ | ? | ❌ | ? | ? | ? |

¹ 整个面板进程组空闲时的私有内存，0 到 200 个用户，未连接节点，在 1 vCPU / 1 GB 的测试服务器上测得（3x-ui 包含它的 Xray 内核和 fail2ban）。方法、版本和局限见[基准测试](docs/zh/reference/benchmarks.md)。「?」表示该项目自己的 README 和文档中没有说明。

在协议覆盖范围、社区规模和功能完整的 Telegram 机器人方面，其他面板更领先。带来源的完整对照表（核对于 2026-10-09）见[与其他面板的对比](docs/zh/reference/comparison.md)。

## 文档

全部内容在 [mistgate.app](https://mistgate.app/zh/) 和 [`docs/`](docs/README.md) 中。可以从这里开始：

- [概览](docs/zh/getting-started/overview.md)和[要求](docs/zh/getting-started/requirements.md)
- [安装面板](docs/zh/getting-started/install-panel.md)、[通过 SSH 安装节点](docs/zh/getting-started/ssh-install.md)、[首批用户](docs/zh/getting-started/first-users.md)
- [运行状况](docs/zh/operations/health.md)、[更新](docs/zh/operations/updates.md)、[面板加密备份](docs/zh/operations/backups.md)、[故障排查](docs/zh/operations/troubleshooting.md)
- [CLI](docs/zh/reference/cli.md)、[API](docs/zh/reference/api.md)、[MCP](docs/zh/reference/mcp.md)、[架构](docs/zh/reference/architecture.md)

## 参与开发

欢迎提交缺陷报告、修复和文档改进。[CONTRIBUTING.md](CONTRIBUTING.md)（英文）介绍了构建方法、代码结构、CI 运行的检查和约定。编码智能体请先阅读 [`AGENTS.md`](AGENTS.md)；[AI 智能体指南](docs/zh/getting-started/ai-agents.md#使用编码智能体参与开发)中有一段现成的提示词，可用于参与开发。

## 安全

请私下报告漏洞，见 [SECURITY.md](SECURITY.md)。请备份面板的数据目录（`/var/lib/mistgate`）：其中保存着数据库、主密钥和节点所信任的 CA。

## 许可证

Mistgate 是自由软件，采用 [GNU Affero General Public License v3.0 only](LICENSE)。如果你为他人运行修改过的面板，其管理后台会显示一个「源代码」链接（`serve --source-url`），它应指向你的源代码。

## Star 历史

<a href="https://www.star-history.com/?repos=mistgate%2Fmistgate&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&legend=top-left" />
   <img alt="Star 历史图表" src="https://api.star-history.com/chart?repos=mistgate/mistgate&type=date&legend=top-left" />
 </picture>
</a>
