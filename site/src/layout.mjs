// HTML templates and UI strings. No dependencies: plain template literals.
import { SITE, GITHUB, pageUrl, editUrl } from "./urls.mjs";
import { icon, NAV } from "./icons.mjs";
import { renderLanding } from "./landing.mjs";

export const T = {
  en: {
    hreflang: "en", htmlLang: "en", ogLocale: "en_US", name: "English", short: "EN", docs: "Docs", home: "Home", siteTitle: "Mistgate docs",
    skip: "Skip to content", menu: "Menu", closeMenu: "Close menu", navLabel: "Documentation", crumbs: "Breadcrumb",
    search: "Search docs", searchLabel: "Search the documentation", searchEmpty: "Nothing found. Try another word.", searchClose: "Close search", searchIn: "in",
    onPage: "On this page", prev: "Previous", next: "Next", edit: "Edit this page on GitHub", theme: "Switch theme", github: "Mistgate on GitHub", stars: "Mistgate on GitHub, {n} stars",
    language: "Language", copy: "Copy", copied: "Copied", table: "Table", plain: "Text", diagram: "Diagram", permalink: "Link to the section", pending: "This page is not written yet", roadmap: "Roadmap phases",
    license: "Released under AGPL-3.0", other: "Other",
    inAdmin: "In the admin", sections: { overview: "Overview", nodes: "Nodes", users: "Users", profiles: "Profiles", subscriptions: "Subscriptions", health: "Health", reachability: "Reachability", updates: "Updates", integrations: "Integrations", settings: "Settings" },
    notFound: "Page not found", notFoundText: "There is no such page. It may have moved or not been written yet.", toHome: "Go to the documentation",
  },
  ru: {
    hreflang: "ru", htmlLang: "ru", ogLocale: "ru_RU", name: "Русский", short: "RU", docs: "Документация", home: "Главная", siteTitle: "Документация Mistgate",
    skip: "К содержимому", menu: "Меню", closeMenu: "Закрыть меню", navLabel: "Документация", crumbs: "Навигация по разделам",
    search: "Поиск по документации", searchLabel: "Поиск по документации", searchEmpty: "Ничего не нашлось. Попробуйте другое слово.", searchClose: "Закрыть поиск", searchIn: "в",
    onPage: "На этой странице", prev: "Назад", next: "Дальше", edit: "Править страницу на GitHub", theme: "Сменить тему", github: "Mistgate на GitHub", stars: "Mistgate на GitHub, звёзд: {n}",
    language: "Язык", copy: "Копировать", copied: "Скопировано", table: "Таблица", plain: "Текст", diagram: "Схема", permalink: "Ссылка на раздел", pending: "Эта страница ещё не написана", roadmap: "Этапы плана",
    license: "Лицензия AGPL-3.0", other: "Прочее",
    inAdmin: "В админке", sections: { overview: "Обзор", nodes: "Ноды", users: "Пользователи", profiles: "Профили", subscriptions: "Подписки", health: "Здоровье", reachability: "Доступность", updates: "Обновления", integrations: "Интеграции", settings: "Настройки" },
    notFound: "Страница не найдена", notFoundText: "Такой страницы нет. Возможно, она переехала или ещё не написана.", toHome: "К документации",
  },
  zh: {
    hreflang: "zh-Hans", htmlLang: "zh-CN", ogLocale: "zh_CN", name: "简体中文", short: "中文", docs: "文档", home: "首页", siteTitle: "Mistgate 文档",
    skip: "跳到正文", menu: "菜单", closeMenu: "关闭菜单", navLabel: "文档", crumbs: "面包屑导航",
    search: "搜索文档", searchLabel: "搜索文档", searchEmpty: "没有找到结果。换个词试试。", searchClose: "关闭搜索", searchIn: "位于",
    onPage: "本页内容", prev: "上一页", next: "下一页", edit: "在 GitHub 上编辑此页", theme: "切换主题", github: "GitHub 上的 Mistgate", stars: "GitHub 上的 Mistgate，{n} 个星标",
    language: "语言", copy: "复制", copied: "已复制", table: "表格", plain: "文本", diagram: "示意图", permalink: "链接到此小节", pending: "此页尚未撰写", roadmap: "路线图阶段",
    license: "基于 AGPL-3.0 发布", other: "其他",
    inAdmin: "在管理后台", inAdminSep: "：", sections: { overview: "概览", nodes: "节点", users: "用户", profiles: "协议配置", subscriptions: "订阅", health: "运行状况", reachability: "可达性", updates: "更新", integrations: "集成", settings: "设置" },
    notFound: "页面不存在", notFoundText: "没有这个页面。它可能已移动，或尚未撰写。", toHome: "前往文档",
  },
};

