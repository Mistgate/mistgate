import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { ProfilesScreen } from "./list";
import { validateProfilesSearch } from "./search";

const all = [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON];
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: {
    listProfiles: () =>
      Promise.resolve({
        profiles: [
          // the server's order: the list sorts by protocol (Hysteria2, VLESS, AmneziaWG, MTProto) and keeps it within one
          { id: "p_tg", name: "tg", protocol: "mtproto", nodeCount: 1, userCount: 3, summary: "TCP 443 · Fake TLS · www.example.com", warnings: [], formats: all.map((format) => ({ format, ok: false, note: "unsupported" })) },
          { id: "p_run", name: "runs", protocol: "vless", nodeCount: 2, userCount: 5, summary: "XHTTP 443 · REALITY · WARP", warnings: [], formats: all.map((format) => ({ format, ok: format !== SubFormat.SINGBOX_JSON, note: format === SubFormat.SINGBOX_JSON ? "xhttp" : "" })) },
          { id: "p_lonely", name: "lonely", protocol: "awg", nodeCount: 1, userCount: 0, summary: "UDP 51842 · AWG 3.1 · DNS", warnings: [{ code: "inbound_failed", params: { node: "de1", inbound: "inb_1", error: "x" } }], formats: all.map((format) => ({ format, ok: format === SubFormat.MIHOMO_YAML, note: format === SubFormat.MIHOMO_YAML ? "" : "awg" })) },
          { id: "p_nowhere", name: "nowhere", protocol: "hysteria2", nodeCount: 0, userCount: 5, summary: "UDP 8443 · hop 20000-30000 · Salamander · Let's Encrypt", warnings: [], formats: all.map((format) => ({ format, ok: true, note: "" })) },
        ],
      }),
    listProtocols: () =>
      Promise.resolve({
        protocols: [
          { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP] },
          { id: "vless", displayName: "VLESS", apps: [App.HAPP] },
          { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA] },
          { id: "mtproto", displayName: "MTProto", apps: [App.TELEGRAM] },
        ],
      }),
  },
  groups: { listGroups: () => Promise.resolve({ groups: [{ id: "g", name: "Family", profileIds: ["p_run", "p_nowhere", "p_tg"], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 }] }) },
}));

// the router: the list reads its filters from the URL and writes them back through navigate({ search })
let search: Record<string, unknown> = {};
let rerender = () => {};
const opened = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => search,
  useNavigate: () => (o: { to?: string; search?: Record<string, unknown> | ((prev: Record<string, unknown>) => Record<string, unknown>) }) => {
    if (typeof o.search === "function") search = o.search(search);
    else opened(o);
    rerender();
    return Promise.resolve();
  },
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
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
  search = {};
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  opened.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = () => root!.render(<QueryClientProvider client={qc}><ProfilesScreen /></QueryClientProvider>);
  rerender = () => act(draw);
  await act(async () => draw());
  await settle();
}
const rows = () => [...document.querySelectorAll<HTMLElement>("[role=listitem]")];
const names = () => rows().map((r) => r.querySelector("a")!.textContent);
const row = (name: string) => rows().find((r) => r.querySelector("a")!.textContent === name)!;
// a row's cells, each one focusable with its words in its name: settings, formats, nodes, people
const cells = (name: string) => [...row(name).querySelectorAll<HTMLElement>("[tabindex='0']")].map((c) => c.getAttribute("aria-label"));
const radio = (start: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent!.startsWith(start))!;

