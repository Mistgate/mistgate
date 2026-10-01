import { SectionLabel } from "@/components/ui/bits";
import { useT } from "@/i18n";
import { CopyRow } from "./domains";

// The data directory whole: the database and the master key (the default --data-dir).
const backupCommand = "tar czf mistgate-backup-$(date +%F).tgz -C /var/lib mistgate";

/** Settings -> Backups: there are none yet, so it says so and how to make a copy by hand. */
export function BackupsPage() {
  const t = useT();
  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
      <SectionLabel as="h2" icon="warn" tone="rose">
        {t("set.backups.title")}
      </SectionLabel>
      <p className="text-[13px] leading-normal text-pretty">{t("set.backups.body")}</p>
      <div className="flex flex-col gap-1.5">
        <span className="text-xs text-muted">{t("set.backups.cmd")}</span>
        <CopyRow value={backupCommand} />
      </div>
      <p className="text-xs leading-normal text-pretty text-muted">{t("set.backups.key")}</p>
    </section>
  );
}
