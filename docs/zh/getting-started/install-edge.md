---
title: 安装 Cloudflare 版
description: 使用 mistgate edge deploy 在 Cloudflare 上运行面板，无需服务器；介绍所需条件、API 令牌、必须备份的密钥文件、首次登录和当前不可用的功能。这是预览版。
---

Cloudflare 版是为 WebAssembly 构建的同一面板，由六个 Cloudflare Workers 和一个 D1 数据库运行，无需在自有 Linux 服务器上运行二进制文件。运行一条命令 `mistgate edge deploy` 即可在 Cloudflare 账户中创建所有内容。节点仍然是你自己的服务器，照常从管理后台添加。本页介绍如何从 Cloudflare 账户开始，在管理后台创建所有者账户。示例使用 `panel.example.com`。

> **警告：** 此版本为预览版。`mistgate edge deploy` 和面板已在模拟的 Cloudflare 环境中测试，但尚未在真实账户上测试（这是下一轮工作）。从 v0.1.36 起，发布版本中已包含 Cloudflare 版发布包。不要将依赖面板的用户迁移到此版本。部分功能尚不可用，见下文「此版本中尚不可用的功能」。如果现在需要可靠的面板，使用[安装面板](install-panel.md)。

## 要求

| 项目 | 要求 |
|:--|:--|
| Cloudflare 计划 | **Workers Paid。** Workers Free 的每个请求最多只能使用 10 ms CPU 并发送 50 个子请求，而仅加载 WebAssembly 面板的冷启动就需要约 125 到 254 ms，因此无法满足要求。D1 和 Durable Objects 按 Cloudflare 的用量计费；尚未测量真实账户上面板的费用。 |
| 域名 | 一个主机名，例如 `panel.example.com`，位于已启用且**使用 Cloudflare DNS** 的区域中。命令会在令牌可访问的账户中查找该主机所属区域；如果不存在对应区域，或区域尚未启用（名称服务器必须已经指向 Cloudflare），命令会在创建任何内容前停止。使用尚无 DNS 记录的名称：Cloudflare 会为 Worker 自定义域名自行添加记录；已有记录时的处理方式尚未测试。 |
| 命令 | `mistgate`，即面板所用的同一个二进制文件。在任何可通过互联网访问 `api.cloudflare.com` 和 `github.com` 的计算机上运行。官方发布二进制文件适用于 Linux（amd64 和 arm64）；在 macOS 或 Windows 上，从源代码构建（见[系统要求](requirements.md)）。 |
| 带有 Cloudflare 版发布包的发布版本 | 命令会下载该二进制文件自身版本对应的 GitHub 发布包 `mistgate-edge.tar.gz` 及其已签名清单；上传前会使用二进制文件内置的发布密钥验证它们。目前尚无发布版本包含此包。开发构建必须通过 `--version` 或 `--bundle` 指定要使用的包。见[发布版本和签名](../operations/releases.md)。 |
| 节点 | 与其他面板相同：见[系统要求](requirements.md)。Agent 通过 HTTPS（WebSocket）连接到 `panel.example.com`，因此节点只需允许到该地址的出站 TCP 443。 |

## 创建 API 令牌

命令会使用你创建的令牌访问 Cloudflare API。在 Cloudflare 控制面板中打开「**我的个人资料 → API 令牌 → 创建令牌 → 创建自定义令牌**」（My Profile → API Tokens → Create Token → Create Custom Token），并授予以下权限：

| 范围 | 权限 | 命令的用途 |
|:--|:--|:--|
| 账户 | 「账户设置：读取」（Account Settings: Read） | 第一个请求 `GET /accounts`：列出令牌可访问的账户，同时检查令牌。 |
| 区域 | 「区域：读取」（Zone: Read） | `GET /zones`：查找主机所属的区域。 |
| 账户 | 「Workers 脚本：编辑」（Workers Scripts: Edit） | 列出脚本并读取它们的部署，上传六个脚本及静态文件，设置 cron 计划，关闭 `workers.dev` 名称，回滚时恢复旧版本（`/workers/scripts/...`）。这一项权限涵盖全部六个脚本。 |
| 账户 | 「D1：编辑」（D1: Edit） | 查找或创建数据库（`/d1/database`）。 |
| 区域 | 「Workers 路由：编辑」（Workers Routes: Edit） | 将主机绑定到前端 Worker 作为自定义域名（`PUT /workers/domains`）。 |

