import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { demoOf, frames, sceneSearch, seeded } from "./dev-kit-profiles";
import { ProfilesScreen } from "./profiles/list";

// The scenes of /dev-kit?only=pn-profiles draw the real screen on made-up data: every state a scene names must draw, in every language.

// the scene's own calls are put on these objects by `seeded`
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, profiles: {}, groups: {}, users: {}, nodes: {}, health: {}, relays: {} }));
let search: Record<string, unknown> = {};
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to: _t, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string }) => (
    <a className={className} {...rest}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => search,
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
  search = {};
});

const states = [...new Set(frames.map((f) => f.state))];

async function draw(state: string, role = Role.OWNER) {
  // what the scene's address carries: the view and the filters
  search = Object.fromEntries(new URLSearchParams(sceneSearch(state)));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfilesScreen demo={demoOf(state)} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const rows = () => document.querySelectorAll("[role=listitem]");
const body = () => document.body.textContent ?? "";

describe("the scenes of the Profiles list", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(document.querySelector("h1")?.textContent).toBeTruthy();
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\bpf\.[a-z]/);
    if (state === "list" || state === "out" || state === "nochecks") expect(rows()).toHaveLength(10);
    if (state === "none") expect(body()).toContain("No profiles yet");
    if (state === "load" || state === "where-load") expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    if (state === "err") expect(document.querySelector("[role=alert]")?.textContent).toContain("Profiles did not load");
    if (state === "find") expect(rows()).toHaveLength(3);
    if (state === "nomatch") expect(body()).toContain("Nothing matches these filters");
    if (state === "proto") expect(rows()).toHaveLength(3);
    if (state === "facet-warn") expect(rows()).toHaveLength(2);
    if (state === "facet-none") expect(rows()).toHaveLength(2);
    if (state === "facet-nogroup") expect(rows()).toHaveLength(2);
    if (state === "facet-warp") expect(rows()).toHaveLength(1);
    if (state === "where") expect(document.querySelectorAll(".pv-mr")).toHaveLength(11);
    if (state === "where-ok") expect(document.querySelector("aside.pv-dr")?.textContent).toContain("Main · 443 on de1");
    if (state === "where-bad") expect(document.querySelector("aside.pv-dr")?.textContent).toContain("Did not start on fi1");
    if (state === "where-part") expect(document.querySelector("aside.pv-dr")?.textContent).toContain("2 rounds in a row failed");
    if (state === "where-busy") expect(document.querySelector("aside.pv-dr")?.textContent).toContain("Starting");
    if (state === "where-gone") expect(document.querySelector("aside.pv-dr")?.textContent).toContain("Node out of touch");
    if (state === "where-warp") expect(document.querySelector("aside.pv-dr")?.textContent).toContain("through msk1");
    if (state === "where-nochecks") expect(body()).toContain("no check yet");
  });

  it("marks a node that does not answer in the list and in the matrix", async () => {
    await draw("out");
    const aw2 = [...rows()].find((r) => r.querySelector("b")?.textContent === "AWG 2.0")!;
    expect(aw2.querySelector(".pv-wh")?.textContent).toBe("de1nl3");
    expect(aw2.querySelector(".pv-wh > span:last-child")?.className).toBe("off");
    act(() => root?.unmount());
    host?.remove();
    await draw("where-gone");
    expect(document.querySelector(".pv-nh.off")?.textContent).toContain("nl3");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the list and the matrix with a cell open draw in %s", async (lang) => {
    setLang(lang);
    await draw("where-bad");
    expect(document.querySelectorAll(".pv-mr")).toHaveLength(11);
    expect(document.querySelector("aside.pv-dr")).not.toBeNull();
    expect(body()).not.toContain("undefined");
    act(() => root?.unmount());
    host?.remove();
    await draw("list");
    expect(rows()).toHaveLength(10);
  });

  it("gives a helper and an observer the list with nothing that changes anything", async () => {
    for (const role of [Role.HELPER, Role.READONLY]) {
      await draw("list", role);
      expect([...document.querySelectorAll("button")].some((b) => b.textContent === "New profile")).toBe(false);
      expect(body()).not.toContain("Put on nodes");
      expect(rows()).toHaveLength(10);
      act(() => root?.unmount());
      host?.remove();
    }
  });
});
