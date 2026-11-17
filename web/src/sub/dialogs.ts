import { deviceModal, deviceModalLabel } from "./add";
import type { Kid } from "./dom";
import { dnsLabel, dnsModal } from "./servers";
import type { Ctx } from "./state";

// Which window is open over the page, what it holds and how it closes. One function each, so main.ts does not know the
// windows: the page's state (the dns, amz parts) decides.

/** A window is open (the "add a device" window, a key, the new key of a stale device, the DNS choice). */
export const dialogOpen = ({ d, s }: Pick<Ctx, "d" | "s">): boolean => s.dns.open !== null || s.amz.adding || (d.amnezia !== null && (s.amz.renew !== null || s.amz.open !== null));

/** The accessible name and the content of the open window. */
export function dialogView(c: Ctx): { label: string; content: Kid[] } {
  return c.s.dns.open ? { label: dnsLabel(c), content: dnsModal(c) } : { label: deviceModalLabel(c), content: deviceModal(c) };
}

/** Esc or the backdrop: the state follows the window. */
export function dialogClosed({ s, a }: Pick<Ctx, "s" | "a">): void {
  if (s.dns.open) a.dns.open(null);
  else if (s.amz.adding || s.amz.renew || s.amz.open) a.amz.add(false);
}
