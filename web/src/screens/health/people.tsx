import { StatusDot } from "@/components/ui/status";
import { useT } from "@/i18n";
import { peopleOrder, type Alert } from "@/lib/health";
import { AlertCard } from "./alerts";
import type { FixFlow } from "./fix";

/**
 * People: the alerts the panel derives about a person, not a node (access ended but the app keeps asking; a key that
 * never connected or went stale; a quiet app). Warnings first, then info; each is an alert card with the person's name,
 * a button to their page and Mute.
 */
export function PeopleTab({ active, now, flow }: { active: Alert[]; now: number; flow: FixFlow }) {
  const t = useT();
  const rows = peopleOrder(active);
  return (
    <div className="flex flex-col gap-3.5">
      {rows.length === 0 && (
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
