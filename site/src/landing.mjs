// The home page: who Mistgate is for and what it does, how it fits together (D1), why it is worth a look, how to
// start, and the docs by admin section. The copy follows README.md / README.ru.md; facts and numbers come from the
// docs (benchmarks, comparison). docs/<lang>/index.md stays the sidebar's source and the docs' start page on GitHub.
import { fileURLToPath } from "node:url";
import { GITHUB, pageUrl } from "./urls.mjs";
import { icon, NAV } from "./icons.mjs";
import { inlineSvg } from "./diagrams.mjs";

const DOCS = fileURLToPath(new URL("../../docs/", import.meta.url));
const esc = (s) => String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
// `code` in copy -> <code>
const rich = (s) => esc(s).replace(/`([^`]+)`/g, "<code>$1</code>");

export const LANDING = {
  en: {
    meta: "Mistgate is a self-hosted VPN panel for family, friends, a community or a small team: Hysteria2 and AmneziaWG on your own servers, one link per person, every server checked the way an app sees it.",
    eyebrow: "Beta · self-hosted · open source · AGPL-3.0",
    h1: "Your own VPN for your people, without babysitting servers.",
    lead: "For family, friends, a community or a small team. Install one panel, add your servers from the browser and send each person one link. Mistgate keeps the servers configured and checks them the way an app does, so you hear about an outage first.",
    install: "Install the panel", how: "How it works",
    facts: ["Hysteria2", "AmneziaWG 2.0 · 3.1", "Cloudflare WARP", "Linux amd64 · arm64", "SQLite, no Docker", "English · Русский"],
    shotAlt: "The Mistgate admin: nodes with their health and throughput, an open problem and recent events",
    fitH: "How it fits together",
    fitP: "Two programs: the panel and an agent on every server. A person's app talks to the panel only to fetch its link; VPN traffic goes straight to the node.",
    fitAlt: "How Mistgate fits together: you use the admin, a person's app fetches its link from the panel, VPN traffic goes from the app to the node and out to the internet, and the node keeps one connection to the panel",
    whyH: "Why Mistgate",
    why: [
      ["subscriptions", "One link, several protocols", "Apps on the Mihomo core get Hysteria2 and AmneziaWG from the same link: when one protocol is blocked on someone's network, the other is already in their app.", "guide/client-apps"],
      ["nodes", "Servers added from the browser", "Type the IP and the root password. The panel pins the host key, checks the server, opens the ports, installs the signed agent and waits for it to connect.", "getting-started/ssh-install"],
      ["health", "You find out first", "Every 5 minutes the panel connects to each server the way an app does. A doctor on every node runs 16 host checks, and alerts go to Telegram.", "operations/health"],
      ["settings", "Light and hidden", "Two static Go binaries and SQLite, no Docker. The panel idles at 16–20 MiB, and a stranger who opens its address sees a decoy site.", "operations/security"],
    ],
    more: "More",
    proof: [
      ["gauge", "Benchmarks", "On a 1 vCPU / 1 GB server the panel idles at 16–20 MiB; 3x-ui at 68–74, PasarGuard at 262–265, Remnawave at 426–454. The method, the versions, and where Mistgate is slower.", "reference/benchmarks"],
      ["layers", "Comparison with other panels", "Mistgate next to 3x-ui, Marzban, PasarGuard, Remnawave, Hiddify Manager and s-ui, with sources, including where the others are ahead.", "reference/comparison"],
    ],
    startH: "Quick start",
    startP: "From an empty Linux server to the first person with a working link.",
    steps: [
      ["Install the panel", "The signed binary from the latest release, `mistgate setup` and a systemd unit. Or give the install prompt to an AI agent.", ["getting-started/install-panel", "getting-started/ai-agents"]],
      ["Add a node", "**Nodes → Add node** installs the agent over SSH, or gives you a one-time command to run on the server yourself.", ["getting-started/ssh-install", "getting-started/add-node"]],
      ["Give people their links", "Put a profile on the node, create a user and send them the link.", ["getting-started/first-users"]],
      ["Make it safe to rely on", "Link Telegram for alerts, check the security settings and set up encrypted backups before others depend on it.", ["operations/telegram-alerts", "operations/security", "operations/backups"]],
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
      subscriptions: "Subscription formats, delivery rules, mirrors and the page users see.",
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
    meta: "Mistgate — панель для своего VPN для семьи, друзей, сообщества или небольшой команды: Hysteria2 и AmneziaWG на ваших серверах, одна ссылка на человека, каждый сервер проверяется так же, как его видит приложение.",
    eyebrow: "Бета · на своих серверах · открытый код · AGPL-3.0",
    h1: "Свой VPN для своих людей — без возни с серверами.",
    lead: "Для семьи, друзей, сообщества или небольшой команды. Вы ставите одну панель, добавляете серверы из браузера и отправляете каждому человеку одну ссылку. Mistgate держит серверы настроенными и проверяет их так же, как приложение, поэтому о сбое вы узнаёте первыми.",
    install: "Установить панель", how: "Как это устроено",
    facts: ["Hysteria2", "AmneziaWG 2.0 · 3.1", "Cloudflare WARP", "Linux amd64 · arm64", "SQLite, без Docker", "English · Русский"],
    shotAlt: "Админка Mistgate: ноды с их состоянием и скоростью, открытая проблема и последние события",
    fitH: "Как это устроено",
    fitP: "Две программы: панель и агент на каждом сервере. С панелью приложение человека говорит, только когда забирает ссылку; VPN-трафик идёт прямо на ноду.",
    fitAlt: "Как устроен Mistgate: вы работаете в админке, приложение человека забирает ссылку у панели, VPN-трафик идёт от приложения на ноду и в интернет, а нода держит одно соединение с панелью",
    whyH: "Зачем Mistgate",
    why: [
      ["subscriptions", "Одна ссылка, несколько протоколов", "Приложения на ядре Mihomo получают по одной ссылке и Hysteria2, и AmneziaWG: если в сети у человека заблокируют один протокол, второй уже есть в приложении.", "guide/client-apps"],
      ["nodes", "Серверы добавляются из браузера", "Введите IP и пароль root. Панель запомнит SSH-ключ сервера, проверит его, откроет порты, поставит подписанный агент и дождётся, пока он подключится.", "getting-started/ssh-install"],
      ["health", "Вы узнаёте первым", "Каждые 5 минут панель подключается к каждому серверу так же, как приложение. Доктор на каждой ноде делает 16 проверок хоста, предупреждения приходят в Telegram.", "operations/health"],
      ["settings", "Лёгкая и незаметная", "Два статических бинарника на Go и SQLite, без Docker. Панель в простое занимает 16–20 МиБ, а посторонний по её адресу видит сайт-ширму.", "operations/security"],
    ],
    more: "Подробнее",
    proof: [
      ["gauge", "Бенчмарки", "На сервере с 1 vCPU и 1 ГБ панель в простое занимает 16–20 МиБ; 3x-ui — 68–74, PasarGuard — 262–265, Remnawave — 426–454. Метод, версии и где Mistgate медленнее.", "reference/benchmarks"],
      ["layers", "Сравнение с другими панелями", "Mistgate рядом с 3x-ui, Marzban, PasarGuard, Remnawave, Hiddify Manager и s-ui, с источниками и с тем, в чём другие впереди.", "reference/comparison"],
    ],
    startH: "Быстрый старт",
    startP: "От пустого Linux-сервера до первого человека с рабочей ссылкой.",
    steps: [
      ["Установите панель", "Подписанный бинарник из последнего релиза, `mistgate setup` и unit для systemd. Или отдайте промпт установки ИИ-агенту.", ["getting-started/install-panel", "getting-started/ai-agents"]],
      ["Добавьте ноду", "**Ноды → Добавить ноду**: панель поставит агент по SSH или выдаст одноразовую команду, которую вы запустите на сервере сами.", ["getting-started/ssh-install", "getting-started/add-node"]],
      ["Раздайте ссылки", "Поставьте профиль на ноду, создайте пользователя и отправьте ему ссылку.", ["getting-started/first-users"]],
      ["Подготовьте к работе", "Подключите Telegram для алертов, проверьте настройки безопасности и настройте зашифрованные бэкапы, прежде чем на панель начнут полагаться другие.", ["operations/telegram-alerts", "operations/security", "operations/backups"]],
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
      subscriptions: "Форматы подписки, правила выдачи, зеркала и страница, которую видит пользователь.",
      health: "Алерты, проверки глазами клиента и доктор флота.",
      updates: "Версии панели и агентов нод, поэтапные выкатки и откат.",
      integrations: "API-токены для скриптов, MCP для ИИ-агентов и изменения, которые они просят одобрить.",
      settings: "Админы, безопасность, бэкапы и домены.",
    },
    refDesc: "Команды, флаги, переменные, архитектура и ответы на частые вопросы.",
    status: "Mistgate в бете: сейчас выходят релизы 0.1.x, и до 1.0 API, хранимые настройки и протокол нод ещё могут меняться.",
    statusLink: "Статус и план развития",
  },
};

/** Hero and body of the home page. `nav` gives the order and titles of the pages; `section(key)` names the admin section a page
 *  describes, "start" for the pages before the admin, null for the reference. */
export function renderLanding({ lang, t, nav, section }) {
  const L = LANDING[lang];
  const pages = new Map(nav.flatMap((g) => g.items).map((i) => [i.key, i]));
  const url = (key) => pageUrl(lang, key);
  const title = (key) => pages.get(key)?.title ?? key;
  const chip = (id, size = 26) => `<span class="id-chip" data-tone="${NAV[id].tone}" style="width:${size}px;height:${size}px">${icon(`nav-${id}`, Math.round(size * 0.6))}</span>`;
  const bold = (s) => rich(s).replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");

  const hero = `<section class="hero">
