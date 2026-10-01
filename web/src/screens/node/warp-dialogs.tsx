import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { warp as warpApi } from "@/lib/api";
import { looksLikeWgcfProfile, safeHttpUrl } from "@/lib/warp";
import { useTx } from "@/screens/users/t";
import { Check, TypeConfirmModal } from "@/screens/users/ui";
import { warpError } from "./warp-model";

/** What a change of the WARP account refreshes: the card and the node (its badge, its profiles' state). */
function useRefresh(nodeId: string) {
  const qc = useQueryClient();
  return () => Promise.all([qc.invalidateQueries({ queryKey: ["warp", nodeId] }), qc.invalidateQueries({ queryKey: ["node", nodeId] }), qc.invalidateQueries({ queryKey: ["nodes"] })]);
}

/**
 * "Enable WARP": the terms link, what the click does in the owner's name, a checkbox that must be ticked, then the
 * step-up. The panel registers an anonymous device account with Cloudflare from its own address, accepts the terms
 * on the owner's behalf and stores the account for this node. `again`: Cloudflare revoked the node's account, and the
 * new one replaces it in the same step (the server deletes the old device; nothing is left half done).
 */
export function RegisterWarpDialog({
  open,
  onOpenChange,
  nodeId,
  tosUrl,
  onImport,
  again = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodeId: string;
  tosUrl: string;
  onImport: () => void;
  again?: boolean;
}) {
  const t = useTx();
  const toast = useToast();
  const guard = useStepUp();
  const refresh = useRefresh(nodeId);
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ text: string; rateLimited: boolean } | null>(null);
  const url = safeHttpUrl(tosUrl);

  async function register() {
    setBusy(true);
    setError(null);
    try {
      await guard(() => warpApi.registerWarp({ nodeId, acceptTos: true, tosUrlShown: tosUrl, replaceExisting: again }));
      await refresh();
      onOpenChange(false);
      setAccepted(false);
      toast(t(again ? "warp.reregistered" : "warp.registered"));
    } catch (e) {
      if (isStepUpCancelled(e)) return;
      // the window stays open with the reason above its buttons
      setError({ text: warpError(e, t), rateLimited: ConnectError.from(e).code === Code.ResourceExhausted });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!busy) onOpenChange(o);
        if (!o) setError(null);
      }}
      title={t(again ? "warp.reg.titleAgain" : "warp.reg.title")}
      description={t(again ? "warp.reg.bodyAgain" : "warp.reg.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={!accepted || busy} onClick={() => void register()}>
            {busy ? t("warp.reg.busy") : t(again ? "warp.reregister" : "warp.reg.do")}
          </Button>
        </>
      }
    >
      <ul className="flex flex-col gap-2">
        {(["warp.reg.l1", "warp.reg.l2", "warp.reg.l3"] as const).map((k) => (
          <li key={k} className="flex gap-2 text-[13px] leading-snug">
            <span aria-hidden className="font-extrabold text-accent-text">
              ·
            </span>
            <span className="text-pretty">{t(k)}</span>
          </li>
        ))}
      </ul>
      {url ? (
        <a href={url} target="_blank" rel="noopener noreferrer" className="w-fit text-[13px] font-bold text-accent-text underline decoration-dotted underline-offset-2">
          {t("warp.tosLink")}
        </a>
      ) : (
        <p className="text-xs text-muted">{t("warp.tosNone")}</p>
      )}
      <label className="flex cursor-pointer items-start gap-3 rounded-field border border-line bg-canvas p-3">
        <Check checked={accepted} onCheckedChange={setAccepted} label={t("warp.reg.accept")} />
        <span className="text-[13px] leading-snug text-pretty">{t("warp.reg.accept")}</span>
      </label>
      {error && (
        <Notice tone="danger">
          {error.text}
          {error.rateLimited && (
            <Button
              className="ml-3"
              size="sm"
              onClick={() => {
                onOpenChange(false);
                onImport();
              }}
            >
              {t("warp.import")}
            </Button>
          )}
        </Notice>
      )}
    </Modal>
  );
}

const area = "min-h-[110px] w-full resize-y rounded-field border border-line bg-surface p-3 font-mono text-xs leading-relaxed text-fg outline-none transition-colors duration-200 focus:border-accent";

/** A text box that also takes a file: the wgcf files are tiny, and on a phone the owner may paste instead. */
function FileText({ label, hint, value, onChange, accept, invalid }: { label: string; hint: string; value: string; onChange: (v: string) => void; accept: string; invalid?: string }) {
  const t = useTx();
  const input = useRef<HTMLInputElement>(null);
  async function pick(file: File | undefined) {
    if (file && file.size <= 64 * 1024) onChange(await file.text());
  }
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2">
        <span className="flex-1 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{label}</span>
        <Button variant="secondary" size="xs" onClick={() => input.current?.click()}>
          {t("warp.imp.file")}
        </Button>
        <input ref={input} type="file" accept={accept} hidden onChange={(e) => void pick(e.target.files?.[0])} />
      </div>
      <textarea aria-label={label} spellCheck={false} autoComplete="off" autoCapitalize="off" value={value} onChange={(e) => onChange(e.target.value)} className={area} aria-invalid={!!invalid || undefined} />
      <span className={invalid ? "text-xs text-danger-text" : "text-xs leading-snug text-muted"}>{invalid || hint}</span>
    </div>
  );
}

