import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DeviceKind, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { dictionaries, fill, setLang, type T } from "@/i18n";
import { errorText } from "@/lib/errors";
import { meQuery } from "@/lib/session";
import { WaysOfGroup } from "./groups";
import { isTrustTunnelKey, splitDevices } from "./link-devices";
import type { DeviceN, UserN } from "./model";
import { TrustTunnelSection, pauseOf, trustTunnelShown } from "./trusttunnel-key";
import { DevicesPanel } from "./user-detail";

// The TrustTunnel key of a person on their card: the nine states of the brief, the refusals in words, a confirmation before
// the key is revoked, the key told apart from an AmneziaVPN key, and no secret left behind.

const getTrustTunnelLinks = vi.fn();
const issueTrustTunnelKey = vi.fn();
const revokeTrustTunnelKey = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  trustTunnel: {
    getTrustTunnelLinks: (...a: unknown[]) => getTrustTunnelLinks(...a),
    issueTrustTunnelKey: (...a: unknown[]) => issueTrustTunnelKey(...a),
    revokeTrustTunnelKey: (...a: unknown[]) => revokeTrustTunnelKey(...a),
  },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [] }, effectiveTitle: "" }) },
  profiles: { listProtocols: () => Promise.resolve({ protocols: [{ id: "hysteria2", apps: [App.HAPP] }, { id: "trusttunnel", apps: [App.TRUST_TUNNEL] }] }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params }: { children?: ReactNode; to: string; params?: { id: string } }) => <a href={params ? to.replace("$id", params.id) : to}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let qc: QueryClient;
beforeEach(() => setLang("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getTrustTunnelLinks, issueTrustTunnelKey, revokeTrustTunnelKey]) m.mockReset();
  localStorage.clear();
});

const owner = { admin: { id: "adm_1", role: Role.OWNER }, edition: "vps", unavailable: [] };
async function mount(ui: ReactNode, me: object = owner) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const rows = () => [...document.querySelectorAll("section li")];
const links = () => [...document.querySelectorAll(".tq-sv > div")];
// the dictionary as the page reads it (errorText takes a T)
const tOf = () => ((k: string, v?: Record<string, string | number>) => fill((dictionaries.en as Record<string, string>)[k] ?? k, v)) as unknown as T;

const user = (over: object = {}) => ({ id: "usr_alice", name: "alice", groupName: "Family", deviceLimit: 3, devicesUsed: 2, status: UserStatus.ACTIVE, apps: { happ: true, amnezia: false, telegram: false, trustTunnel: true }, ...over }) as unknown as UserN;
const url = "tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20FEHVhbGljZTIzNDU2NzIzNDU";
const link = (over: object = {}) => ({ nodeId: "nod_de1", nodeName: "de1", profileId: "prf_tt", profileName: "TrustTunnel · 443", url, hasIpv6: false, relay: false, via: "", note: "", ...over });
const key = (over: object = {}) => ({ hasKey: true, keyDeviceId: "dev_tt", keyIssuedUnix: BigInt(1_790_000_000), links: [link()], ...over });
const section = (u: UserN, extra: Partial<Parameters<typeof TrustTunnelSection>[0]> = {}) => (
  <TrustTunnelSection user={u} hasKeyDevice={false} countryOf={() => "DE"} onToggle={vi.fn()} onChanged={vi.fn()} {...extra} />
);

