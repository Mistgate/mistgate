import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import { bestDelivered, lossPct, reasonShort, reasonText, senderLabel } from "@/lib/port-check";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";

// "UDP ports" under the profiles of a node (design/udp-port-check.md §6.6): what the delivery check found for each port, when
// and from where, which profile listens there, and the button that runs it (about 8 seconds). It sits with the profiles
// because a port is a profile's: the row names the profile, the profile card names the port.

type Check = Plain<GetNodeResponse>["portChecks"][number];

/** How a stored verdict reads: the pill and its tone. "" (not checked) and anything unknown read as not checked. */
export function verdictPill(t: T, c: Pick<Check, "verdict" | "sent" | "got">): { kind: StatusKind; label: string } {
  const lost = lossPct(c.sent, c.got);
  switch (c.verdict) {
    case "ok":
      return { kind: "ok", label: t("ports.ok") };
    case "lossy":
      return { kind: "warn", label: t("ports.lossy", { lost }) };
    case "broken":
      return { kind: "bad", label: t("ports.broken", { lost }) };
    default:
      return { kind: "off", label: t("ports.unchecked") };
  }
}

/** A bar that fills in the time a run takes (about 8 s) and stops short of full until the answer comes. */
function Progress({ label }: { label: string }) {
  const [started, setStarted] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setStarted(true));
    return () => cancelAnimationFrame(id);
  }, []);
  return (
    <div role="progressbar" aria-label={label} className="h-1 overflow-hidden rounded-full bg-surface-2">
      <div className="h-full rounded-full bg-accent transition-[width] duration-[8000ms] ease-linear motion-reduce:transition-none" style={{ width: started ? "92%" : "4%" }} />
    </div>
  );
}

export function UdpPorts({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const node = data.node!;
  const role = useQuery(meQuery).data?.admin?.role;
  const may = role === Role.OWNER || role === Role.HELPER;
  const linked = agentLinked(node.status);
  const run = useMutation({
    mutationFn: async () => plain(await nodesApi.checkPorts({ nodeId: node.id })),
    onSettled: () => void qc.invalidateQueries({ queryKey: ["node", node.id] }),
  });

  const stored = data.portChecks ?? [];
  const profilesOn = (port: number) => data.inbounds.filter((i) => i.port === port).map((i) => i.profileName);
  // the ports of the profiles come first (the stored ones, then any that were never checked), then the others
  const unchecked: Check[] = [];
  if (stored.length > 0) {
    for (const port of new Set(data.inbounds.map((i) => i.port))) {
      if (port > 0 && !stored.some((c) => c.port === port)) unchecked.push({ nodeId: node.id, port, verdict: "", sent: 0, got: 0, checkedUnix: 0, badUnix: 0, sender: "", reason: "" });
    }
  }
  const rows = [...stored, ...unchecked].sort((a, b) => Number(profilesOn(b.port).length > 0) - Number(profilesOn(a.port).length > 0) || a.port - b.port);

  const result = run.data;
  const why = run.error ? errorText(run.error, t) : result?.errorCode ? reasonText(t, result.errorCode, { best: bestDelivered(result.ports) }) : "";
  // the counts of a run that could not tell (inconclusive) or stayed on one host: what did arrive
  const counts = result?.errorCode && (result.errorCode === "inconclusive" || result.errorCode === "same_host") ? result.ports.map((p) => `${p.port}: ${p.got}/${p.sent}`).join(" · ") : "";

  return (
    <section aria-labelledby="udp-ports-title" className="mt-2 flex flex-col gap-2.5 rounded-card border border-line bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 id="udp-ports-title" className="min-w-0 flex-1 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
          {t("ports.title")}
        </h2>
        {may && (
          <span title={linked ? undefined : t("ports.offline")} className="flex">
            <Button variant="secondary" size="sm" disabled={run.isPending || !linked} onClick={() => run.mutate()}>
              {run.isPending ? t("ports.checking") : t("ports.check")}
            </Button>
          </span>
        )}
      </div>
      <p className="text-xs leading-snug text-pretty text-muted">{run.isPending ? t("ports.checkingNote") : !linked ? t("ports.offline") : stored.length === 0 ? t("ports.never") : t("ports.intro")}</p>
      {run.isPending && <Progress label={t("ports.checking")} />}
      {why && !run.isPending && (
        <Notice className="items-start">
          <span className="flex flex-col gap-1">
            {why}
            {counts && <span className="font-mono text-[11px] text-muted">{counts}</span>}
          </span>
        </Notice>
      )}
      {rows.length > 0 && (
        <ul className="flex flex-col">
          {rows.map((c) => {
            const pill = verdictPill(t, c);
            const profiles = profilesOn(c.port);
            const checked = c.checkedUnix > 0;
            const detail = [
              c.sent > 0 ? t("ports.row.arrived", { got: c.got, sent: c.sent }) : "",
              checked ? fmt.ago(c.checkedUnix) : "",
              checked && c.sender ? t("ports.row.from", { sender: senderLabel(t, c.sender) }) : "",
              !checked && c.reason ? reasonShort(t, c.reason) : "",
            ]
              .filter(Boolean)
              .join(" · ");
            return (
              <li key={c.port} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line py-2.5 first:border-t-0">
                <span className="w-[72px] font-mono text-[13px] font-bold">udp/{c.port}</span>
                <StatusPill kind={pill.kind} label={pill.label} sm />
                <span className="min-w-[160px] flex-1 text-xs leading-snug text-muted">
                  {detail}
                  {/* a port that lost packets once and is clean now is still avoided for 30 days: say when */}
                  {c.verdict === "ok" && c.badUnix > 0 && <span className="block text-warn-text">{t("ports.row.cleanNow", { when: fmt.ago(c.badUnix) })}</span>}
                </span>
                <span className="min-w-0 text-xs break-words">{profiles.length > 0 ? profiles.join(", ") : <span className="text-muted">{t("ports.row.noProfile")}</span>}</span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
