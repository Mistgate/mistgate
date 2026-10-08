import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { groups as groupsApi, nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { reasons } from "@/lib/port-check";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { AddInbound } from "@/screens/node/add-inbound";
import { ProfilesTab } from "@/screens/node/profiles";
import { UdpPorts } from "@/screens/node/udp-ports";
import { DeployDialog } from "@/screens/profiles/deploy";
import { TwinPanel, twinPlanKey } from "@/screens/profiles/twin";
import { groupsQuery, profileListQuery } from "@/screens/users/rpc";

// Development only, part of /dev-kit: the UDP delivery check (design/udp-port-check.md) on made-up data — the node's "UDP
// ports" block in every state, the refusal port_lossy in the add / edit / quick-profile dialogs and the deploy window, the
// twin's badges, no_clean_port. The calls a dialog makes are answered here (like the deploy demo), refusing as the panel does.

const now = Math.floor(Date.now() / 1000);

const profileRows = [
  { id: "prf_main", name: "hy2 · 443 · Salamander", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 3, userCount: 12, version: 4, warnings: [] },
  { id: "prf_warp", name: "hy2 · WARP · 8443", protocol: "hysteria2", summary: "UDP 8443 · Salamander · Let's Encrypt · WARP", nodeCount: 2, userCount: 12, version: 2, warnings: [] },
  { id: "prf_new", name: "Main", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
];
const kitNodes = [
  { id: "nod_de1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", address: "de1.example.com", protocols: ["hysteria2"], awgBackend: "auto" },
  { id: "nod_fi1", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI", address: "fi1.example.com", protocols: [], awgBackend: "auto" },
  { id: "nod_de2", name: "de2", status: NodeStatus.ONLINE, countryCode: "DE", address: "de2.example.com", protocols: [], awgBackend: "auto" },
  { id: "nod_se1", name: "se1", status: NodeStatus.ONLINE, countryCode: "SE", address: "se1.example.com", protocols: [], awgBackend: "auto" },
];

const baseNode = (over: object = {}) => ({
  ...kitNodes[0],
  location: "Frankfurt",
  provider: "examplehost",
  online: [{ protocol: "hysteria2", users: 5 }],
  agentVersion: "0.3.1",
  lastSeenUnix: now - 5,
  warp: { state: WarpState.NOT_CONFIGURED },
  ...over,
});
const inbound = (over: object) => ({
  id: "inb_1",
  profileId: "prf_main",
  profileName: "hy2 · 443 · Salamander",
  protocol: "hysteria2",
  nodeId: "nod_de1",
  nodeName: "de1",
  state: InboundState.ACTIVE,
  port: 443,
  tlsServerName: "de1.example.com",
  certNotAfterUnix: now + 70 * 86400,
  certPinSha256: "",
  lastError: "",
  egress: "direct",
  ...over,
});
const check = (over: object) => ({ nodeId: "nod_de1", port: 443, verdict: "ok", sent: 300, got: 300, checkedUnix: now - 7 * 60, badUnix: 0, sender: "de2", reason: "", ...over });

const twoInbounds = [inbound({}), inbound({ id: "inb_2", profileId: "prf_warp", profileName: "hy2 · WARP · 8443", port: 8443, egress: "warp" })];
const threeInbounds = [...twoInbounds, inbound({ id: "inb_3", profileId: "prf_awg", profileName: "AWG 3.1 · QUIC", protocol: "awg", port: 33183, tlsServerName: "", certNotAfterUnix: 0 }), inbound({ id: "inb_4", profileId: "prf_ext", profileName: "Backup", port: 51820 })];

type Fill = [readonly unknown[], unknown];

/** A private cache: every query the screens ask is answered from here, nothing is fetched. */
function Cache({ fill = [], role = Role.OWNER, children }: { fill?: Fill[]; role?: Role; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
    c.setQueryData(profileListQuery.queryKey, profileRows as never);
    c.setQueryData(nodesQuery.queryKey, { nodes: kitNodes } as never);
    c.setQueryData(groupsQuery.queryKey, [{ id: "grp_all", name: "Everyone", profileIds: [], userCount: 14, dnsPresetId: "" }] as never);
    for (const [key, value] of fill) c.setQueryData(key, value as never);
    return c;
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function Demo({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <div className="rounded-card-lg border border-dashed border-line p-4">{children}</div>
    </section>
  );
}

// ---- the calls behind the dialogs ---------------------------------------------------------------------------------------
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
const lossy = (node: string, port: number, free: number) => `port_lossy: port=${port}&node=${node}&sent=300&got=189&at=${now - 120}&sender=de2&free=${free}`;
const warn = (code: string, params: Record<string, string>) => ({ code, params });
let checkCode = "";

/** Dev only: the calls of these demos answered here. Arms on a click, so another demo's fake does not leave it half-working. */
function fakePorts() {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.createInbound = async (r: { nodeId: string; portOverride: number; validateOnly?: boolean; allowLossyPort?: boolean }) => {
    if (r.nodeId === "nod_de1") {
      // the add dialog and the quick profile
      if (r.validateOnly) {
        if (r.portOverride === 0 && quickRun) throw new ConnectError(lossy("de1", 443, 2053), Code.FailedPrecondition);
        return { inbound: { port: r.portOverride || 8443, tlsServerName: "de1.example.com", nodeName: "de1" }, warnings: [], freePort: 0 };
      }
      await wait(3000); // the run that proves the port: up to ~8 s
      if (!quickRun && (r.portOverride === 0 || r.portOverride === 8443) && !r.allowLossyPort) throw new ConnectError(lossy("de1", 8443, 4443), Code.FailedPrecondition);
      if (r.allowLossyPort) return { inbound: {}, warnings: [warn("port_lossy", { node: "de1", port: "8443", sent: "300", got: "189", at: String(now - 120), sender: "de2" })] };
      return { inbound: {}, warnings: r.portOverride === 4443 ? [warn("port_unchecked", { node: "de1", port: "4443", reason: "no_sender" })] : [] };
    }
    // the deploy window: fi1 loses packets on 443, de2 could not be checked, se1 is clean
    await wait(1200);
    if (r.nodeId === "nod_fi1" && !r.allowLossyPort && r.portOverride !== 2053) throw new ConnectError(lossy("fi1", 443, 2053), Code.FailedPrecondition);
    if (r.nodeId === "nod_fi1" && r.allowLossyPort) return { inbound: {}, warnings: [warn("port_lossy", { node: "fi1", port: "443", sent: "300", got: "189", at: String(now - 120), sender: "de2" })] };
    if (r.nodeId === "nod_de2") return { inbound: {}, warnings: [warn("port_unchecked", { node: "de2", port: "443", reason: "agent_too_old" })] };
    return { inbound: {}, warnings: [] };
  };
  p.updateInbound = async (r: { portOverride?: number; validateOnly?: boolean; allowLossyPort?: boolean }) => {
    if (r.validateOnly) return { inbound: { port: r.portOverride ?? 443 }, warnings: [], freePort: 0 };
    await wait(3000);
    if (r.portOverride === 8443 && !r.allowLossyPort) throw new ConnectError(lossy("de1", 8443, 4443), Code.FailedPrecondition);
    return { inbound: {}, warnings: r.allowLossyPort ? [warn("port_lossy", { node: "de1", port: "8443", sent: "300", got: "189", at: String(now - 120), sender: "de2" })] : [] };
  };
  p.createProfile = async () => ({ profile: { id: "prf_quick", name: "Hysteria2 · 443", version: 1 } });
  p.updateProfile = async () => ({ profile: { id: "prf_quick", version: 2 } });
  (groupsApi as unknown as Record<string, unknown>).updateGroup = async () => ({});
  p.twinProfile = async (r: { profileId: string }) => {
    await wait(r.profileId === "prf_slow" ? 5000 : 3000);
    if (r.profileId === "prf_none") throw new ConnectError("no_clean_port", Code.FailedPrecondition);
    return twinPlan;
  };
  (nodesApi as unknown as Record<string, unknown>).checkPorts = async () => {
    await wait(7000);
    const ports = [check({}), check({ port: 8443, verdict: "lossy", got: 270, badUnix: now })];
    if (!checkCode || checkCode === "ok") return { ports, errorCode: "", sender: "de2" };
    // the counts that did arrive come with inconclusive and same_host
    return { ports: checkCode === "inconclusive" || checkCode === "same_host" ? [check({ verdict: "", got: 120, reason: checkCode }), check({ port: 8443, verdict: "", got: 90, reason: checkCode })] : [], errorCode: checkCode, sender: "" };
  };
}
let quickRun = false;

// ---- the node's block ---------------------------------------------------------------------------------------------------
function RunDemo() {
  const [code, setCode] = useState("ok");
  return (
    <Cache>
      <div className="flex flex-col gap-3" onClickCapture={fakePorts}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted">The next check ends with:</span>
          <Segmented variant="thumb" aria-label="Result of the next check" value={code} onValueChange={(v) => {
              checkCode = v;
              setCode(v);
            }} options={["ok", ...reasons].map((r) => ({ value: r, label: r }))} />
        </div>
        <UdpPorts data={{ node: baseNode(), inbounds: twoInbounds, portChecks: [check({ checkedUnix: now - 3600 }), check({ port: 8443, verdict: "lossy", got: 270 })] } as never} />
      </div>
    </Cache>
  );
}

// ---- the dialogs --------------------------------------------------------------------------------------------------------
const createCheck = (nodeId: string, profileId: string, port: string) => ["inbound-check", { kind: "create", nodeId, profileId, port, sni: "" }] as const;

function AddDemo({ label, quick, checks, list }: { label: string; quick?: boolean; checks: Fill[]; list?: object[] }) {
  const [open, setOpen] = useState(false);
  const fill: Fill[] = [...checks];
  if (list) fill.push([profileListQuery.queryKey, list]);
  return (
    <Cache fill={fill}>
      <Button
        onClick={() => {
          quickRun = !!quick;
          fakePorts();
          setOpen(true);
        }}
      >
        {label}
      </Button>
      <AddInbound open={open} onOpenChange={setOpen} data={{ node: baseNode(), inbounds: [], portChecks: [] } as never} />
    </Cache>
  );
}

function DeployDemo() {
  const [open, setOpen] = useState(false);
  return (
    <Cache>
      <Button
        onClick={() => {
          fakePorts();
          setOpen(true);
        }}
      >
        Put “hy2 · 443” on fi1 / de2 / se1
      </Button>
      <DeployDialog open={open} onOpenChange={setOpen} profile={{ id: "prf_main", name: "hy2 · 443 · Salamander", protocol: "hysteria2", tlsMode: "acme_domain", sni: "" }} inbounds={[{ nodeId: "nod_de1" }]} preselect={["nod_fi1", "nod_de2", "nod_se1"]} />
    </Cache>
  );
}

// ---- the twin -----------------------------------------------------------------------------------------------------------
const twinPlan = {
  name: "hy2 · 443 · WARP",
  egress: "warp",
  port: 2053,
  nodeIds: ["nod_de1", "nod_fi1"],
  groupIds: ["grp_all"],
  hopDropped: false,
  portChecks: [
    { nodeId: "nod_de1", port: 2053, verdict: "ok", sent: 300, got: 300, checkedUnix: BigInt(now - 5), badUnix: 0n, sender: "de2", reason: "" },
    { nodeId: "nod_fi1", port: 2053, verdict: "", sent: 0, got: 0, checkedUnix: 0n, badUnix: 0n, sender: "", reason: "no_sender" },
    { nodeId: "nod_de1", port: 8443, verdict: "lossy", sent: 300, got: 189, checkedUnix: BigInt(now - 5), badUnix: BigInt(now - 5), sender: "de2", reason: "" },
  ],
};
const twinProfile = (id: string, version: number) => ({ id, name: "hy2 · 443 · Salamander", protocol: "hysteria2", nodeCount: 2, userCount: 12, version, warnings: [] }) as unknown as ProfileSummary;

function TwinDemo({ caption, profile, plan }: { caption: string; profile: ProfileSummary; plan?: object }) {
  const fill: Fill[] = plan ? [[twinPlanKey(profile.id, "warp", profile.version), plan]] : [];
  return (
    <Cache fill={[...fill, [nodesQuery.queryKey, { nodes: kitNodes.map((n) => ({ ...n, warp: { state: WarpState.UP } })) }]]}>
      <div onClickCapture={fakePorts} className="flex min-w-[260px] flex-col gap-1.5">
        <span className="text-xs text-muted">{caption}</span>
        <TwinPanel profile={profile} egress="direct" dirty={false} />
      </div>
    </Cache>
  );
}

export function PortsKit() {
  const editFill: Fill[] = [[["inbound-check", { kind: "update", inboundId: "inb_1", port: "8443" }], { of: "inb_1", state: "refused", code: "port_lossy", vars: { port: "8443", node: "de1", sent: "300", got: "189", at: String(now - 120), sender: "de2", free: "4443" } }]];
  return (
    <div className="relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-6">
      <Demo title="UDP ports (node, Profiles tab): never checked">
        <Cache>
          <ProfilesTab data={{ node: baseNode(), inbounds: twoInbounds, portChecks: [] } as never} />
        </Cache>
      </Demo>

      <Demo title="UDP ports: ok with an old loss (clean now) / lossy from the panel / broken / a free port nobody uses / a profile port not in the last check">
        <Cache>
          <ProfilesTab
            data={
              {
                node: baseNode(),
                inbounds: threeInbounds,
                portChecks: [
                  check({ badUnix: now - 3 * 86400, checkedUnix: now - 2 * 3600 }),
                  check({ port: 8443, verdict: "lossy", got: 270, sender: "panel", checkedUnix: now - 40 }),
                  check({ port: 33183, verdict: "broken", got: 120 }),
                  check({ port: 2053 }),
                ],
              } as never
            }
          />
        </Cache>
      </Demo>

      <Demo title="UDP ports: the check takes ~7 s here — pick how it ends (each reason has its one line); no sender is the one-node edge fleet">
        <RunDemo />
      </Demo>

      <Demo title="UDP ports: the agent is offline / a read-only admin">
        <div className="flex flex-col gap-3">
          <Cache>
            <UdpPorts data={{ node: baseNode({ status: NodeStatus.DOWN }), inbounds: twoInbounds, portChecks: [check({}), check({ port: 8443, verdict: "broken", got: 100 })] } as never} />
          </Cache>
          <Cache role={Role.READONLY}>
            <UdpPorts data={{ node: baseNode(), inbounds: twoInbounds, portChecks: [check({})] } as never} />
          </Cache>
        </div>
      </Demo>

      <Demo title="Add a profile: the port of the profile loses packets (clean port filled in) / the real run refuses and the button waits 3 s / quick profile moves to the clean port / edit a node's port">
        <div className="flex flex-wrap gap-2">
          <AddDemo
            label="443 loses packets (stored checks)"
            list={[profileRows[2]!]}
            checks={[
              [createCheck("nod_de1", "prf_new", ""), { of: "nod_de1/prf_new", state: "refused", code: "port_lossy", vars: { port: "443", node: "de1", sent: "300", got: "189", at: String(now - 120), sender: "de2", free: "2053" } }],
              [createCheck("nod_de1", "prf_new", "2053"), { of: "nod_de1/prf_new", state: "ok", inbound: { port: 2053, tlsServerName: "de1.example.com", nodeName: "de1" }, warnings: [], freePort: 0 }],
            ]}
          />
          <AddDemo
            label="8443: refused when saving, Take / Add anyway"
            list={[profileRows[1]!]}
            checks={[[createCheck("nod_de1", "prf_warp", ""), { of: "nod_de1/prf_warp", state: "ok", inbound: { port: 8443, tlsServerName: "de1.example.com", nodeName: "de1" }, warnings: [], freePort: 0 }]]}
          />
          <AddDemo label="Quick profile, 443 loses packets" quick list={[]} checks={[]} />
        </div>
        <div className="pt-3">
          <Cache fill={editFill}>
            <div onClickCapture={fakePorts}>
              <ProfilesTab data={{ node: baseNode(), inbounds: [inbound({})], portChecks: [check({})] } as never} />
              <p className="pt-2 text-xs text-muted">Edit → type 8443: refused from the stored checks (Take 4443 / Save anyway).</p>
            </div>
          </Cache>
        </div>
      </Demo>

      <Demo title="Put a profile on nodes: fi1 loses packets on 443 (clean port offered, or put on anyway), de2 could not be checked, se1 is clean">
        <DeployDemo />
      </Demo>

      <Demo title="Twin: UDP badge per node (checked / unchecked: reason), a skipped candidate / checking on 2 nodes, then the plan / no clean port">
        <div className="flex flex-wrap gap-2">
          <TwinDemo caption="The plan is here" profile={twinProfile("prf_main", 4)} plan={twinPlan} />
          <TwinDemo caption="Checking UDP on 2 nodes… (5 s), then the plan" profile={twinProfile("prf_slow", 5)} />
          <TwinDemo caption="Checking (3 s), then no_clean_port" profile={twinProfile("prf_none", 6)} />
        </div>
      </Demo>
    </div>
  );
}
