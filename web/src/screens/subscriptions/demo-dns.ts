import type { DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import type { DnsServerN, SplitRuleN } from "./model";

/**
 * Development only (the dev kit's scenes, dev-kit-subs-dns.tsx): what the tab "DNS" starts with besides the panel's own answers, for the
 * states that are not saved anywhere (a preset open, a text typed and not saved, a save in flight, the question before a delete, a row
 * just made the default). Empty in the product: every field reads as "not asked".
 */
export type DnsDemo = {
  /** The preset to open at once: its id, or "new". */
  open?: string;
  /** The category chip to start on. */
  cat?: string;
  /** The row that was just made the default (its flash and the name's blink). */
  just?: string;
  /** The note after a change of the default, with the Undo button. */
  nowDefault?: string;
  /** What is typed in the form and not saved yet. */
  draft?: { name?: string; description?: string; servers?: DnsServerN[]; split?: SplitRuleN[]; blockAds?: boolean; splitDirect?: boolean; ipv4Only?: boolean; transport?: DnsTransport };
  /** The catalog panel of the main servers is open. */
  catalog?: boolean;
  /** A save is on its way. */
  busy?: boolean;
  /** The panel's own words about a refused save. */
  fail?: string;
  /** The question before a delete is open. */
  ask?: boolean;
  /** The question "Discard the changes?" (the back button pressed with changes). */
  leave?: boolean;
};

export const dnsDemo: DnsDemo = {};

/** A scene sets what it wants; whatever an earlier scene set is dropped. */
export function setDnsDemo(d: DnsDemo) {
  for (const k of Object.keys(dnsDemo)) delete (dnsDemo as Record<string, unknown>)[k];
  Object.assign(dnsDemo, d);
}