/** The fallback when Cloudflare refuses a registration from the panel: an account made with wgcf elsewhere. */
export function ImportWarpDialog({ open, onOpenChange, nodeId }: { open: boolean; onOpenChange: (open: boolean) => void; nodeId: string }) {
  const t = useTx();
  const toast = useToast();
  const guard = useStepUp();
  const refresh = useRefresh(nodeId);
  const [conf, setConf] = useState("");
  const [toml, setToml] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const bad = conf.trim() !== "" && !looksLikeWgcfProfile(conf);

  async function submit() {
    setBusy(true);
    setError("");
    try {
      await guard(() => warpApi.importWarp({ nodeId, profileConf: conf, accountToml: toml }));
      await refresh();
      onOpenChange(false);
      setConf("");
      setToml("");
      toast(t("warp.imported"));
    } catch (e) {
      if (!isStepUpCancelled(e)) setError(warpError(e, t));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !busy && onOpenChange(o)}
      title={t("warp.imp.title")}
      description={t("warp.imp.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={busy || conf.trim() === "" || bad} onClick={() => void submit()}>
            {t("warp.imp.do")}
          </Button>
        </>
      }
    >
      <FileText label="wgcf-profile.conf" hint={t("warp.imp.confHint")} value={conf} onChange={setConf} accept=".conf,text/plain" invalid={bad ? t("warp.imp.notProfile") : undefined} />
      <FileText label="wgcf-account.toml" hint={t("warp.imp.tomlHint")} value={toml} onChange={setToml} accept=".toml,text/plain" />
      {error && <Notice tone="danger">{error}</Notice>}
    </Modal>
  );
}

/** Who loses the connection when WARP pauses: the node's WARP-exit profiles by name and how many are connected through them. */
export function pauseConsequence(t: ReturnType<typeof useTx>, nodeName: string, inbounds: readonly { profileName: string; online: number }[]): string {
  if (inbounds.length === 0) return t("warp.pause.none", { node: nodeName });
  const online = inbounds.reduce((n, i) => n + i.online, 0);
  const idle = online === 0;
  if (inbounds.length === 1) return t(idle ? "warp.pause.oneIdle" : "warp.pause.one", { profile: inbounds[0]!.profileName, online: t.n("warp.pause.online", online) });
  const profiles = inbounds.map((i) => `«${i.profileName}»`).join(", ");
  return t(idle ? "warp.pause.manyIdle" : "warp.pause.many", { profiles, online: t.n("warp.pause.online", online) });
}

/**
 * "Pause" is not harmless: the WARP-exit profiles stop passing traffic (they never leave directly), so it asks first and
 * says who is affected now. A failure stays in the window, above its buttons.
 */
export function PauseWarpDialog({
  open,
  onOpenChange,
  nodeId,
  nodeName,
  inbounds,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodeId: string;
  nodeName: string;
  inbounds: readonly { profileName: string; online: number }[];
}) {
  const t = useTx();
  const toast = useToast();
  const guard = useStepUp();
  const refresh = useRefresh(nodeId);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function pause() {
    setBusy(true);
    setError("");
    try {
      await guard(() => warpApi.setWarpEnabled({ nodeId, enabled: false }));
      await refresh();
      onOpenChange(false);
      toast(t("warp.paused"));
    } catch (e) {
      if (!isStepUpCancelled(e)) setError(warpError(e, t));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!busy) onOpenChange(o);
        if (!o) setError("");
      }}
      title={t("warp.pause.title", { node: nodeName })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={() => void pause()}>
            {t("warp.pause.do")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-normal text-pretty">{pauseConsequence(t, nodeName, inbounds)}</p>
      {error && <Notice tone="danger">{error}</Notice>}
    </Modal>
  );
}

/** Removing the account: the node loses WARP and its "warp" profiles stop (they never leave directly). The node name is typed. */
export function DeleteWarpDialog({ open, onOpenChange, nodeId, nodeName, hasToken }: { open: boolean; onOpenChange: (open: boolean) => void; nodeId: string; nodeName: string; hasToken: boolean }) {
  const t = useTx();
  const toast = useToast();
  const guard = useStepUp();
  const refresh = useRefresh(nodeId);
  return (
    <TypeConfirmModal
      open={open}
      onOpenChange={onOpenChange}
      match={nodeName}
      title={t("warp.del.title")}
      description={t("warp.del.body")}
      confirmLabel={t("warp.delete")}
      onConfirm={async () => {
        try {
          const res = await guard(() => warpApi.deleteWarp({ nodeId, confirmName: nodeName }));
          await refresh();
          toast(res.remoteDeleted || !hasToken ? t("warp.deleted") : t("warp.deletedLocal"));
        } catch (e) {
          if (!isStepUpCancelled(e)) toast.error(warpError(e, t));
          throw e;
        }
      }}
    >
      <Notice>{hasToken ? t("warp.del.remote") : t("warp.del.local")}</Notice>
    </TypeConfirmModal>
  );
}
