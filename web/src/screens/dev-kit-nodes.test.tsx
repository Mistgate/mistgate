import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { writePref } from "@/lib/storage";
import { demoOf, frames, seeded } from "./dev-kit-nodes";
import { NodesScreen } from "./nodes";

// The scenes of /dev-kit?only=nodes draw the real screen on made-up data: every state a scene names must draw, in every language.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) } }));
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
});

const states = [...new Set(frames.map((f) => f.state))].filter((s) => !s.startsWith("job-"));

async function draw(state: string) {
  writePref("nodes.view", state === "load" ? "load" : state === "map" || state === "map-card" ? "map" : "list");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, Role.OWNER, state === "edge");
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <NodesScreen demo={demoOf[state]} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

describe("the scenes of the Nodes screen", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(host!.querySelector("h1")?.textContent).toBeTruthy();
    expect(host!.textContent).not.toContain("undefined");
    if (state === "first") expect(host!.querySelectorAll("ol li")).toHaveLength(4);
    if (state === "twelve" || state === "mixed") expect(host!.querySelectorAll("section[data-select] a.absolute")).toHaveLength(12);
    if (state === "select") expect(host!.ownerDocument.querySelector("[data-nodes-bar]")?.textContent).toContain("2 nodes selected");
    if (state === "load") expect(host!.querySelectorAll("button[aria-expanded]").length).toBe(4);
    if (state === "map-card") expect(host!.ownerDocument.querySelector("aside")?.textContent).toContain("nl1");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the list draws in %s", async (lang) => {
    setLang(lang);
    await draw("mixed");
    expect(host!.querySelectorAll("section[data-select] a.absolute")).toHaveLength(12);
  });
});
