import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { StatusPill } from "@/components/ui/status";
import { useT } from "@/i18n";
import { warp as warpApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useIsOwner } from "@/lib/updates";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
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
  const qc = useQueryClient();
  const now = Math.floor(useNow(30_000) / 1000);
  // what the last press found when the node could not be asked: the words stay until the next press
  const [refused, setRefused] = useState<{ asked: string; retryS: number } | null>(null);
  // "Check now" asks the panel to ask the node out of turn (GetWarpRequest.check_reach_now); the answer comes back at once and the card reads the result with its usual reads
  const check = useMutation({
    mutationFn: async () => plain(await warpApi.getWarp({ nodeId, checkReachNow: true })),
    onSuccess: (d) => {
      qc.setQueryData(warpQuery(nodeId).queryKey, d);
      const asked = d.reachCheck?.asked ?? "";
      setRefused(asked === "" || asked === "started" || asked === "running" ? null : { asked, retryS: d.reachCheck?.retryAfterS ?? 0 });
    },
  });
  const d = q.data;
  if (!d?.account) return null;
  const look = reachLook(d, !!down, now);
  const running = look.running || check.isPending;
  return (
    <section className="nx-warp nrc relative rounded-card border border-line bg-surface" aria-label="WARP reach" data-state={running ? "running" : look.state} aria-busy={running || q.isFetching || undefined}>
      <Body look={look} running={running} refused={refused} onCheck={() => check.mutate()} />
    </section>
  );
}

/** What the panel said when a press found the node unable to check: the sentence and, with "too soon", the time to wait. */
function refusedText(t: ReturnType<typeof useTx>, fmt: ReturnType<typeof useFmt>, r: { asked: string; retryS: number }): string {
  if (r.asked === "too_soon") return t("warp.reach.ask.too_soon", { when: fmt.duration(Math.max(r.retryS, 1)) });
  return t.opt(`warp.reach.ask.${r.asked}`) ?? t("warp.reach.last.failed");
}
/** How the newest check ended when it did not end well, in words. */
const lastText = (t: ReturnType<typeof useTx>, code: string) => t(code === "busy" ? "warp.reach.last.busy" : code === "too_soon" ? "warp.reach.last.too_soon" : code === "warp_down" ? "warp.reach.last.warp_down" : "warp.reach.last.failed");

function Body({ look, running, refused, onCheck }: { look: ReachLook; running: boolean; refused: { asked: string; retryS: number } | null; onCheck: () => void }) {
  const t = useTx();
  const np = useT();
  const fmt = useFmt();
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
    ) : running ? (
      <StatusPill kind="busy" label={t("warp.reach.p.running")} />
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
      {running && state !== "old" && state !== "off" && <Note title={t("warp.reach.running.t")} body={t("warp.reach.running.b")} busy />}
      {(state === "wait" || state === "nodata") && !paused && !dead && !running && <Note title={t(state === "wait" ? "warp.reach.wait.t" : "warp.reach.nodata.t")} body={t(state === "wait" ? "warp.reach.wait.b" : "warp.reach.nodata.b")} busy={state === "wait"} />}

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
          <Button variant="secondary" size="lg" className={ctl} disabled={running} title={t("warp.reach.refreshHint")} onClick={onCheck} data-testid="reach-check">
            {running ? <Spin /> : <Glyph d={iconRefresh} size={16} stroke={1.5} />}
            {t("warp.reach.refresh")}
          </Button>
          {owner && (
            <Link to="/settings/$section" params={{ section: "system" }} className="nrc-lk">
              {t("warp.reach.list")}
            </Link>
          )}
        </div>
      )}

      {state !== "old" && state !== "off" && !running && (refused || look.failed) && (
        <p className="nrc-tip" role="status" data-testid="reach-ask">
          <Glyph d={iconInfo} size={16} stroke={1.5} />
          <span>{refused ? refusedText(t, fmt, refused) : lastText(t, look.failed!.code)}</span>
        </p>
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
