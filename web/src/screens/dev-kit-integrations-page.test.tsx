import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpProvider } from "@/components/step-up";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang, type Lang } from "@/i18n";
import { SceneProvider } from "./ai/bits";
import { IntegrationsView } from "./integrations";
import { frames, sceneOf, seeded } from "./dev-kit-integrations-page";

// The scenes of /dev-kit?only=pn-integrations draw the real screen on made-up data: every state a scene names must draw, in every language.

// the scene's own calls are put on this object by `seeded`
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, telegram: {}, nodes: {} }));
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

async function draw(state: string, role = Role.OWNER) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  const { scene, flags } = sceneOf(state);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>
            <SceneProvider value={flags}>
              <IntegrationsView scene={scene} />
            </SceneProvider>
          </StepUpProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function clear() {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
}

const body = () => document.body.textContent ?? "";
const $ = (sel: string) => document.querySelector<HTMLElement>(sel);
const all = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];

/** The frames that are drawn as a role other than the owner. */
const roleOf = (params = "") => (params.includes("role=helper") ? Role.HELPER : params.includes("role=readonly") ? Role.READONLY : Role.OWNER);
const scenes = [...new Map(frames.map((f) => [`${f.state}|${roleOf(f.params)}`, f])).values()];

describe("the scenes of the Integrations screen", () => {
  it.each(scenes.map((f) => [f.id, f] as const))("%s draws", async (_id, f) => {
    await draw(f.state, roleOf(f.params));
    expect($("h1")?.textContent).toBe("Integrations");
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(igp|tg|int)\.[a-z]/);
    if (f.state.split(".").includes("load")) expect($("[aria-busy=true]")).not.toBeNull();
    else if (f.state.split(".").includes("err")) expect($("[role=alert]")?.textContent).toContain("Integrations failed to load");
    else expect($(".ig-hero")).not.toBeNull();
  });

  it("draws every state in every language, with no raw key", async () => {
    for (const lang of ["ru", "zh", "fa", "tr"] as Lang[]) {
      for (const state of ["", "wait", "unlinked", "mute", "tfail", "nobot.filled.ferr", "rep", "dlg-bot", "dlg-un", "warn", "nourl"]) {
        setLang(lang);
        await draw(state);
        expect(body(), `${lang} ${state}`).not.toMatch(/\b(igp|tg|int|common)\.[a-zA-Z]/);
        expect(body()).not.toContain("undefined");
        await clear();
      }
    }
  }, 120_000);
});

describe("what a scene shows", () => {
  it("linked, alerts on: the chain is whole and the verdict says so", async () => {
    await draw("");
    expect($(".ig-chain")?.className).toContain("on");
    expect(all(".ig-w").map((w) => w.className)).toEqual(["ig-w on", "ig-w on"]);
    expect($(".ig-head b")?.textContent).toBe("Alerts come to you in Telegram");
    expect(all(".ig-row .pp-lb b").map((b) => b.textContent)).toEqual(["Bot", "Your chat", "Chat ID", "Alerts"]);
    expect($("[data-testid=tg-bot]")?.textContent).toBe("@example_mistgate_bot");
    expect(all(".ig-adm .ai-rec")).toHaveLength(2);
    expect($(".ig-adm .uq-otg")?.textContent).toBe("that’s you");
    expect(all(".ig-get.on")).toHaveLength(5);
  });

  it("the chat states of the board: not linked, waiting for Start, alerts off, the test did not arrive", async () => {
    await draw("unlinked");
    expect($(".ig-head b")?.textContent).toBe("Chat not linked");
    expect(all(".ig-w").map((w) => w.className)).toEqual(["ig-w on", "ig-w off"]);
    expect(all(".ig-get.on")).toHaveLength(0);
    await clear();
    await draw("wait");
    expect($(".ig-head b")?.textContent).toBe("Press Start in the bot");
    expect($(".ig-w.wait")).not.toBeNull();
    expect($("[data-testid=tg-start]")?.textContent).toBe("/start k3n9p2xq");
    await clear();
    await draw("mute");
    expect($(".ig-head b")?.textContent).toBe("Chat linked, alerts off");
    expect($(".ig-w.mute")).not.toBeNull();
    expect($("[role=switch]")?.getAttribute("aria-checked")).toBe("false");
    await clear();
    await draw("tfail");
    expect($(".ig-head.bad b")?.textContent).toBe("The test didn’t arrive");
    expect($(".ig-w.bad")).not.toBeNull();
  });

  it("the bot states: the form for the owner, one line for an observer", async () => {
    await draw("nobot");
    expect($(".ig-head b")?.textContent).toBe("Bot not connected");
    expect($("input[type=password]")).not.toBeNull();
    expect(body()).toContain("a bot is needed first"); // the chat row is there, without a way to link
    expect($(".ig-me button")).toBeNull();
    await clear();
    await draw("nobot", Role.READONLY);
    expect($("input[type=password]")).toBeNull();
    expect(body()).toContain("Bot not connected. The owner sets it up");
  });

  it("an observer gets the first line of what comes, the others struck out, and no way to the AI screen", async () => {
    await draw("", Role.READONLY);
    expect(all(".ig-get.no")).toHaveLength(4);
    expect($(".ig-ai")).toBeNull();
    expect(body()).toContain("You’re an observer, so you get only node and health alerts");
  });

  it("the owner gets the way to MCP and the tokens", async () => {
    await draw("");
    expect($(".ig-ai")?.textContent).toContain("MCP and API tokens are now in the AI section");
  });
});
