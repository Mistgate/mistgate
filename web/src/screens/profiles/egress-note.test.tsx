import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { EgressNote } from "./egress-note";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  nodes: {
    listNodes: () =>
      Promise.resolve({
        nodes: [
          { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", warp: { state: WarpState.UP } },
          { id: "nod_2", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI", warp: { state: WarpState.NOT_CONFIGURED } },
        ],
      }),
  },
}));
// no router in the test: a link is an anchor that carries its target
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, hash, className }: { children?: ReactNode; to: string; params?: object; search?: object; hash?: string; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} data-hash={hash ?? ""} className={className}>
      {children}
    </a>
  ),
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

describe("the note under «Exit: WARP»", () => {
  it("links every node without WARP to its WARP card", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <EgressNote nodeIds={["nod_1", "nod_2"]} />
        </QueryClientProvider>,
      ),
    );
    for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(document.body.textContent).toBe("No working WARP on: fi1. The profile does not start there until it has.");
    const link = host.querySelector<HTMLAnchorElement>("a")!;
    expect([link.textContent, link.getAttribute("href"), link.dataset.params, link.dataset.search, link.dataset.hash]).toEqual(["fi1", "/nodes/$id", JSON.stringify({ id: "nod_2" }), JSON.stringify({ tab: "exit" }), "warp"]);
  });
});
