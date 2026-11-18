import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { originOf, playWave } from "@/components/fx/wave";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Modal } from "@/components/ui/modal";
import { useToast } from "@/components/ui/toast";
import { type StatusKind } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { checksQuery, countryColumn, countryIn } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { candidatesQuery, countryHint, isCut, reachHint, relaysQuery, seenHint, viaLabel, type Candidate, type Hint, type RelayMsg } from "@/lib/relay";
import { blockedOf, bridgeNote } from "@/lib/relay-bridge";
import { hintOf, type WayId } from "@/lib/ways";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { dismissJob, isWatched, jobOf, retryItem, startJob, stopJob, useJob, watch, type Job } from "./relay-job";
import type { Entry } from "./relay-run";
import { waysDemo } from "./ways-demo";
import type { WaysModel } from "./ways";
import "./relay-wizard.css";

// "Make reachable from Russia" (the board's .rw-wz, plan §8, the redesign's PnNode): four steps in one window. Node (chosen already: the wizard opens
// from the node's page), Entry (the nodes people see, with what is known of each), What people get (the servers that go and who gets them), Done
// (the run, server by server). The run does not belong to the window: it lives in relay-job.ts, so the window may be closed over it and opened again,
// and the strip over the ways (relay-strip.tsx) shows how it goes. `?add=1` opens at the entries, `?add=via:<entry>` at the servers of that entry
// (to finish what a run left, or to add more servers), a run on its way opens at its end.

type Data = Plain<GetNodeResponse>;

/** What a scene of the dev kit starts the wizard from; the real page never gets one. */
export type WizardDemo = { step?: 1 | 2 | 3 | 4; entry?: string };

type Step = 1 | 2 | 3 | 4;

const glyph = (k: StatusKind): "ok" | "part" | "bad" | "none" => (k === "ok" ? "ok" : k === "warn" ? "part" : k === "bad" ? "bad" : "none");

const tick = (
  <svg className="i" viewBox="0 0 24 24" aria-hidden>
    <path d="M5 12.5l4.5 4.5L19 7.5" />
  </svg>
);
const info = (
  <svg className="i" viewBox="0 0 24 24" aria-hidden>
    <path d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8v.5" />
  </svg>
);
const link = (
  <svg className="i" viewBox="0 0 24 24" aria-hidden>
    <path d="M9 15l6-6M10.5 6.5l1.5-1.5a4.24 4.24 0 0 1 6 6l-1.5 1.5M13.5 17.5L12 19a4.24 4.24 0 0 1-6-6l1.5-1.5" />
  </svg>
);

/** The box of a choice. */
const Cb = ({ on }: { on: boolean }) => (
  <span className={cx("wy-cb", on && "on")} aria-hidden>
    {tick}
  </span>
);

type Pick = { node: { id: string; name: string; countryCode: string; hasMetrics?: boolean; cpuPct?: number }; hint?: ReturnType<typeof hintOf>; fit: boolean; already: number };

export function RelayWizard({ data, w, start: asked, onClose }: { data: Data; w: WaysModel; start?: string; onClose: (way?: WayId) => void }) {
  const demo = waysDemo.current?.wizard;
  // the router hands "1" over as it was typed in the address, a number in a page that has no validation of its own (the dev kit)
  const start = asked === undefined ? undefined : String(asked);
  const open = start === "1" || !!start?.startsWith("via:") || !!demo;
  if (!open) return null;
  return <Body data={data} w={w} start={start} demo={demo} onClose={onClose} />;
}

