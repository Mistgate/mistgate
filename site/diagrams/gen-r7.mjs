// D3 one node with Hysteria2, AmneziaWG and WARP; D4 a node's ports; D7 who gets what -> docs/<lang>/diagrams/*.svg
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
const docs = fileURLToPath(new URL("../../docs", import.meta.url)); // the repository's docs/ next to site/
import { svg, rect, text, chip, item, flow, lines, yn } from "./svgkit.mjs";
const out = (lang, name, w, h, title, desc, body) => writeFileSync(`${docs}/${lang}/diagrams/${name}.svg`, svg({ w, h, title, desc, body }));
const nodeHead = (x, y, w, title, sub) => [chip("sky", "nav-nodes", x + 16, y + 16, 26), text("h", x + 52, y + 34, title), sub ? text("m", x + w - 16, y + 33, sub, "end") : ""];

// ---- D3: profiles, exits, fail-closed ----
const D3 = {
  en: {
    title: "One node with Hysteria2, AmneziaWG and WARP",
    desc: "Each profile on a node has its own exit. A profile with the Direct exit leaves from the node's address; a profile with the WARP exit leaves through the node's one WARP account, so sites see a Cloudflare address. Without working WARP the WARP-exit profiles carry no traffic and never fall back to the node's address.",
    apps: "Apps", appsSub: ["Happ, kl!ck,", "AmneziaVPN"], node: "Node",
    profiles: [["Hysteria2", "Exit: Direct"], ["Hysteria2, WARP copy", "Exit: WARP"], ["AmneziaWG", "Exit: WARP"]],
    direct: "Direct", directSub: "node's IP", warp: "WARP", warpSub: "one account",
    seesDirect: ["sites see", "the node's IP"], seesWarp: ["sites see", "a Cloudflare IP"],
    failT: "Fail-closed", fail: ["Without working WARP, the WARP-exit profiles carry no traffic:", "they never fall back to the node's own address."],
  },
  ru: {
    title: "Одна нода с Hysteria2, AmneziaWG и WARP",
    desc: "У каждого профиля на ноде свой выход. Профиль с выходом «Напрямую» выходит с адреса ноды, профиль с выходом WARP — через единственный WARP-аккаунт ноды, и сайты видят адрес Cloudflare. Без рабочего WARP профили с выходом WARP не пропускают трафик и никогда не переходят на адрес ноды.",
    apps: "Приложения", appsSub: ["Happ, kl!ck,", "AmneziaVPN"], node: "Нода",
    profiles: [["Hysteria2", "Выход: Напрямую"], ["Hysteria2, копия WARP", "Выход: WARP"], ["AmneziaWG", "Выход: WARP"]],
    direct: "Напрямую", directSub: "адрес ноды", warp: "WARP", warpSub: "один аккаунт",
    seesDirect: ["сайты видят", "IP ноды"], seesWarp: ["сайты видят", "IP Cloudflare"],
    failT: "Закрыто при сбое", fail: ["Без рабочего WARP профили с выходом WARP не пропускают трафик:", "через собственный адрес ноды они не выходят никогда."],
  },
};
for (const [lang, s] of Object.entries(D3)) {
  const py = [96, 156, 216];
  out(lang, "node-exits", 720, 392, s.title, s.desc, [
    rect("card", 24, 128, 150, 84, 14), chip("mint", "phone", 38, 142, 24), text("t", 70, 159, s.apps), lines("m", 38, 184, s.appsSub, 15),
    rect("card", 196, 40, 400, 240, 16), nodeHead(196, 40, 400, s.node, "de1.example.com"),
    s.profiles.map((p, i) => item("sage", "sliders", 212, py[i], 210, 46, p[0], p[1])),
    item("grey", "server", 446, 96, 134, 46, s.direct, s.directSub),
    item("sky", "globe", 446, 176, 134, 46, s.warp, s.warpSub),
    // apps -> each profile
    `<g class="grey"><path class="flow" d="M174 170H190M190 119V239"/></g>`,
    py.map((y) => flow("grey", [[190, y + 23], [212, y + 23]])),
    // profiles -> exits
    flow("grey", [[422, 119], [446, 119]]),
    `<g class="sky"><path class="flow" d="M422 179H434V239H422"/></g>`, flow("sky", [[434, 199], [446, 199]]),
    // exits -> the internet
    flow("grey", [[580, 119], [612, 119]], { cls: "wide" }), lines("s", 620, 114, s.seesDirect, 16),
    flow("sky", [[580, 199], [612, 199]], { cls: "wide" }), lines("s", 620, 194, s.seesWarp, 16),
    rect("card", 24, 296, 672, 76, 14), chip("rose", "shieldOff", 38, 312, 24), text("t", 72, 329, s.failT), lines("s", 72, 348, s.fail, 17),
  ]);
}

