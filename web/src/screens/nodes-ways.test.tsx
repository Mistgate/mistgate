import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { setLang } from "@/i18n";
import { nodesQuery } from "@/lib/queries";
import { demoFor, edgeOfScene, roleOfScene, seeded } from "./dev-kit-nodes";
import { NodesScreen } from "./nodes";

// The ways out of the country on the real screen, on the dev kit's made-up data (every state of the board's toggles has a scene): the
// "From Russia" column of the list, the Paths view and the pair's sheet.

const nav = vi.hoisted(() => vi.fn());
const api = vi.hoisted(() => ({ checkRelay: vi.fn(async () => ({})), updateRelay: vi.fn(async () => ({})), deleteRelay: vi.fn(async () => ({})) }));
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, relays: api }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, to, params }: { children?: ReactNode; className?: string; to?: string; params?: { id: string } }) => <a className={className} href={(to ?? "").replace("$id", params?.id ?? "")}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => nav,
}));

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
  localStorage.clear();
  Object.values(api).forEach((f) => f.mockClear());
  nav.mockClear();
  delete (window as { matchMedia?: unknown }).matchMedia;
});

async function draw(state: string, role?: string, tweak?: (qc: ReturnType<typeof seeded>) => void) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, roleOfScene(state, role ?? null), edgeOfScene(state));
  tweak?.(qc);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <NodesScreen demo={demoFor(state)} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const text = (el: Element | null | undefined) => el?.textContent ?? "";
const ru = (node: string) => document.querySelector(`[data-ru="nod_${node}"]`);
const row = (node: string) => document.querySelector(`.rw-row[data-node="nod_${node}"]`);
const head = (node: string) => document.querySelector(`.rw-eh[data-node="nod_${node}"]`);
const rows = () => [...document.querySelectorAll(".rw-row[data-node]")].map((r) => r.getAttribute("data-node")!.slice(4));
const button = (label: string | RegExp, within: ParentNode = document) => [...within.querySelectorAll("button")].find((b) => (typeof label === "string" ? b.textContent === label : label.test(b.textContent ?? "")));
const sheet = () => document.querySelector("aside.rw-dr");

