import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { BundleStatus, NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { health, nodes as nodesApi, provisioning, updates as updatesApi } from "@/lib/api";
import type { Job, Row } from "@/lib/batch";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { NodesScreen } from "@/screens/nodes";
import { BatchPanel, BulkDock, type Avail } from "@/components/bulk";
import { BulkControls } from "@/screens/nodes-bulk";
import { AgentDialog, BindDialog, panelFor, PasswordDialog, UnbindDialog, type ActionId, type BulkRun } from "@/screens/nodes-bulk-actions";
import { PanelKeyOffer } from "@/screens/nodes-panel-key";
import { Dock, Sidebar } from "@/screens/shell";

// Development only. /dev-kit?only=bulk: the Nodes page's bulk bar drawn in place (no portal), on a desktop width and in a
// 390 px frame: three selected with one hidden by the filter, its menus, the confirms (the password one with a skipped
// node, a key to bind, one to unbind, the agent's rollout), a batch with every row state, its summary with a failure and
// Retry, and a finished port check. /dev-kit?only=bulk-page: the real Nodes screen beside the shell's sidebar and dock, on
// made-up nodes whose calls are answered here (nl1 does not confirm its new password, fi1's port check finds loss).

const now = Math.floor(Date.now() / 1000);
const owner = { admin: { id: "adm_1", role: Role.OWNER, displayName: "Owner" }, version: "dev" };
const panelFp = "SHA256:3kQz7mVb1xN8cR2tY5pL0sW9hD4gF6aJ8uE1oK7iM3n";

type Over = Record<string, unknown>;
const node = (name: string, cc: string, location: string, address: string, over: Over = {}) => ({
  id: `nod_${name}`,
  name,
  countryCode: cc,
  location,
  provider: "examplehost",
  address,
  status: NodeStatus.ONLINE,
  reason: undefined,
  protocols: ["hysteria2", "vless"],
  online: [{ protocol: "hysteria2", users: 4 }],
  trafficTodayBytes: 6.4e9,
  hasMetrics: true,
  cpuPct: 14,
  ramPct: 41,
  uptimeS: 86400 * 9,
  agentVersion: "0.3.1",
  lastSeenUnix: now,
  awgBackend: "auto",
  decoyMode: "builtin",
  decoySupported: true,
  decoyReasonCode: "",
  decoyBundleSha256: "",
  decoyPanelAvailable: true,
  decoyOwnAvailable: true,
  decoyAgentMode: "builtin",
  decoyAgentSha256: "",
  ...over,
});
const fleet = [
  node("de1", "DE", "Frankfurt", "203.0.113.7", { agentVersion: "0.3.0" }),
  node("nl1", "NL", "Amsterdam", "203.0.113.24", { online: [{ protocol: "hysteria2", users: 7 }], cpuPct: 27, agentVersion: "0.3.0" }),
  node("fi1", "FI", "Helsinki", "198.51.100.12", { online: [{ protocol: "vless", users: 2 }], cpuPct: 6 }),
  node("lv1", "LV", "Riga", "203.0.113.41", { status: NodeStatus.DOWN, online: [], hasMetrics: false, uptimeS: 0, trafficTodayBytes: 0 }),
  node("msk1", "RU", "Moscow", "192.0.2.40", { protocols: ["hysteria2"], online: [{ protocol: "hysteria2", users: 3 }], agentVersion: "0.2.4" }),
];
const access = (name: string, host: string, over: Over = {}) => ({ nodeId: `nod_${name}`, nodeName: name, host, port: 22, username: "root", fingerprint: "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0", configuredUnix: now - 86400 * 20, rotationPending: false, passwordGenerated: false, nodeRetired: false, passwordSaved: true, passwordLogin: true, panelKeyFingerprint: "", ...over });
// de1 and msk1 log in by password only, nl1 by the panel key and a password, fi1 by the key only, lv1 has no saved access
const fleetAccess = [access("de1", "203.0.113.7"), access("nl1", "203.0.113.24", { panelKeyFingerprint: panelFp }), access("fi1", "198.51.100.12", { passwordSaved: false, passwordLogin: false, panelKeyFingerprint: panelFp }), access("msk1", "192.0.2.40")];
const upd = (name: string, state: NodeUpdateState, over: Over = {}) => ({ nodeId: `nod_${name}`, name, state, supportsUpdate: true, crashGuard: true, built: 1, version: "0.3.0", onlineUsers: 4, inbounds: 2, scheduledUnix: 0, address: "", arch: "amd64", ...over });
const fleetUpdates = {
  nodes: [upd("de1", NodeUpdateState.OUTDATED), upd("nl1", NodeUpdateState.OUTDATED, { onlineUsers: 7 }), upd("fi1", NodeUpdateState.UP_TO_DATE, { version: "0.3.1", built: 2 }), upd("lv1", NodeUpdateState.OFFLINE), upd("msk1", NodeUpdateState.UNSUPPORTED, { supportsUpdate: false, version: "0.2.4" })],
  bundle: { status: BundleStatus.TRUSTED, version: "0.3.1", built: 2 },
  nowUnix: now,
};

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
/** The calls the page makes, answered here: dev only. */
function fakeCalls() {
  const prov = provisioning as unknown as Record<string, unknown>;
  prov.listNodeServerAccess = async () => ({ access: fleetAccess });
  prov.getPanelSSHKey = async () => ({ publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f2mQ9xW4bN1cR8vT3yL6pZ0sJ5hD2gF9aE7uK4oM mistgate-panel", fingerprint: panelFp });
  prov.rotateNodeServerPassword = async ({ nodeId }: { nodeId: string }) => {
    await wait(1400);
    if (nodeId === "nod_nl1") throw new ConnectError("ssh_password_change_unverified", Code.Unavailable);
    return { rotated: true };
  };
  prov.addNodeServerKey = async () => {
    await wait(900);
    return { key: {} };
  };
  prov.removeNodeServerKey = async ({ nodeId }: { nodeId: string }) => {
    await wait(900);
    if (nodeId === "nod_fi1") throw new ConnectError("ssh_public_key_not_found", Code.NotFound);
    return {};
  };
  (nodesApi as unknown as Record<string, unknown>).checkPorts = async ({ nodeId }: { nodeId: string }) => {
    await wait(1200);
    if (nodeId === "nod_fi1") return { errorCode: "", sender: "de1", ports: [{ verdict: "ok" }, { verdict: "lossy" }], tcp: [{ verdict: "ours" }] };
    return { errorCode: "", sender: "panel", ports: [{ verdict: "ok" }, { verdict: "ok" }], tcp: [{ verdict: "ours" }] };
  };
  (nodesApi as unknown as Record<string, unknown>).setNodesDecoyMode = async () => ({ nodes: [] });
  (health as unknown as Record<string, unknown>).runDoctor = async ({ nodeId }: { nodeId: string }) => {
    await wait(1000);
    const items = nodeId === "nod_nl1" ? [{ status: DoctorStatus.WARN }, { status: DoctorStatus.OK }] : [{ status: DoctorStatus.OK }];
    return { nowUnix: BigInt(now), nodes: [{ nodeId, agentSupported: true, hasReport: true, items }] };
  };
  (updatesApi as unknown as Record<string, unknown>).startRollout = async () => ({});
}

function cache() {
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false, enabled: false } } });
  c.setQueryData(meQuery.queryKey, owner as never);
  c.setQueryData(updatesQuery.queryKey, fleetUpdates as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: fleet, expectedAgentVersion: "0.3.1" } as never);
  c.setQueryData(["node-server-access"], { access: fleetAccess });
  return c;
}

