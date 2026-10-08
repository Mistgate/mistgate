import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { Notice } from "@/components/ui/notice";
import type { Inbound, StatusReason } from "@/gen/mistgate/admin/v1/common_pb";
import { useT, type T } from "@/i18n";
import { profiles as profilesApi } from "@/lib/api";
import { errorCode, errorVars } from "@/lib/errors";
import { plain, type Plain } from "@/lib/plain";

// The check before the click (CreateInbound / UpdateInbound with validate_only): asked again 300 ms after the last change
// of the profile, the port or the domain, never written. The answer is either a coded refusal the dialog words next to the
// field it is about, or the inbound as it would be (its real port and domain, for the placeholders), warnings and a port
// that is free on the node.

/** The refusals that stop the button: the real call would refuse the same way (port_lossy: from the stored UDP checks, never a run). */
export const refusals = ["acme_needs_domain", "port_taken", "hop_taken", "port_in_hop", "sni_needs_domain", "sni_invalid", "already_on_node", "node_retired", "port_lossy"] as const;
export type Refusal = (typeof refusals)[number];

export type InboundCheck =
  | { state: "idle" }
  | { state: "ok"; inbound: Plain<Inbound> | undefined; warnings: Plain<StatusReason>[]; freePort: number }
  | { state: "refused"; code: Refusal; vars: Record<string, string> };

export type CheckRequest =
  | { kind: "create"; nodeId: string; profileId: string; port: string; sni: string }
  | { kind: "update"; inboundId: string; port?: string; sni?: string; enabled?: boolean };

const asPort = (p: string) => (p ? Number(p) : 0);
const subject = (r: CheckRequest) => (r.kind === "create" ? `${r.nodeId}/${r.profileId}` : r.inboundId);

async function ask(req: CheckRequest, signal: AbortSignal) {
  if (req.kind === "create") {
    const r = await profilesApi.createInbound(
      { profileId: req.profileId, nodeId: req.nodeId, portOverride: asPort(req.port), tlsServerNameOverride: req.sni, validateOnly: true },
      { signal },
    );
    return plain(r);
  }
  const r = await profilesApi.updateInbound(
    {
      inboundId: req.inboundId,
      portOverride: req.port === undefined ? undefined : asPort(req.port),
      tlsServerNameOverride: req.sni,
      enabled: req.enabled,
      validateOnly: true,
    },
    { signal },
  );
  return plain(r);
}

/** value, once it has stayed the same for `ms` (the first one at once). Values are compared by their JSON. */
export function useSettled<V>(value: V, ms = 300): V {
  const key = JSON.stringify(value);
  const [settled, setSettled] = useState({ key, value });
  useEffect(() => {
    if (key === settled.key) return;
    const id = setTimeout(() => setSettled({ key, value }), ms);
    return () => clearTimeout(id);
  }, [key, value, ms, settled.key]);
  return settled.value;
}

/** What the check says about `req` (null = nothing to check: a closed dialog, a port that is not a number yet). */
export function useInboundCheck(req: CheckRequest | null): InboundCheck {
  const settled = useSettled(req);
  const q = useQuery({
    queryKey: ["inbound-check", settled],
    queryFn: async ({ signal }) => {
      const of = subject(settled!);
      try {
        const r = await ask(settled!, signal);
        return { of, state: "ok", inbound: r.inbound, warnings: r.warnings ?? [], freePort: r.freePort ?? 0 } as const;
      } catch (e) {
        const c = ConnectError.from(e);
        const code = errorCode(c.rawMessage) as Refusal;
        if (refusals.includes(code)) return { of, state: "refused", code, vars: errorVars(c.rawMessage) } as const;
        throw e; // the network, a server error: not a verdict, the real call reports it
      }
    },
    enabled: settled !== null,
    retry: false,
    staleTime: 5_000,
    placeholderData: keepPreviousData,
  });
  // the answer about another profile (the choice changed, its own check is on the way) says nothing about this one
  if (req === null || !q.data || q.data.of !== subject(req)) return { state: "idle" };
  return q.data;
}

/** Refresh what an inbound change touched: the node itself and the lists that count its profiles. */
export function useInboundRefresh(nodeId: string) {
  const qc = useQueryClient();
  return () => {
    for (const key of [["node", nodeId], ["nodes"], ["profiles"], ["node-events", nodeId], ["inbound-check"], ["health", "checks"]]) {
      void qc.invalidateQueries({ queryKey: key });
    }
  };
}

export const portOk = (v: string) => v === "" || (/^\d{1,5}$/.test(v) && Number(v) >= 1 && Number(v) <= 65535);

/** The placeholder of the domain field: the name the node will really use ("de2.example.com", "from the profile: vpn.example.com"). */
export function sniPlaceholder(t: T, effective: string | undefined, nodeAddress: string): string {
  if (!effective) return t("node.profiles.fromProfileOrNode");
  return effective === nodeAddress ? effective : t("node.profiles.tlsFromProfile", { name: effective });
}

/** "warp_missing" of the check: the WARP exit of the profile has nothing to go through on this node. Says so, does not stop. */
export function WarpWarnings({ check, nodeId, nodeName, onLeave }: { check: InboundCheck; nodeId: string; nodeName?: string; onLeave: () => void }) {
  const t = useT();
  if (check.state !== "ok") return null;
  const w = check.warnings.find((x) => x.code === "warp_missing");
  if (!w) return null;
  const node = nodeName ?? check.inbound?.nodeName ?? "";
  const state = w.params.state;
  return (
    <Notice className="items-start">
      <span className="flex flex-col items-start gap-1.5">
        {t(state === "paused" ? "node.check.warp.paused" : state === "down" ? "node.check.warp.down" : "node.check.warp.none", { node })}
        <Link to="/nodes/$id" params={{ id: nodeId }} search={{ tab: "settings" }} hash="warp" onClick={onLeave} className="font-bold underline decoration-dotted underline-offset-2">
          {t(state === "none" || !state ? "node.check.warp.open" : "node.check.warp.goto", { node })}
        </Link>
      </span>
    </Notice>
  );
}
