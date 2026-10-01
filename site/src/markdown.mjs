// Markdown -> HTML for one language: anchors, callouts, .md link rewriting, highlighted code, scrollable tables.
import MarkdownIt from "markdown-it";
import anchor from "markdown-it-anchor";
import { createHighlighter } from "shiki";
import { posix } from "node:path";
import { pageUrl } from "./urls.mjs";

const LANGS = ["sh", "yaml", "json", "ini", "toml", "javascript", "typescript", "go", "sql", "diff"];
const ALIAS = { bash: "sh", shell: "sh", shellscript: "sh", zsh: "sh", console: "sh", yml: "yaml", js: "javascript", ts: "typescript", conf: "ini", text: "text", txt: "text", plain: "text", plaintext: "text", "": "text" };

export async function createHl() {
  const hl = await createHighlighter({ themes: ["github-light", "github-dark"], langs: LANGS });
  const known = new Set(LANGS);
  return (code, rawLang, warn) => {
    let lang = ALIAS[rawLang] ?? rawLang;
    if (lang !== "text" && !known.has(lang)) { warn(`unknown code language "${rawLang}", rendered as plain text`); lang = "text"; }
    let html = hl.codeToHtml(code.replace(/\n$/, ""), { lang, themes: { light: "github-light", dark: "github-dark" }, defaultColor: false });
    html = html.replace(/^<pre[^>]*>/, '<pre tabindex="0">').replace(/--shiki-dark:#6A737D/gi, "--shiki-dark:#9aa4af");
    return { html, label: rawLang || "text" };
  };
}

// letters and digits of any script survive, so Russian headings get readable ids
const slugify = (s) => s.trim().toLowerCase().replace(/[^\p{L}\p{N}\s-]/gu, "").replace(/\s+/g, "-") || "section";

const CALLOUTS = { note: ["note", "note"], "важно": ["note", "note"], warning: ["warning", "warning"], "внимание": ["warning", "warning"] };
const ICONS = {
  note: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8h.01"/></svg>',
  warning: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 4l9 16H3L12 4zM12 10v4M12 17h.01"/></svg>',
};
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

/** @returns {(src: string, env: {lang: string, key: string, pages: Map<string, object>, warn: Function, headings: object[]}) => string} */
export function createRenderer(lang, t, highlight) {
  const md = new MarkdownIt({ html: false, linkify: false, typographer: false });

  md.use(anchor, {
    level: 2,
    slugify,
    permalink: anchor.permalink.linkInsideHeader({ placement: "after", symbol: "#", class: "anchor", ariaHidden: false, assistiveText: (title) => `${t.permalink}: ${title}`, visuallyHiddenClass: "sr", space: false }),
    callback: (token, info) => { (md.__env ?? { headings: [] }).headings.push({ level: Number(token.tag.slice(1)), title: info.title, id: info.slug }); },
  });

  md.renderer.rules.fence = (tokens, idx, _o, env) => {
    const tok = tokens[idx];
    const raw = tok.info.trim().split(/\s+/)[0];
    const { html, label } = highlight(tok.content, raw, (m) => env.warn(m));
    return `<div class="code"><div class="code__bar"><span class="code__lang">${esc(label)}</span></div>${html}</div>\n`;
  };
  md.renderer.rules.table_open = () => `<div class="table-wrap" role="region" tabindex="0" aria-label="${esc(t.table)}"><table>\n`;
  md.renderer.rules.table_close = () => "</table></div>\n";

  // blockquotes starting with a bold Note/Warning (en) or Важно/Внимание (ru) become callouts
  md.core.ruler.push("mg_callouts", (state) => {
    const tk = state.tokens;
    for (let i = 0; i < tk.length - 3; i++) {
      if (tk[i].type !== "blockquote_open" || tk[i + 1].type !== "paragraph_open" || tk[i + 2].type !== "inline") continue;
      const ch = tk[i + 2].children;
      while (ch?.[0]?.type === "text" && ch[0].content === "") ch.shift();
      if (ch?.[0]?.type !== "strong_open" || ch[1]?.type !== "text" || ch[2]?.type !== "strong_close") continue;
      const hit = CALLOUTS[ch[1].content.replace(/:$/, "").trim().toLowerCase()];
      if (!hit || !/:$/.test(ch[1].content.trim())) continue;
      const label = ch[1].content.replace(/:$/, "").trim();
      ch.splice(0, 3);
      if (ch[0]?.type === "text") ch[0].content = ch[0].content.replace(/^\s+/, "");
      let depth = 0, close = -1;
      for (let j = i; j < tk.length; j++) { if (tk[j].type === "blockquote_open") depth++; if (tk[j].type === "blockquote_close" && --depth === 0) { close = j; break; } }
      tk[i].tag = "div"; tk[i].attrJoin("class", `callout callout--${hit[0]}`); tk[i].attrSet("role", "note");
      tk[close].tag = "div";
      const head = new state.Token("html_block", "", 0);
      head.content = `<div class="callout__label">${ICONS[hit[1]]}<span>${esc(label)}</span></div><div class="callout__body">\n`;
      const tail = new state.Token("html_block", "", 0);
      tail.content = "</div>\n";
      tk.splice(close, 0, tail);
      tk.splice(i + 1, 0, head);
    }
  });

  // short inline code inside table cells stays on one line (flags, variables), long ones may wrap
  md.core.ruler.push("mg_table_code", (state) => {
    state.tokens.forEach((tok, i) => {
      if (tok.type !== "inline" || !/^t[dh]_open$/.test(state.tokens[i - 1]?.type)) return;
      for (const c of tok.children) if (c.type === "code_inline" && c.content.length <= 34) c.attrSet("class", "nw");
    });
  });

  // .md links -> site URLs; links to pages that do not exist yet become a muted span (and a build warning)
  md.core.ruler.push("mg_links", (state) => {
    const env = state.env;
    for (const blk of state.tokens) {
      if (blk.type !== "inline") continue;
      const stack = [];
      blk.children.forEach((tok, n) => {
        if (tok.type === "link_open") {
          const href = tok.attrGet("href") ?? "";
          const m = /^([^:#?]+)\.md(#.*)?$/.exec(href);
          if (!m || /^[a-z][a-z0-9+.-]*:/i.test(href)) { stack.push(false); return; }
          const dir = posix.dirname(env.key === "index" ? "index" : env.key);
          const target = posix.normalize(posix.join(dir === "." ? "" : dir, m[1]));
          if (env.pages.has(target)) { tok.attrSet("href", pageUrl(lang, target) + (m[2] ?? "")); stack.push(false); return; }
          env.warn(`link to a page that does not exist yet: ${href}`);
          tok.tag = "span"; tok.attrs = [["class", "pending"], ["title", t.pending]];
          stack.push(true);
        } else if (tok.type === "link_close") {
          if (stack.pop()) tok.tag = "span";
        } else if (tok.type === "strong_open" && blk.children[n + 1]?.type === "text" && /^(Planned|Планируется)$/.test(blk.children[n + 1].content)) {
          tok.attrJoin("class", "planned");
        }
      });
    }
  });

  return (src, env) => { md.__env = env; return md.render(src, env); };
}

/** Plain parse used for index.md: returns [{title, links:[href]}] for each H3 with a bullet list below it. */
export function parseNav(src) {
  const md = new MarkdownIt();
  const tk = md.parse(src, {});
  const groups = [];
  let cur = null, inBullets = 0;
  for (let i = 0; i < tk.length; i++) {
    const x = tk[i];
    if (x.type === "heading_open") {
      cur = x.tag === "h3" ? { title: tk[i + 1].content, links: [] } : null;
      if (cur) groups.push(cur);
    } else if (x.type === "bullet_list_open") inBullets++;
    else if (x.type === "bullet_list_close") inBullets--;
    else if (x.type === "inline" && cur && inBullets === 1 && tk[i - 1].type === "paragraph_open") {
      const a = x.children.find((c) => c.type === "link_open");
      if (a) cur.links.push({ href: a.attrGet("href"), text: x.children.filter((c) => c !== a).find((c) => c.type === "text")?.content ?? "" });
    }
  }
  return groups.filter((g) => g.links.length);
}
