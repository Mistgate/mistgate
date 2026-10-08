import type { T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";

/** What the node's torrent guard matched ("tracker_connect", "dht_query"...), in words; a code from a newer agent is shown as it is. */
export function torrentEvidenceText(t: T, code: string): string {
  const key = `torrent.evidence.${code}`;
  return Object.hasOwn(en, key) ? t(key as MessageKey) : code;
}
