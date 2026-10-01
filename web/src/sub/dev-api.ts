import type { Api } from "./amz-actions";
import { ApiError, type Answer } from "./api";
import { awgConfig, awgDevice } from "./logic";

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

// the names the server gives keys and files (access/names.go): the brand and the country, "mistgate-de.conf"
const cfg = (node: string, cc: string, stale = false) =>
  awgConfig({
    node_id: `nod_${node}`,
    node_name: node,
    country_code: cc,
    version: "3.1",
    conf: conf(node),
    vpn_key: `vpn://${"AAAAAQAAAGh4nO".repeat(10)}`,
    filename: `mistgate-${cc.toLowerCase()}.conf`,
    stale,
    warnings: ["amnezia_desktop_mtu"],
    min_clients: [{ client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" }, { client: "amnezia", app: "AmneziaWG Android", min: "v3.1.20260814" }],
  });

const nodes = () => [cfg("de1", "DE"), cfg("fi1", "FI")];
const perPlatform = new Map<string, number>();

/** The dev server's password form accepts abcd-2345; three wrong tries in a row get the "try later" answer. */
let wrong = 0;
export async function devUnlock(_url: string, password: string): Promise<void> {
  await pause();
  if (password === "abcd-2345") return;
  wrong += 1;
  if (wrong >= 3) throw new ApiError(429, "locked", 600);
  throw new ApiError(401, "wrong_password", 0, 3 - wrong);
}

export const devApi: Api = {
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
    return { device, configs: nodes() };
  },
  async getConfigs(): Promise<Answer> {
    await pause();
    return { configs: nodes() };
  },
  async rotateKey(): Promise<Answer> {
    await pause();
    return { configs: nodes() };
  },
  async revokeDevice() {
    await pause();
  },
};
