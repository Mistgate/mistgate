import { useEffect, useState, type ReactNode } from "react";
import { Icon, type IconName } from "@/components/ui/icons";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { LiveMsg, LiveTool } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { I } from "./as-icons";

// A turn's working as the board draws it (.as-rs, .as-tc): a "Thinking for 6s" line over what the model said between its tool rounds, and ONE
// bordered group for the tool calls: a summary line ("Used 5 tools · 2 failed · 7s") that opens to a row per call (friendly name, the telling
// argument as a chip, a state glyph that is a ring while the call works and a tick that is drawn when it is done). A row opens to the raw tool
// id, its input (coloured) and its output. A failure is a small red glyph and tinted words, never a red box.

const toolNames: Record<string, MessageKey> = {
  fleet_status: "ai.toolname.fleet_status",
  health_summary: "ai.toolname.health_summary",
  alerts_list: "ai.toolname.alerts_list",
  node_journal: "ai.toolname.node_journal",
  nodes_list: "ai.toolname.nodes_list",
  user_get: "ai.toolname.user_get",
  user_devices: "ai.toolname.user_devices",
  events_search: "ai.toolname.events_search",
};

/** The tool in lowercase words when we have them, else null. */
export const toolWords = (t: T, id: string): string | null => (toolNames[id] ? t(toolNames[id]) : null);

const upFirst = (s: string) => s.charAt(0).toLocaleUpperCase() + s.slice(1);

/** The tool's name for a row: our words, else the id with its underscores made into spaces ("node_install_plan" -> "Node install plan"). */
export const toolLabel = (t: T, id: string) => upFirst(toolWords(t, id) ?? id.replace(/_/g, " "));

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

/** The most telling value(s) of the arguments as short chips: a query, a node, a user ("24 h" for hours); none when there is none. */
export function argChips(argsJson: string): string[] {
  let v: unknown;
  try {
    v = JSON.parse(argsJson);
  } catch {
    return [];
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return [];
  const o = v as Record<string, unknown>;
  const scalar = (k: string) => (typeof o[k] === "string" || typeof o[k] === "number" ? `${o[k]}${k === "hours" ? " h" : ""}` : "");
  const first = preferred.map(scalar).filter(Boolean);
  const rest = Object.keys(o).map(scalar).filter(Boolean);
  return [...new Set(first.length ? first : rest)].slice(0, 2);
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
export function useDuration() {
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
export function Collapse({ open, children }: { open: boolean; children: ReactNode }) {
  return (
    <div className={cx("grid transition-[grid-template-rows,opacity] duration-200 ease-out", open ? "grid-rows-[1fr] opacity-100" : "grid-rows-[0fr] opacity-0")} aria-hidden={!open} inert={!open}>
      <div className="min-h-0 overflow-hidden">{children}</div>
    </div>
  );
}

/** The state of a call (.as-ts): a ring while it works, a tick drawn when it is done, a cross when it failed. */
function State({ tool }: { tool: LiveTool }) {
  const t = useT();
  const cls = !tool.done ? "now" : tool.ok ? "done" : "bad";
  return (
    <span aria-hidden className={cx("as-ts", cls)} data-state={cls}>
      <I g={tool.done && !tool.ok ? "x" : "check"} />
      <span className="sr-only">{t("ai.tool.done", { tool: toolLabel(t, tool.tool) })}</span>
    </span>
  );
}

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="as-lb">
      <span>{label}</span>
      <div dir="ltr">{children}</div>
    </div>
  );
}

export function ToolRow({ tool }: { tool: LiveTool }) {
  const t = useT();
  const dur = useDuration();
  const [open, setOpen] = useState(false);
  const bad = tool.done && !tool.ok;
  const chips = argChips(tool.argsJson);
  const state = !tool.done ? "running" : bad ? "failed" : "ok";
  return (
    <div data-slot="tool" data-state={state} data-open={open || undefined} className="as-tr">
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="as-trh">
        <Icon name={toolIcon(tool.tool)} size={16} className="as-i" />
        <b>{toolLabel(t, tool.tool)}</b>
        <span className="as-tra">
          {chips.map((c) => (
            <span key={c} dir="auto" className="as-tag as-mn">
              {c}
            </span>
          ))}
          {bad && <span className="as-te">{tool.error}</span>}
        </span>
        <State tool={tool} />
        <I g="chev" className="as-cv" />
      </button>
      <Collapse open={open}>
        <div className="as-trd">
          <div className="as-trm">
            <code dir="ltr">{tool.tool}</code>
            {tool.ms !== undefined && <span className="tabular-nums">· {dur(tool.ms)}</span>}
          </div>
          {tool.argsJson && (
            <Labeled label={t("ai.tool.input")}>
              <Json text={tool.argsJson} />
            </Labeled>
          )}
          <Labeled label={t("ai.tool.output")}>
            {!tool.done ? <span className="text-muted">{t("ai.tool.running2")}</span> : bad ? <span className="text-danger-text">{tool.error || "—"}</span> : <span className="text-muted">{tool.count > 0 ? t.n("ai.tool.results", tool.count) : t("ai.tool.ok")}</span>}
          </Labeled>
        </div>
      </Collapse>
    </div>
  );
}

/**
 * The tool calls of a turn in one bordered group. While the turn works it is open with the running call spinning; once the turn is done
 * it folds into its summary line. Past five calls only the last three show, with "+N earlier" above them.
 */
export function ToolGroup({ tools, running, workedMs }: { tools: LiveTool[]; running: boolean; workedMs?: number }) {
  const t = useT();
  const dur = useDuration();
  const [open, setOpen] = useState<boolean | null>(null);
  const [all, setAll] = useState(false);
  const live = running || tools.some((x) => !x.done);
  const failed = tools.filter((x) => x.done && !x.ok).length;
  const isOpen = open ?? live;
  const fold = tools.length > 5 && !all;
  const shown = fold ? tools.slice(-3) : tools;
  return (
    <div data-slot="tools" className={cx("as-tc", isOpen && "open")}>
      <button type="button" onClick={() => setOpen(!isOpen)} aria-expanded={isOpen} className="as-tch">
        <span aria-hidden className={cx("as-ts", live ? "now" : "done")}>
          <I g="check" />
        </span>
        <span>
          {t.n(live ? "ai.grp.using" : "ai.grp.used", tools.length)}
          {failed > 0 && <span className="text-danger-text"> · {t("ai.tools.failed", { n: failed })}</span>}
          {!live && workedMs !== undefined && <span> · {dur(workedMs)}</span>}
        </span>
        <span className="as-cv">
          <I g="chev" />
        </span>
      </button>
      <Collapse open={isOpen}>
        <div>
          {fold && (
            <button type="button" onClick={() => setAll(true)} className="as-more">
              {t.n("ai.tool.earlier", tools.length - 3)}
            </button>
          )}
          {shown.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </div>
      </Collapse>
    </div>
  );
}

/** Milliseconds since `t0`, ticking while `live`. */
function useElapsed(t0: number | undefined, live: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live || t0 === undefined) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [live, t0]);
  return t0 === undefined ? undefined : Math.max(0, now - t0);
}

