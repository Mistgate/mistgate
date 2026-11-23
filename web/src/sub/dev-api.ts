import type { Api } from "./amz-actions";
import { ApiError, type Answer, type DnsAnswer } from "./api";
import type { DnsApi } from "./dns-actions";
import { sampleQr } from "./dev-data";
import { awgConfig, awgDevice, linkDns, serverEntry } from "./logic";
import type { AwgConfig, ServerEntry } from "./types";

// DEV SERVER ONLY (main.ts reaches this through `import.meta.env.DEV`, a build drops it): answers the self-service calls
// of the sample cases ("dev:" endpoints) with made-up data, so the Amnezia section can be worked on without a panel.
// `?fail=<code>` makes every call fail with that code (device_limit, too_many_requests, network...).

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
let n = 100;

const fail = () => new URLSearchParams(location.search).get("fail");
async function pause() {
  await wait(350);
  const f = fail();
  if (f) throw new ApiError(f === "network" ? 0 : 409, f, f === "too_many_requests" ? 1800 : 0);
}

// ~1 KB of config text: the size of a real 3.1 one, so the QR code is the density a user will see
const conf = (name: string) => `[Interface]
PrivateKey = ${"A".repeat(43)}=
Address = 10.66.4.9/32, fd66:66:0:1::9/128
DNS = 1.1.1.1, 8.8.8.8
MTU = 1280
Jc = 6
Jmin = 10
Jmax = 50
S1 = 24
S2 = 24
S3 = 24
S4 = 24
H1 = 100000-800000
H2 = 1000000-8000000
H3 = 10000000-80000000
H4 = 100000000-800000000
I1 = <b 0x${"c3000000010800000000".repeat(8)}><r 24><t>

[Peer]
PublicKey = ${"B".repeat(43)}=
PresharedKey = ${"C".repeat(43)}=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = ${name}.example.com:23456
PersistentKeepalive = 25
`;

// the names the server gives servers, keys and files (access/names.go): the country, "mistgate-de.conf"
// `via` is the country of an entry node: the key of the path through it (the same node id, a name that ends "· via RU", its own file)
const cfg = (node: string, cc: string, stale = false, via = "") =>
  awgConfig({
    node_id: `nod_${node}`,
    ...(via ? { via_node_id: "nod_msk1", via_country: via } : {}),
    // the server names keys in the instance's language; the sample follows the page's, so each page shows its own
    label: (new Intl.DisplayNames([document.documentElement.lang || "ru"], { type: "region" }).of(cc) ?? cc) + (via ? ` · via ${via}` : ""),
    country_code: cc,
    version: "3.1",
    conf: conf(via ? "msk1" : node),
    qr: sampleQr,
    vpn_key: `vpn://${"AAAAAQAAAGh4nO".repeat(10)}`,
    filename: `mistgate-${cc.toLowerCase()}${via ? `-via-${via.toLowerCase()}` : ""}.conf`,
    stale,
    warnings: ["amnezia_desktop_mtu"],
    min_clients: [{ client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" }, { client: "amnezia", app: "AmneziaWG Android", min: "v3.1.20260814" }],
  });

// the "awg-relay" case: the path through the entry in Russia comes right after the direct config of its server
const viaRu = () => new URLSearchParams(location.search).get("case") === "awg-relay";
const fixed = () => (viaRu() ? [cfg("de1", "DE"), cfg("de1", "DE", false, "RU"), cfg("fi1", "FI")] : [cfg("de1", "DE"), cfg("fi1", "FI")]);
const perPlatform = new Map<string, number>();
// the profile of the devices made in this session: they are not in the page's data
const madeOn = new Map<string, string>();
// a page whose servers say which carry a profile gets one config for each of them (the key-* cases); any other page the two above
const nodes = (profile = ""): AwgConfig[] => {
  const on = pageServers().filter((x) => x.connections.some((c) => c.way === "key" && c.profile_id === profile));
  return on.length > 0 ? on.map((x) => cfg(x.id.replace(/^nod_/, ""), x.country_code)) : fixed();
};
const profileOf = (device: string) => madeOn.get(device) ?? pageData().amnezia?.devices?.find((x) => x.id === device)?.profile_id ?? "";

/** The dev server's password form accepts abcd-2345; three wrong tries in a row get the "try later" answer. */
let wrong = 0;
export async function devUnlock(_url: string, password: string): Promise<void> {
  await pause();
  if (password === "abcd-2345") return;
  wrong += 1;
  if (wrong >= 3) throw new ApiError(429, "locked", 600);
  throw new ApiError(401, "wrong_password", 0, 3 - wrong);
}

// the page's own data, to answer a DNS choice with the server (or the link apps' DNS) as it would be afterwards
const pageData = (): { servers?: unknown; dns?: { link?: unknown }; amnezia?: { devices?: { id: string; profile_id: string }[] } } => {
  try {
    return JSON.parse(document.getElementById("mg-data")?.textContent ?? "{}") as ReturnType<typeof pageData>;
  } catch {
    return {};
  }
};
const pageServers = (): ServerEntry[] => {
  const raw = pageData();
  return Array.isArray(raw.servers) ? raw.servers.map(serverEntry) : [];
};
const pageLink = () => linkDns(pageData().dns?.link);

export const devApi: Api & DnsApi = {
  async renameDevice(_e, id, label): Promise<Answer> {
    await pause();
    return { device: awgDevice({ id, label, platform: "android", profile_id: "p31", version: "3.1" }), configs: [] };
  },
  async setDns(_e, body): Promise<DnsAnswer> {
    await pause();
    if ("link" in body) {
      const link = pageLink();
      if (body.preset && !link.options.includes(body.preset)) throw new ApiError(409, "not_allowed");
      return { server: undefined, link: { ...link, choice: body.preset, effective: body.preset || link.default }, stale_devices: [] };
    }
    const srv = pageServers().find((x) => x.id === body.server);
    if (!srv?.dns) throw new ApiError(404, "not_found");
    if (body.preset && !srv.dns.options.includes(body.preset)) throw new ApiError(409, "not_allowed");
    const stale = srv.connections.some((c) => c.way === "key") ? ["d3"] : [];
    return { server: { ...srv, dns: { ...srv.dns, choice: body.preset, effective: body.preset || srv.dns.options[0] || srv.dns.effective, keys_to_refresh: stale } }, stale_devices: stale };
  },
  async addDevice(_e, body): Promise<Answer> {
    await pause();
    n += 1;
    // like the server (access/device.go): a device sent without a name is called "<platform> <n>"
    const k = (perPlatform.get(body.platform) ?? 0) + 1;
    perPlatform.set(body.platform, k);
    const device = awgDevice({
      id: `dev_${n}`,
      platform: body.platform,
      label: body.label || `${body.platform} ${k}`,
      profile_id: body.profile_id,
      profile_name: "Main",
      version: "3.1",
      address: `10.66.4.${n % 250}, fd66:66:0:1::${n % 250}`,
      min_clients: [{ client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" }],
    });
    madeOn.set(device.id, body.profile_id);
    return { device, configs: nodes(body.profile_id) };
  },
  async getConfigs(_e, id): Promise<Answer> {
    await pause();
    return { configs: nodes(profileOf(id)) };
  },
  async rotateKey(_e, id): Promise<Answer> {
    await pause();
    return { configs: nodes(profileOf(id)) };
  },
  async revokeDevice() {
    await pause();
  },
};
