import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { cx } from "@/lib/cx";
import type { UserN as User } from "./model";
import { useTx } from "./t";
import { ConfirmModal, TypeConfirmModal } from "./ui";
import { useReadonly, type Actions } from "./user-actions";
import { SetHead } from "./user-bits";
import { PagePassword, useSubLink } from "./user-overview";

/** "Reset traffic": asks first (the quota period starts again; the history stays). The page opens it from the banner, the TrustTunnel card and here. */
export function ResetTrafficModal({ user, actions, open, onOpenChange }: { user: User; actions: Actions; open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useTx();
  return (
    <ConfirmModal
      open={open}
      onOpenChange={onOpenChange}
      title={t("users.resetTrafficT")}
      description={t("users.resetTrafficBody", { name: user.name })}
      confirmLabel={t("users.resetTraffic")}
      danger
      onConfirm={() => actions.resetTraffic()}
    />
  );
}

/** "Delete the person": the name is typed first (and the owner's second factor asked, when it is on); then the list. */
export function DeleteModal({ user, actions, open, onOpenChange, onDeleted }: { user: User; actions: Actions; open: boolean; onOpenChange: (o: boolean) => void; onDeleted: () => void }) {
  const t = useTx();
  return (
    <TypeConfirmModal
      open={open}
      onOpenChange={onOpenChange}
      match={user.name}
      title={t("users.deleteT")}
      description={t("users.deleteBody", { name: user.name })}
      confirmLabel={t("users.delete")}
      onConfirm={async () => {
        await actions.remove();
        onDeleted();
      }}
    />
  );
}

/**
 * "Subscription page" (the board's first card of Settings): the name the page greets with, the page's password, and the save bar of the
 * name. An empty name shows the panel's. Saved with a toast; a refusal keeps the draft for a correction.
 */
function PageCard({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const toast = useToast();
  const readonly = useReadonly();
  const link = useSubLink(user);
  const [draft, setDraft] = useState<{ server: string; value: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [ok, setOk] = useState(false);
  const name = draft?.server === user.subscriptionName ? draft.value : user.subscriptionName;
  const trimmed = name.trim();
  const dirty = trimmed !== user.subscriptionName;

  async function save() {
    setBusy(true);
    try {
      await actions.update({ subscriptionName: trimmed });
      toast(t("users.subscriptionNameSaved"));
      setOk(true);
      window.setTimeout(() => setOk(false), 2400);
    } catch {
      // actions.update displays the server error and keeps the draft available for correction.
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="up-card">
      <SetHead title={t("users.subscriptionPage")} line={t("up.pageLine")} />
      <div className="up-pad">
        <label className="up-f">
          <span>{t("users.subscriptionName")}</span>
          <input className="up-i" aria-label={t("users.subscriptionName")} value={name} maxLength={64} autoComplete="off" spellCheck={false} placeholder={user.name} disabled={busy || readonly} onChange={(e) => setDraft({ server: user.subscriptionName, value: e.target.value })} />
          <small>{t("users.subscriptionNamePreview", { name: trimmed || user.name })}</small>
        </label>
        {!readonly && <PagePassword link={link} />}
      </div>
      <div className={cx("up-save", (dirty || ok) && "on", ok && !dirty && "ok")}>
        <span>{dirty ? t("up.nameUnsaved") : t("up.saved")}</span>
        <Button variant="primary" size="md" disabled={busy || !dirty} onClick={() => void save()}>
          {t("users.subscriptionNameSave")}
        </Button>
      </div>
    </section>
  );
}

/** The Settings tab: the page, and the danger zone (reset the traffic, switch the access off or on, delete). */
export function SettingsTab({ user, actions, onAskReset, onAskDelete }: { user: User; actions: Actions; onAskReset: () => void; onAskDelete: () => void }) {
  const t = useTx();
  const readonly = useReadonly();
  const disabled = user.status === UserStatus.DISABLED;
  return (
    <div className="up-one">
      <PageCard user={user} actions={actions} />
      <section className="up-card up-dz">
        <SetHead title={t("users.danger")} />
        <div className="up-sv-row">
          <div>
            <b>{t("users.resetTrafficT")}</b>
            <span>{t("up.resetRowHint")}</span>
          </div>
          <Button size="lg" disabled={readonly || user.usedBytes === 0} onClick={onAskReset}>
            {t("users.resetTraffic")}
          </Button>
        </div>
        <div className="up-sv-row">
          <div>
            <b>{disabled ? t("up.enableRow") : t("up.disableRow")}</b>
            <span>{disabled ? t("up.enableRowHint") : t("up.disableRowHint")}</span>
          </div>
          <Button size="lg" disabled={readonly} onClick={() => void actions.setEnabled(disabled)}>
            {t(disabled ? "users.enable" : "users.disable")}
          </Button>
        </div>
        <div className="up-sv-row">
          <div>
            <b>{t("users.deleteT")}</b>
            <span>{t("users.deleteHint")}</span>
          </div>
          <Button size="lg" className="up-del" disabled={readonly} onClick={onAskDelete}>
            {t("users.delete")}
          </Button>
        </div>
      </section>
    </div>
  );
}
