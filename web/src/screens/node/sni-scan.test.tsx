import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { setEffectsEnabled } from "@/lib/effects";
import { nodePageData } from "../dev-kit-node-page";
import { ProfilesTab } from "./profiles";
import { SniScanCard } from "./sni-scan";

// "Sites near the node": the warning before every scan, the states of the card, the names as text only, and who gets the card.

const scanCall = vi.fn();
const updateInbound = vi.fn();
const listProfiles = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  probe: { nodeSniScan: (...a: unknown[]) => scanCall(...a) },
  profiles: { updateInbound: (...a: unknown[]) => updateInbound(...a), listProfiles: (...a: unknown[]) => listProfiles(...a) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useNavigate: () => () => {},
  useSearch: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
let root: Root | null = null;
let host: HTMLDivElement | null = null;
const copied: string[] = [];
beforeEach(() => {
  setLang("en");
  setEffectsEnabled(false);
  copied.length = 0;
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (v: string) => void copied.push(v) } });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  scanCall.mockReset();
  updateInbound.mockReset();
  listProfiles.mockReset();
  document.querySelectorAll("[role=dialog]").forEach((d) => d.remove());
});

const NOW = () => Math.floor(Date.now() / 1000);
const site = (name: string, over: Record<string, unknown> = {}) => ({ ip: "203.0.113.40", name, dns: "here", h2: true, singleSite: true, http: 200, ms: 11, days: 64, ...over });
const answer = (over: Record<string, unknown> = {}) => ({
  nodeName: "de1",
  error: "",
  retryAfterS: 0,
  atUnix: BigInt(NOW() - 3600),
  range: "203.0.113.0/24",
  addresses: 253,
  open: 41,
  tls13: 19,
  mlkem: 7,
  valid: 2,
  partial: false,
  ownLeftOut: 0,
  candidates: [site("shop.example.com"), site("blog.example.net", { ip: "203.0.113.52", dns: "subnet", singleSite: false, http: 301 })],
  ...over,
});
const never = (over: Record<string, unknown> = {}) => answer({ atUnix: 0n, range: "", addresses: 0, open: 0, tls13: 0, mlkem: 0, valid: 0, candidates: [], ...over });

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mountCard(linked = true, servers: object[] = []) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <SniScanCard nodeId="nod_de1" nodeName="de1" linked={linked} servers={servers as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await settle();
}
const text = () => host!.textContent ?? "";
const button = (label: string, within: ParentNode = document) => [...within.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
const disabled = (b?: Element | null) => !!b && (b.hasAttribute("disabled") || b.getAttribute("data-disabled") !== null);

describe("the warning before a scan", () => {
  it("opens from the button with the plan's text and Cancel chosen, and starts nothing by itself", async () => {
    scanCall.mockResolvedValue(never());
    await mountCard();
    expect(scanCall).toHaveBeenCalledWith({ nodeId: "nod_de1", run: false }); // reading what is kept
    await click(button(en["sni.find"]));
    const d = dialog()!;
    expect(d.textContent).toContain("Scan the neighbours of de1?");
    expect(d.textContent).toContain(en["sni.warn.body"]);
    expect(d.textContent).toContain("up to 253 neighbouring addresses");
    expect(d.textContent).toContain("at most 10 a second");
    expect(d.textContent).toContain("To a hoster this looks like scanning");
    expect(d.textContent).toContain("no sooner than in 30 minutes");
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
    expect(scanCall).not.toHaveBeenCalledWith({ nodeId: "nod_de1", run: true });
    await click(button("Cancel", d));
    expect(dialog()).toBeNull();
    expect(scanCall).not.toHaveBeenCalledWith({ nodeId: "nod_de1", run: true });
  });

  it("comes again before every scan, not once", async () => {
    scanCall.mockResolvedValue(never());
    await mountCard();
    for (let n = 1; n <= 2; n++) {
      scanCall.mockResolvedValue(never()); // the kept state does not hold the next scan back: the pause is over
      await click(button(n === 1 ? en["sni.find"] : en["sni.find"]));
      expect(dialog(), `scan ${n}`).not.toBeNull();
      await click(button(en["sni.warn.go"], dialog()!));
      expect(scanCall.mock.calls.filter((c) => (c[0] as { run: boolean }).run)).toHaveLength(n);
      expect(dialog()).toBeNull();
    }
  });
});

describe("the states", () => {
  it("not scanned yet: one sentence and the button", async () => {
    scanCall.mockResolvedValue(never());
    await mountCard();
    expect(text()).toContain(en["sni.none"]);
    expect(text()).toContain(en["sni.lead"]);
    expect(button(en["sni.find"])).toBeDefined();
  });

  it("running: the card says it goes on for up to a minute, the button is busy, and the result comes in when the node answers", async () => {
    scanCall.mockResolvedValueOnce(never());
    await mountCard();
    let done!: (v: unknown) => void;
    scanCall.mockReturnValueOnce(new Promise((r) => (done = r)));
    await click(button(en["sni.find"]));
    await click(button(en["sni.warn.go"], dialog()!));
    expect(text()).toContain(en["sni.running"]);
    expect(text()).toContain(en["sni.running.hint"]);
    expect(host!.querySelector("[role=status]")).not.toBeNull();
    const busy = button(en["sni.running.short"]);
    expect(busy?.getAttribute("aria-busy")).toBe("true");
    expect(disabled(busy)).toBe(true);
    await act(async () => done(answer({ atUnix: BigInt(NOW()) })));
    await settle();
    expect(text()).not.toContain(en["sni.running"]);
    expect(text()).toContain("shop.example.com");
  });

  it("a result: the counters, the sites with their labels, the note about the names", async () => {
    scanCall.mockResolvedValue(answer({ ownLeftOut: 2 }));
    await mountCard();
    for (const part of ["253", "41", "19", "7"]) expect(text()).toContain(part);
    for (const word of ["checked", "answer on 443", "TLS 1.3", "with MLKEM", "suitable"]) expect(text()).toContain(word);
    const rows = [...host!.querySelectorAll(".ss-row")];
    expect(rows).toHaveLength(2);
    expect(rows[0]!.textContent).toContain("shop.example.com");
    expect(rows[0]!.textContent).toContain("203.0.113.40");
    for (const label of ["DNS leads here", "HTTP/2", "own certificate", "MLKEM", "HTTP 200", "11 ms", "certificate for 64 d"]) expect(rows[0]!.textContent, label).toContain(label);
    for (const label of ["DNS: in the subnet", "shared certificate", "HTTP 301"]) expect(rows[1]!.textContent, label).toContain(label);
    expect(text()).toContain(en["sni.names"]);
    expect(text()).toContain("Left out as yours (your nodes and the panel): 2");
    expect(text()).toContain("Scanned 1 h ago");
    // scanned an hour ago: the pause is over, the button starts the next scan
    expect(disabled(button(en["sni.again"]))).toBe(false);
  });

  it("partial: says it ran out of time, and still lists what it found", async () => {
    scanCall.mockResolvedValue(answer({ partial: true, atUnix: BigInt(NOW() - 4000) }));
    await mountCard();
    expect(text()).toContain(en["sni.partial"]);
    expect(host!.querySelectorAll(".ss-row")).toHaveLength(2);
    expect(button(en["sni.again"])).toBeDefined();
  });

  it("empty: nothing in the subnet, and what to do instead", async () => {
    scanCall.mockResolvedValue(answer({ candidates: [], valid: 0, atUnix: BigInt(NOW() - 4000) }));
    await mountCard();
    expect(text()).toContain("No suitable sites in 203.0.113.0/24");
    expect(host!.querySelectorAll(".ss-row")).toHaveLength(0);
  });

  it("too early: the button waits with the minutes, the notice says so, and the last list stays", async () => {
    scanCall.mockResolvedValue(answer({ error: "too_soon", retryAfterS: 18 * 60, atUnix: BigInt(NOW() - 12 * 60) }));
    await mountCard();
    expect(text()).toContain("This node was scanned a moment ago. Again in 18 min.");
    const b = button("Again in 18 min");
    expect(b).toBeDefined();
    expect(disabled(b)).toBe(true);
    expect(host!.querySelectorAll(".ss-row")).toHaveLength(2);
  });

  it("says in words why a node cannot scan: no connection, an old agent, busy, a NAT, no IPv4, no CA roots, a failure", async () => {
    for (const [code, linked, want] of [
      ["agent_too_old", true, en["sni.err.agent_too_old"]],
      ["busy", true, en["sni.err.busy"]],
      ["not_my_address", true, en["sni.err.not_my_address"]],
      ["no_ipv4", true, en["sni.err.no_ipv4"]],
      ["no_roots", true, en["sni.err.no_roots"]],
      ["failed", true, en["sni.err.failed"]],
      ["node_offline", true, en["sni.err.node_offline"]],
      ["", false, en["sni.err.node_offline"]], // the page knows before the panel does
    ] as const) {
      scanCall.mockResolvedValue(never({ error: code }));
      await mountCard(linked);
      expect(text(), code || "unlinked").toContain(want);
      const b = button(en["sni.find"]);
      expect(b, code).toBeDefined();
      // the ones that cannot be fixed by waiting disable the button; busy and a failure may be tried again
      expect(disabled(b), code || "unlinked").toBe(!["busy", "failed"].includes(code));
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("an error of the call itself is shown, and the card stays as it was", async () => {
    scanCall.mockResolvedValueOnce(never());
    await mountCard();
    scanCall.mockRejectedValueOnce(new Error("boom"));
    await click(button(en["sni.find"]));
    await click(button(en["sni.warn.go"], dialog()!));
    expect(host!.querySelector("[role=alert]")).not.toBeNull();
    expect(button(en["sni.find"])).toBeDefined();
  });
});

describe("the names are text from strangers", () => {
  it("are shown as text and never as a link or an image, whatever they hold", async () => {
    const evil = [
      "<a href=https://evil.example/>x</a>.example.com",
      "<img src=https://evil.example/p.png onerror=alert(1)>.example.com",
      "javascript:alert(1).example.com",
      "https://evil.example/steal.example.com",
    ];
    scanCall.mockResolvedValue(answer({ candidates: evil.map((n, i) => site(n, { ip: `203.0.113.${50 + i}` })) }));
    await mountCard();
    expect(host!.querySelectorAll(".ss-row")).toHaveLength(4);
    expect(host!.querySelectorAll("a, img, iframe, object, embed, link, script, source, video, audio")).toHaveLength(0);
    for (const n of evil) expect(text()).toContain(n); // as text
    // nothing in the card can carry a destination or run: no href, no src, no srcset, no handler attribute
    expect(host!.querySelectorAll("[href], [src], [srcset], [action], [onerror], [onclick], [onload]")).toHaveLength(0);
  });

  it("are copied as the text they are", async () => {
    scanCall.mockResolvedValue(answer());
    await mountCard();
    const copy = host!.querySelector<HTMLButtonElement>(`button[aria-label="${en["sni.copy"]}"]`);
    expect(copy).not.toBeNull();
    await click(copy);
    expect(copied).toEqual(["shop.example.com"]);
  });

  it("with no REALITY server on the node there is nothing to apply a name to, and a scan only ever sends the node and run", async () => {
    scanCall.mockResolvedValue(answer());
    await mountCard();
    const labels = [...host!.querySelectorAll("button")].map((b) => b.textContent?.trim() || b.getAttribute("aria-label"));
    expect(labels.some((l) => /appl/i.test(l ?? ""))).toBe(false);
    expect(scanCall.mock.calls.every((c) => Object.keys(c[0] as object).sort().join() === "nodeId,run")).toBe(true);
  });
});

// "Apply…": show a neighbour that DNS leads here on a REALITY server of the node
describe("applying a site to a REALITY server", () => {
  const server = (over: Record<string, unknown> = {}) => ({ id: "inb_v", profileId: "prf_v", profileName: "VLESS · Reality", protocol: "vless", port: 443, realityTarget: "self", realityTargetOverride: "", tlsServerName: "de1.example.com", ...over });
  const applyButtons = () => [...host!.querySelectorAll("button")].filter((b) => b.textContent?.trim() === en["sni.apply"]);
  /** The panel's check: the inbound as it would be, and the Hysteria2 site that yields on the way. */
  const checkSays = (over: { warnings?: unknown[]; refuse?: string } = {}) =>
    updateInbound.mockImplementation(async (r: { realityTargetOverride?: string; validateOnly?: boolean }) => {
      if (over.refuse && r.validateOnly) throw new ConnectError(over.refuse, Code.InvalidArgument);
      return { inbound: { id: "inb_v", realityTarget: r.realityTargetOverride }, warnings: over.warnings ?? [], freePort: 0 };
    });
  beforeEach(() => listProfiles.mockResolvedValue({ profiles: [{ id: "prf_v", name: "VLESS · Reality", userCount: 3 }] }));

  it("is offered for a site that DNS leads here, and for no other", async () => {
    scanCall.mockResolvedValue(answer({ candidates: [site("shop.example.com"), site("blog.example.net", { dns: "subnet" }), site("portal.example.com", { dns: "elsewhere" }), site("old.example.org", { dns: "none" })] }));
    await mountCard(true, [server()]);
    expect(applyButtons()).toHaveLength(1);
    expect(applyButtons()[0]!.closest(".ss-row")?.textContent).toContain("shop.example.com");
    act(() => root?.unmount());
    host?.remove();
    await mountCard(true, []); // no REALITY server
    expect(applyButtons()).toHaveLength(0);
  });

  it("says what strangers see now and after, who restarts and that the apps need the subscription refreshed, and changes nothing until the click", async () => {
    scanCall.mockResolvedValue(answer());
    checkSays({ warnings: [{ code: "hy2_decoy_paused", params: { node: "de1", port: "443", hy2: "Hysteria2", vless: "VLESS · Reality", via: "site", target: "shop.example.com" } }] });
    await mountCard(true, [server()]);
    await click(applyButtons()[0]);
    for (let i = 0; i < 2; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    const d = dialog()!;
    expect(d.textContent).toContain("Show shop.example.com on de1?");
    expect(d.textContent).toContain(`${en["sni.apply.now"]}the node’s own site`);
    expect(d.textContent).toContain(`${en["sni.apply.after"]}shop.example.com`);
    expect(d.textContent).toContain("The server VLESS · Reality restarts");
    expect(d.textContent).toContain(en["sni.apply.clients"]);
    expect(d.textContent).toContain("3 people have this server in their subscription.");
    expect(d.textContent).toContain("Hysteria2"); // the site of the other server that yields
    expect(d.querySelectorAll("img, a[href*='shop.example.com']")).toHaveLength(0); // the name is text; the one link is the guide's
    // the check was asked, nothing saved
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_v", realityTargetOverride: "shop.example.com:443", validateOnly: true }, expect.anything());
    expect(updateInbound.mock.calls.every(([r]) => (r as { validateOnly?: boolean }).validateOnly)).toBe(true);
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
    await click(button("Cancel", d));
    expect(dialog()).toBeNull();
    expect(updateInbound.mock.calls.every(([r]) => (r as { validateOnly?: boolean }).validateOnly)).toBe(true);
  });

  it("saves the target as name:443 on the click, and says so", async () => {
    scanCall.mockResolvedValue(answer());
    checkSays();
    await mountCard(true, [server({ realityTarget: "www.example.org:443" })]);
    await click(applyButtons()[0]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    for (let i = 0; i < 3; i++) await settle();
    expect(dialog()!.textContent).toContain("www.example.org"); // what they see now
    await click(button(en["sni.apply.go"], dialog()!));
    expect(updateInbound.mock.calls.filter(([r]) => !(r as { validateOnly?: boolean }).validateOnly).map(([r]) => r)).toEqual([{ inboundId: "inb_v", realityTargetOverride: "shop.example.com:443" }]);
    expect(document.querySelector("[data-testid=sni-apply]")).toBeNull();
    expect(document.body.textContent).toContain("shop.example.com is now shown on de1");
  });

  it("tells of the port it shares and of the second face before the click, and again in the message after it", async () => {
    scanCall.mockResolvedValue(answer());
    const warnings = [
      { code: "tcp_port_shared", params: { node: "de1", port: "443", profiles: "“VLESS · Reality” and “TT”", default: "TT" } },
      { code: "share_two_faces", params: { node: "de1", port: "443", profile: "VLESS · Reality", default: "TT" } },
    ];
    checkSays({ warnings });
    await mountCard(true, [server({ realityTarget: "www.example.org:443" })]);
    await click(applyButtons()[0]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    for (let i = 0; i < 3; i++) await settle();
    const notes = dialog()!.querySelector("[data-testid=share-notes]")?.textContent ?? "";
    expect(notes).toContain("TCP 443 on de1 is shared by");
    expect(notes).toContain("“VLESS · Reality” shows another site");
    // a name of the neighbour's is nobody's to point a DNS record at
    expect(dialog()!.textContent).not.toContain(en["share.dnsNext"]);
    await click(button(en["sni.apply.go"], dialog()!));
    await settle();
    expect(document.body.textContent).toContain("shop.example.com is now shown on de1");
    expect(document.body.textContent).toContain("TCP 443 on de1 is shared by");
  });

  it("asks which server when the node has several, and checks the one picked", async () => {
    scanCall.mockResolvedValue(answer());
    checkSays();
    await mountCard(true, [server(), server({ id: "inb_w", profileId: "prf_w", profileName: "VLESS · 8443", port: 8443, realityTarget: "www.example.org:443" })]);
    await click(applyButtons()[0]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    expect(dialog()!.textContent).toContain(en["sni.apply.pick"]);
    expect(dialog()!.textContent).toContain("VLESS · Reality");
  });

  it("does not offer to apply what the server shows already", async () => {
    scanCall.mockResolvedValue(answer());
    checkSays();
    await mountCard(true, [server({ realityTarget: "shop.example.com:443", realityTargetOverride: "shop.example.com:443" })]);
    await click(applyButtons()[0]);
    await settle();
    expect(dialog()!.textContent).toContain(en["sni.apply.same"]);
    expect(disabled(button(en["sni.apply.go"], dialog()!))).toBe(true);
  });

  it("shows a refusal of the panel in words and keeps the button off", async () => {
    scanCall.mockResolvedValue(answer());
    checkSays({ refuse: "reality_target_same_node: target=shop.example.com:443&node=de1" });
    await mountCard(true, [server()]);
    await click(applyButtons()[0]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
    for (let i = 0; i < 3; i++) await settle();
    expect(dialog()!.textContent).toContain("“shop.example.com:443” is de1 itself.");
    expect(disabled(button(en["sni.apply.go"], dialog()!))).toBe(true);
  });
});

describe("who gets the card on the Profiles tab", () => {
  async function mountTab(state: Parameters<typeof nodePageData>[0], tweak?: (qc: QueryClient, data: { inbounds: { protocol: string }[]; node: Record<string, unknown> }) => void) {
    const { qc, data } = nodePageData(state);
    tweak?.(qc, data as never);
    scanCall.mockResolvedValue(never());
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <ToastProvider>
            <ProfilesTab data={data as never} />
          </ToastProvider>
        </QueryClientProvider>,
      ),
    );
    for (let i = 0; i < 3; i++) await settle();
    return qc;
  }
  const card = () => host!.querySelector("[data-testid=sni-card]");

  it("the owner of the VPS panel gets it on a node with a VLESS server, under the servers", async () => {
    await mountTab("calm");
    expect(card()).not.toBeNull();
    expect(text()).toContain(en["sni.title"]);
    expect(scanCall).toHaveBeenCalledWith({ nodeId: "nod_de1", run: false });
  });

  it("a helper gets none, and the panel is not asked", async () => {
    await mountTab("calm", (qc) => {
      const me = qc.getQueryData(meQuery.queryKey) as { admin: { role: number } };
      qc.setQueryData(meQuery.queryKey, { ...me, admin: { ...me.admin, role: 2 } } as never);
    });
    expect(card()).toBeNull();
    expect(scanCall).not.toHaveBeenCalled();
  });

  it("a read-only role gets none", async () => {
    await mountTab("viewer");
    expect(card()).toBeNull();
    expect(scanCall).not.toHaveBeenCalled();
  });

  it("the Cloudflare edition gets none", async () => {
    await mountTab("edge");
    expect(card()).toBeNull();
    expect(scanCall).not.toHaveBeenCalled();
  });

  it("a retired node, or one with no VLESS server, gets none", async () => {
    await mountTab("retired");
    expect(card()).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await mountTab("calm", (_qc, data) => {
      data.inbounds = data.inbounds.filter((i) => i.protocol !== "vless");
    });
    expect(card()).toBeNull();
    expect(scanCall).not.toHaveBeenCalled();
  });
});
