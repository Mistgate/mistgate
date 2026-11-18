import { useQuery } from "@tanstack/react-query";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { Flag } from "@/components/ui/flag";
import { meQuery } from "@/lib/session";
import type { Tx } from "@/screens/users/t";
import { chipCountry, chipLabel } from "./model";
import { Svg } from "./parts";

/** The pieces of the DNS tab that both the list and the editor draw (their rules are in subs-dns.css, under .sbp). */

/** The icon paths of the tab that the board does not name in parts.tsx. */
export const dnsPath = {
  back: "M15 6l-6 6 6 6",
  split: "M4 6h5l6 12h5M4 18h5l2-4M20 6h-5l-1 2",
  trash: "M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12M10 11v5M14 11v5",
} as const;

/**
 * What the signed-in admin may do here (internal/panel/auth/policy.go): presets are made, changed and deleted by the owner only,
 * the default for everyone is a setting that the helper changes too, a read-only admin does neither.
 */
export function useDnsRole(): { owner: boolean; write: boolean } {
  const role = useQuery(meQuery).data?.admin?.role;
  return { owner: role === Role.OWNER, write: role === Role.OWNER || role === Role.HELPER };
}

/** The text of a category chip, a regional one with its country's flag in front. */
export function ChipText({ chip, t }: { chip: string; t: Tx }) {
  const country = chipCountry(chip);
  return (
    <>
      {country && <Flag code={country} size={11} />}
      {chipLabel(chip, t)}
    </>
  );
}

/** The back button of the editor: the arrow and "Presets". */
export function Back({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" className="nd-back sb-back" onClick={onClick}>
      <Svg d={dnsPath.back} />
      {label}
    </button>
  );
}
