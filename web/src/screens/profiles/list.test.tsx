import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ProfilesScreen } from "./list";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: {
    listProfiles: () =>
      Promise.resolve({
        profiles: [
          { id: "p_run", name: "runs", protocol: "hysteria2", nodeCount: 2, userCount: 5, summary: "", warnings: [] },
          { id: "p_nowhere", name: "nowhere", protocol: "hysteria2", nodeCount: 0, userCount: 5, summary: "", warnings: [] },
          { id: "p_lonely", name: "lonely", protocol: "awg", nodeCount: 1, userCount: 0, summary: "", warnings: [{ code: "inbound_failed", params: { node: "de1", inbound: "inb_1", error: "x" } }] },
        ],
      }),
    listProtocols: () => Promise.resolve({ protocols: [] }),
  },
  groups: { listGroups: () => Promise.resolve({ groups: [{ id: "g", name: "Все", profileIds: ["p_run", "p_nowhere"], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 }] }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
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
});

describe("the profiles list", () => {
  it("marks a profile that runs nowhere or is in no group, and names the node a profile did not start on", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><ProfilesScreen /></QueryClientProvider>));
    for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    const card = (name: string) => [...document.querySelectorAll("a")].find((a) => a.textContent?.startsWith(name))!.textContent!;
    expect(card("runs")).not.toContain("Runs nowhere");
    expect(card("runs")).not.toContain("In no group");
    expect(card("nowhere")).toContain("Runs nowhere");
    expect(card("nowhere")).not.toContain("In no group");
    expect(card("lonely")).toContain("In no group");
    expect(card("lonely")).toContain("Did not start on de1");
  });
});
