import { useRef } from "react";
import { useT } from "@/i18n";
import { boundsOf, type Rect } from "./geometry";
import { useStoreVersion, type GraphStore } from "./store";
import type { Pos } from "./types";

export const MINIMAP_W = 170;
export const MINIMAP_H = 110;
const PAD = 8;

/** The overview in the corner: node rects (selected ones in the accent) and the viewport; click or drag recentres the view. */
export function Minimap({ store, ids, selected, onCenter }: { store: GraphStore; ids: string[]; selected: ReadonlySet<string>; onCenter: (world: Pos) => void }) {
  useStoreVersion(store, "geo");
  useStoreVersion(store, "view");
  useStoreVersion(store, "measure");
  const t = useT();
  const drag = useRef(false);
  const { view, size } = store;
  const rects: { id: string; r: Rect }[] = [];
  for (const id of ids) {
    const r = store.nodeRect(id);
    if (r) rects.push({ id, r });
  }
  const vp: Rect = { x: -view.x / view.z, y: -view.y / view.z, w: size.w / view.z, h: size.h / view.z };
  const b = boundsOf([...rects.map((x) => x.r), ...(size.w > 0 ? [vp] : [])]) ?? { x: 0, y: 0, w: 1, h: 1 };
  const s = Math.min((MINIMAP_W - PAD * 2) / Math.max(b.w, 1), (MINIMAP_H - PAD * 2) / Math.max(b.h, 1));
  const ox = (MINIMAP_W - b.w * s) / 2 - b.x * s;
  const oy = (MINIMAP_H - b.h * s) / 2 - b.y * s;

  const toWorld = (e: React.PointerEvent<SVGSVGElement>): Pos => {
    const r = e.currentTarget.getBoundingClientRect();
    return { x: (e.clientX - r.left - ox) / s, y: (e.clientY - r.top - oy) / s };
  };

  return (
    <div className="mg-gminimap" data-gui>
      <svg
        width={MINIMAP_W}
        height={MINIMAP_H}
        role="img"
        aria-label={t("graph.minimap")}
        className="block cursor-pointer touch-none"
        onPointerDown={(e) => {
          if (e.button !== 0) return;
          drag.current = true;
          e.currentTarget.setPointerCapture(e.pointerId);
          onCenter(toWorld(e));
        }}
        onPointerMove={(e) => drag.current && onCenter(toWorld(e))}
        onPointerUp={() => (drag.current = false)}
        onPointerCancel={() => (drag.current = false)}
      >
        {rects.map(({ id, r }) => (
          <rect
            key={id}
            x={r.x * s + ox}
            y={r.y * s + oy}
            width={Math.max(r.w * s, 2)}
            height={Math.max(r.h * s, 2)}
            rx={1.5}
            className={selected.has(id) ? "fill-accent" : "fill-muted/45"}
          />
        ))}
        {size.w > 0 && (
          <rect x={vp.x * s + ox} y={vp.y * s + oy} width={vp.w * s} height={vp.h * s} rx={2} className="fill-accent/10 stroke-accent" strokeWidth={1.25} />
        )}
      </svg>
    </div>
  );
}
