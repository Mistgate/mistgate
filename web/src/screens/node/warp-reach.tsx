import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { StatusPill } from "@/components/ui/status";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useIsOwner } from "@/lib/updates";
import { useNow } from "@/lib/time";
import { Glyph, glyphs } from "@/screens/overview-parts";
import { useTx } from "@/screens/users/t";
import { Spin, Wa } from "./parts";
import { useAge } from "./warp";
import { warpQuery } from "./warp-model";
import { reachCounts, reachLook, verdictKind, type ReachLook, type ReachRow, type ReachVerdict } from "./warp-reach-model";
import "./exit.css";
import "./warp-reach.css";

// The reach card of the Exit tab, under the WARP card: which sites show a block page through this node's WARP (GetWarpResponse.reach). The
// panel asks the node by itself every 15 minutes; the card shows the last answer, one row a site, and says plainly when there is none
// (not asked yet, the node cannot, the owner switched it off, WARP is paused or the node is silent). Built of the WARP card's own head,
// rows and pills so it reads as a part of it.

const iconGlobe = "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18";
const iconRefresh = "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4";
const iconInfo = "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17zM12 11v5M12 8v.01";
const iconCross = "M6 6l12 12M18 6L6 18";
const iconDash = "M6 12h12";
const verdictGlyph: Record<ReachVerdict, string> = { ok: glyphs.check, blocked: iconCross, unreachable: iconDash, error: glyphs.warn };
/** The WARP card's own button size (36px, 13px). */
const ctl = "!h-9 !px-3 !text-[13px]";
const verdictTone: Record<ReachVerdict, string> = { ok: "ok", blocked: "bad", unreachable: "warn", error: "warn" };

/** The node's WARP and the sites it is checked against. Nothing without a WARP account: the WARP card above says what to do. */
export function WarpReachCard({ nodeId, down }: { nodeId: string; down?: boolean }) {
  const q = useQuery(warpQuery(nodeId));
  const now = Math.floor(useNow(30_000) / 1000);
  const d = q.data;
  if (!d?.account) return null;
  const look = reachLook(d, !!down, now);
  return (
    <section className="nx-warp nrc relative rounded-card border border-line bg-surface" aria-label="WARP reach" data-state={look.state} aria-busy={q.isFetching || undefined}>
      <Body look={look} fetching={q.isFetching} onRefresh={() => void q.refetch()} />
    </section>
  );
}

