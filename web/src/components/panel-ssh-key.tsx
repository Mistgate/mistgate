import { useQuery } from "@tanstack/react-query";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { provisioning } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";

/** Where the panel key goes on the server: the login's own authorized_keys. */
export const authorizedKeysFile = "~/.ssh/authorized_keys";

/** The panel's public SSH key: created on the first call, the same for every node; the private half never leaves the panel. */
export function usePanelSSHKey() {
  return useQuery({
    queryKey: ["panel-ssh-key"],
    queryFn: ({ signal }) => provisioning.getPanelSSHKey({}, { signal }),
    staleTime: Infinity,
  });
}

/** True once the panel key logs in to at least one node: then it is the default way in for a new one. */
export function usePanelKeyInUse(enabled = true): boolean {
  const list = useQuery({
    queryKey: ["node-server-access"],
    queryFn: ({ signal }) => provisioning.listNodeServerAccess({}, { signal }),
    staleTime: 60_000,
    enabled,
  });
  return !!list.data?.access.some((a) => a.panelKeyFingerprint !== "");
}

/**
 * The panel key for a login: the key to copy, its fingerprint, and how to get it onto a server. The main way is the
 * hoster's SSH keys (saved to the account, every new server gets it); a running server takes the one-line command. The
 * how-to folds away once the key is in use; a refusal of the key opens it (`howTo`).
 */
export function PanelSSHKey({ howTo, onHowTo, className }: { howTo: boolean; onHowTo: (open: boolean) => void; className?: string }) {
  const t = useT();
  const key = usePanelSSHKey();
  const publicKey = key.data?.publicKey ?? "";
  const command = `mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '${publicKey}' >> ${authorizedKeysFile} && chmod 600 ${authorizedKeysFile}`;

  return (
    <div className={cx("flex flex-col gap-3", className)}>
      <div className="flex flex-col gap-1">
        <SectionLabel as="h4" icon="key" tone="sky">
          {t("panelKey.title")}
        </SectionLabel>
        <p className="text-xs leading-snug text-pretty text-muted">{t("panelKey.body")}</p>
      </div>
      {key.isError ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <p role="alert" className="min-w-0 flex-1 text-xs leading-snug text-pretty text-danger-text">
            {errorText(key.error, t)}
          </p>
          <Button type="button" variant="secondary" size="md" onClick={() => void key.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : (
        <>
          {/* the key breaks at its spaces first, inside the base64 run only where a 390 px screen needs it */}
          <code
            dir="ltr"
            data-testid="panel-key"
            className="block min-h-[68px] rounded-field border border-line bg-surface p-3 font-mono text-xs leading-relaxed wrap-anywhere text-fg select-all"
          >
            {key.isPending ? <span className="text-muted">{t("common.loading")}</span> : publicKey}
          </code>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            {publicKey && <CopyButton value={publicKey} label={t("panelKey.copy")} size="md" />}
            {key.data?.fingerprint && (
              // on a phone the fingerprint takes its own line: beside the button it would be a narrow column of mono
              <span className="min-w-0 flex-1 text-xs leading-snug text-muted max-sm:basis-full">
                {t("panelKey.fingerprint")}{" "}
                <span dir="ltr" className="font-mono wrap-anywhere">
                  {key.data.fingerprint}
                </span>
              </span>
            )}
          </div>
          <details open={howTo} onToggle={(e) => onHowTo(e.currentTarget.open)} className="group border-t border-line pt-3">
            <summary className="flex cursor-pointer list-none items-center gap-1.5 text-xs font-bold text-muted select-none hover:text-fg [&::-webkit-details-marker]:hidden">
              <Icon name="chevronRight" size={12} className="transition-transform duration-200 group-open:rotate-90 rtl:group-open:-rotate-90" />
              {t("panelKey.howTo")}
            </summary>
            <div className="flex flex-col gap-3 pt-3">
              <ol className="flex flex-col gap-2">
                {[t("panelKey.step1"), t("panelKey.step2", { name: "mistgate" }), t("panelKey.step3")].map((line, i) => (
                  <li key={i} className="flex gap-2.5 text-[13px] leading-snug text-pretty">
                    <span className="box-border grid size-5 flex-none place-items-center rounded-full border border-line font-mono text-[11px] font-bold text-muted">
                      {i + 1}
                    </span>
                    <span className="min-w-0 pt-px">{line}</span>
                  </li>
                ))}
              </ol>
              {publicKey && (
                <div className="flex flex-col gap-2">
                  <p className="text-xs leading-snug text-pretty text-muted">{t("panelKey.command")}</p>
                  <pre tabIndex={0} dir="ltr" className="rounded-field border border-line bg-surface p-3 font-mono text-xs leading-relaxed wrap-anywhere whitespace-pre-wrap text-fg select-all">
                    {command}
                  </pre>
                  <div>
                    <CopyButton value={command} label={t("node.add.copyCommand")} size="md" />
                  </div>
                </div>
              )}
            </div>
          </details>
        </>
      )}
    </div>
  );
}
