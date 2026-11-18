import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { LeaveGuard } from "./guard";
import { geoHosts, linkMode, routingLimit, routingRefusal, type RoutingInfo, type RoutingProfile, type Settings } from "./model";
import { RoutingTab } from "./routing";

// Subscriptions -> Routing: the list with what each profile holds, a pasted text that the panel reads (or refuses, with its reason), the
// mode a link says, the groups (one profile for a group, one for all), the long profile that goes on a line of the subscription, a save
// that sends what was pasted and the panel's refusal of one.

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
const previewRoutingProfile = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
    previewRoutingProfile: (...a: unknown[]) => previewRoutingProfile(...a),
  },
  groups: { listGroups: async () => ({ groups: [{ id: "grp_family", name: "FAMILY" }, { id: "grp_friends", name: "FRIENDS" }] }) },
  users: { listUsers: async () => ({ users: [] }) },
}));

type Blocker = { status: "idle" | "blocked"; proceed?: () => void; reset?: () => void };
let blocker: Blocker = { status: "idle" };
vi.mock("@tanstack/react-router", async (orig) => ({ ...(await orig<object>()), useBlocker: () => blocker }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings, previewRoutingProfile]) m.mockReset();
  blocker = { status: "idle" };
});

const info = (o: Partial<RoutingInfo> = {}): RoutingInfo =>
  ({ name: "family", directSites: 2, directIp: 1, proxySites: 1, proxyIp: 0, blockSites: 3, blockIp: 0, remoteDnsType: "DoH", remoteDns: "https://dns.example.com/q", domesticDnsType: "DoU", domesticDns: "192.0.2.2", hosts: 0, geoipAddress: "https://geo.example.com/geoip.dat", geositeAddress: "https://geo.example.com/geosite.dat", globalProxy: "true", domainStrategy: "", fakeDns: "", lastUpdated: "", useChunkFiles: false, droppedFields: [], profileBytes: 400, headerBytes: 700, ...o }) as RoutingInfo;
const prof = (profile: string, o: Partial<RoutingProfile> & { i?: Partial<RoutingInfo> } = {}): RoutingProfile => {
  const { i, ...rest } = o;
  return { profile, onlyAdd: false, onlyGroups: false, groupIds: [], info: info({ name: /"Name":"([^"]*)"/.exec(profile)?.[1] ?? "x", ...i }), ...rest } as RoutingProfile;
};
const doc = (list: RoutingProfile[]) => ({ title: "", rules: [], apps: [], routingProfiles: list }) as unknown as Settings;
const FAMILY = '{"Name":"family","DirectSites":["domain:example.ru"]}';

let renew: ((s: Settings) => void) | null = null;
function Renewed({ settings }: { settings: Settings }) {
  const [now, setNow] = useState(settings);
  useEffect(() => {
    renew = setNow;
  }, []);
  return <RoutingTab data={{ settings: now }} />;
}

async function mount(list: RoutingProfile[]) {
  const settings = doc(list);
  getSubscriptionSettings.mockResolvedValue({ settings, effectiveTitle: "", reservedHeaderNames: [] });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <LeaveGuard.Provider value={() => {}}>
            <Renewed settings={settings} />
          </LeaveGuard.Provider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async (ms = 0) => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, ms))));
};
const click = (b: Element) => act(async () => void b.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const buttonWith = (text: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
const text = () => document.body.textContent ?? "";
const heads = () => [...document.querySelectorAll<HTMLButtonElement>("button.hx-rh")];
const box = () => document.querySelector<HTMLTextAreaElement>("textarea.rt-y")!;
const chip = (group: string, label: string) => [...document.querySelectorAll(`[role=group][aria-label="${group}"] button`)].find((b) => b.textContent === label)!;
const saveBtn = () => buttonWith("Save") as HTMLButtonElement;
const paste = async (v: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), v);
    box().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle(120); // the panel is asked 0.4 s after the last key
};
const sent = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.routingProfiles;
const refuse = (m: string) => new ConnectError(`invalid subscription settings: ${m}`, Code.InvalidArgument);

describe("the words around a profile", () => {
  it("reads the mode a link says, and nothing else", () => {
    expect(linkMode("happ://routing/onadd/e30=")).toBe("onadd");
    expect(linkMode(" incy://routing/add/e30=")).toBe("add");
    expect(linkMode("://routing/ONADD/e30=")).toBe("onadd");
    expect(linkMode("happ://routing/off")).toBeNull();
    expect(linkMode('{"Name":"a"}')).toBeNull();
  });
  it("takes the path off a refusal, and says which profile", () => {
    expect(routingRefusal("invalid subscription settings: routing_profiles[1].profile: Geoipurl: an https address (https://host/path)")).toEqual({ text: "Geoipurl: an https address (https://host/path)", index: 1 });
    expect(routingRefusal("invalid subscription settings: routing_profiles[0].group_ids: the group \"g\" is already in the profile \"a\"").index).toBe(0);
    expect(routingRefusal("something else")).toEqual({ text: "something else" });
  });
  it("lists the hosts of the geo files once", () => {
    expect(geoHosts({ geoipAddress: "https://geo.example.com/a.dat", geositeAddress: "https://geo.example.com/b.dat" })).toEqual(["geo.example.com"]);
    expect(geoHosts({ geoipAddress: "", geositeAddress: "" })).toEqual([]);
    expect(routingLimit.header).toBe(3000);
  });
});