const esc = (s) => String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const GH = '<svg class="ico" viewBox="0 0 16 16" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>';

// Runs before the stylesheet: restores the saved theme (none saved = follow the system), marks JS as available.
export const THEME_INIT = `try{var t=localStorage.getItem("mg-theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t}catch(e){}document.documentElement.classList.add("js")`;

// Which admin section a page is about: its icon and pastel come from the panel's sidebar (icons.tsx navPaths), and the
// page wears that tone (table headers, numbered steps, the "In the admin" label). Pages about no single section get an
// icon of their own in the neutral colour.
export const PAGE_SECTION = {
  "getting-started/overview": "overview",
  "getting-started/requirements": "nodes", "getting-started/add-node": "nodes", "getting-started/ssh-install": "nodes",
  "guide/nodes": "nodes", "guide/warp": "nodes", "guide/torrent-protection": "nodes",
  "getting-started/first-users": "users", "guide/users-and-groups": "users",
  "guide/profiles": "profiles", "guide/hysteria2": "profiles", "guide/amneziawg": "profiles",
  "guide/subscriptions": "subscriptions", "guide/client-apps": "subscriptions", "guide/user-page": "subscriptions", "guide/dns": "subscriptions",
  "operations/health": "health", "operations/troubleshooting": "health",
  "operations/reachability": "reachability",
  "operations/updates": "updates", "operations/releases": "updates",
  "getting-started/ai-agents": "integrations", "operations/telegram-alerts": "integrations", "reference/api": "integrations", "reference/mcp": "integrations",
  "getting-started/install-panel": "settings", "guide/decoy-site": "settings", "operations/backups": "settings", "operations/security": "settings", "operations/panel-watcher": "settings",
};
// pages that wear a section's colour but describe no screen of the admin: no "In the admin" label on them
const NOT_IN_ADMIN = new Set(["getting-started/overview", "getting-started/requirements", "getting-started/install-panel"]);
const PAGE_ICON = { index: "map", "reference/cli": "terminal", "reference/configuration": "sliders", "reference/architecture": "network", "reference/faq": "info", "roadmap/status": "list", "reference/comparison": "layers", "reference/benchmarks": "gauge" };
/** {section, tone, icon, inAdmin} of a page; section and tone are null for a page about no single admin section. */
export function pageStyle(key) {
  const section = PAGE_SECTION[key] ?? null;
  return section ? { section, tone: NAV[section].tone, icon: `nav-${section}`, inAdmin: !NOT_IN_ADMIN.has(key) } : { section: null, tone: null, icon: PAGE_ICON[key] ?? "text", inAdmin: false };
}

const wordmark = '<span class="wordmark">mist<span>gate</span></span>';

/** The star count on the GitHub button: 999, then 1k, 1.2k, 12.3k. app.js gets this same function (build.mjs inlines it). */
export const formatStars = (n) => (n > 999 ? `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k` : String(n));

/** The GitHub button: the mark, then a star and the count baked in at build time (none: hidden until app.js fetches one). */
function githubButton(t, stars) {
  return `<a class="ib gh" href="${GITHUB}" rel="noopener" aria-label="${esc(stars ? t.stars.replace("{n}", stars) : t.github)}" data-stars-label="${esc(t.stars)}">${GH}<span class="gh__stars"${stars ? "" : " hidden"}>${icon("star", 14)}<span class="gh__n">${stars ? formatStars(stars) : ""}</span></span></a>`;
}

