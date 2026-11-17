import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { en as E } from "@/i18n/profilesrv";
import { pageDataOf, partsOf, sceneOf, seeded } from "./dev-kit-profile";
import { tabFrames, tabsData } from "./dev-kit-profile-tabs";
import { ProfilePage } from "./profiles/page";

// The scenes of the Servers and People tabs and of the windows of the profile page (dev-kit-profile-tabs.tsx): every state a scene names must
// draw, in every language, and say what it is made to say. The page is the real one, on a private query cache.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, profiles: {}, groups: {}, users: {}, nodes: {}, health: {}, relays: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to: _t, render: _r, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string; render?: unknown }) => (
    <a className={className} {...rest}>
      {children}
    </a>
  ),
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
  document.body.innerHTML = "";
});

const states = [...new Set(tabFrames.map((f) => f.state))];

async function draw(state: string, role = Role.OWNER, wait = 12) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfilePage data={tabsData(pageDataOf(partsOf(state).profile), state)} scene={sceneOf(state)} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < wait; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const body = () => document.body.textContent ?? "";
const q = (s: string) => document.querySelector(s);
const qa = (s: string) => [...document.querySelectorAll(s)];
const btn = (text: string) => qa("button, a").find((b) => b.textContent?.trim() === text);
const dialog = () => q("[role=dialog]");

