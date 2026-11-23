// N1-N4: docs/<lang>/diagrams/{shared-443,update-trust,two-views,mcp-plan-apply}.svg
// Each fact below is checked against the code and the docs: guide/shared-port.md (What a visitor sees), operations/updates.md
// (the agent's checks), internal/release (the panel's own signing context), operations/health.md and reachability.md (the two
// points of view), internal/panel/mcp/plan.go (plan, one-time token, ten minutes, approval).
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
const docs = fileURLToPath(new URL("../../docs", import.meta.url)); // the repository's docs/ next to site/
import { svg, rect, text, chip, item, flow, badge, lines, textWidth } from "./svgkit.mjs";

/** A card with a chip, a title and short lines of text. */
const card = (tone, icon, x, y, w, h, title, body, sub) => [
  rect("card", x, y, w, h, 14),
  chip(tone, icon, x + 12, y + 12, 24),
  text("t", x + 46, y + 29, title),
  sub ? text("m", x + 12, y + 56, sub) : "",
  lines("s", x + 12, y + (sub ? 76 : 58), body, 16),
];
const write = (lang, name, s, W, H, body) => {
  mkdirSync(`${docs}/${lang}/diagrams`, { recursive: true });
  writeFileSync(`${docs}/${lang}/diagrams/${name}.svg`, svg({ w: W, h: H, title: s.title, desc: s.desc, body }));
};

// ---- N1: one TCP 443 for several services
const N1 = {
  en: {
    title: "One TCP 443, several services",
    desc: "Visitors reach TCP 443 of the node. The node's agent reads the name a visitor asks for in the first bytes and hands the connection to the service that holds that name, decrypting nothing. A visitor with no name, another name, or not TLS goes to the default service, the node's own site.",
    client: "A client", stranger: "A stranger", strangerSub: "no name, or not TLS",
    agent: "Node agent", agentBody: ["Reads the name the visitor", "asks for (SNI) in the first", "bytes and hands the", "connection on. Decrypts", "nothing, answers nothing."],
    def: ["default: the node's", "own site"],
    legend: [["A name a service holds: that service", "answers, as if it were alone on the port."], ["Anything else (no name, another name, not", "TLS, a service that is down): the default."]],
  },
  ru: {
    title: "Один TCP 443, несколько сервисов",
    desc: "Посетители приходят на TCP 443 ноды. Агент ноды читает в первых байтах имя, которое запрашивает посетитель, и передаёт соединение сервису с этим именем, ничего не расшифровывая. Посетитель без имени, с чужим именем или не по TLS попадает на сервис по умолчанию, собственный сайт ноды.",
    client: "Клиент", stranger: "Посторонний", strangerSub: "без имени или не TLS",
    agent: "Агент ноды", agentBody: ["Читает в первых байтах", "имя, которое запрашивает", "посетитель (SNI), и", "передаёт соединение.", "Ничего не расшифровывает", "и ничего не отвечает."],
    def: ["по умолчанию: собственный", "сайт ноды"],
    legend: [["Имя, которое держит сервис: отвечает", "он, как будто он один на порту."], ["Всё остальное (нет имени, чужое имя, не", "TLS, сервис не работает): сервис по умолчанию."]],
  },
  zh: {
    title: "一个 TCP 443，多个服务",
    desc: "访问者连到节点的 TCP 443。节点的 agent 在最前面的字节里读取访问者请求的名称，把连接交给持有该名称的服务，全程不解密。没有名称、名称不对或不是 TLS 的访问者，会交给默认服务，也就是节点自己的网站。",
    client: "客户端", stranger: "陌生访问者", strangerSub: "没有名称，或不是 TLS",
    agent: "节点 agent", agentBody: ["在最前面的字节里读取访问", "者请求的名称（SNI），再把", "连接交出去。不解密任何内", "容，自己也不回应。"],
    def: ["默认：节点自己的", "网站"],
    legend: [["某个服务持有的名称：由该服务回应，", "就像它独占这个端口。"], ["其余情况（没有名称、名称不对、不是", "TLS、服务没在运行）：默认服务。"]],
  },
};
for (const [lang, s] of Object.entries(N1)) {
  const W = 720, H = 450;
  const body = [
    item("mint", "phone", 24, 64, 190, 48, s.client, "tt.example.com", "card"),
    item("mint", "phone", 24, 150, 190, 48, s.client, "de1.example.com", "card"),
    item("rose", "person", 24, 262, 190, 48, s.stranger, s.strangerSub, "card"),
    rect("card", 264, 40, 196, 290, 16),
    chip("sky", "nav-nodes", 280, 56, 26),
    text("h", 316, 74, s.agent),
    text("m", 280, 104, "TCP 443"),
    lines("s", 280, 132, s.agentBody, 17),
    item("lav", "tunnel", 520, 64, 176, 48, "TrustTunnel", "tt.example.com", "card"),
    item("sky", "shield", 520, 150, 176, 48, "VLESS REALITY", "de1.example.com", "card"),
    lines("s", 520, 222, s.def, 15),
    item("sage", "chat", 520, 262, 176, 48, "MTProto", "tg.example.com", "card"),
    flow("mint", [[214, 88], [264, 88]]),
    flow("mint", [[460, 88], [520, 88]]),
    flow("mint", [[214, 174], [264, 174]]),
    flow("mint", [[460, 174], [520, 174]]),
    flow("rose", [[214, 286], [264, 286]], { cls: "dash" }),
    flow("rose", [[460, 286], [490, 286], [490, 186], [520, 186]], { cls: "dash" }),
    badge("mint", 489, 124, 1),
    badge("rose", 489, 262, 2),
    `<line class="rule" x1="24" y1="360" x2="696" y2="360"/>`,
    s.legend.map((l, i) => [badge(i ? "rose" : "mint", 34 + i * 348, 386, i + 1), lines("s", 54 + i * 348, 391, l, 18)]),
  ];
  write(lang, "shared-443", s, W, H, body);
}

