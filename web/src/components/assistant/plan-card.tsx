import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { ApprovalState } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT } from "@/i18n";
import type { Plan } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { dangerText, factLabel, factWords, outcomeText, planTitle, planTitleWords, toolTitle, type Fact, type Piece } from "@/lib/integrations";
import { I, type Glyph } from "./as-icons";

/**
 * A change the assistant prepared (ADR 0014), as the board's card (.as-pl): what it is and when it runs out, the facts as a plain two-column
 * list, what makes it risky, and the buttons; once decided, one line says how it ended. In the panel's own words, with the same facts and danger
 * notes as an approval. Only this card's button applies it; the model never can. A plan that needs the owner is approved and applied in one step
 * by the owner (with a fresh step-up), and waits in the approval inbox for anyone else.
 */
export function PlanCardView({ plan, owner, busy, onApply, onDismiss, onOpenApproval, inline }: { plan: Plan; owner: boolean; busy: boolean; onApply: () => void; onDismiss: () => void; onOpenApproval?: () => void; inline?: ReactNode }) {
  const t = useT();
  const fmt = useFmt();
  const open = plan.state === PlanState.PROPOSED;
  const title: Piece[] = planTitleWords(t, fmt, plan.tool, plan.facts as unknown as Fact[]) ?? [planTitle(t, plan.tool) ?? (plan.summary || toolTitle(t, plan.tool))];
  const status = statusLine(t, fmt, plan);
  // a plan that waits for the owner is decided on the approval screen (for an install, where the host key is compared and the
  // password typed: never in the chat): this button opens that screen at this plan
  const openApproval = owner && !!onOpenApproval && ((open && plan.inboxOnly) || plan.state === PlanState.WAITING_OWNER);
  const [now] = useState(() => Math.floor(Date.now() / 1000));
  const left = plan.expiresUnix > 0 ? Math.max(60, plan.expiresUnix - now) : 0;
  // the border calms down once it is decided; a plan that still waits keeps the accent line
  const decided = plan.state !== PlanState.PROPOSED && plan.state !== PlanState.WAITING_OWNER;
  const glyph: Glyph = plan.state === PlanState.APPLIED ? "check" : plan.state === PlanState.WAITING_OWNER ? "clock" : plan.state === PlanState.FAILED ? "warn" : plan.state === PlanState.REJECTED || plan.state === PlanState.DISMISSED ? "x" : "info";
  return (
    <div data-slot="plan" className={cx("as-pl", decided && (plan.state === PlanState.APPLIED ? "ok" : "no"))}>
      <div className="as-plh">
        <span>{t("ai.plan.title")}</span>
        {open && left > 0 && (
          <span className="as-mn" title={t("ai.plan.expires", { time: fmt.clock(plan.expiresUnix) })}>
            {t("as.plan.expires", { time: fmt.duration(left) })}
          </span>
        )}
      </div>
      {/* the board's title is what the change does, in the interface language, with the names it is about ("Turn on the relay of fi1 through msk1"); a plan with none says it in the tool's general words (the panel's English summary is for the model) */}
      <b>
        {title.map((p, i) =>
          typeof p === "string" ? (
            p
          ) : (
            // a name from data: plain in the title, with the panel's note that it is untrusted on hover
            <span key={i} title={t("int.ap.untrusted")}>
              {p.data}
            </span>
          ),
        )}
      </b>
      {plan.facts.length > 0 && (
        <ul>
          {plan.facts.map((f, i) => {
            const fact = f as unknown as Fact;
            return (
              <li key={i}>
                <span className="as-mn">{factLabel(t, fact.key)}</span>
                <span>
                  {factWords(t, fmt, fact).map((p, j) =>
                    typeof p === "string" ? (
                      <span key={j}>{p}</span>
                    ) : (
                      // text a person or a node supplied: plain, with the panel's note that it is untrusted on hover
                      <span key={j} title={t("int.ap.untrusted")} className="as-un">
                        {p.data}
                      </span>
                    ),
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {/* the owner's own button says "Approve and apply"; the line is for the one who cannot approve */}
      {plan.needsApproval && !owner && (
        <p className="as-w">
          <I g="warn" />
          {t("ai.plan.needsOwner")}
        </p>
      )}
      {plan.danger.map((d) => (
        <p key={d} className="as-w">
          <I g="warn" />
          {dangerText(t, d)}
        </p>
      ))}
      {open && plan.inboxOnly && <p className="as-note">{t("ai.plan.inbox")}</p>}
      {openApproval && (
        <div className="as-pa">
          {open && (
            <Button type="button" size="md" disabled={busy} onClick={onDismiss}>
              {t("ai.plan.dismiss")}
            </Button>
          )}
          <Button type="button" size="md" variant={inline ? "secondary" : "primary"} onClick={onOpenApproval}>
            {inline ? t("ai.plan.openWaiting") : t("ai.plan.openConfirm")}
          </Button>
        </div>
      )}
      {open && !plan.inboxOnly && (
        <div className="as-pa">
          <Button type="button" size="md" disabled={busy} onClick={onDismiss}>
            {t("ai.plan.dismiss")}
          </Button>
          <Button type="button" size="md" variant="primary" disabled={busy} onClick={onApply}>
            {busy && <span className="as-sp" aria-hidden />}
            {busy ? t("as.plan.busy") : plan.needsApproval && owner ? t("ai.plan.approveApply") : t("ai.plan.apply")}
          </Button>
        </div>
      )}
      {openApproval && inline && <div className="as-inline">{inline}</div>}
      {!open && status && (
        <div className={cx("as-ps", plan.state === PlanState.FAILED && "bad")}>
          <I g={glyph} />
          <span>{status}</span>
        </div>
      )}
    </div>
  );
}

function statusLine(t: ReturnType<typeof useT>, fmt: ReturnType<typeof useFmt>, p: Plan): string {
  switch (p.state) {
    case PlanState.WAITING_OWNER:
      return t("ai.plan.waiting");
    case PlanState.APPLIED: {
      // what came of it in the interface language (the code and its values); the panel's English line only for a code with no words
      const said = outcomeText(t, fmt, { state: ApprovalState.APPLIED, outcomeCode: p.outcomeCode, outcomeParams: p.outcomeParams });
      const result = said ?? p.result;
      return result ? `${t("ai.plan.applied")}: ${result}` : t("ai.plan.applied");
    }
    case PlanState.FAILED:
      return t("ai.plan.failed", { error: outcomeText(t, fmt, { state: ApprovalState.FAILED, outcomeCode: p.outcomeCode, outcomeParams: p.outcomeParams }) ?? (p.error || "—") });
    case PlanState.REJECTED:
      return t("ai.plan.rejected");
    case PlanState.DISMISSED:
      return t("as.plan.no");
    case PlanState.EXPIRED:
      return t("ai.plan.expired");
    default:
      return "";
  }
}
