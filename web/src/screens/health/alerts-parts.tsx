import { Menu } from "@base-ui/react/menu";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { health, nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { quoteNames } from "@/lib/doctor-detail";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { NETS_INLINE, alertNetworks, muteChoices, networkSources, networksByOperator, restartConsequence, sourceName, useCan, type Alert } from "@/lib/health";
import { plain } from "@/lib/plain";
import { lossPct, regionWord, senderLabel } from "@/lib/port-check";
import "./health.css";

// What an alert's body and its buttons are made of, apart from the rows (alerts.tsx): the hooks that mute and close, the menu of "Mute", the tables of
// the ports that lose packets and of the two senders of a lossy path, the networks of a country alert, the windows of a restart and of the IPv6 switch.

/** Mute for a while, or unmute, one alert or a card's several at once; the toast offers the opposite as "Undo". */
export function useMute() {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const call = useMutation({
    mutationFn: (v: { ids: string[]; seconds: number }) => Promise.all(v.ids.map(async (id) => plain(await health.muteAlert({ alertId: id, durationS: v.seconds })))),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["health", "alerts"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (alerts: readonly Alert[], seconds: number) => {
    const a = alerts[0]!;
    const ids = alerts.map((x) => x.id);
    const n = ids.length;
    call.mutate(
      { ids, seconds },
      {
        onSuccess: (res) => {
          const node = a.nodeName || a.params.user_name || t("hl.alerts.fleetWide");
          if (seconds > 0) {
            const time = fmt.stamp(res[0]?.alert?.mutedUntilUnix || Math.floor(Date.now() / 1000) + seconds);
            const text = n > 1 ? t.n("hl.alerts.mutedToastMany", n, { node, time }) : t("hl.alerts.mutedToast", { node, time });
            toast(text, { undo: () => call.mutate({ ids, seconds: 0 }) });
          } else toast(n > 1 ? t.n("hl.alerts.unmutedToastMany", n, { node }) : t("hl.alerts.unmutedToast", { node }));
        },
      },
    );
  };
}

/**
 * Close one alert or a card's several at once (the alert resolves now and stays closed while its problem goes on); the toast
 * offers "Undo", which re-opens them. No confirmation: it is reversible.
 */
export function useClose() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const call = useMutation({
    mutationFn: (v: { ids: string[]; reopen: boolean }) => Promise.all(v.ids.map(async (id) => plain(await health.dismissAlert({ alertId: id, reopen: v.reopen })))),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["health", "alerts"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (alerts: readonly Alert[], onDone?: () => void) => {
    const ids = alerts.map((x) => x.id);
    const n = ids.length;
    call.mutate(
      { ids, reopen: false },
      {
        onSuccess: () => {
          onDone?.();
          toast(n > 1 ? t.n("hl.alerts.closedToastMany", n) : t("hl.alerts.closedToast"), { undo: () => call.mutate({ ids, reopen: true }) });
        },
      },
    );
  };
}

/**
 * "Close all shown", in the head of a card of alerts (the board's .btn.sm after the count): asks first, naming how many, in the board's window;
 * the toast that follows has "Undo". Nothing for a role that cannot close, or for an empty list.
 */
export function CloseAll({ alerts }: { alerts: readonly Alert[] }) {
  const t = useT();
  const can = useCan();
  const close = useClose();
  const [open, setOpen] = useState(false);
  if (!can.run || alerts.length < 1) return null;
  return (
    <>
      <Button variant="secondary" size="md" className="btn" title={t("hl.alerts.closeHint")} onClick={() => setOpen(true)}>
        {t("hl.alerts.closeAll")}
      </Button>
      <Modal
        open={open}
        onOpenChange={setOpen}
        title={t.n("hl.alerts.closeAllTitle", alerts.length)}
        closeLabel={t("common.close")}
        className="md:max-w-[480px]!"
        footer={
          <>
            <Button variant="secondary" size="lg" onClick={() => setOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="primary" size="lg" onClick={() => close(alerts, () => setOpen(false))}>
              {t("hl.alerts.close")}
            </Button>
          </>
        }
      >
        <div className="hl-dlgb">
          <p>{t("hl.alerts.closeAllBody")}</p>
        </div>
      </Modal>
    </>
  );
}

// a header cell sets its own alignment: the browser centres a <th>, and the table's inherited start does not reach it
const thStart = "px-3 py-2 text-start font-semibold whitespace-nowrap";
const thEnd = "px-3 py-2 text-end font-semibold whitespace-nowrap";

/**
 * The ports of a node that lose packets: port, profile, the share lost and where it was checked from. A port confirmed from a
 * second sender shows both, a line each, in the same order in both cells.
 */
export function LossTable({ alerts }: { alerts: Alert[] }) {
  const t = useT();
  return (
    <div className="overflow-x-auto rounded-field border border-line">
      <table className="w-full text-[13px]">
        <thead className="text-[11px] text-muted">
          <tr className="border-b border-line">
            <th className={thStart}>{t("hl.alerts.col.port")}</th>
            <th className={thStart}>{t("hl.alerts.col.profile")}</th>
            <th className={thEnd}>{t("hl.alerts.col.lost")}</th>
            <th className={thStart}>{t("hl.alerts.col.from")}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {alerts.map((a) => {
            const p = a.params;
            // one line per sender in both cells, so each share stands next to who measured it
            const runs = [{ sender: p.sender ?? "", lost: lossPct(Number(p.sent), Number(p.got)) }];
            if (p.confirm_sender) runs.push({ sender: p.confirm_sender, lost: lossPct(Number(p.confirm_sent), Number(p.confirm_got)) });
            return (
              <tr key={a.id}>
                <td className="px-3 py-2 font-mono font-bold">{p.port}</td>
                <td className="px-3 py-2">{p.profile}</td>
                <td className="px-3 py-2 text-end font-mono whitespace-nowrap text-warn-text tabular-nums">
                  {runs.map((r, i) => (
                    <span key={i} className="block">
                      <span dir="ltr">{r.lost} %</span>
                    </span>
                  ))}
                </td>
                <td className="px-3 py-2 text-muted">
                  {runs.map((r, i) => (
                    <span key={i} className="block">
                      {senderLabel(t, r.sender)}
                    </span>
                  ))}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/**
 * The two senders of a path_lossy alert, so it reads at a glance whose side loses: this node's packets (warn) and the other
 * sender's that got through (ok), each with its region, what arrived of the worst port and the share lost. A filtered path
 * (params.open) says which ports got through instead, "only 443" against "everything", and has no share lost.
 */
export function PathRuns({ a }: { a: Alert }) {
  const t = useT();
  const p = a.params;
  const sent = Number(p.sent);
  const open = p.open ?? "";
  const rows = [
    { name: p.node || a.nodeName, region: p.region ?? "", got: Number(p.got), kind: "warn" as const, arrived: open && t("hl.alerts.path.only", { open }) },
    { name: p.other ?? "", region: p.other_region ?? "", got: Number(p.other_got), kind: "ok" as const, arrived: open && t("hl.alerts.path.all") },
  ];
  return (
    // two rows: kept compact, so the numbers stay next to the names on a wide card
    <div className="max-w-xl overflow-x-auto rounded-field border border-line" data-testid="path-runs">
      <table className="w-full text-[13px]">
        <thead className="text-[11px] text-muted">
          <tr className="border-b border-line">
            <th className={thStart}>{t("hl.alerts.col.from")}</th>
            <th className={thEnd}>{t("hl.alerts.col.arrived")}</th>
            {!open && <th className={thEnd}>{t("hl.alerts.col.lost")}</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {rows.map((r) => (
            <tr key={r.kind}>
              <td className="px-3 py-2">
                <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                  <StatusDot kind={r.kind} />
                  <b className="font-mono">{r.name}</b>
                  <span className="text-xs text-muted">{regionWord(t, r.region)}</span>
                </span>
              </td>
              <td className={cx("px-3 py-2 text-end font-mono whitespace-nowrap tabular-nums", open && (r.kind === "warn" ? "font-semibold text-warn-text" : "text-muted"))}>
                {r.arrived || (
                  <span dir="ltr">
                    {r.got}/{sent}
                  </span>
                )}
              </td>
              {!open && (
                <td className={cx("px-3 py-2 text-end font-mono whitespace-nowrap tabular-nums", r.kind === "warn" ? "font-semibold text-warn-text" : "text-muted")}>
                  <span dir="ltr">{lossPct(sent, r.got)} %</span>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * Every network of a country alert behind "Show all N" when the sentence names only the first operators: whose probes they are,
 * once, then a row per operator with its AS numbers and its regions. A region names its source only where that says something.
 */
export function Networks({ list }: { list: string }) {
  const t = useT();
  const fmt = useFmt();
  const id = useId();
  const [open, setOpen] = useState(false);
  const nets = alertNetworks(list);
  if (nets.length <= NETS_INLINE) return null;
  const sources = networkSources(nets).map((s) => `${sourceName(t, s.value)} ${s.n}`);
  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        data-nets-toggle
        onClick={() => setOpen(!open)}
        className="w-fit rounded-sm text-xs font-semibold text-muted underline decoration-dotted underline-offset-2 hover:text-fg"
      >
        {open ? t("cr.part.fewer") : t("hl.nets.all", { n: nets.length })}
      </button>
      {open && (
        <div id={id} className="flex flex-col gap-2 rounded-field bg-surface-2 px-3 py-2.5 text-xs">
          {sources.length > 0 && <p className="text-[11px] text-muted">{t("common.labeled", { label: t("rc.sources"), value: sources.join(" · ") })}</p>}
          {/* a wide card: operator, AS numbers and regions in columns; a phone puts the regions under the operator */}
          <dl className="grid grid-cols-1 border-t border-line @2xl:grid-cols-[max-content_max-content_minmax(0,1fr)] @2xl:gap-x-4">
            {networksByOperator(nets, fmt.locale).map((o) => {
              const groups = o.groups.filter((g) => g.regions.length > 0 || g.marked);
              return (
                <div key={o.name} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 border-b border-line py-2 last:border-b-0 last:pb-0 @2xl:col-span-3 @2xl:grid @2xl:grid-cols-subgrid">
                  <dt className="font-semibold">{o.name}</dt>
                  {o.asns.length > 0 && (
                    <dd className="font-mono text-[11px] text-muted">
                      <span dir="ltr">{o.asns.map((a) => `AS${a}`).join(" · ")}</span>
                    </dd>
                  )}
                  {groups.length > 0 && (
                    <dd className="flex min-w-0 basis-full flex-col gap-0.5 leading-[18px] text-muted @2xl:col-start-3">
                      {groups.map((g) => (
                        // the source leads its regions, so the tags of an operator's lines stand in one column and its regions wrap beside it
                        <p key={g.source} className="flex items-baseline gap-1.5">
                          {g.marked && <span className="flex-none rounded-[6px] bg-surface px-1.5 py-px font-mono text-[11px] leading-4">{sourceName(t, g.source)}</span>}
                          {g.regions.length > 0 && <span className="min-w-0 break-words">{g.regions.join(t("cr.tip.sep"))}</span>}
                        </p>
                      ))}
                    </dd>
                  )}
                </div>
              );
            })}
          </dl>
        </div>
      )}
    </div>
  );
}

const itemClass = "flex min-h-10 cursor-pointer items-center rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold text-fg outline-none select-none data-highlighted:bg-surface-2";
const muteKeys = muteChoices(new Date(0)).map((c) => c.key);

/** "Mute ▾": for an hour, until the morning, a day or a week, with what muting means under the choices. */
export function MuteMenu({ onMute }: { onMute: (seconds: number) => void }) {
  const t = useT();
  return (
    <Menu.Root>
      {/* the board's quiet link "Mute" (.an-lk): the menu of how long opens from it */}
      <Menu.Trigger className="an-lk">{t("hl.alerts.mute")}</Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
          <Menu.Popup className="w-64 origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            {muteKeys.map((key, i) => (
              // "until morning" is counted at the click, from the clock of that moment
              <Menu.Item key={key} className={itemClass} onClick={() => onMute(muteChoices(new Date())[i]!.seconds)}>
                {t(key)}
              </Menu.Item>
            ))}
            <p className="mt-1 border-t border-line px-2.5 pt-2 pb-1 text-[11px] leading-snug text-pretty text-muted">{t("hl.mute.hint")}</p>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

/**
 * Restarting from an alert asks first, by name and with who drops now (params.online, counted by the panel when it
 * sent the alert). One profile (a check that does not answer) or every profile of the node (none does). A failure
 * stays in the window, above its buttons.
 */
export function RestartDialog({ open, onOpenChange, alert: a, scope }: { open: boolean; onOpenChange: (o: boolean) => void; alert: Alert; scope: "one" | "all" }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const call = useMutation({
    mutationFn: () => nodesApi.restartInbounds({ nodeId: a.nodeId, inboundId: scope === "one" ? (a.params.inbound ?? "") : "" }),
    onSuccess: (r) => {
      onOpenChange(false);
      toast(t("common.labeled", { label: a.nodeName, value: t.n("node.restarted", r.restarted) }));
      for (const k of ["health", "node", "nodes"]) void qc.invalidateQueries({ queryKey: [k] });
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const title = scope === "one" ? t("hl.restart.title", { profiles: quoteNames(t, a.params.profile || a.params.inbound || ""), node: a.nodeName }) : t("hl.restart.titleAll", { node: a.nodeName });
  const body = restartConsequence(t, scope, a.params.online) ?? t("hl.fix.disruptive");
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!call.isPending) onOpenChange(o);
        if (!o) setError("");
      }}
      title={title}
      closeLabel={t("common.close")}
      className="md:max-w-[480px]!"
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={call.isPending} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={call.isPending} onClick={() => call.mutate()}>
            {t("hl.restart.do")}
          </Button>
        </>
      }
    >
      <div className="hl-dlgb">
        <p>{body}</p>
        {error && <Notice tone="danger">{error}</Notice>}
      </div>
    </Modal>
  );
}

/** Turns the node's "IPv6 for clients" off (the same switch as in the node's settings); the agent re-runs its IPv6 check. */
export function ClientIPv6OffDialog({ open, onOpenChange, alert: a }: { open: boolean; onOpenChange: (o: boolean) => void; alert: Alert }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const call = useMutation({
    mutationFn: () => nodesApi.updateNode({ nodeId: a.nodeId, clientIpv6: false }),
    onSuccess: () => {
      onOpenChange(false);
      toast(t("hl.ipv6Off.done", { node: a.nodeName }));
      for (const k of ["health", "node", "nodes"]) void qc.invalidateQueries({ queryKey: [k] });
    },
    onError: (e) => setError(errorText(e, t)),
  });
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!call.isPending) onOpenChange(o);
        if (!o) setError("");
      }}
      title={t("hl.ipv6Off.title", { node: a.nodeName })}
      closeLabel={t("common.close")}
      className="md:max-w-[480px]!"
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={call.isPending} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={call.isPending} onClick={() => call.mutate()}>
            {t("hl.ipv6Off.do")}
          </Button>
        </>
      }
    >
      <div className="hl-dlgb">
        <p>{t("hl.ipv6Off.body")}</p>
        {error && <Notice tone="danger">{error}</Notice>}
      </div>
    </Modal>
  );
}
