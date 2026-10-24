import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { FilterChips } from "@/components/ui/chips";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { TextField } from "@/components/ui/text-field";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { nodeJournalQuery, type JournalEntry, type JournalFilter } from "@/lib/journal";

const limit = 200;

/**
 * The node's saved journal (NodeService.GetNodeJournal): one row per kind of warning or error and hour, newest first, with how
 * often it came. Read by every admin and on both editions; the search goes to the panel (it may hold more than one page).
 */
export function JournalTab({ nodeId }: { nodeId: string }) {
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

  if (q.isError && !q.data) return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Pending />;
  const { entries, truncated, nodesWithoutJournal } = q.data;

  return (
    <div className="flex flex-col gap-2.5">
      <p className="text-[13px] text-muted">{t("journal.intro")}</p>
      {nodesWithoutJournal.includes(nodeId) && <Notice tone="quiet" title={t("journal.unsupported")} />}
      <div className="flex flex-wrap items-center gap-2">
        <FilterChips
          aria-label={t("journal.period")}
          value={String(hours) as "24" | "168"}
          onValueChange={(v) => setHours(v === "168" ? 168 : 24)}
          options={[
            { value: "24", label: t("journal.day") },
            { value: "168", label: t("journal.week") },
          ]}
          className="flex-nowrap gap-1"
        />
        <FilterChips
          aria-label={t("journal.level")}
          value={level}
          onValueChange={setLevel}
          options={[
            { value: "all", label: t("journal.all") },
            { value: "errors", label: t("journal.errors") },
          ]}
          className="flex-nowrap gap-1"
        />
        <TextField
          search
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={t("journal.search")}
          aria-label={t("journal.search")}
          className="min-w-40 flex-1"
        />
      </div>
      <div className={cx("overflow-hidden rounded-[14px] border border-line bg-surface", q.isPlaceholderData && "opacity-60")}>
        {entries.map((e) => (
          <Row key={`${e.nodeId}/${e.hourStartUnix}/${e.source}/${e.message}/${e.inboundId}/${e.attrs.code ?? ""}`} e={e} />
        ))}
        {entries.length === 0 && <p className="px-3.5 py-3 text-muted">{t("journal.empty")}</p>}
      </div>
      {truncated && <p className="text-xs text-muted">{t("journal.truncated", { n: limit })}</p>}
    </div>
  );
}

function Row({ e }: { e: JournalEntry }) {
  const t = useT();
  const fmt = useFmt();
  const error = e.level === EventSeverity.ERROR;
  const attrs = Object.entries(e.attrs).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div className={cx("flex flex-col gap-0.5 border-b border-line px-3.5 py-2 last:border-b-0", error ? "bg-danger-soft" : "bg-warn-soft")}>
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 text-xs">
        <span className="text-faint">{fmt.stamp(e.lastUnix)}</span>
        <span className={cx("font-mono font-bold", error ? "text-danger" : "text-warn")}>{error ? "ERROR" : "WARN"}</span>
        <span className="font-mono text-muted">{e.source}</span>
        {e.profileName && <span className="text-muted">{e.profileName}</span>}
        {e.count > 1 && <span className="font-mono text-muted">{t("journal.times", { n: e.count })}</span>}
      </div>
      <p className="font-mono text-xs break-words">{e.message}</p>
      {attrs.length > 0 && (
        <p className="font-mono text-[11px] break-words text-muted">{attrs.map(([k, v]) => `${k}=${v}`).join("  ")}</p>
      )}
    </div>
  );
}
