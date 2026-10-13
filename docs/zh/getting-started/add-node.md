---
title: 添加节点
description: 在 SSH 安装和手动安装之间进行选择，使用一次性命令注册服务器，查看节点上线情况，并在之后彻底移除节点。
---

节点 Agent 从面板获取证书、将自己安装为 systemd 服务并连接后，节点就会加入集群。**节点 → 添加节点**提供两种方式：

| 方式 | 操作 | 适用情形 |
|:--|:--|:--|
| **通过 SSH 自动安装** | 输入服务器地址，确认 SSH 主机密钥并输入密码；其余由面板完成。 | 通常使用此方式。详见[通过 SSH 安装节点](ssh-install.md)。 |
| **获取手动安装命令** | 将 Agent 复制到服务器，并以 root 身份运行一次性命令。 | 面板无法通过 SSH 访问服务器（在 Cloudflare 版中：地址属于 Cloudflare 或由其代理、网络为私有网络，或 SSH 只允许你自己的 IP）；服务器只允许密钥登录；或你希望自行运行命令。详见本页。 |

AI 智能体也可以通过 MCP 发起 SSH 安装；你需要在审批页面上批准并输入密码：见 [AI 智能体指南](ai-agents.md)。本页也介绍如何移除节点。如果对话框直接显示名称、国家/地区和地址字段，说明面板版本早于 SSH 安装功能；先更新面板（见[面板更新](../operations/updates.md)）。

## 手动安装前

