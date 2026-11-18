import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { meQuery } from "@/lib/session";
import { cards, groupRows, seededCard } from "../dev-kit-node-share";
import { groupsQuery } from "../users/rpc";
import { ShareCard, shareDemo } from "./share";

// The card "Who gets this node": the groups with a switch each, and the two buttons for everyone that ask first.

const setNodeSharing = vi.fn();
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, groups: { setNodeSharing: (...a: unknown[]) => setNodeSharing(...a) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
beforeEach(() => setLang("en"));

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setNodeSharing.mockReset();
  shareDemo.current = undefined;
});

const family = "grp_family";
const friends = "grp_friends";
const team = "grp_team";
const guests = "grp_guests";
const node = (mode: NodeShareMode, over: Record<string, unknown> = {}) =>
  ({ id: "nod_de1", name: "de1", status: NodeStatus.ONLINE, sharing: { mode, groupsReceiving: 2, groupsTotal: 4, changedUnix: 0, idle: false, ...over } }) as never;

const client = () => new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });

async function render(qc: QueryClient, n: ReturnType<typeof node>) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ShareCard node={n} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
async function mount(n: ReturnType<typeof node>, receiving: string[] = [family, friends], role = Role.OWNER) {
  const qc = client();
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role } } as never);
  qc.setQueryData(groupsQuery.queryKey, groupRows(receiving, "nod_de1") as never);
  await render(qc, n);
  return qc;
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => host!.textContent ?? document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const sw = (name: string) => document.querySelector<HTMLElement>(`[role=switch][aria-label="Give the node to ${name}"]`)!;
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const answer = (over: Record<string, unknown> = {}) => ({ mode: NodeShareMode.LISTED, groupIds: [], peopleReceiving: 0, peopleGain: 0, peopleLose: 0, ...over });

