import { useQuery } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { FilterChips } from "@/components/ui/chips";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { TextField } from "@/components/ui/text-field";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { nodeJournalQuery, type JournalEntry, type JournalFilter } from "@/lib/journal";
import { Glyph, glyphs } from "@/screens/overview-parts";
import "./history.css";

const limit = 200;

/**
 * The node's saved journal (NodeService.GetNodeJournal): one row per kind of warning or error and hour, newest first, with how
 * often it came. Read by every admin and on both editions; the search goes to the panel (it may hold more than one page).
 */
export function JournalTab({ nodeId, lead }: { nodeId: string; lead?: ReactNode }) {
  const t = useT();
  const [hours, setHours] = useState<JournalFilter["hours"]>(24);
  const [level, setLevel] = useState<"all" | "errors">("all");
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  useEffect(() => {
    const id = setTimeout(() => setQuery(draft.trim()), 300);
    return () => clearTimeout(id);
  }, [draft]);
  const q = useQuery(nodeJournalQuery(nodeId, { hours, errorsOnly: level === "errors", query }));

  if (q.isError && !q.data)
    return (
      <div className="nh">
        {lead && <div className="nh-bar">{lead}</div>}
        <QueryError error={q.error} onRetry={() => void q.refetch()} />
      </div>
    );
  if (!q.data)
    return (
      <div className="nh">
        {lead && <div className="nh-bar">{lead}</div>}
        <Pending />
      </div>
    );
  const { entries, truncated, nodesWithoutJournal } = q.data;

  return (
    <div className="nh">
      <div className="nh-bar">
        {lead}
        <FilterChips
          aria-label={t("journal.period")}
          value={String(hours) as "24" | "168"}
          onValueChange={(v) => setHours(v === "168" ? 168 : 24)}
          options={[
            { value: "24", label: t("journal.day") },
            { value: "168", label: t("journal.week") },
          ]}
          className="nh-chips flex-nowrap"
        />
        <FilterChips
          aria-label={t("journal.level")}
          value={level}
          onValueChange={setLevel}
          options={[
            { value: "all", label: t("journal.all") },
            { value: "errors", label: t("journal.errors") },
          ]}
          className="nh-chips flex-nowrap"
        />
        <TextField search value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={t("journal.search")} aria-label={t("journal.search")} className="min-w-40 flex-1 max-w-80" />
      </div>
      <p className="text-[13px] text-muted">{t("journal.intro")}</p>
      {nodesWithoutJournal.includes(nodeId) && <Notice tone="quiet" title={t("journal.unsupported")} />}
      <section className={cx("nh-tl overflow-hidden rounded-card border border-line bg-surface", q.isPlaceholderData && "opacity-60")} aria-label={t("node.tab.journal")}>
        {entries.map((e) => (
          <Row key={`${e.nodeId}/${e.hourStartUnix}/${e.level}/${e.source}/${e.message}/${e.inboundId}/${e.attrs.code ?? ""}`} e={e} />
        ))}
        {entries.length === 0 && <p className="px-6 py-5 text-[13px] text-muted max-[720px]:px-4">{t("journal.empty")}</p>}
      </section>
      {truncated && <p className="text-xs text-muted">{t("journal.truncated", { n: limit })}</p>}
    </div>
  );
}

/** A line of the journal (the board's .nh-a): when, a mark of how bad it is, the message with its attributes, how often and from where. */
function Row({ e }: { e: JournalEntry }) {
  const t = useT();
  const fmt = useFmt();
  const error = e.level === EventSeverity.ERROR;
  const attrs = Object.entries(e.attrs).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div className="nh-a">
      <span className="t">{fmt.stamp(e.lastUnix)}</span>
      <span className={cx("av", error && "crit")} aria-hidden>
        <Glyph d={error ? glyphs.bad : glyphs.warn} size={14} stroke={1.75} />
      </span>
      <div className="min-w-0">
        <b>
          <span className={cx("me-2 text-[11px]", error ? "text-danger" : "text-warn")}>{error ? "ERROR" : "WARN"}</span>
          {e.message}
        </b>
        {attrs.length > 0 && <span>{attrs.map(([k, v]) => `${k}=${v}`).join("  ")}</span>}
      </div>
      <span className="tail">
        <span className="font-mono">{e.source}</span>
        {e.profileName && <span>{e.profileName}</span>}
        {e.count > 1 && <span className="font-mono">{t("journal.times", { n: e.count })}</span>}
      </span>
    </div>
  );
}
