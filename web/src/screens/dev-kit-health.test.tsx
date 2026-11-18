import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { frames, SceneBody, seeded } from "./dev-kit-health";

// The scenes of /dev-kit?only=pn-health: every state a scene names must draw, in every language, and say what it is made to say.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, health: {}, nodes: {}, fleet: {}, users: {}, assistant: {}, profiles: {}, groups: {}, relays: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to: _t, render: _r, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string; render?: unknown }) => (
    <a className={className} {...rest}>
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
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
  setLang("en");
  document.body.innerHTML = "";
});

const states = [...new Set(frames.map((f) => f.state))];

async function draw(state: string, role = Role.OWNER) {
  setEffectsEnabled(false, false); // a scene is still unless its address says fx=1 (the pages read it; the tests draw the body)
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
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
  for (let i = 0; i < 12; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const body = () => document.body.textContent ?? "";
const roleOf = (state: string) => (state.split(".").includes("ro") ? Role.READONLY : Role.OWNER);

describe("the scenes of the Health page", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state, roleOf(state));
    expect(document.querySelector("h1")?.textContent).toBe("Health");
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\bhlp?\.[a-z]+\.[a-z]/);
    expect(body()).not.toMatch(/\bhealth\.[a-z]+\.[a-z]/);
    const v = state.split(".");
    if (state === "alerts") {
      expect(document.querySelectorAll(".hl-al")).toHaveLength(5); // the three ports of nl1 are one row
      expect(document.querySelectorAll(".hl-al.open")).toHaveLength(1);
      expect(body()).toContain("Trim the journal");
      expect(document.querySelector(".hl-nodes")?.textContent).toContain("de11 alert");
    }
    if (state === "alerts.calm") expect(body()).toContain("All quiet");
    if (state === "alerts.nonodes") expect(body()).toContain("No nodes yet");
    if (state === "alerts.first") expect(body()).toContain("The first round of checks is running: alerts appear in a few minutes");
    if (state === "alerts.load") expect(document.querySelector(".hl-skl")?.getAttribute("aria-busy")).toBe("true");
    if (state === "alerts.err") expect(document.querySelector(".pv-qe")?.textContent).toContain("Health failed to load");
    if (state === "alerts.crit") expect(document.querySelector(".hl-al.open")?.textContent).toContain("Node is unreachable");
    if (state === "alerts.fixfail") expect(document.querySelector(".hl-fail")?.textContent).toContain("Did not work");
    if (v.includes("ro")) {
      expect(body()).toContain("Only the owner can fix and close alerts");
      expect(body()).not.toContain("Trim the journal");
    }
    if (state === "alerts.dlg") expect(document.querySelector("[role=dialog]")?.textContent).toContain("Shrink the system journal from 3870 MB to 500 MB");
    if (state === "alerts.busy") expect(document.querySelector(".hl-run")?.textContent).toBe("Trimming the journal on de1…");
    if (state === "alerts.closeall") expect(document.querySelector("[role=dialog]")?.textContent).toContain("Close 7 alerts?");
    if (state === "alerts.toast") expect(body()).toContain("Alert closed");
    if (state === "alerts.toastmute") expect(body()).toContain("Undo");
    if (state === "alerts.toastmute") expect(body()).toMatch(/de1: alert muted until /);
    if (state === "alerts.toastmany") expect(body()).toContain("5 alerts closed");
    if (state === "doctor.toastaccept") expect(body()).toContain("System journal size");
    if (state === "alerts.toastfix") expect(body()).toContain("The journal on de1 is trimmed");
    if (state === "alerts.node.fi1") expect(document.querySelectorAll(".hl-al")).toHaveLength(2);
    if (state === "alerts.node.msk1.mskclean") expect(document.querySelector(".pv-nf")?.textContent).toContain("No open alerts on this node");
    if (state === "people") expect(document.querySelectorAll(".hl-al")).toHaveLength(3);
    if (state === "checks") {
      expect(document.querySelectorAll(".hl-cw")).toHaveLength(4);
      expect(document.querySelector(".hl-cw.open .hl-cnode b")?.textContent).toBe("fi1");
    }
    if (state === "checks.first") {
      expect(document.querySelectorAll(".hl-cw.ckw")).toHaveLength(4);
      expect(body()).toContain("queued");
    }
    if (state === "checks") expect(document.querySelector(".hl-cw.open")?.textContent).not.toContain("✕ ");
    if (state === "doctor.first") {
      expect(document.querySelector(".hl-dsum")?.textContent).toContain("Nodes4");
      expect(document.querySelector(".hl-quiet")).toBeNull(); // the list at the side already says "not checked" of each
    }
    if (state === "checks.ckrun") expect(document.querySelector(".hl-cw.cking")).not.toBeNull();
    if (state === "checks.ckdone") expect(document.querySelectorAll(".hl-cw.ckn")).toHaveLength(4);
    if (state === "checks.legend") expect(document.querySelector(".hl-leg")).not.toBeNull();
    if (state === "checks.off") expect(body()).toContain("no link");
    if (state === "doctor") {
      expect(document.querySelectorAll(".hl-find")).toHaveLength(1);
      expect(body()).toContain("Items checked54");
    }
    if (state === "doctor.docbusy") expect(document.querySelector(".hl-dsum .an-sp")).not.toBeNull();
    if (state === "alerts.warpreach.open.wr") expect(document.querySelector(".hl-al.open")?.textContent).toContain("example.com shows a block page through WARP");
    if (state === "alerts.edge") expect(body()).toContain("Not available in the Cloudflare edition");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the page, a window and a table draw in %s", async (lang) => {
    for (const state of ["alerts", "alerts.dlg", "people", "checks", "doctor"]) {
      setLang(lang);
      await draw(state);
      expect(document.querySelector("h1")?.textContent).toBeTruthy();
      expect(body()).not.toContain("undefined");
      expect(body()).not.toMatch(/\bhlp?\.[a-z]+\.[a-z]/);
      act(() => root?.unmount());
      host?.remove();
      root = host = null;
      document.body.innerHTML = "";
    }
  });
});
