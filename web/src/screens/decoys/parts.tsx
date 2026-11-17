import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState, type HTMLAttributes, type InputHTMLAttributes, type ReactNode, type RefObject } from "react";
import { Flag } from "@/components/ui/flag";
import { fieldClass } from "@/components/ui/text-field";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { plainText, useT, type T } from "@/i18n";
import { accessQuery, askAssistant } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { type Delivery, type DeliveryKind, type Node } from "@/lib/decoysites";
import type { Fmt } from "@/lib/format";

// Small pieces the list and the page of a site share.

/** The state glyph of the board (.rw-gl): a ring, a full dot, a half dot, a cross, a spinning ring. Decorative: the word sits beside it. */
export function Gl({ kind, className }: { kind: DeliveryKind | ""; className?: string }) {
  return <i aria-hidden className={cx("dz-gl", kind === "off" ? "" : kind, className)} />;
}

const sourcePath = {
  upload: "M12 16V5M7.5 9.5L12 5l4.5 4.5M5 19h14",
  // the assistant's cat, filled
  assistant:
    "M12 21.5c-4.5 0-8-3-8-7.5 0-1.75.4-3.25 1-4.5L4.5 3l5 3.5c.8-.2 1.65-.3 2.5-.3s1.7.1 2.5.3L19.5 3 19 9.5c.6 1.25 1 2.75 1 4.5 0 4.5-3.5 7.5-8 7.5zM7.45 13.5a1.3 1.85 0 1 0 2.6 0a1.3 1.85 0 1 0-2.6 0zM13.95 13.5a1.3 1.85 0 1 0 2.6 0a1.3 1.85 0 1 0-2.6 0z",
  mcp: "M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4",
  phone: "M8 3.5h8a1.5 1.5 0 0 1 1.5 1.5v14a1.5 1.5 0 0 1-1.5 1.5H8A1.5 1.5 0 0 1 6.5 19V5A1.5 1.5 0 0 1 8 3.5zM11 17.5h2",
  pc: "M4 5h16v11H4zM9 20h6M12 16v4",
  folder: "M3.5 7a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z",
} as const;
export type SourceKind = keyof typeof sourcePath;

/** A 16px icon of the board's own paths (the sources of a site, the folder of the panel's directory); the cat is a filled shape. */
export function DzIcon({ kind, size = 16, className }: { kind: SourceKind; size?: number; className?: string }) {
  return (
    <svg aria-hidden width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" className={cx("block flex-none", kind === "assistant" && "dz-fill", className)}>
      <path d={sourcePath[kind]} />
    </svg>
  );
}

export const sourceKey = { upload: "dz.src.upload", assistant: "dz.src.assistant", mcp: "dz.src.mcp" } as const;
export const asSource = (v: string): "upload" | "assistant" | "mcp" => (v === "assistant" || v === "mcp" ? v : "upload");

/** The panel's square of four (the Overview's section glyph): the panel in a row of nodes. */
export function PanelGlyph({ className }: { className?: string }) {
  return (
    <svg aria-hidden width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" className={cx("block flex-none", className)}>
      <path d="M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z" />
    </svg>
  );
}

/** A flag for a node, the panel's glyph for the panel. */
export function TargetMark({ node, panel }: { node?: Pick<Node, "countryCode">; panel?: boolean }) {
  return <span aria-hidden className="dz-pli">{panel ? <PanelGlyph /> : <Flag code={node?.countryCode ?? ""} size={16} />}</span>;
}

/** "2 nodes and the panel" / "not in use": where a site stands, from the counts of the list. */
export function usageText(t: T, nodes: number, panel: boolean): string {
  if (nodes === 0 && !panel) return t("dz.use.none");
  const n = nodes > 0 ? t.n("dz.use.nodes", nodes) : "";
  const p = panel ? t("dz.use.panel") : "";
  return n && p ? t("dz.use.and", { a: n, b: p }) : n || p;
}

/** The same in the case of "stands on …": "on 2 nodes and on the panel" (the languages decline it). */
export function onText(t: T, nodes: number, panel: boolean): string {
  const n = nodes > 0 ? t.n("dz.on.nodes", nodes) : "";
  const p = panel ? t("dz.on.panel") : "";
  return n && p ? t("dz.on.and", { a: n, b: p }) : n || p;
}

