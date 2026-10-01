import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { WarpSource, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { fill } from "@/i18n";
import { en } from "@/i18n/en";
import { describeWarpError } from "@/lib/warp-error";
import { WarpCard } from "./warp";

const getWarp = vi.fn();
const restartWarp = vi.fn();
const setWarpEnabled = vi.fn();
const registerWarp = vi.fn();
vi.mock("@/lib/api", () => ({
  warp: {
    getWarp: (...a: unknown[]) => getWarp(...a),
    restartWarp: (...a: unknown[]) => restartWarp(...a),
    setWarpEnabled: (...a: unknown[]) => setWarpEnabled(...a),
    registerWarp: (...a: unknown[]) => registerWarp(...a),
  },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  fleet: {},
  nodes: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
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
  for (const f of [getWarp, restartWarp, setWarpEnabled, registerWarp]) f.mockReset();
});

const nowS = () => Math.floor(Date.now() / 1000);
const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" }) as never;

type Health = Record<string, unknown>;
/** A node that reported a minute ago: the tunnel handshakes, nothing else is claimed unless the test says so. */
function data(health: Health, over: Record<string, unknown> = {}) {
  return {
    account: { enabled: true, source: WarpSource.REGISTERED, accountType: "free", hasToken: true, useReserved: false, endpointV4: "162.159.192.1" },
    health: {
      state: WarpState.UP, backend: "kernel", endpoint: "162.159.192.1:2408", lastHandshakeUnix: nowS() - 20, warpFlag: "on", colo: "ARN",
      probeCloudflareOk: true, probeOtherOk: true, consecutiveFailures: 0, rxBytes: 1000, txBytes: 500, lastError: "", reportedUnix: nowS() - 3,
      checkedUnix: 0, ...health,
    },
    pendingApply: false, applyError: "", agentSupports: true, tosUrl: "", needsAttention: false, attentionReason: "", inbounds: [], ...over,
  };
}

async function mount(d: unknown) {
  getWarp.mockResolvedValue(d);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <WarpCard nodeId="nod_1" nodeName="de1" retired={false} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const spinner = () => document.querySelector('[class*="mg-ui-spin"]');
/** The class of the dot of one probe row ("Cloudflare", "Other site"). */
const dotOf = (label: string) => {
  const row = [...document.querySelectorAll("span.flex")].find((r) => r.children[1]?.textContent === label);
  return row?.children[0]?.className ?? "";
};

// The case the owner saw: Cloudflare's edge answers every request late, the probe times out, the other probe passes.
const slowEdge = {
  state: WarpState.STARTING, probeCloudflareOk: false, probeOtherOk: true, warpFlag: "", consecutiveFailures: 1, checkedUnix: nowS() - 4,
  lastError: "probe_cloudflare_failed; ladder: reassert",
  probeCloudflare: { ok: false, latencyMs: 6001, atUnix: nowS() - 4 }, probeOther: { ok: true, latencyMs: 410, atUnix: nowS() - 4 },
};

describe("the WARP card after a failed check", () => {
  it("says what happened in words, and what the node does about it", async () => {
    await mount(data(slowEdge));
    expect(text()).toContain("Cloudflare did not answer through WARP within 6 s.");
    expect(text()).toContain("The tunnel is up, but WARP is slow or not passing traffic right now.");
    expect(text()).toContain("Trying to recover: reconnecting.");
    expect(text()).not.toContain("probe_cloudflare_failed"); // never the raw code for a code it knows
  });

  it("shows the state it is in, not a spinner: starting with a failing check is a warning", async () => {
    await mount(data(slowEdge));
    expect(text()).toContain("Starting: check failing");
    expect(spinner()).toBeNull();
  });

  it("shows the failed probe as a red dot and 'no answer', the passing one with its latency", async () => {
    await mount(data(slowEdge));
    expect(text()).toContain("Cloudflareno answer");
    expect(text()).toContain("Other siteok · 0.4 s");
    expect(dotOf("Cloudflare")).toContain("tone-bad");
    expect(dotOf("Cloudflare")).not.toContain("tone-ok");
    expect(dotOf("Other site")).toContain("tone-ok");
  });

  it("says how long ago the node checked", async () => {
    await mount(data(slowEdge));
    expect(text()).toMatch(/Checked \d s ago/);
  });

  it("a green 'Working' cannot sit next to a failed check", async () => {
    await mount(data({ ...slowEdge, state: WarpState.UP }));
    expect(text()).toContain("Working: last check failed");
    expect(text()).toContain("Cloudflare did not answer through WARP");
  });

  it("is quiet when the latest check passed: no error text, plain 'Working'", async () => {
    await mount(data({ state: WarpState.UP, checkedUnix: nowS() - 2, probeCloudflare: { ok: true, latencyMs: 380, atUnix: nowS() }, probeOther: { ok: true, latencyMs: 200, atUnix: nowS() } }));
    expect(text()).toContain("Working");
    expect(text()).not.toContain("last check failed");
    expect(text()).not.toContain("Trying to recover");
    expect(text()).toContain("Cloudflareok · 0.4 s");
  });
});

describe("slow probes", () => {
  it("a probe that passes but takes over 2 s is 'slow' with a warn dot", async () => {
    await mount(data({ checkedUnix: nowS() - 1, probeCloudflare: { ok: true, latencyMs: 3200, atUnix: nowS() }, probeOther: { ok: true, latencyMs: 300, atUnix: nowS() } }));
    expect(text()).toContain("Cloudflareslow · 3.2 s");
    expect(dotOf("Cloudflare")).toContain("tone-warn");
  });
});

describe("every code of the agent", () => {
  const reasons = [
    "probe_cloudflare_failed", "probe_other_failed", "warp_flag_off", "handshake_stale", "handshake_never", "link_down", "stat_failed",
    "backend_unavailable", "up_failed: open /dev/net/tun: no such file or directory",
  ];
  it.each(reasons)("%s renders a sentence", async (reason) => {
    await mount(data({ state: WarpState.DOWN, lastError: `${reason}; ladder: rotated to 162.159.192.7:500`, checkedUnix: nowS() }));
    const w = describeWarpError(t, reason)!;
    expect(w.head).toBeTruthy();
    expect(text()).toContain(w.head);
    expect(text()).toContain("switched the address and port to 162.159.192.7:500");
    expect(text()).not.toContain(`${reason.split(":")[0]}; ladder`);
  });

  it.each([["revoked", "Cloudflare no longer knows this account"], ["table_in_use", "routing table WARP uses (51820)"], ["rule_pref_in_use", "(90 and 110)"], ["apply_failed", "could not apply the WARP account"], ["down_after_ladder", "cannot recover by itself"]])(
    "the attention code %s is worded",
    async (code, part) => {
      await mount(data({ state: WarpState.DOWN, lastError: "link_down" }, { needsAttention: true, attentionReason: code }));
      expect(text()).toContain(part);
    },
  );

  it("shows an unknown code as it is, small and in mono", async () => {
    await mount(data({ state: WarpState.DOWN, lastError: "brand_new_failure; ladder: dance", checkedUnix: nowS() }, { needsAttention: true, attentionReason: "brand_new_attention" }));
    const mono = [...document.querySelectorAll(".font-mono")].map((e) => e.textContent);
    expect(mono).toContain("brand_new_failure; dance");
    expect(mono).toContain("brand_new_attention");
    expect(text()).toContain("cannot recover by itself"); // the generic attention text stays
  });
});

const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent === label);
async function click(el: Element | null | undefined) {
  expect(el).toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const dialog = () => document.querySelector('[role="dialog"]')!;

describe("what the owner can do with a node's WARP", () => {
  it("«Pause» asks first and says whose traffic stops", async () => {
    setWarpEnabled.mockResolvedValue({});
    await mount(data({}, { inbounds: [{ inboundId: "inb_1", profileName: "hy2 · WARP · 8443", online: 3 }] }));
    await click(button("Pause"));
    expect(text()).toContain("Pause WARP on de1?");
    expect(text()).toContain("The profile “hy2 · WARP · 8443” stops passing traffic: 3 connections through it now. It does not go out directly.");
    expect(setWarpEnabled).not.toHaveBeenCalled();
    await click(button("Pause WARP", dialog()));
    expect(setWarpEnabled).toHaveBeenCalledWith({ nodeId: "nod_1", enabled: false });
  });

  it("«Resume» needs no confirmation", async () => {
    setWarpEnabled.mockResolvedValue({});
    const d = data({});
    await mount({ ...d, account: { ...d.account, enabled: false } });
    await click(button("Resume"));
    expect(setWarpEnabled).toHaveBeenCalledWith({ nodeId: "nod_1", enabled: true });
  });

  it("a revoked account: «Register again» is the main action and replaces the account in one step", async () => {
    registerWarp.mockResolvedValue({});
    await mount(data({ state: WarpState.DOWN, lastError: "warp_flag_off" }, { needsAttention: true, attentionReason: "revoked" }));
    const again = button("Register again")!;
    expect(again.className).toContain("bg-accent");
    expect(button("Restart WARP")).toBeUndefined();
    await click(again);
    expect(text()).toContain("Register WARP again");
    await click(dialog().querySelector('[role="checkbox"]'));
    await click(button("Register again", dialog()));
    expect(registerWarp).toHaveBeenCalledWith(expect.objectContaining({ nodeId: "nod_1", acceptTos: true, replaceExisting: true }));
  });

  it("the tunnel is down on a live account: for how long, how many checks, and «Restart WARP» first", async () => {
    restartWarp.mockResolvedValue({ confirmed: true });
    await mount(data({ state: WarpState.DOWN, lastHandshakeUnix: nowS() - 3 * 3600 - 60, consecutiveFailures: 37, lastError: "handshake_stale" }));
    expect(text()).toContain("The handshake with Cloudflare has been failing for 3 h (37 checks in a row)");
    expect(text()).toContain("The WARP tunnel is silent"); // the agent's own words stay, under the line
    const restart = button("Restart WARP")!;
    expect(restart.className).toContain("bg-accent");
    expect(button("Read from Cloudflare again")!.className).not.toContain("bg-accent");
    await click(restart);
    expect(restartWarp).toHaveBeenCalledWith({ nodeId: "nod_1" });
    expect(text()).toContain("WARP restarted");
  });

  it("a handshake under a minute ago is not «0 min»", async () => {
    await mount(data({ state: WarpState.DOWN, lastHandshakeUnix: nowS() - 20, consecutiveFailures: 1, lastError: "probe_other_failed" }));
    expect(text()).toContain("The handshake with Cloudflare is failing (1 check in a row)");
    expect(text()).not.toContain("0 min");
  });

  it("the backend, Cloudflare's flag and the reserved bytes sit under «Details»", async () => {
    const d = data({});
    await mount({ ...d, account: { ...d.account, useReserved: true } });
    const details = document.querySelector("details")!;
    expect(details.querySelector("summary")?.textContent).toContain("Details");
    expect(details.textContent).toContain("kernel");
    expect(details.textContent).toContain("warp=on");
    expect(details.textContent).toContain("stamped");
  });
});

describe("a link to #warp", () => {
  afterEach(() => {
    history.replaceState(null, "", location.pathname);
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    vi.useRealTimers();
  });

  it("brings the card into view and lights it up for 2 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    history.replaceState(null, "", "#warp");
    await mount(data({}));
    expect(scroll).toHaveBeenCalled();
    expect(document.getElementById("warp")?.hasAttribute("data-glow")).toBe(true);
    await act(async () => void vi.advanceTimersByTime(2100));
    expect(document.getElementById("warp")?.hasAttribute("data-glow")).toBe(false);
  });

  it("does nothing without the anchor", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    await mount(data({}));
    expect(scroll).not.toHaveBeenCalled();
    expect(document.getElementById("warp")?.hasAttribute("data-glow")).toBe(false);
  });
});

describe("a node whose agent predates the per-probe results", () => {
  it("falls back to the flags and last_error, and to the report time for 'checked'", async () => {
    await mount(data({ state: WarpState.DOWN, probeCloudflareOk: true, probeOtherOk: false, lastError: "probe_other_failed", checkedUnix: 0, reportedUnix: nowS() - 20 }));
    expect(text()).toContain("Cloudflareok");
    expect(text()).not.toContain("Cloudflareok ·");
    expect(text()).toContain("Other siteno answer");
    expect(text()).toContain("A site outside Cloudflare did not answer through WARP");
    expect(text()).toMatch(/Checked \d+ s ago/);
  });
});
