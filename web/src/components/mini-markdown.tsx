import type { ReactNode } from "react";

// A tiny, safe renderer for release notes, which are text from a GitHub release page: informational, not signed, and never
// trusted as HTML. It knows what those notes use and nothing else: "#"/"##" and "###" headings, "-" bullets (two levels),
// paragraphs, **bold**, `code` and [text](https://…) links. Everything is built as React elements from strings, so markup in
// the text stays text (no dangerouslySetInnerHTML anywhere), and a link is kept only when its address parses as https: a
// "javascript:", "data:" or plain "http:" address leaves just the link text.

export type Inline =
  | { kind: "text"; text: string }
  | { kind: "bold"; children: Inline[] }
  | { kind: "code"; text: string }
  | { kind: "link"; text: string; href: string };

export type Block =
  | { kind: "heading"; level: 2 | 3; children: Inline[] }
  | { kind: "paragraph"; children: Inline[] }
  | { kind: "list"; items: { depth: 0 | 1; children: Inline[] }[] };

const inlineRe = /`([^`]+)`|\*\*(.+?)\*\*|\[([^\]]+)\]\(([^)\s]+)\)/g;

/** The https address of a link, normalised, or "" for anything else (no user info either: it would hide the real host). */
export function safeHttpsUrl(raw: string): string {
  try {
    const u = new URL(raw);
    return u.protocol === "https:" && u.hostname !== "" && !u.username && !u.password ? u.href : "";
  } catch {
    return "";
  }
}

export function parseInline(src: string, depth = 0): Inline[] {
  const out: Inline[] = [];
  let last = 0;
  const push = (text: string) => text && out.push({ kind: "text", text });
  for (const m of src.matchAll(inlineRe)) {
    push(src.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[1] !== undefined) out.push({ kind: "code", text: m[1] });
    else if (m[2] !== undefined) out.push({ kind: "bold", children: depth < 2 ? parseInline(m[2], depth + 1) : [{ kind: "text", text: m[2] }] });
    else {
      const href = safeHttpsUrl(m[4]!);
      if (href) out.push({ kind: "link", text: m[3]!, href });
      else push(m[3]!);
    }
  }
  push(src.slice(last));
  return out;
}

export function parseMarkdown(src: string): Block[] {
  const blocks: Block[] = [];
  let para: string[] = [];
  const endPara = () => {
    if (para.length) blocks.push({ kind: "paragraph", children: parseInline(para.join(" ")) });
    para = [];
  };
  let list: Extract<Block, { kind: "list" }> | null = null;
  for (const raw of src.replace(/\r\n?/g, "\n").split("\n")) {
    const line = raw.replace(/\s+$/, "");
    if (line.trim() === "") {
      endPara();
      list = null;
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      endPara();
      list = null;
      blocks.push({ kind: "heading", level: heading[1]!.length <= 2 ? 2 : 3, children: parseInline(heading[2]!.replace(/\s+#+$/, "")) });
      continue;
    }
    const bullet = /^(\s*)[-*+]\s+(.*)$/.exec(line);
    if (bullet) {
      endPara();
      if (!list) blocks.push((list = { kind: "list", items: [] }));
      list.items.push({ depth: bullet[1]!.replace(/\t/g, "  ").length >= 2 ? 1 : 0, children: parseInline(bullet[2]!) });
      continue;
    }
    if (list && /^\s+\S/.test(line)) {
      // a wrapped line of the previous bullet
      const last = list.items[list.items.length - 1]!;
      last.children = parseInline(`${plainOf(last.children)} ${line.trim()}`);
      continue;
    }
    list = null;
    para.push(line.trim());
  }
  endPara();
  return blocks;
}

/** The source text of inline nodes, to glue a wrapped bullet line back on (marks are re-read, so nothing is lost). */
function plainOf(nodes: Inline[]): string {
  return nodes
    .map((n) => {
      switch (n.kind) {
        case "text":
          return n.text;
        case "code":
          return `\`${n.text}\``;
        case "bold":
          return `**${plainOf(n.children)}**`;
        case "link":
          return `[${n.text}](${n.href})`;
      }
    })
    .join("");
}

function renderInline(nodes: Inline[]): ReactNode[] {
  return nodes.map((n, i) => {
    switch (n.kind) {
      case "text":
        return n.text;
      case "bold":
        return (
          <strong key={i} className="font-bold text-fg">
            {renderInline(n.children)}
          </strong>
        );
      case "code":
        return (
          <code key={i} className="rounded-[5px] bg-surface-2 px-1 py-px font-mono text-[0.92em] break-words text-fg">
            {n.text}
          </code>
        );
      case "link":
        return (
          <a
            key={i}
            href={n.href}
            target="_blank"
            rel="noopener noreferrer"
            className="cursor-pointer text-accent-text underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            {n.text}
          </a>
        );
    }
  });
}

/** Release notes as markdown, rendered with the page's own type and colours. */
export function MiniMarkdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={className}>
      {parseMarkdown(text).map((b, i) => {
        switch (b.kind) {
          case "heading":
            return b.level === 2 ? (
              <h4 key={i} className="mt-3 mb-1 text-[13px] font-bold tracking-[-0.01em] text-fg first:mt-0">
                {renderInline(b.children)}
              </h4>
            ) : (
              <h5 key={i} className="mt-2.5 mb-1 text-xs font-bold text-muted first:mt-0">
                {renderInline(b.children)}
              </h5>
            );
          case "list":
            return (
              <ul key={i} className="my-1 flex flex-col gap-1">
                {b.items.map((it, j) => (
                  <li
                    key={j}
                    className={
                      it.depth === 1
                        ? "relative ml-5 pl-3.5 before:absolute before:top-[0.6em] before:left-0 before:h-px before:w-2 before:bg-faint"
                        : "relative pl-3.5 before:absolute before:top-[0.55em] before:left-0 before:size-1 before:rounded-full before:bg-muted"
                    }
                  >
                    {renderInline(it.children)}
                  </li>
                ))}
              </ul>
            );
          default:
            return (
              <p key={i} className="my-1.5 first:mt-0">
                {renderInline(b.children)}
              </p>
            );
        }
      })}
    </div>
  );
}
