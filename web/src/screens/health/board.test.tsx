import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { health as healthApi } from "@/lib/api";
import { SceneBody, seeded } from "@/screens/dev-kit-health";

// The behaviour the board's script asks of the page, on the scene's made-up data and its own answers of the panel: which row is open, the filter by node,
// "Close all shown" with its window and its Undo, a fix from asking to done (and the wave) and to failing (and "Try again"), a link to an alert, the roles,
// "Run again" over the nodes.

const wave = vi.fn();
vi.mock("@/components/fx/wave", () => ({ playWave: (...a: unknown[]) => wave(...a), originOf: () => ({ x: 1, y: 1 }) }));
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, health: {}, nodes: {}, fleet: {}, users: {}, assistant: {}, profiles: {}, groups: {}, relays: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to, render: _r, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string; render?: unknown }) => (
    <a className={className} href={to} {...rest}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  wave.mockReset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
  setLang("en");
  document.body.innerHTML = "";
  history.replaceState(null, "", location.pathname);
});

async function settle(n = 8) {
  for (let i = 0; i < n; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function draw(state: string, role = Role.OWNER, before?: () => void) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  before?.();
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <SceneBody state={state} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle(12);
}
const body = () => document.body.textContent ?? "";
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => void el!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
const toggles = () => [...document.querySelectorAll<HTMLButtonElement>("[data-alert-toggle]")];
const sideNode = (name: string) => [...document.querySelectorAll<HTMLButtonElement>(".hl-nodes button")].find((b) => b.querySelector("b")?.textContent === name);
const calls = (f: unknown) => (f as ReturnType<typeof vi.fn>).mock.calls;

describe("the head of the page", () => {
  it("counts the problems and the tabs from what the panel says: alerts in amber, people and the doctor quiet", async () => {
    await draw("alerts");
    expect(document.querySelector(".sb-h p")?.textContent).toBe("7 problems · checks every 5 min");
    const badges = [...document.querySelectorAll('[role="tab"]')].map((t) => [t.firstChild?.textContent, t.querySelector(".bdg")?.textContent, t.querySelector(".bdg")?.className]);
    expect(badges).toEqual([
      ["Alerts", "7", "bdg"],
      ["People", "2", "bdg mut"],
      ["Checks", undefined, undefined],
      ["Doctor", "1", "bdg mut"],
    ]);
  });

  it("says how many are critical", async () => {
    await draw("alerts.crit");
    expect(document.querySelector(".sb-h p")?.textContent).toBe("5 problems · 1 critical · checks every 5 min");
  });

  it("says when there is nothing, no data, no nodes or the first round", async () => {
    await draw("alerts.calm");
    expect(document.querySelector(".sb-h p")?.textContent).toBe("No problems · checks every 5 min");
    act(() => root?.unmount());
    host?.remove();
    await draw("alerts.first");
    expect(document.querySelector(".sb-h p")?.textContent).toBe("The first round of checks is running");
    expect(document.querySelector(".rc-nt .rw-gl.busy")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await draw("alerts.nonodes");
    expect(document.querySelector(".sb-h p")?.textContent).toBe("No nodes yet");
    expect(button("Add a node") ?? [...document.querySelectorAll("a")].find((a) => a.textContent === "Open nodes")).toBeTruthy();
  });
});

describe("the open alerts", () => {
  it("opens one row at a time, the first by itself, and a second press shuts it", async () => {
    await draw("alerts");
    expect(toggles().map((t) => t.getAttribute("aria-expanded"))).toEqual(["true", "false", "false", "false", "false"]);
    await click(toggles()[2]);
    expect(toggles().map((t) => t.getAttribute("aria-expanded"))).toEqual(["false", "false", "true", "false", "false"]);
    expect(document.querySelectorAll(".hl-al.open")).toHaveLength(1);
    await click(toggles()[2]);
    expect(document.querySelectorAll(".hl-al.open")).toHaveLength(0);
  });

  it("picks one node in By node, lists only its alerts, and lets go on a second press or on «Show all»", async () => {
    await draw("alerts");
    expect(sideNode("de1")?.textContent).toContain("1 alert");
    expect(sideNode("nl1")?.textContent).toContain("3 alerts");
    await click(sideNode("fi1"));
    expect(sideNode("fi1")?.getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelectorAll(".hl-al")).toHaveLength(2);
    expect(document.querySelector(".pp-sh .hl-cn")?.textContent).toBe("2");
    await click(sideNode("fi1"));
    expect(document.querySelectorAll(".hl-al")).toHaveLength(5);
    act(() => root?.unmount());
    host?.remove();
    await draw("alerts.node.msk1.mskclean");
    expect(document.querySelector(".pv-nf")?.textContent).toContain("No open alerts on this node");
    await click(button("Show all"));
    expect(document.querySelectorAll(".hl-al").length).toBeGreaterThan(0);
  });

  it("closes the alerts shown after a window, and Undo opens them again", async () => {
    await draw("alerts");
    const dismiss = vi.fn(async () => ({ alert: {} }));
    (healthApi as unknown as Record<string, unknown>).dismissAlert = dismiss;
    await click(button("Close all shown"));
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("Close 7 alerts?");
    expect(dismiss).not.toHaveBeenCalled();
    await click(button("Close", document.querySelector("[role=dialog]")!));
    expect(calls(dismiss).map((c) => (c[0] as { reopen: boolean }).reopen)).toEqual(Array(7).fill(false));
    expect(body()).toContain("7 alerts closed");
    await click(button("Undo"));
    expect(calls(dismiss).filter((c) => (c[0] as { reopen: boolean }).reopen)).toHaveLength(7);
  });

  it("closes the shown ones of a node only, when the list is filtered", async () => {
    await draw("alerts.node.fi1");
    const dismiss = vi.fn(async () => ({ alert: {} }));
    (healthApi as unknown as Record<string, unknown>).dismissAlert = dismiss;
    await click(button("Close all shown"));
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("Close 2 alerts?");
  });

  it("runs a fix from the question to done: the window, the line that runs in place of the buttons, the wave", async () => {
    await draw("alerts");
    const apply = vi.fn(async (req: { dryRun: boolean }) => (req.dryRun ? { planId: "p1", plan: { fixId: "journald_vacuum", titleKey: "health.fix.journald_vacuum.plan", params: { before_mb: "3870", target_mb: "500" }, detail: "", disruptive: false } } : { applied: true, resultParams: {} }));
    (healthApi as unknown as Record<string, unknown>).applyFix = apply;
    await click(button("Trim the journal"));
    const dlg = document.querySelector("[role=dialog]")!;
    expect(dlg.textContent).toContain("Trim the journal on de1");
    expect(dlg.textContent).toContain("Shrink the system journal from 3870 MB to 500 MB");
    expect(calls(apply)[0]![0]).toMatchObject({ dryRun: true, fixId: "journald_vacuum" });
    await click(button("Apply", dlg));
    expect(calls(apply)[1]![0]).toMatchObject({ dryRun: false, planId: "p1" });
    // done: the row says so (and the alert goes with the next report), and the wave leaves the row
    expect(document.querySelector(".hl-run.ok")?.textContent).toBe("Fixed");
    expect(button("Trim the journal")).toBeUndefined();
    expect(wave).toHaveBeenCalledTimes(1);
  });

  it("says in the row that a fix did not work, with the reason and «Try again», which asks the window again", async () => {
    await draw("alerts");
    const apply = vi.fn(async (req: { dryRun: boolean }) => (req.dryRun ? { planId: "p1", plan: { fixId: "journald_vacuum", titleKey: "health.fix.journald_vacuum.plan", params: { before_mb: "3870", target_mb: "500" }, detail: "", disruptive: false } } : { applied: false, error: "busy" }));
    (healthApi as unknown as Record<string, unknown>).applyFix = apply;
    await click(button("Trim the journal"));
    await click(button("Apply", document.querySelector("[role=dialog]")!));
    const box = document.querySelector(".hl-fail")!;
    expect(box.getAttribute("role")).toBe("alert");
    expect(box.textContent).toContain("Did not work");
    expect(box.textContent).toContain("Fix failed");
    expect(wave).not.toHaveBeenCalled();
    // no toast says it twice
    expect(document.querySelector("[data-toast-viewport]")?.textContent ?? "").not.toContain("Fix failed");
    await click(button("Retry", box));
    expect(calls(apply).filter((c) => (c[0] as { dryRun: boolean }).dryRun)).toHaveLength(2);
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
  });

  it("opens the row a link points at, marks it, and brings it into view", async () => {
    const into = vi.fn();
    Element.prototype.scrollIntoView = into;
    history.replaceState(null, "", "#alert-x");
    await draw("alerts");
    const row = document.getElementById("alert-x")!;
    expect(row.className).toContain("open");
    expect(row.className).toContain("hit");
    expect(toggles().filter((t) => t.getAttribute("aria-expanded") === "true")).toHaveLength(1);
    expect(into).toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();
    // a port of the card of three: the card opens
    history.replaceState(null, "", "#alert-u3");
    await draw("alerts");
    expect(document.getElementById("alert-u1")?.className).toContain("open");
  });

  it("opens a muted alert's anchor with its fold", async () => {
    history.replaceState(null, "", "#alert-mu1");
    await draw("alerts.muted");
    expect(document.querySelector("[data-muted-toggle]")?.getAttribute("aria-expanded")).toBe("true");
    expect(document.getElementById("alert-mu1")?.className).toContain("open");
  });
});

describe("the roles", () => {
  it("lets an observer read: no fix, no close, no mute, and the row says who may", async () => {
    await draw("alerts", Role.READONLY);
    expect(button("Trim the journal")).toBeUndefined();
    expect(button("Close all shown")).toBeUndefined();
    expect(button("Close")).toBeUndefined();
    expect(button("Mute")).toBeUndefined();
    expect(document.querySelector(".hl-ro")?.textContent).toBe("Only the owner can fix and close alerts");
    // the way to the node stays
    expect([...document.querySelectorAll("a")].some((a) => a.textContent === "Open node")).toBe(true);
  });

  it("gives a helper the closing and the muting, not the fix", async () => {
    await draw("alerts", Role.HELPER);
    expect(button("Trim the journal")).toBeUndefined();
    expect(button("Close")).toBeDefined();
    expect(button("Close all shown")).toBeDefined();
    expect(button("This is normal for this node")).toBeDefined();
    expect(document.querySelector(".hl-ro")).toBeNull();
  });
});

describe("the Doctor tab", () => {
  it("walks the nodes while «Run again» is on, the line over the sum fills, and ends when the panel has answered", async () => {
    let answer: (v: unknown) => void = () => {};
    await draw("doctor", Role.OWNER, () => {
      (healthApi as unknown as Record<string, unknown>).runDoctor = () => new Promise((r) => (answer = r));
    });
    vi.useFakeTimers();
    try {
      const go = button("Run again")!;
      await act(async () => void go.dispatchEvent(new MouseEvent("click", { bubbles: true })));
      await act(async () => void vi.advanceTimersByTime(0)); // the cache tells its watchers on a timer
      expect(go.textContent).toBe("Checking");
      expect(go.getAttribute("aria-disabled")).toBe("true");
      const nodes = () => [...document.querySelectorAll(".hl-dn > div")];
      expect(nodes()[0]!.className).toBe("cking");
      expect(nodes()[0]!.textContent).toContain("checking…");
      expect(nodes()[1]!.textContent).toContain("queued");
      expect(document.querySelector<HTMLElement>(".hl-dsum .rw-pg")!.style.getPropertyValue("--w")).toBe("0%");
      await act(async () => void vi.advanceTimersByTime(700));
      expect(nodes()[1]!.className).toBe("cking");
      expect(document.querySelector<HTMLElement>(".hl-dsum .rw-pg")!.style.getPropertyValue("--w")).toBe("33%");
      await act(async () => void answer(JSON.parse("{\"nowUnix\":1,\"nodes\":[]}")));
      await act(async () => void vi.advanceTimersByTime(0));
    } finally {
      vi.useRealTimers();
    }
    await settle();
    expect(button("Run again")).toBeDefined();
    expect(document.querySelector(".hl-dn .cking")).toBeNull();
  });

  it("shows the finding with its fix and the line that runs while it works", async () => {
    await draw("doctor.busy");
    expect(document.querySelector(".hl-find .hl-run")?.textContent).toBe("Fixing…");
    expect(button("Trim the journal")).toBeUndefined();
    expect(document.querySelector(".hl-find")?.className).toContain("busy");
  });

  it("says why a fix of the finding did not work, in its card", async () => {
    await draw("doctor.fixfail");
    expect(document.querySelector(".hl-find .hl-fail")?.textContent).toContain("Did not work");
  });
});

describe("the People tab", () => {
  it("says that nothing is open, with the card for the assistant under it", async () => {
    await draw("people.calm");
    expect(body()).toContain("No open alerts about people");
  });
});
