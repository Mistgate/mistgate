---
title: 发布版本和签名
description: 面向维护者和使用自有密钥构建 Mistgate 的用户：发布密钥、可复现构建、发布版本的签名与发布、自有发布包以及密钥轮换。
---

Mistgate 信任一个 Ed25519 发布密钥：持有其私钥的人决定节点 Agent 和面板可以安装哪些内容。官方二进制包含项目的密钥，维护者会使用它为每个 GitHub 发布版本签名。如果安装的是官方发布版本，本页的内容都不需要操作；[更新](updates.md)介绍如何运行集群。本页面向维护者，以及使用自有密钥构建二进制的用户。

## 发布密钥

- 私钥必须离线保存。构建时，公钥会写入两个二进制：`make build` 使用 `RELEASE_KEY`，`mistgate release build` 使用 `--key`，发布工作流使用仓库变量 `MISTGATE_RELEASE_PUBLIC_KEY`。
- 面板首次启动时，会将密钥保存在 `<data-dir>/release.pub`。无论哪个方向，`release.pub` 与编译进面板的密钥不同时，都不会被静默接受；在运行 `mistgate release trust-key` 前，面板不会信任任何发布包（参见下文「轮换发布密钥」）。
- 未使用密钥构建的二进制没有签名：其中的节点 Agent 不会自我更新，面板也无法安装面板发布版本，或为 SSH 安装提供可信 Agent。

### 生成密钥（仅一次）

```sh
mistgate release keygen --out ~/mistgate-release.key
```

命令会将私钥写入文件（权限模式 0600；不会覆盖现有文件），并输出公钥及其指纹（SHA-256 的前 16 个十六进制字符）。

> **警告：** 将密钥文件离线保存并做好备份。没有它就无法从面板更新节点；你需要创建新密钥、使用它构建新二进制，然后手动更新每个节点和面板一次。

### 使用公钥构建

```sh
git checkout v0.1.4
RELEASE_KEY=<public key> VERSION=v0.1.4 make build
```

此命令使用 `mistgate release build` 构建 `bin/mistgate-linux-{amd64,arm64}` 和 `bin/mistgate-node-linux-{amd64,arm64}`。面板和 Agent 使用相同版本、构建时间（检出提交的提交时间）和发布公钥；`mistgate version` 与 `mistgate-node version` 会输出密钥指纹。

## 发布正式版本

对于每个稳定标签 `vMAJOR.MINOR.PATCH`，`.github/workflows/release.yml` 会运行 4 个任务。`node` 只使用 Go 构建两个 Linux 节点 Agent（不使用 Node.js 或 npm 软件包）；`panel` 使用 pnpm 构建管理后台 SPA，然后构建两个面板二进制；`edge` 构建 Cloudflare 版（`mistgate-edge.tar.gz`，见下文「Cloudflare 版发布包」）并在 workerd 中加载。三个任务都只有仓库只读权限。唯一可写且不运行构建代码的 `publish` 任务会创建一个**草稿**发布版本，包含 4 个二进制、`mistgate-edge.tar.gz`、`BUILDINFO` 和 `SHA256SUMS`。每个操作都固定到一个提交 SHA。草稿发布版本不会作为最新版本提供给面板。工作流需要仓库变量 `MISTGATE_RELEASE_PUBLIC_KEY`，并会将它写入全部 4 个二进制；如果该变量缺失或无效，工作流会失败，不会发布无法签名的版本。

私钥不会传给 Actions，你也无需信任 CI 构建的二进制：在持有密钥的机器上，使用该标签自行构建发布版本；`mistgate release sign` 只会为与你的构建逐字节一致的二进制签名。然后上传 7 个清单和签名文件（包括 `panel-manifest.sshsig` 和两个 `edge-manifest` 文件），再发布草稿。10 分钟内，面板会下载并验证节点发布包（所有者更新或定时更新前，节点仍运行原有版本），而**检查 GitHub**会提供面板版本。