describe("the TrustTunnel key of a person: the states", () => {
  it("1. the switch is off: a line and the switch, which turns it on; nothing is asked of the panel", async () => {
    const onToggle = vi.fn();
    await mount(section(user({ apps: { happ: true, trustTunnel: false } }), { onToggle }));
    expect(text()).toContain("Off: the person has no tt:// links");
    expect(getTrustTunnelLinks).not.toHaveBeenCalled();
    await click(document.querySelector("[role=switch]"));
    expect(onToggle).toHaveBeenCalledWith(true);
  });

  it("2. on, an active person, no key: \"Issue key\" makes one and shows its links", async () => {
    getTrustTunnelLinks.mockResolvedValue({ hasKey: false, links: [] });
    issueTrustTunnelKey.mockResolvedValue({ trustTunnel: key() });
    const onChanged = vi.fn();
    await mount(section(user(), { onChanged }));
    expect(text()).toContain("No TrustTunnel key: the links need one.");
    await click(button("Issue key"));
    await settle();
    expect(issueTrustTunnelKey).toHaveBeenCalledWith({ userId: "usr_alice" });
    expect(onChanged).toHaveBeenCalled();
    expect(rows()[0]!.textContent).toContain("TrustTunnel key");
    expect(rows()[0]!.textContent).toContain("takes 1 place");
    expect(rows()).toHaveLength(1);
    expect(links()).toHaveLength(1);
  });

  it("2. on, a person who is disabled, expired or over the quota, no key: no button that would be refused, and why", async () => {
    getTrustTunnelLinks.mockResolvedValue({ hasKey: false, links: [] });
    for (const [status, words] of [
      [UserStatus.DISABLED, "The person is disabled: enable them to issue a key."],
      [UserStatus.EXPIRED, "The person’s term has ended: extend it to issue a key."],
      [UserStatus.LIMITED, "The person’s quota is used up: lift it to issue a key."],
    ] as const) {
      await mount(section(user({ status })));
      expect(text()).toContain(words);
      expect(button("Issue key")).toBeUndefined();
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("3. a key and a person who is disabled or whose term ended: paused, the key kept, no links to hand over", async () => {
    getTrustTunnelLinks.mockResolvedValue(key({ links: [] }));
    await mount(section(user({ status: UserStatus.DISABLED })));
    expect(text()).toContain("Paused");
    expect(text()).toContain("Paused: the person is disabled. The key is kept and works again once they are enabled.");
    expect(rows()).toHaveLength(1); // the key (the note is the line under the head)
    expect(links()).toHaveLength(0);
    expect(document.querySelector("svg[role=img]")).toBeNull();
    expect(button("Copy link")).toBeUndefined();
    act(() => root?.unmount());
    host?.remove();
    await mount(section(user({ status: UserStatus.EXPIRED })));
    expect(text()).toContain("Paused: the person’s term has ended.");
  });

  it("4. a key and no server for it: says why, and where the profile is made", async () => {
    getTrustTunnelLinks.mockResolvedValue(key({ links: [] }));
    await mount(section(user()));
    expect(text()).toContain("No links yet: no TrustTunnel profile runs on the nodes of this person’s group.");
    expect(document.querySelector('a[href="/profiles"]')?.textContent).toBe("TrustTunnel profiles");
    expect(text()).not.toContain("Paused");
  });

  it("5. a server with a self-signed certificate: no link, no QR, \"needs a domain with a public certificate\", and the way to its profile", async () => {
    getTrustTunnelLinks.mockResolvedValue(key({ links: [link({ url: "", note: "self_signed", nodeName: "fi1", profileId: "prf_self" })] }));
    await mount(section(user()));
    const row = links()[0]!;
    expect(row.textContent).toContain("fi1");
    expect(row.textContent).toContain("Needs a domain with a public certificate");
    expect(row.querySelector("button")).toBeNull();
    expect(row.querySelector('a[href="/profiles/prf_self"]')).not.toBeNull();
    expect(text()).toContain("only the Mistgate app trusts it");
  });

  it("6 and 7. a relay's link is marked \"via RU\" and one with IPv6 on its exit \"IPv6\"", async () => {
    getTrustTunnelLinks.mockResolvedValue(key({ links: [link(), link({ hasIpv6: true, url: `${url}2` }), link({ relay: true, via: "RU", url: `${url}3` })] }));
    await mount(section(user()));
    expect(links().map((r) => r.querySelector("b")!.textContent + r.querySelector(".tq-mks")!.textContent)).toEqual(["de1", "de1IPv6", "de1via RU"]);
  });

  it("8. a refusal at the device limit stays in the block, in words, with who holds the places", async () => {
    getTrustTunnelLinks.mockResolvedValue({ hasKey: false, links: [] });
    issueTrustTunnelKey.mockRejectedValue(new ConnectError("device_limit: 3/3: alice-phone, laptop, router", Code.FailedPrecondition));
    await mount(section(user()));
    await click(button("Issue key"));
    await settle();
    const alert = [...document.querySelectorAll("[role=alert]")].find((n) => n.textContent?.includes("device limit"));
    expect(alert?.textContent).toBe("The device limit is reached (3 of 3): alice-phone, laptop, router. Raise the limit or remove a device first.");
    expect(button("Issue key")).toBeDefined(); // the person may try again once a place is free
  });

  it("9. revoking asks first; nothing is revoked until it is confirmed, and then the links go and the button to issue returns", async () => {
    getTrustTunnelLinks.mockResolvedValue(key());
    revokeTrustTunnelKey.mockResolvedValue({ trustTunnel: { hasKey: false, links: [] } });
    const onChanged = vi.fn();
    await mount(section(user(), { onChanged }));
    await click(button("TrustTunnel key actions"));
    await click([...document.querySelectorAll("[role=menuitem]")].find((i) => i.textContent === "Revoke key"));
    expect(text()).toContain("Revoke the TrustTunnel key?");
    expect(text()).toContain("the key’s place in the device limit is freed");
    expect(revokeTrustTunnelKey).not.toHaveBeenCalled();
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Revoke key"));
    await settle();
    expect(revokeTrustTunnelKey).toHaveBeenCalledWith({ userId: "usr_alice" });
    expect(onChanged).toHaveBeenCalled();
    expect(button("Issue key")).toBeDefined();
  });

  it("cancelling the question revokes nothing", async () => {
    getTrustTunnelLinks.mockResolvedValue(key());
    await mount(section(user()));
    await click(button("TrustTunnel key actions"));
    await click([...document.querySelectorAll("[role=menuitem]")].find((i) => i.textContent === "Revoke key"));
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Cancel"));
    expect(revokeTrustTunnelKey).not.toHaveBeenCalled();
  });
});

describe("the link of a key", () => {
  it("is copied or scanned, never printed; the QR code is drawn on request, for a link as long as a real one", async () => {
    const real = `tt://?${"AAEBAQ9kZTEuZXhhbXBsZS5jb20FEHVhbGljZTIzNDU2NzIzNDUGIGFCMjM0NTY3Y0QyMzQ1NjdlRjIzNDU2N2dIMjM0NTY3AhNkZTEuZXhhbXBsZS5jb206NDQzBAEACQEBDANkZTENFgoxOTIuMC4yLjUzCjE5Mi4wLjIuNTQ"}`;
    getTrustTunnelLinks.mockResolvedValue(key({ links: [link({ url: real })] }));
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText: write }, configurable: true });
    await mount(section(user()));
    expect(text()).not.toContain("tt://");
    expect(document.querySelector("svg[role=img]")).toBeNull();
    await click(button("Copy link"));
    expect(write).toHaveBeenCalledWith(real);
    expect(document.querySelector(".tq-sv [data-cp=done]")).not.toBeNull(); // the button says it
    expect(text()).toContain("Link copied. It holds the key: send it to this person only");
    await click(button("QR code"));
    await settle();
    expect(document.querySelector("svg[role=img]")?.getAttribute("aria-label")).toBe("QR code of the tt:// link to de1");
    expect(document.querySelectorAll("svg[role=img] path").length).toBeGreaterThan(0);
    expect(text()).not.toContain("tt://");
  });

  it("is not kept: after the card goes away the cache holds no link, and nothing was written to the browser's storage", async () => {
    getTrustTunnelLinks.mockResolvedValue(key());
    await mount(section(user()));
    expect(qc.getQueryCache().getAll().some((q) => JSON.stringify(q.state.data, (_, v) => (typeof v === "bigint" ? String(v) : v))?.includes("tt://"))).toBe(true); // while it is shown
    act(() => root?.unmount());
    host?.remove();
    root = null;
    await new Promise((r) => setTimeout(r, 20));
    expect(qc.getQueryCache().findAll({ queryKey: ["users", "trusttunnel"] })).toHaveLength(0);
    // the page's own preferences (the language) are there; no link and no key
    const stored = [localStorage, sessionStorage].flatMap((s) => Object.keys(s).map((k) => `${k}=${s.getItem(k)}`));
    expect(stored.filter((x) => /tt:|AAEB|trusttunnel/i.test(x))).toEqual([]);
  });
});

