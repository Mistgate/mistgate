import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { AddNodeProvider, useAddNode } from "@/components/add-node";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { editionFeatures } from "@/i18n/edition";
import { countryQuery } from "@/lib/globalping";
import { alertsQuery, checksQuery } from "@/lib/health";
import { approvalsQuery, tokensQuery } from "@/lib/integrations";
import { overviewQuery, userCountQuery } from "@/lib/queries";
import { telegramQuery } from "@/lib/telegram";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { IntegrationsScreen } from "@/screens/integrations";
import { HealthScreen } from "@/screens/health";
import { BackupsPage } from "@/screens/settings-pages/backups";
import { UpdatesScreen } from "@/screens/updates";
import { available, page as updatesPage, panelOffer, upNode } from "./dev-kit-redesign";

// Development only (the /dev-kit page): the Cloudflare ("edge") edition. Me says edition "edge" with every feature unavailable,
// and the screens that carry a feature say so in place of its buttons. Private query caches with demo data, no panel behind them.
// ?only=edge&tab=people shows the People tab of every Health shot.

const now = Math.floor(Date.now() / 1000);
const edgeMe = { admin: { id: "adm_1", displayName: "Owner", role: Role.OWNER }, version: "0.1.24", stepUpUntilUnix: 0n, edition: "edge", unavailable: [...editionFeatures] };

const vpsMe = { ...edgeMe, edition: "vps", unavailable: [] };

function Shot({ id, title, seed = [], me = edgeMe, children }: { id: string; title: string; seed?: [readonly unknown[], unknown][]; me?: object; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, me as never);
    for (const [key, data] of seed) c.setQueryData(key, data);
    return c;
  });
  return (
    <section data-shot={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <AddNodeProvider>
          <div className="flex flex-col gap-3.5">{children}</div>
        </AddNodeProvider>
      </QueryClientProvider>
    </section>
  );
}

const token = (over: object) => ({
  id: "tok_1", name: "claude-ops", profile: TokenProfile.OPERATOR, createdUnix: now - 86400 * 3, expiresUnix: now + 86400 * 60, lastUsedUnix: now - 180,
  lastUsedIp: "203.0.113.7", lastUsedVia: TokenChannel.API, revokedUnix: 0, createdByName: "Alice", rateLimitPerMin: 120, hint: "ab12", ...over,
});

const tokens = { nowUnix: now, tokens: [token({}), token({ id: "tok_2", name: "nightly-report", profile: TokenProfile.READONLY, hint: "zz99", lastUsedUnix: now - 86400 })] };
const telegramOff = { bot: { configured: false, username: "", error: "" }, links: [], adminUrlKnown: false };
const approvals = { awaiting: 0, nowUnix: now, receivedMs: Date.now(), approvals: [] };

// Health with two quiet nodes, no alert and no check result: the same data on both editions (?tab=people etc. switches the tab).
const fleetNode = (id: string, name: string) => ({ id, name, status: NodeStatus.ONLINE, countryCode: id === "nod_de1" ? "DE" : "NL", sparkBytes: [] });
const overview = { nowUnix: now, nodesTotal: 2, nodesProblem: 0, usersOnline: 3, nodes: [fleetNode("nod_de1", "de1"), fleetNode("nod_nl1", "nl1")], traffic: [], online: [], events: [], topConsumers: [], alertsActive: 0, alertsCritical: 0 };
const healthRest: [readonly unknown[], unknown][] = [
  [userCountQuery.queryKey, { counts: { all: 3 } }],
  [checksQuery().queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [] }],
];
const healthSeed: [readonly unknown[], unknown][] = [[overviewQuery().queryKey, overview], [alertsQuery.queryKey, { nowUnix: now, active: [], history: [] }], ...healthRest];

// The same with de1 down for 12 minutes and alice turned away by torrent protection (an alert about a person: the People tab).
const alertBase = { nodeId: "", nodeName: "", subject: "", resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, lastSeenUnix: now };
const nodeDown = {
  ...alertBase, id: "alt_down", severity: AlertSeverity.CRITICAL, kind: AlertKind.NODE_DOWN, nodeId: "nod_de1", nodeName: "de1",
  titleKey: "health.alert.node_down.title", whyKey: "health.alert.node_down.why", params: { minutes: "12" },
  firstSeenUnix: now - 720, openedUnix: now - 720, actions: ["open_node", "mute"],
};
const torrent = {
  ...alertBase, id: "alt_torrent", severity: AlertSeverity.WARNING, kind: AlertKind.TORRENT, subject: "usr_alice",
  titleKey: "health.alert.torrent.title", whyKey: "health.alert.torrent.why", firstSeenUnix: now - 3600, openedUnix: now - 3600, actions: ["open_user", "mute"],
  params: { user_id: "usr_alice", user_name: "alice", nodes: "nl1", count: "4", last_unix: String(now - 300), evidence: "tracker_connect", ports: "6969" },
};
const downSeed: [readonly unknown[], unknown][] = [
  [overviewQuery().queryKey, { ...overview, nodesProblem: 1, alertsActive: 1, alertsCritical: 1, nodes: [{ ...fleetNode("nod_de1", "de1"), status: NodeStatus.DOWN }, fleetNode("nod_nl1", "nl1")] }],
  [alertsQuery.queryKey, { nowUnix: now, active: [nodeDown, torrent], history: [] }],
  ...healthRest,
];

