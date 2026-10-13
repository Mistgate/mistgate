// The roadmap of roadmap/status: a ```roadmap block of YAML inside the page, so the English and Russian pages stay ordinary
// Markdown pages and the parity test can compare their roadmaps (roadmapShape). The site turns the block into a timeline
// of phases with cards (roadmapTokens, a core rule of markdown.mjs); the wiki and anything else that reads Markdown get
// the same content as plain headings and lists (expandRoadmap).
//
// - status: released | now | next | later | goal     one per phase, in order; the phase's h2 is its title
//   title: Next
//   meta: v0.1.0 – v0.1.36                            optional, a short chip next to the title
//   lead: One paragraph.                              optional, inline Markdown
//   cards:
//     - title: Card title                             an h3 (in search, not in the table of contents)
//       section: nodes                                the admin section it belongs to (its icon and pastel), or
//       icon: globe                                   a panel icon on a neutral chip
//       release: v0.1.36                              optional version chip
//       tag: Preview                                  optional word chip
//       current: true                                 optional, the highlighted card across the whole row
//       text: One paragraph.                          optional, inline Markdown
//       points: [Inline Markdown, ...]                optional bullet list
import YAML from "yaml";
import { icon, NAV } from "./icons.mjs";

// the glyph of each status, on the timeline and in the phase strip; the h2 says the status in words
const MARK = { released: "check", now: "pulse", next: "chevronRight", later: "clock", goal: "lock" };
const FENCE = /^(`{3,})roadmap[ \t]*\n([\s\S]*?)\n\1[ \t]*$/gm;
const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

/** The phases of one block, checked; throws with the reason when the block is not a roadmap. */
export function parseRoadmap(src) {
  let data;
  try { data = YAML.parse(src); } catch (e) { throw new Error(`roadmap block: ${e.message}`); }
  const bad = (m) => { throw new Error(`roadmap block: ${m}`); };
  if (!Array.isArray(data) || !data.length) bad("expected a list of phases");
  for (const p of data) {
    if (!MARK[p?.status]) bad(`phase "${p?.title}": status must be one of ${Object.keys(MARK).join(", ")}`);
    if (!p.title) bad(`a ${p.status} phase has no title`);
    if (!Array.isArray(p.cards) || !p.cards.length) bad(`phase "${p.title}" has no cards`);
    for (const c of p.cards) {
      if (!c?.title) bad(`phase "${p.title}": a card has no title`);
      if (c.section && !NAV[c.section]) bad(`card "${c.title}": unknown admin section "${c.section}"`);
      if (c.section && c.icon) bad(`card "${c.title}": section or icon, not both`);
      if (c.points && !(Array.isArray(c.points) && c.points.every((x) => typeof x === "string"))) bad(`card "${c.title}": points must be a list of strings`);
    }
  }
  return data;
}

/** The roadmap blocks of a page body. */
export const roadmapBlocks = (body) => [...body.replace(/\r\n?/g, "\n").matchAll(FENCE)].map((m) => parseRoadmap(m[2]));

/** What must match between languages: phases, cards, their chips and the pages they link to; the words may differ. */
export function roadmapShape(phases) {
  const links = (c) => [...[c.text ?? "", ...(c.points ?? [])].join("\n").matchAll(/\]\(([^)#\s]+)/g)].map((m) => m[1]).sort();
  return phases.map((p) => ({
    status: p.status, meta: Boolean(p.meta), lead: Boolean(p.lead),
    cards: p.cards.map((c) => ({ section: c.section ?? null, icon: c.icon ?? null, release: c.release ?? null, tag: Boolean(c.tag), current: Boolean(c.current), text: Boolean(c.text), points: c.points?.length ?? 0, links: links(c) })),
  }));
}

/** The block as plain Markdown, for readers that do not run this generator (the GitHub wiki). */
export function roadmapMarkdown(phases) {
  const out = [];
  for (const p of phases) {
    out.push(`## ${p.title}`, "");
    if (p.meta) out.push(`*${p.meta}*`, "");
    if (p.lead) out.push(p.lead, "");
    for (const c of p.cards) {
      out.push(`### ${c.title}`, "");
      const chips = [c.release && `\`${c.release}\``, c.tag && `*${c.tag}*`].filter(Boolean);
      if (chips.length) out.push(chips.join(" · "), "");
      if (c.text) out.push(c.text, "");
      if (c.points?.length) out.push(...c.points.map((x) => `- ${x}`), "");
    }
  }
  return out.join("\n").trim();
}

