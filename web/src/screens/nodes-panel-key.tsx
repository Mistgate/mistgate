import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { IconChip } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { readPref, writePref } from "@/lib/storage";
import { accessQuery, passwordOnly } from "./nodes-bulk-actions";

// the nodes the owner hid the offer for (sorted ids): a node that turns password-only later brings it back
const hiddenPref = "mistgate.panelKeyBulk.hidden";

/**
 * Nodes page, owner only: when live nodes log in with a saved password only, a calm offer to put the panel key on all of
 * them. Only on the owner's click: it selects those nodes and starts "Panel key" in the bulk bar, which asks for step-up
 * once, goes node by node (each is an SSH session of several seconds) and lists how each went.
 */
export function PanelKeyOffer({ onStart, busy }: { onStart: (ids: string[]) => void; busy: boolean }) {
  const t = useT();
  const list = useQuery(accessQuery);
  const [hidden, setHidden] = useState(() => readPref(hiddenPref) ?? "");
  const candidates = (list.data?.access ?? []).filter(passwordOnly);
  const ids = candidates.map((a) => a.nodeId).sort();

  if (candidates.length === 0 || hidden === ids.join(",")) return null;

  function hide() {
    writePref(hiddenPref, ids.join(","));
    setHidden(ids.join(","));
  }

  return (
    <section data-panel-key-offer className="flex flex-wrap items-center gap-x-3 gap-y-3 rounded-card border border-line bg-surface px-3.5 py-3">
      <div className="flex min-w-[240px] flex-1 items-start gap-2.5">
        <IconChip icon="key" tone="lavender" />
        <div className="flex min-w-0 flex-col gap-0.5">
          <p className="text-[13px] leading-snug font-bold text-pretty">{t.n("nodes.panelKey.banner", candidates.length)}</p>
          <p className="text-xs leading-snug text-pretty text-muted">{t("nodes.panelKey.body")}</p>
        </div>
      </div>
      <div className="flex flex-wrap gap-2 max-sm:w-full">
        <Button variant="secondary" size="md" data-bulk-start className="max-sm:flex-1" disabled={busy} onClick={() => onStart(ids)}>
          {t("nodes.panelKey.action")}
        </Button>
        <Button variant="ghost" size="md" onClick={hide}>
          {t("nodes.panelKey.hide")}
        </Button>
      </div>
    </section>
  );
}
