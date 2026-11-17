import { Segmented } from "@/components/ui/segmented";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { agentLinked } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { EventsTab } from "./events";
import { JournalTab } from "./journal";
import { LogsTab } from "./logs";
import { historyViews, type HistoryView } from "./tabs";
import "./history.css";

const labels = { events: "node.tab.events", journal: "node.tab.journal", logs: "node.tab.logs" } as const;

/**
 * History: what happened on the node, as its events, its journal of warnings and errors, or its live log (`?view=`). The switch is the
 * first thing of the bar every view draws; the view adds its own filters after it.
 */
export function HistoryTab({ data, view, onView }: { data: Plain<GetNodeResponse>; view: HistoryView; onView: (view: HistoryView) => void }) {
  const t = useT();
  const node = data.node!;
  const lead = (
    <Segmented
      aria-label={t("node.tab.history")}
      value={view}
      onValueChange={onView}
      options={historyViews.map((v) => ({ value: v, label: t(labels[v]) }))}
    />
  );
  return (
    <>
      {view === "events" && <EventsTab nodeId={node.id} lead={lead} createdUnix={data.createdUnix} down={!agentLinked(node.status)} />}
      {view === "journal" && <JournalTab nodeId={node.id} lead={lead} />}
      {/* where the edition does not relay the agent's log, the logs lead to the journal */}
      {view === "logs" && <LogsTab data={data} lead={lead} onJournal={() => onView("journal")} />}
    </>
  );
}
