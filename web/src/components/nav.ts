import type { MessageKey } from "@/i18n/en";
import type { NavIconName } from "@/components/ui/icons";

// The 11 sections of the admin, in order. The first three sit in the mobile dock next to "More", all of them are in the
// phone's menu. `group` puts a labelled separator above the item in the sidebar (Setup, Operations, System).
export const sections = [
  { id: "overview", to: "/" },
  { id: "nodes", to: "/nodes" },
  { id: "users", to: "/users" },
  { id: "profiles", to: "/profiles", group: "nav.group.setup" },
  { id: "subscriptions", to: "/subscriptions" },
  // the Decoy sites section (design board PnDecoys) goes here, last in Setup, when its screen exists
  { id: "health", to: "/health", group: "nav.group.ops" },
  { id: "reachability", to: "/reachability" },
  { id: "updates", to: "/updates" },
  { id: "ai", to: "/ai", group: "nav.group.system" },
  { id: "integrations", to: "/integrations" },
  { id: "settings", to: "/settings" },
] as const satisfies readonly { id: NavIconName; to: string; group?: MessageKey }[];

export type Section = (typeof sections)[number];
export type SectionId = Section["id"];

export const navKey = (id: SectionId) => `nav.${id}` as const satisfies MessageKey;
export const descKey = (id: SectionId) => `section.${id}.desc` as const satisfies MessageKey;
