import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { cellLook, countryColumn, listOf, SLOW_CONNECT_MS, type CheckCell, type CountryColumn } from "@/lib/health";
import type { GetChecksResponse } from "@/gen/mistgate/admin/v1/health_pb";
import type { Plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { protocolName } from "@/lib/series";
import { CheckRows } from "@/screens/node/checks";
import { CountryCell, CountryHead, CountryRow } from "./country";
import { CellTip, HistoryCard, SlowMark, useRunChecks } from "./history";

type Checks = Plain<GetChecksResponse>;

// One tone per cell: the pill colours of the status dots, in the soft tint of the 44 px cells.
const toneCls: Record<string, string> = {
  ok: "tone-ok tint tone-text",
  warn: "tone-warn tint tone-text",
  bad: "tone-bad tint tone-text",
  busy: "tone-busy tint tone-text",
  blip: "tone-blip tint tone-text",
  off: "border border-line bg-surface-2 text-muted",
  none: "border border-line text-faint",
};

/**
 * "The panel's server loses inbound UDP" (GetChecksResponse.udp_loss): the slow UDP connects come from the panel's own
 * network, people are not affected. Calm and grey, it is not a problem of the nodes.
 */
export function UdpLossNotice({ data }: { data: Checks }) {
  const t = useT();
  const fmt = useFmt();
  const loss = data.udpLoss;
  if (!loss || loss.nodeIds.length === 0) return null;
  const nodes = loss.nodeIds.map((id) => data.rows.find((r) => r.nodeId === id)?.nodeName ?? id);
  const udp = [...new Set(data.columns.filter((c) => c.protocol === "hysteria2" || c.protocol === "awg" || c.protocol === "amneziawg").map((c) => protocolName(c.protocol)))];
  const title = t("hl.udp.title");
  const body = t("hl.udp.body", {
    protocols: listOf(fmt.locale, udp),
    to: t.n("hl.udp.to", nodes.length, { nodes: listOf(fmt.locale, nodes) }),
    share: loss.sharePercent,
    tcp: data.columns.some((c) => c.protocol === "vless") ? t("hl.udp.tcpOk") : "",
  });
  return (
    <Notice tone="quiet">
      {/* no space after a full-width colon (zh) */}
      <b>{title}</b>
      {title.endsWith("：") ? "" : " "}
      {body}
    </Notice>
  );
}

/** What the words in the cells mean, under the matrix: the tooltip says it too, but a phone has no hover. */
export function CheckLegend({ slow = false }: { slow?: boolean }) {
  const t = useT();
  const items: [string, "hl.legend.ms" | "hl.legend.fail" | "hl.legend.failed" | "hl.legend.off" | "hl.legend.offline" | "hl.legend.na"][] = [
    ["ok", "hl.legend.ms"],
    ["bad", "hl.legend.fail"],
    ["bad", "hl.legend.failed"],
    ["off", "hl.legend.off"],
    ["blip", "hl.legend.offline"],
    ["none", "hl.legend.na"],
  ];
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1.5 text-[11px] leading-snug text-muted">
      {items.map(([tone, key]) => (
        <li key={key} className="flex items-center gap-1.5">
          <span aria-hidden className={cx("size-2.5 flex-none rounded-[3px]", toneCls[tone])} />
          {t(key)}
        </li>
      ))}
      {slow && (
        <li className="flex items-center gap-1.5">
          <Icon name="clock" size={10} aria-hidden className="flex-none" />
          {t("hl.legend.slow")}
        </li>
      )}
    </ul>
  );
}

/**
 * Node × profile matrix of the synthetic checks; a click opens the 24 h history of that cell under it. A phone gets a card per
 * node. With the country check on, a last column (a last row on the phone) says what probes inside that country saw.
 */
