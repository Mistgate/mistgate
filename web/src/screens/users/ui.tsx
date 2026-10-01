import { Checkbox } from "@base-ui/react/checkbox";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import type { IconName, Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { cx } from "@/lib/cx";
import { useTx } from "./t";

/** Card with an eyebrow title and an optional aside on its right (a count, a hint). `icon` + `tone` put a tinted chip before the title. */
export function Panel({
  title,
  aside,
  icon,
  tone,
  children,
  className,
}: {
  title?: ReactNode;
  aside?: ReactNode;
  icon?: IconName;
  tone?: Tone;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Card lg className={cx("flex min-w-0 flex-col gap-3 p-4", className)}>
      {(title || aside) && (
        <div className={cx("flex gap-2.5", icon ? "items-center" : "items-baseline")}>
          <SectionLabel className="flex-1" icon={icon} tone={tone}>
            {title}
          </SectionLabel>
          {aside}
        </div>
      )}
      {children}
    </Card>
  );
}

/** A settings line: label and hint on the left, the control on the right, a hairline above all but the first. */
export function SettingRow({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cx("flex min-h-[52px] items-center gap-3 border-t border-line first:border-t-0", className)}>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-[13px] font-bold">{label}</span>
        {hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>}
      </div>
      {children}
    </div>
  );
}

/** SettingRow with a switch; the whole line is the hit area (a label around the switch). */
export function SwitchRow({
  label,
  hint,
  checked,
  onCheckedChange,
  disabled,
  className,
}: {
  label: ReactNode;
  hint?: ReactNode;
  checked: boolean;
  onCheckedChange: (on: boolean) => void;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <label className={cx("flex min-h-[52px] cursor-pointer items-center gap-3 border-t border-line first:border-t-0", className)}>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-[13px] font-bold">{label}</span>
        {hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>}
      </span>
      <Switch checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
    </label>
  );
}

/** 18px checkbox (22px on the phone cards): accent fill with a drawn tick. */
export function Check({
  checked,
  onCheckedChange,
  label,
  large,
  disabled,
}: {
  checked: boolean;
  onCheckedChange: (on: boolean) => void;
  label: string;
  large?: boolean;
  disabled?: boolean;
}) {
  return (
    <Checkbox.Root
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
      aria-label={label}
      onClick={(e) => e.stopPropagation()}
      className={cx(
        "grid flex-none place-items-center border-[1.5px] border-faint transition-colors duration-150 data-checked:border-accent data-checked:bg-accent data-disabled:opacity-40",
        large ? "size-[22px] rounded-md" : "size-[18px] rounded-[5px]",
      )}
    >
      <Checkbox.Indicator className="block">
        <span className="-mt-0.5 block h-[7px] w-[3px] rotate-45 border-r-2 border-b-2 border-on-accent" />
      </Checkbox.Indicator>
    </Checkbox.Root>
  );
}

type ConfirmProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  confirmLabel: ReactNode;
  /** Red button for something that cannot be undone. */
  danger?: boolean;
  confirmDisabled?: boolean;
  /** Runs on confirm; the modal closes when it resolves and stays (with the button free again) if it throws. */
  onConfirm: () => Promise<unknown>;
  className?: string;
};

/** Cancel + confirm modal. The confirm button is busy while the call runs, so a double click sends one request. */
export function ConfirmModal({ open, onOpenChange, title, description, children, confirmLabel, danger, confirmDisabled, onConfirm, className }: ConfirmProps) {
  const t = useTx();
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      await onConfirm();
      onOpenChange(false);
    } catch {
      // the caller reports the failure (toast or inline); the modal stays so the admin can retry or cancel
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      className={className}
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("users.cancel")}
          </Button>
          <Button variant={danger ? "danger" : "primary"} disabled={busy || confirmDisabled} onClick={go}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children}
    </Modal>
  );
}

/** ConfirmModal that also asks for `match` to be typed (the user's name) before the red button lights up. */
/** A typed confirmation ignores how the spaces were typed: "Alice  Smith" and "Alice Smith" are the same name to a person. */
const spaced = (s: string) => s.trim().replace(/\s+/g, " ");

export function TypeConfirmModal({ match, ...props }: Omit<ConfirmProps, "confirmDisabled" | "danger"> & { match: string }) {
  const t = useTx();
  const [typed, setTyped] = useState("");
  const { onOpenChange } = props;
  return (
    <ConfirmModal
      {...props}
      onOpenChange={(o) => {
        if (!o) setTyped("");
        onOpenChange(o);
      }}
      danger
      confirmDisabled={spaced(typed) !== spaced(match)}
    >
      {props.children}
      <TextField
        aria-label={t("users.deleteType", { name: match })}
        hint={
          // pre-wrap: a name with two spaces in a row shows them; the button copies it exactly
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="whitespace-pre-wrap">{t("users.deleteType", { name: match })}</span>
            <CopyButton value={match} />
          </span>
        }
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
      />
    </ConfirmModal>
  );
}

export function useDebounced<V>(value: V, ms: number): V {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

/**
 * A control that edits a server value with steppers and toggles: clicks show at once (the draft) and one save goes
 * out `delay` ms after the last click, so five clicks on "+" are one request. The draft is dropped when the server
 * value moves after the save went out (or someone else changed it), and when the save fails. Clicks made while a
 * save is in flight start a new draft that survives that save's refresh.
 */
export function useDraft<V>(server: V, save: (v: V) => Promise<unknown>, delay = 450): [V, (v: V) => void] {
  const [draft, setDraft] = useState<{ v: V; sent: boolean } | null>(null);
  const saveRef = useRef(save);
  useEffect(() => {
    saveRef.current = save;
  });

  // "adjust state while rendering" (react.dev): when the server value changes, forget a draft that was already sent
  const key = JSON.stringify(server);
  const [seen, setSeen] = useState(key);
  if (key !== seen) {
    setSeen(key);
    if (draft?.sent) setDraft(null);
  }

  useEffect(() => {
    if (!draft || draft.sent) return;
    const id = setTimeout(() => {
      setDraft({ v: draft.v, sent: true });
      saveRef.current(draft.v).catch(() => setDraft(null));
    }, delay);
    return () => clearTimeout(id);
  }, [draft, delay]);

  return [draft ? draft.v : server, (v) => setDraft({ v, sent: false })];
}
