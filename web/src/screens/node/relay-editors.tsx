import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import type { RelayMsg } from "@/lib/relay";
import { blockedOf, blockedText, modeOf, modes, type Mode } from "@/lib/relay-bridge";

// The two editors that open under a relay's row (the board's .nq-ed, replacing the windows of the older page): the port on the entry and
// the way the server works (auto, forwarding only, bridge only). Both go through the same UpdateRelay the switch uses, and the panel
// checks the port is free. Owner's, like every relay screen.

/** The board's rule for a port: two to five digits, up to 65535, and not the one it has. The panel refuses what it does not take (443 or 1024 and up). */
export const portText = (text: string, now: number): "ok" | "bad" | "same" | "" => {
  if (text === "") return "";
  if (!/^\d{2,5}$/.test(text) || Number(text) > 65535) return "bad";
  return Number(text) === now ? "same" : "ok";
};

/** "Change port…": a field with the port, what it does to people and the two buttons. */
export function PortEditor({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [text, setText] = useState(String(r.port));
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  // what is there to begin with is no mistake: the hint says what a change does, and the error comes with the first key
  const [typed, setTyped] = useState(false);
  const state = portText(text, r.port);
  const save = async () => {
    if (state !== "ok" || pending) return;
    setPending(true);
    setError(null);
    try {
      await relaysApi.updateRelay({ id: r.id, port: Number(text) });
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
    <form
      className="wy-ed"
      data-testid="relay-port-editor"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <TextField
        label={t("wy.ed.port", { entry: r.viaNodeName })}
        mono
        inputMode="numeric"
        autoFocus
        className="max-w-40"
        value={text}
        onChange={(e) => {
          setText(e.target.value.replace(/\D/g, "").slice(0, 5));
          setTyped(true);
          setError(null);
        }}
        error={!typed ? undefined : state === "bad" ? t("wy.ed.portBad") : state === "same" ? t("wy.ed.portSame") : undefined}
        hint={t("wy.ed.portHint")}
      />
      {r.protocol === "awg" && <small>{t("relay.dlg.portAwg")}</small>}
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
      <div>
        <Button size="md" type="button" disabled={pending} onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button variant="primary" size="md" type="submit" disabled={pending || state !== "ok"}>
          {t("wy.ed.change")}
        </Button>
      </div>
    </form>
  );
}

/** "Method…": the three ways in a row of buttons, a click saves it; the bridge is greyed with its reason where the server cannot use one. */
export function ModeEditor({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [value, setValue] = useState<Mode>(modeOf(r));
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const n = { entry: r.viaNodeName, exit: r.exitNodeName };
  const blocked = blockedText(t, blockedOf(r), n);
  const pick = async (m: Mode) => {
    if (m === value || pending || (m === "reverse" && blocked)) return;
    setPending(true);
    setError(null);
    try {
      await relaysApi.updateRelay({ id: r.id, mode: m });
      setValue(m);
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
    <div className="wy-ed" data-testid="relay-mode-editor">
      <span className="wy-sl">{t("nw.s.mode")}</span>
      <div role="group" aria-label={t("nw.s.mode")} className="flex w-fit max-w-full gap-0.5 rounded-field border border-line bg-canvas p-0.5 max-[720px]:w-full max-[720px]:flex-col">
        {modes.map((m) => (
          <button
            key={m}
            type="button"
            aria-pressed={value === m}
            aria-disabled={m === "reverse" && !!blocked}
            title={m === "reverse" && blocked ? blocked : undefined}
            onClick={() => void pick(m)}
            className={cx(
              "h-7 rounded-ctl px-3 text-[13px] transition-colors duration-150 ease-out hover:text-fg aria-disabled:cursor-default aria-disabled:opacity-45 aria-disabled:hover:text-sec",
              value === m ? "bg-surface-2 text-fg" : "text-sec",
            )}
          >
            {t(`nw.mode.opt.${m}` as const)}
          </button>
        ))}
      </div>
      <small>{blocked || t("wy.ed.drop")}</small>
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
      <div>
        <Button size="md" type="button" onClick={onClose}>
          {t("common.done")}
        </Button>
      </div>
    </div>
  );
}
