import { deviceModal, deviceModalLabel, deviceModalStage } from "./add";
import { confirmHas, confirmWindow, menuHas, menuWindow, renameHas, renameWindow, staleHas, staleWindow, who } from "./dlg-device";
import { dnsHas, dnsLabel, dnsStage, dnsWindow } from "./dlg-dns";
import type { Kid } from "./dom";
import type { Ctx } from "./state";
// after the styles of the windows it builds on (add.ts brings sub-a.css): this one may override them
import "./sub-dlg.css";

// Which window is open over the page, what it holds and how it closes. One function each, so main.ts does not know the
// windows: the page's state (the dns, amz parts) decides. The windows, over each other in this order: "New device" (the add form,
// a key, the new key of a stale device), the question of a stale key, the question before removing a device or replacing its
// key, the name of a device, the menu of a device, the DNS choice (and its result).

type Which = "add" | "stale" | "confirm" | "rename" | "menu" | "dns";

function which(c: Ctx): Which | null {
  const { d, s } = c;
  if (s.amz.adding || (d.amnezia !== null && (s.amz.renew !== null || s.amz.open !== null))) return "add";
  if (staleHas(c)) return "stale";
  if (confirmHas(c)) return "confirm";
  if (renameHas(c)) return "rename";
  if (menuHas(c)) return "menu";
  if (dnsHas(c)) return "dns";
  return null;
}

/** A window is open (the "add a device" window, a key, the new key of a stale device, a device's menu, name or question, the DNS choice). */
export const dialogOpen = (c: Ctx): boolean => which(c) !== null;

/** The accessible name, the content and the stage of the open window (`stage` changes when the window is another one, which plays its entrance). */
export function dialogView(c: Ctx): { label: string; content: Kid[]; stage: string } {
  const { s, t } = c;
  switch (which(c)) {
    case "stale":
      return { label: t.dgStaleT(who(c, s.amz.stale)?.name ?? ""), content: staleWindow(c), stage: `stale:${s.amz.stale}` };
    case "confirm":
      return { label: s.amz.confirm!.kind === "rotate" ? t.rotateKey : t.dgDelete, content: confirmWindow(c), stage: `confirm:${s.amz.confirm!.kind}:${s.amz.confirm!.id}` };
    case "rename":
      return { label: t.rename, content: renameWindow(c), stage: `rename:${s.amz.rename!.id}` };
    case "menu":
      return { label: t.dgMenuAria, content: menuWindow(c), stage: `menu:${s.amz.menu}` };
    case "dns":
      return { label: dnsLabel(c), content: dnsWindow(c), stage: dnsStage(c) };
    default:
      return { label: deviceModalLabel(c), content: deviceModal(c), stage: deviceModalStage(c) };
  }
}

/** Esc or the backdrop: the state follows the window that was open. */
export function dialogClosed(c: Ctx): void {
  const { a } = c;
  switch (which(c)) {
    case "add":
      return a.amz.add(false);
    case "stale":
      return a.amz.stale(null);
    case "confirm":
      return a.amz.ask(null);
    case "rename":
      return a.amz.renameCancel();
    case "menu":
      return a.amz.menu(null);
    case "dns":
      return a.dns.open(null);
  }
}
