import { describe, expect, it } from "vitest";
import { sanitizeLogo, setBrand } from "./brand";

const svg = (body: string, attrs = "") => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" ${attrs}>${body}</svg>`;

describe("sanitizeLogo", () => {
  it("binds the three tone fills to the logo variables and keeps other fills", () => {
    const out = sanitizeLogo(
      svg('<path fill="#FFFFFF" d="M0 0h1"/><path fill="#cba5fa" d="M0 0h2"/><path fill="#693fc2" d="M0 0h3"/><path fill="#123456" d="M0 0h4"/>'),
    )!;
    expect(out).toContain("fill:var(--logo-w)");
    expect(out).toContain("fill:var(--logo-l)");
    expect(out).toContain("fill:var(--logo-d)");
    expect(out).toContain('fill="#123456"');
    expect(out).toContain('aria-hidden="true"');
  });

  it("drops scripts, handlers, links, styles, foreign content and metadata", () => {
    const out = sanitizeLogo(
      svg(
        '<script>alert(1)</script><metadata>secret</metadata><foreignObject><div/></foreignObject><image href="https://evil.example/x.png"/>' +
          '<style>*{fill:red}</style><a href="javascript:alert(1)"><path d="M0 0h1" onclick="alert(1)" style="fill:red" fill="url(https://evil.example/a)"/></a>' +
          '<path d="M0 0h1" fill="url(#g)"/>',
        'onload="alert(1)" xmlns:c2pa="http://c2pa.org/manifest"',
      ),
    )!;
    for (const bad of ["script", "metadata", "foreignObject", "image", "<style", "<a ", "href", "onclick", "onload", "evil", "c2pa", "javascript"]) {
      expect(out).not.toContain(bad);
    }
    expect(out).toContain('fill="url(#g)"');
  });

  it("rejects things that are not a usable SVG", () => {
    expect(sanitizeLogo("<div>not svg</div>")).toBeNull();
    expect(sanitizeLogo("<svg><path></svg>")).toBeNull();
    expect(sanitizeLogo(svg("") + " ".repeat(300 * 1024))).toBeNull();
  });
});

describe("setBrand", () => {
  it("keeps the current logo when the new one is invalid and derives the display name", () => {
    setBrand({ wordmark: ["north", "star"], logoSvg: "nope" });
    setBrand({ logoSvg: svg('<path fill="#693fc2" d="M0 0h1"/>') });
    expect(document.title).toBe("Northstar");
  });
});