describe("the list's column: From Russia", () => {
  it("says what each node does: closed and carried, closed on some networks and carried by a bridge, open, and an entry", async () => {
    await draw("ru-norm");
    expect(text(ru("de1"))).toBe("via msk1direct is blocked");
    expect(text(ru("nl1"))).toBe("via msk1 · bridgedirect, except Operator A");
    expect(text(ru("fi1"))).toBe("directall networks");
    expect(text(ru("msk1"))).toBe("visibleentry for 2");
    expect(document.body.textContent).toContain(en["nw.role"]);
    expect(document.querySelector(`[data-ru="nod_de1"]`)?.getAttribute("title")).toBe("via msk1 · direct is blocked");
  });
  it("an entry shows no people and what it relayed today, to the exits", async () => {
    await draw("ru-norm");
    const r = ru("msk1")!.closest("div.group")!;
    expect(text(r)).toContain(en["nw.role"]);
    expect(r.querySelector(`[title="${en["nw.tip.today"]}"]`)).not.toBeNull();
  });
  it("the head of the column names the country", async () => {
    await draw("ru-norm");
    expect(text(document.querySelector("[role=columnheader]:nth-of-type(3)")) || document.body.textContent).toContain("From Russia");
  });
  it("closed with no way: no path, and Bypass… opens the relay dialog through the best entry", async () => {
    await draw("ru-twelve");
    expect(text(ru("de2"))).toContain("no path");
    expect(text(ru("de2"))).toContain("direct is blocked");
    await click(button(en["nw.fix"], ru("de2")!));
    // the wizard of de2's page, at the choice of the entry (the old dialog is gone)
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(nav).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_de2" }, search: { tab: "ways", add: "1" } });
  });
  it("a node of the board in each of its states: partly, two entries, no data", async () => {
    await draw("ru-twelve");
    expect(text(ru("nl2"))).toBe("via msk1 · 1 of 2msk1 → nl2 fails (UDP)");
    expect(text(ru("de3"))).toBe("via msk1 and msk2direct is blocked");
    expect(text(ru("nl3"))).toBe("no data");
    expect(button(en["nw.fix"], ru("nl2")!)).toBeUndefined();
  });
  it("the title line counts the nodes with no way and the relays that fail; the filter No path keeps only those", async () => {
    await draw("ru-twelve");
    const sub = document.querySelector("h1")!.parentElement!.textContent!;
    expect(sub).toContain("1 with no path from Russia");
    expect(sub).toContain("1 relay not working");
    await click([...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent?.startsWith(en["nw.f.noway"])));
    expect(text(document.querySelector("section[data-select]"))).toContain("de2");
    expect(document.querySelectorAll("[data-ru]")).toHaveLength(1);
  });
  it("offers the filter only while there is a node with no way", async () => {
    await draw("ru-norm");
    expect([...document.querySelectorAll("[role=radio]")].some((r) => r.textContent?.startsWith(en["nw.f.noway"]))).toBe(false);
    expect(document.querySelector("h1")!.parentElement!.textContent).not.toContain("no path");
  });
  it("a helper sees what the country sees of each node, no way to bypass, and no Paths view", async () => {
    await draw("ru-helper");
    expect(text(ru("de1"))).toBe("direct is blocked");
    expect(text(ru("fi1"))).toBe("direct");
    expect(button(en["nw.fix"])).toBeUndefined();
    expect([...document.querySelectorAll("[role=radio]")].map((r) => r.textContent)).not.toContain(en["nw.view"]);
  });
  it("loading says so and offers nothing; a failed relay query says that and offers nothing", async () => {
    await draw("ru-load");
    expect(text(ru("de1"))).toBe("loading…");
    act(() => root!.unmount());
    host!.remove();
    await draw("ru-err");
    expect(text(ru("de1"))).toBe("no relay data");
    expect(button(en["nw.fix"])).toBeUndefined();
  });
  it("without measurements every node has no data, and where a relay carries it, which", async () => {
    await draw("ru-nomeas");
    expect(text(ru("fi1"))).toBe("no data");
    expect(text(ru("de1"))).toBe("no datavia msk1");
    expect(text(ru("msk1"))).toBe("no dataentry for 2");
  });
  it("on the Cloudflare edition there is no measurement: no data", async () => {
    await draw("ru-cf");
    expect(text(ru("fi1"))).toBe("no data");
  });
  it("an entry that is offline, not seen, or old says it; its exits say it once, and offer nothing to bypass", async () => {
    await draw("ru-entoff");
    expect(text(ru("msk1"))).toContain("offline");
    expect(text(ru("de1"))).toBe("no pathmsk1 is offline");
    expect(button(en["nw.fix"])).toBeUndefined();
    act(() => root!.unmount());
    host!.remove();
    await draw("ru-enthid");
    expect(text(ru("msk1"))).toContain("not visible from Russia");
    expect(text(ru("msk1"))).toContain("people can’t reach any exit");
    expect(text(ru("de1"))).toBe("no pathmsk1 is not visible");
    act(() => root!.unmount());
    host!.remove();
    await draw("ru-oldagent");
    expect(text(ru("msk1"))).toContain(en["nw.entry.old"]);
  });
  it("a measurement older than a day says how old", async () => {
    await draw("ru-stale");
    expect(text(ru("fi1"))).toContain("measurement is 2 days old");
  });
  it("the same words in every language", async () => {
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      setLang(lang);
      await draw("ru-twelve");
      expect(text(ru("de2"))).not.toContain("nw.");
      expect(text(ru("nl2"))).toContain("msk1");
      act(() => root!.unmount());
      host!.remove();
    }
  });
  it("offers no relay to an entry in the exit's own country: Bypass… says there is none", async () => {
    // every node of the fleet is in the same country as the entries
    await draw("ru-twelve", undefined, (qc) => {
      const d = qc.getQueryData(nodesQuery.queryKey) as { nodes: { countryCode: string }[] };
      qc.setQueryData(nodesQuery.queryKey, { ...d, nodes: d.nodes.map((n) => ({ ...n, countryCode: "RU" })) } as never);
    });
    await click(button(en["nw.fix"], ru("de2")!));
    expect(document.body.textContent).toContain(en["nw.noEntry"]);
    expect(document.body.textContent).not.toContain("Relay through");
  });
});

