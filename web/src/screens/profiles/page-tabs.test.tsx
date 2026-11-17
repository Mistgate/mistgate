import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { en as E } from "@/i18n/profilesrv";
import { groups, health, nodes, profiles } from "@/lib/api";
import { pageDataOf, partsOf, sceneOf, seeded } from "@/screens/dev-kit-profile";
import { tabsData } from "@/screens/dev-kit-profile-tabs";
import { ProfilePage } from "./page";

// What the Servers and People tabs and their windows do with the panel: the calls they make, with what, and what they say of the answers. The
// page is the real one on the dev kit's made-up data (its scenes also draw every state: dev-kit-profile-tabs.test.tsx).

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
const gone = vi.fn();
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
  setLang("en");
  document.body.innerHTML = "";
  gone.mockClear();
});

const settle = async (n = 12) => {
  for (let i = 0; i < n; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function draw(state: string, role = Role.OWNER) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfilePage data={tabsData(pageDataOf(partsOf(state).profile), state)} scene={sceneOf(state)} onGone={gone} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const qa = (s: string) => [...document.querySelectorAll(s)];
const q = (s: string) => document.querySelector(s);
const btn = (text: string) => qa("button, a").find((b) => b.textContent?.trim() === text) as HTMLElement | undefined;
const click = async (el: Element | undefined) => {
  expect(el).toBeDefined();
  await act(async () => (el as HTMLElement).click());
  await settle(6);
};
async function type(input: Element | null, value: string) {
  expect(input).not.toBeNull();
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(input, value);
    input!.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle(6);
}
const spy = <T extends object>(o: T, k: string) => vi.spyOn(o as never, k as never) as unknown as ReturnType<typeof vi.fn>;
const row = (name: string) => qa(".pp-pn").find((r) => r.querySelector("b")?.textContent === name)!;

describe("put on nodes", () => {
  it("puts the profile on the ticked nodes, one CreateInbound each, and says each one's result", async () => {
    await draw("hy1.servers.put");
    const create = spy(profiles, "createInbound");
    await click(btn("Put on 6 nodes"));
    await settle(30);
    const real = create.mock.calls.map((c) => c[0]).filter((r) => !r.validateOnly);
    expect(real.map((r: { nodeId: string }) => r.nodeId).sort()).toEqual(["nod_de2", "nod_de3", "nod_fi2", "nod_msk2", "nod_nl2", "nod_nl3"]);
    expect(real.every((r: { profileId: string }) => r.profileId === "prf_hy1")).toBe(true);
    expect(q("[role=dialog] .rw-rh b")?.textContent).toBe("Running on 6 of 6");
    expect(qa(".pp-pn em").map((e) => e.textContent)).toEqual(Array(6).fill("Running"));
  });

  it("asks for a domain where Let's Encrypt cannot work on an IP, checks again with it and ticks the node", async () => {
    await draw("hy1.servers.put");
    expect(row("fi3").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("false");
    const set = btn(E["ps.pt.setDomain"]);
    expect(set?.getAttribute("aria-disabled")).toBe("true");
    await type(row("fi3").querySelector("input"), "vpn.example.com");
    await click(btn(E["ps.pt.setDomain"]));
    await settle(10);
    expect(row("fi3").querySelector("em")?.textContent).toBe("Possible · domain set");
    expect(row("fi3").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
    expect(btn("Put on 7 nodes")).toBeDefined();
  });

  it("takes the free port a refusal names, so the node is ticked from the start", async () => {
    await draw("hy1.servers.put");
    expect(row("nl2").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
    expect(row("nl2").querySelector("em")?.textContent).toContain("444 is free");
  });

  it("leaves a node that is an entry unticked, and one with an old agent not tickable", async () => {
    await draw("hy1.servers.put");
    expect(row("msk1").querySelector("[role=checkbox]")?.getAttribute("aria-checked")).toBe("false");
    expect(row("spb1").querySelector("[role=checkbox]")?.getAttribute("aria-disabled")).toBe("true");
  });

  it("offers to retry a node that failed, and to take the profile off where it failed", async () => {
    await draw("hy1.servers.put.run.fail.on-de2+fi2");
    await settle(30);
    const remove = spy(profiles, "removeProfileFromNodes");
    await click(btn(E["ps.pt.rmFailed"]));
    expect(remove).toHaveBeenCalledWith({ profileId: "prf_hy1", nodeIds: ["nod_fi2"], deleteProfile: false, confirmName: "" });
  });

  it("is the owner's: nothing opens for a helper", async () => {
    await draw("hy1.servers.put", Role.HELPER);
    expect(q("[role=dialog]")).toBeNull();
  });
});

describe("taking off and deleting", () => {
  it("takes the ticked nodes off in one call and keeps the profile", async () => {
    await draw("hy1.servers.take.pick-de1+nl1");
    const remove = spy(profiles, "removeProfileFromNodes");
    await click(btn("Take off 2 nodes"));
    expect(remove).toHaveBeenCalledWith({ profileId: "prf_hy1", nodeIds: ["nod_de1", "nod_nl1"], deleteProfile: false, confirmName: "" });
    expect(document.body.textContent).toContain("Taken off 2 nodes");
    expect(gone).not.toHaveBeenCalled();
  });

  it("deletes only once the name is typed, in the same call that takes it off every node, and tells the page", async () => {
    await draw("hy1.servers.del");
    const remove = spy(profiles, "removeProfileFromNodes");
    await click(btn("Take off all and delete"));
    expect(remove).not.toHaveBeenCalled();
    await type(q("[role=dialog] label input"), "Main · 443");
    await click(btn("Take off all and delete"));
    expect(remove).toHaveBeenCalledWith({ profileId: "prf_hy1", nodeIds: ["nod_de1", "nod_fi1", "nod_nl1"], deleteProfile: true, confirmName: "Main · 443" });
    expect(gone).toHaveBeenCalled();
  });

  it("with the switch off only takes it off the nodes, and the name is not asked", async () => {
    await draw("hy1.servers.del");
    await click(q("[role=dialog] .fz-sw") ?? undefined);
    expect(q("[role=dialog] label input")).toBeNull();
    const remove = spy(profiles, "removeProfileFromNodes");
    await click(btn("Take off 3 nodes"));
    expect(remove).toHaveBeenCalledWith(expect.objectContaining({ deleteProfile: false }));
    expect(gone).not.toHaveBeenCalled();
  });

  it("deletes a profile that is on no node with one DeleteProfile call", async () => {
    await draw("hy3.servers.del");
    const del = spy(profiles, "deleteProfile");
    await click(btn("Delete"));
    expect(del).toHaveBeenCalledWith({ profileId: "prf_hy3" });
    expect(gone).toHaveBeenCalled();
  });

  it("says why when the panel refuses, and keeps the window", async () => {
    await draw("hy1.servers.del.typed");
    spy(profiles, "removeProfileFromNodes").mockRejectedValue(Object.assign(new Error("[not_found] profile not found"), { code: 5, rawMessage: "profile not found" }));
    await click(btn("Take off all and delete"));
    expect(q("[role=dialog] .pp-xne")).not.toBeNull();
    expect(gone).not.toHaveBeenCalled();
  });

  it("says what the click costs: the relay that goes with the server, and the AmneziaWG keys", async () => {
    await draw("hy1.servers.take.pick-de1");
    expect(qa(".pp-imp").map((i) => i.textContent).join("|")).toMatch(/relay/i);
    act(() => root?.unmount());
    host?.remove();
    await draw("aw1.servers.del.typed");
    expect(qa(".pp-imp").map((i) => i.textContent).join("|")).toContain("AmneziaWG devices are revoked");
  });
});

describe("the copy", () => {
  it("makes the copy with the planned port, leaving out the node without WARP that was unticked", async () => {
    await draw("aw1.servers.twin");
    const twin = spy(profiles, "twinProfile");
    expect(qa("[role=dialog] input[type=checkbox]").map((c) => (c as HTMLInputElement).checked)).toEqual([true, false]);
    await click(btn(E["ps.tw.make"]));
    expect(twin).toHaveBeenCalledWith({ profileId: "prf_aw1", egress: "warp", port: 47214, skipNodeIds: ["nod_nl1"] });
    expect(q("[role=dialog] h2")?.textContent).toContain("is made");
  });

  it("waits while changes are typed", async () => {
    await draw("aw1.servers.twin");
    expect(btn(E["ps.tw.make"])?.getAttribute("aria-disabled")).toBe("false");
  });
});

describe("the servers", () => {
  it("restarts a server, switches it off, leaves its address out of the subscriptions", async () => {
    await draw("hy1.servers.menu");
    const restart = spy(nodes, "restartInbounds");
    await click(qa("[role=menuitem]").find((i) => i.textContent === "Restart"));
    expect(restart).toHaveBeenCalledWith({ nodeId: "nod_fi1", inboundId: "inb_hy1_fi1" });
    const update = spy(profiles, "updateInbound");
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.menu");
    const upd = spy(profiles, "updateInbound");
    await click(qa("[role=menuitem]").find((i) => i.textContent === "Turn off"));
    expect(upd).toHaveBeenCalledWith({ inboundId: "inb_hy1_fi1", enabled: false });
    expect(update).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    await draw("hy1.servers.menu");
    const upd2 = spy(profiles, "updateInbound");
    await click(qa("[role=menuitem]").find((i) => i.textContent?.startsWith("Remove the node")));
    expect(upd2).toHaveBeenCalledWith({ inboundId: "inb_hy1_fi1", directInSubscriptions: false });
  });

  it("takes a server off its node only at the second click in the menu", async () => {
    await draw("hy1.servers.menu");
    const remove = spy(profiles, "removeProfileFromNodes");
    const last = () => qa("[role=menuitem]").at(-1);
    await click(last());
    expect(remove).not.toHaveBeenCalled();
    expect(last()?.textContent).toBe("Really remove from fi1?");
    await click(last());
    expect(remove).toHaveBeenCalledWith({ profileId: "prf_hy1", nodeIds: ["nod_fi1"], deleteProfile: false, confirmName: "" });
  });

  it("saves a changed port of one server with UpdateInbound, after asking the panel", async () => {
    await draw("hy1.servers.edit.port");
    const input = q(".pp-ed input.an-i") as HTMLInputElement;
    expect(input.value).toBe("1443");
    const upd = spy(profiles, "updateInbound");
    await type(input, "8443");
    await click(btn("Save"));
    expect(upd).toHaveBeenCalledWith(expect.objectContaining({ inboundId: "inb_hy1_fi1", portOverride: 8443 }));
  });

  it("asks the panel to check now and walks the working servers", async () => {
    await draw("vl1.servers");
    const run = spy(health, "runChecksNow");
    await click(btn("Check now"));
    expect(run).toHaveBeenCalled();
    expect(qa(".pp-sv.cking").length + qa(".pp-sv.ckw").length).toBeGreaterThan(0);
  });
});

describe("the groups", () => {
  it("takes the profile out of a group with the old set minus this profile", async () => {
    await draw("hy1.people");
    const update = spy(groups, "updateGroup");
    await click(qa(".pp-grx")[0]);
    expect(update).toHaveBeenCalledWith({ groupId: "grp_family", profileIds: { values: expect.not.arrayContaining(["prf_hy1"]) } });
  });

  it("offers only the groups without the profile and adds it to the one chosen", async () => {
    await draw("vl1.people.menu", Role.HELPER);
    expect(qa("[role=menuitem]").map((i) => i.textContent)).toEqual(["Family9"]);
    const update = spy(groups, "updateGroup");
    await click(qa("[role=menuitem]")[0]);
    expect(update).toHaveBeenCalledWith({ groupId: "grp_family", profileIds: { values: expect.arrayContaining(["prf_vl1"]) } });
  });

  it("makes the first group with the profile in it when the panel has none", async () => {
    await draw("vl3.people.nogroups");
    const create = spy(groups, "createGroup");
    await click(btn("Make the group “Everyone” with this profile"));
    expect(create).toHaveBeenCalledWith({ name: "Everyone", profileIds: ["prf_vl3"] });
  });
});
