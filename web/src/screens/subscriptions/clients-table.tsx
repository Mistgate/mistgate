import { Link } from "@tanstack/react-router";
import type { ComponentProps } from "react";
import { Card } from "@/components/ui/bits";
import { Pagination } from "@/components/ui/pagination";
import { StatusPill } from "@/components/ui/status";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { needText, platformName, stateKind, stateWord, type CompatDevice } from "@/lib/clientcompat";
import { useTx, type Tx } from "@/screens/users/t";
import { ActualCard, ActualCell } from "./clients-actual";

type Pager = ComponentProps<typeof Pagination>;

/** The person: a link to their page. */
function Person({ d }: { d: CompatDevice }) {
  return (
    <Link to="/users/$id" params={{ id: d.userId }} className="block truncate text-[13px] font-semibold hover:underline">
      {d.userName || d.userId}
    </Link>
  );
}

/** The device: its model (or the shared place of the link), the platform and OS under it. */
function Device({ d, t }: { d: CompatDevice; t: Tx }) {
  const sub = [d.platform && platformName(d.platform), d.osVersion].filter(Boolean).join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className={cx("truncate text-[13px]", d.shared && "text-muted")}>{d.shared ? t("cc.shared") : d.model || "—"}</span>
      {sub && <span className="truncate text-[11px] text-muted">{sub}</span>}
    </div>
  );
}

/** The app: its name in bold and what it reported as a User-Agent under it. What the app reported is data: always text. */
function App({ d, t }: { d: CompatDevice; t: Tx }) {
  if (!d.appTitle && !d.client) return <span className="text-[13px] text-faint">{t("cc.noClient")}</span>;
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate text-[13px] font-bold" title={d.appTitle ? undefined : d.client}>
        {d.appTitle || d.client}
      </span>
      {d.appTitle && d.client && (
        <span className="truncate font-mono text-[11px] text-muted" title={d.client}>
          {d.client}
        </span>
      )}
    </div>
  );
}

const State = ({ d, t }: { d: CompatDevice; t: Tx }) => <StatusPill sm kind={stateKind(d.state)} label={t(stateWord[d.state])} />;

const col = {
  person: "w-[13%] ps-4 pe-3 text-start",
  device: "w-[13%] pe-3 text-start",
  app: "w-[14%] pe-3 text-start",
  state: "w-[20%] pe-2 text-start",
  actual: "w-[17%] pe-2 text-start",
  need: "w-[23%] pe-4 text-start",
} as const;
const heads = ["person", "device", "app", "state", "actual", "need"] as const;

/**
 * The devices: a table from 700px of the tab's own width, cards below. The old rows stay (dimmed) while the next page loads.
 * A page is keyed by position: a device has no id on a shared link place, and a person can have several of them.
 */
export function DevicesTable({ rows, windowHours, pager }: { rows: readonly CompatDevice[]; windowHours: number; pager: Pager }) {
  const t = useTx();
  const fmt = useFmt();
  const seen = (d: CompatDevice) => (d.lastSeenUnix ? fmt.ago(d.lastSeenUnix) : "—");
  const busy = pager.busy;
  return (
    <>
      <Card lg className="hidden overflow-hidden @min-[700px]:block">
        <table className="w-full table-fixed text-start" aria-label={t("cc.table")}>
          <thead>
            <tr className="h-10 border-b border-line text-[11px] font-bold tracking-[0.06em] text-muted uppercase">
              {heads.map((h) => (
                <th key={h} scope="col" className={cx("truncate font-bold", col[h])}>
                  {t(`cc.col.${h}`)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className={cx("transition-opacity duration-200", busy && "opacity-60")} aria-busy={busy || undefined}>
            {rows.map((d, i) => (
              <tr key={`${d.userId}:${d.deviceId}:${i}`} className="border-b border-line align-top last:border-b-0">
                <td className={cx("py-3", col.person)}>
                  <Person d={d} />
                  <span className="mt-0.5 block truncate text-[11px] text-muted" title={t("cc.col.seen")}>
                    {seen(d)}
                  </span>
                </td>
                <td className={cx("py-3", col.device)}>
                  <Device d={d} t={t} />
                </td>
                <td className={cx("py-3", col.app)}>
                  <App d={d} t={t} />
                </td>
                <td className={cx("py-3", col.state)}>
                  <State d={d} t={t} />
                </td>
                <td className={cx("py-3", col.actual)}>
                  <ActualCell d={d} windowHours={windowHours} t={t} />
                </td>
                <td className={cx("py-3 text-xs leading-snug text-pretty text-muted", col.need)}>{needText(t, d)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <Pagination className="px-4" {...pager} />
      </Card>
      <div className="flex flex-col gap-2 @min-[700px]:hidden">
        <div className={cx("flex flex-col gap-2 transition-opacity duration-200", busy && "opacity-60")} aria-busy={busy || undefined}>
          {rows.map((d, i) => (
            <Card key={`${d.userId}:${d.deviceId}:${i}`} lg className="flex min-w-0 flex-col gap-2.5 p-3.5">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <Person d={d} />
                </div>
                <State d={d} t={t} />
              </div>
              <Device d={d} t={t} />
              <App d={d} t={t} />
              <p className="text-xs leading-snug text-pretty text-muted">{needText(t, d)}</p>
              <ActualCard d={d} windowHours={windowHours} t={t} />
              <span className="text-[11px] text-faint">
                {t("cc.col.seen")}: {seen(d)}
              </span>
            </Card>
          ))}
        </div>
        {pager.total > Math.min(...(pager.sizes ?? [pager.size])) && (
          <Card lg className="px-4">
            <Pagination flush {...pager} />
          </Card>
        )}
      </div>
    </>
  );
}
