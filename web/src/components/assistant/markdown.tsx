import { Link } from "@tanstack/react-router";
import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { cx } from "@/lib/cx";
import { parseInline, safeHttpsUrl, type Inline } from "@/components/mini-markdown";

// The assistant's answers (ADR 0014): Markdown from a model that read untrusted data, so it is rendered like the release
// notes (components/mini-markdown): React elements built from strings, no raw HTML, no images (an image URL would send data
// out by itself). On top of the notes' subset: fenced code blocks, headings by level, numbered and nested lists, tables,
// quotes and rules, and links to the admin's own pages ("/nodes/nod_…") as router links. Other links only when they parse
// as https, shown with their host as text beside the link (what a title would hide) and opened apart.

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

type Ctx = { onNavigate?: () => void; entities?: readonly ChatEntity[]; pattern?: RegExp | null; bold?: boolean; words?: boolean };

/**
 * A run of text as one span per word, each fading in as it mounts (.chat-word): an answer that streams in. The key is the
 * word's place in its run, so a word already shown keeps its element and never fades in again.
 */
function wordSpans(text: string, key: string): ReactNode[] {
  return text.split(/(\s+)/).map((w, i) => (!w || /^\s/.test(w) ? w : <span key={`${key}.${i}`} className="chat-word">{w}</span>));
}

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
  const plain = (s: string, at: number) => (ctx.words ? wordSpans(s, `${keyBase}-${at}`) : s);
  if (!pattern || !entities?.length) return [plain(text, 0)];
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(pattern)) {
    const name = m[1]!;
    const e = entities.find((x) => x.name === name);
    if (!e || (commonWords.has(name.toLowerCase()) && !ctx.bold)) continue;
    if (m.index! > last) out.push(plain(text.slice(last, m.index), last));
    const chip = <EntityChip e={e} label={name} onNavigate={ctx.onNavigate} />;
    out.push(ctx.words ? <span key={`${keyBase}-${m.index}`} className="chat-word">{chip}</span> : <Fragment key={`${keyBase}-${m.index}`}>{chip}</Fragment>);
    last = m.index! + name.length;
  }
  if (last < text.length) out.push(plain(text.slice(last), last));
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
          <code key={i} className={cx("rounded-md bg-fg/[0.07] px-1.5 py-0.5 font-mono text-[0.84em] font-normal whitespace-nowrap", ctx.words && "chat-word")}>
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
          <Fragment key={i}>
            <a href={href} target="_blank" rel="noopener noreferrer" title={href} className="text-accent-text underline decoration-accent-line underline-offset-2">
              {n.text}
              <Icon name="externalLink" size={11} className="ms-0.5 inline-block align-[-1px]" />
            </a>
            <span data-link-host="" className="ms-1 text-[0.85em] break-all text-muted">
              {new URL(href).hostname}
            </span>
          </Fragment>
        );
      }
    }
  });
}

const alignCls = { left: "text-start", right: "text-end", center: "text-center" } as const;

/**
 * An answer of the assistant. onNavigate: a page link was followed (the chat may close on a phone). entities: the things the
 * panel knows by name (nodes), drawn as chips that open them. words: the answer is streaming in, each new word fades in.
 */
