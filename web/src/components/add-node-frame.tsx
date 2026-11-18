import { Dialog } from "@base-ui/react/dialog";
import { Popover } from "@base-ui/react/popover";
import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Flag } from "@/components/ui/flag";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { fieldClass } from "@/components/ui/text-field";
import { Glyph, glyphs } from "@/components/windows";
import { plainText, useT } from "@/i18n";
import { countryCodes } from "@/lib/countries";
import type { ShareChoice } from "@/lib/node-share";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import "./add-node.css";

// The frame of the new-node window (the board's PnAddNode, .an): the window with its rail of steps on the left, the panels
// of a step, and the small things the steps are made of (the copy button, the box of a command, the country list). The
// steps themselves (ssh-node-install.tsx, add-node.tsx) fill it in.

/** Where the window is narrow enough to be a sheet with its list of countries under the field: the window's own breakpoint (760) plus its margins. */
export function useNarrow(): boolean {
  const query = "(max-width: 807px)";
  const [narrow, setNarrow] = useState(() => typeof matchMedia === "function" && matchMedia(query).matches);
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const m = matchMedia(query);
    const on = () => setNarrow(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return narrow;
}

/**
 * The window: a scrim, and under it the window 880 wide at most, 56 from the top (a sheet from the bottom edge on a phone).
 * The wrapper is the window's container: its rules for "narrow" answer to the window's width, not the screen's.
 */
export function AddNodeShell({ open, onOpenChange, children }: { open: boolean; onOpenChange: (open: boolean) => void; children: ReactNode }) {
  const popup = useRef<HTMLDivElement>(null);
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 animate-[wn-fade_0.25s_var(--e)_backwards] bg-(--scrim) transition-opacity duration-200 data-ending-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-50 flex justify-center overflow-y-auto px-6 pt-14 pb-6 max-[760px]:p-3 max-[720px]:items-end max-[720px]:p-0">
          <div className="an-host h-fit max-[720px]:max-w-none">
            <Dialog.Popup
              ref={popup}
              // the first field of the page takes the focus (a touch screen keeps its keyboard down: the window itself does)
              initialFocus={(type) => (type === "touch" ? true : (popup.current?.querySelector<HTMLElement>(".an-b input, .an-b textarea") ?? true))}
              className="an animate-[wn-dlg_0.35s_var(--e)_backwards] shadow-(--shadow-dlg) outline-none transition-[opacity,transform] duration-200 ease-out max-[720px]:animate-[wn-sheet_0.4s_var(--e)_backwards] data-ending-style:translate-y-2 data-ending-style:opacity-0 max-[720px]:data-ending-style:translate-y-full"
            >
              {children}
            </Dialog.Popup>
          </div>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export type RailStep = {
  label: string;
  /** What was entered on the step: shown in mono under its name once it is behind. */
  sum: string;
  state: "done" | "now" | "err" | "";
  /** A step behind can be gone back to (not while the install runs). */
  onGo?: () => void;
};

/** The rail (.an-s): the title, the way to install, the steps with what was entered, and a line about what is safe. */
export function Rail({
  title,
  mode,
  onMode,
  modeLocked,
  steps,
  note,
}: {
  title: ReactNode;
  mode?: "ssh" | "cmd";
  onMode?: (mode: "ssh" | "cmd") => void;
  modeLocked?: boolean;
  steps: RailStep[];
  note: string;
}) {
  const t = useT();
  return (
    <aside className="an-s">
      <Dialog.Title render={<div />} className="an-t">
        {title}
      </Dialog.Title>
      {mode && onMode && (
        <RadioGroup aria-label={t("win.node.method")} value={mode} onValueChange={(v) => onMode(v as "ssh" | "cmd")} className="an-seg">
          <Radio.Root value="ssh" disabled={modeLocked && mode !== "ssh"}>
            {t("win.node.bySsh")}
          </Radio.Root>
          <Radio.Root value="cmd" disabled={modeLocked && mode !== "cmd"}>
            {t("win.node.byCommand")}
          </Radio.Root>
        </RadioGroup>
      )}
      <ol className="an-st" aria-label={t("node.ssh.steps")}>
        {steps.map((s, i) => (
          <li key={s.label} className={s.state}>
            <button type="button" onClick={s.onGo} disabled={!s.onGo} aria-current={s.state === "now" || s.state === "err" ? "step" : undefined}>
              <span className="an-g" aria-hidden>
                {s.state === "done" ? <Icon name="check" /> : <span className="an-mono">{i + 1}</span>}
              </span>
              <span className="an-sl">
                <b>{s.label}</b>
                <span className="an-sum">{s.sum}</span>
              </span>
            </button>
          </li>
        ))}
      </ol>
      <p className="an-n">
        <Glyph d={glyphs.shield} />
        {note}
      </p>
    </aside>
  );
}

/** The window's own close, in the corner of the main part. */
export function CloseX({ onClose }: { onClose: () => void }) {
  const t = useT();
  return (
    <IconButton aria-label={t("common.close")} onClick={onClose} className="an-x">
      <Icon name="x" />
    </IconButton>
  );
}

/** The panel of a step (.an-p): comes in from the side. `key` it by the step. With `onSubmit` it is a form: Enter in a field goes on, as the main button does. */
export function Panel({ children, className, onSubmit }: { children: ReactNode; className?: string; onSubmit?: (e: FormEvent<HTMLFormElement>) => void }) {
  return onSubmit ? (
    <form className={cx("an-p", className)} onSubmit={onSubmit}>
      {children}
    </form>
  ) : (
    <div className={cx("an-p", className)}>{children}</div>
  );
}

export function PanelHead({ eyebrow, title, children }: { eyebrow: ReactNode; title: ReactNode; children?: ReactNode }) {
  return (
    <header className="an-h">
      <span className="an-e">{eyebrow}</span>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
    </header>
  );
}

/** The foot of a panel (.an-a): the button that goes back, a line, the main button. */
export function PanelFoot({ children }: { children: ReactNode }) {
  return <footer className="an-a">{children}</footer>;
}

/** The line of a foot: a glyph and a few words, pushed to the side (`end`) or to the start. */
export function FootNote({ children, glyph, end }: { children?: ReactNode; glyph?: string; end?: boolean }) {
  return (
    <span className={cx("an-an", end && "an-ar")}>
      {glyph && <Glyph d={glyph} />}
      {children}
    </span>
  );
}

/** A copy button of the board (.btn.sm, words only): "Copy" turns into "Copied" for a moment. The browser refusing is said in words. */
export function CopyChip({ value, label, className }: { value: string; label?: string; className?: string }) {
  const t = useT();
  const [state, setState] = useState<"" | "done" | "fail">("");
  useEffect(() => {
    if (!state) return;
    const id = setTimeout(() => setState(""), 1400);
    return () => clearTimeout(id);
  }, [state]);
  async function copy() {
    try {
      await navigator.clipboard.writeText(plainText(value));
      setState("done");
    } catch {
      setState("fail");
    }
  }
  return (
    <Button type="button" variant="secondary" size="md" className={className} onClick={() => void copy()}>
      <span aria-live="polite">{state === "done" ? t("common.copied") : state === "fail" ? t("win.node.copyFailed") : (label ?? t("common.copy"))}</span>
    </Button>
  );
}

/** A command or a key in its own box (.an-code): one line cut with an ellipsis, or (`wrap`) up to three lines; the copy button inside. */
export function CodeBox({ code, label, wrap, testId }: { code: string; label?: string; wrap?: boolean; testId?: string }) {
  return (
    <div className={cx("an-code", wrap && "an-cw")}>
      <code dir="ltr" data-testid={testId}>
        {code}
      </code>
      <CopyChip value={code} label={label} />
    </div>
  );
}

/** One way in the "how to log in" row (.an-ws): an icon and a word; the one chosen is a step lighter. */
export function WaySwitch<V extends string>({
  value,
  onValueChange,
  options,
  label,
}: {
  value: V;
  onValueChange: (v: V) => void;
  options: readonly { value: V; label: string; d: string }[];
  label: string;
}) {
  return (
    <RadioGroup aria-label={label} value={value} onValueChange={(v) => onValueChange(v as V)} className="an-ws">
      {options.map((o) => (
        <Radio.Root key={o.value} value={o.value}>
          <Glyph d={o.d} />
          {o.label}
        </Radio.Root>
      ))}
    </RadioGroup>
  );
}

const none = "none";

/**
 * The country of a server: a button that opens a list with a search (.an-sel, .an-pop). The list is above the button on a wide
 * window and below it on a narrow one; every row is a flag, the name in the language of the panel and the code. "Not set" is
 * the first row. `value` is a country code or "none".
 */
export function CountryField({ value, onChange, label, hint, className, defaultOpen }: { value: string; onChange: (code: string) => void; label: ReactNode; hint?: ReactNode; className?: string; defaultOpen?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const narrow = useNarrow();
  const [open, setOpen] = useState(!!defaultOpen);
  const [q, setQ] = useState("");
  const all = useMemo(
    () => [...countryCodes].map((code) => ({ code, name: fmt.country(code) })).sort((a, b) => a.name.localeCompare(b.name, fmt.locale)),
    [fmt],
  );
  const needle = q.trim().toLowerCase();
  const shown = needle ? all.filter((c) => c.name.toLowerCase().includes(needle) || c.code.toLowerCase().startsWith(needle)) : all;
  const current = value === none ? undefined : all.find((c) => c.code === value);
  return (
    <div className={cx("an-f an-cc flex min-w-0 flex-col gap-1.5", className)}>
      <span className="text-[13px] text-sec">{label}</span>
      <Popover.Root
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          setQ("");
        }}
      >
        <Popover.Trigger type="button" aria-haspopup="listbox" className={cx(fieldClass, "an-i an-sel h-10 px-3")}>
          {current ? <Flag code={current.code} size={16} /> : <i aria-hidden className="an-fl-none" />}
          <span className={current ? "" : "ph"}>{current ? current.name : t("node.add.countryNone")}</span>
          <Glyph d={glyphs.chevron} />
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Positioner side={narrow ? "bottom" : "top"} align="start" sideOffset={6} className="z-[60] outline-none" style={narrow ? { width: "var(--anchor-width)" } : undefined}>
            <Popover.Popup initialFocus className={cx("an-pop", narrow && "!w-full")}>
              <label className="an-q">
                <Glyph d={glyphs.search} />
                <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("win.node.findCountry")} aria-label={t("win.node.findCountry")} autoComplete="off" />
              </label>
              <div role="listbox" aria-label={t("node.add.country")} className="an-ls wn-scroll">
                {!needle && (
                  <button
                    type="button"
                    role="option"
                    aria-selected={value === none}
                    onClick={() => {
                      onChange(none);
                      setOpen(false);
                    }}
                  >
                    <i aria-hidden className="an-fl-none" />
                    <span>{t("node.add.countryNone")}</span>
                    <span />
                  </button>
                )}
                {shown.map((c) => (
                  <button
                    key={c.code}
                    type="button"
                    role="option"
                    aria-selected={value === c.code}
                    onClick={() => {
                      onChange(c.code);
                      setOpen(false);
                    }}
                  >
                    <Flag code={c.code} size={16} />
                    <span>{c.name}</span>
                    <span className="an-code-cc">{c.code}</span>
                  </button>
                ))}
                {shown.length === 0 && <p>{t("win.node.noCountry")}</p>}
              </div>
            </Popover.Popup>
          </Popover.Positioner>
        </Popover.Portal>
      </Popover.Root>
      {hint && <p className="text-xs leading-4 text-muted">{hint}</p>}
    </div>
  );
}

export { none as noCountry };

/** The things the first step shares with the by-command flow, kept when the person switches between them. `share` is who gets the node (undefined: not touched, the window's own default). */
export type NodeDraft = { name: string; address: string; country: string; share?: ShareChoice };
