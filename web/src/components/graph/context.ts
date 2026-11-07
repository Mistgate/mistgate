import { Children, createContext, isValidElement, useContext, type KeyboardEvent, type ReactNode } from "react";
import type { GraphStore } from "./store";
import type { GraphNode, PortRef } from "./types";

export interface PortHighlight {
  /** The port the wire started from. */
  source: string;
  /** The ports that can take it. */
  targets: ReadonlySet<string>;
  /** The keyboard's current target. */
  current: string | null;
}

export interface GraphCtx {
  store: GraphStore;
  typeColors: Record<string, string>;
  connected: ReadonlySet<string>;
  highlight: PortHighlight | null;
  readOnly: boolean;
  onPortKeyDown: (e: KeyboardEvent, ref: PortRef) => void;
  onFocusNode: (id: string) => void;
}

export const GraphContext = createContext<GraphCtx | null>(null);
export const useGraph = () => {
  const g = useContext(GraphContext);
  if (!g) throw new Error("graph parts must be rendered inside <Graph>");
  return g;
};

export interface NodeCtx {
  node: GraphNode;
  claim: (portId: string) => void;
  release: (portId: string) => void;
}
export const NodeContext = createContext<NodeCtx | null>(null);

export const nodeLabel = (n: GraphNode) => n.label ?? (textOf(n.title) || n.id);

/** The plain text of a ReactNode (for aria labels and announcements). */
export function textOf(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join(" ").replace(/\s+/g, " ").trim();
  if (isValidElement<{ children?: ReactNode }>(node)) return Children.toArray(node.props.children).map(textOf).join(" ").replace(/\s+/g, " ").trim();
  return "";
}
