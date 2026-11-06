import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { Plan } from "@/lib/assistant";
import { useFmt } from "@/lib/format";
import { dangerText, toolTitle, type Fact } from "@/lib/integrations";
import { cx } from "@/lib/cx";
import { FactRow } from "@/screens/integrations/approvals";

/**
 * A change the assistant prepared (ADR 0014): in the panel's own words, with the same facts and danger notes as an approval.
 * Only this card's button applies it; the model never can. A plan that needs the owner is approved and applied in one step
 * by the owner (with a fresh step-up), and waits in the approval inbox for anyone else.
 */
export function PlanCardView({ plan, owner, busy, onApply, onDismiss }: { plan: Plan; owner: boolean; busy: boolean; onApply: () => void; onDismiss: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const open = plan.state === PlanState.PROPOSED;
  const status = statusLine(t, plan);
  return (
    <div className={cx("flex flex-col gap-2.5 rounded-field border p-3", open ? "border-accent-line bg-accent-soft" : "border-line bg-surface-2")}>
      <div className="flex items-start gap-2">
        <Icon name="sliders" size={15} className="mt-0.5 flex-none text-accent-text" />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.plan.title")}</span>
          <b className="text-[13.5px] leading-snug">{toolTitle(t, plan.tool)}</b>
          {plan.summary && <span className="text-[13px] leading-snug text-pretty">{plan.summary}</span>}
        </div>
      </div>
      {plan.facts.length > 0 && (
        <dl className="flex flex-col gap-1.5 border-t border-line pt-2.5">
          {plan.facts.map((f, i) => (
            <FactRow key={i} fact={f as unknown as Fact} />
          ))}
        </dl>
      )}
      {(plan.danger.length > 0 || plan.needsApproval) && (
        <div className="flex flex-wrap gap-1.5">
          {plan.needsApproval && <span className="tone-warn tint rounded-ctl px-2 py-0.5 text-[11px] font-bold">{t("ai.plan.needsOwner")}</span>}
          {plan.danger.map((d) => (
            <span key={d} className="rounded-ctl bg-surface px-2 py-0.5 text-[11px] font-semibold text-muted">
              {dangerText(t, d)}
            </span>
          ))}
        </div>
      )}
      {open && plan.inboxOnly && <p className="text-xs leading-snug text-muted">{t("ai.plan.inbox")}</p>}
      {open && !plan.inboxOnly && (
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" size="sm" variant="primary" disabled={busy} onClick={onApply}>
            {plan.needsApproval && owner ? t("ai.plan.approveApply") : t("ai.plan.apply")}
          </Button>
          <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onDismiss}>
            {t("ai.plan.dismiss")}
          </Button>
          {plan.expiresUnix > 0 && <span className="ms-auto text-[11px] text-faint">{t("ai.plan.expires", { time: fmt.clock(plan.expiresUnix) })}</span>}
        </div>
      )}
      {open && plan.inboxOnly && null}
      {!open && status && (
        <p className={cx("flex items-center gap-1.5 text-xs font-semibold", plan.state === PlanState.APPLIED ? "text-ok-text" : plan.state === PlanState.FAILED ? "text-danger-text" : "text-muted")}>
          <Icon name={plan.state === PlanState.APPLIED ? "check" : plan.state === PlanState.WAITING_OWNER ? "clock" : "info"} size={13} />
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
