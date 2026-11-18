import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { errorCode, errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery, viaLabel, type RelayMsg } from "@/lib/relay";
import { wantsReach } from "@/lib/relay-bridge";
import { settingsQuery } from "@/screens/subscriptions/queries";

// Relays on the node page (relay.proto; the cascade plan §8 screens 1-3), the pieces the "Ways in" tab (ways.tsx) is made
// of: a relay's row with its state, its two checks (the leg from the entry to the exit's server, the path from outside
// through the entry), the round the panel makes through it and what went through, and the switches. RelayService is the
// owner's: nothing here renders, and nothing is asked, for a helper.

/** The country code of every node, for the small code chip in front of a name. */
export function useCountryOf(): (id: string) => string {
  const q = useQuery(nodesQuery);
  const by = new Map((q.data?.nodes ?? []).map((n) => [n.id, n.countryCode]));
  return (id) => by.get(id) ?? "";
}

type Busy = "check" | "on" | "off";

/** The record without one key. */
function without<V>(rec: Record<string, V>, key: string): Record<string, V> {
  const out = { ...rec };
  delete out[key];
  return out;
}

/** What runs on the rows of a list: the calls, which row is busy with which, and the sentence a failed call left. */
export function useRelayRuns() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const ccOf = useCountryOf();
  const hours = useQuery(settingsQuery).data?.settings.updateIntervalHours || 12;
  const [busy, setBusy] = useState<Record<string, Busy>>({});
  const [notes, setNotes] = useState<Record<string, string>>({});

  const run = async (r: RelayMsg, kind: Busy, call: () => Promise<unknown>, done?: () => void, refresh = true) => {
    setBusy((b) => ({ ...b, [r.id]: kind }));
    setNotes((n) => without(n, r.id));
    try {
      await call();
      done?.();
    } catch (e) {
      // a failed check is the row's own story (the stored check names the stage): no second sentence for it
      if (errorCode(ConnectError.from(e).rawMessage) !== "relay_check_failed") setNotes((n) => ({ ...n, [r.id]: errorText(e, t) }));
    } finally {
      setBusy((b) => without(b, r.id));
      if (refresh) await qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  // a relay that may use a bridge is checked both ways: the exit's reach to the entry's bridge comes with the leg
  const check = (r: RelayMsg, refresh = true) => run(r, "check", () => relaysApi.checkRelay({ id: r.id, ...(wantsReach(r) ? { reverseReach: true } : {}) }), undefined, refresh);
  return {
    busy,
    notes,
    check: (r: RelayMsg) => void check(r),
    on: (r: RelayMsg) =>
      void run(r, "on", () => relaysApi.updateRelay({ id: r.id, enabled: true }), () => toast(t("relay.turnedOn", { via: viaLabel(t, ccOf(r.viaNodeId)), hours }), { ms: 8000 })),
    off: (r: RelayMsg) => void run(r, "off", () => relaysApi.updateRelay({ id: r.id, enabled: false }), () => toast(t("relay.turnedOff"))),
    /** One after another: the panel checks one relay per exit at a time anyway, and the rows answer in order. */
    checkAll: async (list: readonly RelayMsg[]) => {
      for (const r of list) await check(r, false);
      await qc.invalidateQueries({ queryKey: ["relays"] });
    },
    checking: Object.values(busy).length > 0,
  };
}
export type RelayRuns = ReturnType<typeof useRelayRuns>;

/** Removes a relay at once (the row menu has asked "Really remove?" already): the toast says it, a failure is the toast's too. */
export function useRemoveRelay() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  return async (r: RelayMsg) => {
    try {
      await relaysApi.deleteRelay({ id: r.id });
      toast(t("relay.removed"));
    } catch (e) {
      toast.error(errorText(e, t));
    } finally {
      void qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
}

export function RemoveRelay({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const remove = async () => {
    setPending(true);
    try {
      await relaysApi.deleteRelay({ id: r.id });
      onClose();
      toast(t("relay.removed"));
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
      onOpenChange={(o) => !o && onClose()}
      title={t("relay.removeTitle", { entry: r.viaNodeName, exit: r.exitNodeName })}
      description={t("relay.removeBody", { profile: r.profileName, exit: r.exitNodeName, entry: r.viaNodeName })}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={pending} onClick={() => void remove()}>
            {t("relay.remove")}
          </Button>
        </>
      }
    >
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}

export type { Entry } from "./relay-run";

/** The relays with this node at either end (the ways in, a profile's removal, retiring the node). */
export function useRelaysOf(nodeId: string, enabled: boolean) {
  return useQuery({ ...relaysQuery(nodeId), enabled }).data ?? [];
}

/** The relays that go when a server is removed (§8 screen 8): "Also removes 2 relays through msk1." */
export function alsoRemovesText(t: T, gone: RelayMsg[]): string {
  if (gone.length === 0) return "";
  const entries = [...new Set(gone.map((r) => r.viaNodeName))].join(", ");
  return t.n("relay.removeAlso", gone.length, { entries });
}

/** "Change port": the relay's port on its entry, through the same UpdateRelay the switch uses; the panel checks the port is free. */
export function ChangeRelayPort({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [port, setPort] = useState(String(r.port));
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const n = Number(port);
  const valid = Number.isInteger(n) && n >= 1 && n <= 65535;
  const save = async () => {
    setPending(true);
    try {
      await relaysApi.updateRelay({ id: r.id, port: n });
      onClose();
      toast(t("common.saved"));
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
      title={t("wg.port.title", { entry: r.viaNodeName })}
      description={t("wg.port.hint", { entry: r.viaNodeName, exit: r.exitNodeName })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={pending || !valid || n === r.port} onClick={() => void save()}>
            {t("wg.port.save")}
          </Button>
        </>
      }
    >
      <TextField label={t("wg.port.label")} mono inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))} />
      {r.protocol === "awg" && <Notice tone="quiet">{t("relay.dlg.portAwg")}</Notice>}
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}
