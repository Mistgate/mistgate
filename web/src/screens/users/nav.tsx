import { Link, useNavigate } from "@tanstack/react-router";
import type { MouseEvent, ReactNode } from "react";

// web-ops owns the router and registers /users/$id, /profiles/new and /profiles/$id. Navigation here names
// the route by its path, so these screens type-check (and run) whichever way the route tree is declared.
type Params = Record<string, string>;

export function useGo() {
  const navigate = useNavigate();
  return (to: string, opts?: { params?: Params; replace?: boolean }) => navigate({ to, ...opts } as never);
}

type LinkProps = { to: string; params?: Params; className?: string; children: ReactNode; "aria-label"?: string; onClick?: (e: MouseEvent) => void };
export const AppLink = Link as unknown as (props: LinkProps) => ReactNode;
