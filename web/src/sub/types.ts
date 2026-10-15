// The page data the server embeds as <script type="application/json" id="mg-data">.
// logic.ts reads it defensively: a field the server forgot must not blank the page.

export type Lang = "ru" | "en" | "zh";
export type Platform = "ios" | "android" | "windows" | "macos" | "linux";
export type Kind = "happ" | "amnezia";
export type Status = "active" | "expired" | "limited" | "disabled";
export type Theme = "auto" | "light" | "dark";

/** One app of the settings. Several may share a platform and a kind; the page lists them in the settings' order. */
export type AppEntry = { platform: Platform; kind: Kind; name: string; download_url: string; add_url: string; description: string; recommended: boolean };

/** The mode of the device limit for the apps on the link: counted only, refused over the limit, or also refused without a device ID. */
export type LinkMode = "track" | "enforce" | "strict";

export type Device = {
  id: string;
  platform: string;
  /** The model the app reported, or the name the person gave the device. */
  model: string;
  app: string;
  last_seen_unix: number;
  online: boolean;
  /** "link": an app that reports a device ID; "shared": the one place of the apps that report none; "key": an AmneziaVPN key. */
  kind: "link" | "shared" | "key";
  /** The person may remove or rename it on this page (never the shared place, never in the admin's preview). */
  can_remove: boolean;
  can_rename: boolean;
  /** What the app said about itself: the OS version ("18.1") and the app ("Happ/3.4.2"); "" when it said nothing. */
  os_version: string;
  client: string;
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
/**
 * A preset the page names. `name` and `description` are in the language of the browser; `names` and `descriptions` hold the
 * stock words of a built-in preset in every page language (absent for an owner's own text), so a page switched to another
 * language than the browser's shows its own.
 */
export type DnsPreset = { id: string; name: string; description: string; category: string; names?: Partial<Record<Lang, string>>; descriptions?: Partial<Record<Lang, string>> };
/**
 * The DNS of the apps that use the link: one for all servers. `effective` is what they get, `choice` the person's pick ("" =
 * none), `default` what they get without one, `options` what the owner offers (empty: no choice).
 */
export type LinkDns = { effective: string; choice: string; default: string; options: string[] };
export type DnsInfo = {
  enabled: boolean;
  /** Where the choice is posted ("" in the admin's preview and when the choice is off). */
  endpoint: string;
  refresh_hours: number;
  link: LinkDns;
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
  /** Where a device of the link is removed and renamed (POST <it>/<id>/revoke, /rename); "" in the admin's preview and with self-service off. */
  devices_endpoint: string;
  link_device_limit: LinkMode;
  amnezia: AmneziaData | null;
  /** The page asks for its password: only `brand`, `title`, `lang` and `unlock_url` are real (subs/unlock.go). */
  locked: boolean;
  unlock_url: string;
};
