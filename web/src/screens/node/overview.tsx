import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import type { Plain } from "@/lib/plain";
import { NodeChecks } from "./checks";
import { LoadHistory } from "./load-history";
import { NodeLimits } from "./limits";
import { NodeAlerts, NodeClosed } from "./overview-parts";
import { ResourcesCard } from "./resources";
import { ShareCard } from "./share";
import { OnlineNow, TopToday } from "./users";
import { WaysStrip, type WaysModel } from "./ways";

/**
 * The Overview of a node (the board's first tab): the ways in on one line, then two columns. On the left what waits for you here,
 * the resources (CPU, memory, disk and the link with what took them), the load over the hours with the traffic, and the checks as a
 * client sees them, and who gets the node (a card of its own); on the right who is on the node, the traffic of the day, what can run out, and what closed lately.
 */
export function OverviewTab({ data, ways }: { data: Plain<GetNodeResponse>; ways?: WaysModel }) {
  const node = data.node!;
  const retired = node.status === NodeStatus.RETIRED;
  // a node that was there and went quiet has stale readings (dimmed, with the time it was last heard from), not "not yet" ones
  const dead = node.status === NodeStatus.DOWN;
  return (
    <div className="flex flex-col gap-4">
      {ways && !retired && <WaysStrip data={data} w={ways} />}
      <div className="mt-1 grid grid-cols-[minmax(0,1fr)_400px] items-start gap-4 max-[1360px]:grid-cols-[minmax(0,1fr)_340px] max-[1200px]:grid-cols-1">
        <div className="flex min-w-0 flex-col gap-4">
          <NodeAlerts nodeId={node.id} nodeName={node.name} />
          {!retired && <ShareCard node={node} />}
          {!retired && <ResourcesCard metrics={data.metrics} cpus={data.facts?.cpuCount ?? 0} offlineSince={dead ? node.lastSeenUnix : undefined} />}
          {!retired && <LoadHistory nodeId={node.id} dead={dead} />}
          <NodeChecks nodeId={node.id} inbounds={data.inbounds} />
        </div>
        <div className="flex min-w-0 flex-col gap-4">
          <OnlineNow data={data} />
          <TopToday data={data} />
          {!retired && !dead && <NodeLimits metrics={data.metrics} bandwidthMbps={node.bandwidthMbps} />}
          <NodeClosed nodeId={node.id} />
        </div>
      </div>
    </div>
  );
}
