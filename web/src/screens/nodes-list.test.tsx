import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { en } from "@/i18n/en";
import { setLang } from "@/i18n";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { NodesScreen } from "./nodes";

// The list of the Nodes screen as the board draws it: problems first, columns that sort, a search, the state chips, the filters,
// the empty states (no node yet; a filter that hides all), the check column of the selecting mode.

const addNode = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) } }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className, title, tabIndex, onClick }: { children?: ReactNode; to: string; params?: { id: string }; className?: string; title?: string; tabIndex?: number; onClick?: (e: unknown) => void }) => (
    <a href={to.replace("$id", params?.id ?? "")} className={className} title={title} tabIndex={tabIndex} onClick={onClick}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
}));
vi.mock("@/components/add-node", () => ({ useAddNode: () => addNode }));

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
  addNode.mockReset();
  localStorage.clear();
});

const node = (name: string, over: Record<string, unknown> = {}) => ({
  id: `nod_${name}`,
  name,
  countryCode: "DE",
  location: "Frankfurt",
  provider: "Example Cloud",
  notes: "",
  address: `203.0.113.${name.length}`,
  status: NodeStatus.ONLINE,
  protocols: ["hysteria2", "vless"],
  online: [{ protocol: "hysteria2", users: name.length }],
  trafficTodayBytes: 1e9,
  hasMetrics: true,
  cpuPct: 12,
  uptimeS: 86400 * 3,
  agentVersion: "0.3.1",
  ...over,
});
// order from the panel: de1 works, nl1 does not answer, fi1 wants a look
const fleet = [node("de1", { online: [{ protocol: "hysteria2", users: 5 }] }), node("nl1", { online: [], status: NodeStatus.DOWN, reason: { code: "agent_silent", params: { minutes: "14" } }, countryCode: "NL", location: "Amsterdam" }), node("fi1", { online: [{ protocol: "vless", users: 2 }], reason: { code: "state_drift", params: {} }, countryCode: "FI", location: "Helsinki", protocols: ["vless"] })];

async function mount(nodes: ReturnType<typeof node>[] = fleet, role = Role.OWNER) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
  qc.setQueryData(updatesQuery.queryKey, { nodes: [{ nodeId: "nod_de1", state: NodeUpdateState.UP_TO_DATE }] } as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes, expectedAgentVersion: "0.3.1" } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodesScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const names = () => [...document.querySelectorAll("section[data-select] b.font-mono")].map((b) => b.textContent);
const head = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=columnheader]")].find((h) => h.textContent === label)!;
const typeInto = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const search = () => document.querySelector<HTMLInputElement>(`input[aria-label="${en["nv.searchLabel"]}"]`)!;
const radio = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent?.startsWith(label))!;

describe("the list", () => {
  it("puts the nodes that need a look first, then the rest as the panel sent them", async () => {
    await mount();
    expect(names()).toEqual(["nl1", "fi1", "de1"]);
    expect(head(en["nv.col.state"]).getAttribute("aria-sort")).toBe("descending");
  });

  it("sorts by a column on a press, and the other way on the next", async () => {
    await mount();
    await click(head(en["nodes.col.node"]).querySelector("button"));
    expect(names()).toEqual(["de1", "fi1", "nl1"]);
    expect(head(en["nodes.col.node"]).getAttribute("aria-sort")).toBe("ascending");
    await click(head(en["nodes.col.node"]).querySelector("button"));
    expect(names()).toEqual(["nl1", "fi1", "de1"]);
    expect(head(en["nodes.col.node"]).getAttribute("aria-sort")).toBe("descending");
    // a number sorts from the largest
    await click(head(en["nodes.col.online"]).querySelector("button"));
    expect(names()).toEqual(["de1", "fi1", "nl1"]);
  });

  it("every row is one link to its node, the state says its word and its reason, a node that does not answer is dimmed", async () => {
    await mount();
    const links = [...document.querySelectorAll("a.absolute")].map((a) => a.getAttribute("href"));
    expect(links).toEqual(["/nodes/nod_nl1", "/nodes/nod_fi1", "/nodes/nod_de1"]);
    const text = document.body.textContent!;
    expect(text).toContain(en["node.reason.agent_silent"].replace("{span}", "14 min"));
    expect(text).toContain("Frankfurt · Example Cloud");
    expect(document.querySelectorAll("section[data-select] .opacity-50").length).toBeGreaterThan(0);
  });

  it("the search finds by name, city or address, and a search that finds nothing says so and resets", async () => {
    await mount();
    await typeInto(search(), "hel");
    expect(names()).toEqual(["fi1"]);
    await typeInto(search(), "203.0.113.3");
    expect(names()).toEqual(["nl1", "fi1", "de1"]);
    await typeInto(search(), "zzz");
    expect(names()).toEqual([]);
    expect(document.body.textContent).toContain(en["nv.noMatch"]);
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === en["nv.resetFilter"]));
    expect(names()).toEqual(["nl1", "fi1", "de1"]);
    expect(search().value).toBe("");
  });

  it("the chips count and filter by state", async () => {
    await mount();
    expect(radio(en["nodes.all"]).textContent).toBe(`${en["nodes.all"]}3`);
    expect(radio(en["nv.f.problems"]).textContent).toBe(`${en["nv.f.problems"]}2`);
    expect(radio(en["nodes.working"]).textContent).toBe(`${en["nodes.working"]}1`);
    await click(radio(en["nv.f.problems"]));
    expect(names()).toEqual(["nl1", "fi1"]);
    await click(radio(en["nodes.working"]));
    expect(names()).toEqual(["de1"]);
  });

  it("the last row adds a node, and the title line counts the nodes and those with problems", async () => {
    await mount();
    expect(document.body.textContent).toContain("3 nodes");
    expect(document.body.textContent).toContain("2 with problems");
    await click([...document.querySelectorAll("section[data-select] button")].find((b) => b.textContent === en["node.add.button"]));
    expect(addNode).toHaveBeenCalledTimes(1);
  });
});

