import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { reasons } from "@/lib/port-check";
import { Ports } from "./ports";

const checkPorts = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  nodes: { checkPorts: (...a: unknown[]) => checkPorts(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  checkPorts.mockReset();
});

const now = Math.floor(Date.now() / 1000);
const check = (over: Record<string, unknown>) => ({ nodeId: "nod_1", port: 443, verdict: "ok", sent: 300, got: 300, checkedUnix: now - 120, badUnix: 0, sender: "de2", reason: "", ...over });
const tcp = (over: Record<string, unknown>) => ({ nodeId: "nod_1", port: 80, role: "acme", alpn: false, profile: "", verdict: "ours", reason: "", sender: "de2", checkedUnix: now - 120, ...over });
const inbound = (over: Record<string, unknown>) => ({ id: "inb_1", profileId: "prf_1", profileName: "hy2 · 443", port: 443, ...over });
const data = (over: Record<string, unknown> = {}) => ({ node: { id: "nod_1", name: "de1", status: NodeStatus.ONLINE }, inbounds: [inbound({})], portChecks: [], ...over }) as never;

async function mount(d: never) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <Ports data={d} />
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => host?.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const rows = () => [...document.querySelectorAll("li")].map((li) => li.textContent);

describe("the stored results", () => {
  it("says it was never checked, and offers the check", async () => {
    await mount(data());
    expect(text()).toContain("Ports");
    expect(text()).toContain(en["ports.never"]);
    expect(rows()).toEqual([]);
    expect(button("Check ports")).toBeDefined();
  });

  it("shows ok, lossy and broken ports with the loss, when, from where and which profile listens", async () => {
    await mount(
      data({
        inbounds: [inbound({}), inbound({ id: "inb_2", profileName: "hy2 · WARP · 8443", port: 8443 }), inbound({ id: "inb_3", profileName: "AWG", port: 33183 })],
        portChecks: [
          check({}),
          check({ port: 8443, verdict: "lossy", got: 270, sender: "panel", badUnix: now - 120 }),
          check({ port: 33183, verdict: "broken", got: 120 }),
          check({ port: 2053, got: 300 }),
        ],
      }),
    );
    const r = rows();
    expect(r[0]).toContain("udp/443");
    expect(r[0]).toContain("OK");
    expect(r[0]).toContain("300 of 300 arrived · 2 min ago · from de2");
    expect(r[0]).toContain("hy2 · 443");
    expect(r.find((x) => x?.includes("udp/8443"))).toContain("Loses 10 %");
    expect(r.find((x) => x?.includes("udp/8443"))).toContain("from the panel");
    expect(r.find((x) => x?.includes("udp/33183"))).toContain("60 % lost");
    // a port no profile listens on goes last and says so
    expect(r.at(-1)).toContain("udp/2053");
    expect(r.at(-1)).toContain("no profile on it");
  });

  it("says when packets were lost but the last run is clean", async () => {
    await mount(data({ portChecks: [check({ badUnix: now - 3 * 86400 })] }));
    expect(rows()[0]).toContain("OK");
    expect(rows()[0]).toContain("Lost packets 3 d ago, clean now.");
  });

  it("lists a profile's port that is not in the checks as not checked", async () => {
    await mount(data({ inbounds: [inbound({}), inbound({ id: "inb_2", port: 8443, profileName: "WARP" })], portChecks: [check({})] }));
    expect(rows().find((x) => x?.includes("udp/8443"))).toContain("Not checked");
  });

  it("hides the button from a read-only admin and disables it while the agent is away", async () => {
    role = Role.READONLY;
    await mount(data({ portChecks: [check({})] }));
    expect(button("Check ports")).toBeUndefined();
    act(() => root?.unmount());
    host?.remove();
    role = Role.HELPER;
    await mount(data({ node: { id: "nod_1", name: "de1", status: NodeStatus.DOWN }, portChecks: [check({})] }));
    expect((button("Check ports") as HTMLButtonElement).disabled).toBe(true);
    expect(text()).toContain(en["ports.offline"]);
  });
});