export function ChatMarkdown({ text, onNavigate, entities, words }: { text: string; onNavigate?: () => void; entities?: readonly ChatEntity[]; words?: boolean }) {
  const pattern = useMemo(() => entityPattern(entities ?? []), [entities]);
  const ctx: Ctx = { onNavigate, entities, pattern, words };
  const inlineCtx = (c: Inline[]) => inline(c, ctx);
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
                {inlineCtx(b.children)}
              </h3>
            ) : b.level === 2 ? (
              <h4 key={i} className="mt-1 text-[15.5px] leading-snug font-bold first:mt-0">
                {inlineCtx(b.children)}
              </h4>
            ) : (
              <h5 key={i} className="mt-0.5 text-[13px] leading-snug font-bold tracking-[0.02em] text-muted uppercase first:mt-0">
                {inlineCtx(b.children)}
              </h5>
            );
          case "paragraph":
            return <p key={i}>{inlineCtx(b.children)}</p>;
          case "quote":
            return (
              <blockquote key={i} className="border-s-2 border-line ps-3 text-muted">
                {inlineCtx(b.children)}
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
                    {inlineCtx(it.text)}
                    {it.children.length > 0 && (
                      <ul className="mt-1 flex list-[circle] flex-col gap-1 ps-4 text-[14px] text-fg/85 marker:text-faint">
                        {it.children.map((c, k) => (
                          <li key={k}>{inlineCtx(c)}</li>
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
                          {inlineCtx(c)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((r, j) => (
                      <tr key={j} className="border-b border-line/60 align-top last:border-0">
                        {r.map((c, k) => (
                          <td key={k} className={cx("px-3 py-2", k === 0 && "font-semibold whitespace-nowrap", alignCls[b.align[k] ?? "left"])}>
                            {inlineCtx(c)}
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

/** Where the text's last whole word ends (just past its last whitespace); 0 when there is none yet. */
const wholeWords = (text: string) => {
  const m = /\s\S*$/.exec(text);
  return m ? m.index + 1 : 0;
};

/**
 * While streaming, a stop never leaves a half behind: not inside a **bold** or `code` whose end has not arrived (it waits
 * before it, or goes on past its end when that is there), nor just after a list marker or heading mark (no bare bullet).
 */
export function settle(text: string, stop: number, end: number): number {
  for (const mark of ["**", "`"]) {
    if ((text.slice(0, stop).split(mark).length - 1) % 2 === 0) continue;
    const close = text.indexOf(mark, stop);
    if (close >= 0 && close + mark.length <= end) {
      const rest = text.slice(close + mark.length, end).search(/\s/);
      stop = rest < 0 ? end : close + mark.length + rest;
    } else stop = text.lastIndexOf(mark, stop - 1);
  }
  const bare = /(^|\n)[ \t]*(?:[-*+]|\d+[.)]|#{1,6})[ \t]*$/.exec(text.slice(0, stop));
  return bare ? bare.index + bare[1]!.length : stop;
}

/**
 * The answer as it is shown while it streams: it follows the text that has arrived word by word, faster the further behind it
 * is (a backlog clears in about 0.4 s, and it never goes slower than 60 characters a second), so chunks of any size flow in
 * evenly instead of landing in lumps. While `streaming`, a word still arriving waits for its end. Text that is already there
 * when the bubble mounts (history) is shown at once. The second value: this bubble streamed (its words fade in).
 */
export function useSmoothText(text: string, streaming: boolean): [string, boolean] {
  const [shown, setShown] = useState(text);
  const [animated, setAnimated] = useState(false);
  const ref = useRef(text);
  useEffect(() => {
    if (!text.startsWith(ref.current)) {
      // replaced, not grown (another conversation, or the words so far became the turn's thinking): no reveal
      ref.current = text;
      setShown(text);
      return;
    }
    const end = streaming ? Math.max(ref.current.length, wholeWords(text)) : text.length;
    if (ref.current.length >= end) return;
    setAnimated(true);
    let raf = 0;
    let last = performance.now();
    const step = (now: number) => {
      const dt = Math.min(50, now - last);
      last = now;
      const at = ref.current.length;
      const next = Math.min(end, at + Math.max(1, Math.round(dt * Math.max(0.06, (end - at) / 400))));
      const rest = text.slice(next, end).search(/\s/); // finish the word it stopped in
      const stop = streaming ? settle(text, rest < 0 ? end : next + rest, end) : rest < 0 ? end : next + rest;
      if (stop <= at) return; // waits for the rest of a bold, a code span or a list line
      ref.current = text.slice(0, stop);
      setShown(ref.current);
      if (ref.current.length < end) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [text, streaming]);
  return [shown, animated];
}
