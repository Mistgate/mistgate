import { useQuery } from "@tanstack/react-query";
import { useEffect, useState, type CSSProperties } from "react";
import { LogViewer } from "@/components/log-viewer";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Pending } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import {
  clockLeft,
  debugLevels,
  debugListQuery,
  debugMinutes,
  debugPage,
  debugSources,
  isLive,
  secondsLeft,
  useDebugActions,
  type DebugCapture,
  type DebugLevel,
} from "@/lib/debug-capture";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import { isUnavailable } from "@/lib/ops";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { SvCard, SvHead, SvRow } from "./server-parts";

type Node = Plain<NodeMsg>;

/** The seconds clock of a countdown, ticking once a second while `on`. */
function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const first = setTimeout(() => setNow(Date.now()), 0); // catch up when a capture starts, then tick
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => {
      clearTimeout(first);
      clearInterval(id);
    };
  }, [on]);
  return now;
}

/**
 * "Debug capture" of a node (Server tab): the owner picks the sources and the length, sees the countdown, and reads the cleaned
 * records when the node has sent them (OpsService, VPS edition). Only the owner can start, stop or read captures.
 */
export function DebugCaptureCard({ node }: { node: Node }) {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const online = agentLinked(node.status);
  const list = useQuery({ ...debugListQuery(node.id), enabled: owner });
  const { start, stop } = useDebugActions(node.id, node.name);
  const [minutes, setMinutes] = useState<number>(5);
  const [level, setLevel] = useState<DebugLevel>("debug");
  const [sources, setSources] = useState<string[]>([]);
  const [picked, setPicked] = useState("");

  const captures = list.data?.captures ?? [];
  const running = captures.find(isLive);
  const now = useNow(running !== undefined);
  const current = captures.find((c) => c.captureId === picked) ?? captures[0];
  const toggle = (s: string) => setSources((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]));

  const unavailable = list.isError && isUnavailable(list.error);
  return (
    <SvCard aria-label={t("debug.title")}>
      <SvHead title={t("debug.title")} hint={t("debug.intro")} />
      {unavailable && (
        <div className="px-6 pb-5 max-[720px]:px-4">
          <Notice tone="quiet">{t("debug.unavailable")}</Notice>
        </div>
      )}
      {!unavailable &&
        (owner ? (
          <>
            {!running && (
              <>
                <SvRow title={t("np.sv.dbg.howLong")} hint={t("np.sv.dbg.howLongHint")}>
                  <Segmented aria-label={t("debug.duration")} value={String(minutes)} onValueChange={(v) => setMinutes(Number(v))} options={debugMinutes.map((m) => ({ value: String(m), label: t("debug.minutes", { n: m }) }))} />
                </SvRow>
                <SvRow title={t("debug.level")}>
                  <Segmented aria-label={t("debug.level")} value={level} onValueChange={setLevel} options={debugLevels.map((l) => ({ value: l, label: t(`debug.level.${l}`) }))} />
                </SvRow>
                <div className="sv-body line">
                  <span className="sv-lb">{t("debug.sources")}</span>
                  <div className="flex flex-wrap gap-1.5" role="group" aria-label={t("debug.sources")}>
                    {debugSources.map((s) => (
                      <button
                        key={s}
                        type="button"
                        aria-pressed={sources.includes(s)}
                        onClick={() => toggle(s)}
                        className="h-8 cursor-pointer rounded-[16px] border border-line bg-surface px-3 font-mono text-xs text-sec transition-colors duration-150 hover:text-fg aria-pressed:border-accent-line aria-pressed:bg-accent-soft aria-pressed:text-fg"
                      >
                        {s}
                      </button>
                    ))}
                  </div>
                  <span className="text-xs text-muted">{sources.length === 0 ? t("debug.sourcesAll") : t("debug.sourcesHint")}</span>
                </div>
                <div className="sv-foot">
                  <span>{!online ? t("debug.offline") : ""}</span>
                  <Button variant="primary" size="lg" disabled={!online || start.isPending} onClick={() => start.mutate({ level, sources, minutes })}>
                    {t("debug.button", { n: minutes })}
                  </Button>
                </div>
              </>
            )}

            {running && <RunningRow capture={running} now={now} owner={owner} stopping={stop.isPending} onStop={() => stop.mutate(running.captureId)} />}

            {list.isPending && (
              <div className="sv-body line">
                <Pending />
              </div>
            )}
            {list.data && captures.length === 0 && <p className="sv-note !pt-3.5">{t("debug.none")}</p>}
            {current && !isLive(current) && <Viewer key={`${current.captureId}:${current.state}`} capture={current} captures={captures} onPick={setPicked} />}
          </>
        ) : (
          <p className="sv-note">{t("debug.ownerOnly")}</p>
        ))}
    </SvCard>
  );
}

function RunningRow({ capture, now, owner, stopping, onStop }: { capture: DebugCapture; now: number; owner: boolean; stopping: boolean; onStop: () => void }) {
  const t = useT();
  const left = secondsLeft(capture.endsUnix, now);
  const total = Math.max(1, capture.endsUnix - capture.startedUnix);
  const w = Math.min(100, Math.max(0, Math.round((1 - left / total) * 100)));
  return (
    <div className="sv-rec" role="status">
      <i className="dot" aria-hidden />
      <div>
        <b>{left > 0 ? t("debug.running", { time: clockLeft(left) }) : t("debug.shipping")}</b>
        <span className="font-mono text-xs">{capture.sources.length ? capture.sources.join(", ") : t("debug.sourcesAll")}</span>
      </div>
      {owner && (
        <Button variant="secondary" size="md" disabled={stopping} onClick={onStop}>
          {t("debug.stop")}
        </Button>
      )}
      <i className="bar" style={{ "--w": `${w}%` } as CSSProperties} aria-hidden />
    </div>
  );
}

/** The records of one finished capture: who started it and with what, and the shared reader with its filters and "More records". */
function Viewer({ capture, captures, onPick }: { capture: DebugCapture; captures: DebugCapture[]; onPick: (id: string) => void }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <div className="sv-body line">
      <div className="flex flex-wrap items-center gap-2.5">
        <h4 className="font-medium">{t("debug.records")}</h4>
        {captures.length > 1 && (
          <Select
            compact
            aria-label={t("debug.pick")}
            value={capture.captureId}
            onValueChange={onPick}
            options={captures.map((c) => ({ value: c.captureId, label: t("debug.pickLabel", { time: fmt.stamp(c.startedUnix), level: c.level, records: c.records }) }))}
          />
        )}
        <span className="text-xs text-muted">
          {t("debug.by", { actor: capture.actor || "?", sources: capture.sources.length ? capture.sources.join(", ") : t("debug.sourcesAll") })}
        </span>
      </div>
      {capture.state === "expired" && <Notice tone="warn">{t("debug.expired")}</Notice>}
      {capture.dropped > 0 && <p className="text-xs text-muted">{t("debug.dropped", { n: capture.dropped })}</p>}
      {capture.state === "done" && <LogViewer queryKey={["ops", "debug", "records", capture.captureId, capture.state]} load={(f, after) => debugPage(capture.captureId, f, after)} />}
    </div>
  );
}
