// D1 "How it fits together": docs/<lang>/diagrams/how-it-fits.svg
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
const docs = fileURLToPath(new URL("../../docs", import.meta.url)); // the repository's docs/ next to site/
import { svg, rect, text, chip, item, flow, badge, lines } from "./svgkit.mjs";

const S = {
  en: {
    title: "How Mistgate fits together",
    you: "You", owner: "the owner", app: "Person's app", apps: "Happ, kl!ck, …",
    panel: "Panel", admin: "Admin", adminSub: "secret address", decoy: "Decoy site", decoySub: "for everyone else",
    subs: "Subscriptions", subsSub: "one link each", agents: "Agent endpoint", agentsSub: "mutual TLS",
    node: "Node", hy: "Hysteria2", hySub: "UDP 443", awg: "AmneziaWG", awgSub: "UDP ports",
    net: "Internet", netSub: ["direct", "or WARP"],
    legend: [
      ["You run everything in the admin,", "on a secret address."],
      ["A person's app fetches one link", "and gets its list of servers."],
      ["VPN traffic goes straight to the node", "and out, never through the panel."],
      ["The node dials the panel: it gets the", "desired state and reports back."],
    ],
  },
  ru: {
    title: "Как устроен Mistgate",
    you: "Вы", owner: "владелец", app: "Приложение", apps: "Happ, kl!ck, …",
    panel: "Панель", admin: "Админка", adminSub: "секретный адрес", decoy: "Сайт-ширма", decoySub: "для всех остальных",
    subs: "Подписки", subsSub: "ссылка на человека", agents: "Агенты нод", agentsSub: "взаимный TLS",
    node: "Нода", hy: "Hysteria2", hySub: "UDP 443", awg: "AmneziaWG", awgSub: "UDP-порты",
    net: "Интернет", netSub: ["напрямую", "или WARP"],
    legend: [
      ["Вы управляете всем в админке", "на секретном адресе."],
      ["Приложение человека получает", "по одной ссылке список серверов."],
      ["VPN-трафик идёт прямо на ноду", "и в интернет, мимо панели."],
      ["Нода сама подключается к панели:", "получает настройки и отчитывается."],
    ],
  },
  zh: {
    title: "Mistgate 各部分如何配合",
    you: "你", owner: "所有者", app: "用户的应用", apps: "Happ, kl!ck, …",
    panel: "面板", admin: "管理后台", adminSub: "私密地址", decoy: "伪装站点", decoySub: "面向其他访问者",
    subs: "订阅", subsSub: "每人一个链接", agents: "Agent 接入端点", agentsSub: "双向 TLS",
    node: "节点", hy: "Hysteria2", hySub: "UDP 443", awg: "AmneziaWG", awgSub: "UDP 端口",
    net: "互联网", netSub: ["直连", "或 WARP"],
    legend: [
      ["你在管理后台里完成所有操作，", "后台位于私密地址。"],
      ["用户的应用通过一个链接，", "获取自己的服务器列表。"],
      ["VPN 流量直接发往节点，再转到", "互联网，不经过面板。"],
      ["节点主动连接面板：获取", "期望状态并回报情况。"],
    ],
  },
};

const W = 720, H = 524;
for (const [lang, s] of Object.entries(S)) {
  const tones = ["lav", "mint", "sky", "lav"];
  const body = [
    // you and the person's app, level with the two rows of the panel
    item("lav", "person", 24, 82, 180, 48, s.you, s.owner, "card"),
    item("mint", "phone", 24, 138, 180, 48, s.app, s.apps, "card"),
    // the panel
    rect("card", 276, 24, 420, 178, 16),
    chip("lav", "nav-overview", 292, 40, 26),
    text("h", 328, 58, s.panel),
    text("m", 680, 57, "panel.example.com", "end"),
    item("lav", "lock", 292, 82, 192, 48, s.admin, s.adminSub),
    item("sage", "mask", 492, 82, 192, 48, s.decoy, s.decoySub),
    item("mint", "link", 292, 138, 192, 48, s.subs, s.subsSub),
    item("sky", "shield", 492, 138, 192, 48, s.agents, s.agentsSub),
    // the node and the internet
    rect("card", 276, 262, 290, 122, 16),
    chip("sky", "nav-nodes", 292, 278, 26),
    text("h", 328, 296, s.node),
    text("m", 550, 295, "de1.example.com", "end"),
    item("sky", "bolt", 292, 320, 125, 48, s.hy, s.hySub),
    item("sky", "key", 425, 320, 125, 48, s.awg, s.awgSub),
    rect("card", 594, 290, 102, 102, 16),
    chip("grey", "globe", 633, 304, 24),
    text("t", 645, 348, s.net, "middle"),
    lines("m", 645, 364, s.netSub, 14, "middle"),
    // 1 you -> admin, 2 app -> subscriptions, 3 traffic app -> node -> internet, 4 node dials the panel
    flow("lav", [[204, 106], [292, 106]]),
    flow("mint", [[204, 162], [292, 162]]),
    flow("sky", [[114, 186], [114, 344], [292, 344]], { cls: "wide" }),
    flow("sky", [[550, 344], [594, 344]], { cls: "wide" }),
    flow("lav", [[534, 262], [534, 186]], { both: true, cls: "dash" }),
    badge("lav", 248, 106, 1),
    badge("mint", 248, 162, 2),
    badge("sky", 200, 344, 3),
    badge("lav", 534, 232, 4),
    // legend
    `<line class="rule" x1="24" y1="414" x2="696" y2="414"/>`,
    s.legend.map((l, i) => {
      const x = i % 2 ? 372 : 24, y = i < 2 ? 444 : 488;
      return [badge(tones[i], x + 10, y - 4, i + 1), lines("s", x + 30, y, l, 18)];
    }),
  ];
  const desc = s.legend.map((l, i) => `${i + 1}. ${l.join(lang === "zh" ? "" : " ")}`).join(" ");
  mkdirSync(`${docs}/${lang}/diagrams`, { recursive: true });
  writeFileSync(`${docs}/${lang}/diagrams/how-it-fits.svg`, svg({ w: W, h: H, title: s.title, desc, body }));
}
console.log("D1 written");
