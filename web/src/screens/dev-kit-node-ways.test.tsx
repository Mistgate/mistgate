import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { nodePageData } from "./dev-kit-node-page";
import { seedWays, waysFrames, waysSceneOf } from "./dev-kit-node-ways";
import { setJob } from "./node/relay-job";
import { useWays, WaysTab } from "./node/ways";

// The scenes of the Ways in tab (/dev-kit?only=nodepage&tab=ways) draw the real tab on made-up data: every scene must draw, in every
// language, and say what it is about. The calls a click would make never answer (the cache is the scene).

const search: { current: Record<string, string> } = { current: {} };
vi.mock("@/lib/api", () => {
  const never = () => new Proxy({}, { get: () => () => new Promise(() => {}) });
  return new Proxy({ basepath: "/" } as Record<string, unknown>, { get: (t, k) => (k in t ? t[k as string] : never()) });
});
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => search.current,
  useNavigate: () => vi.fn(),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Element.prototype.scrollIntoView ??= () => {};
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
  setLang("en");
  setJob(undefined);
});

function Tab({ data }: { data: never }) {
  const w = useWays(data);
  return <WaysTab data={data} ways={w} />;
}

/** `fx` is the scene's `&fx=1`: a scene is still unless its address says so. */
async function draw(q: string, state: string, fx = false) {
  setEffectsEnabled(fx, false);
  const p = new URLSearchParams(q);
  search.current = { tab: "ways", ...Object.fromEntries(p) };
  const { qc, data } = nodePageData(state as never);
  const d = seedWays(qc, data as never, waysSceneOf(p), data.node.status);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Tab data={d as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
  return host.textContent ?? "";
}

describe("the scenes of the Ways in tab", () => {
  it.each(waysFrames.map((f) => [f.id, f] as const))("%s draws", async (_id, f) => {
    const text = await draw(f.q, f.state ?? "calm");
    expect(text).not.toContain("undefined");
    expect(text).not.toMatch(/\bwy\.[a-z]/);
    expect(text).toContain(f.q.includes("role=entry") ? "Whom it carries" : "How people get here");
  });

  it("says what each state of the cascade is", async () => {
    const say = async (q: string) => draw(`w=1&way=via:nod_msk1&${q}`, "calm");
    expect(await say("")).toContain("Forwarding");
    expect(await say("")).toContain("msk1 connects to de1 itself");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cas=br")).toContain("de1 connects to msk1 itself and keeps the line open");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cas=full")).toContain("New people are refused over the bridge");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cas=nobr")).toContain("update the agent of de1");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cause=fw")).toContain("Allow forwarding");
  });

  it.each([
    ["connect", "The bridge is down: de1 cannot connect to msk1", "Check de1 → msk1"],
    ["pin_mismatch", "The bridge is down: the certificate of the site of msk1 did not match the expected one", "Check again"],
    ["no_verification", "de1 does not know how to verify the site of msk1", null],
    ["tls_protocol", "The bridge is down: something other than the Mistgate site answers on the bridge port of msk1", "Open the profiles of msk1"],
    ["connection_limit", "msk1 reached its limit of connections from de1", null],
    ["pair_timeout", "The bridge loses people’s connections", "Check the speed"],
    ["target_connect", "The bridge is down: the server on de1 does not take connections from the bridge", "Open the profiles of de1"],
    ["identity_unavailable", "The bridge is down: the agent has no node certificate yet", "Open “Server”"],
    ["site_listen_failed", "The bridge is down: the bridge site on msk1 did not come up (site_listen_failed)", "Open the Doctor of msk1"],
    ["bad_config", "The bridge is down: the panel sent bad bridge settings", "Check again"],
    ["closed", "The bridge is restarting", null],
  ])("the bridge's word %s: its line and its button", async (code, line, action) => {
    const text = await draw(`w=1&err=${code}&way=via:nod_msk1`, "calm");
    expect(text).toContain(line);
    const note = host!.querySelector<HTMLElement>(".wy-plt")!;
    expect(note.querySelector("small")?.textContent).toBe(code);
    const buttons = [...note.querySelectorAll("button, a")].map((b) => b.textContent);
    expect(buttons).toEqual(action ? [action] : []);
  });

  it("the cascade's read error is one line and a way to ask again", async () => {
    const text = await draw("w=1&read=1&cas=br&way=via:nod_msk1", "calm");
    expect(text).toContain("Could not read the state of the bridge");
    expect(text).toContain("Try again");
    expect(host!.querySelector("[data-testid=pair-way] .wy-kv")).toBeNull();
  });

  it.each(["port", "mode"])("the %s editor is open under the first server's row", async (edit) => {
    await draw(`w=1&edit=${edit}&way=via:nod_msk1`, "calm");
    expect(host!.querySelector(`[data-testid=relay-${edit}-editor]`)).not.toBeNull();
    expect(host!.querySelectorAll("[data-testid^=relay-][data-testid$=-editor]")).toHaveLength(1);
  });

  it("draws the direct way's ports as one table with the switches of the subscriptions", async () => {
    const text = await draw("w=1", "calm");
    expect(text).toContain("Servers and ports");
    expect(text).toContain("udp/443");
    expect(text).toContain("tcp/80");
    expect(host!.querySelectorAll("[data-testid=port-row] [role=switch]").length).toBeGreaterThan(0);
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("draws an exit and an entry in %s with every word translated", async (lang) => {
    setLang(lang);
    for (const q of ["w=1&way=via:nod_msk1&cas=br", "w=1&role=entry&cas=full&way=out:nod_nl1"]) {
      const text = await draw(q, "calm");
      expect(text).not.toMatch(/\b(wy|nw|ways|relay)\.[a-z]/);
      act(() => root?.unmount());
      host?.remove();
    }
  });
});

