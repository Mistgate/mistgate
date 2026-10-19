import { EditionNotice } from "@/components/edition-notice";
import { StatusDot } from "@/components/ui/status";
import { useT } from "@/i18n";
import { useEdition } from "@/lib/edition";
import { peopleOrder, type Alert } from "@/lib/health";
import { AlertCard } from "./alerts";
import type { FixFlow } from "./fix";

/**
 * People: the alerts the panel derives about a person, not a node (access ended but the app keeps asking; a key that
 * never connected or went stale; a quiet app). Warnings first, then info; each is an alert card with the person's name,
 * a button to their page and Mute. The Cloudflare edition derives none of them, so it says that instead of "all is well".
 */
export function PeopleTab({ active, now, flow }: { active: Alert[]; now: number; flow: FixFlow }) {
  const t = useT();
  const rows = peopleOrder(active);
  const noEval = useEdition().off("health_checks");
  return (
    <div className="flex flex-col gap-3.5">
      <EditionNotice feature="health_checks">{t("hl.people.edgeHint")}</EditionNotice>
      {rows.length === 0 && !noEval && (
        <div className="flex flex-wrap items-center gap-2.5 rounded-card border border-line bg-surface px-4 py-3.5 text-[13px]">
          <StatusDot kind="ok" />
          <b>{t("hl.people.quiet")}</b>
          <span className="text-muted">{t("hl.people.quietNote")}</span>
        </div>
      )}
      {rows.map((a) => (
        <AlertCard key={a.id} alert={a} now={now} flow={flow} />
      ))}
    </div>
  );
}
