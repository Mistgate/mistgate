import { ClientCompatActual, ClientCompatState, type CompatDevice, type CompatRule } from "@/lib/clientcompat";

// Development only: the made-up devices and the table of minimum versions of the App versions scenes (dev-kit-subs-scenes.tsx).
export const ago = (s: number) => Math.floor(Date.now() / 1000) - s;
export const dev = (o: Partial<CompatDevice> & Pick<CompatDevice, "userId" | "userName" | "state" | "reason">): CompatDevice => ({
  deviceId: `${o.userId}-1`,
  client: "",
  platform: "",
  model: "",
  osVersion: "",
  shared: false,
  lastSeenUnix: 0,
  needVersion: "",
  fingerprint: "",
  appTitle: "",
  actual: ClientCompatActual.NONE,
  helloPq: 0,
  helloClassic: 0,
  helloUnknown: 0,
  tableMismatch: "",
  ...o,
});
export const B = ClientCompatState.BELOW_MINIMUM;
export const U = ClientCompatState.UNSUPPORTED;
export const K = ClientCompatState.UNKNOWN;
export const PQ = ClientCompatActual.PQ;
export const CL = ClientCompatActual.CLASSIC;
export const MX = ClientCompatActual.MIXED;
export const compatDevices: CompatDevice[] = [
  dev({ userId: "alice", userName: "alice", state: B, reason: "version", needVersion: "3.1.1", client: "Happ/3.0.2", appTitle: "Happ", platform: "android", model: "Pixel 8", osVersion: "15", lastSeenUnix: ago(420), actual: PQ, helloPq: 41, tableMismatch: "better" }),
  dev({ userId: "alice", userName: "alice", deviceId: "alice-2", state: U, reason: "fingerprint", fingerprint: "random", client: "Mihomo/1.19.12", appTitle: "Mihomo", platform: "windows", model: "ThinkPad T14", osVersion: "11", lastSeenUnix: ago(3 * 3600), actual: CL, helloClassic: 17 }),
  dev({ userId: "bob", userName: "bob", state: U, reason: "app", client: "SFI/1.14.3", appTitle: "sing-box (SFA, SFI, SFM)", platform: "ios", model: "iPhone 15", osVersion: "18.1", lastSeenUnix: ago(86400 * 2), actual: CL, helloClassic: 6, helloUnknown: 1 }),
  dev({ userId: "carol", userName: "carol", deviceId: "", shared: true, state: K, reason: "app_unknown", client: "NovaVPN/1.2", lastSeenUnix: ago(86400), actual: MX, helloPq: 8, helloClassic: 3 }),
  dev({ userId: "dave", userName: "dave", state: B, reason: "version", needVersion: "1.9.30", client: "v2rayNG/1.8.5", appTitle: "v2rayNG", platform: "android", model: "Galaxy S23", osVersion: "14", lastSeenUnix: ago(86400 * 6), actual: PQ, helloPq: 3, tableMismatch: "better" }),
  dev({ userId: "erin", userName: "erin", state: K, reason: "no_version", client: "FlClash", appTitle: "FlClash", platform: "macos", model: "MacBook Air", osVersion: "15.0", lastSeenUnix: ago(1800) }),
  dev({ userId: "frank", userName: "frank", state: K, reason: "no_threshold", client: "Happ/3.4.2", appTitle: "Happ", platform: "ios", model: "iPhone 13", osVersion: "17.5", lastSeenUnix: ago(86400 * 11) }),
  dev({ userId: "grace", userName: "grace", state: K, reason: "no_version", platform: "linux", model: "Laptop", lastSeenUnix: 0 }),
  dev({ userId: "heidi", userName: "heidi", state: U, reason: "app", client: "Karing/1.2.3", appTitle: "Karing", platform: "ios", model: "iPad Air", osVersion: "17.2", lastSeenUnix: ago(86400 * 20) }),
];
export const count = (state: ClientCompatState, devices: number, people: number) => ({ state, devices, people });
export const compatCounts = [count(ClientCompatState.OK, 14, 6), count(B, 2, 2), count(U, 3, 3), count(K, 4, 4)];
// What the nodes saw, over all devices: new-style 12, old-style 3, mixed 1, no data 9; two disagreements with the table (an OK device that sends
// old-style handshakes, a device below the minimum that sends new-style ones), and one node whose agent does not count yet.
export const actualCount = (actual: ClientCompatActual, devices: number, people: number) => ({ actual, devices, people });
export const compatActual = [actualCount(ClientCompatActual.NONE, 9, 5), actualCount(PQ, 12, 6), actualCount(CL, 3, 2), actualCount(MX, 1, 1)];
export const compatPage = (devices: CompatDevice[], counts = compatCounts, actualCounts = compatActual, o: { worse?: number; better?: number; nodes?: string[] } = {}) => ({
  devices,
  total: devices.length,
  counts,
  fingerprints: ["chrome", "firefox"],
  actualCounts,
  helloWindowHours: 168,
  mismatchWorse: o.worse ?? 1,
  mismatchBetter: o.better ?? 2,
  helloKexNodesWithout: o.nodes ?? ["nl2"],
});
// the "In fact" view: every state, the devices that sent old-style or mixed handshakes (the table's OK device among them)
export const classicDevices: CompatDevice[] = [
  dev({ userId: "ivan", userName: "ivan", deviceId: "ivan-1", state: ClientCompatState.OK, reason: "", client: "Happ/3.4.2", appTitle: "Happ", platform: "android", model: "Pixel 7", osVersion: "15", lastSeenUnix: ago(600), actual: CL, helloClassic: 9, tableMismatch: "worse" }),
  compatDevices[1]!,
  compatDevices[2]!,
];
export const rule = (app: string, platform: string, core: string, minChrome: string, minOther: string, o: Partial<CompatRule> = {}): CompatRule => ({ app, platform, core, unsupported: false, minChrome, minOther, title: o.title ?? app, ...o });
export const compatDefaults: CompatRule[] = [
  rule("happ", "", "xray", "3.1.1", "3.1.1", { title: "Happ" }),
  rule("mihomo", "", "mihomo", "1.19.31", "1.19.31", { title: "Mihomo" }),
  rule("v2rayng", "android", "xray", "1.9.30", "1.9.30", { title: "v2rayNG" }),
  rule("karing", "", "singbox", "", "", { title: "Karing", unsupported: true }),
  rule("flclash", "", "mihomo", "0.8.90", "", { title: "FlClash" }),
];
export const compatRules: CompatRule[] = [compatDefaults[0]!, { ...compatDefaults[1]!, minOther: "1.19.40" }, ...compatDefaults.slice(2), rule("novavpn", "", "unknown", "1.4", "", { title: "novavpn" })];
