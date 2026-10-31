import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import { bestDelivered, lossPct, reasonShort, reasonText, renewalOf, renewalText, senderLabel } from "@/lib/port-check";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";

// "Ports" in the Direct way in of a node (design/udp-port-check.md §6.6, design/vless.md §7.4): what the delivery check found
// for each UDP port and each TCP port (does a connection reach Mistgate), when and from where, which profile listens there,
// one line on whether the certificates renew, and the button that runs the check (about 8 seconds). It answers "do people
// get through to the node", so it lives with the ways in; the row names the profile, the profile row has the port's dot.

type Check = Plain<GetNodeResponse>["portChecks"][number];
type TcpCheck = Plain<GetNodeResponse>["tcpChecks"][number];

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

/** How a stored TCP verdict reads: the pill and its tone. "" (not probed, see the reason) and anything unknown read as not checked. */
export function tcpPill(t: T, verdict: string): { kind: StatusKind; label: string } {
  switch (verdict) {
    case "ours":
      return { kind: "ok", label: t("ports.tcp.ours") };
    case "other":
    case "busy":
      return { kind: "warn", label: t(`ports.tcp.${verdict}`) };
    case "refused":
    case "timeout":
      return { kind: "bad", label: t(`ports.tcp.${verdict}`) };
    default:
      return { kind: "off", label: t("ports.unchecked") };
  }
}

/** A bar that fills in the time a run takes (`ms`, about 8 s for the ports) and stops short of full until the answer comes. */
export function Progress({ label, ms = 8000 }: { label: string; ms?: number }) {
  const [started, setStarted] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setStarted(true));
    return () => cancelAnimationFrame(id);
  }, []);
  return (
    <div role="progressbar" aria-label={label} className="h-1 overflow-hidden rounded-full bg-surface-2">
      <div className="h-full rounded-full bg-accent transition-[width] ease-linear motion-reduce:transition-none" style={{ width: started ? "92%" : "4%", transitionDuration: `${ms}ms` }} />
    </div>
  );
}

/** The port check of a node: one run at a time, its answer kept for the lines under the rows. */
export function usePortCheck(nodeId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => plain(await nodesApi.checkPorts({ nodeId })),
    onSettled: () => void qc.invalidateQueries({ queryKey: ["node", nodeId] }),
  });
}
export type PortCheck = ReturnType<typeof usePortCheck>;

/** "Check ports", for those who may run it (owner, helper); off while the agent is not on the line. */
export function CheckPortsButton({ data, run, primary }: { data: Plain<GetNodeResponse>; run: PortCheck; primary?: boolean }) {
  const t = useT();
  const role = useQuery(meQuery).data?.admin?.role;
  const linked = agentLinked(data.node!.status);
  if (role !== Role.OWNER && role !== Role.HELPER) return null;
  return (
    <span title={linked ? undefined : t("ports.offline")} className="flex">
      <Button variant={primary ? "primary" : "secondary"} size="md" disabled={run.isPending || !linked} onClick={() => run.mutate()}>
        {run.isPending ? t("ports.checking") : t("ports.check")}
      </Button>
    </span>
  );
}

/**
 * The rows the port check shows: the UDP ports (those of the UDP profiles first, a profile port never checked among them
 * once anything was), then the TCP ports. The Direct lane counts them, so the list and its count never disagree.
 */
export function portRows(data: Pick<Plain<GetNodeResponse>, "node" | "inbounds" | "portChecks" | "tcpChecks">) {
  const stored = data.portChecks ?? [];
  const tcp = [...(data.tcpChecks ?? [])].sort((a, b) => a.port - b.port);
  // the UDP rows: the profiles that listen on UDP (VLESS is TCP: its ports are the TCP rows)
  const udpInbounds = data.inbounds.filter((i) => i.protocol !== "vless");
  const profilesOn = (port: number) => udpInbounds.filter((i) => i.port === port).map((i) => i.profileName);
  // the ports of the profiles come first (the stored ones, then any that were never checked), then the others
  const unchecked: Check[] = [];
  if (stored.length > 0) {
    for (const port of new Set(udpInbounds.map((i) => i.port))) {
      if (port > 0 && !stored.some((c) => c.port === port)) unchecked.push({ nodeId: data.node!.id, port, verdict: "", sent: 0, got: 0, checkedUnix: 0, badUnix: 0, sender: "", reason: "" });
    }
  }
  const udp = [...stored, ...unchecked].sort((a, b) => Number(profilesOn(b.port).length > 0) - Number(profilesOn(a.port).length > 0) || a.port - b.port);
  return { udp, tcp, profilesOn };
}

