import { Segmented } from "@/components/ui/segmented";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { EventsTab } from "./events";
import { JournalTab } from "./journal";
import { LogsTab } from "./logs";
import { historyViews, type HistoryView } from "./tabs";

const labels = { events: "node.tab.events", journal: "node.tab.journal", logs: "node.tab.logs" } as const;

/** History: what happened on the node, as its events, its journal of warnings and errors, or its live log (`?view=`). */
export function HistoryTab({ data, view, onView }: { data: Plain<GetNodeResponse>; view: HistoryView; onView: (view: HistoryView) => void }) {
  const t = useT();
  const node = data.node!;
  return (
    <div className="flex flex-col gap-3.5">
      <Segmented
        aria-label={t("node.tab.history")}
        variant="thumb"
        value={view}
        onValueChange={onView}
        options={historyViews.map((v) => ({ value: v, label: t(labels[v]) }))}
        className="w-full max-w-[420px]"
      />
      {view === "events" && <EventsTab nodeId={node.id} />}
      {view === "journal" && <JournalTab nodeId={node.id} />}
      {/* where the edition does not relay the agent's log, the logs lead to the journal */}
      {view === "logs" && <LogsTab data={data} onJournal={() => onView("journal")} />}
    </div>
  );
}