// ---- D4: what a node listens on and dials ----
const D4 = {
  en: {
    title: "The ports of a node",
    desc: "A node listens on UDP for its servers: Hysteria2 on 443, an optional Hysteria2 port-hopping range from 1024 up, AmneziaWG on a port between 10000 and 60000. TCP 443 serves the HTTPS site Hysteria2 shows to everyone who is not a client, TCP 80 is for Let's Encrypt, and new SSH connections are rate-limited. Out of the node go the agent's connection to the panel, Let's Encrypt and DNS, Cloudflare WARP when a profile uses it, and a speed test only when you measure. The panel needs no port on the node.",
    inbound: "Inbound", outbound: "Outbound", node: "Node",
    rows: [["sky", "bolt", "Hysteria2", "UDP 443", "apps"], ["sky", "bolt", "Hop range", "UDP, from 1024, optional", "apps"], ["sky", "key", "AmneziaWG", "UDP 10000–60000", "apps"], ["sage", "mask", "HTTPS site", "TCP 443, for non-clients", "browsers"], ["sand", "lock", "Let's Encrypt", "TCP 80, when free", "Let's Encrypt"], ["grey", "terminal", "SSH", "new connections limited", "you"]],
    outs: [["lav", "nav-overview", "Panel", "TCP 443"], ["grey", "globe", "Let's Encrypt, DNS", ""], ["sky", "globe", "Cloudflare WARP", "if used"], ["grey", "gauge", "Speed test", "on request"]],
    noPortT: "No management port", noPort: ["The agent dials the panel; the panel never connects to the node.", "An active UFW gets tagged rules for the UDP ports; firewalld is only checked."],
  },
  ru: {
    title: "Порты ноды",
    desc: "Нода слушает UDP для своих серверов: Hysteria2 на 443, необязательный диапазон прыжков Hysteria2 от 1024, AmneziaWG на порту от 10000 до 60000. TCP 443 отдаёт HTTPS-сайт, который Hysteria2 показывает всем, кто не клиент, TCP 80 нужен Let's Encrypt, а новые SSH-подключения ограничены по частоте. Из ноды наружу идут соединение агента с панелью, Let's Encrypt и DNS, Cloudflare WARP, если его использует профиль, и тест скорости только когда вы меряете. Панели не нужен никакой порт на ноде.",
    inbound: "Входящие", outbound: "Исходящие", node: "Нода",
    rows: [["sky", "bolt", "Hysteria2", "UDP 443", "приложения"], ["sky", "bolt", "Диапазон прыжков", "UDP от 1024, по желанию", "приложения"], ["sky", "key", "AmneziaWG", "UDP 10000–60000", "приложения"], ["sage", "mask", "HTTPS-сайт", "TCP 443, для чужих", "браузеры"], ["sand", "lock", "Let's Encrypt", "TCP 80, если свободен", "Let's Encrypt"], ["grey", "terminal", "SSH", "новые подключения", "вы"]],
    outs: [["lav", "nav-overview", "Панель", "TCP 443"], ["grey", "globe", "Let's Encrypt, DNS", ""], ["sky", "globe", "Cloudflare WARP", "если включён"], ["grey", "gauge", "Тест скорости", "по запросу"]],
    noPortT: "Порта управления нет", noPort: ["Агент сам подключается к панели, панель к ноде не подключается никогда.", "Во включённый UFW агент ставит свои правила для UDP-портов, firewalld только проверяет."],
  },
};
for (const [lang, s] of Object.entries(D4)) {
  const ry = (i) => 92 + i * 50, oy = (i) => 96 + i * 72;
  out(lang, "node-ports", 720, 496, s.title, s.desc, [
    `<text class="m" x="24" y="56" font-weight="700" letter-spacing="1">${s.inbound.toUpperCase()}</text>`,
    `<text class="m" x="696" y="56" font-weight="700" letter-spacing="1" text-anchor="end">${s.outbound.toUpperCase()}</text>`,
    rect("card", 196, 36, 300, 352, 16), nodeHead(196, 36, 300, s.node, "de1"),
    s.rows.map(([tone, ico, t, sub, from], i) => [item(tone, ico, 212, ry(i), 268, 42, t, sub), text("s", 24, ry(i) + 25, from), flow("grey", [[136, ry(i) + 21], [212, ry(i) + 21]])]),
    s.outs.map(([tone, ico, t, sub], i) => [flow(tone, [[496, oy(i) + 24], [532, oy(i) + 24]]), item(tone, ico, 532, oy(i), 164, 48, t, sub || null, "card")]),
    rect("card", 24, 404, 672, 76, 14), chip("grey", "shield", 38, 420, 24), text("t", 72, 437, s.noPortT), lines("s", 72, 456, s.noPort, 17),
  ]);
}