在**账户资源**（Account Resources）中选择你的账户，在**区域资源**（Zone Resources）中选择该主机所属的单个区域。

> **注意：** 此权限清单是根据命令发出的 API 请求整理的，尚未在真实账户上确认；只有首次真实部署后才能确定最小权限。如果命令因权限错误停止，错误信息会指出失败的请求。最可能缺少的是自定义域名权限，它可能还需要「**区域：DNS：编辑**」（Zone: DNS: Edit）；添加该权限，然后使用 `--resume` 重新运行命令。

只能通过环境变量 `CLOUDFLARE_API_TOKEN` 向命令提供令牌。没有对应标志，因此令牌不会出现在进程列表中；命令也不会将它写入密钥文件或 Cloudflare。在错误信息中，令牌会替换为 `[REDACTED]`。在 bash 中，下面的命令也不会将它写入历史记录；在 PowerShell 中使用 `$env:CLOUDFLARE_API_TOKEN = "..."`：

```sh
read -rs CLOUDFLARE_API_TOKEN && export CLOUDFLARE_API_TOKEN
```

只有你计算机上的命令会使用令牌；已部署的面板不会收到它。完成后在控制面板中删除令牌，下次部署时重新创建。

## 部署

先查看计划。`--dry-run` 只会读取 Cloudflare 中的信息（令牌、区域、现有脚本和数据库），检查发布包并输出实际运行时将执行的操作。不会创建文件，也不会更改 Cloudflare：

```sh
mistgate edge deploy --host panel.example.com --dry-run
```

然后正式运行：

```sh
mistgate edge deploy --host panel.example.com
```

| 标志 | 默认值 | 说明 |
|:--|:--|:--|
| `--host` | 无，必填 | 面板的公网主机名，不带方案、端口或路径。 |
| `--name` | `mistgate-edge` | 前端 Worker 脚本和 D1 数据库的名称；其余五个脚本名为 `<name>-link`、`<name>-public`、`<name>-admin`、`<name>-jobs` 和 `<name>-provision`。要在同一账户中运行第二个面板，指定另一个名称。 |
| `--d1-location` | `weur` | D1 保存数据库的位置：`wnam`、`enam`、`weur`、`eeur`、`apac` 或 `oc`。这是提供给 Cloudflare 的提示；选择离你最近的区域。 |
| `--version` | 二进制文件对应的发布版本 | 要下载的 Cloudflare 版发布版本，格式为 `vX.Y.Z`。 |
| `--bundle` | 无 | 使用磁盘上已有的 Cloudflare 版发布包目录（含 `mistgate-edge.tar.gz`、`edge-manifest.json`、`edge-manifest.sig`），而不下载。不能与 `--version` 同时使用。 |
| `--resume` | 关闭 | 从密钥文件继续中断的运行。见下文。 |
| `--dry-run` | 关闭 | 输出将要发出的请求（只有绑定名称，不含值）；从 Cloudflare 读取信息，但不更改内容。 |
| `--rollback` | 关闭 | 让每个 Worker 回到上次更新之前的版本。见下文「回滚」。 |
| `--insecure-skip-signature` | 关闭 | 仅与 `--bundle` 一起用于自行构建的开发包：跳过签名检查并输出警告。切勿用于非自行构建的包。 |

面板由六个 Worker 组成。只有第一个绑定域名；其余的没有自己的地址，通过它访问：

- `<name>`，前端 Worker：不含 WebAssembly 的小型路由。它按路径把每个请求转给其他 Worker，并拥有节点 Agent 所连接的 Durable Objects。
- `<name>-public`：订阅、用户页面和伪装站点。
- `<name>-link`：面板一侧的 Agent 连接处理。
- `<name>-admin`：管理后台及其 API。只有它会在空数据库上创建面板。
- `<name>-jobs`：由 cron 触发器每分钟启动的任务。
- `<name>-provision`：通过 SSH 安装节点。