function head({ lang, t, title, description, canonical, alternates, assets, noindex, home }) {
  const fonts = (lang === "ru" ? ["onest-cyrillic", "onest-latin"] : ["onest-latin"]).map((f) => `<link rel="preload" href="/fonts/${f}.woff2" as="font" type="font/woff2" crossorigin>`).join("\n");
  const alts = alternates.map((a) => `<link rel="alternate" hreflang="${a.lang}" href="${SITE}${a.url}">`).join("\n");
  const fullTitle = home ? title : `${title} — ${t.siteTitle}`;
  return `<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(fullTitle)}</title>
<meta name="description" content="${esc(description)}">
${noindex ? '<meta name="robots" content="noindex">' : `<link rel="canonical" href="${SITE}${canonical}">`}
${alts}
<meta property="og:site_name" content="Mistgate">
<meta property="og:type" content="website">
<meta property="og:title" content="${esc(fullTitle)}">
<meta property="og:description" content="${esc(description)}">
<meta property="og:url" content="${SITE}${canonical}">
<meta property="og:locale" content="${t.ogLocale}">
${assets.og ? `<meta property="og:image" content="${SITE}${assets.og}">\n<meta name="twitter:card" content="summary_large_image">` : '<meta name="twitter:card" content="summary">'}
<meta name="color-scheme" content="dark light">
<meta name="theme-color" media="(prefers-color-scheme: dark)" content="#0c0c0e">
<meta name="theme-color" media="(prefers-color-scheme: light)" content="#f8f8f6">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
${assets.favicon32 ? '<link rel="icon" href="/favicon-32.png" sizes="32x32" type="image/png">\n<link rel="apple-touch-icon" href="/apple-touch-icon.png">' : ""}
${fonts}
<script>${THEME_INIT}</script>
<link rel="stylesheet" href="${assets.css}">
<script src="${assets.js}" defer></script>`;
}

/** The language menu: a native <details> (works without JS; app.js closes it on outside click and Esc). One entry per language
 *  that has docs: the same page when it exists there, else that language's home (build.mjs decides the url). */
export function langMenu({ lang, t, langLinks }) {
  if (!langLinks || langLinks.length < 2) return "";
  const item = (l) => {
    const n = T[l.lang], cur = l.lang === lang;
    const inner = `<span class="lang__name">${esc(n.name)}</span><span class="lang__code">${esc(n.short)}</span><span class="lang__tick">${cur ? icon("check", 14) : ""}</span>`;
    return `<li>${cur ? `<span class="lang__item" lang="${n.htmlLang}" aria-current="true">${inner}</span>` : `<a class="lang__item" href="${l.url}" lang="${n.htmlLang}" hreflang="${n.hreflang}">${inner}</a>`}</li>`;
  };
  return `<details class="lang"><summary class="ib lang__btn" aria-label="${esc(t.language)}">${icon("globe", 18)}<span class="lang__cur" lang="${t.htmlLang}">${esc(t.short)}</span>${icon("chevronDown", 14, "lang__chev")}</summary>
<ul class="lang__panel">${langLinks.map(item).join("")}</ul></details>`;
}

function header({ lang, t, langLinks, stars }) {
  return `<header class="top"><div class="top__in">
<a class="ib menu-btn" href="#nav" data-menu aria-controls="nav" aria-label="${esc(t.menu)}">${icon("menu", 20)}</a>
<a class="brand" href="${pageUrl(lang, "index")}" aria-label="Mistgate — ${esc(t.docs)}"><img src="/mistgate-badge.svg" width="32" height="32" alt="">${wordmark}</a>
<span class="chip tag">${esc(t.docs)}</span>
<span class="grow"></span>
<button class="search-btn js-only" type="button" data-search-open aria-label="${esc(t.search)}" aria-keyshortcuts="Control+K Meta+K /">${icon("search", 16)}<span class="search-btn__t">${esc(t.search)}</span><span class="kbds"><kbd data-mod>Ctrl</kbd><kbd>K</kbd></span></button>
${langMenu({ lang, t, langLinks })}
<button class="ib js-only hide-xs" type="button" data-theme-toggle aria-label="${esc(t.theme)}"><span class="theme-ico theme-ico--sun">${icon("sun", 18)}</span><span class="theme-ico theme-ico--moon">${icon("moon", 18)}</span></button>
${githubButton(t, stars)}
</div></header>`;
}