describe("the Paths view", () => {
  it("is an exit per row, no way first, then partly, no data, then the rest; an entry per column", async () => {
    await draw("rw-twelve");
    expect(rows()).toEqual(["de2", "nl2", "nl3", "de1", "de3", "fi1", "fi2", "fi3", "nl1"]);
    expect([...document.querySelectorAll(".rw-eh")].map((h) => h.getAttribute("data-node"))).toEqual(["nod_msk1", "nod_msk2", "nod_spb1"]);
    expect(text(row("de2"))).toContain("no path");
    expect(text(row("fi1"))).toContain("has a path");
  });
  it("counts what is reached and what is not, and the relays", async () => {
    await draw("rw-twelve");
    expect(document.body.textContent).toContain("From Russia: 6 of 9 reachable · no path 1 · 13 relays, 1 not working");
    expect([...document.querySelectorAll("[role=radio]")].map((r) => r.textContent).filter((x) => /^(All|No path|Partly|Over the bridge)/.test(x ?? ""))).toEqual(["All9", "No path1", "Partly1", "Over the bridge1"]);
  });
  it("names the direct way of each exit and the pair of each cell: forwarding, a bridge and since when, partly, no way offered", async () => {
    await draw("rw-twelve");
    expect(text(row("de1")!.querySelector(".rw-d"))).toBe("blocked");
    expect(text(row("nl1")!.querySelector(".rw-d"))).toBe("except Operator A");
    expect(text(row("fi1")!.querySelector(".rw-d"))).toBe("all networks");
    expect(text(row("nl3")!.querySelector(".rw-d"))).toBe("no data");
    const cells = (n: string) => [...row(n)!.querySelectorAll(".rw-c")].map((c) => text(c));
    expect(cells("de1")[0]).toBe("forwarding3 relays");
    expect(cells("nl1")[0]).toMatch(/^bridge2 relays · (almost full|since )/);
    expect(cells("nl1")[2]).toBe("forwarding1 relay");
    expect(cells("nl2")[0]).toBe("1 of 2 relaysmsk1 → nl2 fails (UDP)");
    // the third entry carries nothing of de3's: a quiet offer, as de3 is closed
    expect(cells("de3")).toEqual(["forwarding3 relays", "forwarding2 relays", "＋ Route through spb1…"]);
  });
  it("offers Bypass through the best entry to an exit with no way, and a quiet add to the other entries of a closed one; none to an open one", async () => {
    await draw("rw-twelve");
    const de2 = [...row("de2")!.querySelectorAll(".rw-c")];
    expect(text(de2[0])).toBe("Bypass through msk1…");
    expect(text(de2[1])).toBe("＋ Route through msk2…");
    expect(text(de2[2])).toBe("＋ Route through spb1…");
    expect([...row("fi1")!.querySelectorAll(".rw-c button")]).toHaveLength(0);
    // the entry named on the button is the one the wizard opens at (its servers)
    await click(de2[0]!.querySelector("button"));
    expect(nav).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_de2" }, search: { tab: "ways", add: "via:nod_msk1" } });
    await click(de2[1]!.querySelector("button"));
    expect(nav).toHaveBeenLastCalledWith({ to: "/nodes/$id", params: { id: "nod_de2" }, search: { tab: "ways", add: "via:nod_msk2" } });
  });
  it("shows an entry's trouble once, in its head: the column's cells are dashes", async () => {
    await draw("rw-entoff");
    expect(text(head("msk1"))).toContain("offline");
    expect(head("msk1")!.className).toContain("bg-danger-soft");
    expect(row("de1")!.querySelectorAll(".rw-c")[0]!.querySelector(".rw-dash")).not.toBeNull();
    expect(document.body.textContent).toContain("From Russia: entry msk1 offline");
    expect(document.body.textContent).toContain("msk1 is offline: relays through it don’t work until it’s back");
  });
  it("an entry the country does not see, an old agent: each its note and its summary", async () => {
    await draw("rw-enthid");
    expect(text(head("msk1"))).toContain("not visible from Russia");
    expect(document.body.textContent).toContain("msk1 isn’t visible from Russia: people can’t reach any exit through it");
    act(() => root!.unmount());
    host!.remove();
    await draw("rw-oldagent");
    expect(text(head("msk1"))).toContain("agent is outdated · update");
    expect(document.body.textContent).toContain("Update the msk1 agent to pass relays");
  });
  it("tells when the measurements are old", async () => {
    await draw("rw-stale");
    expect(document.body.textContent).toContain("Measurements from Russia are 2 days old");
  });
  it("loading is a skeleton and no table; an error says so and retries; nothing to start from", async () => {
    await draw("rw-load");
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(document.querySelector(".rw-mx")).toBeNull();
    expect(document.body.textContent).toContain("Loading paths…");
    expect(button(en["nw.wiz"].replace("{country}", "Russia"))).toBeUndefined();
    act(() => root!.unmount());
    host!.remove();
    await draw("rw-err");
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["nw.err.t"]);
    expect(button(en["common.retry"])).toBeDefined();
    expect(document.querySelector(".rw-mx")).toBeNull();
  });
  it("the three empty states: no relay needed, no entry, no measurement", async () => {
    await draw("rw-empty");
    expect(document.body.textContent).toContain("All nodes are reachable from Russia directly");
    expect(button(en["nw.emp.route"])).toBeDefined();
    expect(document.querySelector(".rw-mx")).toBeNull();
    act(() => root!.unmount());
    host!.remove();
    await draw("rw-noent");
    expect(document.body.textContent).toContain("There’s no node that people from Russia can go through");
    expect(button(en["nw.emp.add"])).toBeDefined();
    expect(button("Make reachable from Russia…")).toBeUndefined();
    act(() => root!.unmount());
    host!.remove();
    await draw("rw-nomeas");
    expect(document.body.textContent).toContain("No measurements from Russia");
    expect(button("Make reachable from Russia…")).toBeDefined();
  });
  it("one node and no entry is the no-entry state", async () => {
    await draw("rw-one");
    expect(document.body.textContent).toContain("There’s no node that people from Russia can go through");
  });
  it("the Cloudflare edition has no Direct column and says why", async () => {
    await draw("rw-cf");
    expect(document.querySelector(".rw-mx.cf")).not.toBeNull();
    expect(document.body.textContent).toContain(en["nw.note.cf"]);
  });
  it("the wizard button opens the wizard of the exit that needs a way most (the one with no path)", async () => {
    await draw("rw-twelve");
    await click(button("Make reachable from Russia…"));
    expect(nav).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_de2" }, search: { tab: "ways", add: "1" } });
  });
  it("filters the rows by No path, Partly, Over the bridge; the chips that count nothing are not there", async () => {
    await draw("rw-twelve");
    const chip = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent?.startsWith(label));
    await click(chip("No path"));
    expect(rows()).toEqual(["de2"]);
    await click(chip("Partly"));
    expect(rows()).toEqual(["nl2"]);
    await click(chip("Over the bridge"));
    expect(rows()).toEqual(["nl1"]);
    await click(chip("All"));
    expect(rows()).toHaveLength(9);
    act(() => root!.unmount());
    host!.remove();
    await draw("rw-norm");
    expect([...document.querySelectorAll("[role=radio]")].some((r) => r.textContent?.startsWith("No path"))).toBe(false);
  });
  it("a helper has no Paths view, even for the address that asks for one", async () => {
    await draw("rw-norm", "helper");
    expect(document.querySelector(".rw-mx")).toBeNull();
    expect([...document.querySelectorAll("[role=radio]")].map((r) => r.textContent)).not.toContain(en["nw.view"]);
  });
  it("the same view in every language, right to left included", async () => {
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      setLang(lang);
      await draw("rw-br-near");
      expect(rows()).toHaveLength(9);
      expect(sheet()).not.toBeNull();
      expect(document.body.textContent).not.toContain("nw.");
      act(() => root!.unmount());
      host!.remove();
    }
  });
});