describe("the TCP rows and the renewal line", () => {
  const vless = inbound({ id: "inb_v", profileName: "VLESS · 443", port: 443, protocol: "vless" });

  it("shows each verdict, who answered, and what listens, after the UDP rows", async () => {
    await mount(
      data({
        inbounds: [inbound({}), vless],
        portChecks: [check({})],
        tcpChecks: [
          tcp({}),
          tcp({ port: 443, role: "vless", alpn: true, profile: "VLESS · 443", verdict: "other", sender: "panel" }),
          tcp({ port: 8443, role: "site", profile: "hy2 · 8443", verdict: "busy" }),
          tcp({ port: 2053, role: "vless", profile: "VLESS · 2053", verdict: "refused" }),
          tcp({ port: 2083, role: "vless", profile: "VLESS · 2083", verdict: "timeout" }),
        ],
      }),
    );
    const r = rows();
    expect(r[0]).toContain("udp/443");
    expect(r[1]).toContain("tcp/80");
    expect(r[1]).toContain(en["ports.tcp.ours"]);
    expect(r[1]).toContain("2 min ago · from de2");
    expect(r[1]).toContain(en["ports.tcp.role.acme"]);
    expect(r[2]).toContain("tcp/443");
    expect(r[2]).toContain(en["ports.tcp.other"]);
    expect(r[2]).toContain("from the panel");
    expect(r[2]).toContain("VLESS · 443");
    expect(r[3]).toContain(en["ports.tcp.refused"]);
    expect(r[4]).toContain(en["ports.tcp.timeout"]);
    expect(r[5]).toContain(en["ports.tcp.busy"]);
    expect(r[5]).toContain("Hysteria2 site · hy2 · 8443");
  });

  it("shows only TCP rows when there are no UDP ones, and is no longer 'never checked'", async () => {
    await mount(data({ inbounds: [vless], tcpChecks: [tcp({})] }));
    expect(rows()).toHaveLength(1);
    expect(rows()[0]).toContain("tcp/80");
    expect(text()).not.toContain(en["ports.never"]);
  });

  it("names the reason of a row that could not be probed", async () => {
    await mount(data({ tcpChecks: [tcp({ verdict: "", reason: "agent_too_old", sender: "" })] }));
    expect(rows()[0]).toContain(en["ports.unchecked"]);
    expect(rows()[0]).toContain(en["ports.short.agent_too_old"]);
  });

  it("says the certificates renew when port 80 is ours", async () => {
    await mount(data({ tcpChecks: [tcp({})] }));
    expect(text()).toContain(en["ports.renewal.ok80"]);
  });

  it("says they renew over 443 when port 80 is closed and an alpn listener there is ours", async () => {
    await mount(data({ tcpChecks: [tcp({ verdict: "refused" }), tcp({ port: 443, role: "site", alpn: true, profile: "hy2 · 443" })] }));
    expect(text()).toContain(en["ports.renewal.ok443"]);
    expect(document.querySelector("[role=status]")).toBeNull();
  });

  it("warns that certificates will stop renewing, naming the VLESS profile that holds 443", async () => {
    await mount(data({ tcpChecks: [tcp({ verdict: "timeout" }), tcp({ port: 443, role: "vless", profile: "VLESS · 443", verdict: "ours" })] }));
    expect(text()).toContain("Port 80 is closed and TCP 443 does not answer Let’s Encrypt (it goes to “VLESS · 443”, REALITY with another site): certificates on this node will stop renewing.");
    expect(document.querySelector("[role=status]")).not.toBeNull();
  });

  it("says nothing about renewal when port 80 was not probed or has no row", async () => {
    await mount(data({ tcpChecks: [tcp({ verdict: "", reason: "agent_too_old" })] }));
    expect(text()).not.toContain("renew");
    act(() => root?.unmount());
    host?.remove();
    await mount(data({ tcpChecks: [tcp({ port: 443, role: "vless", profile: "VLESS · 443" })] }));
    expect(text()).not.toContain("renew");
  });
});

describe("running the check", () => {
  it("shows a progress state while it runs, then the call is made for the node", async () => {
    let done: (v: unknown) => void = () => {};
    checkPorts.mockReturnValue(new Promise((r) => (done = r)));
    await mount(data());
    await click(button("Check ports"));
    await settle();
    expect(checkPorts).toHaveBeenCalledWith({ nodeId: "nod_1" });
    expect(button("Checking…")).toBeDefined();
    expect((button("Checking…") as HTMLButtonElement).disabled).toBe(true);
    expect(document.querySelector("[role=progressbar]")).not.toBeNull();
    expect(text()).toContain(en["ports.checkingNote"]);
    await act(async () => done({ ports: [], errorCode: "", sender: "de2" }));
    await settle();
    expect(document.querySelector("[role=progressbar]")).toBeNull();
    expect(button("Check ports")).toBeDefined();
  });

  it.each(reasons.filter((r) => r !== "inconclusive"))("explains %s in one line", async (reason) => {
    checkPorts.mockResolvedValue({ ports: [], errorCode: reason, sender: "" });
    await mount(data());
    await click(button("Check ports"));
    await settle();
    expect(text()).toContain(en[`ports.why.${reason}`]);
  });

  it("explains an inconclusive run with the counts that did arrive", async () => {
    checkPorts.mockResolvedValue({
      ports: [{ port: 443, verdict: "", sent: 300, got: 120, reason: "inconclusive" }, { port: 8443, verdict: "", sent: 300, got: 90, reason: "inconclusive" }],
      errorCode: "inconclusive",
      sender: "de2",
    });
    await mount(data());
    await click(button("Check ports"));
    await settle();
    expect(text()).toContain("even the best port got only 40 %");
    expect(text()).toContain("443: 120/300 · 8443: 90/300");
  });

  it("on same_host explains that nothing was saved and does not show the run's unsaved TCP rows", async () => {
    checkPorts.mockResolvedValue({ ports: [{ port: 443, verdict: "", sent: 300, got: 300, reason: "same_host" }], tcp: [tcp({ port: 80, verdict: "ours" })], errorCode: "same_host", sender: "panel" });
    await mount(data());
    await click(button("Check ports"));
    await settle();
    expect(text()).toContain(en["ports.why.same_host"]);
    expect(rows()).toEqual([]);
  });

  it("says why when the TCP part of a run could not probe anything", async () => {
    checkPorts.mockResolvedValue({ ports: [check({})], tcp: [tcp({ verdict: "", reason: "agent_too_old", sender: "" })], errorCode: "", sender: "de2" });
    await mount(data({ portChecks: [check({})] }));
    await click(button("Check ports"));
    await settle();
    expect(text()).toContain(en["ports.why.agent_too_old"]);
  });

  it("shows a refused call (an RPC error) in words, not as a verdict", async () => {
    checkPorts.mockRejectedValue(new Error("boom"));
    await mount(data());
    await click(button("Check ports"));
    await settle();
    expect(text()).toContain(en["err.generic"]);
  });
});