describe("who gets a node", () => {
  const share = (mode: NodeShareMode, idle = false) => ({ sharing: { mode, groupsReceiving: mode === NodeShareMode.NONE ? 0 : 1, groupsTotal: 3, changedUnix: 0, idle } });

  it("labels a node that no group gets, and only that one", async () => {
    await mount([node("de1", share(NodeShareMode.ALL)), node("nl1", share(NodeShareMode.NONE)), node("fi1", share(NodeShareMode.LISTED)), node("se1")]);
    const labels = [...document.querySelectorAll("b.font-mono")].filter((b) => b.textContent?.includes(en["ns.label"])).map((b) => b.textContent);
    expect(labels).toEqual([`nl1${en["ns.label"]}`]);
    expect(document.querySelector(`[title="${en["ns.label.tip"]}"]`)).not.toBeNull();
  });

  it("says how long nobody has had it once it is over a day, and labels no retired node", async () => {
    await mount([node("nl1", share(NodeShareMode.NONE, true)), node("fi1", { ...share(NodeShareMode.NONE), status: NodeStatus.RETIRED })]);
    expect(document.querySelector(`[title="${en["ns.label.tipIdle"]}"]`)).not.toBeNull();
    expect(document.body.textContent?.split(en["ns.label"]).length).toBe(2);
  });

  it("has a link to the decoy sites beside the button that adds a node", async () => {
    await mount();
    const link = [...document.querySelectorAll("a")].find((a) => a.textContent === en["nav.decoys"]);
    expect(link?.getAttribute("href")).toBe("/decoys");
  });
});

describe("selecting", () => {
  it("opens the check column with Select, a click on a row ticks it instead of opening the node, Done closes it", async () => {
    await mount();
    const section = () => document.querySelector("section[data-select]")!;
    expect(section().getAttribute("data-select")).toBe("false");
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === en["nv.select"]));
    expect(section().getAttribute("data-select")).toBe("true");
    const open = document.querySelector<HTMLAnchorElement>("a.absolute")!;
    const click1 = new MouseEvent("click", { bubbles: true, cancelable: true });
    await act(async () => void open.dispatchEvent(click1));
    expect(click1.defaultPrevented).toBe(true);
    expect(document.querySelector('[data-nodes-bar]')?.textContent).toContain("1 node selected");
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === en["nv.done"]));
    expect(section().getAttribute("data-select")).toBe("false");
    expect(document.querySelector("[data-nodes-bar]")).toBeNull();
  });

  it("is the owner's: a helper has no Select, no check boxes' bar", async () => {
    await mount(fleet, Role.HELPER);
    expect([...document.querySelectorAll("button")].some((b) => b.textContent === en["nv.select"])).toBe(false);
    expect(document.querySelectorAll("[role=checkbox]")).toHaveLength(0);
  });
});

describe("the first state", () => {
  it("says what to do, offers one button and shows the four steps", async () => {
    await mount([]);
    const text = document.body.textContent!;
    expect(text).toContain(en["nv.first.lead"]);
    for (const k of ["nv.first.s1", "nv.first.s2", "nv.first.s3", "nv.first.s4"] as const) expect(text).toContain(en[k]);
    expect(document.querySelectorAll("section ol li")).toHaveLength(4);
    expect(document.querySelector("section[data-select]")).toBeNull();
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === en["node.add.button"]));
    expect(addNode).toHaveBeenCalled();
  });
});

describe("the filters", () => {
  it("a protocol and a country narrow the list from the Filters panel, which counts what it holds", async () => {
    const press = async (el: Element | null | undefined) => {
      expect(el).toBeTruthy();
      await act(async () => {
        for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
      });
      for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    };
    await mount();
    await press([...document.querySelectorAll("button")].find((b) => b.textContent === en["nv.filters"]));
    // the protocol select: only fi1 has nothing but vless, de1 and nl1 have both; pick the country instead for a sure cut
    await press(document.querySelector(`[aria-label="${en["nodes.filterCountry"]}"]`));
    await press([...document.querySelectorAll("[role=option]")].find((o) => o.textContent?.includes("Finland") || o.textContent?.includes("FI")));
    expect(names()).toEqual(["fi1"]);
    const filters = [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith(en["nv.filters"]))!;
    expect(filters.textContent).toBe(`${en["nv.filters"]}1`);
    expect(filters.getAttribute("aria-pressed")).toBe("true");
  });
});