六个 Worker 共用同一个 D1 数据库。

命令会按以下顺序执行这些步骤，并在首次失败时停止：

1. 下载发布包并验证签名。
2. 检查令牌，并查找主机所属的账户和区域。
3. 如果已有同名 Worker 脚本或 D1 数据库，但缺少密钥文件，则拒绝继续：命令不会接管或替换无法使用自身密钥打开的内容。
4. 写入密钥文件（见下文），**然后**才创建任何内容。
5. 创建 D1 数据库。
6. 按以下顺序上传六个脚本：link、provision、public、admin、jobs，最后上传前端 Worker，这样被绑定的脚本总是先于绑定它的脚本存在。第一次部署时，前端 Worker 会先于所有脚本上传，并且不绑定其他脚本，因为 link Worker 要绑定它的 Durable Objects；最后它会带着这些绑定再上传一次。管理后台静态文件随 public 和 admin 一起上传。密钥随各个脚本一并上传：运行面板的五个脚本都使用 `MASTER_KEY`；管理后台私密路径、订阅私密路径和设置令牌的哈希值只给 admin。内容与上次运行相同的脚本不会再次上传。
7. 每个脚本上传后立即关闭它的 `workers.dev` 名称和预览 URL，在 jobs 脚本上设置每分钟运行一次的 cron 触发器，并将 `panel.example.com` 作为自定义域名绑定到前端 Worker。Cloudflare 会创建 DNS 记录和证书。
8. 最多等待五分钟，直到 `https://panel.example.com/` 上的伪装页面响应，然后输出链接。

六个脚本的 Workers Logs 都会关闭。

## 密钥文件

命令会在当前目录中写入 `mistgate-edge-<name>.json`（使用默认名称时为 `mistgate-edge-mistgate-edge.json`），并输出完整路径。该文件保存所有无法从 Cloudflare 读回的内容：面板的**主密钥**、管理后台和订阅的私密路径、设置令牌，以及账户、区域和数据库的 ID。更新之后，它还记录每个脚本更新前的版本，用于回滚。

> **警告：** 命令输出路径后，立即备份此文件：将副本保存在密码管理器或加密驱动器中。Cloudflare 不会再次显示已设置的密钥。如果丢失此文件，主密钥也会丢失，面板数据库中加密保存的所有内容（SSH 密码、设备密钥、集群 CA）都将无法恢复；之后运行 `deploy` 也会因缺少密钥而拒绝操作已创建的脚本和数据库。持有此文件的人可以读取面板加密保存的内容，因此切勿将其放入代码仓库或共享文件夹。

在 Linux 和 macOS 上，文件权限为 0600。在 Windows 上不会强制设置权限：文件会继承所在文件夹的权限，因此将其保存在私密文件夹中（命令会对此发出警告）。使用 `--resume` 时，当前运行目录中必须有此文件；`setup-link` 需要传入该文件的路径。

如果运行中途停止（网络错误、Ctrl+C 或 Cloudflare 错误），已完成的更改不会回滚，密钥文件也会保留。解决问题后，使用 `--resume` 重新运行相同的命令：它会读取密钥文件和其中记录的发布包版本，并继续使用相同的密钥和设置令牌；已经上传的脚本不会再次上传。若不使用 `--resume`，发现密钥文件时会将此运行视为更新（见下文）。

## 部署完成后

命令会输出密钥文件位置、管理后台地址（`https://panel.example.com/` 加上 24 个随机字符组成的私密路径）和一次性设置链接：

```text
Admin URL: https://panel.example.com/<secret path>/
Setup link (one-time; the token is valid for 30 minutes from the panel's first start): https://panel.example.com/<secret path>/setup#<token>
```

与 VPS 面板的私密路径模式一样，管理后台地址本来就是私密的：其他人打开主机时只会看到伪装站点。妥善保管该地址；密钥文件中也保存了它。