```sh
VERSION=v0.1.6
git clone https://github.com/Mistgate/mistgate.git && cd mistgate   # 或在你的克隆中运行 git fetch --tags
git checkout "$VERSION"
(cd web && pnpm install --frozen-lockfile && pnpm build)           # 面板二进制和 Cloudflare 版发布包会嵌入 SPA
(cd edge/worker && pnpm install --frozen-lockfile)                  # 用于打包 Cloudflare Worker 脚本的 esbuild
go build -o ../mistgate-signer ./cmd/mistgate                       # 签名程序，任何版本都可以
gh release download "$VERSION" --repo Mistgate/mistgate \
  --pattern 'mistgate-linux-*' --pattern 'mistgate-node-linux-*' --pattern 'mistgate-edge.tar.gz' --dir ../downloaded
../mistgate-signer release sign --key ~/mistgate-release.key --version "$VERSION" --expires 90d \
  ../downloaded/mistgate-node-linux-amd64 ../downloaded/mistgate-node-linux-arm64 \
  ../downloaded/mistgate-linux-amd64 ../downloaded/mistgate-linux-arm64 ../downloaded/mistgate-edge.tar.gz --out ../signed
gh release upload "$VERSION" ../signed/manifest.json ../signed/manifest.sig \
  ../signed/panel-manifest.json ../signed/panel-manifest.sig ../signed/panel-manifest.sshsig \
  ../signed/edge-manifest.json ../signed/edge-manifest.sig \
  --repo Mistgate/mistgate --clobber
gh release edit "$VERSION" --repo Mistgate/mistgate --draft=false
```

`release sign` 会拒绝不完全匹配标签或存在本地更改的检出，然后使用 `mistgate release build` 和密钥的公钥部分重新构建每个二进制，并与下载的版本比较。最后一条命令会发布草稿；上传全部 7 个文件前不要发布。

如果签名程序因二进制与重新构建的版本不匹配而拒绝签名，不要继续签名；先查明原因。常见原因包括 `MISTGATE_RELEASE_PUBLIC_KEY` 中的密钥不同、标签在构建后被移动，或工具链不同（见下文）；无法解释的差异可能意味着构建运行器已被入侵。

### 可复现构建

工作流、`make build` 和 `release sign` 的检查都会使用 `mistgate release build`。同一个标签和密钥会生成逐字节一致的二进制；已验证 Linux 构建与同一提交的 Windows 交叉编译结果一致。满足以下条件时可以复现：

- 使用 `go.mod` 的 `toolchain` 行指定的 Go 工具链。`release build` 会请求准确的版本；本地 Go 版本不同则会从 Go 模块代理下载对应版本一次；
- 源代码是标签的干净检出。未跟踪文件（构建后的 SPA、`bin/`）不会写入二进制（`-buildvcs=false`）；保留仓库的 `.gitattributes`，它会在所有系统上以 LF 行尾检出每个文件；
- 使用 `release build` 设置的固定标志：`CGO_ENABLED=0`、`-trimpath`、`GOAMD64=v1`、`GOARM64=v8.0`，以及 `-ldflags "-s -w"`；其中只有 `Version`（标签）、`Built`（标签提交时间）和 `ReleaseKey` 会变化；
- 面板二进制使用 `pnpm install --frozen-lockfile && pnpm build` 构建 SPA（Node.js 22，版本由 `web/package.json` 中的 pnpm 锁定）。

该检查证明 CI 构建了标签指定的内容，但不会检查标签代码或锁定的依赖项：`go.sum` 或 `pnpm-lock.yaml` 中的恶意软件包也会在你的机器上生成相同结果。

### Cloudflare 版发布包

`mistgate-edge.tar.gz` 是 Cloudflare 版：六个 Worker 脚本和管理后台 SPA。其中五个脚本是面板的角色，各自作为独立程序编译为 `js/wasm`（`link`、`provision`、`public`、`admin` 和 `jobs`：`<角色>.js` 与 `<角色>.wasm`，和其他二进制一样写入版本、构建时间和发布公钥）；第六个 `front.js` 是持有主机名的路由器，没有 wasm。脚本由 `edge/worker/package.json` 锁定的 esbuild 打包。tar 包还包含 SPA（`assets/`，上传到 `public` 和 `admin`）以及 `edge.json`（schema 2）：它按上传顺序列出脚本、绑定、Durable Object 迁移、密钥名称、`jobs` 的 cron 和 `front` 的路由，供 `mistgate edge deploy` 使用。SPA 构建完成并在 `edge/worker` 中运行 `pnpm install --frozen-lockfile` 后，`mistgate release build --version V --key PUBLIC mistgate-edge.tar.gz --out DIR` 会构建发布包；`release sign` 会用相同方式重新构建，如果 tar 包不一致就拒绝签名，并写入 `edge-manifest.json`（含版本、构建时间、有效期、tar 包大小和 SHA-256）与 `edge-manifest.sig`。Edge 清单使用同一个发布密钥在独立的 Ed25519 上下文中签名，因此它不能通过面板或节点签名校验，反之亦然。`panel-manifest.json` 不会更改。

