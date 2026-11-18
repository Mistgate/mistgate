import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { cx } from "@/lib/cx";
import { errorCode, errorVars } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { useTx } from "./t";
import "./group.css";

// The pieces the group page's tabs share: the card with a title and a line under it, the save bar that slides out of its foot,
// and the row of a thing that is on or off. The values are the board's (PnGroup: sv-, up-, ug-).

/** What the dev kit starts the page with (a search, the panel open, a menu open, a bar with changes, a button asking twice); the app starts with none. */
export type GroupDemo = {
  pick?: boolean;
  /** How many of the people in the panel are ticked. */
  tick?: number;
  find?: string;
  menu?: boolean;
  /** The profile card's draft: the first profile off. */
  dirty?: boolean;
  saving?: boolean;
  ok?: boolean;
  /** The settings: a name typed, and the button of the danger zone that has been pressed once. */
  rename?: string;
  ask?: "mv" | "del";
  /** The page of a group that was just deleted. */
  deleted?: boolean;
};

/** The card of a tab: a title (16/24) and a line under it, then the rows; it rises 8px as it appears. */
export function SvCard({ title, sub, children, className }: { title: ReactNode; sub?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("relative animate-[mg-up_0.25s_var(--e)_both] overflow-hidden rounded-card border border-line bg-surface", className)}>
      <div className="px-6 pt-5 pb-4 max-md:px-4 max-md:pt-4 max-md:pb-3">
        <h3 className="m-0 text-base leading-6 font-medium">{title}</h3>
        {sub && <span className="mt-0.5 block max-w-[640px] text-[13px] leading-[18px] text-sec">{sub}</span>}
      </div>
      {children}
    </section>
  );
}

/** A padded block inside a card (up-pad). */
export const padClass = "flex flex-col gap-2 px-6 pb-5 max-md:px-4 max-md:pb-4";

/**
 * The bar at the foot of a card that holds changes: closed (no height) until something is not saved, then it opens and says what the
 * change does; "Saving" while the call runs, "Saved" for a moment after it. A bar that is shut cannot be tabbed into.
 */
export function SaveBar({
  state,
  text,
  danger,
  disabled,
  saveLabel,
  onUndo,
  onSave,
}: {
  state: "idle" | "dirty" | "saving" | "ok";
  text: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  saveLabel?: ReactNode;
  onUndo?: () => void;
  onSave: () => void;
}) {
  const t = useTx();
  const open = state !== "idle";
  return (
    <div
      inert={!open || state === "ok"}
      data-on={open || undefined}
      data-state={state}
      className="flex max-h-0 items-center gap-2 overflow-hidden border-t border-transparent bg-[color-mix(in_srgb,var(--text)_3%,transparent)] px-6 transition-[max-height,padding,border-color] duration-300 ease-(--e) data-on:max-h-16 data-on:border-line data-on:py-3 max-md:flex-wrap max-md:px-4 max-md:data-on:max-h-48"
    >
      <span role="status" className={cx("me-auto text-[13px] leading-[18px] max-md:basis-full", state === "ok" ? "text-accent-text" : "text-warn-text")}>
        {text}
      </span>
      <div className={cx("flex items-center gap-2", state === "ok" && "invisible")}>
        {onUndo && (
          <Button size="md" disabled={state === "saving"} onClick={onUndo}>
            {t("grp.bar.undo")}
          </Button>
        )}
        <Button size="md" variant={danger ? "danger" : "primary"} disabled={disabled || state === "saving"} onClick={onSave}>
          {state === "saving" && <span aria-hidden className="size-3.5 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent" />}
          {saveLabel ?? t("grp.bar.save")}
        </Button>
      </div>
    </div>
  );
}

/** The state a save bar shows, from what the card knows: unsaved changes, a running call, the moment after it. */
export const barState = (dirty: boolean, saving: boolean, ok: boolean): "idle" | "dirty" | "saving" | "ok" => (saving ? "saving" : dirty ? "dirty" : ok ? "ok" : "idle");

/** The round-cornered square with a glyph, 36px, in front of a row of what the group gives. */
export function GlyphTile({ children }: { children: ReactNode }) {
  return (
    <span aria-hidden className="grid size-9 place-items-center rounded-[10px] bg-step text-sec [&_svg]:size-[18px]">
      {children}
    </span>
  );
}

/** A decorative tick box: the row around it is the checkbox. */
export function Tick({ on, className }: { on: boolean; className?: string }) {
  return (
    <span
      aria-hidden
      className={cx(
        "grid size-5 flex-none place-items-center rounded-chip border transition-[background-color,border-color] duration-150 ease-out",
        on ? "border-accent-mark bg-accent-mark text-on-accent" : "border-[color-mix(in_srgb,var(--text)_28%,transparent)] text-transparent",
        className,
      )}
    >
      <Icon name="check" size={14} strokeWidth={2.5} />
    </span>
  );
}

/** A line of small print with a glyph in front (up-nt). */
export function Note({ icon, children, className }: { icon: "info" | "clock"; children: ReactNode; className?: string }) {
  return (
    <p className={cx("m-0 grid grid-cols-[14px_minmax(0,1fr)] gap-2 text-xs leading-4 text-muted", className)}>
      <Icon name={icon} size={14} className="mt-px block flex-none" />
      <span>{children}</span>
    </p>
  );
}

/**
 * The node a refused group update names ("node_held: node=<id>"): the owner holds it back, the group was not given it, so a group's page
 * cannot switch it on. "" when the refusal names no node; undefined for any other failure.
 */
export function heldNodeOf(e: unknown): string | undefined {
  const c = ConnectError.from(e);
  if (c.code !== Code.FailedPrecondition || errorCode(c.rawMessage) !== "node_held") return undefined;
  return errorVars(c.rawMessage).node ?? "";
}

/** What to do about it: the way is the node's own page, "Who gets it", not the group's. */
export function HeldNote({ nodeId, className }: { nodeId: string; className?: string }) {
  const t = useTx();
  const nodes = useQuery(nodesQuery).data?.nodes;
  const name = nodes?.find((n) => n.id === nodeId)?.name ?? "";
  return (
    <Callout
      kind="warn"
      alert
      className={className}
      action={
        nodeId ? (
          <Link to="/nodes/$id" params={{ id: nodeId }} className={buttonClass("secondary", "md")}>
            {t("grp.held.open")}
          </Link>
        ) : undefined
      }
    >
      <b className="block font-medium">{name ? t("grp.held.title", { node: name }) : t("grp.held.titleAny")}</b>
      <span className="mt-0.5 block text-sec">{t("grp.held.body")}</span>
    </Callout>
  );
}
