import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { cx } from "@/lib/cx";
import { parseInline, safeHttpsUrl, type Inline } from "@/components/mini-markdown";

// The assistant's answers (ADR 0014): Markdown from a model that read untrusted data, so it is rendered like the release
// notes (components/mini-markdown): React elements built from strings, no raw HTML, no images (an image URL would send data
// out by itself). On top of the notes' subset: fenced code blocks, numbered lists, and links to the admin's own pages
// ("/nodes/nod_…") as router links. Other links only when they parse as https, shown with their host and opened apart.

type Block =
  | { kind: "code"; text: string }
  | { kind: "heading"; children: Inline[] }
  | { kind: "paragraph"; children: Inline[] }
  | { kind: "list"; ordered: boolean; items: Inline[][] };

/** A path of the admin itself: "/" then letters, digits and the usual id characters, no "//", no scheme. */
export const internalPath = (href: string) => /^\/(?!\/)[\w.~!$&'()*+,;=:@%/?#-]*$/.test(href) && !href.includes("//");

/** A link of an answer: an admin page as it is, an https address normalised, anything else dropped (its text stays). */
const chatHref = (raw: string) => (internalPath(raw) ? raw : safeHttpsUrl(raw));
const inlineOf = (src: string) => parseInline(src, 0, chatHref);

export function parseChatMarkdown(src: string): Block[] {
  const blocks: Block[] = [];
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  let para: string[] = [];
  let list: { ordered: boolean; items: string[] } | null = null;
  const endPara = () => {
    if (para.length) blocks.push({ kind: "paragraph", children: inlineOf(para.join(" ")) });
    para = [];
  };
  const endList = () => {
    if (list) blocks.push({ kind: "list", ordered: list.ordered, items: list.items.map((i) => inlineOf(i)) });
    list = null;
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    if (/^\s*```/.test(line)) {
      endPara();
      endList();
      const body: string[] = [];
      for (i++; i < lines.length && !/^\s*```/.test(lines[i]!); i++) body.push(lines[i]!);
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }
    const heading = /^#{1,4}\s+(.*)$/.exec(line);
    const bullet = /^\s*[-*]\s+(.*)$/.exec(line);
    const numbered = /^\s*\d{1,3}[.)]\s+(.*)$/.exec(line);
    if (heading) {
      endPara();
      endList();
      blocks.push({ kind: "heading", children: inlineOf(heading[1]!) });
    } else if (bullet || numbered) {
      endPara();
      const ordered = !!numbered;
      if (list && list.ordered !== ordered) endList();
      list ??= { ordered, items: [] };
      list.items.push((bullet ?? numbered)![1]!);
    } else if (line.trim() === "") {
      endPara();
      endList();
    } else if (list && /^\s{2,}\S/.test(line)) {
      list.items[list.items.length - 1] += " " + line.trim();
    } else {
      endList();
      para.push(line.trim());
    }
  }
  endPara();
  endList();
  return blocks;
}

function inline(nodes: Inline[], onNavigate?: () => void): ReactNode[] {
  return nodes.map((n, i) => {
    switch (n.kind) {
      case "text":
        return n.text;
      case "bold":
        return <strong key={i}>{inline(n.children, onNavigate)}</strong>;
      case "em":
        return <em key={i}>{inline(n.children, onNavigate)}</em>;
      case "code":
        return (
          <code key={i} className="rounded-md bg-fg/[0.07] px-1.5 py-0.5 font-mono text-[0.86em] font-normal">
            {n.text}
          </code>
        );
      case "link": {
        if (internalPath(n.href)) {
          return (
            <Link key={i} to={n.href} onClick={onNavigate} className="font-semibold text-accent-text underline decoration-accent-line underline-offset-2">
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

/** An answer of the assistant. onNavigate: a page link was followed (the chat may close on a phone). */
export function ChatMarkdown({ text, onNavigate }: { text: string; onNavigate?: () => void }) {
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
            return (
              <p key={i} className="font-bold">
                {inline(b.children, onNavigate)}
              </p>
            );
          case "paragraph":
            return <p key={i}>{inline(b.children, onNavigate)}</p>;
          case "list": {
            const Tag = b.ordered ? "ol" : "ul";
            return (
              <Tag key={i} className={cx("flex flex-col gap-1.5 ps-5 marker:text-muted", b.ordered ? "list-decimal marker:tabular-nums" : "list-disc")}>
                {b.items.map((it, j) => (
                  <li key={j}>{inline(it, onNavigate)}</li>
                ))}
              </Tag>
            );
          }
        }
      })}
    </div>
  );
}
