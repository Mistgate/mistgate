import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useAddNode } from "@/components/add-node";
import { Button, buttonClass } from "@/components/ui/button";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { NodeCard as NodeCardMsg } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { useNow } from "@/lib/time";
import { countNodes, healthKind, problemCount } from "@/lib/fleet";
import { alertsQuery, alertTitle, isLoud, type Alert } from "@/lib/health";
import { isProblem, nodeKind, statusLine } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";

type NodeCard = Plain<NodeCardMsg>;

export type StripView = {
  kind: StatusKind;
  headline: string;
  detail: string;
  /** The node the strip's main button opens, and what for. */
  target: { id: string; name: string; action: "open" | "profiles" | "newCommand" } | null;
  /** The number of problems (the same as the header's), for the Health button. */
  problems: number;
};

const names = (nodes: readonly { name: string }[]) => nodes.map((n) => n.name).join(", ");

/**
 * What the strip says, pure (tested): one number and one word for the problems ("2 problems: de1, nl1"), the same number
 * the header shows; a fleet that only waits for its install is grey and says until when the command works; "all up"
 * counts only the nodes that really serve, the rest (waiting, blipped, updating) is a tail.
 */
export function stripView(
  t: T,
  clock: (unix: number) => string,
  { cards, usersOnline, alertCount, criticalCount, alerts, now }: {
    cards: readonly NodeCard[];
    usersOnline: number;
    alertCount: number;
    criticalCount: number;
    alerts: readonly Alert[];
    /** ms */
    now: number;
  },
): StripView {
  const counts = countNodes(cards);
  const problems = problemCount({ problems: counts.problems, alerts: alertCount });
  const bad = cards.filter(isProblem);
  const kind = healthKind(counts.broken, problems, criticalCount);

  if (kind !== "ok") {
    // the nodes named: those the alerts are about, then the broken ones the alerts do not cover yet
    const alertNodes = [...new Set(alerts.map((a) => a.nodeName).filter(Boolean))];
    const named = [...alertNodes, ...bad.map((n) => n.name).filter((n) => !alertNodes.includes(n))];
    const more = named.length > 3 ? ` ${t("hl.strip.more", { n: named.length - 3 })}` : "";
    const headline = `${t.n("ov.problems", problems)}${named.length ? `: ${named.slice(0, 3).join(", ")}${more}` : ""}`;
    const covered = new Set(alerts.map((a) => a.nodeId));
    const detail = [
      ...alerts.slice(0, 2).map((a) => `${a.nodeName ? `${a.nodeName}: ` : ""}${alertTitle(t, a)}`),
      ...bad.filter((n) => !covered.has(n.id)).map((n) => `${n.name}: ${statusLine(t, n)}`),
    ]
      .slice(0, 3)
      .join(" · ");
    // the node to open: the first alert that has one, else the first problem node; a node without profiles goes to them
    const alertNode = alerts.find((a) => a.nodeId);
    const first = bad[0];
    const target = alertNode
      ? { id: alertNode.nodeId, name: alertNode.nodeName, action: "open" as const }
      : first
        ? { id: first.id, name: first.name, action: first.reason?.code === "no_profiles" ? ("profiles" as const) : ("open" as const) }
        : null;
    return { kind, headline, detail, target, problems };
  }

  const up = cards.filter((n) => nodeKind(n) === "ok");
  const pending = cards.filter((n) => n.status === NodeStatus.PENDING);
  const blips = cards.filter((n) => nodeKind(n) === "blip");
  const updating = cards.filter((n) => nodeKind(n) === "busy");
  const online = usersOnline === 0 ? t("ov.onlineNone") : t.n("ov.onlineNow", usersOnline);

  // only waiting for the install: grey, with until when the command works and the button for a new one
  if (up.length === 0 && pending.length > 0 && blips.length === 0 && updating.length === 0) {
    if (pending.length > 1) return { kind: "off", headline: t.n("ov.tail.pending", pending.length), detail: t("ov.pending.detail"), target: null, problems };
    const p = pending[0]!;
    const until = Number(p.reason?.params.expires_unix ?? 0);
    const live = p.reason?.code === "enrollment_pending" && until * 1000 > now;
    return {
      kind: "off",
      headline: `${t("ov.tail.pendingOne", { name: p.name })} · ${live ? t("ov.pending.until", { time: clock(until) }) : t("ov.pending.expired")}`,
      detail: t(live ? "ov.pending.detail" : "ov.pending.expiredDetail"),
      target: { id: p.id, name: p.name, action: "newCommand" },
      problems,
    };
  }

  const tail = [
    pending.length === 1 ? t("ov.tail.pendingOne", { name: pending[0]!.name }) : pending.length ? t.n("ov.tail.pending", pending.length) : "",
    blips.length ? t("ov.tail.blip", { names: names(blips) }) : "",
    updating.length ? t.n("ov.tail.updating", updating.length, { names: names(updating) }) : "",
  ].filter(Boolean);
  let headline: string;
  if (up.length === 0) headline = tail.join(" · ");
  else if (up.length === 1) headline = [t("ov.oneUp", { name: up[0]!.name }), ...tail].join(" · ");
  else headline = [tail.length ? t.n("ov.someUp", up.length) : t.n("ov.allOk", up.length), ...tail].join(" · ");
  return { kind: up.length ? "ok" : blips.length ? "blip" : updating.length ? "busy" : "off", headline, detail: online, target: null, problems };
}

