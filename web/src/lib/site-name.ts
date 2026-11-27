// The site name a node already has: its address when that is a host name (not an IP, not the subdomain itself), "" when it has
// none. The subscription host's forms put it in rather than asking again (#55); it is harmless when an inbound on 443 serves
// the node's site, as the host then ignores it. internal/panel/mcp siteNameOf is the same rule.
const hostName = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

export function siteNameOf(address: string, domain: string): string {
  const a = address.trim().toLowerCase().replace(/\.$/, "");
  const d = domain.trim().toLowerCase().replace(/\.$/, "");
  if (!hostName.test(a) || /^[0-9.]+$/.test(a) || a === d) return "";
  return a;
}
