import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { frames, pageDataOf, partsOf, sceneOf, seeded } from "./dev-kit-profile";
import { tabFrames, tabsData } from "./dev-kit-profile-tabs";
import { ProfilePage } from "./profiles/page";

// The scenes of /dev-kit?only=pn-profile that are the page's own (the head, the verdict, the Settings tab, the window "Save changes?"): every
// state a scene names must draw, in every language, and say what it is made to say. The tabs' and windows' scenes are tested beside theirs.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, profiles: {}, groups: {}, users: {}, nodes: {}, health: {}, relays: {} }));
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

const own = new Set(tabFrames.map((f) => f.id));
const states = [...new Set(frames.filter((f) => !own.has(f.id)).map((f) => f.state))];

async function draw(state: string, role = Role.OWNER) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfilePage data={tabsData(pageDataOf(partsOf(state).profile), state)} scene={sceneOf(state)} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 10; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const body = () => document.body.textContent ?? "";

describe("the scenes of the profile page's Settings tab", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(document.querySelector("h1")?.textContent).toBeTruthy();
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(pf|ps)\.[a-z]/);
    if (state === "hy1.settings") {
      expect(body()).toContain("Did not start on fi1");
      expect(document.querySelectorAll(".pp-sec").length).toBeGreaterThan(2);
    }
    if (state === "hy1.settings.dirty") expect(document.querySelector(".pf-save.on")?.textContent).toContain("Changed 1");
    if (state === "hy1.settings.impact") expect(document.querySelector("[data-shot=impact]")?.textContent).toContain("Where");
    if (state === "hy1.settings.errors") {
      expect(document.querySelector(".pp-rmc")?.textContent).toContain("cannot be saved");
      expect(document.querySelector(".pf-save button:last-child")?.getAttribute("aria-disabled")).toBe("true");
    }
    if (state === "hy1.settings.saved") expect(document.querySelector(".pf-save.ok")?.textContent).toContain("Saved");
    if (state === "hy1.settings.stale") expect(body()).toContain("The profile was changed elsewhere");
    if (state === "hy3.settings") expect(body()).toContain("Not on any node");
    if (state === "vl3.settings") expect(body()).toContain("In no group");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the page and its window draw in %s", async (lang) => {
    setLang(lang);
    await draw("hy1.settings.impact");
    expect(document.querySelector("[data-shot=impact]")).not.toBeNull();
    expect(body()).not.toContain("undefined");
  });

  it("gives an observer the form to read: no bar, no Delete, the rows out of reach", async () => {
    await draw("hy1.settings", Role.READONLY);
    expect(document.querySelector(".pf-save")).toBeNull();
    expect(body()).not.toContain("Danger zone");
    expect(document.querySelector(".pp-form[inert]")).not.toBeNull();
  });
});