describe("who gets this node", () => {
  it("shows the groups with a switch each and says how many get the node", async () => {
    await mount(node(NodeShareMode.LISTED));
    expect(text()).toContain(en["ns.card.title"]);
    expect(text()).toContain("2 of 4 groups");
    expect(text()).toContain(en["ns.hint.listed"]);
    expect(["family", "friends", "team", "guests"].map((g) => sw(g).getAttribute("aria-checked"))).toEqual(["true", "true", "false", "false"]);
    expect(text()).toContain("9 people");
  });

  it("a switch writes at once: the group is added to the list", async () => {
    setNodeSharing.mockResolvedValue(answer());
    await mount(node(NodeShareMode.LISTED));
    await click(sw("team"));
    expect(setNodeSharing).toHaveBeenCalledTimes(1);
    expect(setNodeSharing).toHaveBeenCalledWith({ nodeId: "nod_de1", mode: NodeShareMode.LISTED, groupIds: [family, friends, team], dryRun: false });
    expect(document.body.textContent).toContain("de1 goes to 3 groups");
  });

  it("switching the last group off is nobody yet", async () => {
    setNodeSharing.mockResolvedValue(answer({ mode: NodeShareMode.NONE }));
    await mount(node(NodeShareMode.LISTED, { groupsReceiving: 1 }), [family]);
    await click(sw("family"));
    expect(setNodeSharing).toHaveBeenCalledWith({ nodeId: "nod_de1", mode: NodeShareMode.NONE, groupIds: [], dryRun: false });
  });

  it("switching a group off for a node everyone gets lists the others", async () => {
    setNodeSharing.mockResolvedValue(answer());
    await mount(node(NodeShareMode.ALL, { groupsReceiving: 4 }), [family, friends, team, guests]);
    expect(text()).toContain(en["ns.pill.all"]);
    await click(sw("guests"));
    expect(setNodeSharing).toHaveBeenCalledWith({ nodeId: "nod_de1", mode: NodeShareMode.LISTED, groupIds: [family, friends, team], dryRun: false });
  });

  it("taking the node off everyone asks first, with the people the panel counted, and writes only on the yes", async () => {
    setNodeSharing.mockImplementation(async (r: { dryRun: boolean }) => (r.dryRun ? answer({ mode: NodeShareMode.NONE, peopleLose: 13 }) : answer({ mode: NodeShareMode.NONE })));
    await mount(node(NodeShareMode.LISTED));
    await click(button(en["ns.takeAll"]));
    await settle();
    // the question is a dry run: nothing is written yet
    expect(setNodeSharing).toHaveBeenCalledTimes(1);
    expect(setNodeSharing).toHaveBeenLastCalledWith({ nodeId: "nod_de1", mode: NodeShareMode.NONE, groupIds: [], dryRun: true });
    expect(text()).toContain("Take de1 off everyone?");
    expect(text()).toContain("2 groups and 13 people stop getting it");
    await click(button(en["ns.ask.none.yes"]));
    await settle();
    expect(setNodeSharing).toHaveBeenCalledTimes(2);
    expect(setNodeSharing).toHaveBeenLastCalledWith({ nodeId: "nod_de1", mode: NodeShareMode.NONE, groupIds: [], dryRun: false });
    expect(document.body.textContent).toContain("de1 is taken off everyone");
    expect(host!.textContent).not.toContain("Take de1 off everyone?");
  });

  it("cancelling the question writes nothing", async () => {
    setNodeSharing.mockResolvedValue(answer({ mode: NodeShareMode.NONE, peopleLose: 3 }));
    await mount(node(NodeShareMode.LISTED));
    await click(button(en["ns.takeAll"]));
    await settle();
    await click(button("Cancel"));
    expect(setNodeSharing).toHaveBeenCalledTimes(1);
    expect(text()).not.toContain("Take de1 off everyone?");
    expect(button(en["ns.takeAll"])).toBeTruthy();
  });

  it("giving the node to everyone says how many people start to get it", async () => {
    setNodeSharing.mockResolvedValue(answer({ mode: NodeShareMode.ALL, peopleGain: 4 }));
    await mount(node(NodeShareMode.LISTED));
    await click(button(en["ns.giveAll"]));
    await settle();
    expect(setNodeSharing).toHaveBeenLastCalledWith({ nodeId: "nod_de1", mode: NodeShareMode.ALL, groupIds: [], dryRun: true });
    expect(text()).toContain("Give de1 to everyone?");
    expect(text()).toContain("4 people start getting it");
  });

  it("a node that was retired meanwhile is refused in words, and cannot be confirmed", async () => {
    setNodeSharing.mockRejectedValue(new ConnectError("node_retired", Code.FailedPrecondition));
    await mount(node(NodeShareMode.LISTED));
    await click(button(en["ns.takeAll"]));
    await settle();
    expect(text()).toContain(en["err.node_retired"]);
    expect((button(en["ns.ask.none.yes"]) as HTMLButtonElement).disabled).toBe(true);
  });

  it("a refused switch says why and leaves the card as the panel has it", async () => {
    setNodeSharing.mockRejectedValue(new ConnectError("node_retired", Code.FailedPrecondition));
    await mount(node(NodeShareMode.LISTED));
    await click(sw("team"));
    await settle();
    expect(document.body.textContent).toContain(en["err.node_retired"]);
    expect(sw("team").getAttribute("aria-checked")).toBe("false");
  });

  it("nobody yet: the hint is quiet, and there is nothing to take off", async () => {
    await mount(node(NodeShareMode.NONE, { groupsReceiving: 0 }), []);
    expect(text()).toContain(en["ns.pill.none"]);
    expect(text()).toContain(en["ns.hint.none"]);
    expect((button(en["ns.takeAll"]) as HTMLButtonElement).disabled).toBe(true);
    expect((button(en["ns.giveAll"]) as HTMLButtonElement).disabled).toBe(false);
  });

  it("nobody for over a day says it is fine while the node is being set up", async () => {
    await mount(node(NodeShareMode.NONE, { groupsReceiving: 0, idle: true }), []);
    expect(text()).toContain(en["ns.hint.noneIdle"]);
  });

  it("a read-only role sees who gets the node and cannot change it", async () => {
    await mount(node(NodeShareMode.LISTED), [family, friends], Role.READONLY);
    expect(sw("family").hasAttribute("data-disabled")).toBe(true);
    expect(button(en["ns.giveAll"])).toBeUndefined();
    expect(button(en["ns.takeAll"])).toBeUndefined();
    expect(text()).toContain(en["ns.readOnly"]);
  });

  it("a helper may change it", async () => {
    await mount(node(NodeShareMode.LISTED), [family, friends], Role.HELPER);
    expect(sw("family").hasAttribute("data-disabled")).toBe(false);
    expect(button(en["ns.takeAll"])).toBeTruthy();
  });

  it("while the groups are coming it says so", async () => {
    const qc = client();
    qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: Role.OWNER } } as never);
    void qc.prefetchQuery({ queryKey: groupsQuery.queryKey, queryFn: () => new Promise(() => {}) });
    await render(qc, node(NodeShareMode.LISTED));
    expect(text()).toContain(en["common.loading"]);
    // with no groups to count, the two buttons wait
    expect((button(en["ns.giveAll"]) as HTMLButtonElement).disabled).toBe(true);
    expect((button(en["ns.takeAll"]) as HTMLButtonElement).disabled).toBe(true);
  });

  it("every state of the dev kit draws, in every language, with no key left bare", async () => {
    for (const lang of ["en", "ru", "zh", "fa", "tr"] as const) {
      for (const state of Object.keys(cards)) {
        setLang(lang);
        const made = seededCard(state);
        await render(made.qc, (made.data as unknown as { node: never }).node);
        expect(text(), `${lang} ${state}`).not.toMatch(/\bns\.|undefined|NaN/);
        expect(host!.querySelector("section")?.getAttribute("aria-label"), `${lang} ${state}`).toBeTruthy();
        act(() => root?.unmount());
        host!.remove();
        root = host = null;
      }
    }
  });
});
