import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { langs, setLang, type Lang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { nowRoundOf, ruChecksOf, ruKinds, type RuKind } from "@/screens/dev-kit-node-ru";
import { NodeRu } from "./checks-ru";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const NOW = 1_800_000_000;
let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  vi.spyOn(Date, "now").mockReturnValue(NOW * 1000);
  setLang("en");
  setEffectsEnabled(false);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const check = (kind: RuKind) => ruChecksOf(kind, NOW)[0] as never;
const mount = async (props: Partial<Parameters<typeof NodeRu>[0]> = {}) => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<NodeRu check={undefined} code="RU" {...props} />));
};
const row = () => host!.querySelector<HTMLElement>("[data-testid=node-ru]")!;
const said = (e: Element) => e.textContent!.replaceAll(" ", " ");

describe("the Russia row of the node's checks card (the board's nd-ru)", () => {
  const titles: [RuKind, string, string][] = [
    ["ok", "ok", "From Russia the node is reachable"],
    ["partial", "partial", "From Russia the node is not visible from all networks"],
    ["tcp", "warn", "From Russia the node’s TCP is blocked"],
    ["bad", "bad", "From Russia the node is unreachable"],
    ["listed", "listed", "The node is in a block list, not checked live from Russia"],
    ["unknown", "unknown", "No data on the node from Russia"],
    ["none", "none", "The node has not been checked from Russia yet"],
  ];
  it.each(titles)("%s: the flag, the words and a line under them", async (kind, state, title) => {
    await mount({ check: kind === "none" ? undefined : check(kind) });
    expect(row().dataset.kind).toBe(state);
    expect(row().querySelector("b")!.textContent).toBe(title);
    expect(row().querySelector(".g svg")).not.toBeNull();
    expect(row().querySelector('a[href="/reachability"]')).not.toBeNull();
  });

  it("a silent node says what the board says, whatever the last verdict was", async () => {
    await mount({ check: check("ok"), status: NodeStatus.DOWN });
    expect(row().dataset.kind).toBe("down");
    expect(row().querySelector("b")!.textContent).toBe("From Russia the node is not responding now");
  });

  it("the line: the networks counted and named, the probe node apart, the registry, the time last and whole", async () => {
    await mount({ check: check("probe") });
    const sub = row().querySelector("b + span")!;
    expect(said(sub)).toBe("2 networks: Operator A and Operator B · probe node msk1 · in the RKN registry · 34 min ago");
    expect(sub.querySelector(".font-mono")!.textContent).toBe("msk1");
    expect(sub.lastElementChild!.className).toContain("whitespace-nowrap");
    // a "·" never starts a line: it is glued to the word before it
    expect(sub.textContent).toContain(" · ");
  });

  it("more than five networks: the first four and 'and N more'", async () => {
    await mount({ check: check("long") });
    expect(said(row().querySelector("b + span")!)).toContain("8 networks: Example Telecom of the Central Federal District, Operator Mobile Communications Limited, Operator B, Operator C and 4 more");
  });

  it("no line under the words for a node nobody has checked", async () => {
    await mount({ check: undefined });
    expect(row().querySelector("b + span")).toBeNull();
  });

  it("a partial block lays the networks under the row, in the column of the text", async () => {
    await mount({ check: check("partial") });
    const more = row().querySelector("[data-testid=partial-breakdown]")!;
    expect(more.parentElement!.className).toContain("col-start-2");
    expect(more.textContent).toContain("Operator A");
    expect(row().querySelector("[data-testid=own-points]")).toBeNull();
  });

  it("the owner's own probes are lines under the row with what each saw, 'no clear answer' included", async () => {
    await mount({ check: check("points") });
    const lines = [...row().querySelectorAll("[data-testid=own-points] li")].map((li) => li.textContent);
    expect(lines).toEqual(["?AS64500 · Antalya — probe “dacha” no clear answer · 5 h ago", "✓Operator E · Perm — probe “office” reaches the node · 12 min ago"]);
  });

  it("a result days old says so; long names of networks and probes stay in their column", async () => {
    await mount({ check: check("old") });
    expect(said(row())).toContain("3 d ago");
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: check("long") });
    expect(row().querySelectorAll("[data-testid=partial-breakdown] li").length).toBeGreaterThan(8);
    expect(row().className).toContain("grid-cols-[28px_minmax(0,1fr)]");
  });
});

describe("the animations of the row (the reveal animation: its switch and the system's 'reduce motion')", () => {
  const round = (state: Parameters<typeof nowRoundOf>[0]) => nowRoundOf(state, NOW)[0] as never;
  it("a round of the country check that is running sends a radar off the flag, with the effects on", async () => {
    setEffectsEnabled(true);
    await mount({ check: check("ok"), round: round("running") });
    expect(row().classList.contains("now")).toBe(true);
    await act(async () => root!.render(<NodeRu check={check("ok")} code="RU" round={round("done")} />));
    expect(row().classList.contains("now")).toBe(false);
  });

  it("no radar for anything else: no round, a round that has ended, a press that was too early, nothing to ask", async () => {
    setEffectsEnabled(true);
    await mount({ check: check("ok") });
    for (const r of [undefined, round("done"), round("early"), round("off"), round("failed"), round("nobudget")]) {
      await act(async () => root!.render(<NodeRu check={check("ok")} code="RU" round={r} />));
      expect(row().classList.contains("now"), JSON.stringify(r)).toBe(false);
    }
  });

  it("with the effects off it stays still, the words are there", async () => {
    await mount({ check: check("ok"), round: round("running") });
    expect(row().classList.contains("now")).toBe(false);
    expect(row().textContent).toContain("Rechecking now: Globalping, TSPU probe, cheburcheck, Own probes");
  });

  it("a result that has come makes the words come up for two seconds", async () => {
    setEffectsEnabled(true);
    vi.useFakeTimers();
    await mount({ check: check("ok") });
    expect(row().classList.contains("fresh")).toBe(false);
    const newer = { ...(check("ok") as object), atUnix: NOW - 60 } as never;
    await act(async () => root!.render(<NodeRu check={newer} code="RU" />));
    expect(row().classList.contains("fresh")).toBe(true);
    await act(async () => void vi.advanceTimersByTime(2100));
    expect(row().classList.contains("fresh")).toBe(false);
    // the same result again is no news
    await act(async () => root!.render(<NodeRu check={{ ...(newer as object) } as never} code="RU" />));
    expect(row().classList.contains("fresh")).toBe(false);
  });
});

describe("the words in every language", () => {
  it.each(langs)("%s: every state has a sentence, with the country in it and nothing left to fill", async (lang: Lang) => {
    setLang(lang);
    const seen = new Set<string>();
    for (const kind of ruKinds.filter((k) => ["ok", "partial", "tcp", "bad", "listed", "unknown", "none"].includes(k))) {
      await mount({ check: kind === "none" ? undefined : check(kind) });
      const title = row().querySelector("b")!.textContent!;
      expect(title.length).toBeGreaterThan(8);
      expect(title).not.toMatch(/[{}]|np\.ru\./);
      seen.add(title);
      act(() => root?.unmount());
      host?.remove();
    }
    await mount({ check: check("ok"), status: NodeStatus.DOWN });
    seen.add(row().querySelector("b")!.textContent!);
    expect(seen.size).toBe(8);
    setLang("en");
  });
});
