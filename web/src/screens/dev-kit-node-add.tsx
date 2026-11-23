import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { profiles as profilesApi } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { meQuery } from "@/lib/session";
import { applyScene } from "./dev-kit-scene";
import { AddInbound } from "./node/add-inbound";
import { profileListQuery } from "./users/rpc";

// Development only. /dev-kit?only=node-add-page[&state=<state>][&theme=light][&lang=ru]: the window "Put a profile on de1" of the node page
// (the board's PnNode, dlg=add) on made-up data, alone, at the width of the frame that opens it (an iframe or the screenshot's own window).
//   state=list      (default) five profiles the node does not have: two that work, one on a free port, one with a name held on a shared
//                   port, one with an old agent; the profile Main · 443 is on the node already
//   state=check     the panel is still asking about each profile
//   state=few       two profiles, both fine
// The names are made up: the node de1, documentation addresses.

const profile = (id: string, name: string, protocol: string, summary: string) => ({ id, name, protocol, summary, nodeCount: 1, userCount: 3, version: 1, warnings: [] });
const profiles = [
  profile("prf_main", "Main · 443", "hysteria2", "UDP 443 · Salamander"),
  profile("prf_warp", "Primary · WARP", "hysteria2", "UDP 8443 · exit via WARP"),
  profile("prf_gecko", "Mihomo · Gecko", "hysteria2", "UDP 2053"),
  profile("prf_xhttp", "VLESS · XHTTP", "vless", "TCP 443 · XHTTP"),
  profile("prf_cdn", "VLESS · CDN", "vless", "TCP 443 · CDN"),
  profile("prf_awg2", "AWG 2.0 (new)", "awg", "UDP 53 · AWG 2.0"),
];
const NEVER = new Promise<never>(() => {});
const refuse = (msg: string, code = Code.FailedPrecondition) => new ConnectError(msg, code);

/** The panel's answers to the check before the click (CreateInbound with validate_only), for the state a scene asks for. */
function fakeChecks(state: string) {
  (profilesApi as unknown as Record<string, unknown>).createInbound = async (req: { profileId: string; validateOnly?: boolean }) => {
    if (state === "check") return NEVER;
    const ports: Record<string, number> = { prf_warp: 8443, prf_gecko: 2053 };
    if (req.profileId === "prf_xhttp") throw refuse("port_taken: port=443&profile=VLESS · Reality&free=8443");
    if (req.profileId === "prf_cdn") throw refuse("sni_taken: port=443&node=de1&profile=VLESS · Reality&name=de1.example.com&free=2083&share=1", Code.AlreadyExists);
    if (req.profileId === "prf_awg2") throw refuse("agent_too_old: the node's agent is older than this profile needs");
    return { inbound: { port: ports[req.profileId] ?? 443, tlsServerName: "" }, warnings: [], freePort: 0 };
  };
}

export function NodeAddPage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "list";
  const [scene] = useState(() => {
    applyScene(params);
    setEffectsEnabled(false, false);
    fakeChecks(state);
    const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: Role.OWNER }, version: "0.1.71", stepUpUntilUnix: 0n } as never);
    qc.setQueryData(profileListQuery.queryKey, (state === "few" ? profiles.slice(0, 3) : profiles) as never);
    return qc;
  });
  const node = { id: "nod_de1", name: "de1", address: "de1.example.com", countryCode: "DE", status: NodeStatus.ONLINE, protocols: ["hysteria2"], awgBackend: "auto", online: [], sparkBytes: [] };
  return (
    <QueryClientProvider client={scene}>
      <ToastProvider>
        <AddInbound open onOpenChange={() => {}} data={{ node, inbounds: [{ profileId: "prf_main" }] } as never} />
      </ToastProvider>
    </QueryClientProvider>
  );
}
