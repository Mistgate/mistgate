// Markdown -> HTML for one language: anchors, callouts, .md link rewriting, highlighted code, scrollable tables.
import MarkdownIt from "markdown-it";
import anchor from "markdown-it-anchor";
import { createHighlighter } from "shiki";
import { posix, join } from "node:path";
import { pageUrl } from "./urls.mjs";
import { icon } from "./icons.mjs";
import { inlineSvg, mermaidSvg } from "./diagrams.mjs";

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
    return { html, lang };
  };
}

// letters and digits of any script survive, so Russian headings get readable ids
const slugify = (s) => s.trim().toLowerCase().replace(/[^\p{L}\p{N}\s-]/gu, "").replace(/\s+/g, "-") || "section";

// "> **Note:** ..." and friends; GitHub shows the same blockquote, the wiki turns it into an alert (wiki.mjs ALERTS)
const CALLOUTS = { note: "note", "важно": "note", tip: "tip", "совет": "tip", warning: "warning", "внимание": "warning", danger: "danger", "опасно": "danger" };
const CALLOUT_CHIP = { note: ["info", "lavender"], tip: ["sparkle", "mint"], warning: ["warn", "sand"], danger: ["warn", "rose"] };
// code block bar: language name and icon; a word after the language (```ini /etc/x.service) names the file instead
const CODE_NAME = { sh: "Shell", yaml: "YAML", json: "JSON", ini: "INI", toml: "TOML", javascript: "JavaScript", typescript: "TypeScript", go: "Go", sql: "SQL", diff: "Diff" };
// a table cell that is a yes / no / partial answer becomes a pill (colour + glyph + word, like the panel's status)
const ANSWER = /^(yes|no|partial|да|нет|частично)(?=$|:|\s\()/iu;
const ANSWER_KIND = { yes: "yes", "да": "yes", no: "no", "нет": "no", partial: "part", "частично": "part" };
// a name worth finding: starts with a letter, a dash or an underscore, no placeholders or prose
const SEARCHABLE = /^(?=.{3,60}$)(?=.*[A-Za-z])[A-Za-z_-][\w.:=/@-]*(?: [\w.:=/@-]+){0,3}$/;
const NOISE = new Set(["true", "false", "yes", "no", "on", "off", "http", "https", "localhost"]);
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
    const [raw = "", ...name] = tok.info.trim().split(/\s+/);
    if (raw === "mermaid") return `<figure class="diagram diagram--mermaid" tabindex="0">${mermaidSvg(tok.content, t.diagram, (m) => env.warn(m))}</figure>\n`;
    const { html, lang } = highlight(tok.content, raw, (m) => env.warn(m));
    const label = name.length ? name.join(" ") : CODE_NAME[lang] ?? t.plain;
    const ico = lang === "sh" ? "terminal" : lang === "text" ? "text" : "code";
    const kind = name.length && CODE_NAME[lang] ? `<span class="code__kind">${CODE_NAME[lang]}</span>` : "";
    return `<div class="code"><div class="code__bar"><span class="code__lang${name.length ? " code__lang--file" : ""}">${icon(ico, 14)}<span>${esc(label)}</span>${kind}</span></div>${html}</div>\n`;
  };
  md.renderer.rules.table_open = (tokens, idx, o, _env, self) => `<div class="table-wrap" role="region" tabindex="0" aria-label="${esc(t.table)}">${self.renderToken(tokens, idx, o)}`;
  md.renderer.rules.table_close = () => "</table></div>\n";

  // ![alt](../diagrams/x.svg) on a line of its own: the SVG is inlined (see diagrams.mjs), alt becomes its name
  const image = md.renderer.rules.image;
  md.renderer.rules.image = (tokens, idx, o, env, self) => {
    const tok = tokens[idx], src = tok.attrGet("src") ?? "";
    if (!src.endsWith(".svg") || /^([a-z][a-z0-9+.-]*:|\/)/i.test(src)) return image(tokens, idx, o, env, self);
    const alt = self.renderInlineAsText(tok.children, o, env);
    if (!alt) env.warn(`diagram ${src} has no alt text`);
    return `<figure class="diagram diagram--svg" tabindex="0">${inlineSvg(join(env.dir, src), alt || t.diagram)}</figure>`;
  };
  md.core.ruler.push("mg_figures", (state) => {
    state.tokens.forEach((tok, i) => {
      const ch = tok.type === "inline" && tok.children?.filter((c) => !(c.type === "text" && !c.content.trim()));
      if (ch?.length === 1 && ch[0].type === "image" && /\.svg$/.test(ch[0].attrGet("src")) && state.tokens[i - 1].type === "paragraph_open") {
        state.tokens[i - 1].hidden = state.tokens[i + 1].hidden = true;
      }
    });
  });

  // blockquotes starting with a bold Note/Warning (en) or Важно/Внимание (ru) become callouts
  md.core.ruler.push("mg_callouts", (state) => {
    const tk = state.tokens;
    for (let i = 0; i < tk.length - 3; i++) {
      if (tk[i].type !== "blockquote_open" || tk[i + 1].type !== "paragraph_open" || tk[i + 2].type !== "inline") continue;
      const ch = tk[i + 2].children;
      while (ch?.[0]?.type === "text" && ch[0].content === "") ch.shift();
      if (ch?.[0]?.type !== "strong_open" || ch[1]?.type !== "text" || ch[2]?.type !== "strong_close") continue;
      const kind = CALLOUTS[ch[1].content.replace(/:$/, "").trim().toLowerCase()];
      if (!kind || !/:$/.test(ch[1].content.trim())) continue;
      const label = ch[1].content.replace(/:$/, "").trim();
      ch.splice(0, 3);
      if (ch[0]?.type === "text") ch[0].content = ch[0].content.replace(/^\s+/, "");
      let depth = 0, close = -1;
      for (let j = i; j < tk.length; j++) { if (tk[j].type === "blockquote_open") depth++; if (tk[j].type === "blockquote_close" && --depth === 0) { close = j; break; } }
      tk[i].tag = "div"; tk[i].attrJoin("class", `callout callout--${kind}`); tk[i].attrSet("role", "note");
      tk[close].tag = "div";
      const head = new state.Token("html_block", "", 0);
      head.content = `<div class="callout__label"><span class="id-chip" data-tone="${CALLOUT_CHIP[kind][1]}">${icon(CALLOUT_CHIP[kind][0], 13)}</span><span>${esc(label)}</span></div><div class="callout__body">\n`;
      const tail = new state.Token("html_block", "", 0);
      tail.content = "</div>\n";
      tk.splice(close, 0, tail);
      tk.splice(i + 1, 0, head);
    }
  });

  // tables: each cell carries its column name (cards below 720 px read it), roles keep the table a table for screen
  // readers when CSS turns rows into cards. In a column of answers (at least half of its cells are yes / no / partial
  // or "?") the answers become pills. English decides which columns those are (env.answers collects them) and the
  // translation follows (env.answersFrom): in Russian "нет" also means "none", which is not an answer.
  md.core.ruler.push("mg_tables", (state) => {
    const tk = state.tokens;
    let heads = [], cols = [], col = 0, table = null, n = -1;
    const out = (state.env.answers = []);
    tk.forEach((tok, i) => {
      if (tok.type === "table_open") { table = tok; heads = []; cols = []; n++; tok.attrSet("role", "table"); }
      else if (tok.type === "thead_open" || tok.type === "tbody_open") tok.attrSet("role", "rowgroup");
      else if (tok.type === "tr_open") { col = 0; tok.attrSet("role", "row"); }
      else if (tok.type === "th_open") { tok.attrSet("role", "columnheader"); heads.push(tk[i + 1].content.replace(/[`*_]/g, "").trim()); }
      else if (tok.type === "thead_close") table.attrSet("class", `cols-${heads.length}`);
      else if (tok.type === "td_open") {
        tok.attrSet("role", "cell");
        if (heads[col]) tok.attrSet("data-label", heads[col]);
        if (tk[i + 1].content.trim()) (cols[col] ??= []).push(tk[i + 1]);
        col++;
      } else if (tok.type === "table_close") {
        const from = state.env.answersFrom?.[n];
        out[n] = [];
        cols.forEach((cells, k) => {
          const hits = cells.map((c) => c.children[0]?.type === "text" && ANSWER.exec(c.children[0].content));
          const yes = from ? from.includes(k) : k > 0 && cells.filter((c, j) => hits[j] || c.content.trim() === "?").length * 2 >= cells.length;
          if (!yes) return;
          out[n].push(k);
          cells.forEach((c, j) => {
            const m = hits[j];
            if (!m) return;
            const pill = new state.Token("html_inline", "", 0);
            pill.content = `<span class="yn yn--${ANSWER_KIND[m[1].toLowerCase()]}"><span class="yn__g" aria-hidden="true"></span>${esc(m[1])}</span>`;
            c.children[0].content = c.children[0].content.slice(m[1].length).replace(/^:\s*/, " ");
            c.children.unshift(pill);
          });
        });
      }
    });
  });

  // short inline code inside table cells stays on one line (flags, variables), long ones may wrap
  md.core.ruler.push("mg_table_code", (state) => {
    state.tokens.forEach((tok, i) => {
      if (tok.type !== "inline" || !/^t[dh]_open$/.test(state.tokens[i - 1]?.type)) return;
      for (const c of tok.children) if (c.type === "code_inline" && c.content.length <= 34) c.attrSet("class", "nw");
    });
  });

  // names in inline code (flags, MISTGATE_* variables, MCP tools, commands) go to the search index with the section
  // they appear in; a table row's first cell or a heading is where a name is defined, so it wins over a mention
  md.core.ruler.push("mg_codes", (state) => {
    const codes = state.env.codes;
    if (!codes) return;
    let id = "";
    state.tokens.forEach((tok, i) => {
      if (tok.type === "heading_open") { id = tok.attrGet("id") ?? id; return; }
      if (tok.type !== "inline") return;
      const def = state.tokens[i - 1].type === "heading_open" || (state.tokens[i - 1].type === "td_open" && state.tokens[i - 2]?.type === "tr_open");
      for (const c of tok.children ?? []) {
        if (c.type !== "code_inline" || !SEARCHABLE.test(c.content) || NOISE.has(c.content.toLowerCase())) continue;
        const had = codes.get(c.content);
        if (!had || (def && !had.def)) codes.set(c.content, { id, def });
      }
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
