import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { PagePasswordAction } from "@/gen/mistgate/admin/v1/user_pb";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useTx } from "./t";
import { G, P } from "./user-bits";

/** What can be done to the password of a person's page. */
export type PwAction = "change" | "remove" | "restore";
/** The state after an action: the person's password was removed (the page opens on the link alone), and the new password ("" when there is none to show). */
export type PwNow = { open: boolean; password: string };

const wire = { change: PagePasswordAction.CHANGE, remove: PagePasswordAction.REMOVE, restore: PagePasswordAction.RESTORE } as const;

/**
 * Changes, removes or brings back the page password of one person. The password is computed from the link and a small number the panel
 * keeps, so the update carries none back: after it the new one is read (GetSubscriptionLink, never cached). Every list of people and every
 * link read is refreshed, so the row, the person's page and the send window agree.
 */
export function usePagePasswordAction(userId: string) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (action: PwAction): Promise<PwNow & { action: PwAction }> => {
      const r = await users.updateUser({ userId, pagePasswordAction: wire[action] });
      const l = await users.getSubscriptionLink({ userId });
      return { action, open: r.user?.pageOpen ?? false, password: l.pagePassword };
    },
    onSuccess: (r) => {
      toast(t(r.action === "change" ? "ppw.doneChange" : r.action === "remove" ? "ppw.doneRemove" : "ppw.doneRestore"));
      void qc.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
}

/**
 * The page password of a person (the board's .us-pw) in every state it has:
 *  - with a password: hidden dots, "Show", "Change" and "Remove" (each asks first, in the block);
 *  - just changed (or brought back): the new password shown and copyable, with "the old one no longer works";
 *  - removed: "not set, anyone with the link can open the page" and "Bring the password back";
 *  - the instance asks for none (the setting is off for every page): said so, with the way to the setting.
 * `open` is the person's own state (their password was removed); `password` is what the panel gives for their link ("" when the instance
 * asks for none or the password was removed); `ready` is false while it is being read. `stacked` puts the buttons under the text (a narrow
 * card); `revealed` starts with the password shown. The state after an action is kept here until the props catch up, and also given to `onDone` (the send window keeps its own copy).
 */