describe("who sees it, and where it does not exist", () => {
  it("the Cloudflare edition: no switch, no call, the notice of the edition", async () => {
    await mount(section(user()), { admin: { id: "adm_1", role: Role.OWNER }, edition: "edge", unavailable: ["backups"] });
    expect(text()).toContain("Not available in the Cloudflare edition yet: TrustTunnel profiles and keys.");
    expect(document.querySelector("[role=switch]")).toBeNull();
    expect(getTrustTunnelLinks).not.toHaveBeenCalled();
  });

  it("a read-only admin: one line, no call (the key and links are the write level), no button", async () => {
    await mount(section(user()), { admin: { id: "adm_2", role: Role.READONLY }, edition: "vps", unavailable: [] });
    expect(text()).toContain("The key and links of TrustTunnel are seen by the owner and helpers.");
    expect(getTrustTunnelLinks).not.toHaveBeenCalled();
    expect(document.querySelector("button")).toBeNull();
  });

  it("a helper sees and does all of it, as the owner does", async () => {
    getTrustTunnelLinks.mockResolvedValue({ hasKey: false, links: [] });
    await mount(section(user()), { admin: { id: "adm_3", role: Role.HELPER }, edition: "vps", unavailable: [] });
    expect(button("Issue key")).toBeDefined();
  });
});

