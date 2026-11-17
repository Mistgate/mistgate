import type { NodeCard as NodeCardMsg, OverviewResponse as OverviewMsg } from "@/gen/mistgate/admin/v1/fleet_pb";
import type { Plain } from "@/lib/plain";

export type NodeCard = Plain<NodeCardMsg>;
export type OverviewResponse = Plain<OverviewMsg>;
