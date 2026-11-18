import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpProvider } from "@/components/step-up";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang, type Lang } from "@/i18n";
import { AiView } from "./ai";
import { SceneProvider } from "./ai/bits";
import { frames, parse, sceneOf, seeded } from "./dev-kit-ai-page";

// The scenes of /dev-kit?only=pn-ai draw the real screen on made-up data: every state a scene names must draw, in every language.

// the scene's own calls are put on these objects by `seeded`
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, assistant: {}, tasks: {}, apiTokens: {}, approvals: {}, provisioning: {}, nodes: {} }));
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
  const scene = sceneOf(state);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>
            <SceneProvider value={scene.flags}>
              <AiView scene={{ tab: scene.tab }} />
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

describe("the scenes of the AI screen", () => {
  it.each(scenes.map((f) => [f.id, f] as const))("%s draws", async (_id, f) => {
    await draw(f.state, roleOf(f.params));
    expect(document.querySelector("h1")?.textContent).toBe("AI");
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\baip\.[a-z]/);
    const { tab, mods } = parse(f.state);
    const open = $("[role=tab][aria-selected=true]")?.id;
    expect(open).toBe(`aip-tab-${{ as: "assistant", mem: "memory", tk: "tasks", mcp: "mcp", ap: "approvals" }[tab]}`);
    const role = roleOf(f.params);
    const owner = role === Role.OWNER;
    if (mods.has("load") && owner) expect($("[aria-busy=true]")).not.toBeNull();
    else if (mods.has("err") && owner) expect($("[role=alert]")?.textContent).toContain("The AI section failed to load");
    else if (mods.has("edge")) expect(body()).toContain("Not available in the Cloudflare edition yet");
    else if (!owner && (tab === "as" || tab === "mcp" || tab === "ap")) expect($("[role=tabpanel]")?.textContent).toBeTruthy();
    else expect($("[role=tabpanel]")).not.toBeNull();
  });

  it("draws every tab in every language, with no raw key", async () => {
    for (const lang of ["ru", "zh", "fa", "tr"] as Lang[]) {
      for (const state of ["as", "as.dlg-connect-zai", "mem", "tk.runs", "tk.dlg-task", "mcp", "mcp.dlg-tok.op", "mcp.dlg-secret", "ap", "ap.exp"]) {
        setLang(lang);
        await draw(state);
        expect(body(), `${lang} ${state}`).not.toMatch(/\b(aip|ai|task|int|common)\.[a-zA-Z]/);
        expect(body()).not.toContain("undefined");
        await clear();
      }
    }
  }, 120_000);
});

describe("what a scene shows", () => {
  it("the assistant: the provider in use is open, the others are rows with their state", async () => {
    await draw("as");
    expect(all(".pp-sh h3").map((h) => h.textContent)).toEqual(["Assistant", "Access and history", "Providers", "This month"]);
    expect(all(".ai-prov")).toHaveLength(5);
    expect($(".ai-prov.on.open")?.textContent).toContain("Anthropic");
    expect($(".ai-prov.on.open")?.textContent).toContain("$3.41 of $20");
    expect($(".ai-prov.off")?.textContent).toContain("Not connected");
    expect(body()).toContain("owner");
    // the board starts on "Owner and helpers" for who may use it
    expect(all("[role=radiogroup] [role=radio][aria-checked=true]").map((r) => r.textContent)).toContain("Owner and helpers");
  });

  it("the assistant: the three trouble banners are the board's", async () => {
    await draw("as.bud");
    expect($(".rc-pb.bad")?.textContent).toContain("Monthly budget used up");
    expect(all(".rc-pb button").map((b) => b.textContent)).toEqual(["Raise to $40"]);
    await clear();
    await draw("as.key");
    expect($(".rc-pb.bad")?.textContent).toContain("Anthropic did not accept the key");
    expect($(".ai-prov.bad")?.textContent).toContain("Key rejected");
    await clear();
    await draw("as.noprov");
    expect($(".ai-np")?.textContent).toContain("No provider connected");
    expect($(".ai-body")).not.toBeNull();
    await clear();
    await draw("as.off");
    expect($(".ai-body.off")).not.toBeNull();
    expect(body()).toContain("Assistant is off");
  });

  it("memory: notes with the assistant's mark, conversations with theirs", async () => {
    await draw("mem");
    expect(all(".ai-note")).toHaveLength(3);
    expect(all(".ai-note i.uq-otg")).toHaveLength(2);
    expect(all(".ai-conv")).toHaveLength(4);
    expect(all(".ai-conv i.uq-otg").map((i) => i.textContent)).toEqual(["Scheduled"]);
    await clear();
    await draw("mem.empty");
    expect(body()).toContain("No notes");
    expect(body()).toContain("No conversations");
  });

  it("tasks: each task says what it is, when it runs, how its last run ended", async () => {
    await draw("tk");
    expect(all(".ai-task")).toHaveLength(3);
    expect($(".ai-task .ai-find")?.textContent).toContain("de1: journald_size");
    expect($("[role=tab] .bdg")?.textContent).toBe("1"); // a number on the tab: the tasks that need a look
    await clear();
    await draw("tk.fail.runs");
    expect(all(".ai-task")[2]?.querySelector(".ai-find.bad")?.textContent).toContain("did not answer");
    expect(all(".ai-run")).toHaveLength(2);
    await clear();
    await draw("tk.empty");
    expect(body()).toContain("No scheduled tasks");
  });

  it("approvals: the card counts down, goes amber under a minute, and is replaced by the board's answer when it lapses", async () => {
    await draw("ap");
    expect($(".ai-ap .ai-left b")?.textContent).toMatch(/^[67]:\d\d$/);
    expect($(".ai-left.soon")).toBeNull();
    expect($(".ai-ap .hl-cnode")?.textContent).toBe("de1"); // a node of the panel is drawn with its flag
    expect(all(".ai-rec")).toHaveLength(5);
    expect($("[role=tab] .bdg")?.textContent).toBe("1");
    await clear();
    await draw("ap.soon");
    expect($(".ai-left.soon")).not.toBeNull();
    expect($(".ai-apg.soon")).not.toBeNull();
    await clear();
    await draw("ap.exp");
    expect($(".ai-ap")).toBeNull();
    expect(body()).toContain("Request expired");
    await clear();
    await draw("ap.none");
    expect(body()).toContain("Nothing waiting");
    await clear();
    await draw("ap.done");
    expect($(".ai-ap")).toBeNull();
    expect(all(".ai-rec")).toHaveLength(6);
    expect($(".ai-rec.fresh")?.textContent).toContain("Done");
    await clear();
    await draw("ap.denied");
    expect($(".ai-rec.fresh")?.textContent).toContain("Rejected");
  });

  it("the summary under the title follows what waits", async () => {
    await draw("ap");
    expect($(".sb-h p")?.textContent).toBe("1 change is waiting for your decision");
    await clear();
    await draw("as.none");
    expect($(".sb-h p")?.textContent).toBe("Assistant is on · nothing awaits a decision");
    await clear();
    await draw("as.off.none");
    expect($(".sb-h p")?.textContent).toBe("Assistant is off");
  });
});
