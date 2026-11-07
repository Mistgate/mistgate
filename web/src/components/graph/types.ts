import type { ReactNode } from "react";
import type { LayoutEdge, LayoutNode } from "./layout";

/** A port's type: a free-form tag ("udp", "tcp", "any"). Two ports connect when the tags match or either is "any". */
export type PortType = string;
export type PortSide = "in" | "out";
export type GraphTone = "mint" | "sky" | "lavender" | "sand" | "rose" | "sage" | "muted";

export interface GraphPort {
  id: string;
  side: PortSide;
  type: PortType;
  label?: ReactNode;
  /** In: false by default (one wire). Out: true by default (many wires). */
  multi?: boolean;
  disabled?: boolean;
  /** What the way into or out of this port is like: a filled dot works (ok, warn), hollow is none, a ring with a slash is blocked. Without it a port is filled when a wire is connected. */
  status?: "ok" | "warn" | "none" | "blocked";
}

export interface GraphNode<D = unknown> {
  id: string;
  title: ReactNode;
  /** The accessible name and the name in announcements; defaults to the text of the title. */
  label?: string;
  icon?: ReactNode;
  tone?: GraphTone;
  /** The column of the auto-layout; without it the longest path from the edges decides. */
  rank?: number;
  ports: GraphPort[];
  /** Rows and controls under the header; anchor a port to a row with <GraphPortRow port="id" />. */
  body?: ReactNode;
  selectable?: boolean;
  draggable?: boolean;
  /** Card width in px (default 220). */
  width?: number;
  data?: D;
}

/** "blocked": a way something cuts off (a country), drawn thin, muted and dashed, never as an alarm. */
export type EdgeState = "ok" | "warn" | "bad" | "off" | "pending" | "blocked";

export interface PortRef {
  node: string;
  port: string;
}

export interface GraphEdge {
  id: string;
  from: PortRef;
  to: PortRef;
  state?: EdgeState;
  label?: ReactNode;
  /** Where the label sits: on the middle of the wire (default) or just before the target port, like a badge on it. */
  labelAt?: "mid" | "end";
  /** A slow flowing dash in the direction of traffic (not with reduced motion). */
  animated?: boolean;
}

export interface Pos {
  x: number;
  y: number;
}
export type Positions = Record<string, Pos>;

/** true, or the reason the pair cannot connect (shown at the cursor while hovering the target). `from` is always the out port. */
export type ConnectCheck = true | string;

export interface GraphProps<D = unknown> {
  nodes: GraphNode<D>[];
  edges: GraphEdge[];
  /** CSS colours by port type; a missing type is drawn muted. */
  typeColors: Record<PortType, string>;
  /** Positions are kept in localStorage under this key (uncontrolled use). */
  layoutKey?: string;
  positions?: Positions;
  onPositionsChange?: (positions: Positions) => void;
  canConnect?: (from: PortRef, to: PortRef) => ConnectCheck;
  onConnect?: (from: PortRef, to: PortRef) => void | Promise<void>;
  /** Without it wires cannot be deleted (or re-plugged). */
  onEdgeDelete?: (edge: GraphEdge) => void | Promise<void>;
  /** A small popover at the wire's midpoint, on click or Enter. */
  renderEdgeMenu?: (edge: GraphEdge, close: () => void) => ReactNode;
  selected?: string[];
  onSelectionChange?: (ids: string[]) => void;
  readOnly?: boolean;
  minimap?: boolean;
  /** Explains the line styles and chips; shown in a small collapsible panel at the bottom-left. */
  legend?: ReactNode;
  /** Replaces the default column layout (first layout and Arrange). */
  arrange?: (nodes: LayoutNode[], edges: LayoutEdge[]) => Positions;
  height?: number | string;
  ariaLabel: string;
  className?: string;
}

export interface View {
  x: number;
  y: number;
  z: number;
}

export const portKey = (r: PortRef) => `${r.node}\u0001${r.port}`;
export const keyRef = (k: string): PortRef => {
  const [node = "", port = ""] = k.split("\u0001");
  return { node, port };
};
