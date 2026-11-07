import { useQuery } from "@tanstack/react-query";
import { Icon } from "@/components/ui/icons";
import { ApprovalState } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT } from "@/i18n";
import { approvalsQuery, useTick } from "@/lib/integrations";
import { ApprovalCard } from "@/screens/integrations/approvals";

/**
 * The approval of a plan that waits for the owner, right under its card: the same form as AI → Waiting for you (for a node
 * install: the host key to compare, the login choice, the password in a secure field, Approve and Reject), with the same
 * calls. The assistant sees none of it: it learns the outcome from the plan's state, never a value. Owner only (the caller
 * checks); once decided it is a calm line with the result.
 */
export function InlineApproval({ planId, onDecided }: { planId: string; onDecided: () => void }) {
  const t = useT();
  const q = useQuery(approvalsQuery);
  const a = q.data?.approvals.find((x) => x.id === planId);
  const nowMs = useTick(!!a && a.state === ApprovalState.AWAITING);
  if (!q.data || !a) return null;
  if (a.state === ApprovalState.AWAITING) {
    return (
      <div data-slot="inline-approval" className="chat-in w-full">
        <ApprovalCard a={a} data={q.data} nowMs={nowMs} onDecided={onDecided} />
      </div>
    );
  }
  const approved = a.state === ApprovalState.APPROVED || a.state === ApprovalState.APPLIED;
  return (
    <p data-slot="inline-approval" className="flex items-center gap-2 text-[13px] text-ok-text">
      <Icon name={approved ? "check" : "info"} size={14} className="flex-none" />
      {approved ? t("ai.plan.confirmedByOwner") : t("ai.plan.rejectedByOwner")}
      {a.result ? <span className="text-muted">· {a.result}</span> : null}
    </p>
  );
}