function Body({ look, fetching, onRefresh }: { look: ReachLook; fetching: boolean; onRefresh: () => void }) {
  const t = useTx();
  const np = useT();
  const age = useAge();
  const owner = useIsOwner();
  const { state, rows, colo, checked, paused, dead, stale } = look;
  const c = reachCounts(rows);
  const shows = state === "ready";
  const dim = shows && (paused || dead || stale);

  const pill =
    state === "old" ? (
      <StatusPill kind="off" label={t("warp.reach.p.old")} />
    ) : state === "off" ? (
      <StatusPill kind="off" label={t("warp.reach.p.off")} />
    ) : paused ? (
      <StatusPill kind="off" label={t("warp.reach.p.paused")} />
    ) : dead ? (
      <StatusPill kind="bad" label={np("np.wp.state.dead")} />
    ) : state === "wait" ? (
      <StatusPill kind="busy" label={t("warp.reach.p.wait")} />
    ) : state === "nodata" ? (
      <StatusPill kind="off" label={t("warp.reach.p.nodata")} />
    ) : c.blocked > 0 ? (
      <StatusPill kind="bad" label={t("warp.reach.p.blocked", { n: c.blocked, m: c.total })} />
    ) : c.trouble > 0 ? (
      <StatusPill kind="warn" label={t("warp.reach.p.trouble", { n: c.trouble, m: c.total })} />
    ) : (
      <StatusPill kind="ok" label={t("warp.reach.p.ok")} />
    );
  const sub =
    shows && checked > 0 ? t(colo ? "warp.reach.sub.colo" : "warp.reach.sub.ago", { colo, ago: age(checked) }) : state === "old" ? t("warp.reach.sub.old") : state === "off" ? t("warp.reach.sub.off") : t("warp.reach.sub.none");

  return (
    <>
      <div className="nx-ch">
        <span className="nd-wi nrc-wi" aria-hidden>
          <Glyph d={iconGlobe} size={20} stroke={1.5} />
        </span>
        <div className="nd-wt">
          <h3>{t("warp.reach.title")}</h3>
          <span>{sub}</span>
        </div>
        <span className="pillw">{pill}</span>
      </div>

      {state === "old" && (
        <Note title={t("warp.reach.old.t")} body={t("warp.reach.old.b")}>
          <Link to="/updates" className={cx(buttonClass("secondary", "lg"), ctl)}>
            {t("warp.openUpdates")}
          </Link>
        </Note>
      )}
      {state === "off" && (
        <Note title={t("warp.reach.off.t")} body={t("warp.reach.off.b")}>
          {owner && (
            <Link to="/settings/$section" params={{ section: "system" }} className={cx(buttonClass("secondary", "lg"), ctl)}>
              {t("warp.reach.list")}
            </Link>
          )}
        </Note>
      )}
      {(state === "wait" || state === "nodata") && !paused && !dead && <Note title={t(state === "wait" ? "warp.reach.wait.t" : "warp.reach.nodata.t")} body={t(state === "wait" ? "warp.reach.wait.b" : "warp.reach.nodata.b")} busy={state === "wait"} />}

      {state !== "old" && state !== "off" && paused && (
        <Wa tone="warn" title={t("warp.reach.paused.t")} className="nrc-wa">
          {t(shows ? "warp.reach.paused.last" : "warp.reach.paused.b")}
        </Wa>
      )}
      {state !== "old" && state !== "off" && dead && !paused && (
        <Wa tone="crit" title={np("np.wp.dead")} className="nrc-wa">
          {t(shows ? "warp.reach.dead.last" : "warp.reach.dead.b")}
        </Wa>
      )}
      {shows && stale && !paused && !dead && (
        <Wa tone="warn" title={t("warp.reach.stale.t")} className="nrc-wa">
          {t("warp.reach.stale.b")}
        </Wa>
      )}

      {shows && rows.length > 0 && (
        <ul className={cx("nrc-ls", dim && "dim")} aria-label={t("warp.reach.title")}>
          {rows.map((r) => (
            <Row key={r.domain} r={r} />
          ))}
        </ul>
      )}

      {shows && c.blocked > 0 && !paused && !dead && (
        <p className="nrc-tip">
          <Glyph d={glyphs.warn} size={16} stroke={1.5} />
          <span>{t("warp.reach.fix")}</span>
        </p>
      )}

      {state !== "old" && state !== "off" && (
        <div className="nrc-act">
          <Button variant="secondary" size="lg" className={ctl} disabled={fetching} title={t("warp.reach.refreshHint")} onClick={onRefresh}>
            {fetching ? <Spin /> : <Glyph d={iconRefresh} size={16} stroke={1.5} />}
            {t("warp.reach.refresh")}
          </Button>
          {owner && (
            <Link to="/settings/$section" params={{ section: "system" }} className="nrc-lk">
              {t("warp.reach.list")}
            </Link>
          )}
        </div>
      )}

      <p className="nx-warn">
        <Glyph d={iconInfo} size={16} stroke={1.5} />
        <span>{t("warp.reach.every")}</span>
      </p>
    </>
  );
}

/** A quiet note in place of the list: what there is to know and, when there is one, where to go. */
function Note({ title, body, busy, children }: { title: string; body: string; busy?: boolean; children?: ReactNode }) {
  return (
    <div className="nrc-note" role={busy ? "status" : undefined}>
      <span className="g" aria-hidden>
        {busy ? <Spin className="size-4" /> : <Glyph d={iconInfo} size={18} stroke={1.5} />}
      </span>
      <div>
        <b>{title}</b>
        <span>{body}</span>
        {children && <div className="nrc-row">{children}</div>}
      </div>
    </div>
  );
}

/** What the node saw on one site, in a line under its name: the status, how long it took, why it is not "reachable". */
function detail(t: ReturnType<typeof useTx>, r: ReachRow): string {
  const http = r.status > 0 ? t("warp.reach.d.http", { status: r.status }) : "";
  const ms = r.ms > 0 ? t("warp.reach.d.ms", { ms: r.ms }) : "";
  switch (r.verdict) {
    case "ok":
      return [http, ms].filter(Boolean).join(" · ");
    case "blocked":
      return [http, t("warp.reach.d.blocked"), r.streak >= 2 ? t.n("warp.reach.d.streak", r.streak) : ""].filter(Boolean).join(" · ");
    case "unreachable":
      return t("warp.reach.d.none");
    default:
      // a bare 403 is the one people ask about: not a block page the panel knows, so it is not called a block
      return [http, t(r.status === 403 ? "warp.reach.d.e403" : r.status === 429 ? "warp.reach.d.e429" : r.status >= 500 ? "warp.reach.d.e5xx" : "warp.reach.d.eother")].join(" · ");
  }
}

function Row({ r }: { r: ReachRow }) {
  const t = useTx();
  const tone = verdictTone[r.verdict];
  return (
    <li className="nrc-li" data-verdict={r.verdict}>
      <span className={cx("g", tone)} aria-hidden>
        <Glyph d={verdictGlyph[r.verdict]} size={16} stroke={2} />
      </span>
      <div className="d">
        <b className="font-mono" dir="ltr">
          {r.domain}
        </b>
        <em>{detail(t, r)}</em>
      </div>
      <StatusPill kind={verdictKind[r.verdict]} sm label={t(`warp.reach.v.${r.verdict}`)} />
    </li>
  );
}