/** A moment as the cards say it: "2 days ago" for the last week, then the date. */
export function whenText(fmt: Fmt, t: T, unix: number, now = Date.now()): string {
  if (unix <= 0) return "";
  const ageS = now / 1000 - unix;
  if (ageS < 60) return t("dz.fact.justNow");
  return ageS < 7 * 86_400 ? fmt.ago(unix, now) : fmt.date(unix);
}

/** The nodes of the panel that count: a retired one is gone from the library's numbers too. */
export const liveNodes = (nodes: readonly Node[] | undefined): Node[] => (nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED);

/** Whether the assistant is set up on this panel (the chat button answers the same), and the window that asks it something. */
export function useAssistant() {
  const access = useQuery(accessQuery).data;
  const off = access !== undefined && !access.available;
  return { off, ask: (text: string) => !off && askAssistant(plainText(text)) };
}

/** True once the element has come near the window (a card's preview is asked for then, not before); a browser without observers says yes at once. */
export function useSeen<E extends Element>(margin = 240): [RefObject<E | null>, boolean] {
  const ref = useRef<E>(null);
  const [seen, setSeen] = useState(() => typeof IntersectionObserver === "undefined");
  useEffect(() => {
    const el = ref.current;
    if (seen || !el) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && setSeen(true), { rootMargin: `${margin}px` });
    io.observe(el);
    return () => io.disconnect();
  }, [seen, margin]);
  return [ref, seen];
}

/** The delivery of a node as a glyph and a word; the hint sits under it where the row has room. */
export function DeliveryWord({ d }: { d: Delivery }) {
  const t = useT();
  return (
    <span className="dz-dl">
      <Gl kind={d.kind} />
      {t(d.word)}
    </span>
  );
}

/** A card of the board: the border and the surface, nothing more. */
export function DzCard({ className, children, ...rest }: { className?: string; children: ReactNode } & Omit<HTMLAttributes<HTMLElement>, "className">) {
  return (
    <section className={cx("dz-card", className)} {...rest}>
      {children}
    </section>
  );
}

/** The header of a card on the page of a site: the title, and a count or a hash at its end. */
export function CardHead({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="dz-ch2">
      <h3>{title}</h3>
      {children}
    </div>
  );
}

/** A coloured message of the board (.dz-msg): a glyph and two lines. */
export function Msg({ kind, title, children, role }: { kind: "" | "ok" | "bad" | "part"; title: ReactNode; children?: ReactNode; role?: "alert" }) {
  return (
    <div className={cx("dz-msg", kind)} role={role}>
      <Gl kind={kind} />
      <div>
        <b>{title}</b>
        {children}
      </div>
    </div>
  );
}

/** A text field of the board's windows: the label with its counter, the box, a hint or a refusal under it. */
export function Field({
  label,
  value,
  max,
  onChange,
  placeholder,
  hint,
  error,
  ...rest
}: {
  label: string;
  value: string;
  max: number;
  onChange: (v: string) => void;
  placeholder?: string;
  hint?: string;
  error?: string;
} & Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "className" | "maxLength">) {
  const id = useId();
  return (
    <div className={cx("dz-fld", error && "err")}>
      <label htmlFor={id}>
        {label}
        <span className="dz-m">
          {value.length} / {max}
        </span>
      </label>
      <input id={id} type="text" value={value} placeholder={placeholder} maxLength={max} spellCheck={false} autoComplete="off" aria-invalid={error ? true : undefined} className={cx(fieldClass, "h-10 px-3")} onChange={(e) => onChange(e.target.value)} {...rest} />
      {hint && !error && <p>{hint}</p>}
      {error && (
        <p className="err" role="alert">
          <Gl kind="bad" />
          {error}
        </p>
      )}
    </div>
  );
}

/** A link to the page of a site (or of a built-in entry: "builtin", "panel_dir"). */
export function SiteLink({ id, className, children }: { id: string; className?: string; children: ReactNode }) {
  return (
    <Link to="/decoys/$id" params={{ id }} className={className}>
      {children}
    </Link>
  );
}

/** The arrow of "from → to" (the board's path); it turns round in a right-to-left page by itself (data-flip). */
export function Arrow() {
  return (
    <svg aria-hidden data-flip width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" className="block flex-none">
      <path d="M5 12h14M13 6l6 6-6 6" />
    </svg>
  );
}