// ---- Updates once node updates are wired there: Me lists every gap but agent_updates. Nodes follow the panel's own signed
// release (v0.1.50), offered for download (no dist folder); nl1 runs v0.1.47, an agent without update-link/1, updated by hand
// once. The panel row is updated with mistgate edge deploy. The VPS shots carry the same nodes on a VPS panel.
const updMe = { ...edgeMe, unavailable: editionFeatures.filter((f) => f !== "agent_updates") };
const REL = "v0.1.50";
const B50 = now - 6 * 3600;
const hex = (seed: string) => seed.repeat(64).slice(0, 64);
const edgeBundle = {
  status: BundleStatus.TRUSTED, version: REL, built: B50, expiresUnix: now + 88 * 86400, errorKey: "", params: {}, scannedUnix: now - 120,
  files: [
    { os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 23_400_000, sha256: hex("9dd4c01e") },
    { os: "linux", arch: "arm64", name: "mistgate-node-linux-arm64", size: 21_900_000, sha256: hex("1a2b3c4d") },
  ],
  downloadBase: `https://github.com/Mistgate/mistgate/releases/download/${REL}/`,
};
const panelRow = (update: object) => ({
  version: REL, built: B50, hasReleaseKey: true, releaseKeyFingerprint: "abcd1234abcd1234",
  update: { version: "", url: "", publishedUnix: 0, checkedUnix: 0, available: false, supported: false, installable: false, installing: false, errorKey: "unsupported", built: 0, sha256: "", ...update },
});
const newer = { version: "v0.1.51", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.1.51", publishedUnix: now - 3600, checkedUnix: now - 300, available: true, errorKey: "", built: now - 3600, sha256: hex("77ab10") };
const current = { version: REL, url: `https://github.com/Mistgate/mistgate/releases/tag/${REL}`, publishedUnix: B50, checkedUnix: now - 300, errorKey: "", built: B50, sha256: hex("9dd4c01e") };
const n50 = (over: object) => upNode({ version: REL, built: B50, ...over });
const legacy = upNode({ nodeId: "nod_nl1", name: "nl1", version: "v0.1.47", built: now - 20 * 86400, state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false, onlineUsers: 2, address: "nl1.example.com" });
const edgeUpd = (over: object) => updatesPage({ distDir: "", bundle: edgeBundle, ...over });
const edgeOutdated = edgeUpd({
  panel: panelRow(newer),
  nodes: [upNode({ nodeId: "nod_de1", name: "de1", version: "v0.1.49", built: now - 9 * 86400, state: NodeUpdateState.OUTDATED, onlineUsers: 5 }), legacy, n50({ nodeId: "nod_fi1", name: "fi1", onlineUsers: 1, address: "fi1.example.com" })],
});
const edgeManual = edgeUpd({ panel: panelRow({}), nodes: [n50({ nodeId: "nod_de1", name: "de1" }), legacy, n50({ nodeId: "nod_fi1", name: "fi1", address: "fi1.example.com" })] });
const step = (nodeId: string, nodeName: string, stage: number, state: StepState, errorKey = "") => ({ nodeId, nodeName, stage, state, fromVersion: "v0.1.49", fromBuilt: now - 9 * 86400, startedUnix: now - 900, finishedUnix: state === StepState.PASSED ? now - 400 : 0, errorKey, params: {} });
const edgeRunning = edgeUpd({
  panel: panelRow(current),
  nodes: [
    n50({ nodeId: "nod_de1", name: "de1", onlineUsers: 1 }),
    upNode({ nodeId: "nod_fi1", name: "fi1", version: "v0.1.49", built: now - 9 * 86400, state: NodeUpdateState.UPDATING, address: "fi1.example.com" }),
    upNode({ nodeId: "nod_se1", name: "se1", version: "v0.1.49", built: now - 9 * 86400, state: NodeUpdateState.OUTDATED, address: "se1.example.com" }),
    legacy,
  ],
  rollout: {
    id: "rol_1", status: RolloutStatus.RUNNING, toVersion: REL, toBuilt: B50, batchSize: 1, createdUnix: now - 900, finishedUnix: 0, pauseKey: "", pauseParams: {},
    steps: [
      step("nod_de1", "de1", 0, StepState.PASSED),
      step("nod_fi1", "fi1", 1, StepState.GATING),
      step("nod_se1", "se1", 2, StepState.PENDING),
      step("nod_nl1", "nl1", 3, StepState.SKIPPED, "updates.step.err.unsupported"),
    ],
  },
});
// the same pages on a VPS panel: a dist folder, no download base, a panel that installs its own release
const onVps = <D extends { panel: { update: object } }>(d: D) => ({ ...d, distDir: "/var/lib/mistgate/dist", bundle: { ...edgeBundle, downloadBase: "" }, panel: { ...d.panel, update: { ...d.panel.update, supported: true, installable: true, errorKey: "" } } });

function AddNodeButton() {
  const addNode = useAddNode();
  return <Button onClick={() => addNode()}>Add node</Button>;
}

export function EdgeKit() {
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1040px)] -translate-x-1/2 flex-col gap-8">
      <Shot id="edge-health" title="Edge: Health, quiet (alerts every minute, one line on the tunnel checks)" seed={healthSeed}>
        <HealthScreen />
      </Shot>
      <Shot id="edge-health-down" title="Edge: Health, de1 down and alice on People" seed={downSeed}>
        <HealthScreen />
      </Shot>
      <Shot id="vps-health" title="VPS: Health, the same quiet data (unchanged)" seed={healthSeed} me={vpsMe}>
        <HealthScreen />
      </Shot>
      <Shot id="vps-health-down" title="VPS: Health, de1 down (unchanged)" seed={downSeed} me={vpsMe}>
        <HealthScreen />
      </Shot>
      <Shot
        id="edge-integrations"
        title="Edge: Integrations (the token list stays, no new token; MCP is a notice; Telegram works as on the VPS)"
        seed={[[tokensQuery.queryKey, tokens], [approvalsQuery.queryKey, approvals], [telegramQuery.queryKey, telegramOff]]}
      >
        <IntegrationsScreen />
      </Shot>
      <Shot
        id="vps-integrations"
        title="VPS: Integrations with the country check set up (no pointer to Reachability any more)"
        seed={[[tokensQuery.queryKey, tokens], [approvalsQuery.queryKey, approvals], [countryQuery.queryKey, { country: "RU" }], [telegramQuery.queryKey, telegramOff]]}
        me={vpsMe}
      >
        <IntegrationsScreen />
      </Shot>
      <Shot id="edge-updates" title="Edge: Updates (nodes and releases listed, no buttons that start anything)" seed={[[updatesQuery.queryKey, { ...available, panel: panelOffer.panel }]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="edge-upd-outdated" title="Edge, node updates wired: de1 outdated, nl1 on v0.1.47 by hand, a newer panel release on GitHub" seed={[[updatesQuery.queryKey, edgeOutdated]]} me={updMe}>
        <UpdatesScreen />
      </Shot>
      <Shot id="edge-upd-manual" title="Edge, node updates wired: only nl1 left, by hand (the panel not checked yet)" seed={[[updatesQuery.queryKey, edgeManual]]} me={updMe}>
        <UpdatesScreen />
      </Shot>
      <Shot id="edge-upd-running" title="Edge, node updates wired: a rollout running (no client-eye check in the gate)" seed={[[updatesQuery.queryKey, edgeRunning]]} me={updMe}>
        <UpdatesScreen />
      </Shot>
      <Shot id="vps-upd-outdated" title="VPS: the same nodes (unchanged)" seed={[[updatesQuery.queryKey, onVps(edgeOutdated)]]} me={vpsMe}>
        <UpdatesScreen />
      </Shot>
      <Shot id="vps-upd-manual" title="VPS: only nl1 left, by hand (unchanged)" seed={[[updatesQuery.queryKey, onVps(edgeManual)]]} me={vpsMe}>
        <UpdatesScreen />
      </Shot>
      <Shot id="vps-upd-running" title="VPS: the same rollout (unchanged)" seed={[[updatesQuery.queryKey, onVps(edgeRunning)]]} me={vpsMe}>
        <UpdatesScreen />
      </Shot>
      <Shot id="edge-add-node" title="Edge: Add node (SSH install and the manual command)">
        <AddNodeButton />
      </Shot>
      <Shot id="edge-backups" title="Edge: Settings → Backups">
        <BackupsPage />
      </Shot>
    </div>
  );
}
