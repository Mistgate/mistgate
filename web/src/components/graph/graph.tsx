import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { GraphContext, nodeLabel, textOf, type GraphCtx, type PortHighlight } from "./context";
import { EdgeLayer, routeFor, wireInk, type RenderEdge } from "./edges";
import { readPref, writePref } from "@/lib/storage";
import { accumulate, glideStopped, inertiaStep, releaseVelocity, smoothStep, wheelFactor } from "./motion";
import { GraphHelp, modKey, type HelpTab } from "./help";
import { decideDrop, grabIdle, grabStep, grabTargets, handleAt, handlePoint, type End, type GrabState, type Handle } from "./reconnect";
import { MAX_ZOOM, MIN_ZOOM, boundsOf, clamp, dragWire, fitView, nearestPoint, rectsIntersect, snapTo, zoomAround, type Rect } from "./geometry";
import { clearOf, computeLayout, tabOrder } from "./layout";
import { Minimap } from "./minimap";
import { NodeCard } from "./node";
import { checkConnection, findPort, orient, validTargets, type Verdict } from "./rules";
import { GraphStore, useStoreVersion } from "./store";
import { portKey, type ConnectCheck, type GraphEdge, type GraphProps, type Pos, type PortRef, type Positions, type View } from "./types";
import { currentTarget, idle, wiringStep, type WiringAction, type WiringState } from "./wiring";
import { pathsThrough, wireFocus } from "./focus";

const DRAG_THRESHOLD = 3;
const SNAP_RADIUS = 20;
const GRID = 8;
const INTERACTIVE = "button,a,input,select,textarea,label,summary,[role=switch],[role=slider],[role=button],[role=checkbox],[role=combobox],[role=menuitem],[role=tab],[contenteditable=true],[data-nodrag]";
const TYPING = "input,textarea,select,[contenteditable=true]";

type Mode =
  | { kind: "pan"; id: number; sx: number; sy: number; v: View; moved: boolean; samples: { x: number; y: number; t: number }[] }
  | { kind: "node"; id: number; sx: number; sy: number; node: string; ids: string[]; starts: Map<string, Pos> | null; mod: boolean; was: boolean; canDrag: boolean }
  | { kind: "marquee"; id: number; sx: number; sy: number; base: string[]; moved: boolean }
  | { kind: "wire"; id: number; sx: number; sy: number; ref: PortRef; started: boolean; source: PortRef; replace?: string; snap: PortRef | null; grab?: { edge: RenderEdge; end: End }; end?: Pos; bad?: string }
  | { kind: "pinch"; d0: number; z0: number; v0: View; c0: Pos };

interface DragWire {
  source: PortRef;
  targets: ReadonlySet<string>;
  replace?: string;
}

const reducedMotion = () => typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
const storageKey = (k: string) => `mg.graph.${k}`;

export function loadPositions(layoutKey?: string): Positions {
  if (!layoutKey) return {};
  try {
    const raw = JSON.parse(localStorage.getItem(storageKey(layoutKey)) ?? "{}") as Record<string, Partial<Pos>>;
    const out: Positions = {};
    for (const [id, p] of Object.entries(raw)) if (p && Number.isFinite(p.x) && Number.isFinite(p.y)) out[id] = { x: p.x!, y: p.y! };
    return out;
  } catch {
    return {};
  }
}
function savePositions(layoutKey: string | undefined, p: Positions) {
  if (!layoutKey) return;
  try {
    localStorage.setItem(storageKey(layoutKey), JSON.stringify(p));
  } catch {
    /* private mode: positions just are not kept */
  }
}

const ease = (t: number) => 1 - Math.pow(1 - t, 3);
const nowMs = () => performance.now();

