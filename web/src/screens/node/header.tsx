import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/ui/bits";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { plainText, useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { agentLinked, nodeKind, useNodeStatus } from "@/lib/node-status";
import { nodeLoad } from "@/lib/node-load";
import type { Plain } from "@/lib/plain";
import { useNow } from "@/lib/time";
import { useIsOwner, useOlderNodes } from "@/lib/updates";
import { warpKind, warpWord } from "@/lib/warp";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";

type Data = Plain<GetNodeResponse>;

/** The WARP tag of the header (the board's sand tag), tinted by the state when it is not up, and the way to the WARP card. */
function WarpTag({ node }: { node: NonNullable<Data["node"]> }) {
  const t = useT();
  const state = node.warp?.state ?? WarpState.UNSPECIFIED;
  if (state === WarpState.NOT_CONFIGURED || state === WarpState.UNSPECIFIED) return null;
  const up = state === WarpState.UP;
  const kind = warpKind[state];
  const label = node.warp?.colo && up ? `WARP · ${node.warp.colo}` : `WARP · ${t(warpWord[state])}`;
  return (
    <Link
      to="/nodes/$id"
      params={{ id: node.id }}
      search={{ tab: "exit" }}
      hash="warp"
      aria-label={t("node.warpChip", { state: t(warpWord[state]) })}
      title={t("node.warpChip", { state: t(warpWord[state]) })}
      className="rounded-chip transition-opacity duration-150 hover:opacity-80"
    >
      {up ? (
        <Chip tone="sand">{label}</Chip>
      ) : (
        <Chip className={cx("tint", `tone-${kind}`, "text-(--c-fg)")}>
          <span aria-hidden className="size-1.5 rounded-full bg-(--c)" />
          {label}
        </Chip>
      )}
    </Link>
  );
}

/** "Load low / medium / high" (the board's last meta piece): the three-step meter of the user's page as words, the percentage on the hint. */
function LoadText({ data }: { data: Data }) {
  const t = useT();
  const fmt = useFmt();
  const now = useNow(10_000);
  const node = data.node!;
  const load = nodeLoad(data.metrics, node.bandwidthMbps, Math.floor(now / 1000));
  if (node.status === NodeStatus.DOWN && node.lastSeenUnix > 0) return <span>{t("np.noData", { time: fmt.clock(node.lastSeenUnix) })}</span>;
  if (!load || !agentLinked(node.status)) return null; // the page counts connected nodes only
  return (
    <span title={`${t(node.bandwidthMbps > 0 ? "node.loadHint" : "node.loadHintCpu")} · ${load.percent}%`} className={cx(load.level === "high" && "text-warn-text")}>
      {t(`np.load.${load.level}`)}
    </span>
  );
}

/** The address, copied on a click: the icon turns into a tick for a moment. */
function AddressButton({ address }: { address: string }) {
  const t = useT();
  const toast = useToast();
  const [done, setDone] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(plainText(address));
      setDone(true);
      setTimeout(() => setDone(false), 1400);
    } catch {
      toast(t("common.copyFailed"));
    }
  }
  return (
    <button
      type="button"
      onClick={() => void copy()}
      title={t("np.copyAddress")}
      className="-ms-2 -me-1.5 inline-flex h-6 items-center gap-1.5 rounded-chip ps-2 pe-1.5 font-mono text-xs font-medium tabular-nums transition-colors duration-150 ease-out hover:bg-step"
    >
      <bdi>{address}</bdi>
      <Icon name={done ? "check" : "copy"} size={12} className="text-muted" aria-hidden />
    </button>
  );
}

/** The ways of the entry role: what the node is for people from the country, as a chip next to its name (owner only). */
function RoleChip({ nodeId, live }: { nodeId: string; live: boolean }) {
  const t = useT();
  const owner = useIsOwner();
  const relays = useQuery({ ...relaysQuery(nodeId), enabled: owner && live }).data ?? [];
  const exits = new Set(relays.filter((r) => r.viaNodeId === nodeId).map((r) => r.exitNodeId));
  if (exits.size === 0) return null;
  return (
    <Link
      to="/nodes/$id"
      params={{ id: nodeId }}
      search={{ tab: "ways" }}
      className="inline-flex h-[18px] items-center rounded-[9px] bg-tone-sand-bg px-1.5 text-[11px] leading-none font-medium whitespace-nowrap text-tone-sand"
    >
      {t.n("np.role.entry", exits.size)}
    </Link>
  );
}