function Shot({ id, title, phone, tall, children }: { id: string; title: string; phone?: boolean; tall?: boolean; children: ReactNode }) {
  const [qc] = useState(cache);
  return (
    <section data-shot={id} className={`flex w-full flex-col gap-3 ${phone ? "max-w-[390px]" : ""}`}>
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          {/* the room a menu needs above the bar it opens from */}
          <div className={tall ? "flex min-h-[380px] flex-col justify-end" : "flex flex-col"}>{children}</div>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

/** Three selected: one logs in by key only (no password to change), all three already use the panel key (off, and says why). */
function Bar({ busy }: { busy?: boolean }) {
  const t = useT();
  const avail: Record<ActionId, Avail> = {
    ports: { count: 3 },
    doctor: { count: 3 },
    agent: { count: 1 },
    password: { count: 2 },
    panelKey: { count: 0, why: t("bulk.skip.hasPanelKey") },
    bindKey: { count: 3 },
    unbindKey: { count: 3 },
    decoy: { count: 3 },
  };
  return <BulkControls count={3} hidden={1} avail={avail} agentOff={false} busy={!!busy} onAction={() => {}} onClear={() => {}} />;
}

const row = (name: string, host: string, state: Row["state"], over: Partial<Row> = {}): Row => ({ id: `nod_${name}`, name, host, state, ...over });

function useJobs() {
  const t = useT();
  const running: Job = {
    spec: { action: "password", limit: 1, error: () => "" },
    running: true,
    stopping: false,
    rows: [
      row("de1", "203.0.113.7", "ok", { link: true }),
      row("nl1", "203.0.113.24", "bad", { note: t("node.sshAccess.error.unverified") }),
      row("fi1", "198.51.100.12", "running"),
      row("lv1", "203.0.113.41", "waiting"),
      row("msk1", "192.0.2.40", "skipped", { note: t("bulk.skip.keyOnly") }),
    ],
  };
  const finished: Job = {
    ...running,
    spec: { ...running.spec, call: async () => {} },
    running: false,
    rows: [
      row("de1", "203.0.113.7", "ok", { link: true }),
      row("nl1", "203.0.113.24", "bad", { note: t("node.sshAccess.error.unverified") }),
      row("fi1", "198.51.100.12", "ok", { link: true }),
      row("lv1", "203.0.113.41", "notStarted"),
      row("msk1", "192.0.2.40", "skipped", { note: t("bulk.skip.keyOnly") }),
    ],
  };
  const ports: Job = {
    spec: { action: "ports", limit: 1, error: () => "", call: async () => {} },
    running: false,
    stopping: false,
    rows: [
      row("de1", "203.0.113.7", "ok", { kind: "ok", label: t("bulk.result.ok"), note: t.n("bulk.ports.ok", 3), link: true }),
      row("nl1", "203.0.113.24", "ok", { kind: "warn", label: t("bulk.result.warn"), note: `${t.n("bulk.ports.ok", 2)}, ${t.n("bulk.ports.lossy", 1)}`, link: true }),
      row("fi1", "198.51.100.12", "ok", { kind: "bad", label: t("bulk.result.bad"), note: `${t.n("bulk.ports.ok", 1)}, ${t.n("bulk.ports.broken", 2)}`, link: true }),
      row("lv1", "203.0.113.41", "skipped", { note: t("bulk.skip.offline") }),
    ],
  };
  return { running, finished, ports };
}

/** A run whose rules read the made-up fleet; the confirm's button does nothing here. */
const kitRun: BulkRun = {
  ctx: { access: new Map(fleetAccess.map((a) => [a.nodeId, a])), updates: fleetUpdates as never },
  run: async () => {},
};

// de1, fi1 (key only) and msk1, msk1 hidden by the filter; the agent's window gets the outdated de1 and nl1 among four
const picked = [fleet[0]!, fleet[2]!, fleet[4]!] as never;
const hidden = new Set(["nod_msk1"]);

/** A confirm behind an "Open" button (one window at a time); its own button does nothing here. */
function Confirm({ id, title, which }: { id: string; title: string; which: "password" | "bind" | "unbind" | "agent" }) {
  const [on, setOn] = useState(false);
  const props = { nodes: picked, hidden, run: kitRun, onClose: () => setOn(false) };
  return (
    <Shot id={id} title={title}>
      <div>
        <Button data-open={id} variant="secondary" onClick={() => setOn(true)}>
          Open
        </Button>
        {on && which === "password" && <PasswordDialog {...props} />}
        {on && which === "bind" && <BindDialog {...props} />}
        {on && which === "unbind" && <UnbindDialog {...props} />}
        {on && which === "agent" && <AgentDialog {...props} nodes={[fleet[0]!, fleet[1]!, fleet[2]!, fleet[4]!] as never} />}
      </div>
    </Shot>
  );
}

export function BulkKit() {
  const t = useT();
  const jobs = useJobs();
  const noop = () => {};
  const frames = (phone: boolean) => (
    <>
      <Shot id={phone ? "bulk-bar-phone" : "bulk-bar"} title="Three selected, one hidden by the filter" phone={phone} tall>
        <BulkDock float={false} region={t("bulk.region")}>
          <Bar />
        </BulkDock>
      </Shot>
      <Shot id={phone ? "bulk-running-phone" : "bulk-running"} title="A batch: done, failed, running, waiting, skipped" phone={phone}>
        <BulkDock float={false} wide region={t("bulk.region")}>
          <BatchPanel job={jobs.running} rows={jobs.running.rows} head={panelFor(t, "password").head} open={panelFor(t, "password").open} onStop={noop} onRetry={noop} onClose={noop} />
          <Bar busy />
        </BulkDock>
      </Shot>
      <Shot id={phone ? "bulk-summary-phone" : "bulk-summary"} title="Its end: a failure, one not started, Retry" phone={phone}>
        <BulkDock float={false} wide region={t("bulk.region")}>
          <BatchPanel job={jobs.finished} rows={jobs.finished.rows} head={panelFor(t, "password").head} open={panelFor(t, "password").open} onStop={noop} onRetry={noop} onClose={noop} />
          <Bar />
        </BulkDock>
      </Shot>
      <Shot id={phone ? "bulk-ports-phone" : "bulk-ports"} title="A port check: each node's verdict" phone={phone}>
        <BulkDock float={false} wide region={t("bulk.region")}>
          <BatchPanel job={jobs.ports} rows={jobs.ports.rows} head={panelFor(t, "ports").head} open={panelFor(t, "ports").open} onStop={noop} onRetry={noop} onClose={noop} />
          <Bar />
        </BulkDock>
      </Shot>
    </>
  );
  return (
    // no viewport units here: a screenshot taken past the viewport resizes it for a moment, and the column must not follow
    <div className="mx-auto flex w-full max-w-[1000px] flex-col gap-8">
      {frames(false)}
      <div className="flex flex-wrap items-start gap-8">{frames(true)}</div>
      <Shot id="bulk-offer" title="The offer above the list">
        <PanelKeyOffer busy={false} onStart={noop} />
      </Shot>
      <Confirm id="bulk-password" title="Confirm: change the SSH password" which="password" />
      <Confirm id="bulk-bind" title="Confirm: bind own key" which="bind" />
      <Confirm id="bulk-unbind" title="Confirm: unbind a key" which="unbind" />
      <Confirm id="bulk-agent" title="Confirm: update the agent" which="agent" />
      <p className="text-xs text-muted">{t("bulk.pace")}</p>
    </div>
  );
}

/** The real Nodes screen in the shell's frame: sidebar, the page, the dock (the bar takes its place on a phone). */
export function BulkPage() {
  const [qc] = useState(() => {
    fakeCalls();
    const c = cache();
    c.setQueryData(["integrations", "awaiting"], { awaiting: 0 });
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <div className="min-h-dvh md:ps-56">
          <Sidebar />
          <main className="px-4 pt-4 pb-24 md:px-[clamp(24px,4vw,64px)] md:pt-6 md:pb-8">
            <div className="mx-auto max-w-[1360px]">
              <NodesScreen />
            </div>
          </main>
          <Dock />
        </div>
      </ToastProvider>
    </QueryClientProvider>
  );
}
