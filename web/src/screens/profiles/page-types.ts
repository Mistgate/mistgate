import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary, ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import type { ServerRow } from "./profiles-model";
import type { DeployProfile } from "./deploy";

// What the profile page's parts share: the data the page was drawn from, what the owner has in the form now, and the windows that open
// from more than one place (the head's button, the verdict, a tab).

export type PageData = {
  profile: ProfileSummary;
  info: ProtocolInfo;
  inbounds: Inbound[];
  /** What the server holds: the name and the settings (secrets masked). */
  initial: { name: string; settingsJson: string };
};

/** The windows of the page: put on nodes, take off, copy through WARP / without it, delete. */
export type Dlg = "" | "put" | "take" | "twin" | "del";

export type TabCtx = {
  data: PageData;
  /** Only the owner changes what runs on the nodes (policy.go); a helper changes groups; an observer reads. */
  owner: boolean;
  /** Helper or owner: the day-to-day (groups). */
  canGroups: boolean;
  servers: ServerRow[];
  /** Every group of the panel, and the ones that carry this profile. */
  groups: Group[];
  inGroups: Group[];
  /** What "Put on nodes" must know before the click (a domain where Let's Encrypt cannot work on an IP): from the saved settings. */
  cert: Pick<DeployProfile, "tlsMode" | "sni">;
  /** The form holds this many unsaved changes: copies and windows that read the saved version wait for them. */
  edits: number;
  /** The profile's own exit: "direct" or "warp" (the saved one); empty when the protocol has none. */
  egress: "" | "direct" | "warp";
  openDlg: (d: Dlg) => void;
  /** Takes the page to another tab. */
  goTab: (tab: "settings" | "servers" | "people") => void;
  /** The address of a server the head or the verdict wants the Servers tab to open for editing (its port and domain), once. */
  edit: string;
  onEdit: (inboundId: string) => void;
  /** The dev kit's scene variant ("menu", "edit", "put-run": the state a scene fixes inside a tab or a window); empty on a real page. */
  variant: string;
};
