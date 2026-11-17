import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { editionFeatures } from "@/i18n/edition";
import { decoySites, nodes as nodesApi } from "@/lib/api";
import { sitesQuery } from "@/lib/decoysites";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { DecoyCard } from "@/screens/node/decoy";
import { NodesScreen } from "@/screens/nodes";
import { installLibrary } from "./dev-kit-decoys";

// Development only, part of /dev-kit: the decoy site of a node (Node -> Server -> "Decoy site", the library's choice and the bar of
// its delivery) in the states the panel can report, the bulk change on Nodes, and the same block in the Cloudflare edition, on made-up
// data in private query caches. The library is the made-up one of the Decoy sites scenes; the calls an admin makes are answered
// here: a refused archive and an accepted bulk change.

const now = Math.floor(Date.now() / 1000);
const sha = (c: string) => c.repeat(64).slice(0, 64);

const owner = { admin: { id: "adm_1", role: Role.OWNER, displayName: "Owner" }, version: "dev" };
const edge = { admin: { id: "adm_1", role: Role.OWNER, displayName: "Owner" }, version: "dev", edition: "edge", unavailable: [...editionFeatures] };

type Over = Record<string, unknown>;
/** A node as ListNodes / GetNode say it; the decoy fields are the point, the rest is a plain healthy node. */
const node = (name: string, over: Over = {}) => ({
  id: `nod_${name}`,
  name,
  countryCode: "DE",
  location: "",
  provider: "examplehost",
  address: `${name}.example.com`,
  status: NodeStatus.ONLINE,
  reason: undefined,
  protocols: ["hysteria2"],
  online: [{ protocol: "hysteria2", users: 3 }],
  trafficTodayBytes: 3.1e9,
  hasMetrics: true,
  cpuPct: 9,
  ramPct: 38,
  uptimeS: 86400 * 5,
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

const applied = node("de1", { decoyMode: "own", decoySiteId: "dsi_bk", decoyBundleSha256: sha("a3"), decoyAgentMode: "own", decoyAgentSha256: sha("a3") });
const pending = node("fi1", { countryCode: "FI", decoyMode: "panel", decoyReasonCode: "pending", decoyAgentMode: "builtin", decoyAgentSha256: "" });
const tooOld = node("se1", { countryCode: "SE", agentVersion: "0.2.4", decoyMode: "panel", decoySupported: false, decoyReasonCode: "agent_too_old", decoyAgentMode: "builtin" });
const noPanel = node("nl1", { countryCode: "NL", decoyMode: "panel", decoyPanelAvailable: false, decoyReasonCode: "panel_no_index", decoyAgentMode: "builtin" });
const failed = node("pl1", { countryCode: "PL", decoyMode: "own", decoySiteId: "dsi_ph", decoyBundleSha256: sha("7d"), decoyReasonCode: "apply_failed", decoyAgentMode: "panel", decoyAgentSha256: sha("c9") });
const plain = node("pl1", { countryCode: "PL", decoyPanelAvailable: false });
const edgeNode = node("de2", { decoyOwnAvailable: false, decoyPanelAvailable: true });

/** The fleet for the bulk change: one on each option, one offline, one that cannot take own (no bundle). */
const fleet = [
  applied,
  pending,
  tooOld,
  node("fr1", { countryCode: "FR", decoyMode: "own", decoySiteId: "dsi_wd", decoyBundleSha256: sha("5e"), decoyAgentMode: "own", decoyAgentSha256: sha("5e") }),
  node("nl2", { countryCode: "NL", status: NodeStatus.DOWN, online: [], decoyMode: "builtin", decoyReasonCode: "" }),
  node("cz1", { countryCode: "CZ" }),
];

function Shot({ id, title, me = owner, seed = [], children }: { id: string; title: string; me?: object; seed?: [readonly unknown[], unknown][]; children: ReactNode }) {
  const library = useContext(LibraryContext);
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, me as never);
    c.setQueryData(updatesQuery.queryKey, { nodes: [] } as never);
    c.setQueryData(sitesQuery.queryKey, library as never);
    for (const [key, data] of seed) c.setQueryData(key, data as never);
    return c;
  });
  return (
    <section data-shot={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>{children}</ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

const card = (n: object) => <DecoyCard node={n as never} />;

const LibraryContext = createContext<unknown>(undefined);

/** Dev only: the two answers the kit gives, so a dropped archive is refused and a bulk change goes through. */
function fakeCalls() {
  (decoySites as unknown as Record<string, unknown>).createDecoySite = async () => {
    throw new ConnectError("decoy_bundle_bad_path: path=assets%2F..%2F..%2Fetc%2Fcron.d%2Fjob", Code.InvalidArgument);
  };
  (nodesApi as unknown as Record<string, unknown>).setNodesDecoyMode = async () => ({ nodes: [] });
}

export function DecoyKit() {
  const [library] = useState(() => installLibrary());
  useEffect(fakeCalls, []);
  return (
    <LibraryContext.Provider value={library}>
      <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1040px)] -translate-x-1/2 flex-col gap-8">
        <div className="grid gap-8 md:grid-cols-2">
          <Shot id="decoy-applied" title="Decoy site: applied (own site, the node reports the same hash)">
            {card(applied)}
          </Shot>
          <Shot id="decoy-pending" title="Decoy site: panel's site, being applied">
            {card(pending)}
          </Shot>
          <Shot id="decoy-too-old" title="Decoy site: agent too old for decoy/1">
            {card(tooOld)}
          </Shot>
          <Shot id="decoy-no-panel" title="Decoy site: the panel's --decoy-dir has no index.html">
            {card(noPanel)}
          </Shot>
          <Shot id="decoy-failed" title="Decoy site: the last send failed">
            {card(failed)}
          </Shot>
          <Shot id="decoy-plain" title="Decoy site: built-in, the panel has no decoy directory">
            {card(plain)}
          </Shot>
          <Shot id="decoy-edge" title="Decoy site: Cloudflare edition (own site is off)" me={edge}>
            {card(edgeNode)}
          </Shot>
        </div>
        <Shot id="decoy-nodes" title="Nodes: tick several, then Set decoy site" seed={[[nodesQuery.queryKey, { nodes: fleet, expectedAgentVersion: "0.3.1" }]]}>
          <NodesScreen />
        </Shot>
      </div>
    </LibraryContext.Provider>
  );
}