describe("the scenes of the Servers and People tabs and the windows", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\bps\.[a-z]+\./);
    expect(body()).not.toMatch(/\bpf\.[a-z]+\./);
  });

  it("lists the servers, the failed one first, with why it did not start", async () => {
    await draw("hy1.servers");
    const rows = qa(".pp-sv");
    expect(rows).toHaveLength(3);
    expect(rows[0]!.querySelector(".pp-sn")?.textContent).toContain("fi1");
    expect(rows[0]!.querySelector(".rw-why")?.textContent).toContain("udp/443");
    expect(rows[0]!.querySelector(".rw-why code")?.textContent).toContain("address already in use");
    expect(rows[0]!.querySelector(".rw-why button")?.textContent).toBe("Change the port on fi1…");
    expect(q(".pp-svt .pv-sum")?.textContent).toContain("1 did not start");
    // the relay through msk1 is on the server's row and in the relays card
    expect(rows[1]!.querySelector(".pp-ways")?.textContent).toContain("msk1");
    expect(qa(".pp-rl")).toHaveLength(1);
    expect(body()).toContain("Not yet deployed on:");
  });

  it("opens a row's menu and edit with the owner's rows", async () => {
    await draw("hy1.servers.menu");
    const items = qa("[role=menuitem]").map((i) => i.textContent);
    expect(items).toEqual(["Change port or domain…", "Restart", "Turn off", "Remove the node’s address from subscriptions", "Route through another node…", "Open node ›", "Remove from node…"]);
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.edit");
    expect(q(".pp-ed")).not.toBeNull();
    expect(q(".pp-ed input.an-i")).not.toBeNull();
  });

  it("asks once more in the menu before taking a server off", async () => {
    await draw("hy1.servers.menu.ask");
    expect(qa("[role=menuitem]").at(-1)?.textContent).toBe("Really remove from fi1?");
    expect(qa("[role=menuitem]").at(-1)?.className).toContain("ask");
  });

  it("says what the checks do while they load, fail or have not run", async () => {
    await draw("hy1.servers.load");
    expect(qa("[aria-busy=true] .pp-sksv")).toHaveLength(3);
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.err");
    expect(q(".pp-cke")?.textContent).toContain("The checks did not load");
    expect(qa(".pp-sv").length).toBeGreaterThan(0);
    act(() => root?.unmount());
    host?.remove();
    await draw("vl1.servers.nochecks");
    expect(body()).toContain("no check yet");
    act(() => root?.unmount());
    host?.remove();
    await draw("aw2.servers.out");
    expect(qa(".pp-sv .pv-pill").map((p) => p.textContent)).toContain("Node out of touch");
  });

  it("walks the working servers when the check is asked and says the apply course", async () => {
    await draw("vl1.servers.chk");
    expect(qa(".pp-sv.cking")).toHaveLength(1);
    expect(qa(".pp-sv.ckw")).toHaveLength(3);
    expect(btn("Checking")?.className).toContain("busy");
    act(() => root?.unmount());
    host?.remove();
    await draw("vl2.servers.apply");
    expect(q(".pp-apply .pp-aph b")?.textContent).toBe("Applying on 3 nodes · 1 of 3");
    expect(qa(".pp-apply .done")).toHaveLength(1);
    act(() => root?.unmount());
    host?.remove();
    await draw("vl1.servers.applied");
    expect(q(".pp-apply .pp-aph b")?.textContent).toBe("Applied on all nodes");
  });

  it("names the nodes that lack WARP, the devices of AmneziaWG and the certificate", async () => {
    await draw("aw1.servers.warp");
    expect(qa(".pp-s2")[0]?.textContent).toContain("12 devices, 7 online");
    expect(body()).toContain("No working WARP on");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.cert");
    expect(body()).toMatch(/5 d left/);
  });

  it("shows the empty state of a profile on no node, with Put on nodes for the owner only", async () => {
    await draw("hy3.servers");
    expect(q(".pr-none")?.textContent).toContain("isn’t on any node");
    expect(btn("Put on nodes…")).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    await draw("hy3.servers", Role.READONLY);
    expect(q(".pr-none")?.textContent).not.toContain("Put on nodes");
  });

  it("gives a helper the restart and an observer nothing that changes anything", async () => {
    await draw("hy1.servers.menu", Role.HELPER);
    expect(qa("[role=menuitem]").map((i) => i.textContent)).toEqual(["Restart", "Open node ›"]);
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers", Role.READONLY);
    expect(qa(".up-dots")).toHaveLength(0);
    expect(btn("Take off nodes…")).toBeUndefined();
    expect(btn("Put on nodes…")).toBeUndefined();
    expect(body()).not.toContain("Relays to these servers");
  });

  it("says who gets the profile and lets a helper change the groups", async () => {
    await draw("hy1.people");
    expect(qa(".pp-gr")).toHaveLength(2);
    expect(q(".pp-gr")?.textContent).toContain("Family");
    expect(q(".pp-quiet.pp-b")?.textContent).toBe("23 people · groups: 2");
    expect(qa(".pp-grx")).toHaveLength(2);
    act(() => root?.unmount());
    host?.remove();
    await draw("vl1.people.menu", Role.HELPER);
    expect(qa("[role=menuitem]").map((i) => i.textContent)).toEqual(["Family9"]);
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.people", Role.READONLY);
    expect(qa(".pp-grx")).toHaveLength(0);
    expect(btn("Add to group…")).toBeUndefined();
  });

  it("says a profile in no group, and offers the first group when the panel has none", async () => {
    await draw("vl3.people");
    expect(q(".pp-rmk.part")?.textContent).toContain("in no group");
    expect(btn("Add to group…")).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    await draw("vl3.people.nogroups");
    expect(btn("Make the group “Everyone” with this profile")).toBeDefined();
    expect(btn("Add to group…")).toBeUndefined();
  });

  it("checks every node before the click and ticks the ones that will work", async () => {
    await draw("hy1.servers.put");
    const rows = qa(".pp-pn");
    expect(rows).toHaveLength(9);
    const by = (n: string) => rows.find((r) => r.querySelector("b")?.textContent === n)!;
    expect(by("de2").querySelector("em")?.textContent).toBe("Possible · port 443 is free");
    expect(by("de2").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
    expect(by("nl2").querySelector("em")?.textContent).toContain("444");
    expect(by("fi3").querySelector(".pp-pnf input")).not.toBeNull();
    expect(by("fi3").className).toContain("need");
    expect(by("msk1").querySelector(".rw-role")).not.toBeNull();
    expect(by("msk1").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("false");
    expect(by("spb1").textContent).toContain("Open updates");
    expect(btn("Put on 6 nodes")).toBeDefined();
    expect(by("msk2").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
  });

  it("shows the check while it runs, the run, and what is left when it is done or failed", async () => {
    await draw("hy1.servers.put.pchk");
    expect(body()).toContain("Checking nodes…");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.put.run.hang.out.on-de2+nl3+fi2");
    expect(q(".rw-rh b")?.textContent).toMatch(/^Applying · \d of 3$/);
    expect(btn(E["ps.pt.putting"])).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.put.run.out.on-de2+nl3+fi2");
    expect(q(".rw-rh b")?.textContent).toBe("Running on 2 of 3: nl3 starts when it is back");
    expect(btn("Done")).toBeDefined();
    expect(qa(".pp-pn em").map((e) => e.textContent)).toContain("Running");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.put.run.fail.on-de2+fi2");
    expect(q(".rw-rh b")?.textContent).toBe("Running on 1 of 2: fi2 — failed");
    expect(btn("Retry for fi2")).toBeDefined();
    expect(btn(E["ps.pt.rmFailed"])).toBeDefined();
    expect(btn("Change the port on fi2…")).toBeDefined();
  });

  it("asks for a backend on the first AmneziaWG of a node", async () => {
    await draw("aw1.servers.put");
    expect(body()).toContain("First AmneziaWG on");
  });

  it("takes nodes off: nothing ticked, two ticked with the relay it removes, and every refusal the server can make", async () => {
    await draw("hy1.servers.take");
    expect(dialog()?.textContent).toContain("Choose the nodes to remove the profile from");
    expect(btn("Choose nodes")?.getAttribute("aria-disabled")).toBe("true");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.take.pick-de1+nl1");
    expect(qa(".pp-xnr.on")).toHaveLength(2);
    expect(qa(".pp-imp").map((i) => i.textContent).join("|")).toContain("Takes it off 2 nodes: de1, nl1");
    expect(body()).toMatch(/relay/i);
    expect(btn("Take off 2 nodes")?.getAttribute("aria-disabled")).toBe("false");
    for (const [k, title] of [["deployed", E["ps.dl.err.deployed.t"]], ["name", E["ps.dl.err.name.t"]], ["stale", E["ps.dl.err.stale.t"]], ["gone", E["ps.dl.err.gone.t"]]] as const) {
      act(() => root?.unmount());
      host?.remove();
      await draw(`hy1.servers.del.typed.err-${k}`);
      expect(q(".pp-xne")?.textContent).toContain(title);
    }
    expect(btn("Take off all and delete")?.getAttribute("aria-disabled")).toBe("true");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.take.pick-de1+nl1+fi1.err-partial");
    expect(q(".pp-xne")?.textContent).toContain(E["ps.dl.err.partial.t"]);
  });

  it("deletes: every node ticked and the name asked, a profile on no node at once", async () => {
    await draw("hy1.servers.del");
    expect(dialog()?.querySelector("h2")?.textContent).toBe("Delete “Main · 443”?");
    expect(qa(".pp-xnr.on")).toHaveLength(3);
    expect(q(".pp-xnd .fz-sw")?.getAttribute("aria-checked")).toBe("true");
    expect(btn("Take off all and delete")?.getAttribute("aria-disabled")).toBe("true");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.del.typed");
    expect(btn("Take off all and delete")?.getAttribute("aria-disabled")).toBe("false");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.del.typed.run");
    expect(btn("Deleting…")).toBeDefined();
    expect(qa(".pp-apr")).toHaveLength(3);
    act(() => root?.unmount());
    host?.remove();
    await draw("hy3.servers.del");
    expect(qa(".pp-xnr")).toHaveLength(0);
    expect(q(".pp-xnd")).toBeNull();
    expect(btn("Delete")?.getAttribute("aria-disabled")).toBe("false");
    expect(dialog()?.textContent).toContain("The profile is on no node: people lose nothing");
  });

  it("makes a copy through WARP or without it", async () => {
    await draw("aw1.servers.twin");
    expect(dialog()?.querySelector("h2")?.textContent).toBe("A copy through WARP");
    expect(dialog()?.textContent).toContain("udp/47214");
    expect(btn(E["ps.tw.make"])).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    await draw("aw1.servers.warp.twin");
    expect(dialog()?.querySelector("h2")?.textContent).toBe("A copy without WARP");
    act(() => root?.unmount());
    host?.remove();
    await draw("aw1.servers.twin.done");
    expect(dialog()?.querySelector("h2")?.textContent).toContain("is made");
    expect(btn("Open the copy")).toBeDefined();
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the tabs and the windows draw in %s", async (lang) => {
    for (const state of ["hy1.servers.menu", "hy1.people", "hy1.servers.put", "hy1.servers.del.typed.err-name", "aw1.servers.twin"]) {
      setLang(lang);
      await draw(state);
      expect(body()).not.toContain("undefined");
      expect(body()).not.toMatch(/\bps\.[a-z]+\./);
      act(() => root?.unmount());
      host?.remove();
    }
  });
});
