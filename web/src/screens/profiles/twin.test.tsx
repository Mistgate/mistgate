import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { EgressNote } from "./egress-note";
import { TwinPanel, type Egress } from "./twin";

const twinProfile = vi.fn();
const listGroups = vi.fn();
const listNodes = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: { twinProfile: (...a: unknown[]) => twinProfile(...a) },
  groups: { listGroups: (...a: unknown[]) => listGroups(...a) },
  nodes: { listNodes: (...a: unknown[]) => listNodes(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));
// no router in the test: a link is an anchor that carries its target
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, hash, className, onClick }: { children?: ReactNode; to: string; params?: object; search?: object; hash?: string; className?: string; onClick?: () => void }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} data-hash={hash ?? ""} className={className} onClick={onClick}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  listGroups.mockResolvedValue({ groups: [{ id: "grp_1", name: "family", profileIds: ["prf_1"], userCount: 5, dnsPresetId: "" }] });
  listNodes.mockResolvedValue({
    nodes: [
      { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", warp: { state: WarpState.UP } },
      { id: "nod_2", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI", warp: { state: WarpState.NOT_CONFIGURED } },
    ],
  });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [twinProfile, listGroups, listNodes]) m.mockReset();
});

const profile = (over: Record<string, unknown> = {}) => ({ id: "prf_1", name: "files", protocol: "hysteria2", nodeCount: 2, userCount: 7, version: 3, ...over }) as unknown as ProfileSummary;
const plan = (over: Record<string, unknown> = {}) => ({ name: "files · WARP", egress: "warp", port: 8443, nodeIds: ["nod_1", "nod_2"], groupIds: ["grp_1"], hopDropped: false, ...over });

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function mount(p: ProfileSummary, egress: Egress = "direct", dirty = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <TwinPanel profile={p} egress={egress} dirty={dirty} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

/** The node rows of the WARP twin's plan: name, the box and whether it is ticked. */
const nodeRows = () =>
  [...document.querySelectorAll("[role=dialog] li")].map((li) => ({ text: li.textContent, ticked: li.querySelector("[role=checkbox]")?.getAttribute("aria-checked") === "true", box: li.querySelector("[role=checkbox]") }));

describe("the twin action", () => {
  it("offers the opposite exit, and the dialog lists exactly what will be made; a node without WARP is unticked", async () => {
    twinProfile.mockResolvedValue(plan());
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    // the dry run is what the dialog shows
    expect(twinProfile).toHaveBeenCalledWith({ profileId: "prf_1", egress: "warp", dryRun: true }, expect.anything());
    const dialog = document.querySelector("[role=dialog]")!.textContent!;
    expect(dialog).toContain("files · WARP");
    expect(dialog).toContain("udp/8443");
    expect(dialog).toContain("family");
    expect(nodeRows().map((r) => [r.text, r.ticked])).toEqual([
      ["de1", true],
      ["fi1 — no WARP, skip", false],
    ]);
    expect(dialog).not.toContain("No working WARP"); // nothing is made there, so there is nothing to warn about
    expect(dialog).not.toContain("devices (key)");
    expect(dialog).toContain("twice in the app");
  });

  it("ticking a node without WARP warns, with the node's name a link to its WARP card", async () => {
    twinProfile.mockResolvedValue(plan());
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    await click(nodeRows()[1]!.box);
    expect(nodeRows()[1]).toMatchObject({ text: "fi1 — no WARP", ticked: true });
    const dialog = document.querySelector("[role=dialog]")!;
    expect(dialog.textContent).toContain("No working WARP on: fi1. The copy does not start there");
    const link = dialog.querySelector<HTMLAnchorElement>("a[href='/nodes/$id']")!;
    expect(link.textContent).toBe("fi1");
    expect(link.dataset.params).toBe(JSON.stringify({ id: "nod_2" }));
    expect(link.dataset.search).toBe(JSON.stringify({ tab: "settings" }));
    expect(link.dataset.hash).toBe("warp");
  });

  it("makes the twin with the planned port, leaves the unticked node out and shows the result with a link to it", async () => {
    twinProfile.mockResolvedValueOnce(plan({ hopDropped: true })).mockResolvedValueOnce({ ...plan({ hopDropped: true, nodeIds: ["nod_1"] }), profile: { id: "prf_2", name: "files · WARP" } });
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    expect(text()).toContain("Port hopping is off in the copy");
    await click(button("Make the copy"));
    await settle();
    expect(twinProfile).toHaveBeenLastCalledWith({ profileId: "prf_1", egress: "warp", port: 8443, skipNodeIds: ["nod_2"] });
    const dialog = document.querySelector("[role=dialog]")!;
    expect(dialog.textContent).toContain("“files · WARP” is made");
    const link = dialog.querySelector<HTMLAnchorElement>("a[href='/profiles/$id']");
    expect(link?.dataset.params).toBe(JSON.stringify({ id: "prf_2" }));
    expect(dialog.textContent).not.toContain("No working WARP");
  });

  it("a node ticked on purpose is made too, and the result still says it has no WARP", async () => {
    twinProfile.mockResolvedValueOnce(plan()).mockResolvedValueOnce({ ...plan(), profile: { id: "prf_2", name: "files · WARP" } });
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    await click(nodeRows()[1]!.box);
    await click(button("Make the copy"));
    await settle();
    expect(twinProfile).toHaveBeenLastCalledWith({ profileId: "prf_1", egress: "warp", port: 8443, skipNodeIds: [] });
    expect(document.querySelector("[role=dialog]")!.textContent).toContain("No working WARP on: fi1"); // still said after it was made
  });

  it("turns a WARP profile into a direct one, and says that AmneziaWG needs a key of its own", async () => {
    twinProfile.mockResolvedValue(plan({ name: "files", egress: "direct", port: 4443 }));
    await mount(profile({ name: "files · WARP", protocol: "awg" }), "warp");
    await click(button("Copy without WARP"));
    await settle();
    expect(twinProfile).toHaveBeenCalledWith({ profileId: "prf_1", egress: "direct", dryRun: true }, expect.anything());
    const dialog = document.querySelector("[role=dialog]")!.textContent!;
    expect(dialog).toContain("udp/4443");
    expect(dialog).toContain("separate device (key)");
    expect(dialog).not.toContain("No working WARP"); // a direct exit needs none
  });

  it("the note under «Exit: WARP» links every node without WARP to its WARP card", async () => {
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
    await settle();
    expect(text()).toBe("No working WARP on: fi1. The profile does not start there until it has.");
    const link = host.querySelector<HTMLAnchorElement>("a")!;
    expect([link.textContent, link.getAttribute("href"), link.dataset.params, link.dataset.search, link.dataset.hash]).toEqual(["fi1", "/nodes/$id", JSON.stringify({ id: "nod_2" }), JSON.stringify({ tab: "settings" }), "warp"]);
  });

  it("is not offered to anyone but the owner, and waits for unsaved changes", async () => {
    role = Role.HELPER;
    await mount(profile());
    expect(button("WARP copy")).toBeUndefined();
    expect(text()).toBe("");
    act(() => root?.unmount());
    role = Role.OWNER;
    await mount(profile(), "direct", true);
    expect(button("WARP copy")?.disabled).toBe(true);
    expect(text()).toContain("Save the changes first");
  });
});

describe("the UDP delivery check behind the twin's port", () => {
  const pc = (over: Record<string, unknown>) => ({ nodeId: "nod_1", port: 8443, verdict: "ok", sent: 300, got: 300, checkedUnix: 1_790_000_000n, badUnix: 0n, sender: "de2", reason: "", ...over });

  it("says it is checking UDP on the profile's nodes while the plan is made", async () => {
    twinProfile.mockReturnValue(new Promise(() => {}));
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    expect(document.querySelector("[role=dialog]")!.textContent).toContain("Checking UDP on 2 nodes…");
    act(() => root?.unmount());
    host?.remove();
    await mount(profile({ nodeCount: 1 }));
    await click(button("WARP copy"));
    await settle();
    expect(document.querySelector("[role=dialog]")!.textContent).toContain("Checking UDP on 1 node…");
  });

  it("shows a badge per node for the chosen port (checked, or unchecked with the reason) and the candidate skipped for its loss", async () => {
    twinProfile.mockResolvedValue(
      plan({
        portChecks: [pc({}), pc({ nodeId: "nod_2", verdict: "", sent: 0, got: 0, checkedUnix: 0n, sender: "", reason: "no_sender" }), pc({ nodeId: "nod_1", port: 4443, verdict: "lossy", got: 189 })],
      }),
    );
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    const dialog = document.querySelector("[role=dialog]")!.textContent!;
    expect(dialog).toContain("udp/8443");
    expect(dialog).toContain("de1: checked, no loss");
    expect(dialog).toContain("fi1: unchecked (no sender)");
    expect(dialog).toContain("4443 skipped: lost 37 % on de1");
  });

  it("shows the same in the result, and nothing when the server sent no checks", async () => {
    twinProfile.mockResolvedValueOnce(plan()).mockResolvedValueOnce({ ...plan(), portChecks: [pc({})], profile: { id: "prf_2", name: "files · WARP" } });
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    expect(document.querySelector("[role=dialog]")!.textContent).not.toContain("checked, no loss");
    await click(button("Make the copy"));
    await settle();
    expect(document.querySelector("[role=dialog]")!.textContent).toContain("de1: checked, no loss");
  });

  it("explains no_clean_port instead of showing a plan", async () => {
    twinProfile.mockRejectedValue(new ConnectError("no_clean_port", Code.FailedPrecondition));
    await mount(profile());
    await click(button("WARP copy"));
    await settle();
    const dialog = document.querySelector("[role=dialog]")!;
    expect(dialog.textContent).toContain("No port without UDP loss was found on all the nodes of this profile.");
    expect(button("Make the copy")?.hasAttribute("disabled") || button("Make the copy")?.getAttribute("data-disabled") !== null).toBe(true);
  });
});
