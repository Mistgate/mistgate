import { useEffect, useState, type ReactNode } from "react";
import { Icon, type IconName } from "@/components/ui/icons";
import { useT } from "@/i18n";
import type { LiveMsg, LiveTool } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";

// A turn's working, shown the way coding assistants do: a "Thinking for 6s" header over what the model said between its
// tool rounds, then one compact card per tool call (icon by kind, name, the telling argument, time and state). A card opens
// to its input (the arguments, coloured) and its output (what came back in short: a count, or the error).

/** The icon for a family of tools (names are "<area>_<what>", plans end in "_plan" or "_apply"). */
export function toolIcon(tool: string): IconName {
  if (/_(plan|apply)$/.test(tool)) return "sliders";
  if (tool.startsWith("memory_")) return "bookmark";
  if (tool.startsWith("conversation_")) return "chat";
  if (/journal|events|log/.test(tool)) return "list";
  if (/alert|health/.test(tool)) return "bell";
  if (tool.startsWith("user") || tool.startsWith("device") || tool.startsWith("group")) return "person";
  if (tool.startsWith("fleet") || tool.startsWith("node") || tool.startsWith("read_node")) return "server";
  if (/search|find/.test(tool)) return "search";
  if (/profile|config|settings/.test(tool)) return "file";
  return "code";
}

const preferred = ["query", "q", "node", "user", "name", "id", "profile", "group", "device", "hours", "min_level"];

/** The most telling value(s) of the arguments, in a few words: a query, a node, a user; "" when there is none. */
export function argSummary(argsJson: string): string {
  let v: unknown;
  try {
    v = JSON.parse(argsJson);
  } catch {
    return "";
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return "";
  const o = v as Record<string, unknown>;
  const scalar = (k: string) => (typeof o[k] === "string" || typeof o[k] === "number" ? String(o[k]) : "");
  const first = preferred.map(scalar).filter(Boolean);
  const rest = Object.keys(o).map(scalar).filter(Boolean);
  return [...new Set(first.length ? first : rest)].slice(0, 2).join(" · ");
}

/** The input as one coloured line (keys, strings, numbers, words), the way the arguments were sent. Left to right in any language. */
export function Json({ text }: { text: string }) {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return <>{text}</>;
  }
  const punct = (s: string) => <span className="text-muted">{s}</span>;
  const walk = (v: unknown): ReactNode => {
    if (typeof v === "string") return <span className="text-(--json-str)">{JSON.stringify(v)}</span>;
    if (typeof v === "number") return <span className="text-(--json-num)">{v}</span>;
    if (typeof v === "boolean" || v === null) return <span className="text-(--json-lit)">{String(v)}</span>;
    if (Array.isArray(v))
      return (
        <>
          {punct("[")}
          {v.map((x, i) => (
            <span key={i}>
              {i > 0 && punct(", ")}
              {walk(x)}
            </span>
          ))}
          {punct("]")}
        </>
      );
    const entries = Object.entries(v as Record<string, unknown>);
    return (
      <>
        {punct("{")}
        {entries.map(([k, x], i) => (
          <span key={k}>
            {i > 0 && punct(", ")}
            <span className="text-(--json-key)">{JSON.stringify(k)}</span>
            {punct(": ")}
            {walk(x)}
          </span>
        ))}
        {punct("}")}
      </>
    );
  };
  return <>{walk(parsed)}</>;
}

/** "1.4s" under ten seconds, "12s" above, "1m 5s" over a minute; in the language's own unit. */
function useDuration() {
  const fmt = useFmt();
  return (ms: number) => {
    const sec = (n: number, digits: number) => new Intl.NumberFormat(fmt.locale, { style: "unit", unit: "second", unitDisplay: "narrow", maximumFractionDigits: digits }).format(n);
    if (ms < 10_000) return sec(Math.max(0.1, Math.round(ms / 100) / 10), 1);
    const s = Math.round(ms / 1000);
    if (s < 60) return sec(s, 0);
    return `${new Intl.NumberFormat(fmt.locale, { style: "unit", unit: "minute", unitDisplay: "narrow" }).format(Math.floor(s / 60))} ${sec(s % 60, 0)}`;
  };
}

/** Smooth open and close: the body is always there, in a grid row that grows from 0 to its height. */
function Collapse({ open, children }: { open: boolean; children: ReactNode }) {
  return (
    <div className={cx("grid transition-[grid-template-rows,opacity] duration-200 ease-out", open ? "grid-rows-[1fr] opacity-100" : "grid-rows-[0fr] opacity-0")} aria-hidden={!open} inert={!open}>
      <div className="min-h-0 overflow-hidden">{children}</div>
    </div>
  );
}

function Status({ tool }: { tool: LiveTool }) {
  if (!tool.done) return <span aria-hidden className="size-3.5 animate-spin rounded-full border-2 border-faint border-t-accent" />;
  return (
    <svg aria-hidden viewBox="0 0 16 16" className={cx("size-4 flex-none", tool.ok ? "text-ok" : "text-danger")}>
      <circle cx="8" cy="8" r="7" fill="currentColor" />
      <path d={tool.ok ? "M4.8 8.3l2.2 2.2 4.2-4.6" : "M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8"} fill="none" stroke="var(--ink-on-accent)" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-[11px] text-muted">{label}</span>
      <div dir="ltr" className="overflow-x-auto rounded-[10px] bg-canvas px-3 py-2 font-mono text-[11.5px] leading-normal whitespace-nowrap [scrollbar-width:thin]">
        {children}
      </div>
    </div>
  );
}