/** A page body with every roadmap block replaced by its Markdown. */
export const expandRoadmap = (body) => body.replace(/\r\n?/g, "\n").replace(FENCE, (_m, _f, src) => roadmapMarkdown(parseRoadmap(src)));

/** Block tokens for markdown-it, inserted where the fence was (before the inline rule, so titles, text and points are
 *  parsed like any other Markdown: links rewritten, names indexed, headings anchored and listed). */
export function roadmapTokens(phases, { Token, slugify, label }) {
  const out = [];
  const html = (content) => { const x = new Token("html_block", "", 0); x.content = content; x.block = true; out.push(x); };
  const inline = (content) => { const x = new Token("inline", "", 0); x.content = String(content); x.children = []; out.push(x); };
  const block = (type, tag, nesting, cls) => { const x = new Token(type, tag, nesting); x.block = true; if (cls) x.attrSet("class", cls); out.push(x); return x; };
  const heading = (tag, text, attrs = {}) => {
    const h = block("heading_open", tag, 1);
    for (const [k, v] of Object.entries(attrs)) h.attrSet(k, v);
    inline(text);
    block("heading_close", tag, -1);
    return h;
  };
  const para = (text, cls) => { block("paragraph_open", "p", 1, cls); inline(text); block("paragraph_close", "p", -1); };
  const list = (items, cls) => {
    block("bullet_list_open", "ul", 1, cls);
    for (const it of items) {
      block("list_item_open", "li", 1);
      block("paragraph_open", "p", 1).hidden = true;
      inline(it);
      block("paragraph_close", "p", -1).hidden = true;
      block("list_item_close", "li", -1);
    }
    block("bullet_list_close", "ul", -1);
  };
  const ids = phases.map((p) => slugify(p.title));
  const mark = (status, size) => `<span class="rm-mark" data-status="${status}" aria-hidden="true">${icon(MARK[status], size)}</span>`;
  const chip = (c) => c.section
    ? `<span class="id-chip" data-tone="${NAV[c.section].tone}" aria-hidden="true">${icon(`nav-${c.section}`, 14)}</span>`
    : `<span class="id-chip id-chip--plain" aria-hidden="true">${icon(c.icon ?? "list", 14)}</span>`;

  html(`<div class="rm">\n<nav class="rm-nav" aria-label="${esc(label)}"><ol>${phases.map((p, i) => `<li data-status="${p.status}"><a href="#${esc(ids[i])}">${mark(p.status, 12)}<span class="rm-nav__t">${esc(p.title)}</span>${p.meta ? `<span class="rm-nav__m">${esc(p.meta)}</span>` : ""}</a></li>`).join("")}</ol></nav>\n<ol class="rm-phases">\n`);
  phases.forEach((p, i) => {
    html(`<li class="rm-phase" data-status="${p.status}">${mark(p.status, 16)}<div class="rm-phase__head">\n`);
    heading("h2", p.title, { id: ids[i] });
    html(`${p.meta ? `<span class="rm-meta">${esc(p.meta)}</span>` : ""}</div>\n`);
    if (p.lead) para(p.lead, "rm-lead");
    html(`<ul class="rm-cards">\n`);
    for (const c of p.cards) {
      // the chips follow the title and drop below it when the line is full
      html(`<li class="rm-card${c.current ? " rm-card--current" : ""}"><div class="rm-card__head">${chip(c)}<div class="rm-card__t">\n`);
      heading("h3", c.title).meta = { toc: false };
      const chips = [c.release && `<span class="rm-ver">${esc(c.release)}</span>`, c.tag && `<span class="rm-tag">${esc(c.tag)}</span>`].filter(Boolean).join("");
      html(`${chips ? `<span class="rm-chips">${chips}</span>` : ""}</div></div>\n`);
      if (c.text) para(c.text);
      if (c.points?.length) list(c.points, "rm-points");
      html(`</li>\n`);
    }
    html(`</ul>\n</li>\n`);
  });
  html(`</ol>\n</div>\n`);
  return out;
}
