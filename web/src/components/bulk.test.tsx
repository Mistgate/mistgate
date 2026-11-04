import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import type { Job, Row } from "@/lib/batch";
import { BatchPanel, TargetList, type PanelHead } from "./bulk";

// The shared parts of the bulk bars. A long batch (past `fold`) shows counters in place of the rows that went well and
// keeps the failed ones in sight; a short one lists every row; a list of names under a confirm is cut with "and N more".

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

const head: PanelHead = { icon: "calendar", tone: "sand", title: "Term", done: (n, ok) => `Changed for ${ok} of ${n}` };
const row = (i: number, state: Row["state"], over: Partial<Row> = {}): Row => ({ id: `usr_${i}`, name: `p${i}`, host: "", state, ...over });
const job = (rows: Row[], running: boolean): Job => ({ spec: { action: "extend", limit: 1, many: async () => {}, error: () => "" }, rows, running, stopping: false });

async function show(node: ReactNode) {
  act(() => root?.unmount());
  host?.remove();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}
const panel = (rows: Row[], running: boolean, fold?: number) =>
  show(<BatchPanel job={job(rows, running)} rows={rows} head={head} fold={fold} open={(r) => (r.state === "bad" ? { to: "/users/$id", params: { id: r.id } } : null)} onStop={() => {}} onRetry={() => {}} onClose={() => {}} />);

describe("BatchPanel, folded", () => {
  it("shows only the failed rows of 32, with counters for the rest and an Open link on each failure", async () => {
    const rows = Array.from({ length: 31 }, (_, i) => row(i, i < 25 ? "ok" : i === 25 || i === 26 ? "bad" : i < 29 ? "running" : "waiting", i === 25 || i === 26 ? { note: "Not found" } : {}));
    rows.push(row(40, "skipped", { note: "Already on" }));
    await panel(rows, true, 30);
    const items = [...document.querySelectorAll("li")];
    expect(items.map((li) => li.querySelector("b")?.textContent)).toEqual(["p25", "p26"]);
    expect(items.map((li) => li.querySelector("a")?.getAttribute("href"))).toEqual(["/users/usr_25", "/users/usr_26"]);
    const counters = document.querySelector("[data-fold]")!.textContent;
    expect(counters).toContain("Done 25");
    expect(counters).toContain("Running 2");
    expect(counters).toContain("Waiting 2");
    expect(counters).toContain("1 skipped");
  });

  it("keeps the counters for a batch that runs, not for one that ended (its summary says it all)", async () => {
    const rows = Array.from({ length: 40 }, (_, i) => row(i, i < 38 ? "ok" : "skipped"));
    await panel(rows, false, 30);
    expect(document.querySelector("[data-fold]")).toBeNull();
    expect(document.querySelectorAll("li")).toHaveLength(0);
    expect(host!.textContent).toContain("Changed for 38 of 40");
  });

  it("lists every row when the batch is not longer than the fold, and every row when there is none", async () => {
    const rows = Array.from({ length: 30 }, (_, i) => row(i, "ok"));
    await panel(rows, false, 30);
    expect(document.querySelectorAll("li")).toHaveLength(30);
    expect(document.querySelector("[data-fold]")).toBeNull();
    await panel([...rows, row(30, "ok")], false);
    expect(document.querySelectorAll("li")).toHaveLength(31);
  });

  it("sums a finished one up with the page's own words and offers Retry for the failed", async () => {
    const rows = [row(0, "ok"), row(1, "ok"), row(2, "bad", { note: "x" })];
    await panel(rows, false);
    const text = host!.textContent ?? "";
    expect(text).toContain("Changed for 2 of 3");
    expect(text).toContain("1 failed");
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.includes("Retry for 1"))).toBe(true);
  });
});

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
