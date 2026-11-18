import { halo } from "@/components/fx/halo";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useTx } from "@/screens/users/t";
import type { Settings } from "./model";
import { Toggle } from "./parts";
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

  return <Toggle id="link-auto" title={t("subs.linkAuto.title")} hint={t(on ? "subs.linkAuto.on" : "subs.linkAuto.off")} checked={on} disabled={save.isPending} onChange={flip} />;
}