function sidebar({ lang, t, nav, current }) {
  const link = (url, text, key) => {
    const st = pageStyle(key);
    return `<a class="nav-link" href="${url}"${key === current ? ' aria-current="page"' : ""}><span class="nav-ico"${st.tone ? ` data-tone="${st.tone}"` : ""}>${icon(st.icon, 16)}</span><span>${esc(text)}</span></a>`;
  };
  const groups = nav.map((g) => `<div class="grp"><div class="grp__label">${esc(g.title)}</div>${g.items.map((i) => link(i.url, i.title, i.key)).join("")}</div>`).join("");
  return `<nav class="side" id="nav" aria-label="${esc(t.navLabel)}">
<div class="side__head"><a class="brand" href="${pageUrl(lang, "index")}" aria-label="Mistgate — ${esc(t.docs)}"><img src="/mistgate-badge.svg" width="28" height="28" alt="">${wordmark}</a><a class="ib" href="#content" data-menu-close aria-label="${esc(t.closeMenu)}">${icon("x", 18)}</a></div>
<div class="side__scroll"><div class="grp">${link(pageUrl(lang, "index"), t.home, "index")}</div>${groups}
<div class="side__tools"><button class="ib js-only" type="button" data-theme-toggle aria-label="${esc(t.theme)}"><span class="theme-ico theme-ico--sun">${icon("sun", 18)}</span><span class="theme-ico theme-ico--moon">${icon("moon", 18)}</span></button><a class="ib" href="${GITHUB}" aria-label="${esc(t.github)}" rel="noopener">${GH}</a></div></div>
</nav><a class="backdrop" href="#content" data-menu-close tabindex="-1" aria-hidden="true"></a>`;
}

function tocHtml(items) {
  return `<ul>${items.map((h) => `<li class="l${h.level}"><a href="#${esc(h.id)}">${esc(h.title)}</a></li>`).join("")}</ul>`;
}

function footer({ lang, t }) {
  return `<footer class="foot"><div class="foot__in"><div class="foot__brand"><img src="/mistgate-badge.svg" width="24" height="24" alt="">${wordmark}<span class="muted">· ${esc(t.docs.toLowerCase())}</span></div>
<div class="foot__links"><a href="${GITHUB}/blob/main/LICENSE" rel="noopener">${esc(t.license)}</a><span aria-hidden="true">·</span><a href="${GITHUB}" rel="noopener">GitHub</a></div></div></footer>`;
}

function searchDialog(t, lang) {
  return `<dialog class="search" id="search" aria-label="${esc(t.searchLabel)}" data-index="${pageUrl(lang, "search-index.json").replace(/\/$/, "")}">
<div class="search__field">${icon("search", 18)}<input type="search" id="search-q" placeholder="${esc(t.search)}" aria-label="${esc(t.searchLabel)}" autocomplete="off" autocapitalize="off" spellcheck="false" enterkeyhint="go" role="combobox" aria-expanded="false" aria-controls="search-list"><button type="button" class="ib" data-search-close aria-label="${esc(t.searchClose)}">${icon("x", 18)}</button></div>
<ul class="search__list" id="search-list" role="listbox" aria-label="${esc(t.search)}"></ul>
<p class="search__empty" hidden>${esc(t.searchEmpty)}</p>
</dialog>`;
}

const bodyData = (t) => esc(JSON.stringify({ copy: t.copy, copied: t.copied, inn: t.searchIn }));

