import { descKey, navKey, type SectionId } from "@/components/nav";
import { Card, PageTitle } from "@/components/ui/bits";
import { NavIcon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { useT } from "@/i18n";

/** Calm placeholder for a section that has no functionality yet. */
export function SectionScreen({ id }: { id: SectionId }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-4">
      <PageTitle>{t(navKey(id))}</PageTitle>
      <Card className="border-dashed">
        <EmptyState icon={<NavIcon name={id} size={20} />} title={t("section.empty")}>
          {t(descKey(id))}
        </EmptyState>
      </Card>
    </div>
  );
}
