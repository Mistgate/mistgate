import { navPaths } from "@/components/ui/icons";

// The glyphs of the assistant's window: the board's own paths on its 24 grid (PnAssistant), drawn by `.as .i` (assistant.css).
export const P = {
  back: "M15 6l-6 6 6 6",
  list: "M9.5 7h10M9.5 12h10M9.5 17h10M4.5 7h.01M4.5 12h.01M4.5 17h.01",
  plus: "M12 5v14M5 12h14",
  gear: navPaths.settings[0],
  x: "M6 6l12 12M18 6L6 18",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
  trash: "M5 7h14M10 7V4.5h4V7M7 7l1 12.5h8L17 7",
  out: "M7 17L17 7M9 7h8v8",
  link: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
  send: "M12 19V5M6 11l6-6 6 6",
  check: "M5 12.5l4.5 4.5L19 7.5",
  warn: "M12 4.5l8.5 14.5h-17zM12 10.5v4M12 17v.01",
  chev: "M7 10l5 5 5-5",
  server: "M4 5h16v6H4zM4 13h16v6H4zM8 8h.01M8 16h.01",
  plug: "M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8v.01",
  bookmark: "M7 4h10v16l-5-4-5 4z",
  open: "M14 4h6v6M20 4l-9 9M18 14v5H5V6h5",
  lock: "M6 11h12v9H6zM8.5 11V8a3.5 3.5 0 0 1 7 0v3",
} as const;

export type Glyph = keyof typeof P;

/** One glyph of the window. `flip` turns it over in right-to-left (an arrow that points the way of reading). */
export function I({ g, flip }: { g: Glyph; flip?: boolean }) {
  return (
    <svg className="i" viewBox="0 0 24 24" aria-hidden data-flip={flip ? "" : undefined}>
      <path d={P[g]} />
    </svg>
  );
}
