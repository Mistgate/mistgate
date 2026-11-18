import { Fragment, useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Pagination } from "@/components/ui/pagination";
import { ApprovalState, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { clampPage, usePaging } from "@/lib/paging";
import { useNodes } from "@/lib/queries";
import { safeHttpUrl } from "@/lib/warp";
import {
  approvalStateInfo,
  clockText,
  dangerText,
  factLabel,
  factWords,
  outcomeText,
  profileInfo,
  secondsLeft,
  toolTitle,
  useApprovalActions,
  useTick,
  type Approval,
  type Approvals,
} from "@/lib/integrations";
import { ApprovalCard as InstallCard } from "@/screens/integrations/approvals";
import { Empty, Gl, ICONS, Pill, Sec, useFresh, useScene, type Kind } from "./bits";

// What an agent asked for and waits on the owner for, and what was decided lately, as the board draws them (PnAI "Ждут тебя"): one card per
// change with the time left, the facts in the panel's words, the agent's reason apart, and Reject / Approve (Approve asks for a passkey or
// a code once more). A node install (the host key, the server's login) is the Integrations screen's own card, put in the same frame.

const kindOf = (k: string): Kind => (k === "ok" ? "ok" : k === "warn" ? "part" : k === "bad" ? "bad" : k === "busy" ? "busy" : "");

export function ApprovalsTab({ data, focus }: { data: Approvals; focus?: string }) {
  const t = useT();
  const waiting = data.approvals.filter((a) => a.state === ApprovalState.AWAITING);
  const nowMs = useTick(waiting.length > 0);
  const live = waiting.filter((a) => secondsLeft(a, data, nowMs) > 0);
  const lapsed = waiting.length - live.length;
  return (
    <div className="sb-tab">
      {live.length === 0 && (
        <Empty
          calm
          d={ICONS.shield}
          title={t(lapsed > 0 ? "aip.ap.lapsedT" : "aip.ap.noneT")}
          body={t(lapsed > 0 ? "aip.ap.lapsedBody" : "aip.ap.noneBody")}
        />
      )}
      {live.map((a) => (
        <Plan key={a.id} a={a} data={data} nowMs={nowMs} focused={a.id === focus} />
      ))}
      <Recent data={data} />
    </div>
  );
}

function Plan({ a, data, nowMs, focused }: { a: Approval; data: Approvals; nowMs: number; focused: boolean }) {
  // a node install is the Integrations screen's card: the owner confirms the host key and picks the login there
  if (a.tool === "node_install") return <InstallCard a={a} data={data} nowMs={nowMs} focused={focused} />;
  return <Card a={a} data={data} nowMs={nowMs} focused={focused} />;
}

function Card({ a, data, nowMs, focused }: { a: Approval; data: Approvals; nowMs: number; focused: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const { approve, reject, busy } = useApprovalActions();
  const left = secondsLeft(a, data, nowMs);
  const span = Math.max(1, a.expiresUnix - a.createdUnix);
  const soon = left < 60;
  const tool = toolTitle(t, a.tool);
  const [done, setDone] = useState(false);
  // a node of the panel is drawn with its flag, as the board does; a name the panel does not know stays on its plate of "data"
  const flags = new Map((useNodes().data?.nodes ?? []).map((n) => [n.name, n.countryCode]));
  return (
    <section
      id={`approval-${a.id}`}
      ref={(el) => {
        // opened from the assistant's card ("Open and confirm"): this plan is brought into view once
        if (el && focused) el.scrollIntoView?.({ block: "center", behavior: "smooth" });
      }}
      data-focused={focused || undefined}
      className={cx("card ai-ap", done && "done")}
      style={focused ? { boxShadow: "0 0 0 2px var(--accent-mark)" } : undefined}
    >
      <div className="ai-aph">
        <span className="pp-ic sand" aria-hidden="true">
          <Gl d={ICONS.shield} />
        </span>
        <div>
          <h3>{tool}</h3>
          <span>
            {t("int.ap.by", { token: a.tokenName })}
            <i className={cx("pv-pill", a.tokenProfile === TokenProfile.OPERATOR ? "part" : a.tokenProfile === TokenProfile.ADMIN ? "bad" : "")}>{t(profileInfo(a.tokenProfile).key)}</i>
          </span>
        </div>
        <span className={cx("ai-left", soon && "soon")}>
          <em>{t("aip.ap.left")}</em>
          <b className="mono">{clockText(left)}</b>
        </span>
      </div>
      <i className={cx("rw-pg ai-apg", soon && "soon")} style={{ ["--w" as string]: `${Math.round((left / span) * 100)}%` }} aria-hidden="true" />
      <p className="ai-apl">{t("aip.ap.lead")}</p>
      {a.facts.length > 0 && (
        <dl className="uq-dl ai-dl ai-apd">
          {a.facts.map((f, i) => {
            const termsUrl = f.key === "terms_url" ? safeHttpUrl(f.value) : "";
            return (
              <div key={`${f.key}-${i}`}>
                <dt>{factLabel(t, f.key)}</dt>
                <dd>
                  {termsUrl ? (
                    <a href={termsUrl} target="_blank" rel="noopener noreferrer">
                      {t("warp.tosLink")}
                    </a>
                  ) : f.key === "node" && flags.has(f.value) ? (
                    <span className="hl-cnode">
                      <Flag code={flags.get(f.value) ?? ""} size={12} />
                      <b className="mono">{f.value}</b>
                    </span>
                  ) : (
                    factWords(t, fmt, f).map((p, j) =>
                      typeof p === "string" ? (
                        <Fragment key={j}>{p}</Fragment>
                      ) : (
                        <span key={j} className="ai-fv" title={t("int.ap.untrusted")}>
                          {t("common.quoted", { name: p.data })}
                        </span>
                      ),
                    )
                  )}
                </dd>
              </div>
            );
          })}
        </dl>
      )}
      {a.danger.length > 0 && (
        <ul className="ai-risk">
          {a.danger.map((code) => (
            <li key={code}>
              <Gl d={ICONS.warn} />
              {dangerText(t, code)}
            </li>
          ))}
        </ul>
      )}
      {a.reason && (
        <blockquote className="ai-q">
          <span>{t("int.ap.reason")}</span>
          <p>{t("common.quoted", { name: a.reason })}</p>
          <em>{t("int.ap.reason.note")}</em>
        </blockquote>
      )}
      <div className="ai-apa">
        <em>{t("int.ap.approve.hint")}</em>
        <Button size="lg" disabled={busy} aria-label={t("int.ap.reject.aria", { tool })} onClick={() => reject.mutate(a.id)}>
          {t("int.ap.reject")}
        </Button>
        <Button
          size="lg"
          variant="primary"
          disabled={busy}
          aria-label={t("int.ap.approve.aria", { tool })}
          onClick={() =>
            approve.mutate(
              { id: a.id },
              {
                onSuccess: () => {
                  halo("save");
                  setDone(true);
                },
              },
            )
          }
        >
          {approve.isPending && <span className="an-sp" aria-hidden="true" />}
          {approve.isPending ? t("aip.ap.waitKey") : t("int.ap.approve")}
        </Button>
      </div>
    </section>
  );
}

const pageSize = 10;

/** What was decided and what came of it, newest first, ten to a page (the panel sends the latest hundred). */
function Recent({ data }: { data: Approvals }) {
  const t = useT();
  const fmt = useFmt();
  const paging = usePaging({ sizes: [pageSize], defaultSize: pageSize });
  const rows = data.approvals.filter((a) => a.state !== ApprovalState.AWAITING);
  // the row that has just been decided flashes once (the board's "just"); a scene of the dev kit opens with it flashing
  const scene = useScene();
  const fresh = useFresh(rows.map((a) => a.id)) || (scene.has("done") || scene.has("denied") ? (rows[0]?.id ?? "") : "");
  if (rows.length === 0) return null;
  const page = clampPage(paging.page, rows.length, pageSize);
  const shown = rows.slice((page - 1) * pageSize, page * pageSize);
  return (
    <Sec tone="sky" d={ICONS.history} title={t("int.ap.history")}>
      <div className="ai-recs">
        {shown.map((a) => {
          const s = approvalStateInfo(a.state);
          const at = a.appliedUnix || a.decidedUnix || a.createdUnix;
          // our words for the outcome where the panel's code has them, else the panel's own line
          const said = outcomeText(t, fmt, a);
          const res = a.error ? (said ?? a.error) : (said ?? a.result);
          return (
            <div key={a.id} className={cx("ai-rec", a.id === fresh && "fresh")}>
              <div>
                <b>{toolTitle(t, a.tool)}</b>
                <em>{[a.tokenName, a.decidedByName ? t("int.ap.decidedBy", { name: a.decidedByName }) : "", fmt.ago(at)].filter(Boolean).join(" · ")}</em>
              </div>
              {res ? <code className={cx("mono", a.error && "bad")}>{res}</code> : <span />}
              <Pill kind={kindOf(s.kind)}>{t(s.key)}</Pill>
            </div>
          );
        })}
      </div>
      <Pagination total={rows.length} page={page} size={pageSize} onPage={paging.setPage} label={t("int.ap.history")} />
    </Sec>
  );
}
