import { Link } from "@tanstack/react-router";
import { Fragment, useMemo, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { cx } from "@/lib/cx";
import { parseInline, safeHttpsUrl, type Inline } from "@/components/mini-markdown";

// The assistant's answers (ADR 0014): Markdown from a model that read untrusted data, so it is rendered like the release
// notes (components/mini-markdown): React elements built from strings, no raw HTML, no images (an image URL would send data
// out by itself). On top of the notes' subset: fenced code blocks, headings by level, numbered and nested lists, tables,
// quotes and rules, and links to the admin's own pages ("/nodes/nod_…") as router links. Other links only when they parse
// as https, shown with their host and opened apart.

type Item = { text: Inline[]; children: Inline[][] };

type Block =
  | { kind: "code"; text: string }
  | { kind: "heading"; level: 1 | 2 | 3; children: Inline[] }
  | { kind: "paragraph"; children: Inline[] }
  | { kind: "list"; ordered: boolean; items: Item[] }
  | { kind: "table"; head: Inline[][]; rows: Inline[][][]; align: ("left" | "right" | "center")[] }
  | { kind: "quote"; children: Inline[] }
  | { kind: "rule" };

/**
 * Something the panel knows by name, drawn as a chip that opens it when an answer names it: a node (its status as a dot).
 * Matched on the exact name as a whole word, never inside code or a link. A name that is also a common English word ("be",
 * "mass") is matched only where the answer set it in bold, as models do for names.
 */
export type ChatEntity = { name: string; id: string; dot: "ok" | "warn" | "bad" | "off" };

const commonWords = new Set(["a", "an", "as", "at", "be", "by", "do", "go", "he", "if", "in", "is", "it", "me", "my", "no", "of", "on", "or", "so", "to", "up", "us", "we", "all", "and", "any", "are", "but", "can", "for", "has", "new", "not", "now", "one", "out", "the", "two", "was", "way", "edge", "home", "main", "mass", "test", "free", "fast", "next", "last"]);

const dotCls = { ok: "bg-ok", warn: "bg-warn", bad: "bg-danger", off: "bg-faint" } as const;

function entityPattern(list: readonly ChatEntity[]): RegExp | null {
  const names = [...new Set(list.map((e) => e.name).filter((n) => n.trim().length > 0))].sort((a, b) => b.length - a.length);
  if (names.length === 0) return null;
  const alt = names.map((n) => n.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|");
  return new RegExp(`(?<![\\p{L}\\p{N}_-])(${alt})(?![\\p{L}\\p{N}_-])`, "gu");
}

/** A path of the admin itself: "/" then letters, digits and the usual id characters, no "//", no scheme. */
export const internalPath = (href: string) => /^\/(?!\/)[\w.~!$&'()*+,;=:@%/?#-]*$/.test(href) && !href.includes("//");

/** A link of an answer: an admin page as it is, an https address normalised, anything else dropped (its text stays). */
const chatHref = (raw: string) => (internalPath(raw) ? raw : safeHttpsUrl(raw));
const inlineOf = (src: string) => parseInline(src, 0, chatHref);

/** The cells of a table row: "| a | b |" or "a | b"; a pipe inside `code` stays in its cell. */
function cells(line: string): string[] {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
  const out: string[] = [];
  let cur = "";
  let code = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i]!;
    if (c === "`") code = !code;
    if (c === "\\" && s[i + 1] === "|") {
      cur += "|";
      i++;
    } else if (c === "|" && !code) {
      out.push(cur.trim());
      cur = "";
    } else cur += c;
  }
  out.push(cur.trim());
  return out;
}

const isTableRow = (line: string) => /^\s*\|?.*\|.*$/.test(line) && line.includes("|");
const separator = (line: string) => {
  if (!/^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(line)) return null;
  return cells(line).map((c) => (c.startsWith(":") && c.endsWith(":") ? "center" : c.endsWith(":") ? "right" : "left") as "left" | "right" | "center");
};

export function parseChatMarkdown(src: string): Block[] {
  const blocks: Block[] = [];
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  let para: string[] = [];
  let quote: string[] = [];
  let list: { ordered: boolean; items: { text: string; children: string[] }[] } | null = null;
  const endPara = () => {
    if (para.length) blocks.push({ kind: "paragraph", children: inlineOf(para.join(" ")) });
    para = [];
  };
  const endQuote = () => {
    if (quote.length) blocks.push({ kind: "quote", children: inlineOf(quote.join(" ")) });
    quote = [];
  };
  const endList = () => {
    if (list) blocks.push({ kind: "list", ordered: list.ordered, items: list.items.map((i) => ({ text: inlineOf(i.text), children: i.children.map(inlineOf) })) });
    list = null;
  };
  const endAll = () => {
    endPara();
    endQuote();
    endList();
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    if (/^\s*```/.test(line)) {
      endAll();
      const body: string[] = [];
      for (i++; i < lines.length && !/^\s*```/.test(lines[i]!); i++) body.push(lines[i]!);
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }
    // a table: a row of cells, then a separator row of dashes
    const align = i + 1 < lines.length && isTableRow(line) ? separator(lines[i + 1]!) : null;
    if (align) {
      endAll();
      const head = cells(line);
      const rows: string[][] = [];
      for (i += 2; i < lines.length && lines[i]!.trim() !== "" && isTableRow(lines[i]!); i++) rows.push(cells(lines[i]!));
      i--;
      const width = Math.max(head.length, ...rows.map((r) => r.length));
      const pad = (r: string[]) => [...r, ...Array<string>(width - r.length).fill("")].slice(0, width);
      blocks.push({ kind: "table", head: pad(head).map(inlineOf), rows: rows.map((r) => pad(r).map(inlineOf)), align: pad(align as string[]).map((a) => (a || "left") as "left") });
      continue;
    }
    const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    const bullet = /^(\s*)[-*+]\s+(.*)$/.exec(line);
    const numbered = /^(\s*)\d{1,3}[.)]\s+(.*)$/.exec(line);
    const quoted = /^\s*>\s?(.*)$/.exec(line);
    if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      endAll();
      blocks.push({ kind: "rule" });
    } else if (heading) {
      endAll();
      blocks.push({ kind: "heading", level: Math.min(3, Math.max(1, heading[1]!.length - 1)) as 1 | 2 | 3, children: inlineOf(heading[2]!) });
    } else if (quoted) {
      endPara();
      endList();
      quote.push(quoted[1]!.trim());
    } else if (bullet || numbered) {
      endPara();
      endQuote();
      const m = (bullet ?? numbered)!;
      const nested = m[1]!.length >= 2 && list !== null && list.items.length > 0;
      if (nested) {
        list!.items[list!.items.length - 1]!.children.push(m[2]!);
        continue;
      }
      const ordered = !!numbered;
      if (list && list.ordered !== ordered) endList();
      list ??= { ordered, items: [] };
      list.items.push({ text: m[2]!, children: [] });
    } else if (line.trim() === "") {
      endAll();
    } else if (list && /^\s{2,}\S/.test(line)) {
      const last = list.items[list.items.length - 1]!;
      if (last.children.length) last.children[last.children.length - 1] += " " + line.trim();
      else last.text += " " + line.trim();
    } else {
      endList();
      endQuote();
      para.push(line.trim());
    }
  }
  endAll();
  return blocks;
}

