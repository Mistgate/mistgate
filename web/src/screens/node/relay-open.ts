import { useNavigate } from "@tanstack/react-router";

/**
 * Opens "Make reachable from…" for an exit: the wizard lives on the exit's own page (the board opens it from the node's page, where the node is chosen
 * already), so a button anywhere else (the entry's page, the fleet's list, the map, the graph) takes the owner there. `entry` is the node the owner
 * picked (the wizard opens at its servers), none: at the choice of the entry. `only` is one server the owner dragged onto the entry.
 */
export function useOpenWizard() {
  const navigate = useNavigate();
  return (exitId: string, entryId?: string, only?: string) =>
    void navigate({ to: "/nodes/$id", params: { id: exitId }, search: { tab: "ways", add: entryId ? `via:${entryId}${only ? `/${only}` : ""}` : "1" } });
}
