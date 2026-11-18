import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpProvider } from "@/components/step-up";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang, type Lang } from "@/i18n";
import { build, frames, parse, seeded } from "./dev-kit-updates";
import { UpdatesView } from "./updates";

// The scenes of /dev-kit?only=pn-updates draw the real screen on made-up data: every state a scene names must draw, in every language.

// the scene's own calls are put on these objects by `seeded`
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, updates: {}, backups: {}, nodes: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to: _t, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string }) => (
    <a className={className} {...rest}>
      {children}
    </a>
  ),
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
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  const built = build(parse(state));
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>
            <UpdatesView scene={built.scene} />
          </StepUpProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const body = () => document.body.textContent ?? "";
const $ = (sel: string) => document.querySelector<HTMLElement>(sel);
const all = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];

describe("the scenes of the Updates screen", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(document.querySelector("h1")?.textContent).toBe("Updates");
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(uq|up|os|osf)\.[a-z]/);
    const { tab, kind, mods } = parse(state);
    expect($("[role=tab][aria-selected=true]")?.id).toBe(`uq-tab-${tab}`);
    if (kind === "load") expect($("[aria-busy=true]")).not.toBeNull();
    else if (kind === "err") expect($("[role=alert]")?.textContent).toContain("Updates failed to load");
    else {
      expect($("[role=tabpanel]")).not.toBeNull();
      if (tab === "panel" && /^run[0-3]$/.test(kind)) {
        // the takeover: the four steps, the one under way and the ticks before it
        const n = Number(kind[3]);
        expect($(".uq-take[role=alertdialog]")).not.toBeNull();
        expect(all(".uq-take .uq-ttrk li")).toHaveLength(4);
        expect(all(".uq-take .uq-ttrk li.ok")).toHaveLength(n);
        expect(all(".uq-take .uq-ttrk li.now")).toHaveLength(1);
        expect($(".uq-now")?.textContent).toContain(`${n + 1} of 4`);
      }
      if (tab === "panel" && kind === "runx") {
        expect($(".uq-take")).not.toBeNull();
        expect($(".uq-take .uq-ttrk")).toBeNull();
        expect($(".uq-now")?.textContent).toContain("Updating the panel to v0.1.86");
      }
      if (tab === "panel" && kind === "slow") expect($(".uq-take .uq-tslow")?.textContent).toContain("Reload the page");
      if (tab === "panel" && kind === "out") expect($(".uq-take.out")?.textContent).toContain("Panel updated");
      if (tab === "panel" && kind === "done") expect($(".uq-hero.done")?.textContent).toContain("updated just now · was v0.1.71");
      if (tab === "panel" && kind === "back") expect($(".uq-hero.back .rc-pb.bad")?.textContent).toContain("The panel is back on v0.1.71");
      if (tab === "panel" && kind === "sig") {
        expect($(".uq-hero.back .rc-pb.bad")?.textContent).toContain("signature didn’t match");
        // the board keeps What is new (and the security pill) while the release is not installable
        expect(all(".uq-hero .pv-pill").some((p) => p.textContent?.includes("Security fixes"))).toBe(true);
      }
      if (tab === "panel" && kind === "gh") expect($(".uq-hero .rc-pb")?.textContent).toContain("GitHub didn’t respond");
      if (tab === "panel" && kind === "cur") expect($(".uq-hero.upto")?.textContent).toContain("Up to date");
      if (state === "pn.new") expect(all(".uq-trk li")).toHaveLength(4);
      if (mods.has("dlg-upd")) expect(document.querySelector("[data-shot=updates-update-dialog]")?.textContent).toContain("Update the panel to v0.1.86");
      if (mods.has("dlg-ch-test")) expect(document.querySelector("[data-shot=updates-channel] [role=radio][aria-checked=true]")?.textContent).toContain("Testing");
      if (tab === "agents" && kind === "fail") expect($(".uq-ag .rc-pb.bad")?.textContent).toContain("fi1");
      if (tab === "agents" && kind === "many") expect(all(".uq-ft")).toHaveLength(12);
      if (tab === "agents" && kind === "none") expect($(".us-none")?.textContent).toContain("No nodes yet");
      if (tab === "agents" && /^roll[0-2]$/.test(kind)) expect($(".uq-now")?.textContent).toMatch(/Canary fi1|Checking fi1|Waiting for the next stage/);
      if (tab === "os") expect(all(".uq-w")).toHaveLength(5);
      if (tab === "os" && kind === "new" && !mods.has("closed")) expect(all(".uq-w.open")).toHaveLength(1);
      if (tab === "os" && kind === "calm") expect($(".uq-osn")?.textContent).toContain("up to date");
      if (tab === "os" && kind === "busy") expect(all(".uq-w.cking")).toHaveLength(1);
      if (mods.has("tick")) expect(all("input[type=checkbox]:checked")).toHaveLength(2);
    }
  });

  it.each(["pn.new", "pn.run1", "pn.back", "ag.new", "ag.fail", "ag.new.dlg-sch", "os.new", "os.fail"])("%s draws in every language", async (state) => {
    for (const lang of ["en", "ru", "zh", "fa", "tr"] as Lang[]) {
      setLang(lang);
      await draw(state);
      expect(document.querySelector("h1")?.textContent).toBeTruthy();
      expect(body()).not.toContain("undefined");
      expect(body()).not.toMatch(/\b(uq|up|os|osf)\.[a-z]/);
      act(() => root?.unmount());
      host?.remove();
      document.body.innerHTML = "";
    }
  });

  it("gives a helper and an observer the same page without the buttons", async () => {
    for (const role of [Role.HELPER, Role.READONLY]) {
      await draw("pn.new", role);
      expect(body()).toContain("Only the owner can update the panel");
      expect(all("button").some((b) => b.textContent === "Update panel")).toBe(false);
      act(() => root?.unmount());
      host?.remove();
      document.body.innerHTML = "";
    }
  });

  it("opens one tab from another: the panel's, the agents' and the servers'", async () => {
    await draw("pn.new");
    const tabs = all("[role=tab]");
    expect(tabs.map((t) => t.textContent)).toEqual(["This panel1", "Node agents1", "Server systems3"]);
    await act(async () => tabs[1]!.click());
    expect($("[role=tab][aria-selected=true]")?.id).toBe("uq-tab-agents");
    expect($(".uq-ag")).not.toBeNull();
    await act(async () => tabs[2]!.click());
    expect($(".uq-os")).not.toBeNull();
  });
});
