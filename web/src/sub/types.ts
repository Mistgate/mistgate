// The page data the server embeds as <script type="application/json" id="mg-data">.
// data.ts reads it defensively: a field the server forgot must not blank the page.

export type Lang = "ru" | "en";
export type Platform = "ios" | "android" | "windows" | "macos" | "linux";
export type Kind = "happ" | "amnezia";
export type Status = "active" | "expired" | "limited" | "disabled";

/** One app of the settings. Several may share a platform and a kind; the page lists them in the settings' order. */
export type AppEntry = { platform: Platform; kind: Kind; name: string; download_url: string; add_url: string; description: string; recommended: boolean };

export type Device = {
  id: string;
  platform: string;
  model: string;
  app: string;
  last_seen_unix: number;
  online: boolean;
};

/** Fresh network-interface rates; utilization is based on the node's configured capacity when known. */
export type ServerLoad = { name: string; load_percent?: number; rx_bps: number; tx_bps: number; capacity_mbps: number };

/**
 * Amnezia part of the page (subs/devices.go pageAmnezia): the user's AmneziaWG devices and what
 * "add a device" can pick from. There is no key material in it: configs are asked for, one device at a time
 * (`endpoints`), and held in memory only. `null` for a user without the Amnezia app.
 */
export type MinClient = { client: string; app: string; min: string };
export type AwgDevice = {
  id: string;
  platform: string;
  label: string;
  profile_id: string;
  profile_name: string;
  version: string; // "3.1" | "2.0"
  address: string;
  last_handshake_unix: number;
  online: boolean;
  stale: boolean; // the profile changed: fetch the config again and re-import it
  min_clients: MinClient[];
};
/** An AmneziaWG profile a device can be added on; the page names it by its countries and exit, never by `name`. */
export type AwgProfile = { id: string; name: string; version: string; egress: "direct" | "warp"; countries: string[] };
export type AmneziaData = {
  devices: AwgDevice[];
  profiles: AwgProfile[];
  can_add: boolean;
  self_service: boolean; // false: the page only shows the devices
  endpoints: string; // base URL of the self-service calls ("" = none)
};
/** One config of a device on one node, as the endpoints answer. */
export type AwgConfig = {
  node_id: string;
  node_name: string;
  country_code: string;
  version: string;
  conf: string;
  vpn_key: string;
  filename: string;
  stale: boolean;
  warnings: string[];
  min_clients: MinClient[];
};

export type MgData = {
  v: number;
  lang: Lang;
  brand: { parts: [string, string]; logo_svg: string; accent: string };
  title: string;
  subscription_url: string;
  /** Servers the link gives Happ ("all your servers (3) appear in Happ"); 0 when unknown. */
  server_count: number;
  /** Nodes with a fresh sample; load_percent is max(RX, TX) as a share of configured node capacity, when known. */
  server_loads: ServerLoad[];
  user: {
    name: string;
    status: Status;
    expires_unix: number; // 0 = no expiry
    used_bytes: number;
    quota_bytes: number; // 0 = unlimited
    quota_reset: "none" | "day" | "week" | "month" | "rolling_month";
    next_reset_unix: number; // 0 = none
    device_limit: number; // 0 = unlimited
    devices_used: number;
  };
  announcement: string;
  support_url: string;
  options: { show_announcement: boolean; show_support: boolean; show_qr: boolean };
  apps: AppEntry[];
  access: { happ: boolean; amnezia: boolean };
  devices: Device[];
  amnezia: AmneziaData | null;
  /** The page asks for its password: only `brand`, `title`, `lang` and `unlock_url` are real (subs/unlock.go). */
  locked: boolean;
  unlock_url: string;
};