describe("the refusals of the panel, in words", () => {
  it("app_disabled, user_disabled and user_inactive read in the words of this block; the device limit reads in the shared ones", async () => {
    getTrustTunnelLinks.mockResolvedValue({ hasKey: false, links: [] });
    issueTrustTunnelKey.mockRejectedValue(new ConnectError("app_disabled", Code.FailedPrecondition));
    await mount(section(user()));
    await click(button("Issue key"));
    await settle();
    expect(text()).toContain("TrustTunnel is off for this person: turn it on first.");
    issueTrustTunnelKey.mockRejectedValue(new ConnectError("user_inactive", Code.FailedPrecondition));
    await click(button("Issue key"));
    await settle();
    expect(text()).toContain("The person is not active (the term has ended or the quota is used up): fix that first.");
  });

  it("a switch that would make a key at the device limit says so, whoever asks (the toggle uses the shared sentence)", () => {
    const e = new ConnectError("device_limit: 5/5: alice-phone", Code.FailedPrecondition);
    expect(errorText(e, tOf())).toBe("The device limit is reached (5 of 5): alice-phone. Raise the limit or remove a device first.");
    expect(errorText(new ConnectError("device_limit: 5/5", Code.FailedPrecondition), tOf())).toBe("The device limit is reached (5 of 5). Raise the limit or remove a device first.");
    expect(errorText(new ConnectError("unavailable_in_edition: feature=trusttunnel", Code.FailedPrecondition), tOf())).toBe("Not available in the Cloudflare edition yet: TrustTunnel profiles and keys.");
  });
});

