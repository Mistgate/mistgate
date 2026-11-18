import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { Role, type Passkey } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { auth, webauthnSupported } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { createPasskey } from "@/lib/passkey";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { G } from "@/screens/users/user-bits";
import { CloudflareCard, type CloudflareDemo } from "./cloudflare";
import { PasswordCard, type PasswordDemo } from "./password";
import { ICONS, Pb, Sec, Spin, SxGate } from "./sx-bits";
import { SxWindow, useFreshKeys } from "./sx-dlg";

export const passkeysQuery = queryOptions({ queryKey: ["passkeys"], queryFn: async ({ signal }) => plain(await auth.listPasskeys({}, { signal })) });

/** What a scene says about the page: a window that is open, a passkey that has just been added, and the parts of the cards below. */
export type SecurityDemo = {
  add?: { name?: string; error?: boolean; busy?: boolean };
  remove?: string;
  fresh?: string;
  password?: PasswordDemo;
  cloudflare?: CloudflareDemo;
};

/**
 * Settings -> Security (the board's sxIs_sec): the signed-in admin's passkeys (list, add, remove), the password + code login with the way
 * back for a lost phone, and (owner) the Cloudflare check at sign-in. The passkeys are the page's main call: while it is on its way or has
 * failed the page says so under the strip (SxGate); the password card and the Cloudflare card each wait for their own call.
 */
export function SecurityPage({ demo }: { demo?: SecurityDemo }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(passkeysQuery);
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const [adding, setAdding] = useState(!!demo?.add);
  const [removeId, setRemoveId] = useState<string | null>(demo?.remove ?? null);
  const passkeys = q.data?.passkeys ?? [];
  const removing = passkeys.find((p) => p.id === removeId);
  const fresh = useFreshKeys(q.data ? passkeys.map((p) => p.id) : undefined, demo?.fresh ? [demo.fresh] : []);
  const supported = webauthnSupported();

  return (
    <SxGate pending={q.isPending} error={q.isError} onRetry={() => void q.refetch()}>
      <div className={cx("pf-g pp-grid sb-grid sx-grid", !owner && "sx-solo")}>
        <div className="col">
          <Sec
            tone="mint"
            d={ICONS.passkey}
            title={t("sec.passkeys")}
            aside={
              <span className="ai-hb">
                <Button variant="primary" size="md" disabled={!supported} onClick={() => setAdding(true)}>
                  <G d={ICONS.plus} size={14} stroke={1.6} />
                  {t("sec.add")}
                </Button>
              </span>
            }
          >
            {!supported && (
              <Pb tone="part" className="sx-pbm" title={t("auth.noPasskeyTitle")}>
                {t("sec.noSupport")}
              </Pb>
            )}
            {passkeys.length === 0 ? (
              <p className="sb-emp ai-emp">{t("sec.none")}</p>
            ) : (
              <div className="sx-list">
                {passkeys.map((p) => (
                  <div key={p.id} className={cx("sx-it sx-pk", fresh.has(p.id) && "fresh")}>
                    <span className="ai-mono" aria-hidden>
                      <G d={ICONS.key} size={16} stroke={1.5} />
                    </span>
                    <div>
                      <b>{p.name}</b>
                      <em>
                        {[t("sec.added", { date: fmt.date(p.createdAtUnix) }), p.lastUsedAtUnix ? t("sec.usedAgo", { when: fmt.ago(p.lastUsedAtUnix) }) : t("sec.neverUsed"), p.backedUp ? t("sec.synced") : ""]
                          .filter(Boolean)
                          .join(" · ")}
                      </em>
                    </div>
                    <button type="button" className="an-lk ig-lk" onClick={() => setRemoveId(p.id)}>
                      {t("sec.remove")}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </Sec>
          <PasswordCard demo={demo?.password} />
        </div>
        {owner && (
          <aside className="col pp-as">
            <CloudflareCard demo={demo?.cloudflare} />
          </aside>
        )}
      </div>
      {adding && <AddPasskey demo={demo?.add} onClose={() => setAdding(false)} />}
      {removing && <RemovePasskey passkey={removing} last={passkeys.length === 1} onClose={() => setRemoveId(null)} />}
    </SxGate>
  );
}

/** The window "Add passkey": a name, then the device's own prompt. A name left empty asks for one; the prompt turned down says so plainly and stays. */
function AddPasskey({ demo, onClose }: { demo?: SecurityDemo["add"]; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [name, setName] = useState(demo?.name ?? "");
  const [empty, setEmpty] = useState(!!demo?.error);
  const add = useMutation({
    mutationFn: async () => {
      const begin = await guard(() => auth.beginAddPasskey({ name: name.trim() }));
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
  const busy = add.isPending || !!demo?.busy;
  function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    if (!name.trim()) return setEmpty(true);
    add.mutate();
  }
  return (
    <SxWindow
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("sec.addTitle")}
      shot="passkey-add"
      footer={
        <>
          <Button size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" type="submit" form="sx-pq" className={cx(busy && "sx-busy")} aria-busy={busy || undefined}>
            {busy && <Spin />}
            {busy ? t("sx.sec.waiting") : t("sec.create")}
          </Button>
        </>
      }
    >
      <form id="sx-pq" onSubmit={submit} className="contents">
        <p className="sx-dp">{t("sx.sec.add.body")}</p>
        <label className={cx("an-f", empty && "err")}>
          <span>{t("sec.name")}</span>
          <input
            className="an-i"
            type="text"
            value={name}
            maxLength={64}
            placeholder={t("sx.sec.name.ph")}
            autoComplete="off"
            autoFocus
            aria-invalid={empty || undefined}
            onChange={(e) => {
              setName(e.target.value);
              setEmpty(false);
            }}
          />
          <small>{empty ? t("sx.sec.name.empty") : ""}</small>
        </label>
      </form>
    </SxWindow>
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
    <SxWindow
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("sec.removeTitle", { name: passkey.name })}
      shot="passkey-remove"
      footer={
        <>
          <Button size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" className="sx-dng" disabled={remove.isPending} onClick={() => remove.mutate()}>
            {t("sec.remove")}
          </Button>
        </>
      }
    >
      <p className="sx-dp">{last ? t("sec.removeLast") : t("sec.removeBody")}</p>
    </SxWindow>
  );
}