export function renderDoc(ctx) {
  const { lang, t, page, nav, assets, langLinks, alternates, prev, next, crumbGroup, stars } = ctx;
  const isHome = page.key === "index";
  const st = pageStyle(page.key);
  const toc = page.headings.filter((h) => h.level <= 3 && h.toc !== false);
  const showToc = !isHome && toc.length >= 2;
  const home = isHome ? renderLanding({ lang, t, nav, langNames: (langLinks ?? []).map((l) => T[l.lang].name), section: (key) => (NOT_IN_ADMIN.has(key) ? "start" : PAGE_SECTION[key] ?? null) }) : null;
  const crumbs = isHome ? "" : `<nav class="crumbs" aria-label="${esc(t.crumbs)}"><a href="${pageUrl(lang, "index")}">${esc(t.docs)}</a>${crumbGroup ? `<span aria-hidden="true">/</span><span>${esc(crumbGroup)}</span>` : ""}<span aria-hidden="true">/</span><span aria-current="page">${esc(page.title)}</span></nav>`;
  const hero = isHome
    ? home.hero
    : `<header class="doc-head">${crumbs}${st.inAdmin ? `<p class="eyebrow"><span class="id-chip" data-tone="${st.tone}">${icon(st.icon, 13)}</span><span>${esc(t.inAdmin)}${t.inAdminSep ?? ": "}${esc(t.sections[st.section])}</span></p>` : ""}<h1 class="doc-h1">${esc(page.title)}</h1><p class="doc-lead">${esc(page.description)}</p></header>`;
  const inlineToc = showToc ? `<details class="toc-inline"><summary>${esc(t.onPage)}</summary>${tocHtml(toc)}</details>` : "";
  const pn = (x, dir) => x ? `<a class="pn pn--${dir}" href="${x.url}" rel="${dir}"><span class="label">${dir === "prev" ? icon("back", 12) : ""}${esc(t[dir])}${dir === "next" ? icon("chevronRight", 12) : ""}</span><span class="pn__t">${esc(x.title)}</span></a>` : "<span></span>";
  const edit = `<p class="edit"><a href="${editUrl(lang, page.key)}" rel="noopener">${icon("externalLink", 14)}${esc(t.edit)}</a></p>`;
  return `<!doctype html>
<html lang="${t.htmlLang}">
<head>
${head({ lang, t, title: page.title, description: isHome ? home.description : page.description, canonical: page.url, alternates, assets, home: isHome })}
</head>
<body class="mg" data-t="${bodyData(t)}"${st.tone ? ` data-tone="${st.tone}"` : ""}>
<a class="skip" href="#content">${esc(t.skip)}</a>
${header({ lang, t, langLinks, stars })}
<div class="shell">
${sidebar({ lang, t, nav, current: page.key })}
<main class="main" id="content" tabindex="-1">
<article class="doc${isHome ? " doc--home" : ""}">
${hero}
${inlineToc}
${isHome ? `<div class="home">${home.body}</div>` : `<div class="prose">\n${page.html}\n</div>`}
${isHome ? "" : edit}
${isHome ? "" : `<nav class="pns" aria-label="${esc(t.docs)}">${pn(prev, "prev")}${pn(next, "next")}</nav>`}
</article>
</main>
${showToc ? `<aside class="toc" aria-label="${esc(t.onPage)}"><div class="label">${esc(t.onPage)}</div>${tocHtml(toc)}</aside>` : ""}
</div>
${footer({ lang, t })}
${searchDialog(t, lang)}
</body>
</html>
`;
}

/** `others`: the languages besides English that have a home page, each gets its line and button. */
export function render404({ assets, others = [] }) {
  const t = T.en;
  const alternates = [];
  return `<!doctype html>
<html lang="en">
<head>
${head({ lang: "en", t, title: t.notFound, description: t.notFoundText, canonical: "/404.html", alternates, assets, noindex: true })}
</head>
<body class="mg" data-t="${bodyData(t)}">
<a class="skip" href="#content">${esc(t.skip)}</a>
<header class="top"><div class="top__in"><a class="brand" href="/" aria-label="Mistgate"><img src="/mistgate-badge.svg" width="32" height="32" alt="">${wordmark}</a><span class="grow"></span><button class="ib js-only" type="button" data-theme-toggle aria-label="${esc(t.theme)}"><span class="theme-ico theme-ico--sun">${icon("sun", 18)}</span><span class="theme-ico theme-ico--moon">${icon("moon", 18)}</span></button><a class="ib" href="${GITHUB}" aria-label="${esc(t.github)}" rel="noopener">${GH}</a></div></header>
<main class="nf" id="content" tabindex="-1">
<p class="nf__code">404</p>
<h1 class="doc-h1">${esc(t.notFound)}</h1>
<p class="doc-lead">${esc(t.notFoundText)}</p>
${others.map((l) => `<p class="doc-lead" lang="${T[l].htmlLang}">${esc(T[l].notFoundText)}</p>`).join("")}
<div class="hero__cta"><a class="btn btn--primary" href="/">${esc(t.toHome)}${icon("chevronRight", 14)}</a>${others.map((l) => `<a class="btn btn--secondary" href="${pageUrl(l, "index")}" lang="${T[l].htmlLang}">${esc(T[l].toHome)}</a>`).join("")}</div>
</main>
${footer({ lang: "en", t })}
</body>
</html>
`;
}
