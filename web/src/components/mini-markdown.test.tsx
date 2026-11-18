import { describe, expect, it } from "vitest";
import { parseMarkdown, safeHttpsUrl, type Block, type Inline } from "./mini-markdown";

// Release notes are text from a GitHub release page: parsed into data, never into HTML, and a link survives only as https.

const spans = (blocks: Block[]): Inline[] => blocks.flatMap((b) => (b.kind === "list" ? b.items.flatMap((i) => i.children) : b.children));
const flat = (nodes: Inline[]): Inline[] => nodes.flatMap((n) => (n.kind === "bold" || n.kind === "em" ? [n, ...flat(n.children)] : [n]));

describe("parseMarkdown", () => {
  it("reads the markdown that release notes use: headings, two levels of bullets, bold, code, links, paragraphs", () => {
    const blocks = parseMarkdown(
      ["## Fixes", "", "- The **user page** named `eight` presets", "  wrapped on a second line.", "  - a nested bullet with [docs](https://mistgate.app/zh/)", "- second", "", "### Other", "Plain text."].join("\n"),
    );
    expect(blocks.map((b) => b.kind)).toEqual(["heading", "list", "heading", "paragraph"]);
    expect(blocks.filter((b) => b.kind === "heading").map((b) => b.level)).toEqual([2, 3]);
    const list = blocks[1] as Extract<Block, { kind: "list" }>;
    expect(list.items.map((i) => i.depth)).toEqual([0, 1, 0]);
    const first = list.items[0]!.children;
    expect(first.find((n) => n.kind === "bold")).toMatchObject({ children: [{ kind: "text", text: "user page" }] });
    expect(first.find((n) => n.kind === "code")).toMatchObject({ text: "eight" });
    expect(first.map((n) => (n.kind === "text" ? n.text : "")).join("")).toContain("presets wrapped on a second line.");
    expect(list.items[1]!.children.find((n) => n.kind === "link")).toEqual({ kind: "link", text: "docs", href: "https://mistgate.app/zh/" });
  });

  it("cannot inject HTML: markup stays text", () => {
    const evil = '<img src=x onerror="alert(1)"><script>alert(2)</script> **<b>x</b>** `<i>y</i>` [<u>z</u>](https://a.example/)';
    const all = flat(spans(parseMarkdown(`## <h1>boom</h1>\n\n- ${evil}\n\n${evil}`)));
    expect(all.every((n) => ["text", "bold", "code", "link"].includes(n.kind))).toBe(true);
    const text = all.map((n) => (n.kind === "text" ? n.text : "")).join("");
    expect(text).toContain('<img src=x onerror="alert(1)">');
    expect(text).toContain("<script>alert(2)</script>");
    expect(all.find((n) => n.kind === "bold")).toMatchObject({ children: [{ kind: "text", text: "<b>x</b>" }] });
    expect(all.find((n) => n.kind === "code")).toMatchObject({ text: "<i>y</i>" });
    expect(all.filter((n) => n.kind === "link")).toHaveLength(2);
  });

  it("keeps only https links", () => {
    const bad = ["javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,<b>x</b>", "http://mistgate.app/", "//mistgate.app/", "/relative", "ftp://x.example/", "https://user:pw@mistgate.app/"];
    const all = flat(spans(parseMarkdown(bad.map((u, i) => `[link${i}](${u})`).join("\n\n") + "\n\n[good](https://github.com/Mistgate/mistgate)")));
    expect(all.filter((n) => n.kind === "link").map((n) => n.href)).toEqual(["https://github.com/Mistgate/mistgate"]);
    // the text of a refused link stays, the address does not
    const text = all.map((n) => (n.kind === "text" ? n.text : "")).join("");
    expect(text).toContain("link0");
    expect(text).not.toContain("alert(1)");
    expect(safeHttpsUrl("javascript:alert(1)")).toBe("");
    expect(safeHttpsUrl("https://mistgate.app/zh/")).toBe("https://mistgate.app/zh/");
  });

  it("copes with empty and odd input", () => {
    expect(parseMarkdown("")).toEqual([]);
    expect(parseMarkdown("\n\n  \n")).toEqual([]);
    expect(parseMarkdown("**unclosed and `unclosed [x](https://a.example")).toEqual([{ kind: "paragraph", children: [{ kind: "text", text: "**unclosed and `unclosed [x](https://a.example" }] }]);
    expect((parseMarkdown("- a\r\n- b\r\n")[0] as Extract<Block, { kind: "list" }>).items).toHaveLength(2);
  });

  it("parses a hostile body in linear time", () => {
    // 64 KiB of unclosed "[" (the per-release cap) took seconds with unbounded spans
    const started = performance.now();
    expect(parseMarkdown("[".repeat(64 << 10))).toHaveLength(1);
    expect(performance.now() - started).toBeLessThan(1000);
  });
});