在浏览器中打开设置链接并创建所有者账户，操作方式与[安装面板](install-panel.md)（第 8 步「首次登录」）相同。首次请求或首次 cron 触发时，面板会启动；实际通常在上传后一分钟内启动。此时令牌开始计时，**30 分钟后过期**。`#` 后面的部分不会发送到服务器。

如果链接过期，或在账户创建前关闭了页面：

- `mistgate edge setup-link --file mistgate-edge-mistgate-edge.json` 会再次输出已保存的链接。它使用同一个令牌，因此可找回丢失的链接，但无法续期已过期的链接。
- 要获取新令牌，不使用 `--resume`，再次运行 `mistgate edge deploy --host panel.example.com`：它会重新上传相同的发布包并生成新设置令牌。只有尚未创建管理员时此方法才有效；面板已有所有者后，设置链接不会执行任何操作。

如果输出的最后一行说主机尚未响应 HTTP 200，Cloudflare 可能仍在为自定义域名签发证书。部署本身已完成；几分钟后再打开链接。

## 添加节点

像使用 VPS 面板一样，在**节点 → 添加节点**中添加节点；见[添加节点](add-node.md)。此版本也支持通过 SSH 安装，差异如下（详情见[通过 SSH 安装节点](ssh-install.md)）：

- 面板从 Cloudflare 网络连接新服务器，因此安装期间，服务器上的 SSH 必须接受来自互联网的连接，而不只是你自己的地址。
- 面板会拒绝属于 Cloudflare 或由其代理的地址、私有网络地址，以及只能通过 VPN 访问的服务器。向导会显示「面板网络拒绝了此地址，或无法从该网络访问服务器」。使用服务器自己的公网地址，或仅使用 DNS 解析的名称。
- 如果 SSH 无法以这种方式访问，或服务器只接受 SSH 密钥，使用手动安装命令。向导会在错误旁提供该选项。Agent 主动连接面板，因此只要网络可以访问 `panel.example.com`，就能使用。

## 此版本中尚不可用的功能

在下列位置，管理后台会显示「Cloudflare 版暂不可用」；尝试启动此类操作时也会返回相同提示。其余功能与 VPS 面板相同：节点、协议配置、用户、订阅、用户页面、节点诊断和 SSH 安装。

