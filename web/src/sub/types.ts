// The page data the server embeds as <script type="application/json" id="mg-data">.
// logic.ts reads it defensively: a field the server forgot must not blank the page.

export type Lang = "ru" | "en";
export type Platform = "ios" | "android" | "windows" | "macos" | "linux";
export type Kind = "happ" | "amnezia";
export type Status = "active" | "expired" | "limited" | "disabled";
export type Theme = "auto" | "light" | "dark";

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

/** How busy a node's channel is, coarsely (subs/page.go loadLevel); the page never gets the rates. */
export type LoadLevel = "low" | "medium" | "high";
export type ServerLoad = { name: string; level: LoadLevel };

/** One way a server is reachable: the subscription link (as an app names it) or a key (an AmneziaWG profile), direct or by the spare exit. */
export type ServerConn = {
  way: "link" | "key";
  exit: "direct" | "warp";
  app_name: string;
  profile_id: string;
  /** A link only the Mihomo apps (kl!ck, Clash Verge, FlClash) can use: Happ and the other link-list apps do not get it. */
  mihomo_only?: boolean;
};
/** The DNS choice of one server: what the person picked ("" = nothing), what applies, what the owner allows. */
export type ServerDns = {
  choice: string;
  effective: string;
  options: string[];
  /** The owner's default for the server, when the page data says it ("" = not told). */
  default: string;
  /** Key devices on this server that still need a key with the new DNS. */
  keys_to_refresh: string[];
};
/** A server of the person, as the page may name it: never the panel's node name. `id` is only what the DNS call needs. */
export type ServerEntry = {
  id: string;
  country_code: string;
  place: string;
  label: string;
  app_names: string[];
  connections: ServerConn[];
  online: boolean;
  load: LoadLevel | null;
  dns: ServerDns | null;
};
export type DnsPreset = { id: string; name: string; description: string; category: string };
export type DnsInfo = {
  enabled: boolean;
  /** Where the choice is posted ("" in the admin's preview and when the choice is off). */
  endpoint: string;
  refresh_hours: number;
  /** `per_server` false: the apps that use the link have one DNS for all servers; `effective` is that preset. */
  link: { per_server: boolean; effective: string };
};

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
  stale: boolean; // fetch the config again and re-import it
  /** Why: the profile changed, or the DNS of one of its servers did (the key itself stays the same). */
  stale_reason: "profile" | "dns";
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
  /** The server as the page names it ("Germany · Frankfurt", "Server"), never the panel's node name. It is also what the connection is called in the app. */
  label: string;
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
  /** Servers (nodes) the link carries ("all your servers (3) are already there"); 0 when unknown. */
  server_count: number;
  /** Nodes with a set capacity and a fresh sample, in subscription order: the level only. The old way to list servers. */
  server_loads: ServerLoad[];
  /** Every server of the person, in subscription order. Empty on an older panel: the cards then come from `server_loads`. */
  servers: ServerEntry[];
  /** The DNS choice; null on an older panel (no DNS part on the page at all). */
  dns: DnsInfo | null;
  dns_presets: DnsPreset[];
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
