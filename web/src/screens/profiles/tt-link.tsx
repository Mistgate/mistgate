import { Notice } from "@/components/ui/notice";
import { useTx } from "@/screens/users/t";
import { getAt, type Settings } from "./schema";

/**
 * What the official TrustTunnel apps get from this profile, under the Mistgate app's path in "What the client sees": a tt://
 * link, its secret masked, with what is true of every such link. A self-signed profile gets none: the official apps need a
 * domain with a public certificate (the Mistgate app trusts the pinned fingerprint and still gets its path).
 */
export function TrustTunnelLink({ settings }: { settings: Settings }) {
  const t = useTx();
  const selfSigned = getAt(settings, ["tls_mode"]) === "self_signed";
  return (
    <div data-testid="tt-link" className="flex flex-col gap-2 border-t border-line pt-3">
      <span className="text-[13px] font-bold">{t("profiles.tt.linkTitle")}</span>
      {selfSigned ? (
        <Notice tone="warn">{t("profiles.tt.selfSigned")}</Notice>
      ) : (
        <>
          <div className="rounded-xl border border-line bg-canvas p-3 font-mono text-[11px] leading-[1.65] break-all">
            <span className="text-muted">tt://?</span>
            <span aria-hidden>••••••••••••••••••••••••</span>
          </div>
          <ul className="flex flex-col gap-1 text-xs leading-snug text-muted">
            <li>{t("profiles.tt.http2")}</li>
            <li>{t("profiles.tt.secret")}</li>
          </ul>
        </>
      )}
    </div>
  );
}
