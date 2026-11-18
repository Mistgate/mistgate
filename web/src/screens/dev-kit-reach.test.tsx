import { QueryClientProvider } from "@tanstack/react-query";
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang, type Lang } from "@/i18n";
import { ReachabilityScreen } from "@/screens/reachability";
import { ReachNav } from "@/screens/reachability/reach-nav";
import type { ReachSearch } from "@/screens/reachability/search";
import { flagsOf } from "./dev-kit-reach-data";
import { frames, seeded } from "./dev-kit-reach";
import { nid } from "./dev-kit-reach-data";

// The scenes of /dev-kit?only=pn-reach: every frame must draw, in every language, and say what it is made to say. The page is drawn as the scene
// draws it (its own cache, its own panel, its own place for the tab), without the shell around it, which has its own scenes.

vi.mock("@/lib/api", () => ({
  basepath: "/",
  auth: { me: () => Promise.resolve({}) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
  countryReach: {},
  health: {},
  nodes: {},
  globalping: {},
  blocklist: {},
  tspuProbe: {},
  cheburcheck: {},
  bsbord: {},
  clientProbe: {},
  ownProbe: {},
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to: _t, hash: _h, render: _r, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string; hash?: string; render?: unknown }) => (
    <a className={className} {...rest}>
      {children}
    </a>
  ),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
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
  localStorage.clear();
  setLang("en");
  document.body.innerHTML = "";
});

const roleOf = (params = "", state = "") => (params.includes("role=helper") ? Role.HELPER : params.includes("role=readonly") || state.includes(".ro") ? Role.READONLY : Role.OWNER);
const unique = [...new Map(frames.map((fr) => [`${fr.state}|${roleOf(fr.params, fr.state)}`, fr])).values()];

function Page({ state }: { state: string }) {
  const f = flagsOf(state);
  const [search, setSearch] = useState<ReachSearch>({ ...(f.tab !== "nodes" ? { tab: f.tab } : {}), ...(f.src ? { source: f.src } : {}) });
  return (
    <ReachNav.Provider value={{ search, go: setSearch }}>
      <ReachabilityScreen scene={{ filter: f.filter, open: f.open ? nid(f.open) : undefined }} />
    </ReachNav.Provider>
  );
}

async function draw(state: string, role: Role) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Page state={state} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 12; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const body = () => document.body.textContent ?? "";

const langs: Lang[] = ["en", "ru", "zh", "fa", "tr"];

