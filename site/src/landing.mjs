// The home page: what Mistgate is and does, how it fits together (D1), the protocols, how stable it is, how to start,
// how it compares, and the docs by admin section. The copy follows README.md / README.ru.md / README.zh.md in their
// plain voice: the same facts, no claim the README or the docs do not make. docs/<lang>/index.md stays the sidebar's
// source and the docs' start page on GitHub.
// Copy may hold `code`, **bold** and [text](page-key#anchor) or [text](https://...) links.
import { existsSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { GITHUB, pageUrl } from "./urls.mjs";
import { icon, NAV } from "./icons.mjs";
import { inlineSvg } from "./diagrams.mjs";

const DOCS = process.env.MG_DOCS ? join(process.env.MG_DOCS, "/") : fileURLToPath(new URL("../../docs/", import.meta.url)); // as build.mjs
const esc = (s) => String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
// `code` in copy -> <code>
const rich = (s) => esc(s).replace(/`([^`]+)`/g, "<code>$1</code>");
// the columns of the comparison, as in the README's table
const PANELS = ["Mistgate", "3x-ui", "Remnawave", "Hiddify", "PasarGuard", "s-ui"];
const CI = `${GITHUB}/blob/main/.github/workflows/ci.yml`, E2E = `${GITHUB}/blob/main/scripts/e2e-wsl.sh`;

export const LANDING = {
  en: {
    meta: "Mistgate is a self-hosted panel for a small VPN: a few servers of your own and the people you share them with. Hysteria2, VLESS and AmneziaWG; two Go binaries, SQLite and systemd.",
    eyebrow: "Beta · self-hosted · open source · AGPL-3.0",
    h1: "A self-hosted panel for a small VPN",
    lead: "For a few servers of your own and the people you share them with. You put the panel on one server and add your VPN servers to it as nodes. Each person gets one subscription link, and their app picks up every server they may use.",
    install: "Install the panel", how: "How it works",
    facts: ["Hysteria2", "VLESS REALITY", "AmneziaWG 2.0 · 3.1", "Cloudflare WARP", "Linux amd64 · arm64", "SQLite, no Docker"],
    shotAlt: "The Mistgate admin: nodes with their health and throughput, an open problem and recent events",
    doesH: "What it does",
    does: [
      ["nodes", "Nodes from the browser", "Add a node with its IP and root password. The panel pins the SSH host key, checks the server, opens the ports in an active UFW or firewalld and installs the signed agent. A one-time install command works too.", "getting-started/add-node"],
      ["subscriptions", "One link per person", "Mihomo apps (kl!ck, FlClash, Clash Verge Rev) get all three protocols from it, Happ gets Hysteria2 and VLESS, and AmneziaVPN takes a key per device from the person's page.", "guide/client-apps"],
      ["health", "Checks and alerts", "Every 5 minutes the panel connects to every server the way an app does. A doctor on each node runs 17 host checks and offers seven fixes that run only after you confirm. Alerts go to the admin and to Telegram.", "operations/health"],
      ["share", "Relays through your other nodes", "When some networks cannot reach a server, people get to it through another of your nodes, which only forwards the encrypted traffic. The panel checks the path before switching it on and watches it afterwards; the node page draws every way in as a line from the people to the server.", "guide/relays"],
      ["reachability", "Seen from inside a country", "Optional checks of whether your servers open from inside a country, network by network: Globalping probes on home and mobile networks over IPv4 and IPv6, volunteer and SIM probes, block lists and a TSPU probe from your own node. Most sources work only in Russia.", "operations/reachability"],
      ["memory", "Two Go binaries", "No Docker, no database server: SQLite and systemd. The panel idles at 16–20 MiB of memory on a 1 vCPU / 1 GB server.", "reference/benchmarks"],
      ["mask", "A decoy site", "Strangers who open the panel's address see a decoy site; the admin is behind a secret path or host name.", "guide/decoy-site"],
      ["integrations", "MCP for AI agents", "A built-in MCP server lets an AI agent read the fleet and plan changes; risky ones wait for your approval in the admin.", "reference/mcp"],
    ],
    notYet: "Not there yet: a Telegram bot beyond alerts, and the Cloudflare Workers edition is still a [preview](getting-started/install-edge). Some defaults lean towards users in Russia; all of them are settings.",
    fitH: "How it fits together",
    fitP: "Two programs: the panel and an agent on every server. A person's app talks to the panel only to fetch its link; VPN traffic goes straight to the node.",
    fitAlt: "How Mistgate fits together: you use the admin, a person's app fetches its link from the panel, VPN traffic goes from the app to the node and out to the internet, and the node keeps one connection to the panel",
    protoH: "Protocols",
    protos: [
      ["Hysteria2", "UDP (QUIC) on the official Hysteria2 core. Salamander obfuscation, port hopping, a Let's Encrypt or a pinned self-signed certificate.", "guide/hysteria2"],
      ["VLESS", "On xray-core. REALITY over XHTTP or TCP (RAW + Vision); TLS over XHTTP or WebSocket, direct or behind a CDN.", "guide/vless"],
      ["AmneziaWG", "Versions 3.1 (the default) and 2.0, a separate key per device. amneziawg-go in userspace, or the kernel module.", "guide/amneziawg"],
    ],
    protoP: "Hysteria2 and VLESS can share a node, TCP 443 included. Not supported yet: VMess, Trojan, Shadowsocks, TUIC and plain WireGuard. More protocols are planned; each plugs into the same subscription link through [the protocol plugin contract](reference/architecture#the-protocol-plugin-contract).",
    stableH: "Is it stable?",
    stableP: "It is beta, and young: v0.1.0 was tagged on 2026-10-03. The maintainer runs it on their own six servers for family and friends.",
    stable: [
      ["code", "CI", `Every push and pull request: Go tests and vet, the race detector on the panel-agent session, \`govulncheck\`, the admin's lint and tests, the docs build, the installer on Debian and Ubuntu. [ci.yml](${CI})`],
      ["pulse", "End-to-end run", `Before every release: a real panel and node with real Hysteria2, AmneziaWG, Xray, mihomo and sing-box clients, plus agent updates, rollbacks and an install over SSH. By hand in WSL or a VM, not in CI. [e2e-wsl.sh](${E2E})`],
      ["key", "Signed builds", "GitHub Actions builds each tag as a draft. The maintainer rebuilds it from the tag on a machine with the offline release key and signs only binaries that match byte for byte; the panel and nodes install only signed builds.", "operations/releases"],
      ["updates", "Updates with rollback", "Nodes never update on their own. A rollout starts with one canary node and a 5-minute health gate; a node that fails gets its previous build back and the rollout pauses. The panel updates itself only when you confirm, and restores the old build if the new one does not stay up.", "operations/updates"],
    ],
    ai: "Mistgate is written with AI coding agents: the maintainer reviews what they write and decides what is merged and released, and their code goes through the same checks as anyone's.",
    cmpH: "Compared with other panels",
    yn: { yes: "yes", no: "no", part: "partial" },
    cmp: [
      ["Runs without Docker", "✅", "✅", "❌", "✅", "🟡 partial", "✅"],
      ["Panel RAM, idle¹", "16–20 MiB", "68–74 MiB", "426–454 MiB", "not measured", "262–265 MiB", "not measured"],
      ["Add a node from the panel (SSH)", "✅", "❌", "❌", "❌", "❌", "?"],
      ["Hysteria2", "✅", "✅", "🟡 partial", "✅", "✅", "✅"],
      ["AmneziaWG", "✅ 2.0 and 3.1", "✅ 3.1", "?", "?", "?", "?"],
      ["VMess, Trojan, Shadowsocks", "❌", "✅", "🟡 partial", "✅", "✅", "✅"],
      ["Telegram bot", "🟡 alerts only", "✅", "🟡 partial", "✅", "✅", "?"],
    ],
    cmpNote: "¹ Private memory of the whole panel group, idle, no node attached, on a 1 vCPU / 1 GB test server; for 3x-ui it includes its Xray core and fail2ban. “?” means the project's own README and docs do not say. Others are ahead in protocol breadth, community size and full Telegram bots.",
    proof: [
      ["gauge", "Benchmarks", "Memory, CPU, disk, start time and subscription load next to 3x-ui, Remnawave and PasarGuard on the same small server. The method, the versions, and where Mistgate is slower.", "reference/benchmarks"],
      ["layers", "The full table", "Seven panels with sources, checked on 2026-10-09, and where the others are ahead.", "reference/comparison"],
    ],
    startH: "Quick start",
    startP: "From an empty Linux server to the first person with a working link.",
    steps: [
      ["Install the panel", "One command on a fresh Linux server: the installer verifies the signed panel manifest, starts Mistgate under systemd and prints a one-time setup link. Or give the install prompt to an AI agent.", ["getting-started/install-panel", "getting-started/ai-agents"]],
      ["Add a node", "**Nodes → Add node** installs the agent over SSH, or gives you a one-time command to run on the server yourself.", ["getting-started/ssh-install", "getting-started/add-node"]],
      ["Give people their links", "Put a profile on the node, create a user and send them the link.", ["getting-started/first-users"]],
      ["Alerts, security, backups", "Link Telegram for alerts, check the security settings and set up encrypted backups before others depend on it.", ["operations/telegram-alerts", "operations/security", "operations/backups"]],
    ],
    secH: "The docs by admin section",
    secP: "Each section of the admin and the pages about it.",
    reference: "Reference",
    first: "Start here",
    firstDesc: "What Mistgate is, what servers you need, and how to install the panel.",
    desc: {
      overview: "Fleet health at a glance, node cards, traffic and the latest events.",
      nodes: "Servers in your fleet, their protocols, load and the wizard to add a new one.",
      users: "People with access: apps, devices, traffic quotas and expiry dates.",
      profiles: "Ready-made protocol setups that you roll out to nodes and users.",
      subscriptions: "Subscription formats, delivery rules and the page users see.",
      health: "Alerts, client-eye checks and the fleet doctor.",
      updates: "Panel and node agent versions, staged rollouts and rollback.",
      integrations: "API tokens for scripts, MCP for AI agents and the changes they ask you to approve.",
      settings: "Admins, security, backups and domains.",
    },
    refDesc: "Commands, flags, variables, the architecture and answers to common questions.",
    status: "Mistgate is in beta: releases are 0.1.x, and until 1.0 the API, the stored settings and the node protocol may still change.",
    statusLink: "Status and roadmap",
  },
  ru: {
    meta: "Mistgate — панель для своего небольшого VPN: несколько своих серверов и люди, с которыми вы ими делитесь. Hysteria2, VLESS и AmneziaWG; два бинарника на Go, SQLite и systemd.",
    eyebrow: "Бета · на своих серверах · открытый код · AGPL-3.0",
    h1: "Панель для своего небольшого VPN",
    lead: "Для нескольких своих серверов и людей, с которыми вы ими делитесь. Панель ставится на один сервер, а VPN-серверы добавляются в неё как ноды. Каждый человек получает одну ссылку-подписку, и приложение само подтягивает все положенные ему серверы.",
    install: "Установить панель", how: "Как это устроено",
    facts: ["Hysteria2", "VLESS REALITY", "AmneziaWG 2.0 · 3.1", "Cloudflare WARP", "Linux amd64 · arm64", "SQLite, без Docker"],
    shotAlt: "Админка Mistgate: ноды с их состоянием и скоростью, открытая проблема и последние события",
    doesH: "Что умеет",
    does: [
      ["nodes", "Ноды из браузера", "Ноду добавляют по IP и паролю root. Панель запоминает SSH-ключ сервера, проверяет его, открывает порты во включённом UFW или firewalld и ставит подписанный агент. Можно и одноразовой командой.", "getting-started/add-node"],
      ["subscriptions", "Одна ссылка на человека", "Приложения на ядре Mihomo (kl!ck, FlClash, Clash Verge Rev) получают по ней все три протокола, Happ — Hysteria2 и VLESS, а AmneziaVPN берёт ключ на каждое устройство со страницы человека.", "guide/client-apps"],
      ["health", "Проверки и алерты", "Каждые 5 минут панель подключается к каждому серверу, как приложение. Доктор на каждой ноде делает 17 проверок хоста и предлагает семь исправлений; без вашего подтверждения ни одно не запускается. Алерты видны в админке и приходят в Telegram.", "operations/health"],
      ["share", "Ретрансляции через ваши ноды", "Если из каких-то сетей сервер не открывается, люди попадают на него через другую вашу ноду, которая только пересылает зашифрованный трафик. Панель проверяет путь до включения и следит за ним потом; на странице ноды все входы нарисованы линией от людей до сервера.", "guide/relays"],
      ["reachability", "Видно ли изнутри страны", "Необязательная проверка, открываются ли ваши серверы изнутри страны, по сетям: пробы Globalping в домашних и мобильных сетях по IPv4 и IPv6, пробы добровольцев и SIM-пробы, списки блокировок и ТСПУ-зонд с вашей ноды. Большинство источников работает только в России.", "operations/reachability"],
      ["memory", "Два бинарника на Go", "Без Docker и без сервера БД: SQLite и systemd. В простое панель занимает 16–20 МиБ памяти на сервере с 1 vCPU и 1 ГБ.", "reference/benchmarks"],
      ["mask", "Сайт-ширма", "Посторонний по адресу панели видит сайт-ширму; админка — за секретным путём или именем хоста.", "guide/decoy-site"],
      ["integrations", "MCP для ИИ-агентов", "Встроенный MCP-сервер: ИИ-агент читает флот и планирует изменения, а рискованные ждут вашего одобрения в админке.", "reference/mcp"],
    ],
    notYet: "Чего пока нет: Telegram-бот только присылает алерты, а версия для Cloudflare Workers — [предварительная](getting-started/install-edge). Часть настроек по умолчанию рассчитана на пользователей в России; все их можно поменять.",
    fitH: "Как это устроено",
    fitP: "Две программы: панель и агент на каждом сервере. С панелью приложение человека говорит, только когда забирает ссылку; VPN-трафик идёт прямо на ноду.",
    fitAlt: "Как устроен Mistgate: вы работаете в админке, приложение человека забирает ссылку у панели, VPN-трафик идёт от приложения на ноду и в интернет, а нода держит одно соединение с панелью",
    protoH: "Протоколы",
    protos: [
      ["Hysteria2", "UDP (QUIC) на официальном ядре Hysteria2. Обфускация Salamander, port hopping, сертификат Let's Encrypt или закреплённый самоподписанный.", "guide/hysteria2"],
      ["VLESS", "На xray-core. REALITY поверх XHTTP или TCP (RAW + Vision); TLS поверх XHTTP или WebSocket, напрямую или за CDN.", "guide/vless"],
      ["AmneziaWG", "Версии 3.1 (по умолчанию) и 2.0, отдельный ключ на каждое устройство. amneziawg-go в userspace или модуль ядра.", "guide/amneziawg"],
    ],
    protoP: "Hysteria2 и VLESS уживаются на одной ноде, даже на общем TCP 443. Пока не поддерживаются: VMess, Trojan, Shadowsocks, TUIC и обычный WireGuard. Новые протоколы будут; каждый подключается к той же ссылке подписки через [контракт протокольного плагина](reference/architecture#контракт-протокольного-плагина).",
    stableH: "Насколько это стабильно",
    stableP: "Это бета, и проект молодой: тег v0.1.0 поставлен 3 октября 2026 года. Автор держит на Mistgate шесть своих серверов для семьи и друзей.",
    stable: [
      ["code", "CI", `На каждый push и pull request: тесты и vet на Go, race-детектор на сессии панели с агентом, \`govulncheck\`, линт и тесты админки, сборка документации, установщик на Debian и Ubuntu. [ci.yml](${CI})`],
      ["pulse", "Сквозной прогон", `Перед каждым релизом: настоящие панель, нода и клиенты Hysteria2, AmneziaWG, Xray, mihomo и sing-box, плюс обновление агента, откаты и установка по SSH. Вручную в WSL или на виртуалке, не в CI. [e2e-wsl.sh](${E2E})`],
      ["key", "Подписанные сборки", "GitHub Actions собирает каждый тег в черновик релиза. Автор пересобирает его из тега на машине с офлайн-ключом релизов и подписывает только бинарники, совпавшие байт в байт; панель и ноды ставят только подписанные сборки.", "operations/releases"],
      ["updates", "Обновления с откатом", "Ноды сами не обновляются. Раскатка начинается с одной ноды-канарейки и 5 минут проверки; нода, не прошедшая её, получает прежнюю сборку, а раскатка встаёт на паузу. Панель обновляет себя только с вашего подтверждения и возвращает старую версию, если новая не удержалась.", "operations/updates"],
    ],
    ai: "Mistgate пишется с помощью ИИ-агентов: автор читает то, что они пишут, и решает, что попадёт в main и в релиз, а проверки для их кода те же, что для любого другого.",
    cmpH: "Сравнение с другими панелями",
    yn: { yes: "да", no: "нет", part: "частично" },
    cmp: [
      ["Работает без Docker", "✅", "✅", "❌", "✅", "🟡 частично", "✅"],
      ["Память панели в простое¹", "16–20 МиБ", "68–74 МиБ", "426–454 МиБ", "не измеряли", "262–265 МиБ", "не измеряли"],
      ["Добавить ноду из панели (SSH)", "✅", "❌", "❌", "❌", "❌", "?"],
      ["Hysteria2", "✅", "✅", "🟡 частично", "✅", "✅", "✅"],
      ["AmneziaWG", "✅ 2.0 и 3.1", "✅ 3.1", "?", "?", "?", "?"],
      ["VMess, Trojan, Shadowsocks", "❌", "✅", "🟡 частично", "✅", "✅", "✅"],
      ["Telegram-бот", "🟡 только алерты", "✅", "🟡 частично", "✅", "✅", "?"],
    ],
    cmpNote: "¹ Приватная память всей группы панели в простое, без подключённой ноды, на тестовом сервере с 1 vCPU и 1 ГБ; у 3x-ui в группе есть его ядро Xray и fail2ban. «?» значит, что в README и документации проекта об этом не сказано. Другие панели впереди по числу протоколов, размеру сообщества и полноценным Telegram-ботам.",
    proof: [
      ["gauge", "Бенчмарки", "Память, процессор, диск, время старта и нагрузка на подписки рядом с 3x-ui, Remnawave и PasarGuard на одном небольшом сервере. Методика, версии и где Mistgate медленнее.", "reference/benchmarks"],
      ["layers", "Полная таблица", "Семь панелей с источниками, проверено 9 октября 2026 года, и в чём другие впереди.", "reference/comparison"],
    ],
    startH: "Быстрый старт",
    startP: "От пустого Linux-сервера до первого человека с рабочей ссылкой.",
    steps: [
      ["Установите панель", "Одна команда на чистом Linux-сервере: установщик проверит подписанный манифест панели, запустит Mistgate под systemd и напечатает одноразовую ссылку настройки. Или отдайте промпт установки ИИ-агенту.", ["getting-started/install-panel", "getting-started/ai-agents"]],
      ["Добавьте ноду", "**Ноды → Добавить ноду**: панель поставит агент по SSH или выдаст одноразовую команду, которую вы запустите на сервере сами.", ["getting-started/ssh-install", "getting-started/add-node"]],
      ["Раздайте ссылки", "Поставьте профиль на ноду, создайте пользователя и отправьте ему ссылку.", ["getting-started/first-users"]],
      ["Алерты, безопасность, бэкапы", "Подключите Telegram для алертов, проверьте настройки безопасности и настройте зашифрованные бэкапы, прежде чем на панель начнут полагаться другие.", ["operations/telegram-alerts", "operations/security", "operations/backups"]],
    ],
    secH: "Документация по разделам админки",
    secP: "Каждый раздел админки и страницы о нём.",
    reference: "Справочник",
    first: "С чего начать",
    firstDesc: "Что такое Mistgate, какие нужны серверы и как поставить панель.",
    desc: {
      overview: "Здоровье флота с первого взгляда: карточки нод, трафик и последние события.",
      nodes: "Серверы флота, их протоколы и нагрузка, а также мастер добавления новой ноды.",
      users: "Люди с доступом: приложения, устройства, квоты трафика и сроки.",
      profiles: "Готовые наборы настроек протоколов, которые выкатываются на ноды и пользователям.",
      subscriptions: "Форматы подписки, правила выдачи и страница, которую видит пользователь.",
      health: "Алерты, проверки глазами клиента и доктор флота.",
      updates: "Версии панели и агентов нод, поэтапные выкатки и откат.",
      integrations: "API-токены для скриптов, MCP для ИИ-агентов и изменения, которые они просят одобрить.",
      settings: "Админы, безопасность, бэкапы и домены.",
    },
    refDesc: "Команды, флаги, переменные, архитектура и ответы на частые вопросы.",
    status: "Mistgate в бете: сейчас выходят релизы 0.1.x, и до 1.0 API, хранимые настройки и протокол нод ещё могут меняться.",
    statusLink: "Статус и план развития",
  },
  zh: {
    meta: "Mistgate 是一个自托管面板，用来管理你自己的几台 VPN 服务器，以及和你一起用它们的人。支持 Hysteria2、VLESS 和 AmneziaWG；两个 Go 二进制文件，SQLite 加 systemd。",
    eyebrow: "测试版 · 自托管 · 开源 · AGPL-3.0",
    h1: "自托管的小型 VPN 面板",
    lead: "用来管理你自己的几台服务器，以及和你一起用它们的人。面板装在一台服务器上，你的 VPN 服务器作为节点加进来。每个人拿到一个订阅链接，应用会自动获取他可以用的所有服务器。",
    install: "安装面板", how: "工作原理",
    facts: ["Hysteria2", "VLESS REALITY", "AmneziaWG 2.0 · 3.1", "Cloudflare WARP", "Linux amd64 · arm64", "SQLite，无需 Docker"],
    shotAlt: "Mistgate 管理后台：节点及其运行状况和吞吐量、一个未解决的问题和最近的事件",
    doesH: "功能",
    does: [
      ["nodes", "在浏览器里添加节点", "输入 IP 和 root 密码即可添加。面板会固定 SSH 主机密钥、检查服务器、在已启用的 UFW 或 firewalld 中开放端口，并安装已签名的 Agent。也可以用一条一次性命令安装。", "getting-started/add-node"],
      ["subscriptions", "每人一个链接", "基于 Mihomo 内核的应用（kl!ck、FlClash、Clash Verge Rev）从中获取全部三种协议，Happ 获取 Hysteria2 和 VLESS，AmneziaVPN 则在用户页面上为每台设备取一个密钥。", "guide/client-apps"],
      ["health", "检测和告警", "面板每 5 分钟像应用一样连接一次每台服务器。每个节点上的诊断运行 17 项主机检查，并提供七种修复，只有你确认后才会执行。告警显示在管理后台，也会发到 Telegram。", "operations/health"],
      ["share", "经由你的其他节点中转", "某些网络无法访问某台服务器时，用户可以经由你的另一个节点连接，该节点只转发加密流量。面板在启用前检查路径，之后持续监测；节点页面把每个入口画成一条从用户到服务器的线路。", "guide/relays"],
      ["reachability", "从某国境内能否访问", "可选的检测：按网络查看你的服务器在某国境内能否打开——Globalping 在家庭和移动网络中通过 IPv4 和 IPv6 的探测、志愿者和 SIM 探测点、封锁名单，以及从你自己节点发起的 TSPU 探测。大多数来源只在俄罗斯可用。", "operations/reachability"],
      ["memory", "两个 Go 二进制文件", "不需要 Docker，也不需要数据库服务器：SQLite 加 systemd。在 1 vCPU / 1 GB 的服务器上，面板空闲时占用 16–20 MiB 内存。", "reference/benchmarks"],
      ["mask", "伪装站点", "陌生人打开面板地址只会看到伪装站点；管理后台位于私密路径或私密主机名之后。", "guide/decoy-site"],
      ["integrations", "面向 AI 智能体的 MCP", "内置 MCP 服务器：AI 智能体可以读取集群状态并规划修改，有风险的操作要等你在管理后台批准。", "reference/mcp"],
    ],
    notYet: "目前还没有的：Telegram 机器人只能发告警；Cloudflare Workers 版仍是[预览版](getting-started/install-edge)。部分默认值偏向俄罗斯用户，都可以在设置里修改。",
    fitH: "各部分如何配合",
    fitP: "两个程序：面板，以及每台服务器上的 Agent。用户的应用只在获取链接时访问面板；VPN 流量直接发往节点。",
    fitAlt: "Mistgate 的组成：你使用管理后台，用户的应用从面板获取自己的链接，VPN 流量从应用经节点送往互联网，节点与面板保持一条连接",
    protoH: "协议",
    protos: [
      ["Hysteria2", "基于官方 Hysteria2 内核，走 UDP（QUIC）。支持 Salamander 混淆、端口跳跃，以及 Let's Encrypt 证书或固定指纹的自签名证书。", "guide/hysteria2"],
      ["VLESS", "基于 xray-core。REALITY 搭配 XHTTP 或 TCP（RAW + Vision）；TLS 搭配 XHTTP 或 WebSocket，可以直连，也可以放在 CDN 之后。", "guide/vless"],
      ["AmneziaWG", "支持 3.1 版（默认）和 2.0 版，每台设备一个独立密钥。节点上使用用户态的 amneziawg-go 或内核模块。", "guide/amneziawg"],
    ],
    protoP: "Hysteria2 和 VLESS 可以共用一个节点，包括 TCP 443。暂不支持：VMess、Trojan、Shadowsocks、TUIC 和普通 WireGuard。我们计划加入更多协议，每个新协议都通过[协议插件契约](reference/architecture#协议插件契约)接入同一个订阅链接。",
    stableH: "稳定性如何",
    stableP: "还是测试版，而且很新：v0.1.0 的标签打于 2026-10-03。维护者自己就用它管理六台服务器，给家人和朋友用。",
    stable: [
      ["code", "CI", `每次 push 和 pull request 都会运行：Go 测试和 vet、面板与 Agent 会话的竞态检测、\`govulncheck\`、管理后台的 lint 和测试、文档构建，以及在 Debian 和 Ubuntu 上测试安装脚本。[ci.yml](${CI})`],
      ["pulse", "端到端测试", `每次发布前运行：真实的面板和节点，配合真实的 Hysteria2、AmneziaWG、Xray、mihomo 和 sing-box 客户端，还覆盖 Agent 更新、回滚和通过 SSH 安装。在 WSL 或虚拟机里手动运行，不在 CI 中。[e2e-wsl.sh](${E2E})`],
      ["key", "签名构建", "GitHub Actions 把每个标签构建成草稿版本。维护者在存有离线发布密钥的机器上从标签重新构建，只签名逐字节一致的二进制文件；面板和节点只安装已签名的构建。", "operations/releases"],
      ["updates", "可回滚的更新", "节点不会自行更新。滚动更新先从一个金丝雀节点开始，给它 5 分钟通过健康检查；没通过的节点会换回之前的构建，滚动更新随之暂停。面板只有在你确认后才会更新自己，新版本撑不住时会恢复旧版本。", "operations/updates"],
    ],
    ai: "Mistgate 是借助 AI 编码智能体开发的：它们写的代码由维护者审阅，合并什么、发布什么由维护者决定，上面这些检查对它们的代码和对其他人的代码一视同仁。",
    cmpH: "与其他面板的对比",
    yn: { yes: "是", no: "否", part: "部分" },
    cmp: [
      ["无需 Docker 即可运行", "✅", "✅", "❌", "✅", "🟡 部分", "✅"],
      ["面板空闲时占用 RAM¹", "16–20 MiB", "68–74 MiB", "426–454 MiB", "未测量", "262–265 MiB", "未测量"],
      ["在面板中添加节点（SSH）", "✅", "❌", "❌", "❌", "❌", "?"],
      ["Hysteria2", "✅", "✅", "🟡 部分", "✅", "✅", "✅"],
      ["AmneziaWG", "✅ 2.0 和 3.1", "✅ 3.1", "?", "?", "?", "?"],
      ["VMess、Trojan、Shadowsocks", "❌", "✅", "🟡 部分", "✅", "✅", "✅"],
      ["Telegram 机器人", "🟡 仅告警", "✅", "🟡 部分", "✅", "✅", "?"],
    ],
    cmpNote: "¹ 整个面板进程组空闲时的私有内存，未连接节点，在 1 vCPU / 1 GB 的测试服务器上测得；3x-ui 的数字包含它的 Xray 内核和 fail2ban。「?」表示该项目自己的 README 和文档中没有说明。在协议覆盖范围、社区规模和功能完整的 Telegram 机器人方面，其他面板更领先。",
    proof: [
      ["gauge", "基准测试", "在同一台小型服务器上，与 3x-ui、Remnawave 和 PasarGuard 对比内存、CPU、磁盘、启动时间和订阅负载。包含测试方法、版本，以及 Mistgate 较慢的地方。", "reference/benchmarks"],
      ["layers", "完整对照表", "七个面板逐项对比并附来源，核对于 2026-10-09，也写明了其他面板领先的地方。", "reference/comparison"],
    ],
    startH: "快速开始",
    startP: "从一台空的 Linux 服务器，到第一个人拿到可用的链接。",
    steps: [
      ["安装面板", "在一台全新的 Linux 服务器上运行一条命令：安装程序会验证已签名的面板清单，通过 systemd 启动 Mistgate，并打印一次性初始设置链接。也可以把安装提示词交给 AI 智能体。", ["getting-started/install-panel", "getting-started/ai-agents"]],
      ["添加节点", "**节点 → 添加节点**会通过 SSH 安装 Agent，或者给出一条一次性命令，由你自己在服务器上运行。", ["getting-started/ssh-install", "getting-started/add-node"]],
      ["把链接发给用户", "在节点上部署协议配置，创建用户，再把链接发给对方。", ["getting-started/first-users"]],
      ["告警、安全和备份", "在别人开始依赖它之前，绑定 Telegram 接收告警，检查安全设置，并设置好加密备份。", ["operations/telegram-alerts", "operations/security", "operations/backups"]],
    ],
    secH: "按管理后台分区浏览文档",
    secP: "管理后台的每个分区，以及与之对应的页面。",
    reference: "参考",
    first: "从这里开始",
    firstDesc: "Mistgate 是什么、需要哪些服务器，以及如何安装面板。",
    desc: {
      overview: "集群状况一目了然，含节点卡片、流量和最新事件。",
      nodes: "集群中的服务器、它们的协议和负载，以及添加新节点的向导。",
      users: "有访问权限的用户：应用、设备、流量配额和到期日期。",
      profiles: "现成的协议配置，可下发到节点和用户。",
      subscriptions: "订阅格式、下发规则和用户看到的页面。",
      health: "告警、客户端视角检测和集群诊断。",
      updates: "面板和节点 Agent 的版本、分批滚动更新与回滚。",
      integrations: "脚本使用的 API 令牌、供 AI 智能体使用的 MCP，以及需要你审批的变更。",
      settings: "管理员、安全、备份和域名。",
    },
    refDesc: "命令、命令行参数、环境变量、架构，以及常见问题的解答。",
    status: "Mistgate 处于测试阶段：当前发布的是 0.1.x 版本，在 1.0 之前，API、已存储的设置和节点协议仍可能变化。",
    statusLink: "状态与路线图",
  },
};

