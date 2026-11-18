import { Code, ConnectError } from "@connectrpc/connect";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import type { MihomoTemplate, ResponseHeader } from "./subscriptions/model";

// Development only: the made-up data of the scenes of "Response headers" and "Mihomo templates" (dev-kit-subs-hx.tsx): the board's
// own examples (PnSubs: the headers X-Example-*, the templates "Для семьи" and "Для друзей") and the stand-ins that answer the panel's
// two previews the way its rules would.

// The groups and the people of dev-kit-subs-page.tsx (demoGroups, demoPeople), written again here because that file imports the scenes that import
// this one; the test of the scenes holds the two lists equal.
export const groups = [
  { id: "grp_fm", name: "Семья" },
  { id: "grp_fr", name: "Друзья" },
];
export const people = [
  { id: "usr_alice", name: "alice", groupId: "grp_fm" },
  { id: "usr_bob", name: "bob", groupId: "grp_fr" },
  { id: "usr_carol", name: "carol", groupId: "grp_fm" },
  { id: "usr_dave", name: "dave", groupId: "grp_fr" },
];
const groupName = (id: string) => groups.find((g) => g.id === id)?.name ?? "";
/** The person of an id: alice and carol are in Семья, bob and dave in Друзья. */
const person = (id: string) => people.find((p) => p.id === id) ?? people[0]!;

// ---- headers

/** The names the panel keeps (a few of GetSubscriptionSettingsResponse.reserved_header_names, the families the board names first). */
export const reservedNames = ["Accept-*", "Access-Control-*", "Age", "Allow", "Authorization", "Cache-Control", "Connection", "Content-*", "Cookie", "Date", "ETag", "Host", "Location", "Mistgate-*", "Profile-*", "Server", "Set-Cookie", "Subscription-*", "Support-Url", "User-Agent", "Vary", "X-Hwid-*", "X-Powered-By"];

const h = (name: string, value: string, o: Partial<ResponseHeader> = {}): ResponseHeader => ({ name, value, base64: false, formats: [], onlyGroups: false, groupIds: [], ...o });

/** The board's "есть": a header as base64, one for Mihomo and Друзья, and a second of the same name that the first covers. */
export const hdrHas = [
  h("X-Example-Notice", "Плановые работы в субботу", { base64: true }),
  h("X-Example-Tier", "friends", { formats: [SubFormat.MIHOMO_YAML], onlyGroups: true, groupIds: ["grp_fr"] }),
  h("x-example-tier", "default"),
];
/** The board's "ошибки": a name the panel keeps, a name that is no token, a covered one, a group list left empty, a group that is gone. */
export const hdrBad = [
  h("Profile-Title", "Mistgate"),
  h("X Example", "ok"),
  h("X-Example-Notice", "Плановые работы"),
  h("X-Example-Tier", "friends", { formats: [SubFormat.BASE64_URIS], onlyGroups: true }),
  h("X-Example-Old", "x", { onlyGroups: true, groupIds: ["grp_gone"] }),
];
/** The board's "предел": twenty headers. */
export const hdrMax = Array.from({ length: 20 }, (_, i) => h(`X-Example-${i + 1}`, `v${i + 1}`));
/** More bytes than the panel takes as one answer. */
export const hdrOver = Array.from({ length: 5 }, (_, i) => h(`X-Example-${i + 1}`, `value-${i + 1}-`.padEnd(900, "x")));
/** A header of every kind of shape, for the phone and the right-to-left page. */
export const hdrMany = [...hdrHas, h("X-Example-Banner", "Привет, мир", { base64: true, formats: [SubFormat.MIHOMO_YAML, SubFormat.SINGBOX_JSON], onlyGroups: true, groupIds: ["grp_fm", "grp_fr"] })];

const formatOf = (ua: string): SubFormat => {
  const u = ua.toLowerCase();
  return u.includes("mozilla") ? SubFormat.USER_PAGE : u.includes("clash") || u.includes("mihomo") ? SubFormat.MIHOMO_YAML : u.includes("sing-box") || u.includes("sfa") ? SubFormat.SINGBOX_JSON : u.includes("v2ray") || u.includes("xray") ? SubFormat.XRAY_JSON : SubFormat.BASE64_URIS;
};

/** What the panel's preview of the headers answers: the format of the User-Agent, the headers that fit it and the person's group; carol is out of access. */
export function fakeHeaderPreview({ userId, userAgent, headers }: { userId: string; userAgent: string; headers: ResponseHeader[] }) {
  const p = person(userId);
  const format = formatOf(userAgent);
  const base = { format, groupName: groupName(p.groupId) };
  if (p.name === "carol") return { ...base, headers: [], notServed: "no_access" };
  if (format === SubFormat.USER_PAGE) return { ...base, headers: [], notServed: "page" };
  const seen = new Set<string>();
  const sent = headers
    .filter((x) => (x.formats.length === 0 || x.formats.includes(format)) && (!x.onlyGroups || x.groupIds.includes(p.groupId)))
    .filter((x) => !seen.has(x.name.toLowerCase()) && !!seen.add(x.name.toLowerCase()))
    .map((x) => ({ name: x.name, base64: x.base64, value: x.base64 ? `base64:${btoa(String.fromCharCode(...new TextEncoder().encode(x.value)))}` : x.value }));
  return { ...base, headers: sent, notServed: "" };
}

