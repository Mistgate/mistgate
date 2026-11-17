import { Link } from "@tanstack/react-router";
import type { ComponentProps } from "react";
import { Pagination } from "@/components/ui/pagination";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { needText, platformName, stateKind, stateWord, type CompatDevice } from "@/lib/clientcompat";
import { useTx, type Tx } from "@/screens/users/t";
import { ActualCell } from "./clients-actual";
import { Glyph } from "./parts";

type Pager = ComponentProps<typeof Pagination>;

const heads = ["person", "device", "app", "state", "actual"] as const;
const glyph = { ok: "ok", warn: "part", bad: "bad" } as const;

/** The pill of the table's verdict: the glyph, the colour and the word, and what is missing under it. */
function State({ d, t }: { d: CompatDevice; t: Tx }) {
  const kind = stateKind(d.state);
  const need = needText(t, d);
  return (
    <span className="sb-v2">
      <span>
        <span className={cx("pv-pill", kind === "ok" ? "ok" : kind === "warn" ? "part" : kind === "bad" ? "bad" : "")}>
          <Glyph kind={glyph[kind as keyof typeof glyph] ?? "none"} />
          {t(stateWord[d.state])}
        </span>
      </span>
      {need && <em>{need}</em>}
    </span>
  );
}

/**
 * The devices as the board draws them: a card of rows, five columns (the person with when they were last seen, the device, the app
 * with what it reported, the table's verdict with what is missing, what the nodes saw), the same grid in two columns on a phone.
 * The old rows stay (dimmed) while the next page loads. A page is keyed by position: a device has no id on a shared link place,
 * and a person can have several of them.
 */
export function DevicesTable({ rows, windowHours, pager, fingerprints, empty }: { rows: readonly CompatDevice[]; windowHours: number; pager: Pager; fingerprints: readonly string[]; empty: "ok" | "state" }) {
  const t = useTx();
  const fmt = useFmt();
  const seen = (d: CompatDevice) => (d.lastSeenUnix ? fmt.ago(d.lastSeenUnix) : "—");
  const busy = pager.busy;
  return (
    <section className="card sb-vt" data-shot="subs-clients-table">
      <table aria-label={t("cc.table")}>
        <thead>
          <tr className="sb-vr sb-vh">
            {heads.map((h) => (
              <th key={h} scope="col">
                {t(`cc.col.${h}`)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className={cx(busy && "busy")} aria-busy={busy || undefined}>
          {rows.map((d, i) => (
            <tr className="sb-vr" key={`${d.userId}:${d.deviceId}:${i}`}>
              <td>
                <span className="sb-v2">
                  <b>
                    <Link to="/users/$id" params={{ id: d.userId }}>
                      {d.userName || d.userId}
                    </Link>
                  </b>
                  <em title={t("cc.col.seen")}>{seen(d)}</em>
                </span>
              </td>
              <td>
                <span className="sb-v2">
                  <b className={cx(d.shared && "muted")}>{d.shared ? t("cc.shared") : d.model || "—"}</b>
                  <em>{[d.platform && platformName(d.platform), d.osVersion].filter(Boolean).join(" ")}</em>
                </span>
              </td>
              <td>
                {!d.appTitle && !d.client ? (
                  <span className="sb-v2">
                    <b className="muted">{t("cc.noClient")}</b>
                  </span>
                ) : (
                  <span className="sb-v2">
                    <b title={d.appTitle ? undefined : d.client}>{d.appTitle || d.client}</b>
                    {d.appTitle && d.client && (
                      <em className="mono" title={d.client}>
                        {d.client}
                      </em>
                    )}
                  </span>
                )}
              </td>
              <td>
                <State d={d} t={t} />
              </td>
              <td>
                <ActualCell d={d} windowHours={windowHours} t={t} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && (
        <div className="sb-nf">
          <p>{t(empty === "ok" ? "cc.empty.ok" : "cc.empty.state")}</p>
        </div>
      )}
      {fingerprints.length > 0 && <p className="sb-vfoot">{t("cc.fingerprints", { list: fingerprints.join(", ") })}</p>}
      {pager.total > Math.min(...(pager.sizes ?? [pager.size])) && <Pagination className="px-4" {...pager} />}
    </section>
  );
}
