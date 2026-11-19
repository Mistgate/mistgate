import { Code, ConnectError } from "@connectrpc/connect";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { dictionaries, fill } from "@/i18n";
import type { Tx } from "@/screens/users/t";
import { rowOf as putRow, type Asked } from "./put-check";
import { rowOf as wizardRow } from "./wizard-check";
import { ShareRowFix } from "./share-row-fix";

// "Put on nodes" and the wizard's "Which nodes" when the name of a node's TCP port is held: the row says so with the way out, and once
// the name is put the row says that the profile will stand beside the others.

const dict = dictionaries.en as Record<string, string>;
const t = ((k: string, v?: Record<string, string | number>) => fill(dict[k] ?? k, v)) as unknown as Tx;
t.opt = (k: string, v?: Record<string, string | number>) => (k in dict ? fill(dict[k]!, v) : undefined);
vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children?: unknown }) => <a>{children as never}</a> }));

const n = { id: "nod_1", name: "de1", address: "de1.example.com", linked: true, entry: false, firstAwg: false, awgMode: "auto" };
const refused = (msg: string, code = Code.AlreadyExists): Asked => ({ state: "refused", error: new ConnectError(msg, code) });
const held = "sni_taken: port=443&node=de1&profile=Reality+main&name=de1.example.com&free=8443&share=1&suggest=cdn.de1.example.com";
const ok = (name: string): Asked => ({
  state: "ok",
  inbound: { port: 443, tlsServerName: name } as never,
  warnings: [{ code: "tcp_port_shared", params: { node: "de1", port: "443", profiles: "Reality main, Telegram", default: "Reality main", restarts: "Reality main" } }],
  freePort: 0,
});

describe.each([
  ["Put on nodes", (a: Asked, fix = {}) => putRow(a, n, { t, protocol: "mtproto", fix })],
  ["the wizard", (a: Asked, fix = {}) => wizardRow(a, n, { t, protocol: "mtproto", fix, alt: "" })],
])("a node row in %s", (_, rowOf) => {
  it("is held back with the offer of a name of its own and the free port", () => {
    const r = rowOf(refused(held));
    expect(r.need).toBe(true);
    expect(r.on).toBe(false);
    expect(r.text).toContain("de1.example.com");
    expect(r.text).toContain("Reality main");
    expect(r.share?.offer).toMatchObject({ suggest: "cdn.de1.example.com", port: "443", free: "8443" });
    expect(r.share?.free).toBe("8443");
  });

  it("has no offer where the name is not the profile's to set, and links to the updates for an old agent", () => {
    const fixed = rowOf(refused("sni_taken: port=443&node=de1&profile=Telegram&name=www.example.net&free=8443"));
    expect(fixed.need).toBe(true);
    expect(fixed.share).toMatchObject({ offer: null, free: "8443" });
    const old = rowOf(refused("share_agent_old: port=443&node=de1&profile=Reality+main", Code.FailedPrecondition));
    expect(old.upd).toBe(true);
    expect(old.text).toContain("newer agent");
    const site = rowOf(refused("share_needs_site: port=443&node=de1&profile=Reality+main", Code.FailedPrecondition));
    expect(site.need).toBe(true);
    expect(site.text).toContain("own site");
  });

  it("says the profile will stand beside the others once the name passes, with the name whose record is to be made", () => {
    const r = rowOf(ok("cdn.de1.example.com"), { sni: "cdn.de1.example.com" });
    expect(r.need).toBe(false);
    expect(r.text).toContain("Stands beside Reality main, Telegram on 443");
    expect(r.shared?.name).toBe("cdn.de1.example.com");
    expect(r.shared?.text).toContain("“Reality main” restarts");
    // the node's own name leaves no record to make
    expect(rowOf(ok("de1.example.com")).shared?.name).toBe("");
  });
});

describe("what a row offers under its node", () => {
  let host: HTMLDivElement | null = null;
  beforeAll(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });
  afterEach(() => host?.remove());

  it("puts the suggested name, or the one typed, and takes the free port", async () => {
    const onName = vi.fn();
    const onPort = vi.fn();
    host = document.createElement("div");
    document.body.append(host);
    const share = { code: "sni_taken" as const, offer: { port: "443", profile: "Reality main", held: "de1.example.com", suggest: "cdn.de1.example.com", free: "8443" }, free: "8443" };
    await act(async () => createRoot(host!).render(<ShareRowFix share={share} node={n} inputClass="an-i" onName={onName} onPort={onPort} />));
    const input = host.querySelector("input")!;
    expect(input.value).toBe("cdn.de1.example.com");
    const button = (label: string) => [...host!.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!;
    await act(async () => void button("Put beside on 443").click());
    expect(onName).toHaveBeenCalledWith("cdn.de1.example.com");
    await act(async () => void button("Take 8443").click());
    expect(onPort).toHaveBeenCalledWith("8443");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "not a name");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(button("Put beside on 443").hasAttribute("disabled") || button("Put beside on 443").hasAttribute("data-disabled")).toBe(true);
    expect(host.textContent).toContain("Not a host name");
  });
});