/** Hero and body of the home page. `nav` gives the order and titles of the pages; `section(key)` names the admin section a page
 *  describes, "start" for the pages before the admin, null for the reference. */
export function renderLanding({ lang, t, nav, section, langNames = [] }) {
  const L = LANDING[lang];
  const pages = new Map(nav.flatMap((g) => g.items).map((i) => [i.key, i]));
  // a page not translated yet (zh while it is being written) sends the reader to the English one instead of nowhere
  const url = (key) => pageUrl(pages.has(key) ? lang : "en", key);
  // the screenshot and the diagram of a language that has none yet are the English ones
  const shotLang = existsSync(`${DOCS}images/readme/overview-light-${lang}.webp`) && existsSync(`${DOCS}images/readme/overview-dark-${lang}.webp`) ? lang : "en";
  const fitLang = existsSync(`${DOCS}${lang}/diagrams/how-it-fits.svg`) ? lang : "en";
  const title = (key) => pages.get(key)?.title ?? key;
  // an admin section's icon in its tone, or any other icon in lavender
  const chip = (id, size = 26) => `<span class="id-chip" data-tone="${NAV[id]?.tone ?? "lavender"}" style="width:${size}px;height:${size}px">${icon(NAV[id] ? `nav-${id}` : id, Math.round(size * 0.6))}</span>`;
  const bold = (s) => rich(s).replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  // [text](key#anchor): the page in this language; one not translated yet is the English page, without the anchor
  const fmt = (s) => bold(s).replace(/\[([^\]]+)\]\(([^)\s#]+)(#[^)\s]+)?\)/g, (_, text, to, frag = "") =>
    /^https:/.test(to) ? `<a href="${to}${frag}" rel="noopener">${text}</a>` : `<a href="${url(to)}${pages.has(to) ? frag : ""}">${text}</a>`);
  // icon, heading, text and the page it links to (by its title); Chinese puts no space after its full stop
  const list = (items) => `<ul class="why">${items.map(([id, h, p, key]) => `<li>${chip(id, 30)}<div><h3>${esc(h)}</h3><p>${fmt(p)}${key ? `${lang === "zh" ? "" : " "}<a href="${url(key)}">${esc(title(key))}</a>` : ""}</p></div></li>`).join("")}</ul>`;
  // a comparison cell as the README writes it: ✅ ❌ 🟡 and an optional word become the docs' yes / no / partial pill
  const cell = (c) => {
    const m = /^(✅|❌|🟡)\s*(.*)$/u.exec(c);
    if (!m) return esc(c);
    const kind = { "✅": "yes", "❌": "no", "🟡": "part" }[m[1]];
    return `<span class="yn yn--${kind}"><span class="yn__g" aria-hidden="true"></span>${esc(m[2] || L.yn[kind])}</span>`;
  };

  const hero = `<section class="hero">
<svg class="hero__arch" viewBox="0 0 300 260" fill="none" aria-hidden="true"><path d="M30 260V150a120 120 0 0 1 240 0v110" style="stroke:var(--surface-2)" stroke-width="1.5"/><path d="M70 260V160a80 80 0 0 1 160 0v100" style="stroke:var(--border)" stroke-width="1.5"/><path d="M110 260V170a40 40 0 0 1 80 0v90" style="stroke:var(--accent-line)" stroke-width="1.5"/><path d="M0 214c40-14 80 14 120 0s80-14 120 0 40 10 60 4" style="stroke:var(--border)" stroke-width="1.5" stroke-linecap="round"/><path d="M24 238c40-12 70 12 110 0s80-12 120 0" style="stroke:var(--surface-2)" stroke-width="1.5" stroke-linecap="round"/></svg>
<p class="label">${esc(L.eyebrow)}</p>
<h1 class="hero__h1">${esc(L.h1)}</h1>
<p class="hero__lead">${esc(L.lead)}</p>
<div class="hero__cta"><a class="btn btn--primary" href="${url("getting-started/install-panel")}">${esc(L.install)}${icon("chevronRight", 14)}</a><a class="btn btn--secondary" href="${url("getting-started/overview")}">${esc(L.how)}</a><a class="btn btn--secondary" href="${GITHUB}" rel="noopener">GitHub${icon("externalLink", 14)}</a></div>
<ul class="facts">${[...L.facts, langNames.join(" · ")].filter(Boolean).map((f) => `<li>${esc(f)}</li>`).join("")}</ul>
<figure class="shot"><img class="shot__light" src="/img/overview-light-${shotLang}.webp" width="1600" height="1000" alt="${esc(L.shotAlt)}"><img class="shot__dark" src="/img/overview-dark-${shotLang}.webp" width="1600" height="1000" alt="${esc(L.shotAlt)}"></figure>
</section>`;

  const does = `<section class="home-sec" aria-labelledby="does"><h2 id="does">${esc(L.doesH)}</h2>
${list(L.does)}<p class="home-p">${fmt(L.notYet)}</p></section>`;

  const fit = `<section class="home-sec" aria-labelledby="fit"><h2 id="fit">${esc(L.fitH)}</h2><p class="home-p">${esc(L.fitP)}</p>
<figure class="diagram diagram--svg" tabindex="0">${inlineSvg(`${DOCS}${fitLang}/diagrams/how-it-fits.svg`, L.fitAlt)}</figure></section>`;

  const protocols = `<section class="home-sec" aria-labelledby="protocols"><h2 id="protocols">${esc(L.protoH)}</h2>
<div class="proof proof--3">${L.protos.map(([name, p, key]) => `<a class="proof__card" href="${url(key)}"><span class="proof__t">${esc(name)}${icon("chevronRight", 14)}</span><span class="proof__p">${esc(p)}</span></a>`).join("")}</div>
<p class="home-p">${fmt(L.protoP)}</p></section>`;

  const stable = `<section class="home-sec" aria-labelledby="stable"><h2 id="stable">${esc(L.stableH)}</h2><p class="home-p">${esc(L.stableP)}</p>
${list(L.stable)}<p class="home-p">${esc(L.ai)}</p></section>`;

  const compare = `<section class="home-sec" aria-labelledby="compare"><h2 id="compare">${esc(L.cmpH)}</h2>
<div class="cmp" role="region" tabindex="0" aria-labelledby="compare"><table><thead><tr><th></th>${PANELS.map((p) => `<th>${p}</th>`).join("")}</tr></thead><tbody>${L.cmp.map(([h, ...cells]) => `<tr><td>${esc(h)}</td>${cells.map((c) => `<td>${cell(c)}</td>`).join("")}</tr>`).join("")}</tbody></table></div>
<p class="home-p">${esc(L.cmpNote)}</p>
<div class="proof">${L.proof.map(([ico, h, p, key]) => `<a class="proof__card" href="${url(key)}">${chip(ico, 30)}<span class="proof__t">${esc(h)}${icon("chevronRight", 14)}</span><span class="proof__p">${esc(p)}</span></a>`).join("")}</div></section>`;

  const start = `<section class="home-sec" aria-labelledby="start"><h2 id="start">${esc(L.startH)}</h2><p class="home-p">${esc(L.startP)}</p>
<ol class="steps">${L.steps.map(([h, p, keys]) => `<li><h3>${esc(h)}</h3><p>${bold(p)}</p><p class="steps__links">${keys.map((k) => `<a href="${url(k)}">${esc(title(k))}${icon("chevronRight", 12)}</a>`).join("")}</p></li>`).join("")}</ol></section>`;

  // one card per admin section, its pages in sidebar order; the pages of no single section close the grid
  const bySection = new Map(Object.keys(NAV).filter((id) => L.desc[id]).map((id) => [id, []]));
  const first = [], rest = [];
  for (const p of pages.values()) (section(p.key) === "start" ? first : bySection.get(section(p.key)) ?? rest).push(p);
  const card = (head, desc, items) => `<li class="sec-card">${head}<p>${esc(desc)}</p><ul>${items.map((p) => `<li><a href="${p.url}">${esc(p.title)}</a></li>`).join("")}</ul></li>`;
  const sections = `<section class="home-sec" aria-labelledby="sections"><h2 id="sections">${esc(L.secH)}</h2><p class="home-p">${esc(L.secP)}</p>
<ul class="sec-cards">${first.length ? card(`<h3><span class="id-chip" data-tone="lavender" style="width:26px;height:26px">${icon("bolt", 16)}</span><span>${esc(L.first)}</span></h3>`, L.firstDesc, first) : ""}${[...bySection].filter(([, items]) => items.length).map(([id, items]) => card(`<h3>${chip(id)}<span>${esc(t.sections[id])}</span></h3>`, L.desc[id], items)).join("")}${rest.length ? card(`<h3><span class="id-chip id-chip--plain" style="width:26px;height:26px">${icon("text", 16)}</span><span>${esc(L.reference)}</span></h3>`, L.refDesc, rest) : ""}</ul>
<p class="home-status">${esc(L.status)} <a href="${url("roadmap/status")}">${esc(L.statusLink)}</a></p></section>`;

  return { hero, body: does + fit + protocols + stable + start + compare + sections, description: L.meta };
}
