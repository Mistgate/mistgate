import { probe } from "@/lib/api";
import { sniDemo } from "./node/sni-scan";

// Development only. The states of "Sites near the node" (screens/node/sni-scan.tsx) on the Profiles tab:
// /dev-kit?only=nodepage-frame&tab=profiles&sni=<state>. Each shows the card the way the panel's last answer (or a click) leaves it; the call
// is stubbed with the scene's answer, so nothing leaves the page. Made-up node de1 in 203.0.113.0/24, made-up sites on example names.
//   never | warn | running | result | partial | empty | soon | busy | old | nat | noroots | noipv4 | failed | offline
// "offline" needs the frame's state=down (the node is silent); the rest run on the calm node.
export const sniStates = ["never", "warn", "running", "result", "partial", "empty", "soon", "busy", "old", "nat", "noroots", "noipv4", "failed", "offline"] as const;
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

/** Installs the scene: the stub of the call and how the card starts. */
export function sniScene(state: SniState) {
  let answer: Record<string, unknown> = scan();
  let demo: { warn?: boolean; running?: boolean; retryAfterS?: number } = {};
  switch (state) {
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
  sniDemo.current = demo;
}
