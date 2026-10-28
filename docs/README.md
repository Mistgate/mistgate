# Mistgate documentation

The user and operator documentation of [Mistgate](../README.md), a self-hosted panel for your own VPN fleet, in English ([`en/`](en/index.md)), Russian ([`ru/`](ru/index.md)) and Simplified Chinese ([`zh/`](zh/index.md)). Every page exists in all three languages under the same path. The same pages, with search and a sidebar, are published at **[mistgate.app](https://mistgate.app/)** (Russian: [mistgate.app/ru](https://mistgate.app/ru/), Chinese: [mistgate.app/zh](https://mistgate.app/zh/)); this directory is their source and reads the same on GitHub.

Документация на русском: [ru/index.md](ru/index.md) или [mistgate.app/ru](https://mistgate.app/ru/).

简体中文文档：[zh/index.md](zh/index.md) 或 [mistgate.app/zh](https://mistgate.app/zh/)。

## What is where

| Section | English | Русский | 简体中文 | For |
|:--|:--|:--|:--|:--|
| Getting started | [en/getting-started](en/getting-started/overview.md) | [ru/getting-started](ru/getting-started/overview.md) | [zh/getting-started](zh/getting-started/overview.md) | Concepts, requirements, installing the panel and the first node, the first users, the AI agent prompts. |
| Guide | [en/guide](en/guide/nodes.md) | [ru/guide](ru/guide/nodes.md) | [zh/guide](zh/guide/nodes.md) | Day-to-day work in the admin: nodes, profiles, Hysteria2, AmneziaWG, WARP, users, subscriptions, the user page, DNS, torrent protection. |
| Operations | [en/operations](en/operations/health.md) | [ru/operations](ru/operations/health.md) | [zh/operations](zh/operations/health.md) | Health, reachability from a country, Telegram alerts, updates, releases and signing, encrypted backups, security, troubleshooting. |
| Roadmap | [en/roadmap](en/roadmap/status.md) | [ru/roadmap](ru/roadmap/status.md) | [zh/roadmap](zh/roadmap/status.md) | What is done, what comes next, the known limits. |
| Reference | [en/reference](en/reference/cli.md) | [ru/reference](ru/reference/cli.md) | [zh/reference](zh/reference/cli.md) | Every command and flag, the configuration, the API, the MCP server, the architecture, the FAQ, the comparison with other panels, the benchmarks. |

The full page list, in reading order, is in [en/index.md](en/index.md), [ru/index.md](ru/index.md) and [zh/index.md](zh/index.md).

## Reading paths

**I want to install Mistgate.**

1. [Overview](en/getting-started/overview.md) ([ru](ru/getting-started/overview.md), [zh](zh/getting-started/overview.md)) and [Requirements](en/getting-started/requirements.md) ([ru](ru/getting-started/requirements.md), [zh](zh/getting-started/requirements.md)).
2. [Install the panel](en/getting-started/install-panel.md) ([ru](ru/getting-started/install-panel.md), [zh](zh/getting-started/install-panel.md)) from the official release, or let an agent do it with the install prompt in the [AI agent guide](en/getting-started/ai-agents.md#install-mistgate-with-an-ai-agent) ([ru](ru/getting-started/ai-agents.md), [zh](zh/getting-started/ai-agents.md)). To run the panel on Cloudflare Workers instead of a server (a preview), see [Install the Cloudflare edition](en/getting-started/install-edge.md) ([ru](ru/getting-started/install-edge.md), [zh](zh/getting-started/install-edge.md)).
3. [Install a node over SSH](en/getting-started/ssh-install.md) ([ru](ru/getting-started/ssh-install.md), [zh](zh/getting-started/ssh-install.md)), or [add a node by hand](en/getting-started/add-node.md) ([ru](ru/getting-started/add-node.md), [zh](zh/getting-started/add-node.md)).
4. [First users](en/getting-started/first-users.md) ([ru](ru/getting-started/first-users.md), [zh](zh/getting-started/first-users.md)), then [Security](en/operations/security.md) ([ru](ru/operations/security.md), [zh](zh/operations/security.md)) and [encrypted backups](en/operations/backups.md) ([ru](ru/operations/backups.md), [zh](zh/operations/backups.md)).

**I run a fleet.**

- [Nodes](en/guide/nodes.md) ([ru](ru/guide/nodes.md), [zh](zh/guide/nodes.md)), [Profiles](en/guide/profiles.md) ([ru](ru/guide/profiles.md), [zh](zh/guide/profiles.md)), [Users and groups](en/guide/users-and-groups.md) ([ru](ru/guide/users-and-groups.md), [zh](zh/guide/users-and-groups.md)), [Subscriptions](en/guide/subscriptions.md) ([ru](ru/guide/subscriptions.md), [zh](zh/guide/subscriptions.md)), [Relays through an entry node](en/guide/relays.md) ([ru](ru/guide/relays.md), [zh](zh/guide/relays.md)).
- [Health](en/operations/health.md) ([ru](ru/operations/health.md), [zh](zh/operations/health.md)), [Updates](en/operations/updates.md) ([ru](ru/operations/updates.md), [zh](zh/operations/updates.md)), [Torrent protection](en/guide/torrent-protection.md) ([ru](ru/guide/torrent-protection.md), [zh](zh/guide/torrent-protection.md)), [Troubleshooting](en/operations/troubleshooting.md) ([ru](ru/operations/troubleshooting.md), [zh](zh/operations/troubleshooting.md)).

**I integrate an agent or a script.**

- [AI agent guide](en/getting-started/ai-agents.md) ([ru](ru/getting-started/ai-agents.md), [zh](zh/getting-started/ai-agents.md)): ready prompts for installing the panel, running the fleet through MCP and contributing.
- [MCP](en/reference/mcp.md) ([ru](ru/reference/mcp.md), [zh](zh/reference/mcp.md)) and [API](en/reference/api.md) ([ru](ru/reference/api.md), [zh](zh/reference/api.md)): tokens, profiles, every tool, plan / apply and owner approvals.
- [`llms.txt`](https://mistgate.app/llms.txt) on the site lists every page with a one-line summary.

**I contribute.**

- [`AGENTS.md`](../AGENTS.md) and [`CONTRIBUTING.md`](../CONTRIBUTING.md): the code map, the checks and the conventions.
- [Architecture](en/reference/architecture.md) ([ru](ru/reference/architecture.md), [zh](zh/reference/architecture.md)), [CLI](en/reference/cli.md) ([ru](ru/reference/cli.md), [zh](zh/reference/cli.md)), [Configuration](en/reference/configuration.md) ([ru](ru/reference/configuration.md), [zh](zh/reference/configuration.md)), [Releases and signing](en/operations/releases.md) ([ru](ru/operations/releases.md), [zh](zh/operations/releases.md)).

## Notes for editors and site builders

The site is built by [`site/build.mjs`](../site/build.mjs) from `docs/en`, `docs/ru` and `docs/zh`:

```sh
pnpm --dir site install --frozen-lockfile
pnpm --dir site build    # site/dist; fails on a broken internal link
pnpm --dir site test
```

- **Mirrors.** `en/`, `ru/` and `zh/` hold the same pages at the same paths, with the same headings, code blocks, tables and table rows (`site/test/parity.test.mjs` checks it); a language switch maps `en/<path>` to `ru/<path>` and `zh/<path>`. Sections are directories; `index.md` of each language is the only start page.
- **Front matter.** Every page starts with YAML front matter with `title` (the H1 and the sidebar label) and `description` (one sentence, used as the meta description). Quote a value that contains `": "`. The body has no H1 and starts with a sentence or two, then H2 and H3 sections.
- **Links** between pages are relative `.md` links within the same language; the site rewrites them, GitHub follows them as they are.
- **Callouts** are blockquotes that start with `**Note:**`, `**Tip:**`, `**Warning:**` or `**Danger:**` (Russian: `**Важно:**`, `**Совет:**`, `**Внимание:**`, `**Опасно:**`; Chinese: `**注意：**`, `**提示：**`, `**警告：**`, `**危险：**`, each followed by a space); the site styles them, GitHub shows plain quotes and the wiki shows them as alerts. Danger is for what cannot be undone, such as a lost key. A feature that is not in the code yet is marked with the bold word **Planned** (Russian: **Планируется**, Chinese: **计划中**). In Chinese a bold span must not end in full-width punctuation that touches the next word: write `**获取次数**。` or put a space after the closing `**`, or CommonMark leaves the asterisks in the text (`site/test/docs.test.mjs` renders every page and fails on them).
- **Style.** Code blocks always carry a language tag (`sh`, `text`, `json`, `ini`, `toml`, `yaml`); a word after it names the file (```` ```ini /etc/systemd/system/mistgate.service ````). No screenshots in the pages: pictures are diagrams (below); only the site's home page shows the README's admin screenshot. Examples use reserved names only: `panel.example.com`, nodes such as `de1` and `nl1`, `203.0.113.0/24`, `2001:db8::/32`, the user Alice. Russian pages address the reader as «вы» and use the admin's own Russian labels from `web/src/i18n`; Chinese pages address the reader as 你 (or with no pronoun) and quote the admin's own Chinese labels from `web/src/i18n/zh.ts`.
- **Diagrams.** A drawing is an SVG in `<lang>/diagrams/`, in both languages under the same name and canvas, put on a line of its own: `![what it shows, in a sentence](../diagrams/name.svg)`. The site inlines it so it follows the theme; GitHub and the wiki show the file, which carries its own light and dark colours in one `<style>` scoped to `.mgd` (`site/test/diagrams.test.mjs` checks both files). Sequences, states and small flows are ```` ```mermaid ```` blocks with an `accTitle:` line: GitHub renders them itself, the site renders them at build time. The SVG diagrams are drawn by small scripts in `site/diagrams/` (`svgkit.mjs` plus one `gen-*.mjs` per group): change the script and run it with `node`, then commit the scripts and the regenerated files together.
- **Sidebar order** comes only from the list under "All pages" in `<lang>/index.md`: its H3 headings are the groups, its links the pages in order. When a page is added, removed or moved, change that list in both languages; a moved page also gets an entry in `MOVED` in `site/build.mjs`, which writes the `_redirects` file.
