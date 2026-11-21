import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { demoOf, frames, seeded } from "./dev-kit-profile-new";
import { ProfileNewScreen } from "./profiles/wizard";

// The scenes of /dev-kit?only=pn-profile-new: every state a scene names must draw, in every language, on the made-up data of the board.

// the scene's own calls are put on these objects by `seeded`
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<object>()), basepath: "/", auth: { me: () => Promise.resolve({}) }, profiles: {}, groups: {}, awg: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
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
const role = (s: string) => (s === "helper" ? Role.HELPER : s === "readonly" ? Role.READONLY : Role.OWNER);

async function draw(state: string) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role(state));
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfileNewScreen demo={demoOf[state] ?? {}} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 10; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const text = () => document.body.textContent ?? "";
const nodeRows = () => document.querySelectorAll(".pw-nodes .pw-pn");

describe("the scenes of the new-profile wizard", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(text()).not.toContain("undefined");
    expect(text()).not.toContain("wiz.");
    if (state === "loading" || state === "noschema" || state === "helper" || state === "readonly") return;
    expect(document.querySelector("h1")?.textContent).toBe("New profile");
    expect(document.querySelectorAll(".pw-steps li")).toHaveLength(5);
    if (state.startsWith("t-")) expect(document.querySelectorAll("[role=radiogroup] [role=radio]")).toHaveLength(state === "t-few" ? 3 : 6);
    if (state === "t-fast") expect(document.querySelector("[role=radio][aria-checked=true]")?.textContent).toContain("Fast, for most people");
    if (state.startsWith("s2-")) expect(document.querySelector("input[data-w=name]")).not.toBeNull();
    if (state.startsWith("s3-") && state !== "s3-none") expect(nodeRows()).toHaveLength(12);
    if (state === "s3-none") expect(text()).toContain("There are no nodes yet");
    if (state.startsWith("s4") && state !== "s4-nogroup") expect(document.querySelectorAll(".pw-gr")).toHaveLength(2);
    if (state.startsWith("s5-") && state !== "s5-bad") expect(document.querySelectorAll(".pw-apr").length).toBeGreaterThan(1);
  });

  it("draws the board's states of the nodes: a domain, a port, a lossy port, an old agent, an entrance, a node that is not on line", async () => {
    await draw("s3-fast");
    const row = (n: string) => [...nodeRows()].find((r) => r.querySelector(".pw-pnh b")?.textContent === n)!;
    expect(row("fi3").textContent).toContain("203.0.113.32");
    expect(row("fi3").querySelector("input[aria-label='Domain (SNI)']")).not.toBeNull();
    expect(row("fi3").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("false");
    expect(row("nl1").textContent).toContain("8443");
    expect((row("nl1").querySelector("input[aria-label='Port on the node']") as HTMLInputElement).value).toBe("8443");
    expect(row("nl2").textContent).toContain("2053");
    expect(row("spb1").textContent).toContain("Open updates");
    expect(row("msk1").textContent).toContain("Entry");
    expect(row("msk1").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("false");
    expect(row("nl3").textContent).toContain("Offline");
    expect(row("de1").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
    expect(document.querySelector(".pw-all2 [role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("draws the steps in %s", async (lang) => {
    setLang(lang);
    for (const s of ["t-fast", "s2-all", "s3-fast", "s4", "s5-fail"]) {
      await draw(s);
      expect(text()).not.toContain("undefined");
      expect(text()).not.toContain("wiz.");
      act(() => root?.unmount());
      host?.remove();
      document.body.innerHTML = "";
    }
  });

  it("gives a helper and an observer the reason, not the wizard", async () => {
    await draw("helper");
    expect(text()).toContain("Only the owner can change profiles");
    expect(document.querySelector(".pw-steps")).toBeNull();
  });
});
