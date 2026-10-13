---
title: 系统要求
description: 面板服务器、节点服务器和构建机器的要求，以及 Agent 对节点所做的更改。
---

Mistgate 需要一台 Linux 面板服务器、一台或多台 Linux 节点服务器和一个域名。二进制文件可以从 GitHub 发布版本获取，也可以在自己的机器上从源代码构建。

## 面板服务器

| 项目 | 要求 |
|:--|:--|
| 系统 | amd64 或 arm64 Linux。使用 systemd 保持 `mistgate serve` 运行（[安装面板](install-panel.md)中有服务单元示例）。 |
| 访问权限 | 安装时需要 root。面板运行后只需绑定 TCP 443 和 80，并写入数据目录。 |
| 域名 | 一个主机名，其 A 记录（以及使用 IPv6 时的 AAAA 记录）指向服务器，例如 `panel.example.com`。 |
| 入站端口 | TCP 443：用于伪装站点、订阅、用户页面、管理后台（前缀和主机名模式）以及节点 Agent。面板通过 Let’s Encrypt 获取证书时，还需要 TCP 80。除非选择单独的管理后台或 Agent 监听器，否则无需开放其他端口。 |
| 出站连接 | 使用 Let’s Encrypt 时连接其 HTTPS 服务。节点上服务器的端口：面板会像客户端一样检查每台服务器（通过 UDP 运行的 Hysteria2、通过 UDP 运行的 AmneziaWG）。从管理后台安装节点时，SSH（TCP 22 或指定端口）连接节点服务器。通过 HTTPS 连接 GitHub（`api.github.com`、`github.com` 和其下载主机）获取发布版本：面板每 10 分钟检查新的已签名节点包和面板更新。注册 WARP 账户、启用 Turnstile 人机验证或备份到 R2 时，通过 HTTPS 连接 Cloudflare。 |
| 存储 | 默认数据目录为 `/var/lib/mistgate`：包含一个 SQLite 数据库、主密钥、节点包和少量文件。 |
| 其他软件 | 无。面板是一个静态二进制文件。只有自更新会调用 systemd（`systemctl`、`systemd-run`）。 |

> **注意：** Cloudflare 版（预览版）无需面板服务器；其要求是 Workers Paid 计划和一个使用 Cloudflare DNS 的域名，详见[安装 Cloudflare 版](install-edge.md)。

> **提示：** 最好将面板放在独立服务器上。同一主机上的节点会与面板争用 TCP 443（Hysteria2 默认会在此运行 HTTPS 站点）和 TCP 80。

## 节点服务器

| 项目 | 要求 |
|:--|:--|
| 系统 | 使用 systemd 的 Linux：Ubuntu 22.04 或更高版本，或 Debian 12 或更高版本。amd64 或 arm64。 |
| 访问权限 | 使用 SSH 安装器时：通过 SSH 使用 root 登录，或使用支持非交互式 `sudo -n` 的 SSH 用户，并通过密码登录；面板服务器必须能访问 SSH 端口。安装器以 root 身份更改系统，需要服务器至少有 256 MB 内存，面板中还必须有已签名的节点包；如果 UFW 或 firewalld 已启用，安装器会开放 SSH 端口、TCP 80、TCP 443 和 UDP 443。手动运行 `mistgate-node install` 也需要 root；Agent 以 root 身份运行，但可用能力集受到限制。 |
| 地址 | 公网 IPv4 地址。客户端连接节点地址：指向服务器的域名，或服务器 IP。 |
| nftables | `nft` 命令（Debian 12 和 Ubuntu 22.04 及更高版本默认安装）。Agent 通过它管理防火墙规则。端口跳跃需要 `inet` 族中的 `redirect`，Linux 内核 5.2 及更高版本支持此功能。 |
| TUN 设备 | 用户态 AmneziaWG 后端和 WARP 需要 `/dev/net/tun`。在容器 VPS（OpenVZ、LXC）上，需要在服务商控制面板中启用 TUN。 |
| 时钟 | 通过 NTP 同步。Agent 会报告与面板相差超过 30 秒的时钟；诊断中的 `time_sync` 检查也会检查这一点。 |
| 入站端口 | 节点上每个 Hysteria2 和 AmneziaWG 服务器的 UDP 端口：Hysteria2 默认使用 443，AmneziaWG 协议配置会获得 10000 到 60000 之间的随机端口。每个 VLESS 服务器的 TCP 端口（默认 443，不使用 80）。启用 Hysteria2 协议配置的端口跳跃时，还需开放其范围（从 1024 起，最多 20,000 个端口）。TCP 443 用于向非客户端访问者展示 Hysteria2 HTTPS 站点，也用于 Let’s Encrypt。没有其他服务占用时，TCP 80 用于 Let’s Encrypt。 |
| 出站连接 | 通过 TCP 连接面板（通常为 443）。通过 TCP 连接每个 VLESS 服务器的 REALITY 目标（通常为 443）：节点会为每位非客户端访问者连接目标。节点使用 WARP 时，还需要连接 Let’s Encrypt、DNS 和 Cloudflare 的 WARP 端点。只有所有者测量节点网络带宽时，才会连接 `www.speedtest.net` 和最近的 Ookla 测速服务器（端口 8080）；如果不可用，则依次尝试 `speed.cloudflare.com`、`proof.ovh.net` 和 `cachefly.net`。新节点也会测量一次：见[节点](../guide/nodes.md)。 |
| 无管理端口 | Agent 主动连接面板。节点无需接受来自面板的连接。 |

