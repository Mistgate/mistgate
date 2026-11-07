import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { Plan } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { dangerText, factLabel, factWords, toolTitle, type Fact } from "@/lib/integrations";

/**
 * A change the assistant prepared (ADR 0014), as an attachment card: what it is and what it does, the facts as a plain
 * two-column list, and the buttons. In the panel's own words, with the same facts and danger notes as an approval. Only
 * this card's button applies it; the model never can. A plan that needs the owner is approved and applied in one step by the
 * owner (with a fresh step-up), and waits in the approval inbox for anyone else.
 */
export function PlanCardView({ plan, owner, busy, onApply, onDismiss, onOpenApproval, inline }: { plan: Plan; owner: boolean; busy: boolean; onApply: () => void; onDismiss: () => void; onOpenApproval?: () => void; inline?: ReactNode }) {
  const t = useT();
  const fmt = useFmt();
  const open = plan.state === PlanState.PROPOSED;
  const status = statusLine(t, plan);
  // a plan that waits for the owner is decided on the approval screen (for an install, where the host key is compared and the
  // password typed: never in the chat): this button opens that screen at this plan
  const openApproval = owner && !!onOpenApproval && ((open && plan.inboxOnly) || plan.state === PlanState.WAITING_OWNER);
  const [now] = useState(() => Math.floor(Date.now() / 1000));
  const left = plan.expiresUnix > 0 ? Math.max(60, plan.expiresUnix - now) : 0;
  return (
    <div data-slot="plan" className="chat-in flex flex-col gap-3 rounded-2xl bg-fg/[0.04] p-4 ring-1 ring-line/70">
      <div className="flex items-start gap-3">
        <span aria-hidden className="grid size-8 flex-none place-items-center rounded-[10px] bg-accent-soft text-accent-text">
          <Icon name="sliders" size={15} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[11px] font-medium text-muted">{t("ai.plan.title")}</span>
          <b className="text-[14px] leading-snug font-semibold">{toolTitle(t, plan.tool)}</b>
          {plan.summary && <span className="pt-0.5 text-[13px] leading-snug text-pretty text-muted">{plan.summary}</span>}
        </div>
      </div>
      {plan.facts.length > 0 && (
        <dl className="grid grid-cols-[minmax(0,7.5rem)_minmax(0,1fr)] gap-x-4 gap-y-1.5 border-t border-line/60 pt-3 text-[13px]">
          {plan.facts.map((f, i) => {
            const fact = f as unknown as Fact;
            return (
              <div key={i} className="contents">
                <dt className="text-xs leading-5 text-muted">{factLabel(t, fact.key)}</dt>
                <dd className="min-w-0 leading-5 text-pretty wrap-anywhere">
                  {factWords(t, fmt, fact).map((p, j) =>
                    typeof p === "string" ? (
                      <span key={j}>{p}</span>
                    ) : (
                      // text a person or a node supplied: plain, with the panel's note that it is untrusted on hover
                      <span key={j} title={t("int.ap.untrusted")} className="font-medium whitespace-pre-wrap decoration-faint decoration-dotted underline-offset-4 hover:underline">
                        {p.data}
                      </span>
                    ),
                  )}
                </dd>
              </div>
            );
          })}
        </dl>
      )}
      {(plan.danger.length > 0 || plan.needsApproval) && (
        <div className="flex flex-wrap gap-1.5">
          {plan.needsApproval && <span className="tone-warn tint rounded-full px-2.5 py-0.5 text-[11px] font-semibold">{t("ai.plan.needsOwner")}</span>}
          {plan.danger.map((d) => (
            <span key={d} className="rounded-full bg-fg/[0.06] px-2.5 py-0.5 text-[11px] text-muted">
              {dangerText(t, d)}
            </span>
          ))}
        </div>
      )}
      {open && plan.inboxOnly && <p className="text-xs leading-snug text-muted">{t("ai.plan.inbox")}</p>}
      {openApproval && (
        <div className="flex flex-wrap items-center gap-2 pt-0.5">
          <Button type="button" size="sm" variant={inline ? "ghost" : "primary"} onClick={onOpenApproval}>
            {inline ? t("ai.plan.openWaiting") : t("ai.plan.openConfirm")}
          </Button>
          {open && (
            <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onDismiss}>
              {t("ai.plan.dismiss")}
            </Button>
          )}
        </div>
      )}
      {open && !plan.inboxOnly && (
        <div className="flex flex-wrap items-center gap-2 pt-0.5">
          <Button type="button" size="sm" variant="primary" disabled={busy} onClick={onApply}>
            {plan.needsApproval && owner ? t("ai.plan.approveApply") : t("ai.plan.apply")}
          </Button>
          <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onDismiss}>
            {t("ai.plan.dismiss")}
          </Button>
          {left > 0 && (
            <span title={t("ai.plan.expires", { time: fmt.clock(plan.expiresUnix) })} className="ms-auto inline-flex items-center gap-1 rounded-full bg-fg/[0.06] px-2.5 py-1 text-[11px] text-muted">
              <Icon name="clock" size={11} />
              {t("ai.chat.valid", { time: fmt.duration(left) })}
            </span>
          )}
        </div>
      )}
      {openApproval && inline}
      {!open && status && (
        <p className={cx("flex items-center gap-2 text-[13px]", plan.state === PlanState.APPLIED ? "text-ok-text" : plan.state === PlanState.FAILED ? "text-danger-text" : "text-muted")}>
          <Icon name={plan.state === PlanState.APPLIED ? "check" : plan.state === PlanState.WAITING_OWNER ? "clock" : plan.state === PlanState.FAILED ? "warn" : "info"} size={14} className="flex-none" />
          <span className="min-w-0">{status}</span>
        </p>
      )}
    </div>
  );
}

function statusLine(t: ReturnType<typeof useT>, p: Plan): string {
  switch (p.state) {
    case PlanState.WAITING_OWNER:
      return t("ai.plan.waiting");
    case PlanState.APPLIED:
      return p.result ? `${t("ai.plan.applied")}: ${p.result}` : t("ai.plan.applied");
    case PlanState.FAILED:
      return t("ai.plan.failed", { error: p.error || "—" });
    case PlanState.REJECTED:
      return t("ai.plan.rejected");
    case PlanState.DISMISSED:
      return t("ai.plan.dismissed");
    case PlanState.EXPIRED:
      return t("ai.plan.expired");
    default:
      return "";
  }
}
