import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { MiniMarkdown, parseMarkdown, safeHttpsUrl } from "./mini-markdown";

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

function render(text: string): HTMLDivElement {
  act(() => root?.unmount());
  host?.remove();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<MiniMarkdown text={text} />));
  return host;
}

describe("MiniMarkdown", () => {
  it("renders the markdown that release notes use", () => {
    const el = render(
      [
        "## Fixes",
        "",
        "- The **user page** named `eight` presets",
        "  wrapped on a second line.",
        "  - a nested bullet with [docs](https://mistgate.app/zh/)",
        "- second",
        "",
        "### Other",
        "Plain text.",
      ].join("\n"),
    );
    expect(el.querySelector("h4")?.textContent).toBe("Fixes");
    expect(el.querySelector("h5")?.textContent).toBe("Other");
    expect(el.querySelectorAll("li")).toHaveLength(3);
    expect(el.querySelector("li strong")?.textContent).toBe("user page");
    expect(el.querySelector("li code")?.textContent).toBe("eight");
    expect(el.querySelectorAll("li")[0]!.textContent).toContain("presets wrapped on a second line.");
    expect(el.querySelectorAll("li")[1]!.className).toContain("ml-5"); // the second level
    const a = el.querySelector("a")!;
    expect(a.getAttribute("href")).toBe("https://mistgate.app/zh/");
    expect(a.getAttribute("target")).toBe("_blank");
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
    expect(el.querySelector("p")?.textContent).toBe("Plain text.");
  });

  it("cannot inject HTML: markup stays text", () => {
    const evil = '<img src=x onerror="alert(1)"><script>alert(2)</script> **<b>x</b>** `<i>y</i>` [<u>z</u>](https://a.example/)';
    const el = render(`## <h1>boom</h1>\n\n- ${evil}\n\n${evil}`);
    expect(el.querySelector("img, script, b, i, u, h1")).toBeNull();
    expect(el.textContent).toContain('<img src=x onerror="alert(1)">');
    expect(el.textContent).toContain("<script>alert(2)</script>");
    expect(el.querySelector("strong")?.textContent).toBe("<b>x</b>");
    expect(el.querySelector("code")?.textContent).toBe("<i>y</i>");
    expect(el.querySelectorAll("a")).toHaveLength(2);
    for (const node of el.querySelectorAll("*")) for (const attr of node.getAttributeNames()) expect(attr.startsWith("on")).toBe(false);
  });

  it("keeps only https links", () => {
    const bad = [
      "javascript:alert(1)",
      "JaVaScRiPt:alert(1)",
      "data:text/html,<b>x</b>",
      "http://mistgate.app/",
      "//mistgate.app/",
      "/relative",
      "ftp://x.example/",
      "https://user:pw@mistgate.app/",
    ];
    const el = render(bad.map((u, i) => `[link${i}](${u})`).join("\n\n") + "\n\n[good](https://github.com/Mistgate/mistgate)");
    const anchors = [...el.querySelectorAll("a")];
    expect(anchors.map((a) => a.getAttribute("href"))).toEqual(["https://github.com/Mistgate/mistgate"]);
    // the text of a refused link stays, the address does not
    expect(el.textContent).toContain("link0");
    expect(el.textContent).not.toContain("alert(1)");
    expect(safeHttpsUrl("javascript:alert(1)")).toBe("");
    expect(safeHttpsUrl("https://mistgate.app/zh/")).toBe("https://mistgate.app/zh/");
  });

  it("copes with empty and odd input", () => {
    expect(parseMarkdown("")).toEqual([]);
    expect(parseMarkdown("\n\n  \n")).toEqual([]);
    expect(render("**unclosed and `unclosed [x](https://a.example").textContent).toBe("**unclosed and `unclosed [x](https://a.example");
    expect(render("- a\r\n- b\r\n").querySelectorAll("li")).toHaveLength(2);
  });

  it("parses a hostile body in linear time", () => {
    // 64 KiB of unclosed "[" (the per-release cap) took seconds with unbounded spans
    const started = performance.now();
    expect(parseMarkdown("[".repeat(64 << 10))).toHaveLength(1);
    expect(performance.now() - started).toBeLessThan(1000);
  });
});
