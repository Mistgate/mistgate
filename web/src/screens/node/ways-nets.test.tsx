import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { dictionaries, langs, setLang, type Lang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { ruChecksOf, ruKinds, type RuKind } from "@/screens/dev-kit-node-ru";
import { Nets } from "./ways-nets";
import type { WaysModel } from "./ways";

const getHistory = vi.fn();
vi.mock("@/lib/api", () => ({ countryReach: { getReachabilityHistory: (...a: unknown[]) => getHistory(...a) } }));
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
  getHistory.mockResolvedValue({ country: "RU", sources: ["globalping", "lists", "tspu"], sinceUnix: NOW - 7 * 86400, nowUnix: NOW, nodes: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.restoreAllMocks();
  getHistory.mockReset();
});

const check = (kind: RuKind) => ruChecksOf(kind, NOW)[0] as never;
const w = (over: object = {}) => ({ cc: "RU", owner: true, offline: false, ...over }) as unknown as WaysModel;
const mount = async (props: Partial<Parameters<typeof Nets>[0]> = {}, model: object = {}) => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <Nets w={w(model)} name="de1" check={check("ok")} kind="open" {...props} />
      </QueryClientProvider>,
    ),
  );
};
const text = (e: Element | null | undefined) => (e?.textContent ?? "").replaceAll(" ", " ");
const head = () => text(host!.querySelector(".wy-sh h4"));
const chips = () => [...host!.querySelectorAll(".wy-nets > span")].map((c) => text(c));
const more = () => host!.querySelector<HTMLButtonElement>(".wy-sh > button");
const rows = () => [...host!.querySelectorAll("[data-testid=nets-rows] .wy-rur")].map((r) => ({ id: (r as HTMLElement).dataset.testid, glyph: r.querySelector(".wy-gl")?.className.replace("wy-gl", "").trim(), say: [...r.querySelectorAll("b, span, em")].map((x) => text(x)), mut: r.classList.contains("mut") }));
const press = async (e: Element | null | undefined) => {
  await act(async () => void e?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

describe("the seen-from-the-country block of a way (the board's nd-sh, nd-nets, nd-rud)", () => {
  it("says it in the head by what the chips say, with the probe node and when, and has More", async () => {
    await mount({ check: check("ok") });
    expect(head()).toBe("From Russia it opens on every network");
    expect(chips()).toEqual(["Operator A", "Operator B", "Operator C", "Operator D"]);
    expect(host!.querySelector(".wy-sh > span")?.textContent).toBe("9 min ago");
    expect(more()?.textContent).toBe("More");
    expect(more()?.getAttribute("aria-expanded")).toBe("false");
    expect(host!.querySelector("[data-testid=nets-rows]")).toBeNull();
    // the sources come with the probe node: "probe msk1 · 34 min ago"
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: check("probe") });
    expect(host!.querySelector(".wy-sh > span")?.textContent).toBe("probe msk1 · 34 min ago");
    expect(host!.querySelector(".wy-sh > span .mono")?.textContent).toBe("msk1");
  });

  it.each<[RuKind, string, string[]]>([
    ["partial", "From Russia it does not open for everyone", ["Operator A does not open", "Operator B", "Operator C", "Operator D"]],
    ["bad", "From Russia it opens on no network", ["Operator A", "Operator B", "Operator C", "Operator D"]],
  ])("%s: the head and the chips", async (kind, title, list) => {
    await mount({ check: check(kind) });
    expect(head()).toBe(title);
    const got = chips();
    expect(got).toHaveLength(list.length);
    if (kind === "partial") expect(got[0]).toBe("Operator Adoes not open");
    if (kind === "bad") expect(got.every((c) => c.endsWith("does not open"))).toBe(true);
  });

  it("opens a line per source on More, and takes them back on Less", async () => {
    await mount({ check: check("probe") });
    await press(more());
    expect(more()?.textContent).toBe("Less");
    expect(more()?.getAttribute("aria-expanded")).toBe("true");
    const got = rows();
    expect(got.map((r) => r.id)).toEqual(["ru-tspu", "ru-globalping", "ru-lists"]);
    expect(got[0]).toMatchObject({ glyph: "ok", say: ["TSPU probe msk1", "opens", "34 min ago"] });
    expect(got[1]?.say[0]).toBe("Globalping");
    expect(got[1]?.say[1]).toBe("opens from all 2 networks");
    // the registry holds the address: a cross
    expect(got[2]).toMatchObject({ glyph: "bad", mut: false });
    expect(got[2]?.say[1]).toContain("the address is in the registry");
    expect(host!.querySelector("[data-testid=nets-rows] a")?.getAttribute("href")).toBe("/reachability");
    expect(host!.querySelector(".wy-nets")?.className).toContain("open");
    await press(more());
    expect(host!.querySelector("[data-testid=nets-rows]")).toBeNull();
  });

  it("says some of the networks, TCP cut and none in the words of each source", async () => {
    await mount({ check: check("partial") });
    await press(more());
    expect(rows().find((r) => r.id === "ru-globalping")).toMatchObject({ glyph: "part", say: ["Globalping", "opens from 3 networks of 4", "6 min ago"] });
    // the block lists are on: not listed is said
    expect(rows().find((r) => r.id === "ru-lists")).toMatchObject({ glyph: "ok", say: ["Block lists", "the address is not in the RKN registry", ""] });
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: check("conflict") });
    await press(more());
    expect(rows().find((r) => r.id === "ru-tspu")).toMatchObject({ glyph: "part", say: ["TSPU probe msk1", "TCP blocked", "40 min ago"] });
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: check("bad") });
    await press(more());
    expect(rows().find((r) => r.id === "ru-globalping")).toMatchObject({ glyph: "bad", say: expect.arrayContaining(["does not open from any network"]) });
  });

  it("puts a source that says nothing quietly, and an own probe by its AS or its name", async () => {
    await mount({ check: check("points") });
    await press(more());
    const got = rows();
    expect(got.map((r) => r.id)).toEqual(["ru-globalping", "ru-lists", "ru-probe:probe:dacha", "ru-probe:probe:office"].filter((id) => got.some((r) => r.id === id)));
    const dacha = got.find((r) => r.id?.includes("dacha"));
    expect(dacha).toMatchObject({ glyph: "none", mut: true });
    expect(dacha?.say[0]).toBe("Own probe · dacha");
    expect(dacha?.say[1]).toBe("no clear answer");
    expect(got.find((r) => r.id?.includes("office"))).toMatchObject({ glyph: "ok", mut: false });
    // no data from a source: the verdict word, quiet
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: check("unknown") });
    await press(more());
    expect(rows().find((r) => r.id === "ru-globalping")).toMatchObject({ glyph: "none", mut: true });
  });

  it("is the entry's own card without More, and the node's silence leaves the chips and the lines without data", async () => {
    await mount({ check: check("partial"), kind: "self" });
    expect(head()).toBe("Is de1 seen from Russia");
    expect(host!.querySelector(".wy-sh > span")?.textContent).toBe("not on every network · 9 min ago");
    expect(more()).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: check("ok") }, { offline: true });
    expect(chips()).toEqual(["Operator Ano data", "Operator Bno data", "Operator Cno data", "Operator Dno data"]);
    await press(more());
    expect(rows().find((r) => r.id === "ru-globalping")).toMatchObject({ glyph: "none", mut: true });
  });

  it("asks the question of another node as the board does, and says there is no check", async () => {
    await mount({ check: check("ok"), kind: "seen", name: "msk1" });
    expect(head()).toBe("Is msk1 seen from Russia");
    act(() => root?.unmount());
    host?.remove();
    await mount({ check: undefined });
    expect(host!.textContent).toContain("The country check has nothing on this node yet.");
    expect(more()).toBeNull();
    expect(host!.querySelector(".wy-nets")).toBeNull();
  });

  it("keeps twelve networks and long names to their line, and plays the opening only with the effects on", async () => {
    await mount({ check: check("long") });
    expect(chips().length).toBe(12);
    expect(host!.querySelector(".wy-nets b[title], .wy-nets b")?.textContent).toBeTruthy();
    await press(more());
    expect(host!.querySelector("[data-testid=nets-rows]")?.className).not.toContain("fx");
    const names = [...host!.querySelectorAll(".wy-rur b")].map((b) => b.getAttribute("title"));
    expect(names).toContain("TSPU probe msk1-edge-probe-node-with-a-long-name");
    expect(names).toContain("Own probe · dacha-in-the-village-with-a-long-name");
    act(() => root?.unmount());
    host?.remove();
    act(() => setEffectsEnabled(true));
    await mount({ check: check("ok") });
    await press(more());
    expect(host!.querySelector("[data-testid=nets-rows]")?.className).toContain("fx");
  });

  it("speaks all five languages, in the second person where it addresses anyone", async () => {
    const seen = new Map<string, string>();
    for (const l of langs as readonly Lang[]) {
      act(() => setLang(l));
      for (const k of ruKinds) {
        if (k === "none") continue;
        await mount({ check: check(k) });
        await press(more());
        seen.set(`${l}/${k}`, host!.textContent ?? "");
        // no key left raw
        expect(host!.textContent).not.toMatch(/wy\.ru\.|ways\.block/);
        act(() => root?.unmount());
        host?.remove();
        root = host = null;
      }
    }
    expect(seen.get("ru/partial")).toContain("Из России открывается не у всех");
    expect(seen.get("ru/partial")).toContain("открывается из 3 сетей из 4");
    expect(seen.get("zh/ok")).toContain("所有网络都能打开");
    expect(Object.keys(dictionaries)).toHaveLength(5);
  });
});