tar 包具有确定性（条目排序、权限模式固定、所有者和时间固定，gzip 头部不含文件名或时间）。要保持一致，Go 工具链必须与 `go.mod` 中的版本相同；`web/dist` 和 `edge/worker/node_modules` 必须来自锁定的 lockfile；签名程序使用的 Go 版本必须与构建 tar 包的版本相同（gzip 层的压缩取决于签名程序使用的 Go 版本；如果压缩结果不同，`release sign` 会拒绝签名，不会转而签署其他内容）。

### 保持签名有效期较短

`--expires`（默认值为 `30d`）决定两个清单可安装的时长；过期后，已截获的旧清单会停止生效。应使用 `90d` 等较短期限，并在过期前续签：对同一标签运行带有新 `--expires` 的相同 `release sign` 命令，然后再次使用 `--clobber` 上传 7 个文件。面板会刷新同一构建的节点发布包，而无需再次更新节点；每次检查时都会重新读取面板清单。面板发现面板清单过期时会提示过期，并拒绝安装发布版本；过期的节点发布包也无法滚动更新，或用于 SSH 安装。

## 使用自有发布包

使用自有密钥构建的面板会忽略 GitHub 官方发布版本：它们的签名无法通过该面板的密钥校验。此面板会从你签名并复制到数据目录的发布包获取节点更新；面板二进制需要手动替换（参见[更新](updates.md)中的「早期构建和手动更新」）。

### 为节点二进制签名

```sh
mistgate release sign --key ~/mistgate-release.key --version v0.1.4 --expires 90d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
```

- 在标签的检出中运行（或通过 `--source` 指定标签）：如果检出与标签不完全匹配或存在本地更改，命令会拒绝签名；它会重新构建二进制，并在不一致时拒绝签名。
- 二进制文件名必须为 `<name>-<os>-<arch>`，与 `make build` 生成的名称相同。额外提供的 `mistgate-linux-*` 二进制会写入 `panel-manifest.json`。
- 构建时间来自标签提交；如果 `--built` 不一致，命令会拒绝。更新后，构建时间与清单不一致的新构建会自行回滚（`built_mismatch`）。
- `--expires` 的值可以是天数（`90d`）或 Go 时长（`2160h`）；默认值为 `30d`。应设置较短期限并续签（见上文）。
- 命令会写入 `dist/manifest.json` 和 `dist/manifest.sig`，将二进制复制到同一目录，读回并验证结果，然后输出版本、有效期、每个文件的大小和密钥指纹。

### 将发布包放到面板上

```sh
scp dist/* panel.example.com:/var/lib/mistgate/dist/
```

面板会读取 `<data-dir>/dist`，并在一分钟内发现更改；在更新页面中选择**重新读取文件夹**可立即重新读取。只有常规文件才会计入（不会跟随符号链接）。请一次性替换整个发布包；更新过程中更改发布包会暂停滚动更新。更新页面的**发布包**一行会显示签名是否通过校验：参见[更新](updates.md)。

## 轮换发布密钥

1. 使用 `mistgate release keygen` 创建新密钥，并将 `MISTGATE_RELEASE_PUBLIC_KEY`（或自有 `RELEASE_KEY`）设为公钥部分。
2. 每个已安装 Agent 只信任旧密钥：参照更新旧 Agent 的方法（参见[更新](updates.md)中的「旧 Agent：手动更新一次」），使用新密钥构建的 Agent 手动更新每个节点一次。
3. 面板只会安装由编译进二进制的密钥签名的面板发布版本，而它目前仍是旧密钥：手动替换为使用新密钥构建的面板二进制。
4. 新面板会记录 `release key mismatch`，并且不会信任任何发布包。在面板服务器上以 root 或面板服务账户运行 `mistgate release trust-key`（如果数据目录不在 `/var/lib/mistgate`，则添加 `--data-dir`）；它会将新密钥写入 `release.pub`，并输出旧指纹和新指纹。然后重启面板。

如果有人将其他密钥换入 `release.pub`，面板也不会信任其他签名者：只有二进制中编译的密钥才能成为此安装使用的密钥，而且只能通过 `trust-key` 更改。
