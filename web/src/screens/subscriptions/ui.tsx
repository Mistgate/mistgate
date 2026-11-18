import { Button } from "@/components/ui/button";
import { useTx } from "@/screens/users/t";

/** The text box the Subscriptions forms share (40px, the page background, an accent border on focus). */
export const inputCls =
  "h-10 w-full min-w-0 rounded-field border border-line bg-canvas px-3 text-[13px] text-fg outline-none transition-colors duration-200 placeholder:text-faint focus:border-accent aria-invalid:border-danger";

/** The bar that appears while a form has unsaved changes, pinned above the dock on the phone. */
export function SaveBar({ busy, canSave, onSave, onDiscard }: { busy: boolean; canSave: boolean; onSave: () => void; onDiscard: () => void }) {
  const t = useTx();
  return (
    <div className="screen-enter sticky bottom-[84px] z-[3] flex flex-wrap items-center gap-2 rounded-card border border-accent-line bg-surface py-2 pe-2 ps-4 md:bottom-4">
      <span className="min-w-[140px] flex-1 text-[13px] font-bold">{t("subs.dirty")}</span>
      <Button variant="ghost" onClick={onDiscard} disabled={busy}>
        {t("subs.discard")}
      </Button>
      <Button variant="primary" disabled={!canSave || busy} onClick={onSave}>
        {t("subs.save")}
      </Button>
    </div>
  );
}
