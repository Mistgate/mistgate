import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { TextField } from "@/components/ui/text-field";
import type { Inbound, StatusReason } from "@/gen/mistgate/admin/v1/common_pb";
import { useT, type T } from "@/i18n";
import { profiles as profilesApi } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { errorCode, errorVars } from "@/lib/errors";
import { plain, type Plain } from "@/lib/plain";
import { DecoyNotice, useSettled, type InboundCheck } from "./inbound-check";

/**
 * The site a node shows strangers at a REALITY server instead of its profile's target (Inbound.reality_target_override): the check before
 * the click (UpdateInbound with validate_only), shared by the edit window's field and by "Apply…" on a neighbour of the "Sites near the
 * node" card. The panel judges the target (a host name with a port, not the node itself, an agent that takes it); this file only asks and
 * words the answer. Its own check and not inbound-check.tsx's: the refusals here are not a port's or a domain's.
 */

/** The refusals that stop the button; the real call would refuse the same way. */
export const targetRefusals = ["reality_target_invalid", "reality_target_same_node", "reality_target_unsupported", "agent_too_old"] as const;
export type TargetRefusal = (typeof targetRefusals)[number];

export type TargetCheck =
  | { state: "idle" }
  | { state: "ok"; inbound: Plain<Inbound> | undefined; warnings: Plain<StatusReason>[] }
  | { state: "refused"; code: TargetRefusal; vars: Record<string, string> };

/** The hint under "Domain (SNI)": what it is, and for Hysteria2 and TrustTunnel why it is the owner's own domain and not a stranger's name. */
export function tlsHint(t: T, protocol: string | undefined): string {
  return protocol === "hysteria2" || protocol === "trusttunnel" ? `${t("node.profiles.tlsHint")} ${t(`sni.why.${protocol}`)}` : t("node.profiles.tlsHint");
}

/** The host of "host:port" (the name a stranger is asked for); "self" and "" are not hosts. */
export const hostOf = (target: string) => (target === "" || target === "self" ? "" : target.replace(/:\d+$/, ""));

/** What the check says about setting `target` ("" = the profile's own) on the inbound; null = nothing to check. */
export function useTargetCheck(req: { inboundId: string; target: string } | null): TargetCheck {
  const settled = useSettled(req);
  const q = useQuery({
    // under "inbound-check" so that the refresh after a change drops it with the others (useInboundRefresh)
    queryKey: ["inbound-check", "reality-target", settled],
    queryFn: async ({ signal }) => {
      const of = `${settled!.inboundId}/${settled!.target}`;
      try {
        const r = plain(await profilesApi.updateInbound({ inboundId: settled!.inboundId, realityTargetOverride: settled!.target, validateOnly: true }, { signal }));
        return { of, state: "ok", inbound: r.inbound, warnings: r.warnings ?? [] } as const;
      } catch (e) {
        const c = ConnectError.from(e);
        const code = errorCode(c.rawMessage) as TargetRefusal;
        if (targetRefusals.includes(code)) return { of, state: "refused", code, vars: errorVars(c.rawMessage) } as const;
        throw e; // the network, a server error: not a verdict, the real call reports it
      }
    },
    enabled: settled !== null,
    retry: false,
    staleTime: 5_000,
    placeholderData: keepPreviousData,
  });
  if (req === null || !q.data || q.data.of !== `${req.inboundId}/${req.target}`) return { state: "idle" };
  return q.data;
}

/** The answer in the shape the shared notices (the Hysteria2 site that yields) read. */
export const asInboundCheck = (c: TargetCheck): InboundCheck => (c.state === "ok" ? { state: "ok", inbound: c.inbound, warnings: c.warnings, freePort: 0 } : { state: "idle" });

/** The sentence of a refusal, in the interface language. */
export function useRefusalText() {
  const t = useT();
  return (c: TargetCheck) => (c.state === "refused" ? t(`err.${c.code}`, c.vars) : undefined);
}

/**
 * The target field of the edit window of a VLESS REALITY server: shown where the profile shows another site, or the node already has a
 * site of its own (a server that shows the node's own site gets its site from "Sites near the node" and the card's Apply). `save` is what
 * to send (undefined = leave as it is), `blocked` that the panel refused what is typed. `leave` closes the window before the card is shown.
 */
export function useRealityTargetEdit(inbound: Plain<Inbound>, leave: () => void) {
  const t = useT();
  const edition = useEdition();
  const refusalText = useRefusalText();
  const own = inbound.realityTargetOverride ?? "";
  const shown = inbound.realityTarget ?? "";
  const active = inbound.protocol === "vless" && shown !== "" && (own !== "" || shown !== "self");
  const [value, setValue] = useState(own);
  const touched = active && value.trim() !== own;
  const check = useTargetCheck(touched ? { inboundId: inbound.id, target: value.trim() } : null);
  const withCard = !edition.off("sni_scan");
  const placeholder = own ? "" : shown === "self" ? t("sni.target.own") : t("sni.target.fromProfile", { target: shown });
  const field: ReactNode = active ? (
    <>
      <TextField
        label={t("sni.target.label")}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={placeholder}
        hint={t("sni.target.hint")}
        error={refusalText(check)}
        mono
        autoCapitalize="off"
        spellCheck={false}
        data-testid="reality-target-field"
      />
      {(value.trim() !== "" && own !== "") || withCard ? (
        <div className="-mt-1.5 flex flex-wrap gap-2">
          {own !== "" && value.trim() !== "" && (
            <Button type="button" variant="secondary" size="sm" onClick={() => setValue("")}>
              {t("sni.target.reset")}
            </Button>
          )}
          {withCard && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                leave();
                // the card stands under the servers: once the window is gone, bring it into view
                setTimeout(() => document.getElementById("sni-scan")?.scrollIntoView({ behavior: "smooth", block: "start" }), 120);
              }}
            >
              {t("sni.target.find")}
            </Button>
          )}
        </div>
      ) : null}
      <DecoyNotice check={asInboundCheck(check)} />
    </>
  ) : null;
  return { field, active, own: own !== "", touched, blocked: check.state === "refused", save: touched ? value.trim() : undefined };
}
