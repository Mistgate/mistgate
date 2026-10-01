// HTML templates and UI strings. No dependencies: plain template literals.
import { SITE, GITHUB, pageUrl, editUrl } from "./urls.mjs";

export const T = {
  en: {
    hreflang: "en", name: "English", short: "EN", docs: "Docs", home: "Home", siteTitle: "Mistgate docs",
    skip: "Skip to content", menu: "Menu", closeMenu: "Close menu", navLabel: "Documentation", crumbs: "Breadcrumb",
    search: "Search docs", searchLabel: "Search the documentation", searchEmpty: "Nothing found. Try another word.", searchClose: "Close search", searchIn: "in",
    onPage: "On this page", prev: "Previous", next: "Next", edit: "Edit this page on GitHub", theme: "Switch theme", github: "Mistgate on GitHub",
    language: "Language", copy: "Copy", copied: "Copied", table: "Table", permalink: "Link to the section", pending: "This page is not written yet",
    license: "Released under AGPL-3.0", start: "Start reading", docLabel: "Documentation", other: "Other",
    notFound: "Page not found", notFoundText: "There is no such page. It may have moved or not been written yet.", toHome: "Go to the documentation",
  },
  ru: {
    hreflang: "ru", name: "Русский", short: "RU", docs: "Документация", home: "Главная", siteTitle: "Документация Mistgate",
    skip: "К содержимому", menu: "Меню", closeMenu: "Закрыть меню", navLabel: "Документация", crumbs: "Навигация по разделам",
    search: "Поиск по документации", searchLabel: "Поиск по документации", searchEmpty: "Ничего не нашлось. Попробуйте другое слово.", searchClose: "Закрыть поиск", searchIn: "в",
    onPage: "На этой странице", prev: "Назад", next: "Дальше", edit: "Править страницу на GitHub", theme: "Сменить тему", github: "Mistgate на GitHub",
    language: "Язык", copy: "Копировать", copied: "Скопировано", table: "Таблица", permalink: "Ссылка на раздел", pending: "Эта страница ещё не написана",
    license: "Лицензия AGPL-3.0", start: "Начать читать", docLabel: "Документация", other: "Прочее",
    notFound: "Страница не найдена", notFoundText: "Такой страницы нет. Возможно, она переехала или ещё не написана.", toHome: "К документации",
  },
};

const esc = (s) => String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const P = {
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14ZM20 20l-4-4",
  menu: "M4 7h16M4 12h16M4 17h16",
  x: "M6 6l12 12M18 6L6 18",
  sun: "M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4",
  moon: "M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z",
  copy: "M9 9h10v11H9zM5 15V4h10",
  check: "M5 12.5l4.5 4.5L19 7.5",
  ext: "M14 4h6v6M20 4l-9 9M18 14v5H5V6h5",
  chevR: "M9 5l7 7-7 7",
  chevL: "M15 5l-7 7 7 7",
  bolt: "M13 3L5 14h6l-1 7 8-11h-6l1-7z",
  layers: "M12 3l9 5-9 5-9-5 9-5zM3 13l9 5 9-5",
  pulse: "M3 12h4l3-7 4 14 3-7h4",
  text: "M4 6h16M4 10h16M4 14h10M4 18h7",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8h.01",
  tag: "M3 12V4h8l10 10-8 8L3 12zM7.5 8h.01",
  code: "M8 8l-5 4 5 4M16 8l5 4-5 4M14 5l-4 14",
};
export const icon = (name, size = 16, cls = "") =>
  `<svg class="ico ${cls}" viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${P[name]}"/></svg>`;
const GH = '<svg class="ico" viewBox="0 0 16 16" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>';

// Runs before the stylesheet: restores the saved theme (none saved = follow the system), marks JS as available.
export const THEME_INIT = `try{var t=localStorage.getItem("mg-theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t}catch(e){}document.documentElement.classList.add("js")`;

const GROUP_STYLE = { "getting-started": ["bolt", "lavender"], guide: ["layers", "sky"], operations: ["pulse", "rose"], reference: ["text", "sand"] };
const FALLBACK_STYLE = [["tag", "sage"], ["code", "mint"]];
export function groupStyle(dir, n) { return GROUP_STYLE[dir] ?? FALLBACK_STYLE[n % FALLBACK_STYLE.length]; }

const wordmark = '<span class="wordmark">mist<span>gate</span></span>';

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
<meta property="og:locale" content="${lang === "ru" ? "ru_RU" : "en_US"}">
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

function header({ lang, t, switchUrl }) {
  const other = lang === "en" ? "ru" : "en";
  const lg = (l) => `<a href="${l === lang ? "#" : switchUrl}" lang="${l}" hreflang="${l}" ${l === lang ? 'aria-current="true"' : `title="${esc(T[l].name)}"`}>${T[l].short}</a>`;
  return `<header class="top"><div class="top__in">
<a class="ib menu-btn" href="#nav" data-menu aria-controls="nav" aria-label="${esc(t.menu)}">${icon("menu", 20)}</a>
<a class="brand" href="${pageUrl(lang, "index")}" aria-label="Mistgate — ${esc(t.docs)}"><img src="/mistgate-badge.svg" width="32" height="32" alt="">${wordmark}</a>
<span class="chip tag">${esc(t.docs)}</span>
<span class="grow"></span>
<button class="search-btn js-only" type="button" data-search-open aria-label="${esc(t.search)}" aria-keyshortcuts="/">${icon("search", 16)}<span class="search-btn__t">${esc(t.search)}</span><kbd>/</kbd></button>
<div class="lang" role="group" aria-label="${esc(t.language)}">${lg(lang)}${lg(other)}</div>
<button class="ib js-only hide-xs" type="button" data-theme-toggle aria-label="${esc(t.theme)}"><span class="theme-ico theme-ico--sun">${icon("sun", 18)}</span><span class="theme-ico theme-ico--moon">${icon("moon", 18)}</span></button>
<a class="ib hide-s" href="${GITHUB}" aria-label="${esc(t.github)}" rel="noopener">${GH}</a>
</div></header>`;
}