export function PagePasswordBlock({
  userId,
  open,
  password,
  ready = true,
  readonly,
  stacked,
  revealed,
  onDone,
  className,
}: {
  userId: string;
  open: boolean;
  password: string;
  ready?: boolean;
  readonly?: boolean;
  stacked?: boolean;
  /** The password starts shown (the send window: the point is to copy it); else it starts as dots. */
  revealed?: boolean;
  onDone?: (r: PwNow) => void;
  className?: string;
}) {
  const t = useTx();
  const run = usePagePasswordAction(userId);
  const [shown, setShown] = useState(!!revealed);
  const [ask, setAsk] = useState<"change" | "remove" | null>(null);
  const [fresh, setFresh] = useState<"change" | "restore" | null>(null);
  // what the last action gave, with the props it was made against. Each prop is read again on its own (the person's row and the link
  // come back at different moments), so until one has changed the block shows what the action gave and never a half-old state.
  const [last, setLast] = useState<{ res: PwNow; from: PwNow } | null>(null);
  const isOpen = last && last.from.open === open ? last.res.open : open;
  const pw = last && last.from.password === password ? last.res.password : password;
  const state = isOpen ? "removed" : pw ? "set" : "off";
  const busy = run.isPending;

  async function act(a: PwAction) {
    setAsk(null);
    try {
      const r = await run.mutateAsync(a);
      setLast({ res: { open: r.open, password: r.password }, from: { open, password } });
      setFresh(a === "remove" ? null : a);
      setShown(a !== "remove");
      onDone?.({ open: r.open, password: r.password });
    } catch {
      // the toast of the mutation says why; the block stays as it was
    }
  }

  const value =
    !ready ? (
      <span className="truncate text-xs leading-4 text-muted">…</span>
    ) : state === "set" ? (
      <code dir="ltr" className="text-start font-mono text-xs leading-4 font-medium break-all text-sec select-all">
        {shown ? pw : "••••••••••••"}
      </code>
    ) : state === "removed" ? (
      <span className="text-xs leading-4 text-muted">{t("up.pwNone")}</span>
    ) : (
      <span className="text-xs leading-4 text-muted">
        {t("ppw.off")}{" "}
        <Link to="/subscriptions" search={{ tab: "page" }} className="text-fg underline decoration-line underline-offset-4 hover:decoration-current">
          {t("ppw.offLink")}
        </Link>
      </span>
    );

  return (
    <div className={cx("grid items-center gap-x-3 gap-y-2 rounded-field border border-line py-2 ps-2.5 pe-2", stacked ? "grid-cols-[28px_minmax(0,1fr)]" : "grid-cols-[28px_minmax(0,1fr)_auto] @max-[520px]/ust:grid-cols-[28px_minmax(0,1fr)]", className)} data-pw={ready ? state : "loading"}>
      <span aria-hidden className={cx("grid size-7 place-items-center rounded-ctl", ready && state === "set" ? "bg-tone-mint-bg text-accent-text" : "bg-step text-sec")}>
        <G d={P.lock} size={16} stroke={1.5} />
      </span>
      <div className="flex min-w-0 flex-col">
        <b className="text-[13px] font-medium">{t("users.pagePassword")}</b>
        {value}
        {ready && state === "set" && fresh && <small className="text-xs leading-4 text-accent-text">{t(fresh === "change" ? "ppw.fresh" : "ppw.freshRestore")}</small>}
      </div>
      {ready && !readonly && state !== "off" && (
        <div className={cx("flex flex-wrap items-center gap-1.5", stacked ? "col-span-full" : "@max-[520px]/ust:col-span-full")}>
          {state === "set" ? (
            <>
              <Button size="md" data-act="show" aria-pressed={shown} disabled={busy} onClick={() => setShown(!shown)}>
                {shown ? t("up.pwHide") : t("up.pwShow")}
              </Button>
              {shown && <CopyButton iconOnly value={pw} size="md" label={t("users.copyPassword")} />}
              <Button size="md" data-act="change" disabled={busy} aria-expanded={ask === "change"} onClick={() => setAsk("change")}>
                {t("up.pwChange")}
              </Button>
              <Button size="md" data-act="remove" disabled={busy} aria-expanded={ask === "remove"} className="hover:border-danger! hover:text-danger-text!" onClick={() => setAsk("remove")}>
                {t("ppw.remove")}
              </Button>
            </>
          ) : (
            <Button size="md" data-act="restore" disabled={busy} onClick={() => void act("restore")}>
              {t("ppw.restore")}
            </Button>
          )}
        </div>
      )}
      {ask && <Ask what={ask} busy={busy} onNo={() => setAsk(null)} onYes={() => void act(ask)} />}
    </div>
  );
}

/** "Change the password?" / "Remove the password?" in the block itself, in red, with its own two buttons (like "Replace the link?" of the send window). */
function Ask({ what, busy, onNo, onYes }: { what: "change" | "remove"; busy: boolean; onNo: () => void; onYes: () => void }) {
  const t = useTx();
  const el = useRef<HTMLDivElement>(null);
  useEffect(() => {
    el.current?.scrollIntoView?.({ block: "nearest" });
  }, []);
  return (
    <div ref={el} role="alert" className="col-span-full flex flex-wrap items-center justify-end gap-x-3 gap-y-2 rounded-xl bg-danger-soft p-3">
      <div className="flex min-w-48 flex-1 basis-56 flex-col gap-0.5">
        <b className="text-[13px] font-medium">{t(what === "change" ? "ppw.askChangeT" : "ppw.askRemoveT")}</b>
        <span className="text-xs leading-[17px] text-sec">{t(what === "change" ? "ppw.askChangeBody" : "ppw.askRemoveBody")}</span>
      </div>
      <div className="flex items-center gap-2">
        <Button size="md" data-act="no" disabled={busy} onClick={onNo}>
          {t("users.cancel")}
        </Button>
        <Button size="md" variant="primary" data-act="yes" disabled={busy} className="bg-danger! hover:bg-danger!" onClick={onYes}>
          {t(what === "change" ? "up.pwChange" : "ppw.remove")}
        </Button>
      </div>
    </div>
  );
}
