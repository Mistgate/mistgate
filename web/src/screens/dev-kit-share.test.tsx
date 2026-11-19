import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { langs, setLang } from "@/i18n";
import { ShareKit } from "./dev-kit-share";

// The scenes of /dev-kit?only=share draw the real dialogs' blocks and the real card on made-up data, in every language: every state the
// kit names must draw, with no placeholder left in its words.

vi.mock("@/lib/api", () => ({
  profiles: {},
  nodes: { getNodeConnections: () => Promise.resolve({ supported: false, inbounds: [] }) },
  groups: { listGroups: () => Promise.resolve({ groups: [] }) },
  health: { getDoctor: () => Promise.resolve({ nowUnix: 1n, nodes: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children?: ReactNode }) => <a>{children}</a> }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setLang("en");
});

describe("the share scenes", () => {
  it.each(langs)("draw every state in %s", async (lang) => {
    setLang(lang);
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<ShareKit />));
    for (const shot of ["share-offer", "share-refusals", "share-notes", "share-add", "share-edit", "share-card", "share-card-trouble", "share-card-states", "share-profiles-tab"]) {
      expect(host.querySelector(`[data-shot=${shot}]`), shot).toBeTruthy();
    }
    expect(host.querySelectorAll("[data-testid=share-offer]")).toHaveLength(2);
    expect(host.querySelectorAll("[data-testid=share-refusal]")).toHaveLength(4);
    expect(host.querySelectorAll("[data-testid^=shared-port-]").length).toBeGreaterThanOrEqual(7);
    expect(host.querySelectorAll("[data-testid=share-notes]")).toHaveLength(4);
    expect(host.textContent).not.toMatch(/\{\w+\}/);
  });
});