export function Graph<D = unknown>(props: GraphProps<D>) {
  const { nodes, edges, typeColors, layoutKey, canConnect, onConnect, onEdgeDelete, renderEdgeMenu, readOnly = false, minimap = true, legend, height = 560, ariaLabel, className } = props;
  const [zoomHint, setZoomHint] = useState(false);
  const [helpOwn, setHelpOwn] = useState<HelpTab | "">(() => (readPref("mg.graph.help") === "legend" ? "legend" : readPref("mg.graph.help") === "shortcuts" ? "shortcuts" : ""));
  const t = useT();
  const [store] = useState(() => new GraphStore());
  const rootRef = useRef<HTMLDivElement>(null);
  const worldRef = useRef<HTMLDivElement>(null);
  const dragPathRef = useRef<SVGPathElement>(null);
  const marqueeRef = useRef<HTMLDivElement>(null);
  const hintRef = useRef<HTMLDivElement>(null);
  const instrId = `graph-help-${useId()}`;

  // ----- committed state
  // with autoArrange the saved places are only the user's own: without a move of theirs the cards are laid out afresh
  const customKey = layoutKey ? `mg.graph.${layoutKey}.custom` : "";
  const startCustom = !props.autoArrange || (!!customKey && readPref(customKey) === "1");
  const custom = useRef(startCustom);
  const [own, setOwn] = useState<Positions>(() => (props.positions || !startCustom ? {} : loadPositions(layoutKey)));
  const positions = props.positions ?? own;
  const [selOwn, setSelOwn] = useState<string[]>([]);
  const sel = props.selected ?? selOwn;
  const selSet = useMemo(() => new Set(sel), [sel]);
  const [selEdge, setSelEdge] = useState<string | null>(null);
  const [menu, setMenu] = useState<{ id: string; focus: boolean } | null>(null);
  const [virtual, setVirtual] = useState<RenderEdge[]>([]);
  const [drag, setDrag] = useState<DragWire | null>(null);
  const [kb, setKb] = useState<WiringState>(idle);
  const [order, setOrder] = useState<string[]>([]);
  const [compact, setCompact] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  // the card the mouse rests on, for focusPaths; set after a short pause so crossing the canvas does not flicker
  const [hoverCard, setHoverCard] = useState<string | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hoverWanted = useRef<string | null>(null);
  const restOn = useCallback((id: string | null) => {
    if (hoverWanted.current === id) return;
    hoverWanted.current = id;
    if (hoverTimer.current) clearTimeout(hoverTimer.current);
    hoverTimer.current = setTimeout(() => setHoverCard(id), id ? 160 : 220);
  }, []);
  useEffect(() => () => void (hoverTimer.current && clearTimeout(hoverTimer.current)), []);

  const measureVersion = useStoreVersion(store, "measure");

  const canWire = !readOnly && !!onConnect;
  const canDelete = !readOnly && (!!onEdgeDelete || !!props.onReconnect);
  const canGrab = !readOnly && !!props.onReconnect;

  // the freshest props and state for handlers that outlive a render
  const live = useRef({ nodes, edges, virtual, positions, sel, selEdge, canConnect, onConnect, onEdgeDelete, canWire, canDelete, canGrab, readOnly, kb, drag, props, menu });
  useLayoutEffect(() => {
    live.current = { nodes, edges, virtual, positions, sel, selEdge, canConnect, onConnect, onEdgeDelete, canWire, canDelete, canGrab, readOnly, kb, drag, props, menu };
  });
  useEffect(() => {
    live.current.props.onEdgeSelect?.(selEdge);
  }, [selEdge]);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => void (mounted.current = false);
  }, []);

  const allEdges = useMemo<RenderEdge[]>(() => [...edges, ...virtual], [edges, virtual]);
  const connected = useMemo(() => {
    const s = new Set<string>();
    for (const e of allEdges) {
      if (e.virtual === "error") continue;
      s.add(portKey(e.from));
      s.add(portKey(e.to));
    }
    return s;
  }, [allEdges]);

  const announce = useCallback((text: string) => setAnnouncement(text), []);
  const titleOf = useCallback((id: string) => {
    const n = live.current.nodes.find((x) => x.id === id);
    return n ? nodeLabel(n) : id;
  }, []);
  const portName = useCallback(
    (ref: PortRef) => {
      const p = findPort(live.current.nodes, ref);
      return `${titleOf(ref.node)} ${(p && textOf(p.label)) || ref.port}`.trim();
    },
    [titleOf],
  );

  // ----- selection
  const setSel = useCallback((ids: string[]) => {
    const { props: p } = live.current;
    if (!p.selected) setSelOwn(ids);
    p.onSelectionChange?.(ids);
  }, []);
  const selectOnly = useCallback(
    (ids: string[]) => {
      const cur = live.current.sel;
      if (cur.length === ids.length && cur.every((x, i) => x === ids[i])) return;
      setSel(ids);
    },
    [setSel],
  );

  // ----- positions
  const commitPositions = useCallback((next: Positions, byUser = false) => {
    const { props: p } = live.current;
    if (byUser && p.autoArrange && !custom.current) {
      custom.current = true;
      if (p.layoutKey) writePref(`mg.graph.${p.layoutKey}.custom`, "1");
    }
    if (!p.positions) {
      setOwn(next);
      savePositions(p.layoutKey, next);
    }
    p.onPositionsChange?.(next);
  }, []);

  const refreshOrder = useCallback(() => {
    const list = live.current.nodes.flatMap((n) => {
      const r = store.nodeRect(n.id, n.width ?? 220);
      return r ? [{ id: n.id, x: r.x, y: r.y, w: r.w }] : [];
    });
    const next = tabOrder(list);
    setOrder((o) => (o.length === next.length && o.every((x, i) => x === next[i]) ? o : next));
  }, [store]);

  // the store's live positions follow the committed ones (unless an animation is writing them)
  const animating = useRef(false);
  useLayoutEffect(() => {
    if (animating.current) return;
    const ids = new Set(nodes.map((n) => n.id));
    for (const id of [...store.live.keys()]) if (!ids.has(id)) store.live.delete(id);
    for (const n of nodes) {
      const p = positions[n.id];
      if (p) store.live.set(n.id, p);
    }
    store.commitLive();
  }, [store, nodes, positions]);

  const nodeKey = nodes.map((n) => n.id).join("\u0001");
  useLayoutEffect(refreshOrder, [nodeKey, refreshOrder, measureVersion]);

  // ----- view
  const viewAnim = useRef(0);
  const zoomTarget = useRef<{ z: number; p: Pos } | null>(null);
  const zoomRaf = useRef(0);
  const glideRaf = useRef(0);
  const cancelViewAnim = useCallback(() => {
    cancelAnimationFrame(viewAnim.current);
    cancelAnimationFrame(zoomRaf.current);
    cancelAnimationFrame(glideRaf.current);
    viewAnim.current = zoomRaf.current = glideRaf.current = 0;
    zoomTarget.current = null;
  }, []);
  const animateView = useCallback(
    (target: View, ms = 240) => {
      cancelViewAnim();
      const from = store.view;
      if (reducedMotion() || ms <= 0 || typeof requestAnimationFrame !== "function") return store.setView(target);
      const t0 = nowMs();
      const step = (now: number) => {
        const k = ease(Math.min(1, (now - t0) / ms));
        store.setView({ x: from.x + (target.x - from.x) * k, y: from.y + (target.y - from.y) * k, z: from.z + (target.z - from.z) * k });
        viewAnim.current = k < 1 ? requestAnimationFrame(step) : 0;
      };
      viewAnim.current = requestAnimationFrame(step);
    },
    [store, cancelViewAnim],
  );
  const allRects = useCallback(() => {
    const out: Rect[] = [];
    for (const n of live.current.nodes) {
      const r = store.nodeRect(n.id, n.width ?? 220);
      if (r) out.push(r);
    }
    return out;
  }, [store]);
  const fitTarget = useCallback(() => {
    const b = boundsOf(allRects());
    const floor = live.current.props.minFitZoom ?? MIN_ZOOM;
    const v = fitView(b, store.size, live.current.props.fitPad ?? 32, floor);
    const focus = live.current.props.fitFocus ? store.nodeRect(live.current.props.fitFocus) : null;
    // wider than the canvas even at the floor: keep the focus (the node itself) in the middle instead of cutting both ends
    if (b && focus && v.z === floor && b.w * v.z > store.size.w - 64) return { ...v, x: store.size.w / 2 - (focus.x + focus.w / 2) * v.z };
    return v;
  }, [allRects, store]);
  const fit = useCallback((animate = true) => animateView(fitTarget(), animate ? 260 : 0), [animateView, fitTarget]);
  const zoomBy = useCallback(
    (f: number) => animateView(zoomAround(store.view, { x: store.size.w / 2, y: store.size.h / 2 }, store.view.z * f), 160),
    [animateView, store],
  );
  const resetZoom = useCallback(() => animateView(zoomAround(store.view, { x: store.size.w / 2, y: store.size.h / 2 }, 1), 160), [animateView, store]);
  const centerOn = useCallback(
    (w: Pos, animate = false) => {
      const v = store.view;
      const target = { ...v, x: store.size.w / 2 - w.x * v.z, y: store.size.h / 2 - w.y * v.z };
      if (animate) return animateView(target, 260);
      cancelViewAnim();
      store.setView(target);
    },
    [store, cancelViewAnim, animateView],
  );

  /** Wheel zoom: events add up into a target scale, and the view eases toward it around the pointer, a frame at a time. */
  const wheelZoom = useCallback(
    (factor: number, p: Pos) => {
      const z = accumulate(zoomTarget.current?.z ?? store.view.z, factor, MIN_ZOOM, MAX_ZOOM);
      zoomTarget.current = { z, p };
      if (reducedMotion() || typeof requestAnimationFrame !== "function") {
        store.setView(zoomAround(store.view, p, z));
        zoomTarget.current = null;
        return;
      }
      if (zoomRaf.current) return;
      let last = nowMs();
      const step = (now: number) => {
        const target = zoomTarget.current;
        if (!target) {
          zoomRaf.current = 0;
          return;
        }
        const nz = smoothStep(store.view.z, target.z, Math.min(48, now - last));
        last = now;
        store.setView(zoomAround(store.view, target.p, nz));
        if (nz === target.z) {
          zoomTarget.current = null;
          zoomRaf.current = 0;
        } else zoomRaf.current = requestAnimationFrame(step);
      };
      zoomRaf.current = requestAnimationFrame(step);
    },
    [store],
  );

  /** A drag released while moving glides on and slows down. */
  const glide = useCallback(
    (v0: { x: number; y: number }) => {
      if (reducedMotion() || typeof requestAnimationFrame !== "function" || glideStopped(v0, 0.08)) return;
      let vel = v0;
      let last = nowMs();
      const step = (now: number) => {
        const s = inertiaStep(vel, Math.min(48, now - last));
        last = now;
        vel = s.v;
        const cur = store.view;
        store.setView({ ...cur, x: cur.x + s.dx, y: cur.y + s.dy });
        glideRaf.current = glideStopped(vel) ? 0 : requestAnimationFrame(step);
      };
      glideRaf.current = requestAnimationFrame(step);
    },
    [store],
  );

  const showZoomHint = useCallback(() => {
    if (readPref("mg.graph.zoomhint") === "1") return;
    setZoomHint(true);
    window.clearTimeout(hintTimer.current);
    hintTimer.current = window.setTimeout(() => {
      setZoomHint(false);
      writePref("mg.graph.zoomhint", "1");
    }, 1800);
  }, []);
  const hintTimer = useRef(0);

  // canvas size
  useLayoutEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    store.attach(el, worldRef.current);
    store.setView(store.view);
    const apply = () => {
      store.setSize(el.clientWidth, el.clientHeight);
      setCompact(el.clientWidth < 520);
    };
    apply();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(apply);
    ro.observe(el);
    return () => ro.disconnect();
  }, [store]);

  // wheel needs a non-passive listener: Ctrl/Cmd and pinch zoom, the rest pans
  useEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      const target = e.target as HTMLElement;
      if (target.closest("[data-gui],textarea,[data-wheel]")) return;
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? el.clientHeight : 1;
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        cancelAnimationFrame(viewAnim.current);
        cancelAnimationFrame(glideRaf.current);
        wheelZoom(wheelFactor(e.deltaY, e.deltaMode), store.toLocal(e.clientX, e.clientY));
        return;
      }
      // a plain wheel scrolls the page (the graph never traps it); a sideways swipe, Shift + wheel and, full screen, any wheel pan
      const sideways = e.deltaX !== 0 && Math.abs(e.deltaX) > Math.abs(e.deltaY) * 0.6;
      if (live.current.props.wheelPans || sideways || e.shiftKey) {
        e.preventDefault();
        cancelViewAnim();
        const v = store.view;
        const dx = e.shiftKey && e.deltaX === 0 ? e.deltaY : e.deltaX;
        const dy = e.shiftKey && e.deltaX === 0 ? 0 : e.deltaY;
        store.setView({ ...v, x: v.x - dx * unit, y: v.y - dy * unit });
        return;
      }
      showZoomHint();
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [store, cancelViewAnim, wheelZoom, showZoomHint]);

  // ----- first layout and nodes without a position
  const fitted = useRef(false);
  const sizes = useCallback(
    () =>
      live.current.nodes.map((n) => {
        const m = store.measure.get(n.id);
        return { id: n.id, rank: n.rank, w: m?.w || n.width || 220, h: m?.h || 96 };
      }),
    [store],
  );
  const lastSig = useRef("");
  /** The cards' sizes in one string: a change of it is a reason to lay out again. */
  const sig = useCallback(
    () =>
      live.current.nodes
        .map((n) => {
          const m = store.measure.get(n.id);
          return m ? `${n.id}:${Math.round(m.w)}x${Math.round(m.h)}` : "";
        })
        .join("|"),
    [store],
  );
  const layoutEdges = useCallback(() => live.current.edges.map((e) => ({ from: e.from.node, to: e.to.node })), []);
  useLayoutEffect(() => {
    if (measureVersion < 0) return; // measureVersion is a dependency: re-run when a card is measured
    if (!nodes.length || animating.current) return;
    const missing = nodes.filter((n) => !positions[n.id]);
    if (missing.length) {
      if (!missing.every((n) => store.measure.has(n.id))) return;
      // a tick later, on what the cards measure once their rows have claimed their ports (the first measure is of a card
      // that still draws a port twice)
      const timer = setTimeout(() => {
        if (!missing.every((n) => store.measure.has(n.id))) return;
        const all = sizes();
        const full = (live.current.props.arrange ?? computeLayout)(all, layoutEdges());
        const next: Positions = {};
        for (const n of nodes) if (positions[n.id]) next[n.id] = positions[n.id]!;
        const placed = nodes.length > missing.length;
        if (!placed || live.current.props.rearrangeOnChange) Object.assign(next, full);
        else {
          const taken = all.filter((a) => next[a.id]).map((a) => ({ ...next[a.id]!, w: a.w, h: a.h }));
          for (const a of all) {
            if (next[a.id]) continue;
            const p = clearOf({ ...full[a.id]!, w: a.w, h: a.h }, taken);
            next[a.id] = p;
            taken.push({ ...p, w: a.w, h: a.h });
          }
        }
        lastSig.current = sig();
        commitPositions(next);
      }, 0);
      return () => clearTimeout(timer);
    }
    if (!fitted.current && store.size.w > 0 && nodes.every((n) => store.measure.has(n.id) && store.live.has(n.id))) {
      if (live.current.props.height === "fit") {
        // the canvas takes the content at zoom 1, between 560px and the viewport less 200px; then it is fitted in that
        const b = boundsOf(allRects());
        const target = Math.round(clamp((b?.h ?? 0) + 80, 560, Math.max(560, window.innerHeight - 200)));
        if (rootRef.current && rootRef.current.style.height !== `${target}px`) {
          rootRef.current.style.height = `${target}px`;
          requestAnimationFrame(() => store.poke());
          return;
        }
      }
      fitted.current = true;
      fit(false);
    }
  }, [nodes, positions, measureVersion, store, sizes, layoutEdges, commitPositions, fit, allRects, sig]);

  // other nodes: fit again once they are placed
  const lastKey = useRef(nodeKey);
  useLayoutEffect(() => {
    if (lastKey.current !== nodeKey) {
      lastKey.current = nodeKey;
      fitted.current = false;
    }
  }, [nodeKey]);

  // a way picked from outside (the list, a link) is brought into view with an animated pan
  const selectedId = props.selected?.length === 1 ? props.selected[0] : undefined;
  useEffect(() => {
    const id = selectedId;
    const r = id ? store.nodeRect(id) : null;
    if (!r || !fitted.current || store.size.w === 0) return;
    const v = store.view;
    const m = 24;
    const x0 = r.x * v.z + v.x;
    const y0 = r.y * v.z + v.y;
    if (x0 < m || y0 < m || x0 + r.w * v.z > store.size.w - m || y0 + r.h * v.z > store.size.h - m) centerOn({ x: r.x + r.w / 2, y: r.y + r.h / 2 }, true);
  }, [selectedId, store, centerOn]);

  const animateTo = useCallback(
    (target: Positions, ms = 380) => {
      const from = new Map(store.live);
      const finish = () => {
        animating.current = false;
        commitPositions(target);
        refreshOrder();
      };
      animateView(fitView(boundsOf(live.current.nodes.map((n) => ({ ...target[n.id]!, w: store.measure.get(n.id)?.w || n.width || 220, h: store.measure.get(n.id)?.h || 96 }))), store.size, live.current.props.fitPad ?? 32, live.current.props.minFitZoom ?? MIN_ZOOM), ms);
      if (reducedMotion() || typeof requestAnimationFrame !== "function") {
        for (const [id, p] of Object.entries(target)) store.setLive(id, p);
        store.commitLive();
        return finish();
      }
      animating.current = true;
      const t0 = nowMs();
      const step = (now: number) => {
        const k = ease(Math.min(1, (now - t0) / ms));
        for (const [id, p] of Object.entries(target)) {
          const f = from.get(id) ?? p;
          store.setLive(id, { x: f.x + (p.x - f.x) * k, y: f.y + (p.y - f.y) * k });
        }
        store.commitLive();
        if (k < 1) requestAnimationFrame(step);
        else finish();
      };
      requestAnimationFrame(step);
    },
    [store, animateView, commitPositions, refreshOrder],
  );

  const arrange = useCallback(() => {
    const target = (live.current.props.arrange ?? computeLayout)(sizes(), layoutEdges());
    // Arrange gives the layout back to the graph
    custom.current = false;
    if (live.current.props.layoutKey) writePref(`mg.graph.${live.current.props.layoutKey}.custom`, null);
    lastSig.current = sig();
    animateTo(target);
  }, [sizes, layoutEdges, animateTo, sig]);

  // autoArrange: when the cards' sizes change (a state changed a line of text) and the user has not placed them, lay out again
  useLayoutEffect(() => {
    if (measureVersion < 0 || !live.current.props.autoArrange || custom.current || !fitted.current || animating.current) return;
    const s = sig();
    if (!s || s === lastSig.current) return;
    const timer = setTimeout(() => {
      if (custom.current) return;
      lastSig.current = sig();
      const target = (live.current.props.arrange ?? computeLayout)(sizes(), layoutEdges());
      const next: Positions = {};
      for (const n of live.current.nodes) if (target[n.id]) next[n.id] = target[n.id]!;
      commitPositions(next);
    }, 80);
    return () => clearTimeout(timer);
  }, [measureVersion, sig, sizes, layoutEdges, commitPositions]);

  // ----- connecting
  const verdictFor = useCallback((from: PortRef, to: PortRef, ignoreEdge?: string): Verdict => {
    const l = live.current;
    const es = [...l.edges, ...l.virtual.filter((v) => v.virtual === "pending")].filter((e) => e.id !== ignoreEdge);
    return checkConnection(l.nodes, es, from, to, l.canConnect);
  }, []);
  const reasonOf = useCallback(
    (v: Verdict) => (v.ok ? "" : v.code === "custom" ? (v.message ?? "") : t(`graph.err.${v.code}` as "graph.err.self")),
    [t],
  );

  const seq = useRef(0);
  const connectPorts = useCallback(
    async (a: PortRef, b: PortRef, replace?: string) => {
      const l = live.current;
      if (!l.canWire || !l.onConnect) return;
      const o = orient(l.nodes, a, b);
      if (!o) return;
      const v = verdictFor(o.from, o.to, replace);
      const names = { from: portName(o.from), to: portName(o.to) };
      if (!v.ok) return announce(t("graph.failed", { ...names, reason: reasonOf(v) }));
      const id = `\u0000p${++seq.current}`;
      setVirtual((vs) => [...vs, { id, from: o.from, to: o.to, state: "pending", virtual: "pending" }]);
      const drop = (x: string) => mounted.current && setVirtual((vs) => vs.filter((e) => e.id !== x));
      try {
        await l.onConnect(o.from, o.to);
        announce(t("graph.connected", names));
        const old = replace ? l.edges.find((e) => e.id === replace) : undefined;
        if (old && l.onEdgeDelete) await l.onEdgeDelete(old);
        drop(id);
      } catch (err) {
        const reason = err instanceof Error && err.message ? err.message : t("graph.failedGeneric");
        const eid = `\u0000e${++seq.current}`;
        if (mounted.current) setVirtual((vs) => [...vs.filter((e) => e.id !== id), { id: eid, from: o.from, to: o.to, virtual: "error", errorText: reason }]);
        announce(t("graph.failed", { ...names, reason }));
        setTimeout(() => drop(eid), 3200);
      }
    },
    [verdictFor, portName, announce, t, reasonOf],
  );

  const removeEdge = useCallback(
    async (id: string) => {
      const l = live.current;
      const e = l.edges.find((x) => x.id === id);
      if (!e || !l.canDelete || !l.onEdgeDelete) return;
      setMenu(null);
      setSelEdge(null);
      try {
        await l.onEdgeDelete(e);
        announce(t("graph.removed", { from: portName(e.from), to: portName(e.to) }));
      } catch (err) {
        announce(t("graph.failed", { from: portName(e.from), to: portName(e.to), reason: err instanceof Error && err.message ? err.message : t("graph.failedGeneric") }));
      }
    },
    [announce, t, portName],
  );

  // keyboard wiring
  const kbRef = useRef<WiringState>(idle);
  const ensureVisible = useCallback(
    (w: Pos) => {
      const v = store.view;
      const sx = w.x * v.z + v.x;
      const sy = w.y * v.z + v.y;
      const m = 56;
      const dx = sx < m ? m - sx : sx > store.size.w - m ? store.size.w - m - sx : 0;
      const dy = sy < m ? m - sy : sy > store.size.h - m ? store.size.h - m - sy : 0;
      if (dx || dy) store.setView({ ...v, x: v.x + dx, y: v.y + dy });
    },
    [store],
  );
  const kbAct = useCallback(
    (action: WiringAction) => {
      const { state, effect } = wiringStep(kbRef.current, action);
      const prev = kbRef.current;
      kbRef.current = state;
      setKb(state);
      if (effect?.type === "empty") return announce(t("graph.wiring.none"));
      const source = action.type === "start" ? action.source : prev.phase === "picking" ? prev.source : null;
      if (effect?.type === "connect") {
        void connectPorts(effect.source, effect.target);
        store.portEl(effect.source)?.focus();
        return;
      }
      if (action.type === "cancel") {
        if (source) store.portEl(source)?.focus();
        return announce(t("graph.wiring.cancel"));
      }
      const target = currentTarget(state);
      if (state.phase === "picking" && target) {
        const w = store.portWorld(target);
        if (w) ensureVisible(w);
        store.portEl(target)?.focus();
        const text = t("graph.wiring.target", { port: portName(target), i: state.index + 1, n: state.candidates.length });
        announce(action.type === "start" ? `${t("graph.wiring.start", { port: portName(state.source) })} ${text}` : text);
      }
    },
    [announce, t, connectPorts, store, ensureVisible, portName],
  );
  const startKeyboardWire = useCallback(
    (ref: PortRef) => {
      const l = live.current;
      const p = findPort(l.nodes, ref);
      if (!l.canWire || !p || p.disabled) return;
      const es = [...l.edges, ...l.virtual.filter((v) => v.virtual === "pending")];
      const rank = new Map(tabOrder(l.nodes.flatMap((n) => { const r = store.nodeRect(n.id); return r ? [{ id: n.id, x: r.x, y: r.y, w: r.w }] : []; })).map((id, i) => [id, i]));
      const candidates = validTargets(l.nodes, es, ref, l.canConnect).sort((a, b) => {
        const d = (rank.get(a.node) ?? 0) - (rank.get(b.node) ?? 0);
        if (d) return d;
        const ya = store.portWorld(a)?.y ?? 0;
        const yb = store.portWorld(b)?.y ?? 0;
        return ya - yb;
      });
      kbAct({ type: "start", source: ref, candidates });
    },
    [kbAct, store],
  );

  const onPortKeyDown = useCallback(
    (e: ReactKeyboardEvent, ref: PortRef) => {
      if (kbRef.current.phase === "picking") return;
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        e.stopPropagation();
        startKeyboardWire(ref);
      }
    },
    [startKeyboardWire],
  );

  // ----- pointer interaction
  const ptrs = useRef(new Map<number, Pos>());
  const mode = useRef<Mode | null>(null);
  const spaceDown = useRef(false);
  const hover = useRef<{ el: Element; attr: string } | null>(null);
  const clearHover = () => {
    const h = hover.current;
    h?.el.removeAttribute(h.attr);
    hover.current = null;
  };
  const setHover = (el: Element | undefined | null, attr: string) => {
    if (el && hover.current?.el === el && hover.current?.attr === attr) return;
    clearHover();
    if (el) {
      el.setAttribute(attr, "");
      hover.current = { el, attr };
    }
  };
  const hideHint = () => {
    if (hintRef.current) hintRef.current.hidden = true;
  };
  const endWire = () => {
    clearHover();
    hideHint();
    setDrag(null);
    if (dragPathRef.current) dragPathRef.current.removeAttribute("d");
    rootRef.current?.removeAttribute("data-mode");
  };

  const select = (ids: string[]) => {
    const valid = ids.filter((id) => live.current.nodes.find((n) => n.id === id)?.selectable !== false);
    selectOnly(valid);
  };

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    // a popover or menu portaled out of a card is a React child of the canvas but not part of it: its events are its own
    if (!e.currentTarget.contains(e.target as Node)) return;
    const target = e.target as HTMLElement;
    if (target.closest("[data-gui]")) return;
    ptrs.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    cancelViewAnim();
    if (ptrs.current.size === 2) {
      // a second finger: whatever was going on becomes a pinch
      const m = mode.current;
      if (m?.kind === "wire") endWire();
      if (m?.kind === "node" && m.starts) finishNode(m);
      const [a, b] = [...ptrs.current.values()] as [Pos, Pos];
      const c = store.toLocal((a.x + b.x) / 2, (a.y + b.y) / 2);
      mode.current = { kind: "pinch", d0: Math.hypot(a.x - b.x, a.y - b.y) || 1, z0: store.view.z, v0: store.view, c0: c };
      return;
    }
    if (ptrs.current.size > 2 || mode.current) return;
    const mouse = e.pointerType === "mouse";
    const startPan = () => {
      mode.current = { kind: "pan", id: e.pointerId, sx: e.clientX, sy: e.clientY, v: store.view, moved: false, samples: [] };
      rootRef.current?.setPointerCapture(e.pointerId);
      rootRef.current?.setAttribute("data-mode", "pan");
    };
    if (e.button === 1 || (e.button === 0 && spaceDown.current)) {
      e.preventDefault();
      return startPan();
    }
    if (mouse && e.button !== 0) return;
    const portEl = target.closest<HTMLElement>("[data-gport]");
    const grabbed = !portEl && live.current.canGrab ? handleUnder(e.clientX, e.clientY, mouse ? 12 : 22) : null;
    if (grabbed) {
      const edge = allEdgesRef.current.find((x) => x.id === grabbed.edge);
      if (edge) {
        const fixed = grabbed.end === "to" ? edge.from : edge.to;
        grabRef.current = grabStep({ phase: "hover", handle: grabbed }, { type: "press" }).state;
        mode.current = { kind: "wire", id: e.pointerId, sx: e.clientX, sy: e.clientY, ref: fixed, started: false, source: fixed, snap: null, grab: { edge, end: grabbed.end }, replace: edge.id };
        return;
      }
    }
    if (portEl) {
      if (!live.current.canWire) return;
      const ref = { node: (portEl.closest("[data-gnode]") as HTMLElement).dataset.gnode!, port: portEl.dataset.portId! };
      mode.current = { kind: "wire", id: e.pointerId, sx: e.clientX, sy: e.clientY, ref, started: false, source: ref, snap: null };
      return;
    }
    if (target.closest("[data-gedge]")) return;
    setMenu(null);
    const nodeEl = target.closest<HTMLElement>("[data-gnode]");
    if (nodeEl) {
      if (target.closest(INTERACTIVE)) return;
      const id = nodeEl.dataset.gnode!;
      const n = live.current.nodes.find((x) => x.id === id);
      if (!n) return;
      const mod = e.shiftKey || e.ctrlKey || e.metaKey;
      const cur = live.current.sel;
      const was = cur.includes(id);
      let ids = cur;
      if (n.selectable !== false) {
        if (mod) ids = was ? cur : [...cur, id];
        else if (!was) ids = [id];
        select(ids);
      }
      setSelEdge(null);
      // a card that cannot be selected still moves, alone
      const dragIds = (n.selectable === false ? [id] : ids).filter((x) => live.current.nodes.find((m) => m.id === x)?.draggable !== false);
      mode.current = { kind: "node", id: e.pointerId, sx: e.clientX, sy: e.clientY, node: id, ids: dragIds, starts: null, mod, was, canDrag: n.draggable !== false && dragIds.includes(id) };
      return;
    }
    if (e.shiftKey && mouse) {
      mode.current = { kind: "marquee", id: e.pointerId, sx: e.clientX, sy: e.clientY, base: live.current.sel, moved: false };
      rootRef.current?.setPointerCapture(e.pointerId);
      return;
    }
    startPan();
  };

  const finishNode = (m: Extract<Mode, { kind: "node" }>) => {
    if (!m.starts) return;
    const next: Positions = { ...live.current.positions };
    for (const id of m.starts.keys()) {
      const p = store.live.get(id);
      if (p) next[id] = p;
    }
    commitPositions(next, true);
    refreshOrder();
    rootRef.current?.removeAttribute("data-mode");
  };

  // ----- grabbing a wire by its end
  const allEdgesRef = useRef<RenderEdge[]>([]);
  const grabRef = useRef<GrabState>(grabIdle);
  const [handle, setHandle] = useState<Handle | null>(null);
  const reconnectVerdict = (edge: RenderEdge, end: End, target: PortRef | null): ConnectCheck => {
    const l = live.current;
    const cr = l.props.canReconnect;
    if (cr) return cr(edge, end, target);
    if (!target) return true;
    const fixed = end === "to" ? edge.from : edge.to;
    const v = checkConnection(l.nodes, l.edges.filter((x) => x.id !== edge.id), end === "to" ? fixed : target, end === "to" ? target : fixed, l.canConnect);
    return v.ok ? true : reasonOf(v);
  };
  /** The handle of a wire's end within `radius` screen px of the pointer. */
  const handleUnder = (clientX: number, clientY: number, radius: number): Handle | null => {
    const handles: Handle[] = [];
    for (const x of allEdgesRef.current) {
      if (x.virtual || x.grabbable === false) continue;
      const route = routeFor(store, x);
      if (!route) continue;
      handles.push({ ...handlePoint(route, "from"), edge: x.id, end: "from" }, { ...handlePoint(route, "to"), edge: x.id, end: "to" });
    }
    const w = store.toWorld(clientX, clientY);
    return handleAt(w, handles, radius / store.view.z);
  };
  const drawDrag = (source: PortRef, end: Pos) => {
    const sp = findPort(live.current.nodes, source);
    const a = store.portWorld(source);
    if (a && sp && dragPathRef.current) dragPathRef.current.setAttribute("d", sp.side === "out" ? dragWire(a, end) : dragWire(end, a));
  };
  const showReason = (reason: string, at?: Pos) => {
    announce(reason);
    const el = hintRef.current;
    if (!el) return;
    el.textContent = reason;
    el.hidden = false;
    if (at) {
      el.style.left = `${at.x + 14}px`;
      el.style.top = `${at.y + 16}px`;
    }
    window.setTimeout(() => {
      el.hidden = true;
    }, 2200);
  };
  /** The dragged end flies back to its port, then the wire shows again where it was. */
  const springBack = (m: Extract<Mode, { kind: "wire" }>) => {
    const orig = m.grab ? (m.grab.end === "to" ? m.grab.edge.to : m.grab.edge.from) : null;
    const target = orig ? store.portWorld(orig) : null;
    const from = m.end;
    if (!target || !from || reducedMotion() || typeof requestAnimationFrame !== "function") return endWire();
    const t0 = nowMs();
    const step = (now: number) => {
      const k = ease(Math.min(1, (now - t0) / 200));
      drawDrag(m.source, { x: from.x + (target.x - from.x) * k, y: from.y + (target.y - from.y) * k });
      if (k < 1) requestAnimationFrame(step);
      else endWire();
    };
    requestAnimationFrame(step);
  };
  const grabRelease = async (m: Extract<Mode, { kind: "wire" }>, cancelled: boolean) => {
    const { edge, end } = m.grab!;
    const orig = end === "to" ? edge.to : edge.from;
    const { effect } = grabStep({ phase: "drag", edge: edge.id, end, snap: m.snap, refused: m.bad ?? null }, { type: cancelled ? "cancel" : "release" });
    grabRef.current = grabIdle;
    hideHint();
    clearHover();
    const at = m.end ? { x: m.end.x * store.view.z + store.view.x, y: m.end.y * store.view.z + store.view.y } : undefined;
    if (effect?.type !== "drop") return springBack(m);
    if (effect.refused) {
      showReason(effect.refused, at);
      return springBack(m);
    }
    const verdict = effect.target ? true : reconnectVerdict(edge, end, null);
    const d = decideDrop(orig, effect.target, verdict);
    if (d.kind === "noop") return springBack(m);
    if (d.kind === "refuse") {
      showReason(d.reason, at);
      return springBack(m);
    }
    try {
      await live.current.props.onReconnect?.(edge, end, d.target);
      endWire();
    } catch (err) {
      showReason(err instanceof Error && err.message ? err.message : t("graph.failedGeneric"), at);
      springBack(m);
    }
  };

  const wireMove = (m: Extract<Mode, { kind: "wire" }>, e: ReactPointerEvent) => {
    const l = live.current;
    if (!m.started) {
      if (Math.hypot(e.clientX - m.sx, e.clientY - m.sy) < DRAG_THRESHOLD) return;
      const port = findPort(l.nodes, m.ref);
      if (!port || port.disabled) {
        mode.current = null;
        return;
      }
      const source = m.ref;
      const replace = m.grab?.edge.id;
      const es = [...l.edges, ...l.virtual.filter((v) => v.virtual === "pending")].filter((x) => x.id !== replace);
      const targets = new Set((m.grab ? grabTargets(l.nodes, allEdgesRef.current, m.grab.edge, m.grab.end, (x, en, target) => reconnectVerdict(x as RenderEdge, en, target)) : validTargets(l.nodes, es, source, l.canConnect)).map(portKey));
      m.started = true;
      m.source = source;
      m.replace = replace;
      rootRef.current?.setPointerCapture(m.id);
      rootRef.current?.setAttribute("data-mode", "wire");
      setDrag({ source, targets, replace });
    }
    const w = store.toWorld(e.clientX, e.clientY);
    const sp = findPort(l.nodes, m.source);
    if (!sp) return;
    const cands: (Pos & { ref: PortRef })[] = [];
    for (const n of l.nodes)
      for (const p of n.ports) {
        if (p.side === sp.side) continue;
        const ref = { node: n.id, port: p.id };
        const pos = store.portWorld(ref);
        if (pos) cands.push({ ...pos, ref });
      }
    const near = nearestPoint(w, cands, SNAP_RADIUS / store.view.z);
    let end = w;
    m.snap = null;
    let hint = "";
    m.bad = undefined;
    if (near) {
      const o = orient(l.nodes, m.source, near.ref);
      const gv = m.grab ? reconnectVerdict(m.grab.edge, m.grab.end, near.ref) : true;
      const v = m.grab ? (gv === true ? ({ ok: true } as Verdict) : ({ ok: false, code: "custom", message: gv } as Verdict)) : o ? verdictFor(o.from, o.to, m.replace) : ({ ok: false, code: "side" } as Verdict);
      if (v.ok) {
        m.snap = near.ref;
        end = near;
        setHover(store.portEl(near.ref), "data-snap");
      } else {
        setHover(store.portEl(near.ref), "data-bad");
        hint = reasonOf(v);
        m.bad = hint;
      }
    } else clearHover();
    const hintEl = hintRef.current;
    if (hintEl) {
      hintEl.hidden = !hint;
      if (hint) {
        hintEl.textContent = hint;
        const lp = store.toLocal(e.clientX, e.clientY);
        hintEl.style.left = `${lp.x + 14}px`;
        hintEl.style.top = `${lp.y + 16}px`;
      }
    }
    m.end = end;
    if (m.grab) grabRef.current = grabStep(grabRef.current, { type: "move", snap: m.snap, refused: m.bad ?? null }).state;
    drawDrag(m.source, end);
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    // a popover or menu portaled out of a card is a React child of the canvas but not part of it: its events are its own
    if (!e.currentTarget.contains(e.target as Node)) return;
    if (ptrs.current.has(e.pointerId)) ptrs.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const m = mode.current;
    if (!m && props.focusPaths && e.pointerType === "mouse") restOn((e.target as Element).closest("[data-gnode]")?.getAttribute("data-gnode") ?? null);
    if (!m) {
      if (live.current.canGrab && e.pointerType === "mouse") {
        const h = handleUnder(e.clientX, e.clientY, 12);
        grabRef.current = grabStep(grabRef.current, { type: "hover", handle: h }).state;
        setHandle((cur) => (cur?.edge === h?.edge && cur?.end === h?.end ? cur : h));
        rootRef.current?.toggleAttribute("data-handle", !!h);
      }
      return;
    }
    if (m.kind === "pinch") {
      if (ptrs.current.size < 2) return;
      const [a, b] = [...ptrs.current.values()] as [Pos, Pos];
      const d = Math.hypot(a.x - b.x, a.y - b.y) || 1;
      const c = store.toLocal((a.x + b.x) / 2, (a.y + b.y) / 2);
      const nz = clamp(m.z0 * (d / m.d0), MIN_ZOOM, MAX_ZOOM);
      const wx = (m.c0.x - m.v0.x) / m.v0.z;
      const wy = (m.c0.y - m.v0.y) / m.v0.z;
      store.setView({ x: c.x - wx * nz, y: c.y - wy * nz, z: nz });
      return;
    }
    if (e.pointerId !== m.id) return;
    if (m.kind === "pan") {
      const dx = e.clientX - m.sx;
      const dy = e.clientY - m.sy;
      if (!m.moved && Math.hypot(dx, dy) < 2) return;
      m.moved = true;
      m.samples.push({ x: e.clientX, y: e.clientY, t: nowMs() });
      if (m.samples.length > 12) m.samples.shift();
      store.setView({ ...m.v, x: m.v.x + dx, y: m.v.y + dy });
    } else if (m.kind === "node") {
      if (!m.starts) {
        if (!m.canDrag || Math.hypot(e.clientX - m.sx, e.clientY - m.sy) < DRAG_THRESHOLD) return;
        m.starts = new Map(m.ids.map((id) => [id, store.live.get(id) ?? { x: 0, y: 0 }]));
        rootRef.current?.setPointerCapture(m.id);
        rootRef.current?.setAttribute("data-mode", "drag");
      }
      const z = store.view.z;
      const p0 = m.starts.get(m.node)!;
      let dx = (e.clientX - m.sx) / z;
      let dy = (e.clientY - m.sy) / z;
      if (!e.altKey) {
        dx = snapTo(p0.x + dx, GRID) - p0.x;
        dy = snapTo(p0.y + dy, GRID) - p0.y;
      }
      for (const [id, s] of m.starts) store.setLive(id, { x: s.x + dx, y: s.y + dy });
      store.commitLive();
    } else if (m.kind === "marquee") {
      const dx = e.clientX - m.sx;
      const dy = e.clientY - m.sy;
      if (!m.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD) return;
      m.moved = true;
      const a = store.toLocal(m.sx, m.sy);
      const b = store.toLocal(e.clientX, e.clientY);
      const box = { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.abs(a.x - b.x), h: Math.abs(a.y - b.y) };
      const el = marqueeRef.current;
      if (el) {
        el.hidden = false;
        Object.assign(el.style, { left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px` });
      }
      const v = store.view;
      const world: Rect = { x: (box.x - v.x) / v.z, y: (box.y - v.y) / v.z, w: box.w / v.z, h: box.h / v.z };
      const hit = live.current.nodes.filter((n) => {
        const r = store.nodeRect(n.id, n.width ?? 220);
        return r && rectsIntersect(world, r);
      });
      select([...new Set([...m.base, ...hit.map((n) => n.id)])]);
    } else if (m.kind === "wire") wireMove(m, e);
  };

  const onPointerUp = (e: ReactPointerEvent<HTMLDivElement>, cancelled = false) => {
    // a popover or menu portaled out of a card is a React child of the canvas but not part of it: its events are its own
    if (!e.currentTarget.contains(e.target as Node)) return;
    ptrs.current.delete(e.pointerId);
    const m = mode.current;
    if (!m) return;
    if (m.kind === "pinch") {
      if (ptrs.current.size < 2) mode.current = null;
      return;
    }
    if (e.pointerId !== m.id) return;
    mode.current = null;
    if (rootRef.current?.hasPointerCapture(e.pointerId)) rootRef.current.releasePointerCapture(e.pointerId);
    rootRef.current?.removeAttribute("data-mode");
    if (m.kind === "pan") {
      if (!m.moved && !cancelled) {
        selectOnly([]);
        setSelEdge(null);
        setMenu(null);
      } else if (m.moved && !cancelled && e.pointerType !== "mouse") glide(releaseVelocity(m.samples, nowMs()));
      else if (m.moved && !cancelled) glide(releaseVelocity(m.samples, nowMs()));
    } else if (m.kind === "node") {
      if (m.starts) finishNode(m);
      else if (!cancelled) {
        if (m.mod && m.was) selectOnly(live.current.sel.filter((x) => x !== m.node));
        else if (!m.mod && live.current.nodes.find((n) => n.id === m.node)?.selectable !== false) selectOnly([m.node]);
      }
    } else if (m.kind === "marquee") {
      if (marqueeRef.current) marqueeRef.current.hidden = true;
      if (!m.moved && !cancelled && !m.base.length) selectOnly([]);
      if (!m.moved) setSelEdge(null);
    } else if (m.kind === "wire") {
      if (m.grab) {
        if (m.started) void grabRelease(m, cancelled);
        else grabRef.current = grabIdle;
        return;
      }
      if (m.started) {
        const snap = m.snap;
        const replace = m.replace;
        const original = replace ? live.current.edges.find((x) => x.id === replace)?.to : undefined;
        endWire();
        if (snap && !cancelled && !(original && original.node === snap.node && original.port === snap.port)) void connectPorts(m.source, snap, replace);
      }
    }
  };

  // ----- keyboard
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    // a popover or menu portaled out of a card is a React child of the canvas but not part of it: its events are its own
    if (!e.currentTarget.contains(e.target as Node)) return;
    const target = e.target as HTMLElement;
    const l = live.current;
    if (target.closest("[data-gui]") && e.key !== "Escape") return;
    if (kbRef.current.phase === "picking") {
      const map: Record<string, WiringAction | undefined> = {
        Tab: { type: e.shiftKey ? "prev" : "next" },
        ArrowRight: { type: "next" },
        ArrowDown: { type: "next" },
        ArrowLeft: { type: "prev" },
        ArrowUp: { type: "prev" },
        Enter: { type: "confirm" },
        " ": { type: "confirm" },
        Escape: { type: "cancel" },
      };
      const a = map[e.key];
      if (a) {
        e.preventDefault();
        e.stopPropagation();
        kbAct(a);
      }
      return;
    }
    if (target.closest(TYPING)) return;
    const k = e.key;
    if (drag && k === "Escape") {
      mode.current = null;
      return endWire();
    }
    if (k === " " && (target === rootRef.current || target.hasAttribute("data-gnode"))) {
      e.preventDefault();
      spaceDown.current = true;
      rootRef.current?.setAttribute("data-space", "");
      return;
    }
    if (e.ctrlKey || e.metaKey) {
      if (k === "a" || k === "A") {
        e.preventDefault();
        select(l.nodes.map((n) => n.id));
      }
      return;
    }
    if (e.altKey) return;
    if (k === "+" || k === "=") return zoomBy(1.25);
    if (k === "-" || k === "_") return zoomBy(0.8);
    if (k === "0") return resetZoom();
    if (k === "?") return setHelp(help === "shortcuts" ? "" : "shortcuts");
    if (k === "F" && e.shiftKey) return void l.props.onExpand?.();
    if (k === "f" || k === "F") return fit();
    if (k === "Escape") {
      if (help) setHelp("");
      else if (l.menu) setMenu(null);
      else if (l.sel.length || l.selEdge) {
        selectOnly([]);
        setSelEdge(null);
      }
      return;
    }
    if (k === "Delete" || k === "Backspace") {
      if (l.selEdge && l.canDelete) {
        e.preventDefault();
        // Delete is dropping the wire's `to` end on empty canvas: the same mapping
        const edge = allEdgesRef.current.find((x) => x.id === l.selEdge);
        if (l.props.onReconnect && edge) {
          if (edge.grabbable === false) return;
          const v = reconnectVerdict(edge, "to", null);
          if (v !== true) return announce(v);
          void Promise.resolve(l.props.onReconnect(edge, "to", null)).catch((err) => announce(err instanceof Error && err.message ? err.message : t("graph.failedGeneric")));
        } else void removeEdge(l.selEdge);
      }
      return;
    }
    if (k === "Enter") {
      if (l.selEdge && l.props.renderEdgeMenu && (target.hasAttribute("data-gedge") || target === rootRef.current)) {
        e.preventDefault();
        setMenu({ id: l.selEdge, focus: true });
      }
      return;
    }
    if (k.startsWith("Arrow") && l.sel.length && (target === rootRef.current || target.hasAttribute("data-gnode"))) {
      e.preventDefault();
      const step = e.shiftKey ? 1 : GRID;
      const dx = k === "ArrowLeft" ? -step : k === "ArrowRight" ? step : 0;
      const dy = k === "ArrowUp" ? -step : k === "ArrowDown" ? step : 0;
      const next: Positions = { ...l.positions };
      for (const id of l.sel) {
        const n = l.nodes.find((x) => x.id === id);
        const p = store.live.get(id) ?? next[id];
        if (!p || n?.draggable === false) continue;
        next[id] = { x: p.x + dx, y: p.y + dy };
      }
      commitPositions(next, true);
    }
  };
  const onKeyUp = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    // a popover or menu portaled out of a card is a React child of the canvas but not part of it: its events are its own
    if (!e.currentTarget.contains(e.target as Node)) return;
    if (e.key === " ") {
      spaceDown.current = false;
      rootRef.current?.removeAttribute("data-space");
    }
  };

  // ----- edges
  const onEdgeClick = useCallback(
    (id: string) => {
      setSelEdge(id);
      selectOnly([]);
      if (live.current.props.renderEdgeMenu) setMenu({ id, focus: false });
    },
    [selectOnly],
  );
  const onEdgeFocus = useCallback((id: string) => {
    setSelEdge(id);
    setMenu((m) => (m && m.id !== id ? null : m));
  }, []);
  const onFocusNode = useCallback(
    (id: string) => {
      setSelEdge(null);
      if (live.current.nodes.find((n) => n.id === id)?.selectable === false) return;
      if (!live.current.sel.includes(id)) selectOnly([id]);
    },
    [selectOnly],
  );

  const highlight = useMemo<PortHighlight | null>(() => {
    if (kb.phase === "picking") {
      const cur = currentTarget(kb);
      return { source: portKey(kb.source), targets: new Set(kb.candidates.map(portKey)), current: cur ? portKey(cur) : null };
    }
    if (drag) return { source: portKey(drag.source), targets: drag.targets, current: null };
    return null;
  }, [kb, drag]);

  const ctx = useMemo<GraphCtx>(
    () => ({ store, typeColors, connected, highlight, readOnly, onPortKeyDown, onFocusNode }),
    [store, typeColors, connected, highlight, readOnly, onPortKeyDown, onFocusNode],
  );

  const sorted = useMemo(() => {
    const idx = new Map(order.map((id, i) => [id, i]));
    return [...nodes].sort((a, b) => (idx.get(a.id) ?? 1e9) - (idx.get(b.id) ?? 1e9));
  }, [nodes, order]);

  // focusPaths: what is kept bright. A selection wins over the card the mouse rests on; a selected wire keeps itself.
  const focusSel = useMemo(() => sel.filter((id) => nodes.some((n) => n.id === id)), [sel, nodes]);
  const focusWire = selEdge ? edges.find((e) => e.id === selEdge) : undefined;
  const focusFrom: "sel" | "wire" | "hover" | null = !props.focusPaths ? null : focusSel.length ? "sel" : focusWire ? "wire" : hoverCard && !drag ? "hover" : null;
  const focus = useMemo(
    () => (focusFrom === "sel" ? pathsThrough(focusSel, edges) : focusFrom === "wire" ? wireFocus(focusWire!) : focusFrom === "hover" ? pathsThrough([hoverCard!], edges) : null),
    [focusFrom, focusSel, focusWire, hoverCard, edges],
  );
  const nameOf = (id: string) => {
    const n = nodes.find((x) => x.id === id);
    return n ? nodeLabel(n) : id;
  };

  const help = props.help !== undefined ? props.help : helpOwn;
  const setHelp = (tab: HelpTab | "") => {
    if (props.help === undefined) setHelpOwn(tab);
    else writePref("mg.graph.help", tab || null);
    if (props.help === undefined) writePref("mg.graph.help", tab || null);
    props.onHelpChange?.(tab);
  };
  const onDoubleClick = (e: React.MouseEvent<HTMLDivElement>) => {
    // a popover or menu portaled out of a card is a React child of the canvas but not part of it: its events are its own
    if (!e.currentTarget.contains(e.target as Node)) return;
    const nodeEl = (e.target as HTMLElement).closest<HTMLElement>("[data-gnode]");
    if (nodeEl && !(e.target as HTMLElement).closest(INTERACTIVE + ",[data-gui]")) {
      live.current.props.onNodeActivate?.(nodeEl.dataset.gnode!);
      return;
    }
    if ((e.target as HTMLElement).closest("[data-gui],[data-gnode],[data-gedge],.mg-glabel")) return;
    const p = store.toLocal(e.clientX, e.clientY);
    animateView(zoomAround(store.view, p, store.view.z * (e.shiftKey ? 1 / 1.5 : 1.5)), 220);
  };
  useLayoutEffect(() => {
    allEdgesRef.current = allEdges;
  });
  const menuEdge = menu ? edges.find((x) => x.id === menu.id) : undefined;
  const dragColor = drag ? wireInk(typeColors[findPort(nodes, drag.source)?.type ?? ""] ?? "var(--muted)") : undefined;

  return (
    <GraphContext.Provider value={ctx}>
      <div
        ref={rootRef}
        dir="ltr"
        role="application"
        aria-label={ariaLabel}
        aria-describedby={instrId}
        tabIndex={0}
        className={cx("mg-graph relative overflow-hidden rounded-card border border-line outline-none select-none focus-visible:border-accent-line", className)}
        style={height === "fit" ? { minHeight: 560 } : { height }}
        onDoubleClick={onDoubleClick}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={(e) => onPointerUp(e)}
        onPointerCancel={(e) => onPointerUp(e, true)}
        onKeyDown={onKeyDown}
        onKeyUp={onKeyUp}
        onBlur={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
            spaceDown.current = false;
            e.currentTarget.removeAttribute("data-space");
          }
        }}
      >
        <p id={instrId} className="sr-only">
          {t("graph.instructions")}
        </p>
        <div role="status" aria-live="polite" className="sr-only">
          {announcement}
        </div>
        <div ref={worldRef} className="mg-gworld">
          {sorted.map((n) => (
            <NodeCard key={n.id} node={n} pos={positions[n.id]} selected={selSet.has(n.id)} dim={!!focus && !focus.nodes.has(n.id)} />
          ))}
          <EdgeLayer
            store={store}
            nodes={nodes}
            edges={allEdges}
            typeColors={typeColors}
            selectedEdge={selEdge}
            focusEdges={focus?.edges ?? null}
            hiddenEdge={drag?.replace ?? null}
            onEdgeClick={onEdgeClick}
            onEdgeFocus={onEdgeFocus}
          />
          <svg className="mg-gsvg mg-gtop" aria-hidden>
            {drag && <path ref={dragPathRef} className="mg-gwire mg-gwire-drag" style={{ stroke: dragColor }} />}
            {handle && !drag && <circle className="mg-ghandle" cx={handle.x} cy={handle.y} r={5.5 / Math.max(0.6, store.view.z)} />}
            {kb.phase === "picking" && <KeyboardPreview store={store} kb={kb} nodes={nodes} color={wireInk(typeColors[findPort(nodes, kb.source)?.type ?? ""] ?? "var(--muted)")} />}
          </svg>
        </div>
        <div ref={marqueeRef} hidden className="mg-gmarquee" />
        {focus && focusFrom !== "hover" && (
          <div className="mg-gfocus" data-gui>
            <span className="truncate">
              {focusFrom === "wire"
                ? t("graph.focus.wire", { from: nameOf(focusWire!.from.node), to: nameOf(focusWire!.to.node) })
                : focusSel.length === 1
                  ? t("graph.focus.one", { name: nameOf(focusSel[0]!) })
                  : t("graph.focus.many", { n: focusSel.length })}
            </span>
            <button
              type="button"
              className="mg-gfocus-all"
              onClick={() => {
                selectOnly([]);
                setSelEdge(null);
              }}
            >
              {t("graph.focus.all")}
            </button>
            <kbd className="mg-gfocus-key">Esc</kbd>
          </div>
        )}
        <div ref={hintRef} hidden className="mg-ghint" aria-hidden />
        {menu && menuEdge && renderEdgeMenu && (
          <EdgeMenu store={store} edge={menuEdge} focus={menu.focus} onClose={() => setMenu(null)}>
            {renderEdgeMenu(menuEdge, () => setMenu(null))}
          </EdgeMenu>
        )}
        <div className="mg-gtoolbar" data-gui role="toolbar" aria-label={t("graph.toolbar")}>
          <Tip label={t("graph.zoomOut")}>
            <IconButton variant="flat" aria-label={t("graph.zoomOut")} onClick={() => zoomBy(0.8)}>
              <Icon name="minus" size={15} />
            </IconButton>
          </Tip>
          <ZoomLabel store={store} onReset={resetZoom} />
          <Tip label={t("graph.zoomIn")}>
            <IconButton variant="flat" aria-label={t("graph.zoomIn")} onClick={() => zoomBy(1.25)}>
              <Icon name="plus" size={15} />
            </IconButton>
          </Tip>
          <span className="mx-0.5 h-4 w-px bg-line" aria-hidden />
          <Tip label={t("graph.fit")}>
            <IconButton variant="flat" aria-label={t("graph.fit")} onClick={() => fit()}>
              <Icon name="fit" size={15} />
            </IconButton>
          </Tip>
          <Tip label={t("graph.arrange")}>
            <IconButton variant="flat" aria-label={t("graph.arrange")} onClick={arrange}>
              <Icon name="arrange" size={15} />
            </IconButton>
          </Tip>
          {props.onExpand && (
            <Tip label={`${props.expanded ? t("graph.collapse") : t("graph.expand")} (Shift+F)`}>
              <IconButton variant="flat" aria-label={props.expanded ? t("graph.collapse") : t("graph.expand")} data-graph-expand onClick={props.onExpand}>
                <Icon name={props.expanded ? "x" : "expand"} size={15} />
              </IconButton>
            </Tip>
          )}
        </div>
        <GraphHelp open={help} onOpen={setHelp} legend={legend} expand={!!props.onExpand} extraShortcuts={props.extraShortcuts} />
        <div className="mg-gzoomhint" data-show={zoomHint || undefined} aria-hidden>
          {t("graph.zoomHint", { key: modKey })}
        </div>
        {minimap && !compact && <Minimap store={store} ids={nodes.map((n) => n.id)} selected={selSet} onCenter={centerOn} />}
      </div>
    </GraphContext.Provider>
  );
}

function ZoomLabel({ store, onReset }: { store: GraphStore; onReset: () => void }) {
  useStoreVersion(store, "view");
  const t = useT();
  const pct = Math.round(store.view.z * 100);
  return (
    <button
      type="button"
      onClick={onReset}
      aria-label={t("graph.zoomLevel", { pct })}
      className="h-7 min-w-11 rounded-lg px-1.5 text-center font-mono text-[11px] font-medium tabular-nums text-muted transition-colors hover:bg-surface-2 hover:text-fg"
    >
      {pct}%
    </button>
  );
}

/** The wire the keyboard is about to make: from the source to the current target. */
function KeyboardPreview({ store, kb, nodes, color }: { store: GraphStore; kb: Extract<WiringState, { phase: "picking" }>; nodes: GraphProps["nodes"]; color: string }) {
  useStoreVersion(store, "geo");
  const target = currentTarget(kb);
  const a = store.portWorld(kb.source);
  const b = target ? store.portWorld(target) : null;
  if (!a || !b) return null;
  const out = findPort(nodes, kb.source)?.side === "out";
  return <path className="mg-gwire mg-gwire-drag" d={out ? dragWire(a, b) : dragWire(b, a)} style={{ stroke: color }} />;
}

function EdgeMenu({ store, edge, children, focus, onClose }: { store: GraphStore; edge: GraphEdge; children: ReactNode; focus: boolean; onClose: () => void }) {
  useStoreVersion(store, "view");
  useStoreVersion(store, "geo");
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (focus) ref.current?.querySelector<HTMLElement>("button,[tabindex]")?.focus();
  }, [focus]);
  useEffect(() => {
    const off = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node) && !(e.target as HTMLElement).closest?.("[data-gedge],.mg-glabel")) onClose();
    };
    document.addEventListener("pointerdown", off, true);
    return () => document.removeEventListener("pointerdown", off, true);
  }, [onClose]);
  const mid = routeFor(store, edge)?.mid;
  const v = store.view;
  const style: CSSProperties = mid ? { left: v.x + mid.x * v.z, top: v.y + mid.y * v.z } : { left: 0, top: 0, visibility: "hidden" };
  return (
    <div ref={ref} data-gui className="mg-gmenu" role="group" style={style}>
      {children}
    </div>
  );
}