| 功能 | 缺少的内容 |
|:--|:--|
| 备份 | 不支持加密备份和恢复。Cloudflare 自带的 D1 恢复功能（Time Travel）可以恢复数据库，但尚未与面板一起测试。妥善保管密钥文件。 |
| Telegram 告警 | 不会发送消息。 |
| MCP 和 API 令牌 | 不提供 MCP 服务器。已有令牌会显示在列表中，也可以吊销；不能创建新令牌。 |
| 隧道检测 | 面板不会检测服务器的隧道，也不会触发依赖这些检测结果的告警。 |
| 更新节点 | 面板不会更新节点 Agent：不支持滚动更新和定时更新。 |
| 节点日志 | 不显示节点 Agent 的实时日志。 |
| 更新面板 | 不支持自更新：需再次运行命令更新（见下文）。 |
| 自有伪装站点 | 节点无法使用**自有站点**伪装内容：此版本不保存上传的站点包。 |
| TSPU 探测 | 国家视角检测的 [TSPU 探测](../operations/health.md#tspu-探测)不运行：一次运行需要几分钟，而此版本的定时任务只有几秒。卡片会说明它不可用。Globalping 和封锁名单可用。 |

此版本中与设备相关的限制：

- 每台设备在每个节点上都有自己的密钥，而面板把发给节点的内容记在 D1 的一行里，D1 限制这一行不超过 2 MB。这大约相当于每个节点 38,000 个凭据，即每人使用两个订阅链接协议、平均 2.5 台设备时约 7,500 人（设备还没有各自密钥时约为 19,000 人）。该行超过 1.5 MB 时，面板会在日志中对每个节点警告一次。
- 每小时的清理多调用一次 D1（清除已删除的旧设备）。

此版本也不支持从面板注册 WARP 账户；操作会失败并显示「此版本无法注册 WARP」，但管理后台暂时不会标明这一点。

## 更新

下载较新的 `mistgate`（或传入 `--version vX.Y.Z`），然后在密钥文件所在目录中，使用相同的 `--host` 和 `--name` 再次运行命令；不要添加 `--resume`：

```sh
mistgate edge deploy --host panel.example.com
```

命令会读取密钥文件，因此密钥、数据库和私密路径都会保留。它会上传内容有变化的脚本（不涉及前端 Worker 的发布不会重启 Agent 的连接）、应用新的 Durable Object 迁移并生成新设置令牌；可以忽略该令牌（因此 admin 每次都会上传）。如果发布包比当前部署的版本旧，命令会在上传前拒绝操作。前端 Worker 替换期间，已连接的 Agent 会自行重新连接，正在进行的 SSH 安装会重试（第三次中断后失败）。密钥文件会更新为新发布包的版本；主密钥不变，因此先前备份的文件仍可用于解密所有内容。先阅读发布说明：数据库没有可供回退的备份，回滚也不会改动数据库。

## 回滚

如果更新后工作不正常，在密钥文件所在目录中运行：

```sh
mistgate edge deploy --host panel.example.com --rollback
```

更新上传任何内容之前，命令会把六个脚本各自的当前版本和 cron 计划记入密钥文件。`--rollback` 通过 Cloudflare 的部署功能恢复它们：先恢复前端 Worker，再按上传的相反顺序恢复其他脚本，并恢复 cron 计划。它使用同一个令牌，不需要发布包，也不上传任何内容；与 `--dry-run` 一起使用时只列出将要恢复的内容。上次更新新建的脚本没有旧版本，保持原样。

- D1 数据库不会回滚。发布版本只应通过添加内容来更改数据库结构，因此更新前的版本仍可在其上运行。
- 只能回到上一次更新：密钥文件只保留上一个新发布包之前的版本。首次部署之后无处可回。
- 下一次 `mistgate edge deploy` 会重新上传全部六个脚本。

## 在 VPS 面板和 Cloudflare 版之间迁移

目前尚不支持。无法将用户、节点和订阅链接从 VPS 面板迁移到 Cloudflare 版，或反向迁移：VPS 面板的备份归档不能在 Cloudflare 版恢复，节点也无法更改为连接新的面板地址。为新集群选择合适版本，或者计划重新创建用户并重新注册每个节点。支持双向迁移并保留有效订阅链接的功能仍在计划中，尚未实现。

## 命令停止时

| 命令显示 | 处理方法 |
|:--|:--|
| CLOUDFLARE_API_TOKEN is required | 在同一个 shell 中导出环境变量。 |
| list Cloudflare accounts: ... | 令牌错误、已过期或缺少「**账户设置：读取**」（Account Settings: Read）权限。按上文所述重新创建令牌。 |
| no Cloudflare DNS zone matches host panel.example.com; the host's zone must be on Cloudflare DNS | 将域名添加到 Cloudflare，将其名称服务器指向 Cloudflare，并使用该区域中的主机名。 |
| the Cloudflare zone example.com has status "pending", not "active" | 完成域名名称服务器的 Cloudflare 指向，然后重新运行命令。 |
| a Worker script with this name already exists but its local key file is missing; refusing to replace its secrets | 当前目录中没有密钥文件。找到该文件，或使用其他 `--name`（旧脚本和数据库仍保留在账户中；若不需要，可在控制面板中删除）。D1 数据库名称重复时也会出现相同拒绝。 |
| this is a development build with no release version: pass --version vX.Y.Z or --bundle DIR | 告诉命令要使用哪个发布包。 |
| the deployed script is at migration ..., which this bundle does not know | 发布包比当前部署的版本旧。使用更新的发布版本。 |
| --resume requires the local deployment file ... | 当前目录中没有此 `--name` 对应的密钥文件。 |
| nothing to roll back: the local deployment file records no earlier version | 首次部署之后没有更新过，因此没有记录旧版本。回滚需要有一次更新可供返回。 |

## 后续阅读

[添加节点](add-node.md)，然后创建[首批用户](first-users.md)。除上文列出的功能外，使用指南中的其他页面都适用。各部分的配合方式见[架构](../reference/architecture.md)。
