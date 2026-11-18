import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type CSSProperties } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import type { ChannelDirection } from "@/gen/mistgate/probe/v1/probe_pb";
import { useT, type T } from "@/i18n";
import { probe } from "@/lib/api";
import { channelPlanQuery, channelStatusQuery, currentStep, errorCodeOf, minutesOf, offer, parseLimit, progressOf, type Step } from "@/lib/channel-measure";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { Spin } from "./parts";
import "./server.css";
import "./channel-measure.css";

/**
 * "Measure the channel" next to the capacity field. The owner's own action, never a schedule: the node trades data with up to three of the
 * owner's other nodes that have the widest channel (8 streams for 10 seconds, receiving and then sending), and the window that opens says so
 * before anything runs. The run goes on in the panel, so closing the window does not stop it (Stop does), and the card shows it. A result
 * offers a number for the channel field; it is written only by pressing "Set", with the call that saves the node.
 */
/** Development only (/dev-kit, sv=ch&cm=...): the window starts open, and as one that has started its run, the way a click would leave it. */
export const channelDemo: { current?: { open?: boolean; asked?: boolean } } = {};

export function useChannelMeasure(nodeId: string, enabled: boolean) {
  const qc = useQueryClient();
  const demo = channelDemo.current;
  const [open, setOpen] = useState(demo?.open ?? false);
  // this window started the run (a result is shown only for a run it started or one that is still going)
  const [asked, setAsked] = useState(demo?.asked ?? false);
  const [refused, setRefused] = useState("");
  const statusKey = channelStatusQuery(nodeId).queryKey;
  const status = useQuery({ ...channelStatusQuery(nodeId), enabled });
  const running = status.data?.state === "running";
  const plan = useQuery({ ...channelPlanQuery(nodeId), enabled: enabled && open && !asked && !running });
  const start = useMutation({
    mutationFn: () => probe.channelMeasureStart({ nodeId }),
    onSuccess: (r) => {
      const st = r.status;
      if (st?.error) {
        setRefused(st.error);
        void plan.refetch();
        return;
      }
      setRefused("");
      setAsked(true);
      qc.setQueryData(statusKey, st);
    },
  });
  const cancel = useMutation({
    mutationFn: () => probe.channelMeasureCancel({ nodeId }),
    onSuccess: (r) => qc.setQueryData(statusKey, r.status),
  });
  return {
    open,
    setOpen: (v: boolean) => {
      setOpen(v);
      if (v) {
        setRefused("");
        void qc.invalidateQueries({ queryKey: statusKey });
      } else setAsked(false);
    },
    status,
    running,
    asked,
    refused,
    plan,
    start,
    cancel,
  };
}
export type ChannelMeasure = ReturnType<typeof useChannelMeasure>;

const mbpsOf = (t: T, n: number) => t("np.sv.ch.mbps", { n: Math.round(n) });

/** The button at the field's end: it opens the window, or shows that a run is going. */
export function ChannelMeasureButton({ m }: { m: ChannelMeasure }) {
  const t = useT();
  return (
    <Button type="button" variant="secondary" size="lg" aria-busy={m.running} onClick={() => m.setOpen(true)}>
      {m.running && <Spin />}
      {m.running ? t("cm.running") : t("cm.open")}
    </Button>
  );
}

/** The last measure the panel kept, one line under the field: when, the two figures, and who it was measured against. */
export function ChannelLastLine({ m }: { m: ChannelMeasure }) {
  const t = useT();
  const fmt = useFmt();
  const last = m.status.data?.last;
  if (!last || m.running) return null;
  const via = [...new Set([last.down?.partner, last.up?.partner].filter(Boolean))].join(", ");
  const cap = (d?: { capped: boolean }) => (d?.capped ? `${t("cm.last.capped")} ` : "");
  return (
    <p className="cm-last" data-testid="channel-last">
      <span>{t("cm.last", { when: fmt.ago(Number(last.atUnix)), down: cap(last.down) + mbpsOf(t, last.down?.mbitS ?? 0), up: cap(last.up) + mbpsOf(t, last.up?.mbitS ?? 0) })}</span>
      {via && <span className="text-muted">{t("cm.last.via", { partners: via })}</span>}
    </p>
  );
}

/** The window: what it will do and the ask, then the run, then the result and the number it offers. */
export function ChannelMeasureDialog({ m, name, configured, onSet }: { m: ChannelMeasure; name: string; configured: number; onSet: (mbps: number) => Promise<unknown> }) {
  const t = useT();
  const st = m.status.data;
  const state = st?.state ?? "idle";
  const live = m.running || (m.asked && state !== "idle");
  return (
    <Modal open={m.open} onOpenChange={m.setOpen} title={t("cm.title", { name })} closeLabel={t("common.close")} shot="channel-measure"
      footer={live ? <RunFooter m={m} /> : <ConfirmFooter m={m} />}>
      {live ? <RunBody m={m} configured={configured} onSet={onSet} /> : <ConfirmBody m={m} name={name} />}
    </Modal>
  );
}

