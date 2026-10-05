import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import type { PlatformApp } from "./model";

/**
 * Apps the admin's "Add app" can fill in with one choice: the name, the kind, the download link of the chosen platform
 * and the "add" link template. Only apps whose add link is confirmed by the app's own docs or source (the client-apps page
 * of the docs says where); the defaults of a fresh install (subsettings.Defaults) carry the same data. Every field stays editable.
 * Left out on purpose: Streisand and Shadowrocket (the scheme is not confirmed), sing-box (it needs a JSON profile the panel
 * does not serve).
 */
export type KnownApp = { id: string; name: string; kind: App; addLinkTemplate: string; downloads: Partial<Record<Platform, string>> };

const { IOS, ANDROID, WINDOWS, MACOS, LINUX } = Platform;
const gh = (repo: string) => `https://github.com/${repo}/releases/latest`;
const everywhere = (url: string, ...p: Platform[]): Partial<Record<Platform, string>> => Object.fromEntries(p.map((x) => [x, url]));

export const knownApps: readonly KnownApp[] = [
  // the release page: the asset names carry the version and Windows and macOS builds are released separately
  { id: "klick", name: "kl!ck", kind: App.HAPP, addLinkTemplate: "klick://add?url={url_enc}&name={name_enc}", downloads: everywhere(gh("vbu00/klick"), WINDOWS, MACOS) },
  {
    id: "happ",
    name: "Happ",
    kind: App.HAPP,
    addLinkTemplate: "happ://add/{url}",
    downloads: {
      [IOS]: "https://apps.apple.com/us/app/happ-proxy-utility/id6504287215",
      [ANDROID]: "https://play.google.com/store/apps/details?id=com.happproxy",
      [WINDOWS]: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe",
      [MACOS]: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.macOS.universal.dmg",
    },
  },
  {
    id: "amnezia",
    name: "AmneziaVPN",
    kind: App.AMNEZIA,
    addLinkTemplate: "",
    downloads: {
      [IOS]: "https://apps.apple.com/us/app/amneziavpn/id1600529900",
      [ANDROID]: "https://play.google.com/store/apps/details?id=org.amnezia.vpn",
      ...everywhere("https://amnezia.org/downloads", WINDOWS, MACOS, LINUX),
    },
  },
  { id: "clash-verge", name: "Clash Verge Rev", kind: App.HAPP, addLinkTemplate: "clash-verge://install-config?url={url_enc}&name={name_enc}", downloads: everywhere(gh("clash-verge-rev/clash-verge-rev"), WINDOWS, MACOS, LINUX) },
  { id: "flclash", name: "FlClash", kind: App.HAPP, addLinkTemplate: "flclash://install-config?url={url_enc}", downloads: everywhere(gh("chen08209/FlClash"), ANDROID, WINDOWS, MACOS, LINUX) },
  { id: "cmfa", name: "Clash Meta for Android", kind: App.HAPP, addLinkTemplate: "clashmeta://install-config?url={url_enc}&name={name_enc}", downloads: everywhere(gh("MetaCubeX/ClashMetaForAndroid"), ANDROID) },
  {
    id: "hiddify",
    name: "Hiddify",
    kind: App.HAPP,
    addLinkTemplate: "hiddify://import/{url}#{name_enc}",
    downloads: {
      [IOS]: "https://apps.apple.com/us/app/hiddify-proxy-vpn/id6596777532",
      [ANDROID]: "https://play.google.com/store/apps/details?id=app.hiddify.com",
      ...everywhere(gh("hiddify/hiddify-app"), WINDOWS, MACOS, LINUX),
    },
  },
  { id: "v2rayng", name: "v2rayNG", kind: App.HAPP, addLinkTemplate: "v2rayng://install-sub?url={url_enc}", downloads: everywhere(gh("2dust/v2rayNG"), ANDROID) },
];

/** The known apps that exist on a platform, in catalog order. */
export const knownFor = (p: Platform): KnownApp[] => knownApps.filter((a) => a.downloads[p] !== undefined);

/** The card a known app makes on a platform (not recommended, no description: as a hand-made one starts); null when it has no build there. */
export function appOf(id: string, platform: Platform): PlatformApp | null {
  const k = knownApps.find((a) => a.id === id);
  const downloadUrl = k?.downloads[platform];
  if (!k || downloadUrl === undefined) return null;
  return { platform, kind: k.kind, name: k.name, downloadUrl, addLinkTemplate: k.addLinkTemplate, description: "", recommended: false };
}
