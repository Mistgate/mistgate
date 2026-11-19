import { ConnectError, Code } from "@connectrpc/connect";
import { probe, profiles } from "@/lib/api";
import { sniDemo } from "./node/sni-scan";

// Development only. The states of "Sites near the node" (screens/node/sni-scan.tsx) on the Profiles tab:
// /dev-kit?only=nodepage-frame&tab=profiles&sni=<state>. Each shows the card the way the panel's last answer (or a click) leaves it; the call
// is stubbed with the scene's answer, so nothing leaves the page. Made-up node de1 in 203.0.113.0/24, made-up sites on example names.
//   never | warn | running | result | partial | empty | soon | busy | old | nat | noroots | noipv4 | failed | offline
//   apply | apply-pick | apply-same | apply-old   the window of "Apply…" on shop.example.com (one REALITY server; two; one that shows it already;
//                                                 an agent too old for it)
//   edit | edit-own   the edit window of a REALITY server that shows another site: no site of the node's own yet; one set
// "offline" needs the frame's state=down (the node is silent); the rest run on the calm node.
export const sniStates = ["never", "warn", "running", "result", "partial", "empty", "soon", "busy", "old", "nat", "noroots", "noipv4", "failed", "offline", "apply", "apply-pick", "apply-same", "apply-old", "edit", "edit-own"] as const;
export type SniState = (typeof sniStates)[number];

export function sniStateOf(v: string | null): SniState | undefined {
  return (sniStates as readonly string[]).includes(v ?? "") ? (v as SniState) : undefined;
}

const NOW = () => Math.floor(Date.now() / 1000);
const c = (ip: string, name: string, over: Record<string, unknown> = {}) => ({ ip, name, dns: "here", h2: true, singleSite: true, http: 200, ms: 11, days: 64, ...over });
const sites = [
  c("203.0.113.40", "shop.example.com"),
  c("203.0.113.52", "blog.example.net", { dns: "subnet", ms: 14, days: 41 }),
  c("203.0.113.61", "docs.example.org", { http: 301, ms: 9, days: 28 }),
  c("203.0.113.88", "portal.example.com", { dns: "elsewhere", singleSite: false, http: 403, ms: 17, days: 80 }),
  c("203.0.113.120", "media.example.net", { dns: "none", h2: false, singleSite: false, http: 0, ms: 23, days: 19 }),
];
const scan = (over: Record<string, unknown> = {}) => ({
  nodeName: "de1",
  error: "",
  retryAfterS: 0,
  atUnix: BigInt(NOW() - 12 * 60),
  range: "203.0.113.0/24",
  addresses: 253,
  open: 41,
  tls13: 19,
  mlkem: 7,
  valid: sites.length,
  partial: false,
  ownLeftOut: 0,
  candidates: sites,
  ...over,
});
const never = () => scan({ atUnix: 0n, range: "", addresses: 0, open: 0, tls13: 0, mlkem: 0, valid: 0, candidates: [] });

type Server = { id: string; profileId: string; profileName: string; port: number; protocol: string; tlsServerName: string; realityTarget: string; realityTargetOverride: string };

/** Installs the scene: the stub of the calls and how the card starts. `inbounds` are the node's servers, which the scene may change. */
export function sniScene(state: SniState, inbounds: Server[] = []) {
  let answer: Record<string, unknown> = scan();
  let demo: { warn?: boolean; running?: boolean; retryAfterS?: number; apply?: string; edit?: string } = {};
  // the REALITY server of the page shows the node's own site; the scenes below change that
  for (const i of inbounds) Object.assign(i, { realityTarget: i.protocol === "vless" ? "self" : "", realityTargetOverride: "" });
  const vless = inbounds.find((i) => i.protocol === "vless");
  const shop = "shop.example.com";
  switch (state) {
    case "apply":
    case "apply-old":
      demo = { apply: shop };
      break;
    case "apply-pick":
      if (vless) inbounds.push({ ...vless, id: "inb_4", profileId: "prf_inb_4", profileName: "VLESS · Reality · 8443", port: 8443, realityTarget: "www.example.org:443" });
      demo = { apply: shop };
      break;
    case "apply-same":
      if (vless) Object.assign(vless, { realityTarget: `${shop}:443`, realityTargetOverride: `${shop}:443`, tlsServerName: shop });
      demo = { apply: shop };
      break;
    case "edit":
      if (vless) Object.assign(vless, { realityTarget: "www.example.org:443" });
      demo = { edit: vless?.id };
      break;
    case "edit-own":
      if (vless) Object.assign(vless, { realityTarget: `${shop}:443`, realityTargetOverride: `${shop}:443`, tlsServerName: shop });
      demo = { edit: vless?.id };
      break;
    case "never":
      answer = never();
      break;
    case "warn":
      answer = never();
      demo = { warn: true };
      break;
    case "running":
      answer = never();
      demo = { running: true };
      break;
    case "partial":
      answer = scan({ partial: true, addresses: 187, open: 29, tls13: 12, mlkem: 4, valid: 2, candidates: sites.slice(0, 2) });
      break;
    case "empty":
      answer = scan({ open: 22, tls13: 3, mlkem: 0, valid: 0, candidates: [], ownLeftOut: 0 });
      break;
    case "soon":
      answer = scan({ error: "too_soon", retryAfterS: 18 * 60, ownLeftOut: 2 });
      demo = { retryAfterS: 18 * 60 };
      break;
    case "busy":
      answer = { ...never(), error: "busy" };
      break;
    case "old":
      answer = { ...never(), error: "agent_too_old" };
      break;
    case "nat":
      answer = { ...never(), error: "not_my_address" };
      break;
    case "noroots":
      answer = { ...never(), error: "no_roots" };
      break;
    case "noipv4":
      answer = { ...never(), error: "no_ipv4" };
      break;
    case "failed":
      answer = { ...never(), error: "failed" };
      break;
    case "offline":
      answer = scan({ ownLeftOut: 1 });
      break;
    case "result":
      answer = scan({ ownLeftOut: 1 });
      break;
  }
  const stub = probe as unknown as Record<string, unknown>;
  stub.nodeSniScan = async () => answer;
  // the check before the click: the inbound as it would be, the Hysteria2 site that yields on the way; an old agent refuses
  (profiles as unknown as Record<string, unknown>).updateInbound = async (req: { inboundId: string; realityTargetOverride?: string }) => {
    if (state === "apply-old") throw new ConnectError("agent_too_old", Code.FailedPrecondition);
    const was = inbounds.find((i) => i.id === req.inboundId);
    const target = req.realityTargetOverride ?? was?.realityTargetOverride ?? "";
    const host = target.replace(/:\d+$/, "");
    return {
      inbound: { ...was, realityTargetOverride: target, realityTarget: target || "self", tlsServerName: host },
      warnings: target && was?.realityTarget === "self" ? [{ code: "hy2_decoy_paused", params: { node: "de1", port: "443", hy2: "Hysteria2 · 443", vless: was.profileName, via: "site", target: host, port80: "open" } }] : [],
      freePort: 0,
    };
  };
  sniDemo.current = demo;
}
