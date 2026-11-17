import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { editionFeatures } from "@/i18n/edition";
import { en } from "@/i18n/en";
import { setLang } from "@/i18n";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { NodesScreen } from "./nodes";

// Nodes: tick several nodes, then "More ▾ → Decoy site…" in the bulk bar sets one mode for all of them in one call, and the
// bar lists each node with its state read live. Own is off while one of them has no uploaded site, and in the Cloudflare edition.

const setNodesDecoyMode = vi.fn();
vi.mock("@/lib/api", () => ({
  basepath: "/",
  nodes: { setNodesDecoyMode: (...a: unknown[]) => setNodesDecoyMode(...a) },
  auth: { me: () => Promise.resolve({}) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className, title, tabIndex }: { children?: ReactNode; to: string; className?: string; title?: string; tabIndex?: number }) => (
    <a href={to} className={className} title={title} tabIndex={tabIndex}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
}));
vi.mock("@/components/add-node", () => ({ useAddNode: () => vi.fn() }));

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
  setNodesDecoyMode.mockReset();
});

const node = (name: string, over: Record<string, unknown> = {}) => ({
  id: `nod_${name}`,
  name,
  countryCode: "DE",
  location: "",
  provider: "",
  notes: "",
  address: `${name}.example.com`,
  status: NodeStatus.ONLINE,
  protocols: ["hysteria2"],
  online: [],
  trafficTodayBytes: 0,
  hasMetrics: false,
  cpuPct: 0,
  uptimeS: 0,
  agentVersion: "0.3.1",
  decoyMode: "builtin",
  decoyBundleSha256: "",
  decoyPanelAvailable: true,
  decoyOwnAvailable: true,
  ...over,
});
const withBundle = { decoyBundleSha256: "ab".repeat(32) };

