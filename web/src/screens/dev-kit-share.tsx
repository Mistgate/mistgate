import { Code, ConnectError } from "@connectrpc/connect";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { profiles as profilesApi } from "@/lib/api";
import { profileListQuery } from "@/screens/users/rpc";
import { AddInbound } from "@/screens/node/add-inbound";
import { ProfilesTab } from "@/screens/node/profiles";
import { ShareFix, ShareNotes } from "@/screens/node/share-fix";
import { SharedPortCards } from "@/screens/node/shared-port";
import { baseNode, Cache, Demo, inbound } from "@/screens/dev-kit-ports";

// Development only, part of /dev-kit (?only=share): services of one node that share a TCP port by the name in the connection (#51 S5). The
// refusals a dialog words before the click (a held name with the panel's suggestion, with no suggestion for an IP node, a name that is not the
// profile's to change, no site of the node's own, a service with no name, an agent that is too old), what a check that passes says (who
// restarts, which site a stranger sees, the DNS record), the "Add a profile" and "Edit" dialogs with calls answered here, and the node's
// card in each state. The made-up node is de1.example.com; its REALITY holds TCP 443 under the node's own name.

const now = Math.floor(Date.now() / 1000);
const node = { name: "de1", address: "de1.example.com" };
const ipNode = { name: "de3", address: "203.0.113.88" };

const taken = "port=443&node=de1&profile=Reality main&name=de1.example.com";
const frame = (children: ReactNode) => <div className="flex w-[440px] max-w-full flex-col gap-3 rounded-card border border-line bg-surface p-5">{children}</div>;
const noop = () => {};
const fix = (code: Parameters<typeof ShareFix>[0]["code"], vars: string, n = node) => (
  <ShareFix code={code} vars={Object.fromEntries(new URLSearchParams(vars))} node={n} onPut={noop} onTake={noop} />
);

const shared = (restarts = true) => ({ code: "tcp_port_shared", params: { node: "de1", port: "443", profiles: "Reality main, Telegram", default: "Reality main", ...(restarts && { restarts: "Reality main" }) } });
const faces = { code: "share_two_faces", params: { node: "de1", port: "443", profile: "Other site", default: "Reality main" } };
const okCheck = (name: string, ...warnings: object[]) => ({ state: "ok", inbound: { port: 443, tlsServerName: name }, warnings, freePort: 0 }) as never;

