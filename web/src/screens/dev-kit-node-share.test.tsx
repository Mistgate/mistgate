import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeWindow } from "@/components/add-node";
import { StepUpProvider } from "@/components/step-up";
import { setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { cards, frames, seededWindow, windows, windowSeed } from "./dev-kit-node-share";

// The scenes of /dev-kit?only=node-share draw the real card and the real window on made-up data: every state a frame names must exist,
// and the windows must draw in every language. (The card's states are drawn by node/share.test.tsx.)

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, webauthnSupported: () => false }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children?: ReactNode }) => <a>{children}</a>,
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useParams: () => ({}),
  useLocation: () => ({ pathname: "/", search: "" }),
  useBlocker: () => ({ status: "idle" }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: (query: string) => ({ matches: query.includes("prefers-reduced-motion"), media: query, addEventListener() {}, removeEventListener() {} }),
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setLang("en");
});

async function draw(state: string) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={seededWindow(state)}>
        <StepUpProvider>
          <AddNodeWindow open initialMode={windows[state]!.mode} onClose={() => {}} seed={windowSeed(state)} />
        </StepUpProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

describe("the scenes of who gets a node", () => {
  it("every frame names a state that exists", () => {
    for (const f of frames) expect(f.state in cards || f.state in windows, f.id).toBe(true);
    expect(new Set(frames.map((f) => f.id)).size).toBe(frames.length);
  });

  it.each(Object.keys(windows))("%s draws", async (state) => {
    await draw(state);
    const body = document.body.textContent ?? "";
    expect(body).not.toContain("undefined");
    if (state === "win-cmd-first") {
      expect(body).toContain(en["ns.pick.first"]);
      return;
    }
    expect(body).toContain(en["ns.pick.title"]);
    if (state === "win-cmd-pick") expect(body).toContain(en["ns.pick.pickOne"]);
    if (state === "win-cmd-listed") expect(document.querySelectorAll("[role=group] [role=checkbox][aria-checked=true]").length).toBe(2);
    if (state === "win-cmd-load") expect(body).toContain(en["common.loading"]);
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the window draws in %s with no key left bare", async (lang) => {
    for (const state of ["win-cmd-listed", "win-ssh-none", "win-cmd-first"]) {
      setLang(lang);
      await draw(state);
      expect(document.body.textContent, `${lang} ${state}`).not.toMatch(/\bns\.|undefined/);
      act(() => root?.unmount());
      host?.remove();
      root = host = null;
    }
  });
});