describe("the profiles list", () => {
  it("is one row per profile in the protocols' order, the name being the row's link", async () => {
    await mount();
    expect(names()).toEqual(["nowhere", "runs", "lonely", "tg"]);
    expect(row("runs").querySelector("a")!.getAttribute("href")).toBe("/profiles/$id");
  });

  it("shows the port in words and the other settings as icons, every fact spelled out in the cell's label", async () => {
    await mount();
    expect(row("runs").querySelector("[tabindex='0']")!.textContent).toBe("XHTTP 443");
    expect(cells("runs")[0]).toBe("XHTTP 443. Security: REALITY. Exit via WARP");
    expect(cells("nowhere")[0]).toBe("UDP 8443. Port hopping 20000-30000. Obfuscation: Salamander. Certificate: Let's Encrypt");
    expect(cells("lonely")[0]).toBe("UDP 51842. AmneziaWG 3.1. Looks like DNS");
    expect(cells("tg")[0]).toBe("TCP 443. Fake TLS: to an observer it looks like TLS. Another site: www.example.com");
  });

  it("folds a missing format and a node's warning into one amber mark each; AmneziaWG's keys and Telegram are no warning", async () => {
    await mount();
    const [, formats, nodes, people] = cells("runs");
    expect(formats).toBe("Formats: Link list, Mihomo, Xray JSON. not in sing-box: XHTTP. sing-box does not support XHTTP.");
    expect(nodes).toBe("On 2 nodes");
    expect(people).toBe("5 users. Groups: Family");
    expect(row("runs").querySelectorAll(".text-warn-text")).toHaveLength(1);
    expect(cells("lonely")[1]).toBe("Formats: Mihomo. AmneziaVPN keys. The link list, Xray JSON and sing-box do not carry AmneziaWG.");
    expect(cells("lonely")[2]).toBe("On 1 node. Did not start on de1");
    expect(cells("lonely")[3]).toBe("0 users. In no group");
    expect(row("lonely").querySelectorAll(".text-warn-text")).toHaveLength(1);
    expect(cells("tg")[1]).toBe("Telegram only: people add it from a tg:// link");
    expect(cells("nowhere")[2]).toBe("Not on nodes");
  });

  it("filters by protocol, name and state through the URL, each count being what that choice would show", async () => {
    await mount();
    expect(radio("All").textContent).toBe("All4");
    expect(radio("VLESS").textContent).toBe("VLESS1");
    await act(async () => radio("VLESS").click());
    expect(search).toEqual({ protocol: "vless" });
    expect(names()).toEqual(["runs"]);

    await act(async () => radio("All").click());
    const box = document.querySelector<HTMLInputElement>("input[type=search]")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, "  NO ");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(search).toEqual({ q: "NO" });
    expect(names()).toEqual(["nowhere"]);
    expect(radio("All").textContent).toBe("All1");
  });

  it("opens on the state in the URL, and says when nothing matches with a way back", async () => {
    search = { state: "idle" };
    await mount();
    expect(names()).toEqual(["nowhere"]);
    expect(radio("Hysteria2").textContent).toBe("Hysteria21");
    expect(radio("VLESS").textContent).toBe("VLESS0");

    search = { protocol: "vless", state: "idle" };
    rerender();
    expect(rows()).toHaveLength(0);
    expect(document.body.textContent).toContain("Nothing found");
    const reset = [...document.querySelectorAll("button")].find((b) => b.textContent === "Reset filters")!;
    await act(async () => reset.click());
    expect(search).toEqual({});
    expect(names()).toHaveLength(4);
  });
});

describe("New profile from the list", () => {
  it("opens the choice of a protocol, or straight the form of the protocol the list is filtered by", async () => {
    await mount();
    const create = () => [...document.querySelectorAll("button")].find((b) => b.textContent === "New profile")!;
    await act(async () => create().click());
    expect(opened).toHaveBeenLastCalledWith({ to: "/profiles/new", search: {} });
    search = { protocol: "vless" };
    rerender();
    await act(async () => create().click());
    expect(opened).toHaveBeenLastCalledWith({ to: "/profiles/new", search: { protocol: "vless" } });
  });
});

describe("the list's URL", () => {
  it("keeps known filters, turns a typed number back into text and drops the rest", () => {
    expect(validateProfilesSearch({ protocol: "vless", q: 443, state: "warp" })).toEqual({ protocol: "vless", q: "443", state: "warp" });
    expect(validateProfilesSearch({ protocol: "../x", q: "", state: "broken" })).toEqual({});
  });
});
