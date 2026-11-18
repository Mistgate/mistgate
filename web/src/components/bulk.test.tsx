import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import { TargetList } from "./bulk";

// The shared parts of the bulk bars: a list of names under a confirm is cut with "and N more".

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params }: { children?: ReactNode; to: string; params?: { id: string } }) => <a href={to.replace("$id", params?.id ?? "")}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function show(node: ReactNode) {
  act(() => root?.unmount());
  host?.remove();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}

describe("TargetList", () => {
  const names = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `usr_${i}`, name: `p${i}`, host: "" }));

  it("cuts the names past `max` with how many more, and does not cut for a single name over", async () => {
    await show(<TargetList title="Changes (30)" targets={names(30)} skipped={[]} max={12} />);
    expect(host!.textContent).toContain("p11");
    expect(host!.textContent).not.toContain("p12");
    expect(host!.textContent).toContain("and 18 more");
    await show(<TargetList title="Changes (13)" targets={names(13)} skipped={[]} max={12} />);
    expect(host!.textContent).toContain("p12");
    expect(host!.textContent).not.toContain("more");
  });

  it("groups the skipped by their reason, each group cut as well", async () => {
    const skipped = [...names(4).map((x) => ({ ...x, note: "Never expires" })), { id: "usr_9", name: "p9", host: "", note: "Gone" }];
    await show(<TargetList skipped={skipped} max={2} />);
    const text = host!.textContent ?? "";
    expect(text).toContain("Skipped (5)");
    expect(text).toContain("p0, p1");
    expect(text).toContain("and 2 more — Never expires");
    expect(text).toContain("p9 — Gone");
  });
});