![节点端口：Hysteria2、端口跳跃范围和 AmneziaWG 使用入站 UDP，HTTPS 站点使用 TCP 443，Let’s Encrypt 使用 TCP 80，SSH 受速率限制；出站连接包括 Agent 到面板、Let’s Encrypt 和 DNS、WARP 及测速服务的连接；无需开放管理端口](../diagrams/node-ports.svg)

需要了解的事项：

- **IPv6 是可选项。** 没有 IPv6 的节点可通过 IPv4 提供服务。节点有全局 IPv6 地址却无法通过 IPv6 出站连接，比没有 IPv6 更糟：收到 AAAA 响应的应用会一直等待。诊断中的 `ipv6` 检查会对此发出警告。
- **容器。** 在 OpenVZ 或 LXC 上，Agent 无法设置自己的 sysctl 值（会记录这一情况并继续运行），也无法安装 AmneziaWG 内核模块。启用 TUN 后，用户态后端可以正常工作。
- **AmneziaWG 内核模块。** 可选。默认情况下，AmneziaWG 在用户态运行，无需内核模块。按需运行时，Agent（或 `mistgate-node awg prepare-kernel`）会在 Ubuntu 上通过 Amnezia PPA 构建模块，在 Debian 上从源代码构建。这需要 apt 和 systemd，不能在容器中运行，也不能启用 Secure Boot。
- **干净的主机效果最好。** 诊断会报告可能造成干扰的项目：其他 VPN 面板及其容器、Xray、旧 WireGuard 接口、非 Mistgate 的 nftables NAT 规则，以及 Docker 的 FORWARD 丢弃策略（会破坏 AmneziaWG 转发）。

## 发布二进制文件