/** The rows of the port check and what its last run said. */
export function Ports({ data, run }: { data: Plain<GetNodeResponse>; run: PortCheck }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const linked = agentLinked(node.status);

  const { udp: rows, tcp, profilesOn } = portRows(data);
  const stored = data.portChecks ?? [];
  const renewal = renewalOf(tcp);

  const result = run.data;
  // a run whose TCP part could not probe anything stores nothing, so its reason would otherwise go unsaid
  const tcpRun = result && !result.errorCode ? (result.tcp ?? []) : [];
  const tcpReason = tcpRun.length > 0 && tcpRun.every((r) => !r.verdict) ? (tcpRun.find((r) => r.reason)?.reason ?? "") : "";
  const why = run.error ? errorText(run.error, t) : result?.errorCode ? reasonText(t, result.errorCode, { best: bestDelivered(result.ports) }) : tcpReason ? reasonText(t, tcpReason) : "";
  // the counts of a run that could not tell (inconclusive) or stayed on one host: what did arrive
  const counts = result?.errorCode && (result.errorCode === "inconclusive" || result.errorCode === "same_host") ? result.ports.map((p) => `${p.port}: ${p.got}/${p.sent}`).join(" · ") : "";

  return (
    <div data-testid="ports" className="flex flex-col gap-2.5">
      <p className="text-xs leading-snug text-pretty text-muted">{run.isPending ? t("ports.checkingNote") : !linked ? t("ports.offline") : stored.length + tcp.length === 0 ? t("ports.never") : t("ports.intro")}</p>
      {run.isPending && <Progress label={t("ports.checking")} />}
      {why && !run.isPending && (
        <Notice className="items-start">
          <span className="flex flex-col gap-1">
            {why}
            {counts && <span className="font-mono text-[11px] text-muted">{counts}</span>}
          </span>
        </Notice>
      )}
      {rows.length + tcp.length > 0 && (
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
                <span className="min-w-0 text-xs break-words">
                  {profiles.length > 0 ? (
                    <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "profiles" }} className="hover:text-accent-text hover:underline">
                      {profiles.join(", ")}
                    </Link>
                  ) : (
                    <span className="text-muted">{t("ports.row.noProfile")}</span>
                  )}
                </span>
              </li>
            );
          })}
          {tcp.map((c) => (
            <TcpRow key={c.port} c={c} />
          ))}
        </ul>
      )}
      {renewal && (renewal.kind === "risk" ? <Notice>{renewalText(t, renewal)}</Notice> : <RenewalLine text={renewalText(t, renewal)} />)}
    </div>
  );
}

/** One TCP port: the pill, when and from where (or why it could not be probed), and what listens there. */
function TcpRow({ c }: { c: TcpCheck }) {
  const t = useT();
  const fmt = useFmt();
  const pill = tcpPill(t, c.verdict);
  const detail = [!c.verdict && c.reason ? reasonShort(t, c.reason) : "", c.checkedUnix > 0 ? fmt.ago(c.checkedUnix) : "", c.checkedUnix > 0 && c.sender ? t("ports.row.from", { sender: senderLabel(t, c.sender) }) : ""]
    .filter(Boolean)
    .join(" · ");
  // port 80 belongs to no profile: it is the certificates'; a site is the Hysteria2 profile's
  const owner = c.role === "acme" ? t("ports.tcp.role.acme") : [c.role === "site" ? t("ports.tcp.role.site") : "", c.profile].filter(Boolean).join(" · ");
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line py-2.5 first:border-t-0">
      <span className="w-[72px] font-mono text-[13px] font-bold">tcp/{c.port}</span>
      <StatusPill kind={pill.kind} label={pill.label} sm />
      <span className="min-w-[160px] flex-1 text-xs leading-snug text-muted">{detail}</span>
      <span className="min-w-0 text-xs break-words">{owner || <span className="text-muted">{t("ports.row.noProfile")}</span>}</span>
    </li>
  );
}

/** The renewal line when all is well: quiet, with the green dot. */
function RenewalLine({ text }: { text: string }) {
  return (
    <p className="flex items-start gap-2 text-xs leading-snug text-pretty text-muted">
      <span className="mt-[5px] flex"><StatusDot kind="ok" /></span>
      {text}
    </p>
  );
}
