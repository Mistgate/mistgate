import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useTx } from "@/screens/users/t";
import type { Settings } from "./model";
import { Row, Toggle } from "./parts";
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
      },
      (e: unknown) => toast.error(errorText(e, t)),
    );

  return (
    <>
      <Toggle id="link-auto" title={t("subs.linkAuto.title")} hint={t(on ? "subs.linkAuto.on" : "subs.linkAuto.off")} checked={on} disabled={save.isPending} onChange={flip} />
      {on && <LinkAutoIntervalRow settings={settings} />}
    </>
  );
}

/** The seconds between the checks the panel accepts, and the one a settings record without the choice means. */
export const autoIntervals = [60, 120, 300, 600] as const;
export const autoIntervalDefault = 300;
export const linkAutoInterval = (s: Pick<Settings, "linkAutoInterval">) => s.linkAutoInterval ?? autoIntervalDefault;

/** How often the Auto group checks the servers; right under the switch (LinkAutoCard draws it), only while Auto is on. Saved at once. */
function LinkAutoIntervalRow({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const pick = (v: string) =>
    void save.mutateAsync((cur) => ({ ...cur, linkAutoInterval: Number(v) })).then(
      () => {
        toast(t("subs.saved"));
      },
      (e: unknown) => toast.error(errorText(e, t)),
    );
  return (
    <div className="pp-rows sb-ai">
      <Row label={t("subs.linkAuto.every")} hint={t("subs.linkAuto.everyHint")}>
        <Segmented
          aria-label={t("subs.linkAuto.everyAria")}
          value={String(linkAutoInterval(settings))}
          onValueChange={pick}
          options={autoIntervals.map((n) => ({ value: String(n), label: t(`subs.linkAuto.s${n}`) }))}
          className="sb-seg"
        />
      </Row>
    </div>
  );
}
