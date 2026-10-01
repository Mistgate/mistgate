import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Role, type Passkey } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { auth, webauthnSupported } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { createPasskey } from "@/lib/passkey";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { CloudflareCard } from "./cloudflare";
import { PasswordCard } from "./password";

export const passkeysQuery = queryOptions({ queryKey: ["passkeys"], queryFn: async ({ signal }) => plain(await auth.listPasskeys({}, { signal })) });

/** Settings -> Security: the signed-in admin's passkeys (list, add, remove), the password + code login, and (owner) the Cloudflare check. */
export function SecurityPage() {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(passkeysQuery);
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Plain<Passkey> | null>(null);
  const passkeys = q.data?.passkeys ?? [];
  const supported = webauthnSupported();

  return (
    <>
      <section className="rounded-card-lg border border-line bg-surface px-4 py-1.5">
        <div className="flex items-center gap-2.5 py-2">
          <SectionLabel as="h2" className="flex-1" icon="key" tone="lavender">
            {t("sec.passkeys")}
          </SectionLabel>
          <Button variant="secondary" size="sm" disabled={!supported} onClick={() => setAdding(true)}>
            <Icon name="plus" size={12} />
            {t("sec.add")}
          </Button>
        </div>
        {q.isPending && <Pending compact className="border-t border-line py-3.5" />}
        {q.isError && <QueryError compact className="border-t border-line py-3" error={q.error} onRetry={() => void q.refetch()} />}
        {q.data && passkeys.length === 0 && <p className="border-t border-line py-3.5 text-[13px] text-muted">{t("sec.none")}</p>}
        {passkeys.map((p) => (
          <div key={p.id} className="flex min-h-[54px] items-center gap-3 border-t border-line py-2">
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              <b className="truncate text-[13px]">{p.name}</b>
              <span className="text-[11px] text-pretty text-muted">
                {[
                  t("sec.added", { date: fmt.date(p.createdAtUnix) }),
                  p.lastUsedAtUnix ? t("sec.usedAgo", { when: fmt.ago(p.lastUsedAtUnix) }) : t("sec.neverUsed"),
                  p.backedUp ? t("sec.synced") : "",
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            </div>
            <button type="button" onClick={() => setRemoving(p)} className="text-xs font-bold text-danger-text">
              {t("sec.remove")}
            </button>
          </div>
        ))}
      </section>
      {!supported && <Notice title={t("auth.noPasskeyTitle")}>{t("sec.noSupport")}</Notice>}
      <PasswordCard />
      {owner && <CloudflareCard />}
      {adding && <AddPasskey onClose={() => setAdding(false)} />}
      {removing && <RemovePasskey passkey={removing} last={passkeys.length === 1} onClose={() => setRemoving(null)} />}
    </>
  );
}

function AddPasskey({ onClose }: { onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [name, setName] = useState(t("sec.defaultName"));
  const add = useMutation({
    mutationFn: async () => {
      const begin = await guard(() => auth.beginAddPasskey({ name: name.trim() || t("sec.defaultName") }));
      const credentialJson = await createPasskey(begin.optionsJson);
      return guard(() => auth.finishAddPasskey({ ceremonyId: begin.ceremonyId, credentialJson }));
    },
    onSuccess: () => {
      onClose();
      toast(t("sec.added.toast"));
      void qc.invalidateQueries({ queryKey: ["passkeys"] });
    },
    onError: (e) => {
      if (isStepUpCancelled(e)) return; // the admin backed out of the re-authentication: stay open, say nothing
      // a cancelled prompt is not worth an error toast's worth of alarm: say it plainly and stay open
      const say = e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError") ? toast : toast.error;
      say(e instanceof DOMException && e.name === "InvalidStateError" ? t("err.alreadyRegistered") : errorText(e, t));
    },
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    if (!add.isPending) add.mutate();
  }
  return (
    <Modal open onOpenChange={(o) => !o && !add.isPending && onClose()} title={t("sec.addTitle")} description={t("sec.addBody")}>
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        <TextField label={t("sec.name")} value={name} onChange={(e) => setName(e.target.value)} maxLength={64} autoFocus autoComplete="off" />
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="md" disabled={add.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" size="md" disabled={add.isPending}>
            {add.isPending ? t("auth.waitingPasskey") : t("sec.create")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function RemovePasskey({ passkey, last, onClose }: { passkey: Plain<Passkey>; last: boolean; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const remove = useMutation({
    mutationFn: () => guard(() => auth.removePasskey({ id: passkey.id })),
    onSuccess: () => {
      onClose();
      toast(t("sec.removed"));
      void qc.invalidateQueries({ queryKey: ["passkeys"] });
    },
    onError: (e) => {
      if (isStepUpCancelled(e)) return;
      onClose();
      // the server refuses to remove the only way the admin can sign in
      toast.error(ConnectError.from(e).code === Code.FailedPrecondition ? t("sec.onlyWay") : errorText(e, t));
    },
  });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("sec.removeTitle", { name: passkey.name })}
      description={last ? t("sec.removeLast") : t("sec.removeBody")}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={remove.isPending} onClick={() => remove.mutate()}>
            {t("sec.remove")}
          </Button>
        </>
      }
    />
  );
}
