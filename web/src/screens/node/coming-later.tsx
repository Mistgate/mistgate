import type { ReactNode } from "react";
import { Chip } from "@/components/ui/bits";
import { EmptyState } from "@/components/ui/notice";
import { useT } from "@/i18n";

/**
 * A part of a screen that has no data yet (doctor, synthetic checks, updates): it keeps its place and
 * says so calmly. Nothing on it is made up.
 */
export function ComingLater({ title, children, compact }: { title: string; children: ReactNode; compact?: boolean }) {
  const t = useT();
  return (
    <div className="rounded-card-lg border border-dashed border-line">
      <EmptyState title={title} className={compact ? "py-6" : undefined} action={<Chip>{t("common.comingLater")}</Chip>}>
        {children}
      </EmptyState>
    </div>
  );
}
