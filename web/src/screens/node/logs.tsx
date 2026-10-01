import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { LogLevel, type GetNodeResponse, type LogLine } from "@/gen/mistgate/admin/v1/node_pb";
import { plain } from "@/lib/plain";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";

const maxLines = 1000;
type Link = "connecting" | "live" | "lost";
type Level = "all" | "info" | "warn" | "error";

type Line = Plain<LogLine>;
const levelWord = (l: Line["level"]) => (l === LogLevel.ERROR ? "ERROR" : l === LogLevel.WARNING ? "WARN" : "INFO");
const levelMatches = (f: Level, l: Line["level"]) =>
  f === "all" || (f === "error" ? l === LogLevel.ERROR : f === "warn" ? l === LogLevel.WARNING : l !== LogLevel.ERROR && l !== LogLevel.WARNING);

const levelTone: Record<string, string> = { INFO: "text-accent-text", WARN: "text-warn", ERROR: "text-danger" };

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
export function LogsTab({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const node = data.node!;
  const online = agentLinked(node.status); // "no traffic" still has the agent on the line, and its logs are what tells why
  const [lines, setLines] = useState<Line[]>([]);
  const [link, setLink] = useState<Link>("connecting");
  const [frozen, setFrozen] = useState<Line[] | null>(null); // paused: what was on screen when Pause was pressed
  const [level, setLevel] = useState<Level>("all");
  const [query, setQuery] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    if (!online) return;
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
  }, [node.id, online]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (frozen ?? lines).filter((l) => levelMatches(level, l.level) && (!q || l.message.toLowerCase().includes(q) || l.source.toLowerCase().includes(q)));
  }, [frozen, lines, level, query]);

  // follow the tail unless the admin scrolled up to read
  useEffect(() => {
    const el = box.current;
    if (el && shown.length > 0 && stick.current && !frozen) el.scrollTop = el.scrollHeight;
  }, [shown, frozen]);

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
  const dot = status === "live" ? "bg-ok animate-[mg-ui-blink_1.2s_infinite]" : status === "paused" || status === "connecting" ? "bg-warn" : "bg-muted";

  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <FilterChips
          aria-label={t("node.logs.level")}
          value={level}
          onValueChange={setLevel}
          options={[
            { value: "all", label: t("node.logs.all") },
            { value: "info", label: t("node.logs.info") },
            { value: "warn", label: t("node.logs.warn") },
            { value: "error", label: t("node.logs.errors") },
          ]}
          className="flex-nowrap gap-1"
        />
        <TextField
          search
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("node.logs.search")}
          aria-label={t("node.logs.search")}
          className="min-w-40 flex-1"
        />
        <Button variant="secondary" size="md" onClick={() => setFrozen(paused ? null : lines)}>
          {paused ? t("node.logs.resume") : t("node.logs.pause")}
        </Button>
        <Button variant="ghost" size="md" disabled={lines.length === 0} onClick={download}>
          {t("node.logs.download")}
        </Button>
      </div>
      <div role="status" className="flex items-center gap-2 text-xs text-muted">
        <span aria-hidden className={cx("size-[7px] rounded-full", dot)} />
        {liveText}
      </div>
      <div
        ref={box}
        tabIndex={0}
        aria-label={t("node.tab.logs")}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
        className="h-[420px] overflow-auto rounded-[14px] border border-line bg-surface py-2.5 font-mono text-xs leading-[1.6] md:h-[380px]"
      >
        {shown.map((l, i) => {
          const w = levelWord(l.level);
          return (
            <div
              key={`${l.timeUnixMs}/${i}`}
              className={cx("flex gap-2.5 px-3.5 whitespace-nowrap", w === "ERROR" ? "bg-danger-soft" : w === "WARN" ? "bg-warn-soft" : "")}
            >
              <span className="text-faint">{new Date(l.timeUnixMs).toLocaleTimeString(fmt.lang, { hour12: false })}</span>
              <span className={cx("w-10 flex-none font-bold", levelTone[w])}>{w}</span>
              <span className="w-[72px] flex-none truncate text-muted">{l.source}</span>
              <span>{l.message}</span>
            </div>
          );
        })}
        {shown.length === 0 && <p className="px-3.5 text-muted">{online ? t("node.logs.empty") : t("node.logs.offlineEmpty")}</p>}
      </div>
    </div>
  );
}