type Ctx = { onNavigate?: () => void; entities?: readonly ChatEntity[]; pattern?: RegExp | null; bold?: boolean };

function EntityChip({ e, label, onNavigate }: { e: ChatEntity; label: string; onNavigate?: () => void }) {
  return (
    <Link
      to="/nodes/$id"
      params={{ id: e.id }}
      onClick={onNavigate}
      data-entity="node"
      className="mx-px inline-flex items-baseline gap-1 rounded-md bg-fg/[0.07] px-1.5 py-px align-baseline font-semibold text-fg no-underline transition-colors hover:bg-accent-soft hover:text-accent-text"
    >
      <span aria-hidden className={cx("size-1.5 shrink-0 -translate-y-px self-center rounded-full", dotCls[e.dot])} />
      {label}
    </Link>
  );
}

/** Plain text with the names of known things drawn as chips. */
function linkify(text: string, ctx: Ctx, keyBase: string): ReactNode[] {
  const { pattern, entities } = ctx;
  if (!pattern || !entities?.length) return [text];
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(pattern)) {
    const name = m[1]!;
    const e = entities.find((x) => x.name === name);
    if (!e || (commonWords.has(name.toLowerCase()) && !ctx.bold)) continue;
    if (m.index! > last) out.push(text.slice(last, m.index));
    out.push(<EntityChip key={`${keyBase}-${m.index}`} e={e} label={name} onNavigate={ctx.onNavigate} />);
    last = m.index! + name.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function inline(nodes: Inline[], ctx: Ctx): ReactNode[] {
  const onNavigate = ctx.onNavigate;
  return nodes.map((n, i) => {
    switch (n.kind) {
      case "text":
        return <Fragment key={i}>{linkify(n.text, ctx, String(i))}</Fragment>;
      case "bold":
        return (
          <strong key={i} className="font-semibold text-fg">
            {inline(n.children, { ...ctx, bold: true })}
          </strong>
        );
      case "em":
        return <em key={i}>{inline(n.children, ctx)}</em>;
      case "code":
        return (
          <code key={i} className="rounded-md bg-fg/[0.07] px-1.5 py-0.5 font-mono text-[0.84em] font-normal whitespace-nowrap">
            {n.text}
          </code>
        );
      case "link": {
        if (internalPath(n.href)) {
          return (
            <Link key={i} to={n.href} onClick={onNavigate} data-internal="" className="font-semibold text-accent-text underline decoration-accent-line underline-offset-2">
              {n.text}
            </Link>
          );
        }
        const href = safeHttpsUrl(n.href);
        if (!href) return n.text;
        return (
          <a key={i} href={href} target="_blank" rel="noopener noreferrer" title={href} className="text-accent-text underline decoration-accent-line underline-offset-2">
            {n.text}
            <Icon name="externalLink" size={11} className="ms-0.5 inline-block align-[-1px]" />
          </a>
        );
      }
    }
  });
}

const alignCls = { left: "text-start", right: "text-end", center: "text-center" } as const;

/**
 * An answer of the assistant. onNavigate: a page link was followed (the chat may close on a phone). entities: the things the
 * panel knows by name (nodes), drawn as chips that open them.
 */
export function ChatMarkdown({ text, onNavigate, entities }: { text: string; onNavigate?: () => void; entities?: readonly ChatEntity[] }) {
  const pattern = useMemo(() => entityPattern(entities ?? []), [entities]);
  const ctx: Ctx = { onNavigate, entities, pattern };
  const inline_ = (c: Inline[]) => inline(c, ctx);
  return (
    <div className="flex flex-col gap-3 text-[15px] leading-[1.65] font-normal text-pretty break-words">
      {parseChatMarkdown(text).map((b, i) => {
        switch (b.kind) {
          case "code":
            return (
              <pre key={i} className="overflow-x-auto rounded-xl bg-fg/[0.06] px-3.5 py-3 font-mono text-[12.5px] leading-normal">
                <code>{b.text}</code>
              </pre>
            );
          case "heading":
            return b.level === 1 ? (
              <h3 key={i} className="mt-1.5 text-[16.5px] leading-snug font-bold tracking-[-0.01em] first:mt-0">
                {inline_(b.children)}
              </h3>
            ) : b.level === 2 ? (
              <h4 key={i} className="mt-1 text-[15.5px] leading-snug font-bold first:mt-0">
                {inline_(b.children)}
              </h4>
            ) : (
              <h5 key={i} className="mt-0.5 text-[13px] leading-snug font-bold tracking-[0.02em] text-muted uppercase first:mt-0">
                {inline_(b.children)}
              </h5>
            );
          case "paragraph":
            return <p key={i}>{inline_(b.children)}</p>;
          case "quote":
            return (
              <blockquote key={i} className="border-s-2 border-line ps-3 text-muted">
                {inline_(b.children)}
              </blockquote>
            );
          case "rule":
            return <hr key={i} className="my-1 border-line" />;
          case "list": {
            const Tag = b.ordered ? "ol" : "ul";
            return (
              <Tag key={i} className={cx("flex flex-col gap-1.5 ps-5 marker:text-muted", b.ordered ? "list-decimal marker:tabular-nums" : "list-disc")}>
                {b.items.map((it, j) => (
                  <li key={j} className="ps-0.5">
                    {inline_(it.text)}
                    {it.children.length > 0 && (
                      <ul className="mt-1 flex list-[circle] flex-col gap-1 ps-4 text-[14px] text-fg/85 marker:text-faint">
                        {it.children.map((c, k) => (
                          <li key={k}>{inline_(c)}</li>
                        ))}
                      </ul>
                    )}
                  </li>
                ))}
              </Tag>
            );
          }
          case "table":
            return (
              <div key={i} role="region" aria-label="table" tabIndex={0} className="-mx-1 overflow-x-auto rounded-xl border border-line bg-fg/[0.025]">
                <table className="w-full border-collapse text-[13.5px] leading-[1.5]">
                  <thead>
                    <tr className="border-b border-line">
                      {b.head.map((c, j) => (
                        <th key={j} scope="col" className={cx("px-3 py-2 text-[12px] font-semibold whitespace-nowrap text-muted", alignCls[b.align[j] ?? "left"])}>
                          {inline_(c)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((r, j) => (
                      <tr key={j} className="border-b border-line/60 align-top last:border-0">
                        {r.map((c, k) => (
                          <td key={k} className={cx("px-3 py-2", k === 0 && "font-semibold whitespace-nowrap", alignCls[b.align[k] ?? "left"])}>
                            {inline_(c)}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            );
        }
      })}
    </div>
  );
}
