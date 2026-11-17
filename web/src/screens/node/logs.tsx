import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Segmented } from "@/components/ui/segmented";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { LogLevel, type GetNodeResponse, type LogLine } from "@/gen/mistgate/admin/v1/node_pb";
import { plain } from "@/lib/plain";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import "./history.css";

const maxLines = 1000;
type Link = "connecting" | "live" | "lost";
type Level = "all" | "info" | "warn" | "error";

type Line = Plain<LogLine>;
const levelWord = (l: Line["level"]) => (l === LogLevel.ERROR ? "ERROR" : l === LogLevel.WARNING ? "WARN" : "INFO");
const levelMatches = (f: Level, l: Line["level"]) =>
  f === "all" || (f === "error" ? l === LogLevel.ERROR : f === "warn" ? l === LogLevel.WARNING : l !== LogLevel.ERROR && l !== LogLevel.WARNING);


function sleep(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    const id = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => (clearTimeout(id), resolve()), { once: true });
  });
}

/**
 * Live tail of the agent and its engines, relayed by the panel (NodeService.StreamLogs). The last 200 lines
 * arrive first, then new ones as they are written; if the link drops the stream is retried every 5 s while
 * the node is online, and the lines already on screen stay.
 */
export function LogsTab({ data, onJournal, lead }: { data: Plain<GetNodeResponse>; onJournal?: () => void; lead?: ReactNode }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const node = data.node!;
  const off = useEdition().off("agent_logs"); // the edition does not relay the agent's log: no stream to open
  const online = agentLinked(node.status); // "no traffic" still has the agent on the line, and its logs are what tells why
  const [lines, setLines] = useState<Line[]>([]);
  const [link, setLink] = useState<Link>("connecting");
  const [frozen, setFrozen] = useState<Line[] | null>(null); // paused: what was on screen when Pause was pressed
  const [level, setLevel] = useState<Level>("all");
  const [query, setQuery] = useState("");
  const [source, setSource] = useState("");
  const [copied, setCopied] = useState(false);
  // lines that come after the page opened are the fresh ones: they flash in
  const [openedMs] = useState(() => Date.now());
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    if (!online || off) return;
    const ac = new AbortController();
    void (async () => {
      while (!ac.signal.aborted) {
        let first = true;
        try {
          setLink("connecting");
          const stream = nodesApi.streamLogs({ nodeId: node.id, tailLines: 200, follow: true }, { signal: ac.signal });
          for await (const raw of stream) {
            const chunk = plain(raw);
            if (chunk.lines.length > 0) {
              const add = chunk.lines;
              const replace = first; // a fresh tail replaces what a dropped stream left
              setLines((prev) => (replace ? add : [...prev, ...add]).slice(-maxLines));
              first = false;
            }
            setLink(chunk.error ? "lost" : "live");
            if (chunk.eof || chunk.error) break;
          }
        } catch {
          if (ac.signal.aborted) return;
        }
        if (ac.signal.aborted) return;
        setLink("lost");
        await sleep(5000, ac.signal);
      }
    })();
    return () => ac.abort();
  }, [node.id, online, off]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (frozen ?? lines).filter((l) => levelMatches(level, l.level) && (!source || l.source === source) && (!q || l.message.toLowerCase().includes(q) || l.source.toLowerCase().includes(q)));
  }, [frozen, lines, level, query, source]);
  const sources = useMemo(() => [...new Set(lines.map((l) => l.source))].sort(), [lines]);

  // follow the tail unless the admin scrolled up to read
  useEffect(() => {
    const el = box.current;
    if (el && shown.length > 0 && stick.current && !frozen) el.scrollTop = el.scrollHeight;
  }, [shown, frozen]);

  if (off)
    return (
      <div className="nh">
        {lead && <div className="nh-bar">{lead}</div>}
        <EditionNotice feature="agent_logs">
          <p>{t("journal.edgeHint")}</p>
          {onJournal && (
            <Button variant="secondary" size="md" className="mt-2" onClick={onJournal}>
              {t("journal.open")}
            </Button>
          )}
        </EditionNotice>
      </div>
    );

  function download() {
    const text = lines
      .map((l) => `${new Date(l.timeUnixMs).toISOString()} ${levelWord(l.level).padEnd(5)} ${l.source} ${l.message}`)
      .join("\n");
    const url = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${node.name}-agent.log`;
    a.click();
    URL.revokeObjectURL(url);
    toast(t("node.logs.downloaded", { name: `${node.name}-agent.log` }));
  }

  async function copy() {
    const text = shown.map((l) => `${new Date(l.timeUnixMs).toISOString()} ${levelWord(l.level).padEnd(5)} ${l.source} ${l.message}`).join("\n");
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    } catch {
      toast(t("common.copyFailed"));
    }
  }

  const paused = frozen !== null;
  const status = !online ? "off" : paused ? "paused" : link;
  const last = lines.at(-1);
  const liveText = {
    off: last ? t("node.logs.noLinkSince", { time: fmt.clock(last.timeUnixMs / 1000) }) : t("node.logs.noLink"),
    paused: t("node.logs.paused"),
    connecting: t("node.logs.connecting"),
    live: t("node.logs.live"),
    lost: last ? t("node.logs.noLinkSince", { time: fmt.clock(last.timeUnixMs / 1000) }) : t("node.logs.noLink"),
  }[status];
  const following = status === "live";

  return (
    <div className="nh">
      <div className="nh-bar">
        {lead}
        {sources.length > 1 && (
          <FilterChips
            aria-label={t("np.hi.log.source")}
            value={source}
            onValueChange={setSource}
            options={[{ value: "", label: t("np.hi.log.allSources") }, ...sources.map((s) => ({ value: s, label: s }))]}
            className="nh-chips flex-nowrap"
          />
        )}
        <TextField search value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("node.logs.search")} aria-label={t("node.logs.search")} className="min-w-40 max-w-72 flex-1" />
        <button type="button" className={cx("nh-tail", following && "on")} aria-pressed={!paused} disabled={!online} onClick={() => setFrozen(paused ? null : lines)}>
          <i className="dot" aria-hidden />
          {paused ? t("node.logs.resume") : t("node.logs.pause")}
        </button>
      </div>
      <section className="nh-log rounded-card border border-line bg-surface">
        <div className="nh-lh">
          <span className="mono">
            {node.name}-agent.log
            <small role="status">{liveText}</small>
          </span>
          <Segmented
            aria-label={t("node.logs.level")}
            value={level}
            onValueChange={setLevel}
            options={[
              { value: "all", label: t("node.logs.all") },
              { value: "info", label: t("node.logs.info") },
              { value: "warn", label: t("node.logs.warn") },
              { value: "error", label: t("node.logs.errors") },
            ]}
          />
          <Button variant="secondary" size="md" disabled={shown.length === 0} onClick={() => void copy()}>
            {copied ? t("np.hi.log.copied") : t("np.hi.log.copy")}
          </Button>
          <Button variant="ghost" size="md" disabled={lines.length === 0} onClick={download}>
            {t("node.logs.download")}
          </Button>
        </div>
        <div
          ref={box}
          role="log"
          tabIndex={0}
          aria-label={t("node.tab.logs")}
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          }}
          className="nh-lb"
        >
          {shown.map((l, i) => {
            const w = levelWord(l.level);
            return (
              <div key={`${l.timeUnixMs}/${i}`} className={cx(w === "ERROR" ? "crit" : w === "WARN" ? "warn" : "", l.timeUnixMs > openedMs && "fresh")}>
                <span className="t">{new Date(l.timeUnixMs).toLocaleTimeString(fmt.locale, { hour12: false })}</span>
                <span className="lv">{w}</span>
                <span className="src truncate">{l.source}</span>
                <span className="m">{l.message}</span>
              </div>
            );
          })}
          {shown.length === 0 && <span className="none">{online ? t("node.logs.empty") : t("node.logs.offlineEmpty")}</span>}
          {following && (
            <span className="nh-cur" aria-hidden>
              <i />
            </span>
          )}
        </div>
      </section>
    </div>
  );
}