/** "Agent via msk1": the agent reaches the panel through an entry's bridge (R4); the chip opens the sentence under the name. */
function ViaChip({ viaNodeId, open, onToggle }: { viaNodeId: string; open: boolean; onToggle: () => void }) {
  const t = useT();
  const nodes = useQuery(nodesQuery).data?.nodes ?? [];
  const entry = nodes.find((x) => x.id === viaNodeId)?.name ?? viaNodeId;
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="inline-flex h-[18px] cursor-pointer items-center gap-1.5 rounded-[9px] bg-tone-sky-bg px-1.5 text-[11px] leading-none font-medium whitespace-nowrap text-tone-sky"
    >
      {t("np.via", { entry })}
    </button>
  );
}

/**
 * The header of a node page (the board's .nd-back, .nd-h): the way back, the flag, the name in mono 28, the status pill and
 * the role chips, a line of meta (place, address that copies, WARP, agent, load) and the two actions at the end.
 */
export function NodeHeader({ data, doctorN, restartOff, onRestart, onDoctor }: { data: Data; doctorN: number; restartOff: string; onRestart: () => void; onDoctor: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const node = data.node!;
  const older = useOlderNodes().has(node.id);
  const retired = node.status === NodeStatus.RETIRED;
  const [viaOpen, setViaOpen] = useState(false);
  const place = [node.location || fmt.country(node.countryCode), node.provider].filter(Boolean).join(" · ");
  return (
    <>
      <Link
        to="/nodes"
        aria-label={t("node.back")}
        className="group -ms-2 inline-flex h-8 items-center gap-1.5 self-start rounded-field ps-2 pe-3 text-sm text-sec transition-[background-color,color] duration-150 ease-out hover:bg-step hover:text-fg"
      >
        <Icon name="back" size={16} className="transition-transform duration-200 ease-out group-hover:-translate-x-0.5 rtl:group-hover:translate-x-0.5" />
        {t("nav.nodes")}
      </Link>

      <header className="nd-h mt-1 flex items-center gap-4 max-[1200px]:flex-wrap">
        <Flag code={node.countryCode} size={32} className="nd-fl h-8 w-12 rounded-lg! max-[720px]:h-6 max-[720px]:w-9 max-[720px]:rounded-md!" />
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <div className="flex items-center gap-3 max-[720px]:flex-wrap max-[720px]:gap-y-1.5">
            <h1 className="font-mono text-[28px] leading-8 font-medium tracking-[-0.02em] max-[720px]:text-2xl">{node.name}</h1>
            <span className="flex [&_.pill]:h-7">
              <StatusPill kind={nodeKind(node)} label={st.word(node)} />
            </span>
            {!retired && <RoleChip nodeId={node.id} live />}
            {!retired && node.linkViaNodeId && <ViaChip viaNodeId={node.linkViaNodeId} open={viaOpen} onToggle={() => setViaOpen(!viaOpen)} />}
          </div>
          {viaOpen && node.linkViaNodeId && <p className="max-w-[640px] text-[13px] leading-[18px] text-sec">{t("node.linkVia.text")}</p>}
          <div className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[13px] text-sec">
            {place && <span>{place}</span>}
            <AddressButton address={node.address} />
            <WarpTag node={node} />
            {node.agentVersion && (
              <span>
                {t("node.agent")} <span className="font-mono text-xs font-normal text-fg">{node.agentVersion}</span>
              </span>
            )}
            {older && (
              <Link to="/updates" className="text-warn-text underline decoration-dotted underline-offset-2">
                {t("up.node.outdated")}
              </Link>
            )}
            <LoadText data={data} />
          </div>
        </div>
        {!retired && (
          <div className="flex flex-none flex-col gap-1.5 max-[1200px]:w-full">
            <div className="flex gap-2 max-[720px]:[&>*]:flex-1">
              {/* a disabled button shows no tooltip: the reason sits on a wrapper */}
              <span title={restartOff || undefined} className="flex">
                <Button variant="secondary" size="lg" disabled={!!restartOff} onClick={onRestart} className="max-[720px]:flex-1">
                  {t("node.restart")}
                </Button>
              </span>
              <Button variant="secondary" size="lg" onClick={onDoctor}>
                {t("node.doctor")}
                {doctorN > 0 && (
                  <span className="grid h-5 min-w-5 place-items-center rounded-[10px] bg-warn-soft px-1.5 font-mono text-[11px] font-normal text-warn-text">{doctorN}</span>
                )}
              </Button>
            </div>
            {restartOff && <p className="text-xs text-muted lg:hidden">{restartOff}</p>}
          </div>
        )}
      </header>
    </>
  );
}