describe("the key among the devices", () => {
  const device = (id: string, kind: DeviceKind, over: object = {}) => ({ id, kind, model: "", platform: "", client: "", awgProfileId: "", protocols: [], nodeIds: [], firstSeenUnix: 0, lastSeenUnix: 0, lastHandshakeUnix: 0, usedBytes: 0, ...over }) as unknown as DeviceN;
  const tt = device("dev_tt", DeviceKind.KEY, { protocols: ["trusttunnel"], model: "TrustTunnel key" });
  const awg = device("dev_awg", DeviceKind.KEY, { awgProfileId: "prf_awg", awgProfileName: "AWG", model: "iPhone", platform: "ios" });

  it("is a key told apart from an AmneziaVPN key by its protocol, and from the apps on the link", () => {
    expect(isTrustTunnelKey(tt)).toBe(true);
    expect(isTrustTunnelKey(awg)).toBe(false);
    expect(isTrustTunnelKey(device("l1", DeviceKind.LINK, { protocols: ["trusttunnel"] }))).toBe(false);
    const split = splitDevices([tt, awg, device("l1", DeviceKind.LINK)]);
    expect(split.keys.map((d) => d.id)).toEqual(["dev_awg"]);
    expect(split.trustTunnel.map((d) => d.id)).toEqual(["dev_tt"]);
    expect(split.link.map((d) => d.id)).toEqual(["l1"]);
    expect(split.shared).toEqual([]);
  });

  it("is shown when it is on, when the group has a TrustTunnel profile, or when the person holds a key", () => {
    const off = user({ apps: { happ: true, trustTunnel: false } });
    expect(trustTunnelShown(off, [], [])).toBe(false);
    expect(trustTunnelShown(user(), [], [])).toBe(true);
    expect(trustTunnelShown(off, [{ protocol: "trusttunnel" }] as never, [])).toBe(true);
    expect(trustTunnelShown(off, [], [tt])).toBe(true);
    expect(trustTunnelShown(off, [{ protocol: "hysteria2" }] as never, [awg])).toBe(false);
  });

  it("a key on hold is told from the person's status and the switch", () => {
    expect(pauseOf(UserStatus.ACTIVE, true)).toBeNull();
    expect(pauseOf(UserStatus.ACTIVE, false)).toBe("off");
    expect(pauseOf(UserStatus.DISABLED, true)).toBe("disabled");
    expect(pauseOf(UserStatus.EXPIRED, true)).toBe("expired");
    expect(pauseOf(UserStatus.LIMITED, true)).toBe("quota");
  });

  it("stays out of the AmneziaVPN keys and the shared apps of the devices panel, and counts as a place of the limit", async () => {
    getTrustTunnelLinks.mockResolvedValue(key());
    const devices = [tt, awg];
    await mount(<DevicesPanel user={user({ devicesUsed: 2, apps: { happ: false, amnezia: true, trustTunnel: true } })} devices={devices} profiles={[{ protocol: "awg", id: "prf_awg", name: "AWG" }, { protocol: "trusttunnel" }] as never} actions={{ refresh: vi.fn() } as never} onEditGroup={vi.fn()} />);
    expect(text()).toContain("2 of 3");
    expect(text()).not.toContain("Apps without a device ID");
    // one row for the AmneziaVPN key (with "Show key"), one for the TrustTunnel key in its own block, whose menu has "Revoke key" only
    expect([...document.querySelectorAll("button")].filter((b) => b.getAttribute("aria-label") === "Show key")).toHaveLength(1);
    expect(text()).toContain("TrustTunnel key");
    expect(text()).not.toContain("Delete…");
  });
});

describe("the ways a person's group gives", () => {
  it("lists a TrustTunnel profile in a row of its own, not in the subscription", async () => {
    const profiles = [
      { id: "p_hy", name: "Main · 443", protocol: "hysteria2" },
      { id: "p_tt", name: "TrustTunnel · 443", protocol: "trusttunnel" },
    ];
    await mount(<WaysOfGroup profiles={profiles} compact />);
    const lines = [...document.querySelectorAll(".items-start")].map((r) => r.textContent);
    expect(lines[0]).toBe("SubscriptionMain · 443");
    expect(lines.at(-1)).toBe("TrustTunnelTrustTunnel · 443");
  });
});
