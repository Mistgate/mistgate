import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Chip } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { StatusDot, StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { health } from "@/lib/api";
import { itemDetail, itemParams } from "@/lib/doctor-detail";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import {
  checkTitle,
  doctorKind,
  doctorQuery,
  doctorWord,
  isAccepted,
  itemCommand,
  itemFix,
  itemTitle,
  itemWhy,
  manualSteps,
  useCan,
  type DoctorItem,
  type ManualStep,
} from "@/lib/health";
import { plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { FixControl, type FixFlow } from "./fix";

/**
 * What a check looks at, by family: the host's disk, memory and clock sand, the network sky, certificates lavender,
 * the tunnel engines sage. An unknown check (a newer agent) gets no chip. The result's colour stays the status dot's.
 */
const checkMarks: Record<string, { icon: IconName; tone: Tone }> = {
  disk_space: { icon: "disk", tone: "sand" },
  journald_size: { icon: "disk", tone: "sand" },
  dstate_tasks: { icon: "cpu", tone: "sand" },
  memory_pressure: { icon: "memory", tone: "sand" },
  cpu_softirq: { icon: "cpu", tone: "sand" },
  kernel_headers: { icon: "cpu", tone: "sand" },
  time_sync: { icon: "clock", tone: "sand" },
  resolver: { icon: "dns", tone: "sky" },
  ipv6: { icon: "globe", tone: "sky" },
  foreign_vpn: { icon: "shieldOff", tone: "sky" },
  foreign_nft: { icon: "shieldOff", tone: "sky" },
  port_conflicts: { icon: "plug", tone: "sky" },
  net_baseline: { icon: "network", tone: "sky" },
  warp_path: { icon: "bolt", tone: "sky" },
  cert_expiry: { icon: "lock", tone: "lavender" },
  awg_backend: { icon: "shield", tone: "sage" },
};

function CheckChip({ id }: { id: string }) {
  const m = checkMarks[id];
  return m ? <IconChip icon={m.icon} tone={m.tone} size={20} /> : <span className="size-5 flex-none" />;
}

const linkClass = "font-mono text-xs font-bold underline decoration-dotted underline-offset-2 hover:text-accent-text";

/** A node's name that opens its Doctor tab (the fleet tab names the node of every row). */
export function NodeDoctorLink({ id, name }: { id: string; name: string }) {
  return (
    <Link to="/nodes/$id" params={{ id }} search={{ tab: "doctor" }} className={linkClass}>
      {name}
    </Link>
  );
}

/** "Run again": ask the node(s) for a fresh report now and put the answer into the doctor query. */
export function useRunDoctor(nodeId = "") {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => plain(await health.runDoctor({ nodeId })),
    onSuccess: (res) => {
      qc.setQueryData(doctorQuery(nodeId).queryKey, res);
      for (const k of ["alerts", "doctor"]) void qc.invalidateQueries({ queryKey: ["health", k] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(`${t("hl.doctor.rerunFailed")}: ${errorText(e, t)}`),
  });
}

/**
 * "This is normal for this node" and back. The panel keeps the acceptance for the warning's exact fact and drops it by
 * itself when the fact changes or the check fails; the toast offers to take it back at once.
 */
export function useAcceptDoctor() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const call = useMutation({
    mutationFn: async (v: { nodeId: string; checkId: string; accept: boolean }) => {
      const req = { nodeId: v.nodeId, checkId: v.checkId };
      return plain(await (v.accept ? health.acceptDoctorItem(req) : health.unacceptDoctorItem(req)));
    },
    onSettled: () => {
      for (const k of ["alerts", "doctor"]) void qc.invalidateQueries({ queryKey: ["health", k] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const run = (v: { nodeId: string; nodeName: string; checkId: string }, accept: boolean) =>
    call.mutate(
      { nodeId: v.nodeId, checkId: v.checkId, accept },
      {
        onSuccess: () => {
          const words = { node: v.nodeName, check: checkTitle(t, v.checkId) };
          if (accept) toast(t("hl.doctor.acceptedToast", words), { undo: () => run(v, false) });
          else toast(t("hl.doctor.unacceptedToast", words));
        },
      },
    );
  return { run, busy: call.isPending };
}

/**
 * One doctor item: a status dot, the title (with the node's name on the fleet tab) and its status word, the
 * plain-language reason of a problem, the agent's own fact line, and on the right the fix button or what stands in its
 * place: the copyable command, the WARP card, the steps by hand ("Manual ▾"). A warning can be accepted as normal for
 * the node; an accepted one says by whom and when, with the way back. A row that is fine or skipped is one quiet line.
 */
export function DoctorRow({
  flow,
  nodeId,
  nodeName,
  item,
  showNode,
  first,
  stale,
}: {
  flow: FixFlow;
  nodeId: string;
  nodeName: string;
  item: DoctorItem;
  showNode?: boolean;
  first?: boolean;
  /** The node has not sent a newer report than this (its age as text): the row is greyed out. */
  stale?: string;
}) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const accept = useAcceptDoctor();
  const me = useQuery(meQuery).data?.admin?.id ?? "";
  const [open, setOpen] = useState(false);
  const kind = doctorKind(item.status);
  const accepted = isAccepted(item);
  const issue = (item.status === DoctorStatus.FAIL || item.status === DoctorStatus.WARN) && !accepted;
  const params = itemParams(t, fmt, item);
  const why = issue ? itemWhy(t, item, params) : "";
  const fact = itemDetail(t, item, params);
  const command = itemCommand(item);
  const fix = issue ? itemFix(item) : null;
  const steps = issue && !fix && !command ? manualSteps(item) : [];
  const inbound = item.params.inbound_id ?? "";
  const target = fix && { ...fix, nodeId, nodeName, item, key: `${nodeId}/${item.id}/${inbound}`, profile: item.params.profile };
  const phase = target ? flow.phase(target.key) : "idle";
  // a certificate is never "normal" when it runs out; a stale report is not the node's current word
  const canAccept = issue && item.status === DoctorStatus.WARN && item.id !== "cert_expiry" && can.run && !stale;
  const who = { nodeId, nodeName, checkId: item.id };

  let action = null;
  if (target) action = <FixControl flow={flow} target={target} variant={item.status === DoctorStatus.FAIL ? "primary" : "secondary"} />;
  else if (command)
    action = (
      <div className="flex max-w-full items-center gap-2">
        <code className="min-w-0 rounded-field border border-line bg-canvas px-2 py-1 font-mono text-xs break-all text-fg select-all">{command}</code>
        <CopyButton value={command} />
      </div>
    );
  else if (item.id === "warp_path" && issue) action = <WarpPathActions nodeId={nodeId} />;
  else if (steps.length > 0)
    action = (
      <Button variant="secondary" size="md" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        {t("hl.doctor.manual")}
        <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
      </Button>
    );
  else if (issue) action = <Chip>{t("hl.doctor.manual")}</Chip>;

  return (
    <div
      title={stale ? t("hl.doctor.stale", { age: stale }) : undefined}
      className={cx("flex flex-wrap items-center gap-x-3 gap-y-2 py-2.5", !first && "border-t border-line", issue && "min-h-16", stale && "opacity-60")}
    >
      <div className="flex min-w-0 flex-[1_1_260px] items-start gap-2.5">
        <span className="mt-[5px] flex">
          <StatusDot kind={phase === "done" ? "ok" : accepted ? "off" : kind} />
        </span>
        <CheckChip id={item.id} />
        <div className="flex min-w-0 flex-col gap-[3px]">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px]">
            {showNode && <NodeDoctorLink id={nodeId} name={nodeName} />}
            <b>{itemTitle(t, item)}</b>
            {issue && phase === "idle" && <StatusPill kind={kind} label={t(doctorWord(item.status))} sm />}
            {!issue && !accepted && item.status !== DoctorStatus.OK && <span className="text-xs text-muted">{t("hl.doctor.status.skip")}</span>}
          </span>
          {why && <span className="text-xs leading-[1.45] text-pretty text-muted">{why}</span>}
          {fact ? (
            <span className="text-xs leading-snug break-words text-muted">{fact}</span>
          ) : (
            item.detail && <span className="font-mono text-[11px] leading-snug break-words text-muted">{item.detail}</span>
          )}
          {command && <span className="text-xs leading-snug text-muted">{t("hl.doctor.runCommand")}</span>}
        </div>
      </div>
      {accepted ? (
        <span className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted">
          {item.acceptedBy === me
            ? t("hl.doctor.acceptedByYou", { date: fmt.date(item.acceptedUnix) })
            : t("hl.doctor.acceptedBy", { name: item.acceptedByName || item.acceptedBy, date: fmt.date(item.acceptedUnix) })}
          {can.run && (
            <>
              <span aria-hidden>·</span>
              <button type="button" disabled={accept.busy} onClick={() => accept.run(who, false)} className="font-bold text-accent-text underline decoration-dotted underline-offset-2 disabled:opacity-50">
                {t("hl.doctor.unaccept")}
              </button>
            </>
          )}
        </span>
      ) : (
        (action || canAccept) && (
          <div className="flex max-w-full flex-wrap items-center gap-2">
            {action}
            {canAccept && (
              <Button variant="ghost" size="md" disabled={accept.busy} title={t("hl.doctor.acceptHint")} onClick={() => accept.run(who, true)}>
                {t("hl.doctor.accept")}
              </Button>
            )}
          </div>
        )
      )}
      {open && steps.length > 0 && <ManualSteps steps={steps} nodeId={nodeId} />}
    </div>
  );
}

/** "Manual ▾" opened: each step in words, with the exact command to copy or the button to where it is done. */
function ManualSteps({ steps, nodeId }: { steps: readonly ManualStep[]; nodeId: string }) {
  const t = useT();
  return (
    <ol className="flex basis-full flex-col gap-2.5 rounded-field border border-line bg-canvas p-3 sm:ml-12">
      {steps.map((s) => (
        <li key={s.text} className="flex flex-col gap-1.5 text-xs leading-snug">
          <span className="text-pretty">{t(s.text, s.params)}</span>
          {s.command && (
            <div className="flex max-w-full items-center gap-2">
              <code className="min-w-0 rounded-field border border-line bg-surface px-2 py-1 font-mono text-xs break-all text-fg select-all">{s.command}</code>
              <CopyButton value={s.command} />
            </div>
          )}
          {s.open && (
            <Link to="/nodes/$id" params={{ id: nodeId }} search={{ tab: s.open.tab }} className={cx(buttonClass("secondary", "sm"), "w-fit")}>
              {t(s.open.label)}
            </Link>
          )}
        </li>
      ))}
    </ol>
  );
}

/** What the owner can do about warp_path: look at the WARP card (node settings), or have the node report again right now. */
function WarpPathActions({ nodeId }: { nodeId: string }) {
  const t = useT();
  const run = useRunDoctor(nodeId);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Link to="/nodes/$id" params={{ id: nodeId }} search={{ tab: "settings" }} hash="warp" className={buttonClass("primary", "sm")}>
        {t("warp.open")}
      </Link>
      <Button variant="secondary" size="sm" onClick={() => run.mutate()} disabled={run.isPending}>
        {run.isPending ? t("hl.doctor.rerunning") : t("warp.recheck")}
      </Button>
    </div>
  );
}

/**
 * What is fine or was not checked, as a compact two-column list (one column on a phone): the title and the node's
 * fact in one line each, no explanation and no button. Problems stay in DoctorRow with their full text.
 */
export function DoctorCompact({ items, stale }: { items: readonly DoctorItem[]; stale?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <ul className={cx("grid gap-x-8 pt-1.5 pb-2.5 sm:grid-cols-2", stale && "opacity-60")}>
      {items.map((item) => {
        const fact = itemDetail(t, item, itemParams(t, fmt, item)) ?? item.detail;
        return (
          <li key={item.id} className="flex items-start gap-2.5 border-t border-line py-1.5 first:border-t-0 sm:[&:nth-child(2)]:border-t-0">
            <span className="mt-[5px] flex">
              <StatusDot kind={doctorKind(item.status)} />
            </span>
            <CheckChip id={item.id} />
            <span className="min-w-0 text-[13px] leading-snug">
              <b>{itemTitle(t, item)}</b>
              {fact && <span className="ml-2 text-xs break-words text-muted">{fact}</span>}
            </span>
          </li>
        );
      })}
    </ul>
  );
}
