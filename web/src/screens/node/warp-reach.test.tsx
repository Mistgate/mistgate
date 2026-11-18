import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { WarpSource, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { reachCounts, reachLook } from "./warp-reach-model";
import { WarpReachCard } from "./warp-reach";

// The reach card under the WARP card: what the node saw on each site, and the plain words for every state where there is no list.

const getWarp = vi.fn();
const me = vi.fn();
vi.mock("@/lib/api", () => ({
  warp: { getWarp: (...a: unknown[]) => getWarp(...a) },
  auth: { me: (...a: unknown[]) => me(...a) },
  fleet: {},
  nodes: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
// the links of the card are plain anchors here: the router is not what this test is about
vi.mock("@tanstack/react-router", () => ({ Link: ({ to, children, className }: { to: string; children: unknown; className?: string }) => <a href={to} className={className}>{children as never}</a> }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => me.mockResolvedValue({ admin: { id: "adm_1", role: 1 } }));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getWarp.mockReset();
  me.mockReset();
});

const nowS = () => Math.floor(Date.now() / 1000);
const site = (domain: string, verdict: string, httpStatus: number, latencyMs: number, blockedStreak = 0) => ({ domain, verdict, httpStatus, latencyMs, blockedStreak });
const mixed = [site("notion.com", "ok", 200, 240), site("discord.com", "blocked", 403, 180, 3), site("chatgpt.com", "error", 403, 310), site("claude.ai", "unreachable", 0, 12000)];

/** What GetWarp says: a working account, a result of the given sites; `over` replaces any of it. */
function data(over: Record<string, unknown> = {}, account: Record<string, unknown> | null = {}) {
  return {
    account: account && { enabled: true, source: WarpSource.REGISTERED, accountType: "free", hasToken: true, useReserved: false, endpointV4: "198.51.100.1", ...account },
    health: { state: WarpState.UP, reportedUnix: nowS() - 3 },
    pendingApply: false, applyError: "", agentSupports: true, tosUrl: "", needsAttention: false, attentionReason: "", inbounds: [],
    reach: { checkedUnix: nowS() - 240, colo: "FRA", domains: mixed },
    reachState: "ready",
    ...over,
  };
}

async function mount(d: unknown, props: { down?: boolean } = {}) {
  getWarp.mockResolvedValue(d);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <WarpReachCard nodeId="nod_1" {...props} />
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const rows = () => [...document.querySelectorAll<HTMLElement>(".nrc-li")];
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const link = (label: string) => [...document.querySelectorAll("a")].find((a) => a.textContent?.trim() === label);

describe("a result", () => {
  it("lists a site a row: reachable, a block page, a bare 403 that is an error, no answer", async () => {
    await mount(data());
    expect(rows().map((r) => r.dataset.verdict)).toEqual(["ok", "blocked", "error", "unreachable"]);
    const [ok, blocked, error, none] = rows();
    expect(ok!.textContent).toContain("notion.com");
    expect(ok!.textContent).toContain("HTTP 200 · 240 ms");
    expect(blocked!.textContent).toContain("Blocked");
    expect(blocked!.textContent).toContain("a block page");
    expect(blocked!.textContent).toContain("3 checks in a row");
    // a 403 the panel cannot place is "Error": never worded as a block
    expect(error!.textContent).toContain("Error");
    expect(error!.textContent).toContain("HTTP 403 · refused, but not by a known block page");
    expect(error!.textContent).not.toContain("Blocked");
    expect(none!.textContent).toContain("No answer");
  });

  it("says the exit, how long ago, and how many are blocked; offers the restart that may give another exit", async () => {
    await mount(data());
    expect(text()).toContain("Exit FRA · checked 4 min ago");
    expect(text()).toContain("Blocked: 1 of 4");
    expect(text()).toContain(en["warp.reach.fix"]);
  });

  it("is green when every site opens, and has no advice to give", async () => {
    await mount(data({ reach: { checkedUnix: nowS() - 30, colo: "", domains: mixed.map((m) => site(m.domain, "ok", 200, 100)) } }));
    expect(text()).toContain("All reachable");
    expect(text()).not.toContain(en["warp.reach.fix"]);
    expect(text()).toContain("Checked now"); // no exit named: the shorter line
  });

  it("is amber, not red, when sites only fail without a block page", async () => {
    await mount(data({ reach: { checkedUnix: nowS() - 60, colo: "FRA", domains: [site("a.example.com", "error", 429, 90), site("b.example.com", "unreachable", 0, 0)] } }));
    expect(text()).toContain("Trouble: 2 of 2");
    expect(text()).not.toContain("Blocked: ");
  });

  it("reads a verdict it does not know as an error", async () => {
    await mount(data({ reach: { checkedUnix: nowS() - 60, colo: "FRA", domains: [site("a.example.com", "weird", 200, 10)] } }));
    expect(rows()[0]!.dataset.verdict).toBe("error");
  });

  it("refreshes on a press (it re-reads the panel; the node is asked on the panel's own schedule)", async () => {
    await mount(data());
    expect(getWarp).toHaveBeenCalledTimes(1);
    await act(async () => void button("Refresh")?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(getWarp.mock.calls.length).toBeGreaterThan(1);
  });
});

describe("no list", () => {
  it("waits for the first answer while the tunnel is up", async () => {
    await mount(data({ reach: undefined, reachState: "waiting" }));
    expect(text()).toContain("Checking");
    expect(text()).toContain(en["warp.reach.wait.t"]);
    expect(rows()).toHaveLength(0);
    expect(button("Refresh")).toBeTruthy();
  });

  it("has no data when the tunnel is not up", async () => {
    await mount(data({ reach: undefined, reachState: "waiting", health: { state: WarpState.DOWN, reportedUnix: nowS() - 3 } }));
    expect(text()).toContain("No data");
    expect(text()).toContain(en["warp.reach.nodata.t"]);
  });

  it("says the node's agent cannot, and leads to Updates", async () => {
    await mount(data({ reach: undefined, reachState: "agent_too_old" }));
    expect(text()).toContain("Not supported");
    expect(text()).toContain(en["warp.reach.old.t"]);
    expect(link("Open Updates")).toBeTruthy();
    expect(button("Refresh")).toBeUndefined();
  });

  it("says the check is off, and gives the owner the list", async () => {
    await mount(data({ reach: undefined, reachState: "disabled" }));
    expect(text()).toContain(en["warp.reach.off.t"]);
    expect(link("Sites to check")?.getAttribute("href")).toBe("/settings/$section");
  });

  it("does not offer the list to anyone but the owner", async () => {
    me.mockResolvedValue({ admin: { id: "adm_2", role: 2 } });
    await mount(data({ reach: undefined, reachState: "disabled" }));
    expect(link("Sites to check")).toBeUndefined();
  });

  it("is nothing at all without a WARP account: the WARP card says what to do", async () => {
    await mount(data({}, null));
    expect(document.querySelector(".nrc")).toBeNull();
  });
});

describe("a result that is not renewed", () => {
  it("is the last one while WARP is paused, dimmed, with the reason", async () => {
    await mount(data({}, { enabled: false }));
    expect(text()).toContain("WARP paused");
    expect(text()).toContain(en["warp.reach.paused.last"]);
    expect(document.querySelector(".nrc-ls.dim")).not.toBeNull();
    expect(text()).not.toContain(en["warp.reach.fix"]);
  });

  it("is the last one while the node is silent, dimmed", async () => {
    await mount(data(), { down: true });
    expect(text()).toContain(en["np.wp.state.dead"]);
    expect(text()).toContain(en["warp.reach.dead.last"]);
    expect(document.querySelector(".nrc-ls.dim")).not.toBeNull();
  });

  it("is called old after hours", async () => {
    await mount(data({ reach: { checkedUnix: nowS() - 5 * 3600, colo: "FRA", domains: mixed } }));
    expect(text()).toContain(en["warp.reach.stale.t"]);
    expect(document.querySelector(".nrc-ls.dim")).not.toBeNull();
  });
});

describe("the reading of the answer", () => {
  it("counts blocks apart from the other trouble", () => {
    const look = reachLook(data() as never, false, nowS());
    expect(reachCounts(look.rows)).toEqual({ blocked: 1, trouble: 2, total: 4 });
    expect(look.state).toBe("ready");
    expect(look.stale).toBe(false);
  });
});
