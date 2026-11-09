import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useTx } from "@/screens/users/t";
import { ConfirmModal } from "@/screens/users/ui";
import type { Settings } from "./model";
import { settingsQuery, useSaveSettings } from "./queries";

/** Whether the apps on the link get the AmneziaWG servers: absent (settings saved before the switch existed) is on. */
export const linkAwgOn = (s: Pick<Settings, "linkAwg">) => s.linkAwg !== false;

/**
 * "AmneziaWG in apps on the link": whether kl!ck and the other Mihomo apps get the AmneziaWG servers (Happ never does, its
 * format cannot carry them). On is saved at once; off asks first while the link apps hold AmneziaWG keys, with how many:
 * each goes at its app's next refresh. AmneziaVPN keys are never touched. Formats links here with #link-awg.
 */
export function LinkAwgCard({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const save = useSaveSettings();
  const keys = useQuery(settingsQuery).data?.linkAwgKeys ?? 0;
  const on = linkAwgOn(settings);
  const [open, setOpen] = useState(false);

  const apply = (v: boolean) =>
    save.mutateAsync((cur) => ({ ...cur, linkAwg: v })).then(
      () => {
        toast(t("subs.saved"));
        halo("save");
      },
      (e: unknown) => {
        toast.error(errorText(e, t));
        throw e;
      },
    );
  function flip(v: boolean) {
    if (v) return void apply(true).catch(() => {});
    // the dialog names how many keys go: ask the panel now, not what it said up to 30 s ago
    void qc.fetchQuery({ ...settingsQuery, staleTime: 0 }).then(
      (d) => (d.linkAwgKeys > 0 ? setOpen(true) : void apply(false).catch(() => {})),
      (e: unknown) => toast.error(errorText(e, t)),
    );
  }

  return (
    <section id="link-awg">
      <Card lg className="flex min-w-0 items-start gap-4 p-4">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <SectionLabel icon="shield" tone="sky" as="h2">
            {t("subs.linkAwg.title")}
          </SectionLabel>
          <p className="text-xs leading-snug text-pretty text-muted">{t(on ? "subs.linkAwg.on" : "subs.linkAwg.off")}</p>
        </div>
        <Switch checked={on} disabled={save.isPending} onCheckedChange={flip} aria-label={t("subs.linkAwg.title")} className="mt-0.5" />
      </Card>
      <LinkAwgOffDialog open={open} onOpenChange={setOpen} keys={keys} onConfirm={() => apply(false)} />
    </section>
  );
}

/** "Take AmneziaWG out of the apps on the link?": how many keys the apps hold now, and that each goes at its app's next refresh. */
export function LinkAwgOffDialog({ open, onOpenChange, keys, onConfirm }: { open: boolean; onOpenChange: (open: boolean) => void; keys: number; onConfirm: () => Promise<unknown> }) {
  const t = useTx();
  return (
    <ConfirmModal open={open} onOpenChange={onOpenChange} title={t("subs.linkAwg.offT")} confirmLabel={t("subs.linkAwg.offDo")} onConfirm={onConfirm}>
      <div className="flex flex-col gap-2 text-[13px] leading-normal text-pretty">
        <p className="font-semibold text-fg">{t.n("subs.linkAwg.offBody", keys)}</p>
        <p className="text-muted">{t("subs.linkAwg.offMore")}</p>
      </div>
    </ConfirmModal>
  );
}
