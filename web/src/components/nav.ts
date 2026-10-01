import type { MessageKey } from "@/i18n/en";
import type { NavIconName } from "@/components/ui/icons";

// The 9 sections of the admin, in order. The first three sit in the mobile dock next to
// "More", the rest go into its sheet. `group` puts a labelled separator above the item in the sidebar.
export const sections = [
  { id: "overview", to: "/" },
  { id: "nodes", to: "/nodes" },
  { id: "users", to: "/users" },
  { id: "profiles", to: "/profiles", group: "nav.group.setup" },
  { id: "subscriptions", to: "/subscriptions" },
  { id: "health", to: "/health", group: "nav.group.ops" },
  { id: "updates", to: "/updates" },
  { id: "integrations", to: "/integrations" },
  { id: "settings", to: "/settings" },
] as const satisfies readonly { id: NavIconName; to: string; group?: MessageKey }[];

export type Section = (typeof sections)[number];
export type SectionId = Section["id"];

export const navKey = (id: SectionId) => `nav.${id}` as const satisfies MessageKey;
export const descKey = (id: SectionId) => `section.${id}.desc` as const satisfies MessageKey;