async function mount(nodes: ReturnType<typeof node>[], me: Record<string, unknown> = { admin: { id: "adm_1", role: Role.OWNER } }, updates: Record<string, unknown>[] = []) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  qc.setQueryData(updatesQuery.queryKey, { nodes: updates } as never);
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
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const tick = (name: string) => document.querySelector<HTMLElement>(`[role=checkbox][aria-label="${en["bulk.select"].replace("{name}", name)}"]`)!;
const ticked = (name: string) => tick(name).getAttribute("aria-checked") === "true";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const radio = (mode: string) => document.querySelector<HTMLInputElement>(`input[type=radio][value=${mode}]`)!;
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
/** A Base UI menu opens on a press, not on a bare click. */
const press = async (el: Element | null | undefined) => {
  await act(async () => {
    for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el?.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  await settle();
};
const bar = () => document.querySelector("[data-nodes-bar]");
/** The check column opens with "Select". */
const selecting = async () => {
  if (!button(en["nv.done"])) await press(button(en["nv.select"]));
};
/** More ▾ → Decoy site… */
const openDecoy = async () => {
  await press(bar()?.querySelector(`button[aria-label="${en["bulk.more"]}"]`));
  await press([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent?.startsWith(en["bulk.decoy"])));
};

describe("node notes", () => {
  it("shows plain-text notes in the table and mobile cards, with the full text only on the notes", async () => {
    const notes = "<b>Renew in March</b>\n备用节点";
    await mount([node("de1", { notes })]);
    const summaries = [...document.querySelectorAll("p")].filter((p) => p.textContent === notes);
    expect(summaries).toHaveLength(1);
    expect(summaries.every((p) => p.querySelector("b") === null)).toBe(true);
    expect([...document.querySelectorAll("a")].filter((a) => a.title === notes)).toHaveLength(1);
    for (const summary of summaries) {
      const link = summary.closest("a")!;
      expect(link.title).toBe(notes);
      expect(link.getAttribute("href")).toBe("/nodes/$id");
      expect(link.tabIndex).toBe(-1); // the row link remains the single keyboard stop
    }
    const rowLinks = document.querySelectorAll("a.absolute");
    expect(rowLinks).toHaveLength(1);
    expect([...rowLinks].every((a) => !a.hasAttribute("title"))).toBe(true);
  });

  it("omits empty and whitespace-only notes", async () => {
    await mount([node("de1"), node("fi1", { notes: " \n\t " })]);
    expect(document.querySelectorAll("p")).toHaveLength(0);
    expect(document.querySelectorAll("a[title]")).toHaveLength(0);
  });
});

describe("node status and metadata", () => {
  it("says the status once, with one dot, and keeps the notes out of the heading", async () => {
    await mount([node("de1", { notes: "Renew in March", provider: "examplehost" })]);
    const names = [...document.querySelectorAll("b")].filter((b) => b.textContent === "de1");
    expect(names).toHaveLength(1);
    for (const name of names) {
      const heading = name.parentElement!;
      expect(heading.textContent).toContain("examplehost");
      expect(heading.textContent).not.toContain("Renew in March");
      const view = name.closest("div.relative")!;
      expect(view.querySelectorAll("span.rounded-full[aria-hidden]")).toHaveLength(1);
      expect(view.textContent!.split(en["node.status.online"])).toHaveLength(2);
    }
  });

  it("keeps diagnostic reasons without repeating the pending status", async () => {
    await mount([node("de1", { status: NodeStatus.PENDING, reason: { code: "enrollment_expired", params: {} } })]);
    expect(text().split(en["node.status.pending"])).toHaveLength(2);
    expect(text().split(en["node.reason.enrollment_expired"])).toHaveLength(2);
  });

  it("keeps location metadata on the heading when notes are absent", async () => {
    await mount([node("de1", { location: "Germany", provider: "examplehost" })]);
    const names = [...document.querySelectorAll("b")].filter((b) => b.textContent === "de1");
    expect(names).toHaveLength(1);
    expect(names.every((name) => name.parentElement?.textContent?.includes("Germany · examplehost"))).toBe(true);
    expect(document.querySelectorAll("p")).toHaveLength(0);
  });

  it("keeps the separate update action for an outdated healthy node", async () => {
    await mount([node("de1")], undefined, [{ nodeId: "nod_de1", state: NodeUpdateState.OUTDATED }]);
    const updates = [...document.querySelectorAll('a[href="/updates"]')];
    expect(updates).toHaveLength(1);
    expect(updates.every((a) => a.textContent?.includes("0.3.1"))).toBe(true);
    expect(updates.map((a) => a.textContent!.replace(/ 0\.3\.1$/, ""))).toEqual([en["nodes.agentOld"]]);
    for (const update of updates) {
      expect(update.closest("div.relative")!.textContent!.split(en["node.status.online"])).toHaveLength(2);
    }
  });
});

describe("ticking nodes", () => {
  it("shows how many are ticked, and clears them", async () => {
    await mount([node("de1"), node("fi1")]);
    expect(bar()).toBeNull(); // nothing ticked: no bar
    await selecting();
    await click(tick("de1"));
    expect(bar()?.textContent).toContain("1 node selected");
    await click(tick("fi1"));
    expect(bar()?.textContent).toContain("2 nodes selected");
    await click(document.querySelector(`[data-nodes-bar] button[aria-label="${en["bulk.clear"]}"]`));
    expect(bar()).toBeNull();
  });

  it("opens the check column with Select and closes it, clearing the ticks, with Done", async () => {
    await mount([node("de1"), node("fi1")]);
    expect(button(en["nv.done"])).toBeUndefined();
    expect(document.querySelector("section[data-select]")?.getAttribute("data-select")).toBe("false");
    await selecting();
    expect(document.querySelector("section[data-select]")?.getAttribute("data-select")).toBe("true");
    await click(tick("de1"));
    expect(bar()).not.toBeNull();
    await press(button(en["nv.done"]));
    expect(bar()).toBeNull();
    expect(document.querySelector("section[data-select]")?.getAttribute("data-select")).toBe("false");
    expect(ticked("de1")).toBe(false);
  });

  it("ticks every shown node from the quick pick", async () => {
    await mount([node("de1"), node("fi1"), node("se1")]);
    await selecting();
    await press(button(en["bulk.pick"]));
    await press([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent?.startsWith(en["bulk.pick.shown"])));
    expect(bar()?.textContent).toContain("3 nodes selected");
    // "clear all" under the count takes them off again
    await click([...(bar()?.querySelectorAll("button") ?? [])].find((b) => b.textContent === en["nv.bulk.none"]));
    expect(bar()).toBeNull();
  });

  it("is for the owner only", async () => {
    await mount([node("de1")], { admin: { id: "adm_2", role: Role.HELPER } });
    expect(tick("de1")).toBeNull();
  });

  it("does not offer a retired node", async () => {
    await mount([node("de1", { status: NodeStatus.RETIRED })]);
    await selecting();
    expect(tick("de1").hasAttribute("data-disabled")).toBe(true);
  });
});

describe("Set decoy site", () => {
  it("sends the ticked nodes and the picked mode in one call", async () => {
    setNodesDecoyMode.mockResolvedValue({ nodes: [node("de1"), node("fi1")] });
    await mount([node("de1"), node("fi1", { decoyReasonCode: "pending" }), node("se1")]);
    await selecting();
    await click(tick("de1"));
    await click(tick("fi1"));
    await openDecoy();
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("Decoy site for 2 nodes");
    await click(radio("panel"));
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent?.trim() === en["decoy.apply"]));
    expect(setNodesDecoyMode).toHaveBeenCalledTimes(1);
    expect(setNodesDecoyMode).toHaveBeenCalledWith({ nodeIds: ["nod_de1", "nod_fi1"], mode: "panel" });
    expect(document.querySelector("[role=dialog]")).toBeNull();
    // the bar lists both, each with its state from the live list (fi1 is still being applied)
    const rows = [...document.querySelectorAll("[data-batch] li")].map((li) => li.textContent);
    expect(rows).toEqual([expect.stringContaining(en["bulk.state.ok"]), expect.stringContaining(en["bulk.state.running"])]);
    expect(text()).toContain("Decoy site set on 2 nodes");
    expect(bar()?.textContent).toContain("2 nodes selected"); // the selection stays for the next action
  });

  it("keeps own on while every ticked node has its uploaded site", async () => {
    await mount([node("de1", withBundle), node("fi1")]);
    await selecting();
    await click(tick("de1"));
    await openDecoy();
    expect(radio("own").disabled).toBe(false);
  });

  it("turns own off while a ticked node has no uploaded site, and says which", async () => {
    await mount([node("de1", withBundle), node("fi1")]);
    await selecting();
    await click(tick("de1"));
    await click(tick("fi1"));
    await openDecoy();
    expect(radio("own").disabled).toBe(true);
    expect(document.querySelector("[role=dialog]")?.textContent).toContain(en["decoy.bulk.ownBlocked"].replace("{names}", "fi1"));
  });

  it("turns own off in the Cloudflare edition", async () => {
    await mount([node("de1", { ...withBundle, decoyOwnAvailable: false })], { admin: { id: "adm_1", role: Role.OWNER }, edition: "edge", unavailable: [...editionFeatures] });
    await selecting();
    await click(tick("de1"));
    await openDecoy();
    expect(radio("own").disabled).toBe(true);
    expect(document.querySelector("[role=dialog]")?.textContent).toContain(en["err.unavailable_in_edition"].replace("{feature}", en["edition.feature.decoy_own"]));
  });

  it("shows the panel's refusal in words and keeps the selection", async () => {
    setNodesDecoyMode.mockRejectedValue(new ConnectError("panel_unavailable", Code.FailedPrecondition));
    await mount([node("de1")]);
    await selecting();
    await click(tick("de1"));
    await openDecoy();
    await click(radio("panel"));
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent?.trim() === en["decoy.apply"]));
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toBe(en["err.panel_unavailable"]);
    expect(ticked("de1")).toBe(true);
  });
});
