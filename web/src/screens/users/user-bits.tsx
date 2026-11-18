import { useEffect, useRef, useState, type ReactNode } from "react";
import { QrCode } from "@/components/qr-code";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { useToast } from "@/components/ui/toast";
import { plainText } from "@/i18n";
import { cx } from "@/lib/cx";
import { AppLink } from "./nav";
import { groupTone } from "./format";
import { useTx } from "./t";

/** The board's 24-grid glyphs of the person's page, as stroke paths (the same ones the board draws). */
export const P = {
  back: "M15 6l-6 6 6 6",
  check: "M5 12.5l4.5 4.5L19 7.5",
  warn: "M12 4l9 16H3zM12 10v4M12 17v.5",
  copy: "M9 9h10v11H9zM15 9V4H5v11h4",
  qr: "M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h2.5v2.5H14zM17.5 17.5H20V20h-2.5zM14 19.9h.01M19.9 14h.01",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z",
  dots: "M6 12h.01M12 12h.01M18 12h.01",
  pencil: "M4 20h4L19 9l-4-4L4 16zM14 6l4 4",
  trash: "M5 7h14M10 7V4h4v3M7 7l1 13h8l1-13",
  ban: "M5.6 5.6l12.8 12.8M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z",
  key: "M15 7a4 4 0 1 1-3.87 5H8v3H5v-3H3v-2h8.13A4 4 0 0 1 15 7z",
  pc: "M4 5h16v11H4zM9 20h6M12 16v4",
  phone: "M8 3h8a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zM11 18h2",
  tablet: "M6 3h12a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zM11 18h2",
  link: "M9 15l6-6M10.5 6.5L12 5a4.24 4.24 0 0 1 6 6l-1.5 1.5M13.5 17.5L12 19a4.24 4.24 0 0 1-6-6l1.5-1.5",
  plane: "M20 5L4 11l6 2 2 6 3-4 4 3z",
  app: "M4 5h16v14H4zM4 9h16",
  tunnel: "M4.5 19v-6.5a7.5 7.5 0 0 1 15 0V19M9.5 19v-6a2.5 2.5 0 0 1 5 0v6",
  plus: "M12 5v14M5 12h14",
  minus: "M6 12h12",
  down: "M7 10l5 5 5-5",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8v.5",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  file: "M7 3h7l5 5v13H7zM14 3v5h5M10 13h6M10 17h6",
  x: "M6 6l12 12M18 6L6 18",
  external: "M14 4h6v6M20 4l-9 9M18 14v5H5V6h5",
} as const;

/** One glyph of the board: 18 px by default, a 1.75 stroke, round ends. */
export function G({ d, size, stroke, className }: { d: string; size?: number; stroke?: number; className?: string }) {
  return (
    <svg width={size ?? 18} height={size ?? 18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={stroke ?? 1.75} strokeLinecap="round" strokeLinejoin="round" aria-hidden className={cx("block flex-none", className)}>
      <path d={d} />
    </svg>
  );
}

/** A card's head: the title (16/24) with a quiet count, and what goes at the end. */
export function CardHead({ title, count, children }: { title: ReactNode; count?: ReactNode; children?: ReactNode }) {
  return (
    <div className="up-chd">
      <h3>
        {title}
        {count != null && count !== "" && <span>{count}</span>}
      </h3>
      {children}
    </div>
  );
}

/** A card of settings: its title with the line under it (the board's .sv-h). */
export function SetHead({ title, line }: { title: ReactNode; line?: ReactNode }) {
  return (
    <div className="up-sh">
      <h3>{title}</h3>
      {line && <span>{line}</span>}
    </div>
  );
}

/** The person's group as a tag in the group's own colour; with a group page it leads there (7c's /users/groups/$id). */
export function GroupTag({ id, name, color, link = true }: { id: string; name: string; color?: string; link?: boolean }) {
  const tone = groupTone(id, color);
  const cls = "up-gtag";
  if (!link || !id) {
    return (
      <span data-tone={tone} className={cls}>
        {name}
      </span>
    );
  }
  return (
    <AppLink to="/users/groups/$id" params={{ id }} className={cls} {...({ "data-tone": tone } as object)}>
      {name}
    </AppLink>
  );
}

/** The 8 px dot before a group's name in a list (the board's .ug-d), in the group's tone. */
export function GroupDot({ id, color }: { id: string; color?: string }) {
  return <i aria-hidden data-tone={groupTone(id, color)} className="ug-d" />;
}

/** The radio dot of a list of choices (the board's .nx-rad). */
export function Rad() {
  return (
    <span aria-hidden className="up-rad">
      <i />
    </span>
  );
}

/**
 * The board's copy button (.cpb): the icon turns into a tick and the words swap. The value is copied as it is; a refusing browser
 * says so in a toast; `note` is a toast on success (what a link that holds a key asks of whoever sends it). Nothing is printed.
 */
export function CopyBtn({ value, label, doneLabel, variant = "secondary", size = "md", ariaLabel, note }: { value: string; label: string; doneLabel?: string; variant?: "primary" | "secondary"; size?: "md" | "lg"; ariaLabel?: string; note?: string }) {
  const t = useTx();
  const toast = useToast();
  const [done, setDone] = useState(false);
  const timer = useRef(0);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(plainText(value));
      setDone(true);
      if (note) toast(note);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setDone(false), 1400);
    } catch {
      toast(t("common.copyFailed"));
    }
  }
  const was = doneLabel ?? t("common.copied");
  return (
    <Button variant={variant} size={size} aria-label={ariaLabel} onClick={() => void copy()} data-cp={done ? "done" : "idle"} className="up-cpb">
      <span aria-hidden className="up-cpi">
        <G d={P.copy} size={16} />
        <G d={P.check} size={16} stroke={2.6} className="ck" />
      </span>
      <span className="up-cpt">
        <span>{label}</span>
        <span aria-hidden={!done}>{was}</span>
      </span>
    </Button>
  );
}

/** A QR window (the board's .tq-dlg.qr): the code on white, a line under it, "Copy link" and "Close". */
export function QrDialog({ open, onOpenChange, title, text, value, label, copyLabel }: { open: boolean; onOpenChange: (o: boolean) => void; title: string; text: string; value: string; label: string; copyLabel: string }) {
  const t = useTx();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      closeLabel={t("common.close")}
      className="md:max-w-[400px]!"
      footer={
        <>
          <CopyBtn value={value} label={copyLabel} />
          <Button variant="primary" size="lg" onClick={() => onOpenChange(false)}>
            {t("common.close")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col items-center gap-4 pt-1 text-center">
        {open && <QrCode value={value} label={label} size={216} />}
        <p className="text-sm leading-5 text-sec">{text}</p>
      </div>
    </Modal>
  );
}