/**
 * The strip at the top of the Overview. Its number is the one of the header and the menu: the active alerts (not muted,
 * not info) or the problem nodes, whichever is more. While the alert debounce has not caught up with a node that is
 * already broken, the node's own status still turns the strip red, so a fault is never hidden between the two.
 * `onboarding`: the first-run list above leads the way, so the strip does not repeat its button (a new install command,
 * a profile for the node), and says nothing at all while no node has ever connected.
 */
export function HealthStrip({
  cards,
  usersOnline,
  alertCount,
  criticalCount,
  onboarding = false,
}: {
  cards: NodeCard[];
  usersOnline: number;
  alertCount: number;
  criticalCount: number;
  onboarding?: boolean;
}) {
  const t = useT();
  const fmt = useFmt();
  const addNode = useAddNode();
  const q = useQuery({ ...alertsQuery, enabled: alertCount > 0 });
  const nowMs = useNow();
  const now = q.data?.nowUnix ?? Math.floor(nowMs / 1000);
  const alerts = (q.data?.active ?? []).filter((a) => isLoud(a, now));
  const v = stripView(t, fmt.clock, { cards, usersOnline, alertCount, criticalCount, alerts, now: nowMs });
  if (onboarding && countNodes(cards).connected === 0 && alertCount === 0) return null;
  if (onboarding && (v.target?.action === "newCommand" || v.target?.action === "profiles")) v.target = null;
  const problem = v.kind === "bad" || v.kind === "warn";
  const doctorLike = alerts.some((a) => a.params.check) || cards.some((n) => n.id === v.target?.id && n.reason?.code === "doctor_fail");

  return (
    <section
      className={cx(
        "flex flex-wrap items-center gap-3.5 rounded-card-lg border bg-surface px-3.5 py-3.5 md:px-[18px] md:py-4",
        v.kind === "ok" && "border-[color-mix(in_oklch,var(--accent-mint)_35%,var(--border))]",
        v.kind === "warn" && "border-warn-line",
        v.kind === "bad" && "border-[color-mix(in_oklch,var(--danger)_35%,var(--border))]",
        (v.kind === "off" || v.kind === "blip" || v.kind === "busy") && "border-line",
      )}
    >
      <StatusPill kind={v.kind} glyphOnly />
      <div className="flex min-w-[200px] flex-1 flex-col gap-[3px]">
        <span className="text-[15px] font-bold tracking-[-0.02em] text-pretty md:text-base">{v.headline}</span>
        <span className="text-[13px] text-pretty text-muted">{v.detail}</span>
      </div>
      {(problem || v.target) && (
        <div className="flex flex-wrap gap-2">
          {problem && alertCount > 0 && (
            <Link to="/health" className={buttonClass("secondary", "md")}>
              {t("hl.strip.open")}
            </Link>
          )}
          {problem && v.target && doctorLike && (
            <Link to="/nodes/$id" params={{ id: v.target.id }} search={{ tab: "doctor" }} className={buttonClass("secondary", "md")}>
              {t("node.doctor")}
            </Link>
          )}
          {v.target?.action === "open" && (
            <Link to="/nodes/$id" params={{ id: v.target.id }} className={buttonClass("primary", "md")}>
              {t("ov.open", { name: v.target.name })}
            </Link>
          )}
          {v.target?.action === "profiles" && (
            <Link to="/nodes/$id" params={{ id: v.target.id }} search={{ tab: "profiles" }} className={buttonClass("primary", "md")}>
              {t("ov.addProfile", { name: v.target.name })}
            </Link>
          )}
          {v.target?.action === "newCommand" && (
            <Button variant="primary" size="md" onClick={() => addNode({ id: v.target!.id, name: v.target!.name })}>
              {t("node.banner.newCommand")}
            </Button>
          )}
        </div>
      )}
    </section>
  );
}
