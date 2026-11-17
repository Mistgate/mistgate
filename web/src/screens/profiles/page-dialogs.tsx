import type { Dlg, TabCtx } from "./page-types";
import { PutDialog } from "./put-dialog";
import { TakeDialog } from "./take-dialog";
import { TwinDialog } from "./twin-dialog";

/**
 * The windows of the page (the board's ppDlg): put on nodes, take off, copy through WARP / without it, delete. Each is mounted only while it
 * is open, so it starts clean every time. Only the owner changes what runs on the nodes (policy.go), so a window never opens for anyone else.
 */
export function PageDialogs({ ctx, dlg, onClose, onGone }: { ctx: TabCtx; dlg: Dlg; onClose: () => void; onGone: () => void }) {
  if (!ctx.owner || dlg === "") return null;
  const v = ctx.variant.split(".");
  // a scene may start the run at once on the nodes it names (dev kit); a real page has no variant
  const preselect = v.find((x) => x.startsWith("on-"))?.slice(3).split("+");
  if (dlg === "put") return <PutDialog ctx={ctx} onClose={onClose} preselect={preselect} autorun={v.includes("run")} />;
  if (dlg === "twin") return <TwinDialog ctx={ctx} onClose={onClose} />;
  return <TakeDialog ctx={ctx} mode={dlg === "del" ? "del" : "take"} onClose={onClose} onGone={onGone} />;
}
