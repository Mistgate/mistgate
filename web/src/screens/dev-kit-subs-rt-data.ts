import { Code, ConnectError } from "@connectrpc/connect";
import type { RoutingInfo, RoutingProfile } from "./subscriptions/model";

// Development only: the made-up data of the scenes of the "Routing" tab (dev-kit-subs-rt.tsx): profiles in the shape the panel stores
// them, and a stand-in that answers PreviewRoutingProfile the way the panel's reading would (a link, base64 or JSON; the counts, the
// fields it leaves out, or its refusal). Nothing leaves the page.

const known = ["Name", "GlobalProxy", "LastUpdated", "RemoteDNSType", "RemoteDNSDomain", "RemoteDNSIP", "RemoteDns", "DomesticDNSType", "DomesticDNSDomain", "DomesticDNSIP", "DomesticDns", "DnsHosts", "FakeDNS", "DirectSites", "DirectIp", "ProxySites", "ProxyIp", "BlockSites", "BlockIp", "Geoipurl", "Geositeurl", "DomainStrategy", "useChunkFiles"];
const b64 = (s: string) => btoa(String.fromCharCode(...new TextEncoder().encode(s)));
const unb64 = (s: string) => new TextDecoder().decode(Uint8Array.from(atob(s), (c) => c.charCodeAt(0)));
const count = (v: unknown) => (Array.isArray(v) ? v.length : 0);
const str = (v: unknown) => (typeof v === "string" ? v : "");

/** What the panel's reading finds in the JSON text of a profile; throws the refusal of a text it cannot read. */
function read(text: string): Record<string, unknown> {
  const t = text.trim();
  let raw = t;
  if (!t.startsWith("{")) {
    const link = /^[A-Za-z0-9+.-]*:\/\/routing\/(?:onadd|add)\/(.+)$/i.exec(t);
    try {
      raw = unb64(decodeURIComponent((link?.[1] ?? t).replace(/-/g, "+").replace(/_/g, "/")));
    } catch {
      throw new ConnectError("invalid subscription settings: this is not a link, JSON or base64 text of a profile", Code.InvalidArgument);
    }
  }
  let o: unknown;
  try {
    o = JSON.parse(raw);
  } catch {
    throw new ConnectError("invalid subscription settings: the profile is not valid JSON: invalid character", Code.InvalidArgument);
  }
  if (typeof o !== "object" || o === null || Array.isArray(o)) throw new ConnectError("invalid subscription settings: the profile is not a JSON object (it must start with {)", Code.InvalidArgument);
  const obj = o as Record<string, unknown>;
  if (!Object.keys(obj).some((k) => known.includes(k))) throw new ConnectError("invalid subscription settings: no routing field is in the profile (Name, DirectSites, ProxySites, BlockSites and so on)", Code.InvalidArgument);
  for (const k of ["Geoipurl", "Geositeurl"]) if (str(obj[k]) !== "" && !str(obj[k]).startsWith("https://")) throw new ConnectError(`invalid subscription settings: ${k}: an https address (https://host/path)`, Code.InvalidArgument);
  return obj;
}

const link = (json: string) => `happ://routing/onadd/${b64(json)}`;

/** The info of a profile as the panel computes it. */
export function infoOf(json: string, dropped: string[] = []): RoutingInfo {
  const o = read(json);
  const pick = (d: unknown, ip: unknown, alias: unknown) => str(d) || str(ip) || str(alias);
  return {
    name: str(o.Name) || "Routing",
    directSites: count(o.DirectSites), directIp: count(o.DirectIp), proxySites: count(o.ProxySites), proxyIp: count(o.ProxyIp), blockSites: count(o.BlockSites), blockIp: count(o.BlockIp),
    remoteDnsType: str(o.RemoteDNSType), remoteDns: pick(o.RemoteDNSDomain, o.RemoteDNSIP, o.RemoteDns), domesticDnsType: str(o.DomesticDNSType), domesticDns: pick(o.DomesticDNSDomain, o.DomesticDNSIP, o.DomesticDns),
    hosts: o.DnsHosts && typeof o.DnsHosts === "object" ? Object.keys(o.DnsHosts).length : 0,
    geoipAddress: str(o.Geoipurl), geositeAddress: str(o.Geositeurl), globalProxy: str(o.GlobalProxy), domainStrategy: str(o.DomainStrategy), fakeDns: str(o.FakeDNS), lastUpdated: str(o.LastUpdated), useChunkFiles: o.useChunkFiles === true,
    droppedFields: dropped, profileBytes: json.length, headerBytes: link(json).length,
  } as RoutingInfo;
}

