import { useT } from "@/i18n";
import { reloadPage, useStaleVersion } from "@/lib/stale";
import { Button } from "./ui/button";

/**
 * "The panel was updated: reload", under the header, for a tab that outlived a panel update (lib/stale.ts). It stays until the
 * page is reloaded: the old JS cannot be trusted with the new panel's schemas. Nothing at all while the tab is current.
 */
export function StaleBanner() {
  const version = useStaleVersion();
  return version ? <StaleBannerView version={version} /> : null;
}

export function StaleBannerView({ version }: { version: string }) {
  const t = useT();
  return (
    <div
      role="status"
      className="screen-enter sticky top-14 z-[9] mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-field border border-warn-line bg-warn-soft py-2 pr-2 pl-3"
    >
      <span aria-hidden className="size-2 flex-none rounded-full bg-warn" />
      <p className="min-w-[200px] flex-1 text-xs leading-snug text-fg">{t("stale.msg", { version: /^v/i.test(version) ? version : `v${version}` })}</p>
      <Button size="sm" variant="primary" onClick={reloadPage}>
        {t("stale.reload")}
      </Button>
    </div>
  );
}
