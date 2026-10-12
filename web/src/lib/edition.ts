import { useQuery } from "@tanstack/react-query";
import type { EditionFeature } from "@/i18n/edition";
import { meQuery } from "./session";

/**
 * Which edition runs this panel and what it cannot do yet, read from the session the app already holds (MeResponse): the VPS
 * edition has everything, the Cloudflare ("edge") one lists the feature ids it lacks. Before the session is known (or against
 * an older panel that does not say) nothing is unavailable, so a page never hides a button on a guess.
 */
export function useEdition() {
  const me = useQuery(meQuery).data;
  const lacking = me?.unavailable ?? [];
  return { edge: me?.edition === "edge", off: (feature: EditionFeature) => lacking.includes(feature) };
}