describe("the scenes of the Reachability page", () => {
  it("lists a frame for every state of the board and of the product, with no duplicate id", () => {
    expect(new Set(frames.map((fr) => fr.id)).size).toBe(frames.length);
    for (const word of ["nosrc", "nonodes", "first", "load", "err", "allok", "bad", "reg", "tcp", "gperr", "gplim", "bsbud", "tsoff", "dlgoff", "toast", "check", "test", "replace", "ro", "edge", "v6", "dark", "probes", "kz", "dlgadd", "dlgupd", "dlgrev"])
      expect(frames.some((fr) => fr.state.split(".").includes(word)), word).toBe(true);
    for (const id of ["gp", "bl", "ts", "cc", "bs", "kl", "pr"]) expect(frames.some((fr) => fr.state.includes(`src.${id}`)), id).toBe(true);
    for (const tab of ["nodes", "events", "sources"]) expect(frames.some((fr) => fr.state.startsWith(tab)), tab).toBe(true);
  });

  it.each(unique.map((fr) => [fr.state, roleOf(fr.params, fr.state)] as const))("%s draws in every language", async (state, role) => {
    for (const lang of langs) {
      setLang(lang);
      await draw(state, role);
      expect(document.querySelector("h1")?.textContent, `${state} ${lang}`).toBeTruthy();
      expect(body(), `${state} ${lang}`).not.toContain("undefined");
      expect(body(), `${state} ${lang}`).not.toMatch(/\b(rp|rc|cr|gp|bl|tp|cc|bb|kc|pr|src)\.[a-z]/);
      act(() => root?.unmount());
      host?.remove();
      root = host = null;
    }
  });

  it("says what each state of the board is about", async () => {
    await draw("nodes", Role.OWNER);
    expect(document.querySelector("[aria-label='Reachability sections']")).not.toBeNull();
    expect(body()).toContain("fi1");
    expect(document.querySelectorAll("[data-testid=reach-node]")).toHaveLength(4);
    expect(document.querySelector(".rc-sum")?.textContent).toContain("3 of 4");
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.nosrc", Role.OWNER);
    expect(body()).toContain("No sources connected");
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.nonodes", Role.OWNER);
    expect(body()).toContain("No nodes yet");
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.first", Role.OWNER);
    expect(document.querySelector("[data-testid=first-round]")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.load", Role.OWNER);
    expect(document.querySelector(".hl-skl")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.err", Role.OWNER);
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    expect(document.querySelector(".pv-qe")).not.toBeNull();
  });

  it("opens the row of fi1 in every state of the board: the verdict in words, the facts, the networks, the hint", async () => {
    const say = async (state: string) => {
      await draw(state, Role.OWNER);
      const d = document.querySelector(".rc-w.open .r3");
      const out = { v: d?.querySelector(".r3-v b")?.textContent, sub: d?.querySelector(".r3-v span")?.textContent, facts: [...(d?.querySelectorAll(".r3-f dt") ?? [])].map((x) => x.textContent), tip: !!d?.querySelector(".r3-tip"), alert: !!d?.querySelector(".hl-act a:nth-child(2)"), bad: d?.querySelector("[data-testid=nets-bad] .r3-nt em")?.textContent ?? null, ok: d?.querySelector("[data-testid=nets-ok] .r3-nt em")?.textContent ?? null, kl: !!d?.querySelector(".r3-kl") };
      act(() => root?.unmount());
      host?.remove();
      root = host = null;
      return out;
    };
    expect(await say("nodes.open.fi1")).toMatchObject({ v: "Reachable from Russia, but not from every network", sub: "Dark on Operator B (Novosibirsk) and Operator D (Yekaterinburg)", facts: ["Networks", "Started", "Checked"], tip: true, alert: true, bad: "2", ok: "3" });
    expect(await say("nodes.allok.open.fi1")).toMatchObject({ v: "Reachable from Russia", facts: ["Networks", "Checked"], tip: false, alert: false, bad: null, ok: "5" });
    expect(await say("nodes.tcp.open.fi1")).toMatchObject({ v: "Opens, but TCP connections are dropped", tip: true, bad: "1", ok: "4" });
    expect(await say("nodes.bad.open.fi1")).toMatchObject({ v: "Unreachable from Russia", tip: true, bad: "5", ok: null });
    expect(await say("nodes.reg.open.fi1")).toMatchObject({ v: "Reachable, but the address is in the RKN registry", tip: false, alert: true });
    // the first round: nothing to say yet, no facts, no alert
    expect(await say("nodes.first.open.fi1")).toMatchObject({ v: "No data yet", sub: "The sources have not checked this node yet", facts: [], tip: false, alert: false, kl: true });
    // kl!ck with nothing on the node: its own line says so
    await draw("nodes.noreports.open.fi1", Role.OWNER);
    expect(document.querySelector(".rc-w.open .r3-kl")?.textContent).toContain("no reports yet");
    expect(document.querySelectorAll(".rc-w.open .r3-kl .rc-ch")).toHaveLength(0);
  });

  it("keeps twelve networks and the names of live data in the row", async () => {
    await draw("nodes.many.open.fi1", Role.OWNER);
    expect(document.querySelectorAll(".rc-w.open .r3-ni").length).toBeGreaterThanOrEqual(12);
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.long.open.fi1", Role.OWNER);
    const names = [...document.querySelectorAll(".rc-w.open .r3-ni b")].map((b) => b.getAttribute("title"));
    expect(names).toContain("Example Telecom of the Central Federal District");
    expect(document.querySelector(".rc-w.open .r3-ni span[title*='dacha-in-the-village-with-a-long-name']")).not.toBeNull();
  });

  it("says on the Events tab what the board says of the week in each state of the data", async () => {
    const said: [string, string][] = [
      ["events.allok", "All nodes opened from Russia in the last 7 days"],
      ["events.first", "No events yet: the first round is running"],
      ["events.nosrc", "No events: no sources are connected"],
      ["events.nonodes", "All nodes opened from Russia in the last 7 days"],
    ];
    for (const [state, words] of said) {
      await draw(state, Role.OWNER);
      expect(document.querySelector(".rc-evn")?.textContent, state).toBe(words);
      expect(document.querySelectorAll("[data-testid=reach-event]"), state).toHaveLength(0);
      act(() => root?.unmount());
      host?.remove();
    }
    // the default week has fi1 open and nl1 closed, each a row with its pill
    await draw("events", Role.OWNER);
    expect([...document.querySelectorAll("[data-testid=reach-event] .pv-pill")].map((p) => p.textContent)).toEqual(["Open", "Closed"]);
    act(() => root?.unmount());
    host?.remove();
    await draw("events.load", Role.OWNER);
    expect(document.querySelector(".hl-skl")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await draw("events.err", Role.OWNER);
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    expect(document.querySelector(".pv-qe")).not.toBeNull();
  });

  it("opens the row and the chip the scene names, and the source it names", async () => {
    await draw("nodes.open.fi1", Role.OWNER);
    expect(document.querySelector("#rc-d-nod_fi1")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await draw("nodes.filter.part", Role.OWNER);
    expect(document.querySelectorAll("[data-testid=reach-node]")).toHaveLength(1);
    act(() => root?.unmount());
    host?.remove();
    await draw("sources.src.bs", Role.OWNER);
    expect([...document.querySelectorAll<HTMLElement>("[role=tabpanel]")].find((p) => !p.hidden && p.querySelector("[data-source=bsbord]"))).toBeTruthy();
  });

  it("gives an observer the table and the events, and no country, no sources", async () => {
    await draw("nodes.ro", Role.READONLY);
    expect(document.querySelector(".rc-cty")).toBeNull();
    expect(document.querySelector(".rc-sd")).toBeNull();
    expect(document.querySelectorAll("[data-testid=reach-node]")).toHaveLength(4);
  });
});
