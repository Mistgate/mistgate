import { useSyncExternalStore } from "react";
import type { Rect } from "./geometry";
import type { Pos, PortRef, View } from "./types";

export interface NodeMeasure {
  w: number;
  h: number;
  /** Port centres relative to the card's top-left corner, in world units. */
  ports: Map<string, Pos>;
}

type Channel = "view" | "geo" | "measure";

/**
 * The graph's live, non-React state: the view (pan and zoom), the positions while dragging and the measured sizes.
 * Pointer moves write here and to the DOM directly; React reads it through version counters (useStoreVersion), so a
 * pan or a drag re-renders only the parts that draw from it (the wires, the minimap).
 */
export class GraphStore {
  view: View = { x: 0, y: 0, z: 1 };
  size = { w: 0, h: 0 };
  live = new Map<string, Pos>();
  measure = new Map<string, NodeMeasure>();
  cardEls = new Map<string, HTMLElement>();
  world: HTMLElement | null = null;
  canvas: HTMLElement | null = null;
  private versions: Record<Channel, number> = { view: 0, geo: 0, measure: 0 };
  private subs: Record<Channel, Set<() => void>> = { view: new Set(), geo: new Set(), measure: new Set() };

  subscribe = (ch: Channel) => (cb: () => void) => {
    this.subs[ch].add(cb);
    return () => void this.subs[ch].delete(cb);
  };
  version = (ch: Channel) => this.versions[ch];
  private bump(ch: Channel) {
    this.versions[ch]++;
    this.subs[ch].forEach((cb) => cb());
  }

  /** Ask the layout effects to look again (the canvas was resized by hand). */
  poke() {
    this.bump("measure");
  }

  attach(canvas: HTMLElement, world: HTMLElement | null) {
    this.canvas = canvas;
    this.world = world;
  }

  setView(v: View) {
    this.view = v;
    if (this.world) this.world.style.transform = `translate(${v.x}px, ${v.y}px) scale(${v.z})`;
    if (this.canvas) {
      const step = 24 * v.z < 12 ? 48 * v.z : 24 * v.z;
      this.canvas.style.setProperty("--gstep", `${step}px`);
      // the dots are quiet and fade out when zoomed far out
      this.canvas.style.setProperty("--gdot", `${(7 * Math.min(1, Math.max(0, (v.z - 0.35) / 0.35))).toFixed(2)}%`);
      this.canvas.style.setProperty("--gx", `${v.x}px`);
      this.canvas.style.setProperty("--gy", `${v.y}px`);
    }
    this.bump("view");
  }

  setSize(w: number, h: number) {
    if (w === this.size.w && h === this.size.h) return;
    this.size = { w, h };
    this.bump("measure");
  }

  /** A node's position while it moves (also moves its card). */
  setLive(id: string, p: Pos, moveCard = true) {
    this.live.set(id, p);
    const el = this.cardEls.get(id);
    if (el && moveCard) el.style.transform = `translate(${p.x}px, ${p.y}px)`;
  }
  commitLive() {
    this.bump("geo");
  }

  setMeasure(id: string, m: NodeMeasure) {
    const old = this.measure.get(id);
    if (old && old.w === m.w && old.h === m.h && old.ports.size === m.ports.size) {
      let same = true;
      for (const [k, p] of m.ports) {
        const o = old.ports.get(k);
        if (!o || Math.abs(o.x - p.x) > 0.01 || Math.abs(o.y - p.y) > 0.01) same = false;
      }
      if (same) return;
    }
    this.measure.set(id, m);
    this.bump("measure");
    this.bump("geo");
  }
  dropNode(id: string) {
    const had = this.measure.delete(id);
    this.live.delete(id);
    if (had) {
      this.bump("measure");
      this.bump("geo");
    }
  }

  nodeRect(id: string, fallbackW = 220, fallbackH = 80): Rect | null {
    const p = this.live.get(id);
    if (!p) return null;
    const m = this.measure.get(id);
    return { x: p.x, y: p.y, w: m?.w || fallbackW, h: m?.h || fallbackH };
  }

  portWorld(ref: PortRef): Pos | null {
    const p = this.live.get(ref.node);
    const o = this.measure.get(ref.node)?.ports.get(ref.port);
    return p && o ? { x: p.x + o.x, y: p.y + o.y } : null;
  }

  /** Screen point (client) to world coordinates. */
  toWorld(clientX: number, clientY: number): Pos {
    const r = this.canvas?.getBoundingClientRect();
    const v = this.view;
    return { x: (clientX - (r?.left ?? 0) - v.x) / v.z, y: (clientY - (r?.top ?? 0) - v.y) / v.z };
  }
  /** Client point to canvas-relative screen coordinates. */
  toLocal(clientX: number, clientY: number): Pos {
    const r = this.canvas?.getBoundingClientRect();
    return { x: clientX - (r?.left ?? 0), y: clientY - (r?.top ?? 0) };
  }

  /** The port's button, found in its card (a port can briefly exist twice while a row claims it, so there is no registry). */
  portEl(ref: PortRef): HTMLElement | undefined {
    const id = ref.port.replace(/["\\]/g, "\\$&");
    return this.cardEls.get(ref.node)?.querySelector<HTMLElement>(`[data-port-id="${id}"]`) ?? undefined;
  }
}

export function useStoreVersion(store: GraphStore, ch: Channel): number {
  return useSyncExternalStore(store.subscribe(ch), () => store.version(ch));
}
