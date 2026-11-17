import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Chip } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon, type IconName, type Tone } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { health, nodes as nodesApi } from "@/lib/api";
import { itemDetail, itemParams } from "@/lib/doctor-detail";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import {
  checkTitle,
  doctorKind,
  doctorQuery,
  isAccepted,
  itemCommand,
  itemFix,
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
export const checkMarks: Record<string, { icon: IconName; tone: Tone }> = {
  disk_space: { icon: "disk", tone: "sand" },
  journald_size: { icon: "disk", tone: "sand" },
  dstate_tasks: { icon: "cpu", tone: "sand" },
  memory_pressure: { icon: "memory", tone: "sand" },
  cpu_softirq: { icon: "cpu", tone: "sand" },
  cpu_steal: { icon: "cpu", tone: "sand" },
  agent_fds: { icon: "file", tone: "sand" },
  kernel_headers: { icon: "cpu", tone: "sand" },
  time_sync: { icon: "clock", tone: "sand" },
  resolver: { icon: "dns", tone: "sky" },
  ipv6: { icon: "globe", tone: "sky" },
  foreign_vpn: { icon: "shieldOff", tone: "sky" },
  foreign_nft: { icon: "shieldOff", tone: "sky" },
  port_conflicts: { icon: "plug", tone: "sky" },
  net_baseline: { icon: "network", tone: "sky" },
  conntrack_fill: { icon: "network", tone: "sky" },
  warp_path: { icon: "bolt", tone: "sky" },
  cert_expiry: { icon: "lock", tone: "lavender" },
  awg_backend: { icon: "shield", tone: "sage" },
};

const linkClass = "font-mono text-xs font-medium underline decoration-dotted underline-offset-2 hover:text-accent-text";

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
    onError: (e) => toast.error(t("common.labeled", { label: t("hl.doctor.rerunFailed"), value: errorText(e, t) })),
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
 * What one doctor item means and what can be done about it: its kind, whether it is a problem (or accepted as normal), the reason
 * and the node's fact, the action that fits (the fix, the copyable command, the WARP card, the steps by hand), and whether it can
 * be accepted. DoctorRow and the node's Doctor tab both read it, so a fix started from either shows its progress on both.
 */
export function useDoctorItem({ flow, nodeId, nodeName, item, stale }: { flow: FixFlow; nodeId: string; nodeName: string; item: DoctorItem; stale?: string }) {
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
  if (target && item.id === "warp_path")
    action = (
      <>
        <FixControl flow={flow} target={target} variant={item.status === DoctorStatus.FAIL ? "primary" : "secondary"} />
        <WarpPathActions nodeId={nodeId} hasFix />
      </>
    );
  else if (target) action = <FixControl flow={flow} target={target} variant={item.status === DoctorStatus.FAIL ? "primary" : "secondary"} />;
  else if (command)
    action = (
      <div className="flex max-w-full items-center gap-2">
        <code className="min-w-0 rounded-field border border-line bg-canvas px-2 py-1 font-mono text-xs break-all text-fg select-all">{command}</code>
        <CopyButton value={command} />
      </div>
    );
  else if (item.id === "warp_path" && issue) action = <WarpPathActions nodeId={nodeId} />;
  else if (issue && item.id === "awg_backend" && item.params.switch_auto === "1" && can.fix) action = <SwitchToAuto nodeId={nodeId} nodeName={nodeName} />;
  else if (steps.length > 0)
    action = (
      <Button variant="secondary" size="md" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        {t("hl.doctor.manual")}
        <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
      </Button>
    );
  else if (issue) action = <Chip>{t("hl.doctor.manual")}</Chip>;

  return { t, fmt, can, accept, me, open, setOpen, kind, accepted, issue, why, fact, command, steps, phase, canAccept, who, action, fixKey: target?.key ?? "" };
}

/** "Manual ▾" opened: each step in words, with the exact command to copy or the button to where it is done. */
export function ManualSteps({ steps, nodeId }: { steps: readonly ManualStep[]; nodeId: string }) {
  const t = useT();
  return (
    <ol className="flex basis-full flex-col gap-2.5 rounded-field border border-line bg-canvas p-3 sm:ms-12">
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

/**
 * The way out for an agent that cannot fall back to userspace by itself (no awg-fallback/1; the panel marks the row with
 * switch_auto): the node's AmneziaWG backend becomes "auto", the same call the settings card makes. The module is used when it
 * loads and userspace when it does not.
 */
function SwitchToAuto({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const run = useMutation({
    mutationFn: () => nodesApi.updateNode({ nodeId, awgBackend: "auto" }),
    onSuccess: () => {
      toast(t("awg.switchAuto.done", { node: nodeName }));
      for (const k of ["health", "node", "nodes", "overview"]) void qc.invalidateQueries({ queryKey: [k] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (
    <Button variant="primary" size="md" title={t("awg.switchAuto.hint")} disabled={run.isPending} onClick={() => run.mutate()}>
      {t("awg.switchAuto.label")}
    </Button>
  );
}

/** What the owner can do about warp_path: look at the WARP card (node settings), or have the node report again right now. */
function WarpPathActions({ nodeId, hasFix = false }: { nodeId: string; hasFix?: boolean }) {
  const t = useT();
  const run = useRunDoctor(nodeId);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Link to="/nodes/$id" params={{ id: nodeId }} search={{ tab: "exit" }} hash="warp" className={buttonClass(hasFix ? "secondary" : "primary", "sm")}>
        {t("warp.open")}
      </Link>
      <Button variant="secondary" size="sm" onClick={() => run.mutate()} disabled={run.isPending}>
        {run.isPending ? t("hl.doctor.rerunning") : t("warp.recheck")}
      </Button>
    </div>
  );
}
