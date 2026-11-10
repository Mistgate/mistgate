import { queryOptions, useQuery } from "@tanstack/react-query";
import type { StatusKind } from "@/components/ui/status";
import { OsUpdateState, type GetOsUpdatesResponse, type NodeOs } from "@/gen/mistgate/admin/v1/update_pb";
import type { OsStatus } from "@/gen/mistgate/agent/v1/agent_pb";
import type { MessageKey } from "@/i18n/en";
import { updates } from "./api";
import { plain, type Plain } from "./plain";

// Updates -> Server operating system: what each server's packages and kernel look like (UpdateService.GetOsUpdates).
// Read-only for now: the agents read their package lists from disk and the panel shows them; installing and rebooting
// from the panel come later. The VPS edition only.

export type OsUpdates = Plain<GetOsUpdatesResponse>;
export type OsNode = Plain<NodeOs>;
export type Os = Plain<OsStatus>;

export const osUpdatesQuery = queryOptions({
  queryKey: ["updates", "os"],
  queryFn: async ({ signal }) => plain(await updates.getOsUpdates({}, { signal })),
  refetchInterval: 60_000,
});

export const useOsUpdates = (enabled: boolean) => useQuery({ ...osUpdatesQuery, enabled });

/** One row of the table: the panel's own server first, then the nodes. */
export type OsRow = {
  id: string;
  /** The node's name; undefined for the panel's server. */
  name?: string;
  nodeId?: string;
  online?: number;
  onPanelServer: boolean;
  state: OsUpdateState;
  status?: Os;
  /** Why there is nothing to show, as a message key; undefined when there is a status. */
  why?: MessageKey;
};

const errorKey = (code: string): MessageKey =>
  code === "not_apt" ? "up.os.why.not_apt" : code === "no_systemd" ? "up.os.why.no_systemd" : "up.os.why.read_failed";

/** The state of a status the panel read itself (its own server has no NodeOs around it). */
function stateOf(s: Os): OsUpdateState {
  if (s.error) return OsUpdateState.UNSUPPORTED;
  if (s.rebootRequired) return OsUpdateState.REBOOT_REQUIRED;
  return s.pending > 0 ? OsUpdateState.UPDATES : OsUpdateState.UP_TO_DATE;
}

export function osRows(d: OsUpdates): OsRow[] {
  const host = d.panelHost;
  const panel: OsRow = host
    ? { id: "panel", onPanelServer: false, state: stateOf(host), status: host, why: host.error ? errorKey(host.error) : undefined }
    : { id: "panel", onPanelServer: false, state: OsUpdateState.UNSUPPORTED, why: "up.os.why.panel" };
  const nodes = d.nodes.map((n): OsRow => {
    const why: MessageKey | undefined = n.status?.error
      ? errorKey(n.status.error)
      : !n.supported
        ? "up.os.why.agent"
        : undefined;
    return { id: n.nodeId, name: n.name, nodeId: n.nodeId, online: n.onlineUsers, onPanelServer: n.onPanelServer, state: n.state, status: n.status, why };
  });
  return [panel, ...nodes];
}

/** The pill of a row: security updates and a pending reboot ask for attention, plain updates stay quiet. */
export function osPill(row: OsRow): { kind: StatusKind; key: MessageKey } {
  switch (row.state) {
    case OsUpdateState.UP_TO_DATE:
      return { kind: "ok", key: "up.os.state.up_to_date" };
    case OsUpdateState.REBOOT_REQUIRED:
      return { kind: "warn", key: "up.os.state.reboot" };
    case OsUpdateState.UPDATES:
      return (row.status?.security ?? 0) > 0 ? { kind: "warn", key: "up.os.state.security" } : { kind: "off", key: "up.os.state.updates" };
    case OsUpdateState.BUSY:
      return { kind: "busy", key: "up.os.state.busy" };
    case OsUpdateState.FAILED:
      return { kind: "bad", key: "up.os.state.failed" };
    case OsUpdateState.OFFLINE:
      return { kind: "off", key: "up.os.state.offline" };
    case OsUpdateState.UNSUPPORTED:
      return { kind: "off", key: "up.os.state.unsupported" };
    default:
      return { kind: "off", key: "up.os.state.unknown" };
  }
}

const distroNames: Record<string, string> = { ubuntu: "Ubuntu", debian: "Debian" };

/** "Ubuntu 24.04"; an id the panel does not know is shown as the server wrote it. */
export function osSystem(s: Os | undefined): string {
  if (!s?.distroId) return "";
  return [distroNames[s.distroId] ?? s.distroId, s.distroVersion].filter(Boolean).join(" ");
}

/** What the fleet needs, for the line above the table. */
export function osSummary(rows: OsRow[]): { reboot: number; security: number; updates: number; known: number } {
  let reboot = 0, security = 0, withUpdates = 0, known = 0;
  for (const r of rows) {
    if (!r.status || r.status.error) continue;
    known++;
    if (r.status.rebootRequired) reboot++;
    if (r.status.security > 0) security++;
    if (r.status.pending > 0) withUpdates++;
  }
  return { reboot, security, updates: withUpdates, known };
}
