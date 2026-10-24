import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import type { GetNodeJournalResponse, NodeJournalEntry } from "@/gen/mistgate/admin/v1/node_pb";
import { nodes } from "./api";
import { plain, type Plain } from "./plain";

// The saved journal of a node's agent and engines (agent.proto JournalEntry, NodeService.GetNodeJournal): warnings and errors
// grouped per hour, kept 7 days, client addresses cut out. Unlike the live tail it is open to every admin and exists on the
// Cloudflare edition too. The words are in i18n/journal.ts.

export type NodeJournal = Plain<GetNodeJournalResponse>;
export type JournalEntry = Plain<NodeJournalEntry>;

export type JournalFilter = { hours: 24 | 168; errorsOnly: boolean; query: string };

export const nodeJournalQuery = (nodeId: string, f: JournalFilter) =>
  queryOptions({
    queryKey: ["nodes", "journal", nodeId, f],
    queryFn: async ({ signal }) =>
      plain(
        await nodes.getNodeJournal(
          { nodeId, hours: f.hours, minLevel: f.errorsOnly ? EventSeverity.ERROR : EventSeverity.WARNING, query: f.query, limit: 200 },
          { signal },
        ),
      ),
    placeholderData: keepPreviousData, // a new filter keeps the old rows on screen until the answer comes
    refetchInterval: 60_000,
  });