describe("the pair's sheet", () => {
  it("opens on a cell and closes on the cross, on Escape, and on the same cell again", async () => {
    await draw("rw-twelve");
    expect(sheet()).toBeNull();
    const cell = () => row("de1")!.querySelector(".rw-c button")!;
    await click(cell());
    expect(text(sheet()!.querySelector("header"))).toContain("de1");
    expect(text(sheet()!.querySelector("header"))).toContain("msk1");
    expect(sheet()!.textContent).toContain("Running");
    await click(cell());
    expect(sheet()).toBeNull();
    await click(cell());
    await click(sheet()!.querySelector(`[aria-label="${en["common.close"]}"]`));
    expect(sheet()).toBeNull();
    await click(cell());
    await act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(sheet()).toBeNull();
  });
  it("a pair of forwarding says how it works and does not offer a bridge's load", async () => {
    await draw("rw-sheet-fwd");
    const t = sheet()!.textContent!;
    expect(t).toContain("msk1 connects to de1 itself");
    expect(t).toContain("Forwarding");
    expect(t).not.toContain("almost full");
    expect(t).toContain("VLESS TCP");
    expect(t).toContain("HY2");
    expect(t).toContain("msk1 → de1");
  });
  it("a bridge nearly full says how full, why it is a bridge, and how it holds", async () => {
    await draw("rw-br-near");
    const t = sheet()!.textContent!;
    expect(t).toContain("nl1 connects to msk1 itself and keeps the line open");
    expect(t).toContain("msk1 does not reach nl1 directly");
    expect(t).toContain("2 of 2 connections");
    expect(t).toContain("196");
    expect(t).toContain("~230");
    expect(t).toContain("almost full");
    expect(sheet()!.querySelectorAll(".rw-sg u.on")).toHaveLength(9);
    expect(t).toContain("31 Mbit/s");
  });
  it("a bridge at its limit says people are turned away, and offers another entry", async () => {
    await draw("rw-br-lim");
    const t = sheet()!.textContent!;
    expect(t).toContain(en["relay.bridge.atLimit"]);
    expect(t).toContain("New people are refused over the bridge");
    expect(button("Another entry for nl1…", sheet()!)).toBeDefined();
    expect(sheet()!.querySelector(".rw-sg")!.className).toContain("lim");
  });
  it("a bridge that is not held says why, with the agent's word, and offers to check", async () => {
    await draw("rw-br-drop");
    const t = sheet()!.textContent!;
    expect(t).toContain("The bridge is down: ");
    expect(t).toContain("connect");
    expect(button("Check nl1 → msk1", sheet()!)).toBeDefined();
    expect(sheet()!.textContent).toContain("Not working");
  });
  it("a pair with a UDP server that fails says the bridge does not take it", async () => {
    await draw("rw-br-udp");
    expect(sheet()!.textContent).toContain("HY2 does not go over the bridge yet");
    expect(sheet()!.textContent).toContain("Partly");
  });
  it("a pair with one server of two failing says which and since when, and offers another entry", async () => {
    await draw("rw-sheet-part");
    expect(sheet()!.textContent).toContain("Partly");
    expect(sheet()!.textContent).toMatch(/Since \d{1,2}:\d{2}/);
    expect(button(en["nw.sv.other"], sheet()!)).toBeDefined();
  });
  it("Check only runs both ways on every relay of the pair and says nothing was switched", async () => {
    await draw("rw-br-near");
    await click(button(en["nw.m.check"], sheet()!));
    // both relays of nl1 through msk1 may use a bridge: the exit's reach to the entry comes with the leg
    expect(api.checkRelay).toHaveBeenCalledTimes(2);
    expect(api.checkRelay).toHaveBeenCalledWith({ id: "rel_nl1_msk1_0", reverseReach: true });
    expect(sheet()!.textContent).toContain(en["nw.k.checked"]);
    expect(api.updateRelay).not.toHaveBeenCalled();
  });
  it("the speed check asks one relay that can take a bridge, and tells it is under way and done", async () => {
    await draw("rw-br-near");
    await click(button(en["nw.sp.run"], sheet()!));
    expect(api.checkRelay).toHaveBeenCalledWith({ id: "rel_nl1_msk1_0", reverseSpeed: true });
    expect(sheet()!.textContent).toContain("The measurement goes to a separate port on msk1");
  });
  it("the choice of the way changes the mode of every relay that can take it", async () => {
    await draw("rw-sheet-mode");
    expect(sheet()!.textContent).toContain(en["nw.mode.udp"]);
    await click(button(en["nw.mode.opt.dnat"], sheet()!));
    expect(api.updateRelay.mock.calls.map((c) => c as unknown)).toEqual([[{ id: "rel_nl1_msk1_0", mode: "dnat" }], [{ id: "rel_nl1_msk1_1", mode: "dnat" }]]);
  });
  it("removing the last way asks first, says who loses what, and removes every relay of the pair", async () => {
    await draw("rw-sheet-ask");
    const t = sheet()!.textContent!;
    expect(t).toContain("Remove 3 relays of de1 through msk1?");
    expect(t).toContain("People lose “DE · via RU”");
    expect(t).toContain("de1 will have no path from Russia: it doesn’t open directly");
    expect(t).toContain("AmneziaVPN");
    await click(button(en["relay.remove"], sheet()!));
    expect(api.deleteRelay.mock.calls).toHaveLength(3);
  });
  it("asking is no removal: the first press only asks, and Cancel takes it back", async () => {
    await draw("rw-sheet-fwd");
    await click(button("Remove the path through msk1…", sheet()!));
    expect(sheet()!.textContent).toContain("Remove 3 relays");
    expect(api.deleteRelay).not.toHaveBeenCalled();
    await click(button(en["common.cancel"], sheet()!));
    expect(sheet()!.textContent).not.toContain("Remove 3 relays");
    expect(api.deleteRelay).not.toHaveBeenCalled();
  });
  it("More servers opens the wizard of the exit at the servers of this entry", async () => {
    await draw("rw-sheet-fwd");
    await click(button(en["nw.a.more"], sheet()!));
    expect(nav).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_de1" }, search: { tab: "ways", add: "via:nod_msk1" } });
  });
  it("links to both nodes' pages on the ways tab", async () => {
    await draw("rw-sheet-fwd");
    const links = [...sheet()!.querySelectorAll("a")].map((a) => a.textContent);
    expect(links).toEqual(["Open de1 ›", "Open msk1 ›"]);
  });
  it("the sheet of an owner's pair is the owner's: Role says so", () => {
    expect(roleOfScene("rw-norm", null)).toBe(Role.OWNER);
  });
});