// ---- N2: the chain of trust of an update
const N2 = {
  en: {
    title: "What an update is trusted for",
    desc: "The release key signs the release; its private half stays offline. The panel only carries the signed node bundle to the nodes. The node agent checks the signature with the key compiled into it, then the format, the expiry, that the release is newer than itself, and that it has a file for its OS and architecture, and compares the size and SHA-256 with the signed manifest. A failure leaves the installed binary untouched.",
    cards: [["Release key", ["Ed25519; the", "private half is", "kept offline"]], ["Signed release", ["node bundle and", "panel manifest,", "each signed"]], ["Panel", ["only a courier:", "hands over what", "is signed"]], ["Node agent", ["verifies it all", "itself, then", "installs"]]],
    checks: "What the agent checks, in this order",
    steps: [["Signature", "built-in key"], ["Format", "known format"], ["Expiry", "not expired"], ["Newer", "than itself"], ["Its file", "its OS, CPU"]],
    then: ["Then the size and the SHA-256 of the download are compared with the signed manifest.", "Any failure leaves the installed binary untouched."],
    notes: [["A hacked panel cannot make a node run code", "that the release key has not signed."], ["The panel's manifest is signed in its own context,", "so a node never takes it as its own update."]],
  },
  ru: {
    title: "Чему доверяет обновление",
    desc: "Ключ релиза подписывает релиз, его закрытая половина хранится офлайн. Панель только возит подписанный пакет нод. Агент ноды проверяет подпись вшитым в него ключом, затем формат, срок действия, что релиз новее него самого и что в пакете есть файл под его ОС и архитектуру, и сверяет размер и SHA-256 с подписанным манифестом. При любой ошибке установленный бинарник не трогается.",
    cards: [["Ключ релиза", ["Ed25519; закрытая", "половина хранится", "офлайн"]], ["Релиз", ["пакет нод и", "манифест панели,", "оба с подписью"]], ["Панель", ["только курьер:", "передаёт то, что", "подписано"]], ["Агент ноды", ["всё проверяет", "сам, и только", "потом ставит"]]],
    checks: "Что проверяет агент, по порядку",
    steps: [["Подпись", "вшитый ключ"], ["Формат", "известный"], ["Срок", "не истёк"], ["Новее", "него самого"], ["Файл", "под его ОС"]],
    then: ["Затем размер и SHA-256 скачанного сверяются с подписанным манифестом.", "При любой ошибке установленный бинарник не трогается."],
    notes: [["Взломанная панель не заставит ноду запустить код,", "который не подписан ключом релиза."], ["Манифест панели подписан в своём контексте,", "поэтому нода не примет его за своё обновление."]],
  },
  zh: {
    title: "更新凭什么被信任",
    desc: "发布密钥为发布版本签名，其私钥部分离线保存。面板只负责把已签名的节点包送到节点。节点 agent 用编译进自身的密钥验证签名，再依次检查格式、有效期、发布版本比自己新、包中有适合自己操作系统和架构的文件，并把大小和 SHA-256 与已签名的清单比对。任何一步失败，已安装的二进制文件都保持不变。",
    cards: [["发布密钥", ["Ed25519；私钥", "部分离线保存"]], ["已签名的发布", ["节点包和面板清单，", "各自带签名"]], ["面板", ["只是信使：", "转交已签名的内容"]], ["节点 agent", ["自己验证全部内容，", "之后才安装"]]],
    checks: "agent 按顺序检查的内容",
    steps: [["签名", "内置密钥"], ["格式", "已知格式"], ["有效期", "未过期"], ["更新", "比自己新"], ["文件", "适合其系统"]],
    then: ["然后把下载内容的大小和 SHA-256 与已签名的清单比对。", "任何一步失败，已安装的二进制文件都保持不变。"],
    notes: [["被攻破的面板也无法让节点运行", "发布密钥没有签名的代码。"], ["面板的清单在自己的上下文中签名，", "所以节点不会把它当作自己的更新。"]],
  },
};
for (const [lang, s] of Object.entries(N2)) {
  const W = 720, H = 478;
  const tones = [["lav", "key"], ["sand", "file"], ["sky", "nav-overview"], ["mint", "nav-nodes"]];
  const body = [
    s.cards.map(([t, b], i) => card(tones[i][0], tones[i][1], 24 + i * 180, 32, 132, 112, t, b)),
    [0, 1, 2].map((i) => flow(["lav", "sand", "sky"][i], [[156 + i * 180, 88], [204 + i * 180, 88]])),
    rect("card", 24, 176, 672, 168, 16),
    chip("mint", "shield", 40, 192, 26),
    text("h", 76, 210, s.checks),
    s.steps.map(([t, sub], i) => [rect("pill", 40 + i * 130, 234, 118, 54, 11), badge("mint", 58 + i * 130, 261, i + 1), text("t", 74 + i * 130, 258, t), text("s", 74 + i * 130, 276, sub)]),
    lines("s", 40, 316, s.then, 17),
    `<line class="rule" x1="24" y1="372" x2="696" y2="372"/>`,
    s.notes.map((l, i) => [badge(i ? "sky" : "lav", 34 + i * 348, 402, i + 1), lines("s", 54 + i * 348, 407, l, 18)]),
  ];
  write(lang, "update-trust", s, W, H, body);
}

