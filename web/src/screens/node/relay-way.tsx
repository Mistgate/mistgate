import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import type { RelayMsg } from "@/lib/relay";
import { blockedOf, blockedText, modeOf, modes, type Mode } from "@/lib/relay-bridge";

// The way a relay works, per server (relay.proto Relay.mode): the choice of the way and the dialog that saves it. How the whole pair
// carries people, the bridge and what was measured are in relay-pair.tsx. Owner's, like every relay screen.

// ---- the choice of the way ----

/**
 * The three ways with a line each: auto, direct, via bridge. The bridge is greyed with its reason when the pair cannot do it
 * (`blocked` is the sentence: UDP, or the agent to update). `entry` fills the lines.
 */
export function WayChoice({ value, onChange, blocked, entry, label }: { value: Mode; onChange: (m: Mode) => void; blocked: string; entry: string; label: string }) {
  const t = useT();
  return (
    <div role="radiogroup" aria-label={label} data-testid="way-choice" className="flex flex-col gap-2">
      {modes.map((m) => {
        const off = m === "reverse" && blocked !== "";
        return (
          <label
            key={m}
            data-off={off || undefined}
            className={cx(
              "flex items-start gap-2.5 rounded-field border border-line px-3 py-2.5 transition-colors duration-200 has-checked:border-accent-line has-checked:bg-accent-soft",
              off ? "cursor-not-allowed" : "cursor-pointer hover:bg-surface-2 has-checked:hover:bg-accent-soft",
            )}
          >
            <input type="radio" name="relay-way" className="mt-0.5 size-4 flex-none accent-accent" value={m} checked={value === m} disabled={off} onChange={() => onChange(m)} />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className={cx("text-[13px] font-bold", off && "text-muted")}>{t(`relay.way.opt.${m}`)}</span>
              <span className="text-xs leading-snug text-muted">{t(`relay.way.opt.${m}.text`, { entry })}</span>
              {off && <span className="text-xs leading-snug font-semibold text-warn-text">{blocked}</span>}
            </span>
          </label>
        );
      })}
    </div>
  );
}

/** "Change" of a relay row: the way, through the same UpdateRelay the switch and the port use. */
export function ChangeWay({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [value, setValue] = useState<Mode>(modeOf(r));
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const n = { entry: r.viaNodeName, exit: r.exitNodeName };
  const save = async () => {
    setPending(true);
    setError(null);
    try {
      await relaysApi.updateRelay({ id: r.id, mode: value });
      onClose();
      toast(t("relay.way.saved"));
      halo("save");
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
      void qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !pending && onClose()}
      title={t("relay.way.dlg.title", n)}
      description={t("relay.way.dlg.intro", n)}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={pending || value === modeOf(r)} onClick={() => void save()}>
            {t("relay.way.dlg.save")}
          </Button>
        </>
      }
    >
      <WayChoice value={value} onChange={setValue} blocked={blockedText(t, blockedOf(r), n)} entry={r.viaNodeName} label={t("relay.way.title")} />
      {r.enabled && value !== modeOf(r) && <Notice tone="quiet">{t("relay.way.dlg.drop")}</Notice>}
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}

// ---- the exit's card ----

/** "Link to the panel: through msk1": the agent of this node reaches the panel through an entry's bridge (Node.link_via_node_id). */
export function LinkViaLine({ viaNodeId }: { viaNodeId: string }) {
  const t = useT();
  const nodes = useQuery(nodesQuery).data?.nodes ?? [];
  if (!viaNodeId) return null;
  const entry = nodes.find((x) => x.id === viaNodeId)?.name ?? viaNodeId;
  return (
    <Notice tone="quiet" title={t("node.linkVia", { entry })}>
      {t("node.linkVia.text")}
    </Notice>
  );
}
