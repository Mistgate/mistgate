import type { WayTone } from "@/lib/fleet-map";
import { cx } from "@/lib/cx";
import "./nodes-ways.css";

// The small pieces the ways of the Nodes screen share (the list's "From Russia" column and the Ways view): the glyph of a way,
// and the colour of a word by its tone. The board's rw-gl and rw-l1 (pn.css).

export type GlyphKind = WayTone | "shut" | "busy" | "hid" | "off";

/** The glyph of a way: a disc that works, a half disc that works in part, an X that does not, a ring where nothing is known. */
export function WayGlyph({ kind, className }: { kind: GlyphKind; className?: string }) {
  return <i aria-hidden className={cx("wg", `wg-${kind === "off" ? "none" : kind}`, className)} />;
}

/** The word of a way in the colour of its tone: red and amber say so, the rest is the text colour. */
export const toneWord: Record<WayTone, string> = { ok: "", part: "text-warn-text", bad: "text-danger-text", none: "" };
