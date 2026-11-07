import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Notice } from "@/components/ui/notice";
import { Pending } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import {
  clockLeft,
  debugLevels,
  debugListQuery,
  debugMinutes,
  debugPage,
  debugRecordsQuery,
  debugSources,
  defaultDebugFilter,
  isLive,
  secondsLeft,
  useDebugActions,
  type DebugCapture,
  type DebugFilter,
  type DebugLevel,
  type DebugRecords,
} from "@/lib/debug-capture";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import { isUnavailable } from "@/lib/ops";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";

type Node = Plain<NodeMsg>;
type Record = DebugRecords["records"][number];

const levelTone: { [level: string]: string } = { debug: "text-faint", info: "text-accent-text", warn: "text-warn", error: "text-danger" };

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
 * records when the node has sent them (OpsService, VPS edition). Everyone who reaches the page reads the captures; only the owner
 * starts or stops one.
 */
export function DebugCaptureCard({ node }: { node: Node }) {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const online = agentLinked(node.status);
  const list = useQuery(debugListQuery(node.id));
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

  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
      <SectionLabel as="h2" icon="pulse" tone="rose">
        {t("debug.title")}
      </SectionLabel>
      <p className="text-[13px] leading-snug text-pretty text-muted">{t("debug.intro")}</p>
      {list.isError && isUnavailable(list.error) && <Notice tone="quiet">{t("debug.unavailable")}</Notice>}
      {!(list.isError && isUnavailable(list.error)) && (
        <>
          {owner ? (
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("debug.sources")}</span>
                <div className="flex flex-wrap gap-1.5" role="group" aria-label={t("debug.sources")}>
                  {debugSources.map((s) => (
                    <button
                      key={s}
                      type="button"
                      aria-pressed={sources.includes(s)}
                      onClick={() => toggle(s)}
                      className="h-[30px] cursor-pointer rounded-[15px] border border-line bg-surface px-3 font-mono text-xs font-bold text-muted transition-colors duration-200 aria-pressed:border-accent-line aria-pressed:bg-accent-soft aria-pressed:text-fg"
                    >
                      {s}
                    </button>
                  ))}
                </div>
                <span className="text-xs text-muted">{sources.length === 0 ? t("debug.sourcesAll") : t("debug.sourcesHint")}</span>
              </div>
              <div className="flex flex-wrap items-end gap-3">
                <div className="flex flex-col gap-1.5">
                  <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("debug.level")}</span>
                  <Segmented
                    aria-label={t("debug.level")}
                    value={level}
                    onValueChange={setLevel}
                    options={debugLevels.map((l) => ({ value: l, label: t(`debug.level.${l}`) }))}
                  />
                </div>
                <div className="flex flex-col gap-1.5">
                  <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("debug.duration")}</span>
                  <Segmented
                    aria-label={t("debug.duration")}
                    value={String(minutes)}
                    onValueChange={(v) => setMinutes(Number(v))}
                    options={debugMinutes.map((m) => ({ value: String(m), label: t("debug.minutes", { n: m }) }))}
                  />
                </div>
                <Button
                  variant="primary"
                  size="md"
                  disabled={!online || running !== undefined || start.isPending}
                  onClick={() => start.mutate({ level, sources, minutes })}
                >
                  <span>{t("debug.button", { n: minutes })}</span>
                </Button>
              </div>
              {!online && <p className="text-xs text-muted">{t("debug.offline")}</p>}
            </div>
          ) : (
            <p className="text-xs text-muted">{t("debug.ownerOnly")}</p>
          )}

          {running && <RunningRow capture={running} now={now} owner={owner} stopping={stop.isPending} onStop={() => stop.mutate(running.captureId)} />}

          {list.isPending && <Pending />}
          {list.data && captures.length === 0 && <p className="text-[13px] text-muted">{t("debug.none")}</p>}
          {current && !isLive(current) && <Viewer key={`${current.captureId}:${current.state}`} capture={current} captures={captures} onPick={setPicked} />}
        </>
      )}
    </section>
  );
}

function RunningRow({ capture, now, owner, stopping, onStop }: { capture: DebugCapture; now: number; owner: boolean; stopping: boolean; onStop: () => void }) {
  const t = useT();
  const left = secondsLeft(capture.endsUnix, now);
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-field border border-accent-line bg-accent-soft px-3.5 py-2.5" role="status">
      <span className="min-w-0 flex-1 text-[13px] font-semibold">
        {left > 0 ? t("debug.running", { time: clockLeft(left) }) : t("debug.shipping")}
        <span className="ms-2 font-mono text-xs font-normal text-muted">{capture.sources.length ? capture.sources.join(", ") : t("debug.sourcesAll")}</span>
      </span>
      {owner && (
        <Button variant="secondary" size="md" disabled={stopping} onClick={onStop}>
          <span>{t("debug.stop")}</span>
        </Button>
      )}
    </div>
  );
}

