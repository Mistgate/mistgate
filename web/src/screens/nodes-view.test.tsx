import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { setLang } from "@/i18n";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { NodesScreen } from "./nodes";

// The List | Map switch of the Nodes page: the URL's ?view=map, else what this browser used last; the list on a phone.

const ctl = vi.hoisted(() => ({ search: {} as { view?: "map"; focus?: string }, navigate: vi.fn() }));
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) } }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ctl.search,
  useNavigate: () => ctl.navigate,
}));
vi.mock("@/components/add-node", () => ({ useAddNode: () => vi.fn() }));
vi.mock("./nodes-map", () => ({ FleetMap: ({ focus }: { focus?: string }) => <div data-testid="fleet-map">{focus ?? "-"}</div> }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => setLang("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  ctl.search = {};
  ctl.navigate.mockReset();
  localStorage.clear();
  delete (window as { matchMedia?: unknown }).matchMedia;
});

const node = (id: string, name: string) => ({ id, name, countryCode: "DE", location: "", provider: "", address: `${name}.example.com`, status: NodeStatus.ONLINE, protocols: [], online: [], trafficTodayBytes: 0, hasMetrics: false, cpuPct: 0, uptimeS: 0, agentVersion: "0.3.1" });
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER } } as never);
  qc.setQueryData(updatesQuery.queryKey, { nodes: [] } as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes: [node("nod_de1", "de1"), node("nod_nl1", "nl1")], expectedAgentVersion: "0.3.1" } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodesScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const radio = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === label)!;
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("the List | Map switch", () => {
  it("shows the list by default, the map for ?view=map, and remembers the choice in this browser", async () => {
    await mount();
    expect(document.querySelector("[data-testid=fleet-map]")).toBeNull();
    await click(radio("Map"));
    expect(localStorage.getItem("nodes.view")).toBe("map");
    expect(ctl.navigate).toHaveBeenLastCalledWith(expect.objectContaining({ to: "/nodes", search: { view: "map" } }));

    act(() => root!.unmount());
    host!.remove();
    ctl.search = { view: "map", focus: "nod_de1" };
    await mount();
    expect(document.querySelector("[data-testid=fleet-map]")?.textContent).toBe("nod_de1");

    act(() => root!.unmount());
    host!.remove();
    ctl.search = {};
    await mount();
    expect(document.querySelector("[data-testid=fleet-map]")).not.toBeNull(); // the URL says nothing: the browser's last choice
    await click(radio("List"));
    expect(localStorage.getItem("nodes.view")).toBe("list");
    expect(ctl.navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: {} }));
  });

  it("is the list only on a phone, with no switch", async () => {
    (window as { matchMedia?: unknown }).matchMedia = () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} });
    ctl.search = { view: "map" };
    await mount();
    expect(document.querySelector("[data-testid=fleet-map]")).toBeNull();
    expect(document.querySelector("[role=radiogroup][aria-label='Nodes view']")).toBeNull();
  });
});