export function ChecksTab({ data }: { data: Checks }) {
  const t = useT();
  const fmt = useFmt();
  const run = useRunChecks();
  const [sel, setSel] = useState<{ node: string; col: number } | null>(null);
  const cols = data.columns;
  const country = countryColumn(data.countryChecks);
  const min = 90 + cols.length * 102 + (country ? 128 : 0);

  const picked = sel && data.rows.find((r) => r.nodeId === sel.node);
  const cell: CheckCell | undefined = sel ? picked?.cells[sel.col] : undefined;
  const newest = Math.max(0, ...data.rows.flatMap((r) => r.cells.map((c) => c.last?.atUnix ?? 0)));
  const slow = data.rows.some((r) => r.cells.some((c) => c.connectHourMs >= SLOW_CONNECT_MS));

  if (cols.length === 0)
    return <p className="rounded-card border border-dashed border-line px-4 py-6 text-center text-[13px] text-muted">{t("hl.checks.noProfiles")}</p>;

  return (
    <div className="flex flex-col gap-3.5">
      <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <SectionLabel as="h2" className="flex-1" icon="pulse" tone="rose">
            {t("hl.checks.title")}
          </SectionLabel>
          <span className="text-xs text-muted">
            {t("hl.checks.how", { min: Math.max(1, Math.round(data.intervalS / 60)) })}
            {newest > 0 && ` · ${t("hl.checks.checkedAgo", { ago: fmt.ago(newest) })}`}
          </span>
          {run.can && (
            <Button variant="secondary" size="sm" onClick={run.run} disabled={run.running}>
              {t("hl.checks.now")}
            </Button>
          )}
        </div>
        <EditionNotice feature="health_checks" />
        <UdpLossNotice data={data} />
        {/* the padding gives the selection ring and the hover scale room inside the scrolling box */}
        <div className="no-scrollbar -m-1 overflow-x-auto p-1 max-md:hidden">
          <div className="grid gap-1.5" style={{ gridTemplateColumns: `90px repeat(${cols.length}, minmax(90px, 1fr))${country ? " minmax(122px, 1.2fr)" : ""}`, minWidth: min }}>
            <span />
            {cols.map((c) => (
              <span key={c.profileId} className="line-clamp-2 self-end px-1 pb-1 font-mono text-[11px] leading-tight font-bold break-words text-muted" title={c.profileName}>
                {c.profileName}
              </span>
            ))}
            {country && <CountryHead code={country.code} />}
            {data.rows.map((r) => (
              <Row key={r.nodeId} row={r} cols={cols} country={country} selected={sel?.node === r.nodeId ? sel.col : -1} onPick={(col) => setSel({ node: r.nodeId, col })} />
            ))}
          </div>
        </div>
        {/* the phone: a card per node, the history opens right under its row */}
        <div className="flex flex-col gap-4 md:hidden">
          {data.rows.map((r) => (
            <div key={r.nodeId} className="flex flex-col gap-2">
              <Link to="/nodes/$id" params={{ id: r.nodeId }} className="flex w-fit items-center gap-2 text-sm font-bold hover:text-accent-text">
                <span className="font-mono text-[10px] font-bold text-muted">{r.countryCode}</span>
                <span className="font-mono">{r.nodeName}</span>
              </Link>
              <CheckRows row={r} columns={cols} onRun={run.can ? () => run.run() : undefined} running={run.running} />
              {country && <CountryRow check={country.byNode.get(r.nodeId)} code={country.code} />}
            </div>
          ))}
        </div>
        <div className="border-t border-line pt-3">
          <CheckLegend slow={slow} />
        </div>
      </section>
      {picked && cell?.deployed && (
        <div className="max-md:hidden">
          <HistoryCard
            title={t("hl.hist.title", { node: picked.nodeName, profile: cols[sel.col]?.profileName ?? "" })}
            cell={cell}
            onRun={run.can ? () => run.run() : undefined}
            running={run.running}
          />
        </div>
      )}
    </div>
  );
}

function Row({
  row,
  cols,
  country,
  selected,
  onPick,
}: {
  row: Checks["rows"][number];
  cols: Checks["columns"];
  country: CountryColumn | null;
  selected: number;
  onPick: (col: number) => void;
}) {
  const t = useT();
  const fmt = useFmt();
  return (
    <>
      <div className="flex min-w-0 items-center gap-2">
        <span className="w-5 flex-none font-mono text-[10px] font-bold text-muted">{row.countryCode}</span>
        <Link to="/nodes/$id" params={{ id: row.nodeId }} className="truncate font-mono text-[13px] font-bold hover:text-accent-text hover:underline">
          {row.nodeName}
        </Link>
      </div>
      {cols.map((col, i) => {
        const c = row.cells[i];
        if (!c) return <span key={col.profileId} />;
        const look = cellLook(t, fmt, c, row.nodeStatus);
        const inert = look.kind === "none";
        const button = (
          <button
            key={col.profileId}
            type="button"
            disabled={inert}
            onClick={() => onPick(i)}
            title={inert ? look.state : undefined}
            aria-label={t("hl.cell.aria", { node: row.nodeName, profile: col.profileName, state: look.state })}
            aria-pressed={selected === i}
            className={cx(
              "box-border flex h-11 flex-col items-center justify-center rounded-[11px] border px-1.5 text-center font-mono text-xs leading-tight font-bold transition-transform duration-200 ease-spring",
              toneCls[look.kind],
              inert ? "cursor-default" : "cursor-pointer hover:scale-[1.03]",
              selected === i && "outline-2 outline-offset-2 outline-accent",
            )}
          >
            {look.label}
            {look.slow && <SlowMark text={look.slow} />}
          </button>
        );
        // a disabled button gets no pointer events: the dash keeps its plain title
        return inert ? button : (
          <CellTip key={col.profileId} look={look}>
            {button}
          </CellTip>
        );
      })}
      {country && <CountryCell check={country.byNode.get(row.nodeId)} node={row.nodeName} code={country.code} />}
    </>
  );
}
