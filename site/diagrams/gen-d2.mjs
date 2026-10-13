// D2 "Components": docs/<lang>/diagrams/components.svg
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
const docs = fileURLToPath(new URL("../../docs", import.meta.url)); // the repository's docs/ next to site/
import { svg, rect, text, chip, item, flow, lines } from "./svgkit.mjs";

const S = {
  en: {
    title: "Mistgate components",
    desc: "Browsers, subscription apps, scripts and AI agents reach the panel on TCP 443. The panel's public listener routes a request to the decoy site, the subscriptions and user pages, the admin with its API and MCP server, or the agent endpoint. The panel's modules keep their data in SQLite, secrets sealed with the master key. Each node keeps one stream to the agent endpoint, opened by the node. On the node, the agent core applies changes to the engines (Hysteria2, AmneziaWG) and the host; VPN traffic goes from the apps through the engines to the internet.",
    clients: ["Browsers", "Subscription apps", "Scripts", "AI agents"],
    panel: "Panel", listener: "Public listener",
    routes: [["Decoy site", "everything else"], ["Subscriptions", "user pages, self-service"], ["Admin", "/api · /mcp"], ["Agent endpoint", "mutual TLS"]],
    side: [["Modules", "auth, fleet, …"], ["Store", "SQLite + migrations"], ["Vault", "master key, secrets"]],
    stream: "one stream per node, opened by the node",
    node: "Node",
    cols: [["Agent core", ["one stream to the panel", "a single worker", "reliable outbox"]], ["Engines", ["Hysteria2, in process", "AmneziaWG: userspace", "or kernel module"]], ["Host", ["nftables, sysctl", "WARP, certificates", "doctor, self-update"]]],
    traffic: "VPN traffic from the apps, out to the internet",
  },
  ru: {
    title: "Части Mistgate",
    desc: "Браузеры, приложения с подпиской, скрипты и ИИ-агенты приходят к панели на TCP 443. Публичный порт панели отправляет запрос на сайт-ширму, в подписки и страницы пользователей, в админку с API и MCP-сервером или в точку подключения агентов. Модули панели хранят данные в SQLite, секреты зашифрованы мастер-ключом. Каждая нода держит одно соединение с панелью и открывает его сама. На ноде ядро агента применяет изменения к движкам (Hysteria2, AmneziaWG) и к хосту; VPN-трафик идёт из приложений через движки в интернет.",
    clients: ["Браузеры", "Приложения", "Скрипты", "ИИ-агенты"],
    panel: "Панель", listener: "Публичный порт",
    routes: [["Сайт-ширма", "всё остальное"], ["Подписки", "страницы пользователей"], ["Админка", "/api · /mcp"], ["Агенты нод", "взаимный TLS"]],
    side: [["Модули", "auth, fleet, …"], ["Хранилище", "SQLite и миграции"], ["Vault", "ключ, секреты"]],
    stream: "одно соединение на ноду, открывает нода",
    node: "Нода",
    cols: [["Ядро агента", ["соединение с панелью", "один исполнитель", "надёжная очередь"]], ["Движки", ["Hysteria2 в процессе", "AmneziaWG: userspace", "или модуль ядра"]], ["Хост", ["nftables, sysctl", "WARP, сертификаты", "доктор, самообновление"]]],
    traffic: "VPN-трафик из приложений и дальше в интернет",
  },
  zh: {
    title: "Mistgate 的组成",
    desc: "浏览器、订阅类应用、脚本和 AI 智能体通过 TCP 443 访问面板。面板的公开监听端口把请求分发给伪装站点、订阅和用户页面、带 API 与 MCP 服务器的管理后台，或 Agent 接入端点。面板各模块把数据保存在 SQLite 中，机密用主密钥加密封存。每个节点保持一条到 Agent 接入端点的连接，由节点发起。在节点上，Agent 核心把变更应用到引擎（Hysteria2、AmneziaWG）和主机；VPN 流量从应用经引擎送往互联网。",
    clients: ["浏览器", "订阅类应用", "脚本", "AI 智能体"],
    panel: "面板", listener: "公开监听端口",
    routes: [["伪装站点", "其余所有请求"], ["订阅", "用户页面、自助"], ["管理后台", "/api · /mcp"], ["Agent 接入端点", "双向 TLS"]],
    side: [["模块", "auth, fleet, …"], ["存储", "SQLite 与迁移"], ["Vault", "主密钥与机密"]],
    stream: "每个节点一条连接，由节点发起",
    node: "节点",
    cols: [["Agent 核心", ["与面板保持一条连接", "单一工作线程", "可靠发件队列"]], ["引擎", ["Hysteria2，进程内运行", "AmneziaWG：用户态", "或内核模块"]], ["主机", ["nftables, sysctl", "WARP、证书", "诊断、自更新"]]],
    traffic: "来自应用的 VPN 流量，经引擎送往互联网",
  },
};