// ---- Mihomo templates

const Y1 = ["mixed-port: 7890", "mode: rule", "proxies: []", "proxy-groups:", "  - name: Основной", "    type: select", "    include-all-proxies: true", "rule-providers:", "  ads:", "    type: http", "    behavior: domain", "    url: https://example.com/rules/ads.yaml", "    interval: 86400", "rules:", "  - RULE-SET,ads,REJECT", "  - MATCH,Основной", ""].join("\n");
const Y2 = ["mode: rule", "dns:", "  enable: true", "  nameserver:", "    - 1.1.1.1", "proxies: []", "proxy-groups:", "  - name: Все серверы", "    type: select", "    include-all-proxies: true", "rules:", "  - MATCH,Все серверы", ""].join("\n");
/** A text the panel refuses: the key proxy-groups twice (the second at line 10), and a group that points at one that is not there. */
const YB = ["mode: rule", "proxies: []", "proxy-groups:", "  - name: Основной", "    type: select", "    proxies:", "      - Запасной", "rules:", "  - MATCH,Основной", "proxy-groups: []", ""].join("\n");

const t = (name: string, groupIds: string[], yaml: string): MihomoTemplate => ({ name, groupIds, yaml });
export const mtHas = [t("Для семьи", ["grp_fm"], Y1), t("Для друзей", ["grp_fr"], Y2)];
export const mtErr = [t("Для семьи", ["grp_fm"], YB), t("Для друзей", ["grp_fr"], Y2)];
export const mtFree = [t("Черновик", [], Y1)];
export const mtDup = [t("Для семьи", ["grp_fm"], Y1), t("Для друзей", ["grp_fr"], Y2), t("Черновик", [], Y2)];
export const mtMax = Array.from({ length: 20 }, (_, i) => t(`Шаблон ${i + 1}`, i === 0 ? ["grp_fm"] : i === 1 ? ["grp_fr"] : [], Y2));
/** More bytes in all than the panel keeps. */
export const mtOver = Array.from({ length: 5 }, (_, i) => t(`Шаблон ${i + 1}`, i === 0 ? ["grp_fm"] : [], Y2 + "# " + "x".repeat(58 * 1024) + "\n"));
export const mtBig = [t("Для семьи", ["grp_fm"], Y1 + "# " + "x".repeat(66 * 1024) + "\n")];

/**
 * What the panel's preview of a template answers: the profile of the person (their servers after the file's own comments, the group
 * Auto where the groups end, dns when the text has none), or a refusal that names the line; carol is out of access, dave has no server.
 * `warnings` are the panel's own notes (the codes it sends).
 */
export function fakeMihomoPreview(warnings: { code: string; arg: string }[] = []) {
  return ({ userId, yaml }: { userId: string; yaml: string; groupIds: string[] }) => {
    const lines = yaml.split("\n");
    const seen = new Map<string, number>();
    lines.forEach((l, i) => {
      const k = /^([A-Za-z][\w-]*):/.exec(l)?.[1];
      if (!k) return;
      if (seen.has(k)) throw new ConnectError(`line ${i + 1}: mapping key "${k}" already defined at line ${seen.get(k)}`, Code.InvalidArgument);
      seen.set(k, i + 1);
    });
    const bad = lines.findIndex((l) => /^[A-Za-z][\w-]*\s+[^\s:#][^:]*$/.test(l));
    if (bad >= 0) throw new ConnectError(`line ${bad + 1}: could not find expected ':'`, Code.InvalidArgument);
    const p = person(userId);
    const own = { warnings, groupName: groupName(p.groupId) };
    if (p.name === "carol") return { ...own, yaml: "mixed-port: 7890\nmode: rule\n", notServed: "no_access" };
    if (p.name === "dave") return { ...own, yaml: "mixed-port: 7890\nmode: rule\n", notServed: "no_servers" };
    const servers = ["  # добавила панель: серверы человека", "  - name: de1 · Hysteria2", "    type: hysteria2", "    server: de1.example.org", "    port: 443", ...(p.name === "bob" ? [] : ["  - name: nl1 · AmneziaWG", "    type: wireguard", "    server: nl1.example.org", "    port: 47213"])];
    const out: string[] = [];
    let inGroups = false;
    let added = false;
    const flush = () => {
      if (inGroups && !added) out.push("  # добавила панель: автовыбор сервера", "  - name: Auto");
      added = added || inGroups;
      inGroups = false;
    };
    for (const l of lines.filter((x) => x.trim() !== "")) {
      if (/^\S/.test(l)) flush();
      if (/^proxies\s*:/.test(l)) {
        out.push("proxies:", ...servers);
        continue;
      }
      if (/^proxy-groups\s*:/.test(l)) inGroups = true;
      out.push(l);
    }
    flush();
    if (!/^dns\s*:/m.test(yaml)) out.push("# добавила панель: резолверы DNS-пресета человека", "dns:", "  nameserver:", "    - 1.1.1.1");
    return { ...own, yaml: out.join("\n") + "\n", notServed: "" };
  };
}