// ---- N3: two points of view on a node
const N3 = {
  en: {
    title: "Two points of view on a node",
    desc: "The panel's server opens a tunnel to every profile of a node every 5 minutes like an app does and asks two test pages. It is outside the country of your users, so it cannot see a block there. Optional country sources look from inside the country: Globalping probes, block lists, a probe on one of your nodes, cheburcheck volunteers and bsbord SIM probes. Their verdict is the From Russia column.",
    panel: ["The panel's server", ["opens a tunnel like an", "app, every 5 minutes"]], node: ["Node", "Hysteria2 · VLESS · AmneziaWG"], pages: ["Two test pages", ["Does the tunnel carry", "traffic? Where does it leave?"]],
    pageA: "gstatic.com/generate_204", pageB: "cloudflare.com/cdn-cgi/trace",
    cap: ["Client-eye checks: the panel is", "outside the country of your users,", "so it cannot see a block there."],
    inside: "From inside the country (optional, Russia by default)",
    srcs: ["Globalping", "Block lists", "TSPU probe", "cheburcheck", "bsbord"],
    verdict: ["One verdict per node:", "the From Russia column"],
  },
  ru: {
    title: "Два взгляда на ноду",
    desc: "Сервер панели каждые 5 минут, как приложение, открывает туннель к каждому профилю ноды и запрашивает две тестовые страницы. Он находится вне страны ваших людей и не видит блокировку там. Необязательные источники смотрят изнутри страны: пробы Globalping, списки блокировок, проба на одной из ваших нод, добровольцы cheburcheck и SIM-пробы bsbord. Их вердикт — столбец «Из России».",
    panel: ["Сервер панели", ["открывает туннель, как", "приложение, каждые 5 минут"]], node: ["Нода", "Hysteria2 · VLESS · AmneziaWG"], pages: ["Тест-страницы", ["Проходит ли трафик", "и где он выходит?"]],
    pageA: "gstatic.com/generate_204", pageB: "cloudflare.com/cdn-cgi/trace",
    cap: ["Проверки глазами клиента: панель", "вне страны ваших людей, поэтому", "блокировку там она не видит."],
    inside: "Изнутри страны (необязательно, по умолчанию Россия)",
    srcs: ["Globalping", "Списки", "ТСПУ-зонд", "cheburcheck", "bsbord"],
    verdict: ["Один вердикт на ноду:", "столбец «Из России»"],
  },
  zh: {
    title: "看一个节点的两种视角",
    desc: "面板所在的服务器每 5 分钟像应用一样为节点的每个配置打开隧道，并请求两个测试页面。它位于用户所在国家之外，所以看不到那里的封锁。可选的国家检查源从国内看：Globalping 探测点、封锁名单、你某个节点上的探测、cheburcheck 志愿者和 bsbord 的 SIM 探测。它们的结论就是“来自俄罗斯”那一列。",
    panel: ["面板所在服务器", ["像应用一样打开隧道，", "每 5 分钟一次"]], node: ["节点", "Hysteria2 · VLESS · AmneziaWG"], pages: ["两个测试页面", ["隧道能通流量吗？", "流量从哪里出去？"]],
    pageA: "gstatic.com/generate_204", pageB: "cloudflare.com/cdn-cgi/trace",
    cap: ["客户端视角的检查：面板在用户", "所在国家之外，所以看不到", "那里的封锁。"],
    inside: "从国内看（可选，默认俄罗斯）",
    srcs: ["Globalping", "封锁名单", "TSPU 探测", "cheburcheck", "bsbord"],
    verdict: ["每个节点一个结论：", "“来自俄罗斯”一列"],
  },
};
for (const [lang, s] of Object.entries(N3)) {
  const W = 720, H = 430;
  const body = [
    card("lav", "nav-overview", 24, 28, 200, 100, s.panel[0], s.panel[1]),
    rect("card", 260, 28, 200, 100, 16),
    chip("sky", "nav-nodes", 272, 40, 24),
    text("t", 306, 57, s.node[0]),
    text("m", 272, 90, s.node[1].split(" · ").slice(0, 2).join(" · ")),
    text("m", 272, 108, s.node[1].split(" · ")[2]),
    card("mint", "globe", 496, 28, 200, 100, s.pages[0], s.pages[1]),
    flow("sky", [[224, 78], [260, 78]], { cls: "wide" }),
    flow("sky", [[460, 78], [496, 78]], { cls: "wide" }),
    text("m", 596, 150, s.pageA, "middle"),
    text("m", 596, 166, s.pageB, "middle"),
    lines("s", 24, 164, s.cap, 17),
    `<line class="rule dash" x1="24" y1="248" x2="696" y2="248"/>`,
    chip("sand", "map", 24, 262, 24),
    text("h", 58, 280, s.inside),
    s.srcs.map((t, i) => [rect("pill", 24 + i * 136, 304, 124, 40, 11), text("t", 24 + i * 136 + 62, 329, t, "middle")]),
    flow("sand", [[630, 304], [630, 200], [430, 200], [430, 128]], { cls: "dash" }),
    rect("card", 24, 372, 672, 40, 12),
    text("t", 40, 397, s.verdict[0]),
    text("s", 40 + textWidth(s.verdict[0], 7.4, 14) + 10, 397, s.verdict[1]),
  ];
  write(lang, "two-views", s, W, H, body);
}