export function ToolCard({ tool }: { tool: LiveTool }) {
  const t = useT();
  const dur = useDuration();
  const [open, setOpen] = useState(false);
  const bad = tool.done && !tool.ok;
  const summary = argSummary(tool.argsJson);
  return (
    <div data-slot="tool" data-state={!tool.done ? "running" : bad ? "failed" : "ok"} className={cx("rounded-[10px] border transition-colors", bad ? "border-danger/45 bg-surface-2/50" : "border-line bg-surface-2/50")}>
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="flex w-full min-w-0 items-center gap-2.5 rounded-[10px] px-3 py-2 text-start">
        <Icon name={toolIcon(tool.tool)} size={14} className="flex-none text-muted" />
        <code dir="ltr" className="flex-none font-mono text-[12px] font-medium text-fg">
          {tool.tool}
        </code>
        {summary && (
          <span dir="auto" className="min-w-0 flex-1 truncate text-xs text-muted">
            {summary}
          </span>
        )}
        {!summary && <span className="flex-1" />}
        {tool.ms !== undefined && <span className="flex-none text-[11px] text-muted tabular-nums">{dur(tool.ms)}</span>}
        <Status tool={tool} />
        <Icon name="chevronDown" size={13} className={cx("flex-none text-muted transition-transform duration-200", open && "rotate-180")} />
        <span className="sr-only">{t("ai.tool.done", { tool: tool.tool })}</span>
      </button>
      <Collapse open={open}>
        <div className="flex flex-col gap-2.5 px-3 pt-0.5 pb-3">
          {tool.argsJson && (
            <Labeled label={t("ai.tool.input")}>
              <Json text={tool.argsJson} />
            </Labeled>
          )}
          <Labeled label={t("ai.tool.output")}>
            {!tool.done ? (
              <span className="text-muted">{t("ai.tool.running2")}</span>
            ) : bad ? (
              <span className="text-danger-text">{tool.error || "—"}</span>
            ) : (
              <span className="text-muted">{tool.count > 0 ? t.n("ai.tool.results", tool.count) : t("ai.tool.ok")}</span>
            )}
          </Labeled>
        </div>
      </Collapse>
    </div>
  );
}

/** The cards of a turn; past five, only the last three with a muted "+N earlier" above them. */
export function ToolCards({ tools }: { tools: LiveTool[] }) {
  const t = useT();
  const [all, setAll] = useState(false);
  const fold = tools.length > 5 && !all;
  const shown = fold ? tools.slice(-3) : tools;
  return (
    <div data-slot="tools" className="flex w-full flex-col gap-1.5">
      {fold && (
        <button type="button" onClick={() => setAll(true)} className="self-start px-1 text-xs text-muted transition-colors hover:text-fg">
          {t.n("ai.tool.earlier", tools.length - 3)}
        </button>
      )}
      {shown.map((tool) => (
        <ToolCard key={tool.id} tool={tool} />
      ))}
    </div>
  );
}

/** Seconds since `t0`, ticking while `live`. */
function useElapsed(t0: number | undefined, live: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live || t0 === undefined) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [live, t0]);
  return t0 === undefined ? undefined : Math.max(0, now - t0);
}

/** "Thinking for 6s" while the turn works, "Thought for 6s" after; opens to what the model said between tool rounds (no chevron when it said nothing). */
export function Thinking({ m, running, interim }: { m: LiveMsg; running: boolean; interim: string }) {
  const t = useT();
  const fmt = useFmt();
  const [open, setOpen] = useState<boolean | null>(null);
  const live = running && m.workedMs === undefined;
  const elapsed = useElapsed(m.t0, live);
  const ms = m.workedMs ?? elapsed;
  if (ms === undefined) return null;
  const secs = new Intl.NumberFormat(fmt.locale, { style: "unit", unit: "second", unitDisplay: "narrow", maximumFractionDigits: 0 }).format(Math.round(ms / 1000));
  const [before, after] = t(live ? "ai.think.running" : "ai.think.done", { s: "\u0000" }).split("\u0000");
  const label = (
    <>
      {before}
      <b className="font-semibold text-fg">{secs}</b>
      {after}
    </>
  );
  const expandable = interim.length > 0;
  const isOpen = expandable && (open ?? live);
  return (
    <div data-slot="thinking" className="flex w-full flex-col gap-1.5">
      {expandable ? (
        <button type="button" onClick={() => setOpen(!isOpen)} aria-expanded={isOpen} className="flex items-center gap-1 self-start px-1 text-xs text-muted transition-colors hover:text-fg">
          {label}
          <Icon name="chevronDown" size={12} className={cx("transition-transform duration-200", isOpen && "rotate-180")} />
        </button>
      ) : (
        <span className="px-1 text-xs text-muted">{label}</span>
      )}
      {expandable && (
        <Collapse open={isOpen}>
          <div className="ms-1 flex flex-col gap-1.5 border-s-2 border-line ps-3 text-[13px] leading-relaxed text-muted">
            {interim.split(/\n{2,}/).map((p, i) => (
              <p key={i} className="whitespace-pre-wrap break-words">
                {p}
              </p>
            ))}
          </div>
        </Collapse>
      )}
    </div>
  );
}