/**
 * "Thinking for 6s" while the turn works, "Thought for 6s" after (.as-rs); it opens to what the model said between tool rounds. It is
 * there while the turn works or when the model said something; a finished turn without words only has its tool group.
 */
export function Thinking({ m, running, interim }: { m: LiveMsg; running: boolean; interim: string }) {
  const t = useT();
  const fmt = useFmt();
  const [open, setOpen] = useState<boolean | null>(null);
  const live = running && m.workedMs === undefined;
  const elapsed = useElapsed(m.t0, live);
  const ms = m.workedMs ?? elapsed;
  const expandable = interim.length > 0;
  if (ms === undefined || (!live && !expandable)) return null;
  const secs = new Intl.NumberFormat(fmt.locale, { style: "unit", unit: "second", unitDisplay: "narrow", maximumFractionDigits: 0 }).format(Math.round(ms / 1000));
  const [before, after] = t(live ? "ai.think.running" : "ai.think.done", { s: "\u0000" }).split("\u0000");
  const label = (
    <>
      {before}
      <b className="as-mn">{secs}</b>
      {after}
    </>
  );
  const isOpen = expandable && (open ?? live);
  return (
    <div data-slot="thinking" className="flex w-full flex-col gap-2">
      {expandable ? (
        <button type="button" onClick={() => setOpen(!isOpen)} aria-expanded={isOpen} className="as-rs">
          {label}
          <I g="chev" />
        </button>
      ) : (
        <span className="as-rs">{label}</span>
      )}
      {expandable && (
        <Collapse open={isOpen}>
          <div className="as-thought">
            {interim.split(/\n{2,}/).map((p, i) => (
              <p key={i}>{p}</p>
            ))}
          </div>
        </Collapse>
      )}
    </div>
  );
}