function refusalText(t: T, code: string, retryAfterS: number): string {
  const c = errorCodeOf(code);
  return c === "too_soon" ? t("cm.err.too_soon", { minutes: minutesOf(retryAfterS) }) : t(`cm.err.${c}`);
}

function ConfirmBody({ m, name }: { m: ChannelMeasure; name: string }) {
  const t = useT();
  const p = m.plan.data;
  if (m.plan.isPending) return <p className="cm-lead">{t("cm.loading")}</p>;
  if (m.plan.error) return <Notice tone="danger">{errorText(m.plan.error, t)}</Notice>;
  if (!p) return null;
  const why = m.refused || p.error;
  return (
    <>
      {why && <Notice tone={why === "too_soon" || why === "busy" ? "warn" : "danger"}>{refusalText(t, why, p.retryAfterS || m.start.data?.status?.retryAfterS || 0)}</Notice>}
      {(!why || why === "too_soon") && p.partners.length > 0 && (
        <>
          <p className="cm-lead">{t("cm.lead", { name })}</p>
          <div className="cm-partners">
            <span>{t("cm.partners")}</span>
            <ul>
              {p.partners.map((c) => (
                <li key={c.name} className="mono">
                  {c.knownMbps > 0 ? t("cm.partnerKnown", { name: c.name, mbps: mbpsOf(t, c.knownMbps) }) : t("cm.partnerUnknown", { name: c.name })}
                </li>
              ))}
            </ul>
          </div>
          {!why && <Notice tone="warn">{t("cm.warn", { gb: p.maxGb, seconds: p.estSeconds })}</Notice>}
        </>
      )}
    </>
  );
}

function ConfirmFooter({ m }: { m: ChannelMeasure }) {
  const t = useT();
  const p = m.plan.data;
  const can = !!p && !p.error && !m.plan.error && !m.refused;
  return (
    <>
      <Button type="button" variant="secondary" onClick={() => m.setOpen(false)}>
        {t("common.cancel")}
      </Button>
      <Button type="button" variant="primary" disabled={!can || m.start.isPending} aria-busy={m.start.isPending} onClick={() => m.start.mutate()}>
        {m.start.isPending && <Spin />}
        {t("cm.start")}
      </Button>
    </>
  );
}

/** Seconds since the run began, ticking once a second while it goes. */
function useElapsed(startedUnix: number, on: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [on]);
  return startedUnix > 0 ? Math.max(0, Math.floor(now / 1000) - startedUnix) : 0;
}

const stepLabel = (t: T, s: Step) => `${s.direction === "up" ? t("cm.tx") : t("cm.rx")} · ${s.partner}`;

function RunBody({ m, configured, onSet }: { m: ChannelMeasure; configured: number; onSet: (mbps: number) => Promise<unknown> }) {
  const t = useT();
  const st = m.status.data!;
  const steps = st.steps;
  const now = currentStep(steps);
  const elapsed = useElapsed(Number(st.startedUnix), st.state === "running");
  if (st.state === "running") {
    const { over, total } = progressOf(steps);
    return (
      <>
        <div className="cm-now" role="status">
          <b>{now ? t(now.direction === "up" ? "cm.now.up" : "cm.now.down", { partner: now.partner }) : t("cm.running")}</b>
          <span className="mono">{t("cm.stepOf", { n: Math.min(over + 1, total), total })}</span>
          <span className="mono">{t("cm.elapsed", { n: elapsed })}</span>
        </div>
        <div className="sv-runs cm-runs cm-steps">
          {steps.map((s, i) => (
            <StepTile key={i} s={s} />
          ))}
        </div>
        <p className="cm-note">{t("cm.stopNote")}</p>
      </>
    );
  }
  if (st.state === "canceled") return <Notice tone="quiet">{t("cm.canceled")}</Notice>;
  if (st.state === "failed") {
    const reason = refusalText(t, st.error, st.retryAfterS);
    return (
      <>
        <Notice tone="danger">{t("cm.failed", { reason })}</Notice>
        <div className="sv-runs cm-runs cm-steps">
          {steps.map((s, i) => (
            <StepTile key={i} s={s} />
          ))}
        </div>
      </>
    );
  }
  return <Result m={m} configured={configured} onSet={onSet} />;
}

