import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState, type CSSProperties } from "react";
import { CopyButton } from "@/components/copy-button";
import { Scramble } from "@/components/fx/scramble-react";
import { Chip, Card } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconChip, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Callout, Notice } from "@/components/ui/notice";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { probe } from "@/lib/api";
import { useEffectsEnabled } from "@/lib/effects";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { marksOf, minutesOf, sniScanQuery, statsOf, viewOf, type Candidate, type Mark, type Scan, type SniProblem } from "@/lib/sni-scan";
import { useNow } from "@/lib/time";
import { Spin } from "./parts";
import "./sni-scan.css";

/**
 * "Sites near the node" on the node's Profiles tab, for a REALITY server that shows another site: the node itself walks the /24 of its address on
 * port 443 and the card lists what could be the target. It is the owner's action and a thing a hoster may take for scanning, so every scan
 * starts from a window that says so, with "Cancel" chosen. The card never applies a name: the owner copies it into the REALITY server's
 * settings. The names are text from other servers' certificates: they are shown as text, never as a link or an image, and nothing here asks
 * the browser to open them. The card is not drawn for a helper, nor on the Cloudflare edition (the parent decides).
 */

/** Development only (/dev-kit): the window starts open, a scan looks to be running, a too-early scan waits until then. */
export const sniDemo: { current?: { warn?: boolean; running?: boolean; retryAfterS?: number } } = {};

const nowS = () => Math.floor(Date.now() / 1000);

/** Seconds since a scan began, ticking once a second while it goes (a scene starts it at `from` seconds). */
function useElapsed(on: boolean, from = 0) {
  const [began, setBegan] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const t0 = Date.now() - from * 1000;
    const id = setInterval(() => {
      setBegan(t0);
      setNow(Date.now());
    }, 1000);
    return () => clearInterval(id);
  }, [on, from]);
  return on && began > 0 ? Math.max(0, Math.floor((now - began) / 1000)) : from;
}

