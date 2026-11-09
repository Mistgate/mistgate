import { halo } from "@/components/fx/halo";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useTx } from "@/screens/users/t";
import type { Settings } from "./model";
import { useSaveSettings } from "./queries";

/** Whether the link apps get the "Auto" group: absent (settings saved before the switch existed) is on. */
export const linkAutoOn = (s: Pick<Settings, "linkAutoSelect">) => s.linkAutoSelect !== false;

/**
 * "Automatic server choice" (issue #44): the Mihomo profile gets an "Auto" url-test group and sing-box an "Auto" urltest
 * outbound, first in the selector, so the app moves to a working server by itself. Saved at once both ways: nothing is taken
 * from anyone, and a server picked by hand in the app stays picked.
 */
export function LinkAutoCard({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const on = linkAutoOn(settings);

  const flip = (v: boolean) =>
    void save.mutateAsync((cur) => ({ ...cur, linkAutoSelect: v })).then(
      () => {
        toast(t("subs.saved"));
        halo("save");
      },
      (e: unknown) => toast.error(errorText(e, t)),
    );

  return (
    <section id="link-auto">
      <Card lg className="flex min-w-0 items-start gap-4 p-4">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <SectionLabel icon="refresh" tone="mint" as="h2">
            {t("subs.linkAuto.title")}
          </SectionLabel>
          <p className="text-xs leading-snug text-pretty text-muted">{t(on ? "subs.linkAuto.on" : "subs.linkAuto.off")}</p>
        </div>
        <Switch checked={on} disabled={save.isPending} onCheckedChange={flip} aria-label={t("subs.linkAuto.title")} className="mt-0.5" />
      </Card>
    </section>
  );
}
