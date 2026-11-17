import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useT } from "@/i18n";
import { buttonClass } from "@/components/ui/button";
import { useStaleVersion } from "@/lib/stale";
import { meQuery } from "@/lib/session";
import { updateNotices, updatesQuery, useIsOwner, type UpdateNotice } from "@/lib/updates";

// The owner learns about a release without opening Updates: a card at the foot of the menu (and the version beside Updates in
// the list). The status is the one the panel already caches (GetUpdates; its own GitHub check runs every 10 minutes), so this
// asks the panel, never GitHub. The card stays while the release is new: it takes no room from the page and closes nothing.

const pollMs = 3 * 60_000;

export const vee = (v: string) => (/^v/i.test(v) ? v : `v${v}`);

/**
 * What the owner is told about, most important first: a newer signed panel release, or nodes that run an older agent than the
 * trusted bundle. Nothing for anyone but the owner (only the owner changes what runs), and nothing while this tab is stale: it
 * would say what the old code knows, and the reload notice (stale-toast.tsx) comes first.
 */
export function useUpdateNotice(): UpdateNotice | undefined {
  const owner = useIsOwner();
  const stale = useStaleVersion() !== "";
  const active = owner && !stale;
  const { data } = useQuery({ ...updatesQuery, enabled: active, refetchInterval: pollMs });
  return active ? updateNotices(data)[0] : undefined;
}

export function UpdateCard() {
  const t = useT();
  const notice = useUpdateNotice();
  const { data } = useSuspenseQuery(meQuery);
  if (!notice) return null;
  const panel = notice.kind === "panel";
  const version = vee(notice.version);
  return (
    <section aria-label={t("up.toast.label")} className="flex flex-col gap-3 rounded-xl border border-line bg-canvas p-4">
      <div>
        <b className="flex items-center gap-2 font-medium">
          <span aria-hidden className="size-2 flex-none rounded-full bg-tone-sky" />
          {t(panel ? "up.toast.panelTitle" : "up.toast.nodesTitle", { version })}
        </b>
        <span className="mt-1 block text-xs leading-4 text-muted">
          {panel ? t(notice.installable ? "shell.upd.have" : "up.toast.panelManual", { current: vee(data.version) }) : t("up.toast.nodesText", { n: notice.outdated, total: notice.total })}
        </span>
      </div>
      <Link to="/updates" className={`${buttonClass("secondary", "md")} self-start`}>
        {t(panel ? "up.toast.news" : "up.toast.update")}
      </Link>
    </section>
  );
}