// ---- D7: who gets what ----
const D7 = {
  en: {
    title: "Who gets which server",
    desc: "Alice is active, in the group Family, may use all nodes, and has the Happ switch on and AmneziaVPN off. Family holds the profiles Hysteria2 and AmneziaWG. On de1 both profiles are switched on, on nl1 Hysteria2 is switched off. Alice gets Hysteria2 on de1; not Hysteria2 on nl1, which is off; not AmneziaWG, because none of her switched-on apps reads it.",
    alice: "Alice", user: "user", status: "Status", active: "Active", group: "Group", family: "Family", nodes: "Nodes", all: "all", on: "on", off: "off",
    groupSub: "group", profilesNote: "the profiles of the group", nodesT: "Servers on nodes",
    gets: "What Alice gets", yes: "yes", no: "no",
    result: [[true, "Hysteria2 on de1"], [false, "Hysteria2 on nl1: switched off on the node"], [false, "AmneziaWG on de1: none of her apps reads it, AmneziaVPN is off"]],
  },
  ru: {
    title: "Кто какой сервер получает",
    desc: "Алиса активна, в группе «Семья», ей можно пользоваться всеми нодами, переключатель Happ у неё включён, AmneziaVPN выключен. В группе «Семья» профили Hysteria2 и AmneziaWG. На de1 оба профиля включены, на nl1 Hysteria2 выключен. Алиса получает Hysteria2 на de1; не получает Hysteria2 на nl1, он выключен, и не получает AmneziaWG, потому что ни одно её включённое приложение его не читает.",
    alice: "Алиса", user: "пользователь", status: "Статус", active: "Активен", group: "Группа", family: "Семья", nodes: "Ноды", all: "все", on: "вкл", off: "выкл",
    groupSub: "группа", profilesNote: "профили группы", nodesT: "Серверы на нодах",
    gets: "Что получает Алиса", yes: "да", no: "нет",
    result: [[true, "Hysteria2 на de1"], [false, "Hysteria2 на nl1: выключен на ноде"], [false, "AmneziaWG на de1: его не читает ни одно её приложение, AmneziaVPN выключен"]],
  },
};
for (const [lang, s] of Object.entries(D7)) {
  const row = (x, w, y, label, right) => [text("s", x + 16, y + 15, label), !right.startsWith("<") ? text("t", x + w - 16, y + 15, right, "end") : right];
  out(lang, "who-gets-what", 720, 452, s.title, s.desc, [
    // the user
    rect("card", 24, 24, 212, 214, 16), chip("sand", "person", 40, 40, 26), text("h", 76, 58, s.alice), text("m", 220, 57, s.user, "end"),
    row(24, 212, 84, s.status, yn(true, 220, 80, s.active, true)),
    row(24, 212, 114, s.group, s.family),
    row(24, 212, 144, s.nodes, s.all),
    row(24, 212, 174, "Happ", yn(true, 220, 170, s.on, true)),
    row(24, 212, 204, "AmneziaVPN", yn(false, 220, 200, s.off, true)),
    // the group
    rect("card", 254, 24, 212, 214, 16), chip("sand", "people", 270, 40, 26), text("h", 306, 58, s.family), text("m", 450, 57, s.groupSub, "end"),
    item("sage", "sliders", 270, 84, 180, 44, "Hysteria2", null),
    item("sage", "sliders", 270, 136, 180, 44, "AmneziaWG", null),
    text("m", 270, 210, s.profilesNote),
    // the nodes
    rect("card", 484, 24, 212, 214, 16), chip("sky", "nav-nodes", 500, 40, 26), text("t", 536, 57, s.nodesT),
    row(484, 212, 84, "de1 · Hysteria2", yn(true, 680, 80, s.on, true)),
    row(484, 212, 124, "de1 · AmneziaWG", yn(true, 680, 120, s.on, true)),
    row(484, 212, 164, "nl1 · Hysteria2", yn(false, 680, 160, s.off, true)),
    // together
    `<g class="grey"><path class="flow" d="M130 238V254M360 238V254M590 238V254M130 254H590"/></g>`, flow("grey", [[360, 254], [360, 282]]),
    rect("card", 24, 282, 672, 154, 16), chip("mint", "check", 40, 298, 26), text("h", 76, 316, s.gets),
    s.result.map(([ok, t], i) => [yn(ok, 40, 340 + i * 30, ok ? s.yes : s.no), text(ok ? "t" : "s", 98, 355 + i * 30, t)]),
  ]);
}
console.log("D3, D4, D7 written");