function Body({ data, w, start, demo, onClose }: { data: Data; w: WaysModel; start?: string; demo?: WizardDemo; onClose: (way?: WayId) => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const fx = useEffectsEnabled();
  const node = data.node!;
  const exit = { id: node.id, name: node.name, cc: node.countryCode };
  const job = useJob(node.id);
  const hours = useQuery(settingsQuery).data?.settings.updateIntervalHours || 12;
  const startEntry = start?.startsWith("via:") ? start.slice(4) : "";
  const [step, setStep] = useState<Step>(demo?.step ?? (job ? 4 : startEntry ? 3 : 2));
  const [pk, setPk] = useState(demo?.entry ?? job?.entry.id ?? startEntry);
  const [offs, setOffs] = useState<Record<string, boolean>>({});

  // ---- the entries: the nodes other than this one that people may go through
  const nodes = useQuery(nodesQuery).data?.nodes ?? [];
  const fleet = countryColumn(useQuery(checksQuery("")).data?.countryChecks);
  const relays = useQuery(relaysQuery(node.id)).data ?? [];
  const everyRelay = useQuery(relaysQuery("")).data ?? [];
  const own = hintOf(w.country);
  const cut = isCut(own) && own!.dark.length > 0;
  const sameAsExit = (cc: string) => !!node.countryCode && cc === node.countryCode;
  const picks: Pick[] = nodes
    .filter((n) => n.id !== node.id && n.status !== NodeStatus.RETIRED)
    .map((n) => {
      const hint = hintOf(fleet?.byNode.get(n.id));
      // an entry in the exit's own country is no good fit: the people's route to it would not change; it stays pickable
      const fit = cut && !!hint && (hint.verdict === "ok" || hint.verdict === "partial") && !own!.dark.some((d) => hint.dark.includes(d)) && !sameAsExit(n.countryCode);
      return { node: n, hint, fit, already: relays.filter((r) => r.exitNodeId === node.id && r.viaNodeId === n.id).length };
    })
    // good fits first; among them a node inside the people's country (their route to it stays at home), then one every network sees
    .sort(
      (a, b) =>
        Number(b.fit) - Number(a.fit) ||
        Number(b.node.countryCode === w.cc) - Number(a.node.countryCode === w.cc) ||
        Number(b.hint?.verdict === "ok") - Number(a.hint?.verdict === "ok") ||
        a.node.name.localeCompare(b.node.name),
    );
  // an entry that carries this node's servers already is not offered again (more servers go through "Finish" or "Add more"), but the one in hand stays
  const options = picks.filter((p) => p.already === 0 || p.node.id === pk || p.node.id === startEntry);
  const chosen = options.find((p) => p.node.id === pk) ?? options[0];
  const entryNode = chosen?.node;
  const entry: Entry = { id: entryNode?.id ?? "", name: entryNode?.name ?? "", cc: entryNode?.countryCode ?? "" };
  // does the entry reach this node: the TSPU probe's row, asked for the good fits only (it builds every server of the fleet)
  const fits = options.filter((p) => p.fit);
  const reach = useQueries({ queries: fits.map((p) => ({ ...candidatesQuery(p.node.id), enabled: step === 2 })) });
  const candOf = (id: string) => reach[fits.findIndex((p) => p.node.id === id)]?.data?.candidates.find((c) => c.nodeId === node.id);
  const carries = (id: string) => new Set(everyRelay.filter((r) => r.viaNodeId === id).map((r) => r.exitNodeId)).size;

  // ---- the servers that go through the chosen entry
  const cands = useQuery({ ...candidatesQuery(entry.id), enabled: step >= 3 && entry.id !== "" });
  const servers = (cands.data?.candidates ?? []).filter((c) => c.nodeId === node.id);
  const viaBridge = (c: Candidate) => isCut(c.reach) && (c.exitReachesEntry?.verdict === "reach" || c.exitReachesEntry?.verdict === "partial") && blockedOf(c) === "";
  const bridge = servers.some((c) => !c.unsupported && viaBridge(c));
  const exists = (c: Candidate): RelayMsg | undefined => (c.relayId ? relays.find((r) => r.id === c.relayId) : undefined);
  type Row = { c: Candidate; relay?: RelayMsg; why: string; warn?: boolean; off: boolean };
  const rows: Row[] = servers.map((c) => {
    const relay = exists(c);
    const live = !!relay?.enabled;
    const blocked = bridge && blockedOf(c) !== "";
    const why = c.unsupported
      ? (["cdn", "acme_ip", "build_failed"] as const).includes(c.unsupported as "cdn")
        ? t(`relay.unsupported.${c.unsupported as "cdn"}`)
        : c.unsupported
      : live
        ? t("wy.wz.isOn")
        : blocked
          ? t("wy.wz.noBridge")
          : relay
            ? t("wy.wz.made")
            : c.protocol === "awg"
              ? t("wy.wz.awgKey")
              : c.hop
                ? t("relay.dlg.hop", { port: c.port })
                : "";
    return { c, relay, why, warn: !!c.hop && !c.unsupported && !live && !blocked && !relay, off: !!c.unsupported || live || blocked };
  });
  const picked = (r: Row) => !r.off && !offs[r.c.inboundId];
  const go = rows.filter(picked);
  // the people who are given any of the servers: each server's count has the same people in it, so the most of them, not their sum
  const people = Math.max(0, ...go.map((r) => r.c.audience?.people ?? 0));
  const awgDevices = go.reduce((a, r) => a + (r.c.audience?.staticAwgDevices ?? 0), 0);

  // ---- the run: in the store, not here
  const running = job?.state === "running" || job?.state === "stopping";
  useEffect(() => (step === 4 ? watch(node.id) : undefined), [step, node.id]);
  const next = useRef<HTMLButtonElement>(null);
  const was = useRef(job?.state);
  useEffect(() => {
    // the whole run came out: the wave from the button, and the page's lane sweeps in behind the window
    if ((was.current === "running" || was.current === "stopping") && job?.state === "finished" && job.on === job.items.length) playWave(originOf(next.current), { light: document.documentElement.dataset.theme === "light" });
    was.current = job?.state;
  }, [job?.state, job?.on, job?.items.length]);

  // the window is closed when the run ends: the toast is how the owner hears of it, and what is left shows in the strip
  const told = (j: Job) => {
    if (isWatched(node.id)) return;
    toast(j.on === j.items.length ? t("wy.wz.toast.ok", { exit: j.exitName, entry: j.entry.name }) : t("wy.wz.toast.part", { exit: j.exitName, entry: j.entry.name, k: j.on, n: j.items.length }), { ms: 8000 });
    dismissJob(j.exitId);
  };
  const ctx = { t, qc, entry: job?.entry ?? entry, hours, way: "auto" as const };
  const launch = () => {
    if (!entryNode || go.length === 0) return;
    startJob(
      ctx,
      exit,
      go.map((r) => ({ c: r.c, ...(r.relay ? { existing: r.relay } : {}) })),
      bridge,
      told,
    );
    setStep(4);
  };
  const close = () => {
    const j = jobOf(node.id);
    const over = j?.state === "finished" || j?.state === "stopped";
    if (over) dismissJob(node.id);
    onClose(j?.state === "finished" && j.on > 0 ? `via:${j.entry.id}` : undefined);
  };
  const other = () => {
    dismissJob(node.id);
    setOffs({});
    setStep(2);
  };

  const done = (i: number) => step > i || (i === 4 && job?.state === "finished" && !running);
  const labels = [t("wy.wz.s1"), t("wy.wz.s2"), t("wy.wz.s3"), t("wy.wz.s4")];
  const canNext = step === 1 || (step === 2 && !!entryNode) || (step === 3 && go.length > 0) || step === 4;
  const okAll = !!job && job.state === "finished" && job.on === job.items.length;

  const verdict = (() => {
    const direct = w.lanes.find((l) => l.kind === "direct");
    const relayWorks = w.lanes.some((l) => l.kind === "via" && l.state === "ok");
    const badDirect = direct?.ring === "bad" || direct?.seen === "none";
    if (badDirect && !relayWorks) return { c: "bad" as const, t: t("wy.wz.v.bad") };
    if (badDirect || direct?.ring === "half" || direct?.seen === "part") return { c: "part" as const, t: t("wy.wz.v.part") };
    return { c: "ok" as const, t: t("wy.wz.v.ok") };
  })();

  const hints = (p: Pick): Hint[] => {
    const seen = (cut ? seenHint(t, p.node.name, own, p.hint) : null) ?? countryHint(t, w.cc, p.hint);
    const c = candOf(p.node.id);
    const re = reachHint(t, p.node.name, node.name, c?.reach);
    const br = c ? bridgeNote(t, c, { entry: p.node.name, exit: node.name }) : null;
    const same: Hint | null = sameAsExit(p.node.countryCode) ? { tone: "off", text: t("relay.hint.sameCountry") } : null;
    const n = carries(p.node.id);
    const load: Hint = { tone: "ok", text: p.node.hasMetrics ? (n > 0 ? t.n("wy.wz.loadLine", n, { pct: Math.round(p.node.cpuPct ?? 0) }) : t("wy.wz.loadOnly", { pct: Math.round(p.node.cpuPct ?? 0) })) : n > 0 ? t.n("wy.wz.carries", n) : t("wy.wz.carries0") };
    return [seen, re, ...(br ? [br] : []), ...(same ? [same] : []), load];
  };

  const body: Record<Step, ReactNode> = {
    1: (
      <div className="wy-wb">
        <p className="wy-lead">{t("wy.wz.lead1")}</p>
        <div className="wy-wr" role="checkbox" aria-checked="true" aria-disabled="true">
          <Cb on />
          <Flag code={node.countryCode} size={14} className="rounded-[3px]" />
          <b className="mono">{node.name}</b>
          <span>{node.location}</span>
          <em className={verdict.c}>
            <i className={cx("wy-gl", verdict.c)} aria-hidden />
            {verdict.t}
          </em>
        </div>
      </div>
    ),
    2: (
      <div className="wy-wb">
        <p className="wy-lead">{options.length > 0 ? t("wy.wz.lead2", { node: node.name }) : t("wy.wz.lead2.none", { node: node.name })}</p>
        <div role="radiogroup" aria-label={t("wy.wz.s2")} className="wy-rg">
        {options.map((p) => {
          const on = p.node.id === entry.id;
          return (
            <button key={p.node.id} type="button" role="radio" aria-checked={on} data-testid="way-entry" className="wy-we" onClick={() => setPk(p.node.id)}>
              <span className="wy-rad" aria-hidden>
                <i />
              </span>
              <div>
                <b>
                  <Flag code={p.node.countryCode} size={14} className="rounded-[3px]" />
                  <span className="mono">{p.node.name}</span>
                  {p.fit && <span className="wy-tag">{t("wy.wz.fit")}</span>}
                </b>
                {hints(p).map((h) => (
                  <span key={h.text}>
                    <i className={cx("wy-gl", glyph(h.tone))} aria-hidden />
                    {h.text}
                  </span>
                ))}
              </div>
            </button>
          );
        })}
        </div>
      </div>
    ),
    3: (
      <div className="wy-wb">
        {cands.isPending && step === 3 ? (
          <p className="wy-lead">{t("common.loading")}</p>
        ) : rows.length === 0 ? (
          <p className="wy-lead">{t("ways.new.empty", { node: node.name, entry: entry.name })}</p>
        ) : (
          <>
            <div className="wy-pl">
              <div className="h">
                <Flag code={node.countryCode} size={14} className="rounded-[3px]" />
                <b className="mono">{node.name}</b>
                {bridge && (
                  <span className="wy-mchip">
                    {link}
                    {t("wy.wz.bridge")}
                  </span>
                )}
                <em>{t("wy.wz.count", { n: go.length, m: rows.length })}</em>
              </div>
              {bridge && <p className="wy-brn">{t("wy.wz.brNote", { entry: entry.name, exit: node.name })}</p>}
              {rows.map((r) => (
                <button
                  key={r.c.inboundId}
                  type="button"
                  role="checkbox"
                  aria-checked={picked(r)}
                  aria-disabled={r.off}
                  data-testid="wizard-server"
                  className={cx("wy-ps", r.off && "off")}
                  onClick={() => !r.off && setOffs((o) => ({ ...o, [r.c.inboundId]: picked(r) }))}
                >
                  <Cb on={picked(r)} />
                  <b>{r.c.profileName}</b>
                  <span className="mono">{`${r.c.network || "tcp"}/${r.c.port} ${t("wy.wz.on", { entry: entry.name })}`}</span>
                  <em className={cx(r.warn && "warn")}>{r.why}</em>
                </button>
              ))}
            </div>
            <div className="wy-get">
              <span className="wy-sl">{t("wy.wz.s3")}</span>
              {go.length > 0 && (
                <p>
                  <i className="wy-gl ok" aria-hidden />
                  <span>
                    {people > 0
                      ? t.n("wy.wz.get.people", people, { label: `${node.countryCode} · ${viaLabel(t, entry.cc)}`, servers: t.n("wy.wz.servers", go.length, { exit: node.name }), entry: entry.name })
                      : t("wy.wz.get.nobody", { label: `${node.countryCode} · ${viaLabel(t, entry.cc)}`, entry: entry.name })}
                  </span>
                </p>
              )}
              <p>
                <i className="wy-gl ok" aria-hidden />
                <span>{t("wy.wz.get.keep")}</span>
              </p>
              {awgDevices > 0 && (
                <p>
                  <i className="wy-gl ok" aria-hidden />
                  <span>{t.n("wy.wz.get.awg", awgDevices, { via: viaLabel(t, entry.cc) })}</span>
                </p>
              )}
            </div>
            <p className="wy-nt">
              {info}
              <span>{t("wy.foot.sees", { entry: entry.name })}</span>
            </p>
            {bridge && (
              <p className="wy-nt">
                {info}
                <span>{t("wy.wz.brBlind", { exit: node.name })}</span>
              </p>
            )}
          </>
        )}
      </div>
    ),
    4: job ? <Run job={job} stopped={job.state === "stopped"} onOther={other} onTake={(id, port) => retryItem(ctx, node.id, id, String(port), told)} /> : null,
  };

  const foot =
    step === 4
      ? job?.state === "finished"
        ? job.on === job.items.length
          ? t("wy.wz.f4.done")
          : t("wy.wz.f4.part")
        : job?.state === "stopped"
          ? t("wy.wz.f4.stopped")
          : t("wy.wz.f4.run")
      : step === 3
        ? t.n("wy.wz.f3", go.length, { entry: entry.name })
        : step === 2
          ? entry.name
            ? t("wy.wz.f2", { entry: entry.name })
            : t("wy.wz.f2none")
          : t("wy.wz.f1", { node: node.name });

  return (
    <Modal
      open
      wide
      className="wy-wz relative"
      onOpenChange={(o) => !o && close()}
      closeLabel={t("common.close")}
      title={t("wy.wz.title", { country: countryIn(t, w.cc) })}
      footer={
        <>
          <span className="wy-wf">{foot}</span>
          {(step === 2 || step === 3) && (
            <Button variant="ghost" size="md" onClick={() => setStep((step - 1) as Step)}>
              {t("ways.new.back")}
            </Button>
          )}
          <Button
            ref={next}
            variant={step === 4 && running ? "secondary" : "primary"}
            size="md"
            disabled={!canNext}
            onClick={() => {
              if (step === 1) setStep(2);
              else if (step === 2 && entryNode) setStep(3);
              else if (step === 3) launch();
              else if (step === 4) close();
            }}
          >
            {step === 3 ? t("relay.dlg.checkOn") : step === 4 ? (job?.state === "finished" ? t("common.done") : t("common.close")) : t("wy.wz.next")}
          </Button>
        </>
      }
    >
      <div data-fx={fx ? "1" : undefined} data-testid="relay-wizard" data-step={step}>
        {fx && okAll && (
          <>
            <div className="wy-wv" aria-hidden>
              <i />
            </div>
            <span className="wy-eg a" aria-hidden />
            <span className="wy-eg b" aria-hidden />
          </>
        )}
        <ol className="wy-steps">
          {labels.map((l, i) => (
            <li key={l} className={cx(done(i + 1) ? "done" : step === i + 1 ? "cur" : "")} aria-current={step === i + 1 ? "step" : undefined}>
              <i>
                {tick}
                <span className="mono">{i + 1}</span>
              </i>
              {l}
            </li>
          ))}
        </ol>
        {body[step]}
      </div>
    </Modal>
  );
}

/** The last step: how many are through, and for each server what it is doing now, then what came of it. */
function Run({ job, stopped, onOther, onTake }: { job: Job; stopped: boolean; onOther: () => void; onTake: (inboundId: string, port: number) => void }) {
  const t = useT();
  const n = job.items.length;
  const over = job.state === "finished" || job.state === "stopped";
  const w = n ? Math.min(100, Math.round((job.done / n) * 100)) : 0;
  const tone = over && job.on === 0 ? "bad" : over && job.on < n ? "soon" : "";
  const { exitName: exit } = job;
  const entry = job.entry.name;
  const title = job.state === "finished" ? t("wy.wz.run.fin", { k: job.done, n }) : stopped ? t("wy.wz.run.stop", { k: job.done, n }) : t("wy.wz.run.t", { k: job.done, n });
  return (
    <div className="wy-wb" data-testid="wizard-run">
      <div className="wy-rh">
        <b>{title}</b>
        {!over && (
          <Button size="md" disabled={job.state === "stopping"} onClick={() => stopJob(job.exitId)}>
            {job.state === "stopping" ? t("wy.wiz.stopping") : t("wy.wiz.stop")}
          </Button>
        )}
      </div>
      <i className={cx("wy-pg", tone)} style={{ "--pgw": `${w}%` } as CSSProperties} role="progressbar" aria-label={title} aria-valuenow={w} aria-valuemin={0} aria-valuemax={100} />
      <div className="wy-rn">
        <span>
          <Flag code={job.exitCc} size={14} className="rounded-[3px]" />
          <b className="mono">{exit}</b>
        </span>
        <div>
          {job.items.map((it) => {
            const r = job.runs[it.c.inboundId];
            const ended = r?.tone !== undefined;
            const started = !!r && r.leg !== "waiting";
            const state = ended ? (r.tone === "ok" ? "ok" : "bad") : started ? "busy" : "none";
            const bridged = r?.bridge === "ok" || r?.bridge === "running" || r?.reach === "running" || (job.bridge && !ended);
            const said =
              state === "ok"
                ? r?.bridge === "ok"
                  ? t("wy.wz.r.okBr", { entry, exit })
                  : t("wy.wz.r.ok")
                : state === "bad"
                  ? (r?.text ?? "")
                  : state === "busy"
                    ? bridged
                      ? t("wy.wz.r.busyBr", { entry, exit })
                      : t("wy.wz.r.busy", { entry, exit })
                    : stopped
                      ? t("wy.wz.r.skip")
                      : t("wy.wz.r.wait");
            return (
              <div key={it.c.inboundId} className={state} data-testid="wizard-run-row" data-state={state}>
                <i className={cx("wy-gl", state === "busy" ? "busy" : state)} aria-hidden />
                <span>
                  <b>{it.c.profileName}:</b> {said}
                </span>
                {state === "bad" && r?.free ? (
                  <Button size="xs" disabled={!over} onClick={() => onTake(it.c.inboundId, r.free!)}>
                    {t("node.check.port.take", { port: r.free })}
                  </Button>
                ) : null}
              </div>
            );
          })}
        </div>
      </div>
      {over && job.state === "finished" && (
        <>
          <p className="wy-res">{job.on === n ? t("wy.wz.res.ok", { exit, entry }) : job.on > 0 ? t("wy.wz.res.part", { exit, k: job.on, n }) : t("wy.wz.res.bad", { exit })}</p>
          {job.on > 0 && <p className="wy-lead">{t("wy.wz.res.next")}</p>}
          {job.on < n && (
            <div>
              <Button size="md" onClick={onOther}>
                {t("nw.sv.other")}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