export function SniScanCard({ nodeId, nodeName, linked }: { nodeId: string; nodeName: string; linked: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const fx = useEffectsEnabled();
  const demo = sniDemo.current;
  const titleId = useId();
  const stored = useQuery(sniScanQuery(nodeId));
  const [warn, setWarn] = useState(demo?.warn ?? false);
  // the second a "too early" answer said to wait until
  const [retryUntil, setRetryUntil] = useState(demo?.retryAfterS ? nowS() + demo.retryAfterS : 0);
  const run = useMutation({
    mutationFn: () => probe.nodeSniScan({ nodeId, run: true }),
    onSuccess: (r) => {
      qc.setQueryData(sniScanQuery(nodeId).queryKey, r);
      setRetryUntil(r.error === "too_soon" ? nowS() + r.retryAfterS : 0);
    },
  });
  const running = run.isPending || !!demo?.running;
  const now = Math.floor(useNow(15_000) / 1000);
  const scan = stored.data as Scan | undefined;
  const view = viewOf(scan, { running, linked, now, retryUntil });
  const elapsed = useElapsed(running, demo?.running ? 23 : 0);

  const label = running ? t("sni.running.short") : view.waitS > 0 ? t("sni.againIn", { minutes: minutesOf(view.waitS) }) : t(view.kept ? "sni.again" : "sni.find");
  return (
    <Card className="ss">
      <section className="ss-body" aria-labelledby={titleId} data-testid="sni-card" data-phase={view.phase} data-fx={fx ? "" : undefined}>
        <div className="ss-h">
          <IconChip icon="network" tone="sage" size={36} />
          <div className="ss-hd">
            <h3 id={titleId}>{t("sni.title")}</h3>
            <p>{t("sni.lead")}</p>
          </div>
          <Button type="button" variant={view.kept ? "secondary" : "primary"} size="lg" disabled={!view.canRun} aria-busy={running} onClick={() => setWarn(true)}>
            {running && <Spin />}
            {label}
          </Button>
        </div>
        {view.problem && (
          <div className="ss-n">
            <Problem t={t} problem={view.problem} minutes={minutesOf(view.waitS)} />
          </div>
        )}
        {run.error && (
          <div className="ss-n">
            <Notice tone="danger">{errorText(run.error, t)}</Notice>
          </div>
        )}
        {stored.isPending && !scan ? null : stored.error && !scan ? (
          <div className="ss-n">
            <Notice tone="danger">{errorText(stored.error, t)}</Notice>
          </div>
        ) : view.phase === "running" ? (
          <Running t={t} elapsed={elapsed} />
        ) : view.phase === "never" ? (
          <p className="ss-none">{t("sni.none")}</p>
        ) : (
          scan && <Result t={t} fmt={fmt} scan={scan} phase={view.phase} />
        )}
      </section>
      <Modal open={warn} onOpenChange={setWarn} title={t("sni.warn.title", { name: nodeName })} shot="sni-scan-warning"
        footer={
          <>
            <Button type="button" variant="secondary" autoFocus onClick={() => setWarn(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="button"
              variant="primary"
              onClick={() => {
                setWarn(false);
                run.mutate();
              }}
            >
              {t("sni.warn.go")}
            </Button>
          </>
        }
      >
        <Callout kind="warn">{t("sni.warn.body")}</Callout>
      </Modal>
    </Card>
  );
}

function Problem({ t, problem, minutes }: { t: T; problem: SniProblem; minutes: number }) {
  const tone = problem === "node_offline" ? "quiet" : problem === "too_soon" || problem === "busy" ? "warn" : "danger";
  return <Notice tone={tone}>{t(`sni.err.${problem}` as MessageKey, { minutes })}</Notice>;
}

function Running({ t, elapsed }: { t: T; elapsed: number }) {
  return (
    <div className="ss-run" role="status">
      <div className="ss-run-l">
        <b>{t("sni.running")}</b>
        <span className="mono">{t("sni.elapsed", { n: elapsed })}</span>
      </div>
      <div className="ss-bar" aria-hidden>
        <i />
      </div>
      <small>{t("sni.running.hint")}</small>
    </div>
  );
}

function Result({ t, fmt, scan, phase }: { t: T; fmt: ReturnType<typeof useFmt>; scan: Scan; phase: "result" | "partial" | "empty" }) {
  const stats = statsOf(scan);
  return (
    <div className="ss-res">
      <p className="ss-meta">
        <span>{t("sni.when", { when: fmt.ago(Number(scan.atUnix)) })}</span>
        {scan.range && <span className="mono">{t("sni.range", { range: scan.range })}</span>}
      </p>
      <dl className="ss-stats">
        {stats.map((s) => (
          <div key={s.id} className={s.id === "valid" ? "hot" : undefined}>
            <dd className="mono">
              <Scramble value={String(s.n)} once />
            </dd>
            <dt>{t(`sni.stat.${s.id}` as MessageKey)}</dt>
          </div>
        ))}
      </dl>
      {phase === "partial" && <Notice tone="quiet">{t("sni.partial")}</Notice>}
      {phase === "empty" && <Notice tone="quiet">{t("sni.empty", { range: scan.range })}</Notice>}
      {scan.candidates.length > 0 && (
        <>
          <h4 className="ss-found">{t("sni.found")}</h4>
          <p className="ss-how">{t("sni.how")}</p>
          <ul className="ss-list">
            {scan.candidates.map((c, i) => (
              <Row key={`${c.ip}-${c.name}`} t={t} c={c} i={i} />
            ))}
          </ul>
        </>
      )}
      {(scan.candidates.length > 0 || scan.ownLeftOut > 0) && (
        <div className="ss-foot">
          <p>{t("sni.names")}</p>
          {scan.ownLeftOut > 0 && <p>{t("sni.leftOut", { n: scan.ownLeftOut })}</p>}
        </div>
      )}
    </div>
  );
}

const chipTone: Record<NonNullable<Mark["tone"]>, Tone | undefined> = { good: "sage", warn: "sand", plain: undefined };

/** One neighbour: its name and address as plain text (never a link or an image: the name is a stranger's) with its labels and "Copy". */
function Row({ t, c, i }: { t: T; c: Candidate; i: number }) {
  const marks = marksOf(c);
  return (
    <li className="ss-row" style={{ "--i": Math.min(i, 10) } as CSSProperties}>
      <div className="ss-name">
        <b className="mono" dir="ltr" translate="no">
          {c.name}
        </b>
        <span className="mono ss-ip" dir="ltr">
          {c.ip}
        </span>
      </div>
      <div className="ss-marks">
        {marks.map((m) => (
          <Chip key={m.id} tone={chipTone[m.tone ?? "plain"]} title={m.hint ? t(m.hint as MessageKey) : undefined} mono={m.id === "ms" || m.id === "http"}>
            {t(m.key as MessageKey, m.vars)}
          </Chip>
        ))}
      </div>
      <div className="ss-cp">
        <CopyButton value={c.name} label={t("sni.copy")} size="md" iconOnly />
      </div>
    </li>
  );
}