const W = 720, H = 688;
const routeTone = [["sage", "mask"], ["mint", "link"], ["lav", "lock"], ["sky", "shield"]];
const sideTone = [["sand", "layers"], ["sand", "disk"], ["sand", "key"]];
const colTone = [["lav", "pulse"], ["sky", "bolt"], ["sand", "terminal"]];
const clientIcon = ["globe", "phone", "terminal", "sparkle"];

for (const [lang, s] of Object.entries(S)) {
  const body = [
    // who calls the panel
    rect("card", 24, 24, 672, 48, 14),
    s.clients.map((c, i) => [chip("grey", clientIcon[i], 40 + i * 166, 37, 22), text("t", 70 + i * 166, 52.5, c)]),
    // the panel
    rect("card", 24, 104, 672, 270, 16),
    chip("lav", "nav-overview", 40, 120, 26),
    text("h", 76, 138, s.panel),
    text("m", 680, 137, "mistgate serve", "end"),
    flow("grey", [[180, 72], [180, 166]]),
    text("m", 190, 93, "TCP 443"),
    item("grey", "network", 40, 166, 180, 48, s.listener, "TCP 443"),
    // listener -> its four routes
    `<g class="grey"><path class="flow" d="M220 190H240M240 187V337M240 187H253M240 237H253M240 287H253M240 337H253"/></g>`,
    s.routes.map((r, i) => item(routeTone[i][0], routeTone[i][1], 260, 166 + i * 50, 220, 42, r[0], r[1])),
    s.side.map((r, i) => item(sideTone[i][0], sideTone[i][1], 500, 166 + i * 56, 180, 48, r[0], r[1])),
    // one stream per node, opened by the node
    flow("lav", [[370, 412], [370, 358]], { both: true, cls: "dash" }),
    text("s", 386, 397, s.stream),
    // the node
    rect("card", 24, 412, 672, 176, 16),
    chip("sky", "nav-nodes", 40, 428, 26),
    text("h", 76, 446, s.node),
    text("m", 680, 445, "mistgate-node run", "end"),
    s.cols.map(([t, ls], i) => {
      const x = 40 + i * 220;
      return [rect("pill", x, 468, 200, 104, 11), chip(colTone[i][0], colTone[i][1], x + 12, 480, 22), text("t", x + 44, 495.5, t), lines("m", x + 14, 526, ls, 16)];
    }),
    // VPN traffic
    flow("sky", [[370, 572], [370, 620]], { both: true, cls: "wide" }),
    rect("card", 24, 620, 672, 44, 14),
    chip("grey", "globe", 40, 631, 22),
    text("t", 70, 646.5, s.traffic),
  ];
  mkdirSync(`${docs}/${lang}/diagrams`, { recursive: true });
  writeFileSync(`${docs}/${lang}/diagrams/components.svg`, svg({ w: W, h: H, title: s.title, desc: s.desc, body }));
}
console.log("D2 written");