const stampOf = (fmt: ReturnType<typeof useFmt>, ms: number) => `${fmt.clock(Math.floor(ms / 1000))}.${String(ms % 1000).padStart(3, "0")}`;

/** The records of one finished capture with their filters; "More records" walks the cursor. */
function Viewer({ capture, captures, onPick }: { capture: DebugCapture; captures: DebugCapture[]; onPick: (id: string) => void }) {
  const t = useT();
  const fmt = useFmt();
  const [filter, setFilter] = useState<DebugFilter>(defaultDebugFilter);
  const [draft, setDraft] = useState("");
  // the pages after the first, for the filter they were read with: a new filter starts over without an effect
  const [paged, setPaged] = useState<{ filter: DebugFilter; extra: Record[]; cursor: number | null }>({ filter, extra: [], cursor: null });
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    const id = setTimeout(() => setFilter((f) => (f.query === draft.trim() ? f : { ...f, query: draft.trim() })), 300);
    return () => clearTimeout(id);
  }, [draft]);
  const q = useQuery({ ...debugRecordsQuery(capture.captureId, filter, capture.state), enabled: capture.state === "done" });
  const first = q.data;
  const pages = paged.filter === filter ? paged : { filter, extra: [], cursor: null }; // null cursor: the first page's own
  const next = pages.cursor ?? first?.nextCursor ?? 0;
  const records = [...(first?.records ?? []), ...pages.extra];

  async function more() {
    setLoading(true);
    try {
      const page = await debugPage(capture.captureId, filter, next);
      setPaged({ filter, extra: [...pages.extra, ...page.records], cursor: page.nextCursor });
    } finally {
      setLoading(false);
    }
  }

  const sources = first?.sources ?? [];
  return (
    <div className="flex flex-col gap-2.5 border-t border-line pt-3">
      <div className="flex flex-wrap items-center gap-2.5">
        <SectionLabel as="h3">{t("debug.records")}</SectionLabel>
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
      {capture.state === "done" && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <FilterChips
              aria-label={t("debug.filter.level")}
              value={filter.minLevel || "all"}
              onValueChange={(v) => setFilter((f) => ({ ...f, minLevel: v === "all" ? "" : (v as DebugFilter["minLevel"]) }))}
              options={[
                { value: "all", label: t("debug.filter.all") },
                { value: "info", label: t("debug.filter.info") },
                { value: "warn", label: t("debug.filter.warn") },
                { value: "error", label: t("debug.filter.error") },
              ]}
              className="flex-nowrap gap-1"
            />
            <Select
              compact
              aria-label={t("debug.filter.source")}
              value={filter.source || "any"}
              onValueChange={(v) => setFilter((f) => ({ ...f, source: v === "any" ? "" : v }))}
              options={[{ value: "any", label: t("debug.filter.anySource") }, ...sources.map((s) => ({ value: s, label: s }))]}
            />
            <TextField
              search
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder={t("debug.filter.search")}
              aria-label={t("debug.filter.search")}
              className="min-w-40 flex-1"
            />
          </div>
          <div className={cx("overflow-hidden rounded-[14px] border border-line bg-surface", q.isPlaceholderData && "opacity-60")} aria-label={t("debug.records")}>
            {records.map((r) => (
              <div key={r.seq} className="flex flex-col gap-0.5 border-b border-line px-3.5 py-2 last:border-b-0">
                <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 text-xs">
                  <span className="text-faint">{stampOf(fmt, r.timeUnixMs)}</span>
                  <span className={cx("font-mono font-bold uppercase", levelTone[r.level] ?? "text-muted")}>{r.level}</span>
                  <span className="font-mono text-muted">{r.source}</span>
                </div>
                <p className="font-mono text-xs break-words">{r.message}</p>
                {Object.keys(r.attrs).length > 0 && (
                  <p className="font-mono text-[11px] break-words text-muted">
                    {Object.entries(r.attrs)
                      .sort(([a], [b]) => a.localeCompare(b))
                      .map(([k, v]) => `${k}=${v}`)
                      .join("  ")}
                  </p>
                )}
              </div>
            ))}
            {q.isPending && <Pending />}
            {first && records.length === 0 && <p className="px-3.5 py-3 text-muted">{t("debug.empty")}</p>}
          </div>
          {next > 0 && (
            <div>
              <Button variant="secondary" size="md" disabled={loading} onClick={() => void more()}>
                <span>{t("debug.more")}</span>
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