<svg class="hero__arch" viewBox="0 0 300 260" fill="none" aria-hidden="true"><path d="M30 260V150a120 120 0 0 1 240 0v110" style="stroke:var(--surface-2)" stroke-width="1.5"/><path d="M70 260V160a80 80 0 0 1 160 0v100" style="stroke:var(--border)" stroke-width="1.5"/><path d="M110 260V170a40 40 0 0 1 80 0v90" style="stroke:var(--accent-line)" stroke-width="1.5"/><path d="M0 214c40-14 80 14 120 0s80-14 120 0 40 10 60 4" style="stroke:var(--border)" stroke-width="1.5" stroke-linecap="round"/><path d="M24 238c40-12 70 12 110 0s80-12 120 0" style="stroke:var(--surface-2)" stroke-width="1.5" stroke-linecap="round"/></svg>
<p class="label">${esc(L.eyebrow)}</p>
<h1 class="hero__h1">${esc(L.h1)}</h1>
<p class="hero__lead">${esc(L.lead)}</p>
<div class="hero__cta"><a class="btn btn--primary" href="${url("getting-started/install-panel")}">${esc(L.install)}${icon("chevronRight", 14)}</a><a class="btn btn--secondary" href="${url("getting-started/overview")}">${esc(L.how)}</a><a class="btn btn--secondary" href="${GITHUB}" rel="noopener">GitHub${icon("externalLink", 14)}</a></div>
<ul class="facts">${L.facts.map((f) => `<li>${esc(f)}</li>`).join("")}</ul>
<figure class="shot"><img class="shot__light" src="/img/overview-light-${lang}.webp" width="1600" height="1000" alt="${esc(L.shotAlt)}"><img class="shot__dark" src="/img/overview-dark-${lang}.webp" width="1600" height="1000" alt="${esc(L.shotAlt)}"></figure>
</section>`;

  const fit = `<section class="home-sec" aria-labelledby="fit"><h2 id="fit">${esc(L.fitH)}</h2><p class="home-p">${esc(L.fitP)}</p>
<figure class="diagram diagram--svg" tabindex="0">${inlineSvg(`${DOCS}${lang}/diagrams/how-it-fits.svg`, L.fitAlt)}</figure></section>`;

  const why = `<section class="home-sec" aria-labelledby="why"><h2 id="why">${esc(L.whyH)}</h2>
<ul class="why">${L.why.map(([id, h, p, key]) => `<li>${chip(id, 30)}<div><h3>${esc(h)}</h3><p>${esc(p)} <a href="${url(key)}">${esc(L.more)}<span class="sr">: ${esc(title(key))}</span></a></p></div></li>`).join("")}</ul>
<div class="proof">${L.proof.map(([ico, h, p, key]) => `<a class="proof__card" href="${url(key)}"><span class="id-chip" data-tone="lavender" style="width:30px;height:30px">${icon(ico, 18)}</span><span class="proof__t">${esc(h)}${icon("chevronRight", 14)}</span><span class="proof__p">${esc(p)}</span></a>`).join("")}</div></section>`;

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

  return { hero, body: fit + why + start + sections, description: L.meta };
}