每个 [GitHub 发布版本](https://github.com/Mistgate/mistgate/releases/latest)都提供两个程序的 amd64 和 arm64 静态 Linux 二进制文件（`mistgate-linux-amd64`、`mistgate-linux-arm64`、`mistgate-node-linux-amd64`、`mistgate-node-linux-arm64`）、`SHA256SUMS`，以及供面板和节点更新使用的已签名清单。二进制文件内置项目的发布密钥，因此使用这些文件安装的面板会从 GitHub 获取已签名的节点包；在 systemd 安装中也可以自行更新（见[更新](../operations/updates.md)）。此时无需构建机器。

## 从源代码构建

| 工具 | 版本 |
|:--|:--|
| Go | 1.27。`go.mod` 指定工具链版本（`go1.27.1`）；使用默认的 `GOTOOLCHAIN=auto` 时，较旧版本的 Go 会自动下载该工具链。 |
| Node.js | 22（CI 使用的版本）或更高版本：管理后台 SPA 需要构建并嵌入面板二进制文件。 |
| pnpm | 10：`web/package.json` 固定使用 `pnpm@10.33.2`，启用 Corepack 时由它管理版本。 |
| make 和 POSIX shell | 在 Windows 上使用 Git Bash 或 WSL。 |
| git | `make build` 会通过 `git describe` 写入版本，通过 `git log` 写入提交时间。没有 git 时，版本为 `0.0.0-dev`，构建时间为 0。 |
| 互联网连接 | 用于下载 Go 模块和 npm 包。 |

`make build` 会为两种架构生成静态 Linux 二进制文件（`CGO_ENABLED=0`）：

```sh
make build
# bin/mistgate-linux-amd64       bin/mistgate-node-linux-amd64
# bin/mistgate-linux-arm64       bin/mistgate-node-linux-arm64
```

> **警告：** 只有使用你自己的发布密钥构建的二进制文件才会自行更新（`RELEASE_KEY=<public key> make build`），并且只会从你签名的发布包更新。没有密钥的构建安装的节点必须手动更新。若要使用签名更新，在首次构建前创建密钥：见[发布版本和签名](../operations/releases.md)。

## DNS 和证书

- **面板。** 将面板主机名直接指向服务器，不要在前面放 CDN 或代理。节点 Agent 固定使用面板自己的 CA，Let’s Encrypt 也会直接在服务器上验证；两者都无法通过终止 TLS 的代理工作。
- **面板证书。** 可以让面板从 Let’s Encrypt 获取证书（`serve --acme-domain`），也可以提供自己的证书文件（`serve --tls-cert` 和 `--tls-key`）。见[安装面板](install-panel.md)。
- **私密管理后台主机名。** 如果管理后台位于私密主机名上，该名称需要单独的 DNS 记录，并且必须包含在某个证书中。Let’s Encrypt 证书会将其名称发布到公共证书透明度日志；使用自己的通配符证书可以避免公开该私密名称。
- **Agent 端点。** 无需 DNS 记录或公网证书。Agent 使用私密 TLS 名称（面板域名下的随机标签）连接到面板地址，面板使用自己的 CA 为该名称提供证书。
- **节点。** 使用 Let’s Encrypt 证书的 Hysteria2 服务器需要一个 A 记录指向节点的域名：可以是节点地址，也可以是协议配置的域名（SNI）。如果节点只能通过 IP 访问，则使用自签名（固定指纹）证书。AmneziaWG 不需要证书。

## Agent 对节点所做的更改

Agent 仅会更改下表列出的内容和自身创建的对象：它不会编辑其他程序的 nftables 表，不会重定向 1024 以下的端口，也不会让端口跳跃范围覆盖 sshd 端口。

| 项目 | 位置 | 时机 |
|:--|:--|:--|
| Agent 二进制文件 | `/usr/local/bin/mistgate-node`（`install --bin` 可更改路径）。自更新会将 `<binary>.new` 和 `<binary>.prev` 放在其旁边。 | `mistgate-node install`、自更新 |
| 服务 | `/etc/systemd/system/mistgate-node.service`，已启用并启动。 | `mistgate-node install` |
| 状态目录 | `/var/lib/mistgate-node`（目录权限为 0700，文件权限为 0600）：节点密钥和证书、面板 CA、面板地址、上次应用的状态、其服务器的证书、更新标记。 | `enroll`，之后由 Agent 使用 |
| sysctl | `/etc/sysctl.d/90-mistgate.conf`：`net.core.default_qdisc = fq`、`net.ipv4.tcp_congestion_control = bbr`，以及至少 16 MiB 的 `net.core.rmem_max` / `net.core.wmem_max`；这些值也会应用到运行中的内核。增大的 UDP 套接字缓冲区有助于 QUIC 在高负载下减少丢包。新的 UDP 中继套接字会从 `net.core.rmem_default` 和 `net.core.wmem_default` 继承至少 1 MiB；`net.ipv4.udp_mem` 仍会限制 UDP 内存总量。`net.netfilter.nf_conntrack_max` 至少为 65,536，`net.netfilter.nf_conntrack_buckets` 至少为 16,384；保留更高的当前值。 | 每次 Agent 启动时 |
| journald | `/etc/systemd/journald.conf.d/90-mistgate.conf`：`SystemMaxUse=200M`、`RuntimeMaxUse=200M`。文件发生更改时重启 journald。 | 每次 Agent 启动时 |
| 防火墙：SSH 防护和端口跳跃 | nftables 表 `inet mistgate_node`。 | 每次 Agent 启动时；端口跳跃范围更改时 |
| AmneziaWG | 接口 `mgawg<port>`、nftables 表 `inet mistgate_awg`（客户端 NAT、MSS 钳制、阻止客户端彼此通信；隧道中除 ping 隧道地址外，无法访问节点上的其他内容）。启用 IPv4 转发；隧道使用 IPv6 时启用 IPv6 转发，并将接口的 `accept_ra=1` 改为 `accept_ra=2`，以便主机保留自己的 IPv6。仅实时设置转发值。 | 节点运行 AmneziaWG 服务器期间 |
| WARP | 接口 `mgwarp`、路由表 51820（规则优先级为 90 和 110）、nftables 表 `inet mistgate_warp`。 | 节点使用 WARP 期间 |
| UDP 交付检测 | 临时 nftables 表 `inet mistgate_udpcheck`，只会在优先级 -500 的 prerouting 处统计并丢弃带有检测 8 字节标记的数据报；检测结束或保留时间到期后移除此表。 | UDP 交付检测运行期间 |
| BT 下载拦截 | nftables 表 `inet mistgate_torrentguard`：将 AmneziaWG 客户端发起连接时的首批数据包交给 Agent；内核会丢弃 Agent 识别为 BitTorrent 的连接。Agent 未响应时不会造成故障（队列会绕过）。Hysteria2 在其引擎内检测，主机上不设置规则。 | 启用 **拦截已识别的 BitTorrent 流量**且 AmneziaWG 服务器正在运行时 |
| 主机防火墙（UFW） | 为每台服务器的 UDP 端口和 Hysteria2 服务器的端口跳跃范围添加允许规则，注释为 `mistgate-node-managed-udp-v1-<port>`；为每台 VLESS 服务器的 TCP 端口添加允许规则，注释为 `mistgate-node-managed-tcp-v1-<port>`。如果相同端口已有非 Agent 创建的规则，Agent 不会添加规则，也不会移除没有自身注释的规则。`ufw` 在 Agent 沙箱外以临时 systemd 单元运行。firewalld 只检查，不会更改。 | UFW 启用期间 |
| DNS 解析器 | 使用 systemd-resolved 时为 `/etc/systemd/resolved.conf.d/90-mistgate.conf`；否则重写 `/etc/resolv.conf`，并将原文件保存在 `/etc/resolv.conf.mistgate.bak`。 | 仅在应用诊断中的 DNS 解析器修复时 |
| 日志大小 | `journalctl --vacuum-size=200M --vacuum-time=7d`。 | 仅在应用诊断修复时 |
| AmneziaWG 内核模块 | apt 软件包、Ubuntu 上的 Amnezia PPA 或 Debian 上的源代码构建、`/etc/modules-load.d/amneziawg.conf`。通过临时单元 `mistgate-awg-prepare` 运行。 | 仅在你要求时 |

> **注意：** SSH 防护会按源地址（IPv6 按每个 /64）限制发往 sshd 端口的**新建**连接速率：初始突发 10 次，之后每分钟 6 次。不会影响已建立的会话，不会封禁地址，豁免 回环地址；表已满时，防护会停止匹配，以免将你锁在服务器外。若工具从同一地址同时打开大量 SSH 连接，可能会受到影响。Agent 会通过 `sshd -T`、sshd 配置文件和 `ssh.socket` 查找 sshd 端口；如果都没有找到，则使用 22。

### 服务单元

`mistgate-node install` 会根据主机内存写入服务单元。主要设置如下：

| 设置 | 值 |
|:--|:--|
| 启动 | `mistgate-node run --state-dir /var/lib/mistgate-node` |
| 启动前 | 崩溃循环防护：更新后连续崩溃三次时，还原之前的二进制文件。 |
| 每次停止后 | `mistgate-node cleanup-net`：移除 AmneziaWG 和 WARP 接口、WARP 路由和规则及其 nftables 表，避免 Agent 崩溃后留下损坏的路由（Agent 启动后会重新创建）。 |
| 重启 | 失败后 5 秒执行 `Restart=on-failure`；退出码 78（尚未注册或已退役）不会触发重启。 |
| 内存 | `GOMEMLIMIT` 约为 RAM 的 60%（至少 64 MiB），`MemoryHigh` 约为 70%，`MemoryMax` 约为 85%；`LimitNOFILE=65536`。 |
| 权限 | 以 root 运行，但仅有 `CAP_NET_ADMIN` 和 `CAP_NET_BIND_SERVICE`，并设置 `NoNewPrivileges=yes`。 |
| 文件 | `ProtectSystem=strict`：只有状态目录、二进制文件目录、`/etc/sysctl.d` 和 `/etc/systemd/journald.conf.d` 可写。 |
| 设备 | 除标准伪设备外，仅允许访问 `/dev/net/tun`。 |
| 其他 | `ProtectHome`、`PrivateTmp`、`SystemCallFilter=@system-service`、`UMask=0077` 和其他常见 systemd 沙箱设置。 |

退役节点会移除哪些内容，以及如何手动清理主机，见[添加节点](add-node.md)中的「移除节点」。