function StepTile({ s }: { s: Step }) {
  const t = useT();
  const cls = s.state === "done" ? "done" : s.state === "running" ? "now" : s.state === "failed" ? "failed" : "";
  const w = s.state === "done" ? "100%" : "0%";
  const v = s.state === "done" ? t("np.sv.ch.mbps", { n: Math.round(s.mbitS) }) : s.state === "failed" ? "—" : "…";
  const note = s.state === "failed" ? refusalText(t, s.error, 600) : s.state === "waiting" ? t("cm.waiting") : s.state === "done" && s.capped ? t("cm.capped") : "";
  return (
    <div className={cls}>
      <span>{stepLabel(t, s)}</span>
      <b className="mono">{v === "…" || v === "—" ? v : <Scramble value={v} once />}</b>
      <i style={{ "--w": w } as CSSProperties} aria-hidden />
      <em>{note}</em>
    </div>
  );
}

function Result({ m, configured, onSet }: { m: ChannelMeasure; configured: number; onSet: (mbps: number) => Promise<unknown> }) {
  const t = useT();
  const res = m.status.data!.result;
  const down = res?.down?.mbitS ?? 0;
  const up = res?.up?.mbitS ?? 0;
  const o = offer(configured, down, up);
  const [text, setText] = useState(String(o.value || ""));
  const [saved, setSaved] = useState(false);
  const typed = parseLimit(text);
  const set = useMutation({ mutationFn: (v: number) => onSet(v), onSuccess: () => setSaved(true) });
  const tile = (key: string, d: ChannelDirection | undefined) => (
    <div className={d && d.mbitS > 0 ? "done" : ""} key={key}>
      <span>{key === "rx" ? t("cm.rx") : t("cm.tx")}</span>
      <b className="mono">{d && d.mbitS > 0 ? <Scramble value={t("np.sv.ch.mbps", { n: Math.round(d.mbitS) })} once /> : "—"}</b>
      <i style={{ "--w": d && d.mbitS > 0 ? "100%" : "0%" } as CSSProperties} aria-hidden />
      <em>{d && d.mbitS > 0 ? `${t("cm.via", { partner: d.partner })}${d.capped ? ` · ${t("cm.capped")}` : ""}` : ""}</em>
    </div>
  );
  return (
    <>
      <div className="sv-runs cm-runs cm-two" role="status">
        {tile("rx", res?.down)}
        {tile("tx", res?.up)}
      </div>
      <p className="cm-note">{t("cm.note")}</p>
      {down <= 0 || up <= 0 ? (
        <Notice tone="quiet">{t("cm.offer.partial")}</Notice>
      ) : o.same ? (
        <Notice tone="quiet">{t("cm.offer.same", { configured: mbpsOf(t, configured) })}</Notice>
      ) : (
        o.value > 0 && (
          <div className="cm-offer">
            <p>
              {configured > 0
                ? t("cm.offer", { configured: mbpsOf(t, configured), measured: mbpsOf(t, Math.min(down, up)), value: mbpsOf(t, o.value) })
                : t("cm.offer.none", { measured: mbpsOf(t, Math.min(down, up)), value: mbpsOf(t, o.value) })}
            </p>
            <div className="cm-offer-row">
              <label>
                <span>{t("cm.offer.field")}</span>
                <input className={cx("cm-input", "mono")} dir="ltr" type="number" inputMode="numeric" min={1} max={1_000_000} step={1} value={text} onChange={(e) => (setText(e.target.value), setSaved(false))} data-invalid={typed === null ? "" : undefined} aria-invalid={typed === null} />
              </label>
              <Button type="button" variant="primary" size="lg" disabled={typed === null || set.isPending || saved || typed === configured} aria-busy={set.isPending} onClick={() => typed !== null && set.mutate(typed)}>
                {set.isPending && <Spin />}
                {saved ? t("common.saved") : t("cm.offer.set", { value: typed === null ? "" : mbpsOf(t, typed) })}
              </Button>
            </div>
            {typed === null && <small className="text-danger-text">{t("cm.offer.bad")}</small>}
          </div>
        )
      )}
    </>
  );
}

function RunFooter({ m }: { m: ChannelMeasure }) {
  const t = useT();
  if (!m.running) {
    return (
      <Button type="button" variant="secondary" onClick={() => m.setOpen(false)}>
        {t("common.close")}
      </Button>
    );
  }
  return (
    <Button type="button" variant="secondary" disabled={m.cancel.isPending} aria-busy={m.cancel.isPending} onClick={() => m.cancel.mutate()}>
      {m.cancel.isPending && <Spin />}
      {m.cancel.isPending ? t("cm.stopping") : t("cm.stop")}
    </Button>
  );
}