function sidebar({ lang, t, nav, current }) {
  const link = (url, text, key) => `<a class="nav-link" href="${url}"${key === current ? ' aria-current="page"' : ""}>${esc(text)}</a>`;
  const groups = nav.map((g) => `<div class="grp"><div class="grp__label" data-tone="${g.tone}"><span class="chip-id">${icon(g.icon, 13)}</span><span>${esc(g.title)}</span></div>${g.items.map((i) => link(i.url, i.title, i.key)).join("")}</div>`).join("");
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
  const { lang, t, page, nav, assets, switchUrl, alternates, prev, next, crumbGroup } = ctx;
  const isHome = page.key === "index";
  const toc = page.headings.filter((h) => h.level <= 3);
  const showToc = toc.length >= 2;
  const lead = isHome ? page.lead : null;
  const body = isHome ? page.html.replace(/^<p>[\s\S]*?<\/p>\n/, "") : page.html;
  const crumbs = isHome ? "" : `<nav class="crumbs" aria-label="${esc(t.crumbs)}"><a href="${pageUrl(lang, "index")}">${esc(t.docs)}</a>${crumbGroup ? `<span aria-hidden="true">/</span><span>${esc(crumbGroup)}</span>` : ""}<span aria-hidden="true">/</span><span aria-current="page">${esc(page.title)}</span></nav>`;
  const hero = isHome
    ? `<section class="hero">
<svg class="hero__arch" viewBox="0 0 300 260" fill="none" aria-hidden="true"><path d="M30 260V150a120 120 0 0 1 240 0v110" style="stroke:var(--surface-2)" stroke-width="1.5"/><path d="M70 260V160a80 80 0 0 1 160 0v100" style="stroke:var(--border)" stroke-width="1.5"/><path d="M110 260V170a40 40 0 0 1 80 0v90" style="stroke:var(--accent-line)" stroke-width="1.5"/><path d="M0 214c40-14 80 14 120 0s80-14 120 0 40 10 60 4" style="stroke:var(--border)" stroke-width="1.5" stroke-linecap="round"/><path d="M24 238c40-12 70 12 110 0s80-12 120 0" style="stroke:var(--surface-2)" stroke-width="1.5" stroke-linecap="round"/></svg>
<div class="hero__id"><img src="/mistgate-badge.svg" width="72" height="72" alt=""><div><div class="label">${esc(t.docLabel)}</div><h1 class="hero__h1"><span aria-hidden="true">${wordmark}</span><span class="sr">${esc(page.title)}</span></h1></div></div>
<p class="hero__lead">${lead ?? esc(page.description)}</p>
<div class="hero__cta">${next ? `<a class="btn btn--primary" href="${next.url}">${esc(t.start)}${icon("chevR", 14)}</a>` : ""}<a class="btn btn--secondary" href="${GITHUB}" rel="noopener">GitHub</a></div>
</section>`
    : `<header class="doc-head">${crumbs}<h1 class="doc-h1">${esc(page.title)}</h1><p class="doc-lead">${esc(page.description)}</p></header>`;
  const inlineToc = showToc ? `<details class="toc-inline"><summary>${esc(t.onPage)}</summary>${tocHtml(toc)}</details>` : "";
  const pn = (x, dir) => x ? `<a class="pn pn--${dir}" href="${x.url}" rel="${dir}"><span class="label">${dir === "prev" ? icon("chevL", 12) : ""}${esc(t[dir])}${dir === "next" ? icon("chevR", 12) : ""}</span><span class="pn__t">${esc(x.title)}</span></a>` : "<span></span>";
  const edit = `<p class="edit"><a href="${editUrl(lang, page.key)}" rel="noopener">${icon("ext", 14)}${esc(t.edit)}</a></p>`;
  return `<!doctype html>
<html lang="${lang}">
<head>
${head({ lang, t, title: page.title, description: page.description, canonical: page.url, alternates, assets, home: isHome })}
</head>
<body class="mg" data-t="${bodyData(t)}">
<a class="skip" href="#content">${esc(t.skip)}</a>
${header({ lang, t, switchUrl })}
<div class="shell">
${sidebar({ lang, t, nav, current: page.key })}
<main class="main" id="content" tabindex="-1">
<article class="doc${isHome ? " doc--home" : ""}">
${hero}
${inlineToc}
<div class="prose">
${body}
</div>
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

export function render404({ assets }) {
  const t = T.en, r = T.ru;
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
<p class="doc-lead" lang="ru">${esc(r.notFoundText)}</p>
<div class="hero__cta"><a class="btn btn--primary" href="/">${esc(t.toHome)}${icon("chevR", 14)}</a><a class="btn btn--secondary" href="/ru/" lang="ru">${esc(r.toHome)}</a></div>
</main>
${footer({ lang: "en", t })}
</body>
</html>
`;
}
