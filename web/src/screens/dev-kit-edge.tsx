import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { AddNodeProvider, useAddNode } from "@/components/add-node";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { editionFeatures } from "@/i18n/edition";
import { approvalsQuery, tokensQuery } from "@/lib/integrations";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { IntegrationsScreen } from "@/screens/integrations";
import { BackupsPage } from "@/screens/settings-pages/backups";
import { UpdatesScreen } from "@/screens/updates";
import { available, panelOffer } from "./dev-kit-redesign";

// Development only (the /dev-kit page): the Cloudflare ("edge") edition. Me says edition "edge" with every feature unavailable,
// and the screens that carry a feature say so in place of its buttons. Private query caches with demo data, no panel behind them.

const now = Math.floor(Date.now() / 1000);
const edgeMe = { admin: { id: "adm_1", displayName: "Owner", role: Role.OWNER }, version: "0.1.24", stepUpUntilUnix: 0n, edition: "edge", unavailable: [...editionFeatures] };

function Shot({ id, title, seed = [], children }: { id: string; title: string; seed?: [readonly unknown[], unknown][]; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, edgeMe as never);
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
const approvals = { awaiting: 0, nowUnix: now, receivedMs: Date.now(), approvals: [] };

function AddNodeButton() {
  const addNode = useAddNode();
  return <Button onClick={() => addNode()}>Add node</Button>;
}

export function EdgeKit() {
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1040px)] -translate-x-1/2 flex-col gap-8">
      <Shot
        id="edge-integrations"
        title="Edge: Integrations (the token list stays, no new token; Telegram and MCP are a notice)"
        seed={[[tokensQuery.queryKey, tokens], [approvalsQuery.queryKey, approvals]]}
      >
        <IntegrationsScreen />
      </Shot>
      <Shot id="edge-updates" title="Edge: Updates (nodes and releases listed, no buttons that start anything)" seed={[[updatesQuery.queryKey, { ...available, panel: panelOffer.panel }]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="edge-add-node" title="Edge: Add node (only the manual command)">
        <AddNodeButton />
      </Shot>
      <Shot id="edge-backups" title="Edge: Settings → Backups">
        <BackupsPage />
      </Shot>
    </div>
  );
}