/** A profile as the settings carry it: its JSON and the info the panel reads from it. */
export const rp = (obj: Record<string, unknown>, o: Partial<Pick<RoutingProfile, "onlyAdd" | "onlyGroups" | "groupIds">> = {}): RoutingProfile => {
  const profile = JSON.stringify(obj);
  return { profile, onlyAdd: false, onlyGroups: false, groupIds: [], info: infoOf(profile), ...o } as RoutingProfile;
};

const family = {
  Name: "Для семьи", GlobalProxy: "true", RemoteDNSType: "DoH", RemoteDNSDomain: "https://dns.example.com/dns-query", RemoteDNSIP: "192.0.2.53", DomesticDNSType: "DoU", DomesticDNSIP: "192.0.2.54", LastUpdated: "1763500000",
  DirectSites: ["geosite:category-ru", "domain:example.ru", "domain:example.su"], DirectIp: ["geoip:ru", "192.0.2.0/24"], ProxySites: ["domain:example.org"], ProxyIp: [], BlockSites: ["geosite:category-ads"], BlockIp: [],
  Geoipurl: "https://geo.example.com/geoip.dat", Geositeurl: "https://geo.example.com/geosite.dat", DomainStrategy: "IPIfNonMatch", FakeDNS: "false",
};
const friends = { Name: "Для друзей", GlobalProxy: "false", DirectSites: [], DirectIp: [], ProxySites: ["geosite:netflix", "domain:example.net"], ProxyIp: [], BlockSites: [], BlockIp: [], DomainStrategy: "AsIs" };
/** A profile whose link does not fit the header: hundreds of domains. */
export const bigObj = { Name: "Большой список", GlobalProxy: "true", DirectSites: Array.from({ length: 140 }, (_, i) => `domain:site${i + 1}-example.example.com`), DirectIp: [], ProxySites: [], ProxyIp: [], BlockSites: [], BlockIp: [] };

export const rtHas = [rp(family, { onlyGroups: true, groupIds: ["grp_fm"] }), rp(friends, { onlyGroups: true, groupIds: ["grp_fr"], onlyAdd: true })];
export const rtOne = [rp(family)];
export const rtBody = [rp(bigObj)];
export const rtMax = Array.from({ length: 20 }, (_, i) => rp({ ...friends, Name: `Профиль ${i + 1}` }, i === 0 ? { onlyGroups: true, groupIds: ["grp_fm"] } : i === 1 ? { onlyGroups: true, groupIds: ["grp_fr"] } : { onlyGroups: true, groupIds: [] }));

/** The rows of the form that are not saved: the first profile with another name; and a new empty row. */
export type Draft = { text: string; text0: string; onlyAdd: boolean; onlyGroups: boolean; groupIds: string[]; info0?: RoutingInfo | undefined };
export const saved = (list: RoutingProfile[]): Draft[] => list.map((p) => ({ text: p.profile, text0: p.profile, onlyAdd: p.onlyAdd, onlyGroups: p.onlyGroups, groupIds: [...p.groupIds], info0: p.info }));
export const pasted = (text: string, o: Partial<Draft> = {}): Draft => ({ text, text0: "", onlyAdd: false, onlyGroups: false, groupIds: [], ...o });
export const incyLink = `incy://routing/add/${b64(JSON.stringify({ ...friends, Name: "Из 3x-ui", Odd: 1, Evil: { a: 1 } }))}`;

/** What the panel's reading answers for a pasted text: the entry it would store, with the info and the fields left out, or its refusal. */
export function fakeRoutingPreview({ input }: { input: string }) {
  const o = read(input);
  const dropped = Object.keys(o).filter((k) => !known.includes(k)).sort();
  const kept = Object.fromEntries(Object.entries(o).filter(([k]) => known.includes(k)));
  const profile = JSON.stringify(kept);
  return { profile: { profile, onlyAdd: /\/routing\/add\//i.test(input), onlyGroups: false, groupIds: [], info: infoOf(profile, dropped) } };
}