- 一台符合[要求](requirements.md)的服务器，并且可以通过 SSH 使用 root 登录。
- 与面板版本相同的 Agent 二进制文件：`mistgate-node-linux-amd64` 或 `mistgate-node-linux-arm64`。如果面板数据目录中有可信的发布包，会显示可直接运行的 `scp` 命令；否则，从与面板相同的 [GitHub 发布版本](https://github.com/Mistgate/mistgate/releases/latest)或自行构建的 `bin/` 目录中获取。
- 服务器必须能通过 TCP 443 访问面板的公网地址（或传给 `serve --agent-addr` 的地址）。面板不会连接节点。

## 1. 选择手动安装

在**节点 → 添加节点**中选择**获取手动安装命令**。从概览页的**添加节点**卡片、命令面板和设置向导末尾，也可以打开同一个窗口。如果已创建待手动安装的节点，要在 SSH 向导中使用相同名称前，先将其退役，或选择其他未使用的名称。

| 字段 | 输入内容 |
|:--|:--|
| 名称 | a–z、0–9 和连字符，2 到 24 个字符，例如 `de1`。活动节点和正在进行的 SSH 安装名称不能重复；节点退役后会释放名称，但保留历史记录。 |
| 国家/地区 | 可选。它会影响 DNS：对于俄罗斯节点，诊断会检查 gosuslugi.ru 并推荐 Yandex DNS。 |
| 地址 | 域名或 IP，不带 `https://` 和端口。客户端会连接到此地址，而且该地址会写入所有订阅。Hysteria2 使用 Let’s Encrypt 证书时，需要 A 记录指向服务器的域名。 |

如果地址是 IP，窗口会在继续前提示：该节点上的 Hysteria2 需要有 A 记录的域名，或在添加协议配置时选择自签名（固定指纹）证书。AmneziaWG 可以直接使用 IP。

按**创建安装命令**。节点会以「等待安装」状态显示在列表中，窗口会显示三个步骤。

## 2. 将 Agent 放到服务器上

如果面板的数据目录中有可信的发布包（官方发布版面板会自动下载），窗口的第 1 步会显示一条可直接运行的 `scp` 命令，在面板服务器上执行即可。该命令会复制 amd64 Agent；若服务器为 ARM 架构，将 `amd64` 改为 `arm64`。

否则，自行将相同版本的 Agent 复制到服务器上的 `/root/mistgate-node`，例如从构建机器运行：

```sh
scp bin/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node
```

## 3. 以 root 身份运行命令

通过 SSH 以 root 身份登录服务器，并粘贴窗口第 2 步中的命令（**复制命令**）。命令如下：

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

| 参数 | 含义 |
|:--|:--|
| `--panel` | Agent 主动连接的主机和端口。 |
| `--sni` | 指向 Agent 端点的私密 TLS 名称。没有此名称时，面板会显示伪装站点。 |
| `--ca-sha256` | 面板 CA 的指纹。节点只会信任指纹匹配的面板。窗口的**详情**中也会显示此信息。 |
| `--token` | 一次性注册令牌。令牌只能使用一次，并在一小时后过期；窗口会显示到期时间。 |

成功时会输出：

```text
enrolled as nod_... with panel.example.com:443; state in /var/lib/mistgate-node
next: mistgate-node install   (or, in the foreground: mistgate-node run)
installed /etc/systemd/system/mistgate-node.service (state /var/lib/mistgate-node)
started; follow it with: journalctl -u mistgate-node -f
```

> **注意：** `enroll` 运行期间，服务器的进程列表中会显示令牌。令牌只能使用一次且有效期很短；如果服务器由其他人共用，设置 `MISTGATE_ENROLL_TOKEN`，或使用标准输入和 `--token-stdin` 传递令牌，而不要使用 `--token`。

## 服务器上会发生什么

`enroll`：

1. 在节点上生成 P-256 密钥和证书请求。密钥不会离开服务器。
2. 使用私密名称通过 TLS 1.3 连接 `--panel`，并且只接受证书链终止于指纹固定 CA 的服务器。
3. 发送令牌和证书请求。面板会检查令牌（只保存其哈希值），签发有效期为 30 天的客户端证书，并连同 CA 一起返回。
4. 将密钥和证书（`identity.pem`）、面板 CA（`ca.pem`）及面板地址（`agent.json`）写入 `/var/lib/mistgate-node`（目录权限为 0700，文件权限为 0600）。

`install`：

1. 检查是否以 root 身份运行，以及状态目录中是否有身份信息。
2. 将自身复制到 `/usr/local/bin/mistgate-node`。之后不再需要 `/root` 中的文件。
3. 根据服务器内存写入强化过的服务单元 `/etc/systemd/system/mistgate-node.service`，然后运行 `systemctl daemon-reload`、`enable` 和 `restart`。

Agent 启动后：

1. 应用主机基线：fq 和 BBR sysctl 值、至少 16 MiB 的 UDP 套接字缓冲区上限（`net.core.rmem_max` 和 `net.core.wmem_max`）、journald 大小上限，以及自己 nftables 表中的 SSH 防护。新的 UDP 中继套接字会从 `net.core.rmem_default` 和 `net.core.wmem_default` 继承至少 1 MiB；`net.ipv4.udp_mem` 仍会限制 UDP 内存总量。conntrack 表至少包含 65,536 个条目和 16,384 个桶；保留更高的当前值。
2. 从状态目录还原上次应用的状态（首次启动时没有状态）。
3. 通过双向 TLS 与面板建立一个长期连接，报告主机信息，并接收设置和期望状态：要运行哪些服务器，以及使用哪些用户凭据。
4. 每 10 秒发送流量、会话和主机指标；连接 30 秒后运行一次诊断，此后每 10 分钟运行一次；证书剩余有效期不足 10 天时，使用新密钥续期。

Agent 会对主机进行的完整更改列表见[要求](requirements.md)中的「Agent 会对节点进行的更改」。

## 4. 节点上线

窗口会自动刷新。第 3 步安装完成后，通常几秒内状态就会变为**已连接**，节点事件中会显示「首次连接」和 Agent 版本。按**向 de1 添加协议配置**继续阅读[首批用户](first-users.md)：在节点上运行协议配置前，状态会显示「没有协议配置：用户无法使用此节点」。

| 状态 | 含义 |
|:--|:--|
| 等待安装 | 节点已创建，但 Agent 尚未注册。节点页面会显示命令的有效期。 |
| 正常 | Agent 已连接，节点上的服务器正在运行。 |
| 主机闪断 | 连接在 10 分钟内中断过，通常是服务商网络问题。 |
| 无法连接 | 已有 10 分钟没有联系。节点页面会显示重启 Agent 和查看日志的命令。 |
| 已退役 | 已从集群中移除。 |

如果节点没有上线，在服务器上查看 Agent 日志：

```sh
systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager
```

`panel connection lost` 行会说明原因。也检查服务器时钟是否准确。

## 新安装命令

等待安装的节点可在节点页面、概览页和快速开始清单中使用**新安装命令**按钮。命令过期或丢失时使用此按钮。生成新命令后，旧的未使用命令会失效。

之前注册过的节点（证书丢失、服务器重装或状态目录丢失）也可在其**设置**页签中使用相同按钮，已退役节点除外。只有所有者能看到该按钮。节点会保留名称、协议配置和用户；新 Agent 连接后，旧证书就会失效。如果服务器上仍有旧状态目录，`enroll` 会拒绝并显示 `already enrolled; use --force to replace the identity`：在 `enroll` 后添加 `--force`。

## 移除节点

打开节点，选择**设置 → 危险区域 → 从集群退役**，然后输入节点名称进行确认。面板随后会：

- 从所有用户的访问权限中移除此节点：用户的订阅中不再包含该节点，但链接保持不变；
- 吊销节点证书，并取消其尚未使用的安装命令；
- 在面板和审计日志中保留节点记录和历史记录；节点名称可供以后重新安装时使用；
- 保留通过 SSH 安装节点的已保存 SSH 凭据：仍可在已退役节点的**设置 → SSH 凭据**中查看密码；只有使用**清除已保存的凭据**才会删除。如果密码由面板生成（通过 MCP 轮换），退役对话框会提醒你先显示并保存副本。

如果 Agent 已连接，它会收到退役指令，并在服务器上执行以下操作：

1. 停止其服务器；
2. 如果节点使用 WARP，则移除 WARP；
3. 移除它对主机所做的更改：其 nftables 表、带有 Agent 标记的 UFW 规则（UDP 监听器规则和 SSH 安装添加的 80/443 规则；保留 SSH 规则）、AmneziaWG 接口、sysctl 和 journald 文件（重启 journald 后，大小上限会立即取消），以及已应用的 DNS 解析器修复；
4. 删除状态目录（先删除密钥），然后退出。

服务器上仍会保留：二进制文件、仍处于启用状态的服务单元、运行中的内核中已设置的值（fq、BBR、UDP 缓冲区、IP 转发；直到下次重启），以及已安装的 AmneziaWG 内核模块和相关软件包。手动运行以下命令以移除其余内容：

```sh
systemctl disable --now mistgate-node
rm -f /etc/systemd/system/mistgate-node.service
systemctl daemon-reload
rm -f /usr/local/bin/mistgate-node /usr/local/bin/mistgate-node.prev /usr/local/bin/mistgate-node.new /root/mistgate-node
rm -rf /var/lib/mistgate-node
```

> **警告：** 节点离线时退役，它不会收到指令。面板会拒绝其证书，但 Agent 会继续运行上次应用的状态（其服务器会继续为原用户提供服务），并不断尝试连接。面板会立即提示这一情况（「{name} 未收到指令」），并显示需要复制的所有命令。先按下方步骤手动清理此类服务器，再运行上面的命令。

```sh
systemctl disable --now mistgate-node      # stop 时运行 cleanup-net：隧道接口、WARP 路由及其表
nft delete table inet mistgate_node        # SSH 防护和端口跳跃规则
rm -f /etc/sysctl.d/90-mistgate.conf /etc/systemd/journald.conf.d/90-mistgate.conf
systemctl restart systemd-journald
```

如果应用过诊断中的 DNS 解析器修复，删除 `/etc/systemd/resolved.conf.d/90-mistgate.conf` 并重启 systemd-resolved；或者使用 `/etc/resolv.conf.mistgate.bak` 还原 `/etc/resolv.conf`（如果存在 `/etc/resolv.conf.mistgate.bak.link`，原文件是指向其中所写路径的符号链接）。

可以随时运行 `mistgate-node cleanup-net`。它会移除 AmneziaWG 和 WARP 接口、WARP 路由和规则，以及对应的 nftables 表；Agent 启动后会重新创建它们。

## 注册失败时

| 提示 | 原因 | 处理方法 |
|:--|:--|:--|
| `enroll: --panel, --sni, --ca-sha256 and --token are required` | 粘贴命令时内容被截断。 | 使用**复制命令**重新复制。 |
| `--ca-sha256 must be 64 hex digits` | 同上。 | 同上。 |
| `enrollment token unknown, expired or used` | 命令已超过一小时、已使用过，或此节点已有更新的命令。 | 在节点页面上选择**新安装命令**。 |
| `too many failed attempts, try later` | 此地址在一分钟内有十次失败尝试。 | 等待一分钟，并使用新命令。 |
| `node retired` | 节点已退役。 | 添加新节点。 |
| `no certificate in the chain matches the pinned CA fingerprint`、`panel did not present its CA certificate` 或 `the CA returned by the panel does not match --ca-sha256` | `--panel` 地址的 TLS 响应来自面板以外的位置：面板前方有 CDN 或反向代理、地址错误，或生成命令后重建了面板数据目录。 | 将面板 DNS 直接指向面板服务器，检查 `--public-url` 和 `--agent-addr`，然后生成新命令。 |
| 超时或 `connection refused` | 面板已停止、防火墙阻止到面板 TCP 443 的连接，或命令中的地址错误。 | 检查面板和防火墙；从节点运行 `curl -I https://panel.example.com/` 进行测试。 |
| `already enrolled; use --force to replace the identity` | 状态目录中已有身份信息。 | 在 `enroll` 后添加 `--force`，或者先移除 `/var/lib/mistgate-node`。 |
| `install: must run as root` | 当前用户不是 root。 | 以 root 身份运行命令。 |
| `holds no identity: run mistgate-node enroll first` | `enroll` 失败，或 `install` 使用了另一个 `--state-dir`。 | 在相同状态目录中重新运行 `enroll`。 |
| 管理后台在**创建安装命令**后显示「未配置面板地址」 | 面板没有 Agent 地址：setup 未使用 `--public-url`，且 `serve` 没有 `--agent-addr`。 | 重启面板并传入 `--agent-addr panel.example.com:443`。 |

更多现象和处理方法见[故障排查](../operations/troubleshooting.md)。日常节点设置见[节点](../guide/nodes.md)。