describe("the list", () => {
  it("shows each profile with its groups, mode and what it holds, and the first one in words", async () => {
    await mount([prof(FAMILY, { onlyGroups: true, groupIds: ["grp_family"], onlyAdd: true }), prof('{"Name":"rest","DirectSites":[]}', { i: { directSites: 0, directIp: 0, proxySites: 0, blockSites: 0, headerBytes: 4000 } })]);
    expect(heads()).toHaveLength(2);
    expect(heads()[0]!.textContent).toContain("family");
    expect(heads()[0]!.textContent).toContain("FAMILY · add only");
    expect(heads()[0]!.textContent).toContain("3 direct · 1 via VPN · 3 blocked");
    expect(heads()[1]!.textContent).toContain("all groups · add and turn on");
    const t = text();
    expect(t).toContain("sites: 2 · addresses: 1"); // the first profile is open: the summary of it
    expect(t).toContain("geo.example.com");
    expect(t).toContain("The routing header (700 bytes)");
    expect(t).toContain("The DNS preset of each person");
    expect(t).toContain("through the VPN"); // GlobalProxy true
    await click(heads()[1]!);
    expect(text()).toContain("A line of the subscription after the servers (4000 bytes: the header holds 3000)");
  });
  it("says what there is to do while it is empty", async () => {
    await mount([]);
    expect(text()).toContain("No routing profile");
    expect(text()).toContain("Open a profile to see what the apps get from it.");
  });
});

describe("a pasted text", () => {
  it("is read by the panel; a link sets the mode, and the summary shows what was found and what was left out", async () => {
    previewRoutingProfile.mockResolvedValue({ profile: prof(FAMILY, { onlyAdd: true, i: { name: "pasted", droppedFields: ["Odd"] } }) });
    await mount([]);
    await click(buttonWith("Add a profile"));
    await paste("incy://routing/add/e30=");
    expect(previewRoutingProfile).toHaveBeenCalledWith({ input: "incy://routing/add/e30=" }, expect.anything());
    expect(chip("The app", "Only adds it").getAttribute("aria-pressed")).toBe("true");
    expect(text()).toContain("pasted");
    expect(text()).toContain("Left out");
    expect(text()).toContain("Odd");
    expect(saveBtn().disabled).toBe(false); // a new profile, read, for all groups: it can be saved
  });
  it("that the panel cannot read says why and cannot be saved", async () => {
    previewRoutingProfile.mockRejectedValue(refuse("Geoipurl: an https address (https://host/path)"));
    await mount([]);
    await click(buttonWith("Add a profile"));
    await paste('{"Geoipurl":"http://x"}');
    expect(text()).toContain("The panel cannot read this: Geoipurl: an https address (https://host/path)");
    expect(saveBtn().disabled).toBe(true);
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
  });
  it("is saved as it was pasted, with the mode and the groups chosen", async () => {
    previewRoutingProfile.mockResolvedValue({ profile: prof(FAMILY, { i: { name: "family" } }) });
    await mount([]);
    await click(buttonWith("Add a profile"));
    await paste(`happ://routing/onadd/e30=`);
    await click(chip("Groups", "FAMILY"));
    expect(saveBtn().disabled).toBe(false);
    await click(saveBtn());
    await settle();
    expect(sent()).toEqual([{ profile: "happ://routing/onadd/e30=", onlyAdd: false, onlyGroups: true, groupIds: ["grp_family"] }]);
  });
  it("shows the panel's refusal of the save at the profile it names", async () => {
    previewRoutingProfile.mockResolvedValue({ profile: prof(FAMILY) });
    updateSubscriptionSettings.mockReset();
    await mount([]);
    updateSubscriptionSettings.mockRejectedValue(refuse("routing_profiles[0].profile: the name \"family\" is used by another profile"));
    await click(buttonWith("Add a profile"));
    await paste(FAMILY);
    await click(saveBtn());
    await settle();
    expect(text()).toContain("The panel did not save the profiles: the name \"family\" is used by another profile");
  });
});

describe("the groups", () => {
  it("a group is in one profile, and one profile is for all groups", async () => {
    await mount([prof(FAMILY, { onlyGroups: true, groupIds: ["grp_family"] }), prof('{"Name":"b","DirectSites":[]}', { onlyGroups: true, groupIds: ["grp_friends"] })]);
    await click(heads()[1]!);
    await click(chip("Groups", "FAMILY"));
    expect(text()).toContain("“FAMILY” is in another profile too");
    expect(saveBtn().disabled).toBe(true);
    await click(chip("Groups", "FAMILY")); // back
    await click(chip("Groups", "All groups"));
    await click(heads()[0]!);
    await click(chip("Groups", "All groups"));
    expect(text()).toContain("Only one profile can be for all groups.");
    expect(saveBtn().disabled).toBe(true);
  });
  it("two profiles of one name are refused", async () => {
    await mount([prof(FAMILY, { onlyGroups: true, groupIds: ["grp_family"] }), prof('{"Name":"Family","DirectSites":[]}', { onlyGroups: true, groupIds: ["grp_friends"] })]);
    expect(text()).toContain("Another profile is called");
    expect(saveBtn().disabled).toBe(true);
  });
  it("a profile for chosen groups with none chosen is not saved", async () => {
    await mount([prof(FAMILY)]);
    await click(chip("Groups", "FAMILY"));
    await click(chip("Groups", "FAMILY"));
    expect(text()).toContain("Pick a group, or choose all groups");
  });
});

describe("the form after a save and changes not saved", () => {
  it("takes the changes back", async () => {
    await mount([prof(FAMILY)]);
    await click(chip("The app", "Only adds it"));
    expect(buttonWith("Discard")).toBeTruthy();
    await click(buttonWith("Discard"));
    expect(heads()[0]!.textContent).toContain("add and turn on");
    expect(renew).toBeTruthy();
  });
});