describe("the scenes of the wizard Make reachable and of the strip over the ways", () => {
  const frame = (id: string) => waysFrames.find((f) => f.id === id)!;
  const dlg = () => document.querySelector<HTMLElement>("[role=dialog]")!;
  const stepOf = () => dlg().querySelector<HTMLElement>("[data-testid=relay-wizard]")!.dataset.step;
  const rowStates = () => [...dlg().querySelectorAll("[data-testid=wizard-run-row]")].map((r) => r.getAttribute("data-state"));

  // scene -> the step it is at and what it says (the board's PnNode: nqW1 .. nqW4)
  const table: [string, string, string[]][] = [
    ["wz-1", "1", ["The wizard was opened from the node’s page, so the node is already chosen", "no way", "Node: de1"]],
    ["wz-1-part", "1", ["partly"]],
    ["wz-2", "2", ["Through which node should people go to de1", "msk1", "Fits", "msk2 → de1 is blocked (TSPU check)", "Direct does not pass, but a bridge is possible: de1 reaches msk2", "Load now 34 %", "No check data from spb1", "Entry: msk1"]],
    ["wz-2-none", "2", ["All the entries are already used for de1", "Entry: none"]],
    ["wz-3", "3", ["3 of 3 servers", "its own key through the entry for every device", "14 people get “DE · via RU”: 3 more servers of de1 through msk1, beside the old ones", "AmneziaVPN: 3 devices — download the config “via RU” again", "3 relays through msk1"]],
    ["wz-3-bridge", "3", ["bridge", "Will go through the bridge: msk2 does not reach de1 directly", "does not go over the bridge yet", "1 of 3 servers", "Through the bridge de1 does not see the addresses of people"]],
    ["wz-4-run", "4", ["Checking and turning on · 1 of 3", "VLESS · Reality: works", "Hysteria2: checking msk1 → de1 and from outside", "AmneziaWG 3.1: waits", "You can close the window: the run goes on"]],
    ["wz-4-run-bridge", "4", ["Hysteria2: checking de1 → msk2 again and waiting for de1 to bring up the bridge (up to 30 s)"]],
    ["wz-4-done", "4", ["Done · 3 of 3", "de1 is reachable through msk1", "People get the new servers with the next subscription update", "All relays are on"]],
    ["wz-4-done-bridge", "4", ["works · bridge: msk2 does not reach de1 directly", "de1 is reachable through msk2"]],
    ["wz-4-part", "4", ["de1 — partly: 2 of 3", "Hysteria2: Not turned on: msk1 cannot reach de1 on 4443/udp", "Another entry…", "Some did not start: they stay off"]],
    ["wz-4-bad", "4", ["de1 — did not work out", "Port 2053 on msk1 is already taken by the profile", "Take 2054"]],
    ["wz-4-stopped", "4", ["Stopped · 1 of 3", "not started", "What is on stays, the rest is not touched"]],
  ];
  it.each(table)("%s: step %s", async (id, step, says) => {
    const text = await draw(frame(id).q, "calm");
    expect(text).not.toContain("undefined");
    expect(stepOf()).toBe(step);
    for (const s of says) expect(dlg().textContent, s).toContain(s);
    expect(dlg().textContent).not.toMatch(/\b(wy|nw|ways|relay)\.[a-z]/);
  });

  it("the steps of the run: its rows are in their states, the bar is as full as the run", async () => {
    await draw(frame("wz-4-run").q, "calm");
    expect(rowStates()).toEqual(["ok", "busy", "none"]);
    expect(dlg().querySelector<HTMLElement>(".wy-pg")!.style.getPropertyValue("--pgw")).toBe("33%");
    act(() => root?.unmount());
    host?.remove();
    await draw(frame("wz-4-part").q, "calm");
    expect(rowStates()).toEqual(["ok", "bad", "ok"]);
    expect(dlg().querySelector(".wy-pg")!.classList.contains("soon")).toBe(true);
    act(() => root?.unmount());
    host?.remove();
    await draw(frame("wz-4-bad").q, "calm");
    expect(dlg().querySelector(".wy-pg")!.classList.contains("bad")).toBe(true);
  });

  it("without the effects the end of a run has no wave and no glints", async () => {
    await draw(frame("wz-4-done").q, "calm");
    expect(dlg().querySelector(".wy-wv")).toBeNull();
    expect(dlg().querySelector(".wy-eg")).toBeNull();
  });

  it("the wave and the glints at the edge come with the end of a run, with the effects on", async () => {
    await draw(frame("wz-4-done").q, "calm", true);
    expect(dlg().querySelector(".wy-wv i")).not.toBeNull();
    expect(dlg().querySelectorAll(".wy-eg")).toHaveLength(2);
  });

  it("a run that came out only in part has no wave, whatever the effects", async () => {
    await draw(frame("wz-4-part").q, "calm", true);
    expect(dlg().querySelector(".wy-wv")).toBeNull();
  });

  it("the strip says what a run left and offers to finish it", async () => {
    await draw(frame("wip-left").q, "calm");
    const strip = document.querySelector<HTMLElement>("[data-testid=relay-unfinished]")!;
    expect(strip.textContent).toContain("Not finished");
    expect(strip.textContent).toContain("2 relays through msk1 were made but not turned on.");
    expect(strip.textContent).toContain("Finish it");
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("a run in the background: the strip has its count and bar, Open and Stop", async () => {
    await draw(frame("wip-run").q, "calm");
    const strip = document.querySelector<HTMLElement>("[data-testid=relay-run-strip]")!;
    expect(strip.textContent).toContain("Turning on relays through msk1 · 1 of 3");
    expect(strip.textContent).toContain("Open");
    expect(strip.textContent).toContain("Stop");
    expect(strip.querySelector<HTMLElement>(".wy-pg")!.style.getPropertyValue("--pgw")).toBe("33%");
  });

  it("an entry's page has the strip too, and its button leads to the exit's page", async () => {
    await draw(frame("wip-entry").q, "calm");
    const strip = document.querySelector<HTMLElement>("[data-testid=relay-unfinished]")!;
    expect(strip.querySelector("a")?.textContent).toBe("Finish it");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("draws the wizard at every step in %s with every word translated", async (lang) => {
    setLang(lang);
    for (const id of ["wz-1", "wz-2", "wz-3-bridge", "wz-4-run", "wz-4-part", "wip-left"]) {
      const text = await draw(frame(id).q, "calm");
      expect(`${text}${document.querySelector("[role=dialog]")?.textContent ?? ""}`).not.toMatch(/\b(wy|nw|ways|relay)\.[a-z]/);
      act(() => root?.unmount());
      host?.remove();
    }
  });
});