// ---- N4: plan, approval, apply
const N4 = {
  en: {
    title: "Plan, approval, apply",
    desc: "An AI agent connects with a token whose profile decides what it may do. A change takes two calls: a plan, where the panel describes the change and hands out a one-time token valid for ten minutes, and an apply that takes only that token, so nothing can change in between. A risky change waits for the owner's approval in the admin; a change that is not risky skips that step. Every call is in the audit log.",
    cards: [["AI agent", ["asks with a token:", "its profile sets", "what it may do"]], ["Plan", ["the panel describes", "the change and gives", "a one-time token"], "10 minutes"], ["Owner approves", ["only a risky change", "waits, in the admin,", "for the owner"]], ["Apply", ["takes only the", "token: nothing can", "change in between"]]],
    skip: "a change that is not risky skips the approval",
    foot: ["Every call is in the audit log. The token's profile (read only, operator or admin)", "decides which tools the agent sees at all."],
  },
  ru: {
    title: "План, одобрение, применение",
    desc: "ИИ-агент подключается с токеном, профиль которого решает, что ему можно. Изменение — это два вызова: план, где панель описывает изменение и выдаёт одноразовый токен на десять минут, и применение, которое принимает только этот токен, так что между ними ничего не поменять. Рискованное изменение ждёт одобрения владельца в админке; нерискованное этот шаг пропускает. Каждый вызов попадает в журнал аудита.",
    cards: [["ИИ-агент", ["приходит с токеном:", "его профиль решает,", "что ему можно"]], ["План", ["панель описывает", "изменение и выдаёт", "одноразовый токен"], "10 минут"], ["Одобрение", ["рискованное", "изменение ждёт", "владельца в", "админке"]], ["Применение", ["принимает только", "токен: между планом", "и применением не", "поменять ничего"]]],
    skip: "нерискованное изменение пропускает одобрение",
    foot: ["Каждый вызов попадает в журнал аудита. Профиль токена (только чтение,", "оператор или админ) решает, какие инструменты агент вообще видит."],
  },
  zh: {
    title: "方案、批准、执行",
    desc: "AI 代理带着令牌连接，令牌的配置决定它能做什么。一次更改要调用两次：先是方案，面板用自己的话描述更改，并给出一个十分钟内有效的一次性令牌；再是执行，它只接受这个令牌，所以两次调用之间什么都改不了。有风险的更改要等所有者在管理后台批准；没有风险的更改跳过这一步。每次调用都记入审计日志。",
    cards: [["AI 代理", ["带着令牌来访：", "令牌的配置决定", "它能做什么"]], ["方案", ["面板描述这次更改，", "并给出一次性令牌"], "10 分钟"], ["所有者批准", ["有风险的更改要在", "管理后台等所有者", "批准"]], ["执行", ["只接受令牌：", "方案与执行之间", "什么都改不了"]]],
    skip: "没有风险的更改跳过批准",
    foot: ["每次调用都记入审计日志。令牌的配置（只读、操作员或管理员）", "决定代理能看到哪些工具。"],
  },
};
for (const [lang, s] of Object.entries(N4)) {
  const W = 720, H = 320;
  const tones = [["lav", "sparkle"], ["sky", "list"], ["rose", "lock"], ["mint", "bolt"]];
  const body = [
    s.cards.map(([t, b, sub], i) => card(tones[i][0], tones[i][1], 24 + i * 174, 74, 150, 140, t, b, sub)),
    [0, 1, 2].map((i) => flow(["lav", "sky", "rose"][i], [[174 + i * 174, 144], [198 + i * 174, 144]])),
    flow("grey", [[273, 74], [273, 50], [621, 50], [621, 74]], { cls: "dash" }),
    text("s", 447, 42, s.skip, "middle"),
    `<line class="rule" x1="24" y1="248" x2="696" y2="248"/>`,
    lines("s", 24, 278, s.foot, 18),
  ];
  write(lang, "mcp-plan-apply", s, W, H, body);
}
console.log("N1-N4 written");