// ---- the calls behind the dialogs ----------------------------------------------------------------------------------------
const profileRows = [
  { id: "prf_tg", name: "Telegram", protocol: "mtproto", summary: "TCP 443 · Fake TLS", nodeCount: 1, userCount: 3, version: 2, warnings: [] },
  { id: "prf_tt", name: "TrustTunnel", protocol: "trusttunnel", summary: "TCP 443 · TLS", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
  { id: "prf_ro", name: "Reality, another site", protocol: "vless", summary: "TCP 443 · REALITY · Vision", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
  { id: "prf_ns", name: "Reality, two sites", protocol: "vless", summary: "TCP 443 · REALITY · Vision", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
  { id: "prf_old", name: "Telegram on an old node", protocol: "mtproto", summary: "TCP 443 · Fake TLS", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
];
const err = (msg: string, code = Code.AlreadyExists) => new ConnectError(msg, code);
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The panel's answers for a profile put on de1, whose REALITY holds 443 under the node's own name. */
function answer(profileId: string, port: number, sni: string) {
  const name = sni.trim().toLowerCase().replace(/\.$/, "");
  if (port !== 443) return { inbound: { port, tlsServerName: name || node.address }, warnings: [], freePort: 0 };
  const free = "&free=8443";
  if (profileId === "prf_tg" || profileId === "prf_tt") {
    if (!name || name === node.address) throw err(`sni_taken: ${taken}${free}&share=1&suggest=${profileId === "prf_tg" ? "cdn" : "static"}.${node.address}`);
    return { inbound: { port, tlsServerName: name }, warnings: [shared(profileId === "prf_tg")], freePort: 0 };
  }
  if (profileId === "prf_ro") throw err(`sni_taken: port=443&node=de1&profile=Telegram&name=www.example.net${free}`);
  if (profileId === "prf_ns") throw err(`share_needs_site: port=443&node=de1&profile=Reality main${free}`, Code.FailedPrecondition);
  if (profileId === "prf_old") throw err("share_agent_old: port=443&node=de1&profile=Reality main", Code.FailedPrecondition);
  return { inbound: { port, tlsServerName: node.address }, warnings: [], freePort: 0 };
}

function fakeShare() {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.createInbound = async (r: { profileId: string; portOverride: number; tlsServerNameOverride: string; validateOnly?: boolean }) => {
    const a = answer(r.profileId, r.portOverride || 443, r.tlsServerNameOverride ?? "");
    if (r.validateOnly) return a;
    await wait(1200);
    return { inbound: { id: "inb_new" }, warnings: a.warnings };
  };
  p.updateInbound = async (r: { inboundId: string; portOverride?: number; tlsServerNameOverride?: string; validateOnly?: boolean }) => {
    const a = answer("prf_tg", r.portOverride ?? 443, r.tlsServerNameOverride ?? node.address);
    if (r.validateOnly) return a;
    await wait(1200);
    return { inbound: {}, warnings: a.warnings };
  };
}

const reality = inbound({ id: "inb_a", profileId: "prf_reality", profileName: "Reality main", protocol: "vless", tlsServerName: node.address, certNotAfterUnix: 0 });
const addData = { node: baseNode({ address: node.address }), inbounds: [reality], portChecks: [] } as never;

function AddDemo({ label }: { label: string }) {
  const [open, setOpen] = useState(false);
  return (
    <Cache fill={[[profileListQuery.queryKey, profileRows]]}>
      <Button
        onClick={() => {
          fakeShare();
          setOpen(true);
        }}
      >
        {label}
      </Button>
      <AddInbound open={open} onOpenChange={setOpen} data={addData} initialProfile="prf_tg" />
    </Cache>
  );
}

// ---- the node's card -----------------------------------------------------------------------------------------------------------
const services = [
  { id: "inb_a", profileId: "prf_reality", profileName: "Reality main", protocol: "vless", name: node.address },
  { id: "inb_b", profileId: "prf_tg", profileName: "Telegram", protocol: "mtproto", name: `cdn.${node.address}` },
  { id: "inb_c", profileId: "prf_tt", profileName: "TrustTunnel", protocol: "trusttunnel", name: `static.${node.address}` },
];
const svInbound = (s: (typeof services)[number], over: object = {}) => inbound({ id: s.id, profileId: s.profileId, profileName: s.profileName, protocol: s.protocol, tlsServerName: s.name, port: 443, certNotAfterUnix: 0, ...over });
const snap = (over: object = {}) => ({
  port: 443,
  defaultInboundId: "inb_a",
  accepted: 18420,
  routes: [
    { inboundId: "inb_a", delivered: 9120 },
    { inboundId: "inb_b", delivered: 6210 },
    { inboundId: "inb_c", delivered: 1980 },
  ],
  defaultNoSni: 612,
  defaultUnknownName: 340,
  defaultNotTls: 118,
  defaultBadHello: 9,
  defaultHelloTooLarge: 0,
  defaultRouteDown: 0,
  timeouts: 23,
  evicted: 0,
  refused: 2,
  noRoute: 0,
  unknownNames: [
    { name: "www.example.org", count: 12 },
    { name: "mail.de1.example.com", count: 4 },
  ],
  bindError: "",
  ...over,
});
const cardData = (over: object = {}) => ({ node: baseNode({ address: node.address }), inbounds: services.map((s) => svInbound(s)), sharedPorts: [snap()], sharedPortsUnix: now - 90, portChecks: [], ...over }) as never;
const oldData = cardData({
  inbounds: [svInbound(services[0]!), ...services.slice(1).map((s) => svInbound(s, { state: InboundState.FAILED, lastError: "share_agent_old: this node's agent cannot share a TCP port between services by SNI (it lacks tcp-share/1), so only the default service of the port is sent to it, update the agent (mistgate-node install, or the self-update)" }))],
  sharedPorts: [],
  sharedPortsUnix: 0,
});

export function ShareKit() {
  const edit = [reality, svInbound(services[1]!, { port: 8443 })];
  return (
    <div className="relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-6">
      <Demo shot="share-offer" title="A held name: put it beside “Reality main” under a name of its own (the panel suggests a subdomain of de1.example.com) / the node is known by an IP address: no suggestion">
        <div className="flex flex-wrap items-start gap-4">
          {frame(fix("sni_taken", `${taken}&free=8443&share=1&suggest=cdn.de1.example.com`))}
          {frame(fix("sni_taken", "port=443&node=de3&profile=TrustTunnel&name=tt.example.com&free=8443&share=1", ipNode))}
        </div>
      </Demo>

      <Demo shot="share-refusals" title="The other refusals, each with its way out: the name is not the profile's to change (a free port) / no service shows the node's own site / a service has no name / the agent is too old">
        <div className="flex flex-wrap items-start gap-4">
          {frame(fix("sni_taken", "port=443&node=de1&profile=Telegram&name=www.example.net&free=8443"))}
          {frame(fix("share_needs_site", "port=443&node=de1&profile=Reality main&free=8443"))}
          {frame(fix("share_unsupported", "port=443&node=de1&profile=Reality main&protocol=vless"))}
          {frame(fix("share_agent_old", "port=443&node=de1&profile=Reality main&free=8443"))}
        </div>
      </Demo>

      <Demo shot="share-notes" title="A check that passes, before the click: who restarts and the name's DNS record / a third service (nobody restarts) / one shows another site / the name is the node's own (no record to make)">
        <div className="flex flex-wrap items-start gap-4">
          {frame(<ShareNotes check={okCheck(`cdn.${node.address}`, shared())} node={node} />)}
          {frame(<ShareNotes check={okCheck(`static.${node.address}`, shared(false))} node={node} />)}
          {frame(<ShareNotes check={okCheck(node.address, shared(false), faces)} node={node} />)}
          {frame(<ShareNotes check={okCheck("tt.example.com", shared())} node={ipNode} />)}
        </div>
      </Demo>

      <Demo shot="share-add" title="Add a profile to de1 (REALITY holds 443 under the node's own name): Telegram and TrustTunnel are offered a name, the others are refused with their reasons; Put beside runs the check again">
        <div className="flex flex-wrap gap-2">
          <AddDemo label="Open the add dialog" />
        </div>
      </Demo>

      <Demo shot="share-edit" title="Edit Telegram (TCP 8443): type 443 in the port, the held name is offered a subdomain">
        <Cache>
          <div onClickCapture={fakeShare}>
            <ProfilesTab data={{ node: baseNode({ address: node.address }), inbounds: edit, portChecks: [] } as never} />
          </div>
        </Cache>
      </Demo>

      <Demo shot="share-card" title="The card of the shared port: three services, the default marked, what the node counted">
        <Cache>
          <SharedPortCards data={cardData()} />
        </Cache>
      </Demo>

      <Demo shot="share-card-trouble" title="A flood (dropped handshakes, refusals, a service that was down, names nobody holds) / the node could not take the port">
        <Cache>
          <div className="flex flex-col gap-3">
            <SharedPortCards data={cardData({ sharedPorts: [snap({ accepted: 52311, evicted: 1480, refused: 37, timeouts: 410, defaultRouteDown: 5, unknownNames: [{ name: "www.example.org", count: 212 }, { name: "mail.de1.example.com", count: 40 }, { name: "vpn.example.net", count: 9 }] })] })} />
            <SharedPortCards data={cardData({ sharedPorts: [snap({ accepted: 0, routes: [], defaultNoSni: 0, defaultUnknownName: 0, defaultNotTls: 0, defaultBadHello: 0, timeouts: 0, refused: 0, unknownNames: [], bindError: "listen tcp :443: bind: address already in use" })] })} />
          </div>
        </Cache>
      </Demo>

      <Demo shot="share-card-states" title="The agent cannot share a port yet (the others wait for an update) / the node has not reported yet / one of the three is switched off (two share)">
        <Cache>
          <div className="flex flex-col gap-3">
            <SharedPortCards data={oldData} />
            <SharedPortCards data={cardData({ sharedPorts: [], sharedPortsUnix: 0 })} />
            <SharedPortCards data={cardData({ inbounds: [svInbound(services[0]!), svInbound(services[1]!), svInbound(services[2]!, { state: InboundState.DISABLED })], sharedPorts: [snap({ routes: [{ inboundId: "inb_a", delivered: 9120 }, { inboundId: "inb_b", delivered: 6210 }] })] })} />
          </div>
        </Cache>
      </Demo>

      <Demo shot="share-profiles-tab" title="The node's Profiles tab with the card above the list">
        <Cache>
          <ProfilesTab data={cardData()} />
        </Cache>
      </Demo>
    </div>
  );
}
