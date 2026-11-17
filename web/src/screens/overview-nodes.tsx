import { Link } from "@tanstack/react-router";
import { useMemo, type CSSProperties } from "react";
import { useAddNode } from "@/components/add-node";
import { Chip } from "@/components/ui/bits";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Row, rowClass, RowHead } from "@/components/ui/row";
import { kindTextClass } from "@/components/ui/status";
import type { GetChecksResponse } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { foldView } from "@/lib/fleet";
import { useFmt } from "@/lib/format";
import { useIsPhone } from "@/lib/media";
import { nodeKind, sortByProblems, useNodeStatus } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { nodeProtocols, nodeTicks } from "./overview-checks";
import { CardFoot, CardHead, footLink, OvDot } from "./overview-parts";
import type { NodeCard } from "./overview-types";

type Checks = Plain<GetChecksResponse>;

const SHOWN = 8;
const onlineOf = (n: NodeCard) => n.online.reduce((a, o) => a + o.users, 0);

/**
 * The nodes as a table: problems first, eight of them, the rest behind "N more". Each row is the node, how it is, the checks of
 * the last 24 hours as cells, the people online, the speed with a bar relative to the fastest, and its profiles as tags. The
 * columns that do not fit fold away as the window narrows (overview.css). `checks` off (the Cloudflare edition has no tunnel
 * checks) leaves the column and its note out.
 */
export function NodesCard({ cards, checks, checksOn, canAdd }: { cards: NodeCard[]; checks: Checks | undefined; checksOn: boolean; canAdd: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const addNode = useAddNode();
  const phone = useIsPhone();
  const sorted = useMemo(() => sortByProblems(cards), [cards]);
  const shown = sorted.slice(0, SHOWN);
  const hidden = sorted.slice(SHOWN);
  const fold = foldView(hidden);
  const rows = useMemo(() => new Map((checks?.rows ?? []).map((r) => [r.nodeId, r])), [checks]);
  const fastest = Math.max(1, ...shown.map((n) => n.downBps));
  const cols = cx("ov-nr", !checksOn && "nochk");

  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead title={t("nav.nodes")} count={cards.length}>
        {checksOn && <Chip className="max-md:hidden">{t("ov.nodes.tag")}</Chip>}
      </CardHead>
      <RowHead aria-hidden className={cx(cols, "max-md:hidden")}>
        <span>{t("ov.col.node")}</span>
        <span>{t("ov.col.state")}</span>
        {checksOn && <span className="c-tk">{t("ov.col.checks")}</span>}
        <span>{t("ov.col.online")}</span>
        <span className="c-sp">{t("ov.col.speed")}</span>
        <span className="c-pf">{t("ov.col.profiles")}</span>
        <span />
      </RowHead>
      {shown.map((n) => {
        const kind = nodeKind(n);
        const row = rows.get(n.id);
        const ticks = nodeTicks(row);
        const protocols = checks ? nodeProtocols(row, checks.columns) : [];
        const tags = protocols.length ? protocols : n.online.map((o) => o.protocol);
        return (
          <Link key={n.id} to="/nodes/$id" params={{ id: n.id }} className={cx(rowClass(), cols)}>
            <span className="flex min-w-0 items-center gap-3">
              <Flag code={n.countryCode} size={16} />
              <b className="font-mono font-medium">{n.name}</b>
              <span className="truncate text-sec">{n.location || fmt.country(n.countryCode)}</span>
            </span>
            <span className={cx("inline-flex items-center gap-2 whitespace-nowrap", kind === "warn" || kind === "bad" ? kindTextClass[kind] : "")} title={st.line(n)}>
              <OvDot kind={kind} />
              {st.word(n)}
            </span>
            {checksOn && (
              <span role="img" aria-label={t("ov.tick.aria")} className="ov-tk c-tk">
                {ticks.map((k, i) => (
                  <i
                    key={i}
                    className={k.state === "ok" ? undefined : k.state}
                    data-t={k.from ? `${fmt.clock(k.from)}–${fmt.clock(k.to)}` : t("ov.tick.none")}
                    data-s={k.state === "none" ? (k.from ? t("ov.tick.none") : "") : k.state === "ok" ? t("ov.tick.ok", { ms: k.ms || "–" }) : k.state === "warn" ? t("ov.tick.part", { failed: k.failed, total: k.ok + k.failed }) : t("ov.tick.fail")}
                  />
                ))}
              </span>
            )}
            <span className="c-on font-mono font-medium tabular-nums">
              {onlineOf(n)} {t("ov.online")}
            </span>
            <span className="c-sp flex flex-col gap-1">
              <span className="font-mono font-medium whitespace-nowrap tabular-nums">↓ {fmt.mbit(n.downBps)}</span>
              <i className="ov-bar h-1" style={{ "--w": `${Math.round((n.downBps / fastest) * 100)}%`, "--fill": "var(--tone-sky)" } as CSSProperties} />
            </span>
            <span className="c-pf flex gap-1 overflow-hidden max-md:flex-wrap">
              {tags.map((p) => (
                <ProtocolTag key={p} protocol={p} compact expanded={phone} />
              ))}
            </span>
            <Icon name="chevronRight" size={16} className="c-ch block flex-none text-muted" />
          </Link>
        );
      })}
      {hidden.length > 0 && (
        <CardFoot>
          <Link to="/nodes" className={footLink}>
            {[t.n("ov.moreNodes", hidden.length), fold.problems ? t.n("ov.moreBad", fold.problems) : fold.pending ? t.n("ov.tail.pending", fold.pending) : t("ov.moreOk")].join(" · ")}
          </Link>
        </CardFoot>
      )}
      {canAdd && (
        <Row as="button" onClick={() => addNode()} className="flex! justify-center gap-2 border-dashed font-medium text-sec hover:text-fg">
          <Icon name="plus" size={16} />
          {t("node.add.button")}
        </Row>
      )}
    </section>
  );
}
