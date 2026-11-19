import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { DnsHint } from "@/screens/node/share-fix";
import { shareName, validShareName } from "@/lib/tcpshare";
import type { RowShare, RowShared } from "./share-row";

/**
 * What a node of "Put on nodes" (and the wizard's "Which nodes") offers under its row when the name its port is shared under is held:
 * a field with the panel's suggestion, the button that puts the profile beside the other one under that name (the row's check runs
 * again with it), the DNS record the name needs, and the free port the old answer would take. Without an offer (the name is not the
 * profile's to change), only the free port, and for an old agent the link to the updates, which the row carries itself.
 */
export function ShareRowFix({ share, node, inputClass, onName, onPort }: { share: RowShare; node: { name: string; address: string }; inputClass: string; onName: (name: string) => void; onPort: (port: string) => void }) {
  const t = useT();
  const [typed, setTyped] = useState<string | null>(null);
  const offer = share.offer;
  const name = typed ?? offer?.suggest ?? "";
  const ok = validShareName(name);
  const take = share.free && (
    <Button size="md" onClick={() => onPort(share.free)}>
      {t("node.check.port.take", { port: share.free })}
    </Button>
  );
  if (!offer) return take ? <div className="flex flex-wrap gap-2">{take}</div> : null;
  return (
    <div data-testid="share-row-fix" className="flex min-w-0 flex-1 flex-col gap-2.5">
      <p className="text-xs leading-snug text-pretty text-sec">{t(offer.suggest ? "share.body" : "share.bodyIp", { profile: offer.profile, port: offer.port, node: node.name, held: offer.held })}</p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          className={`${inputClass} mono min-w-[12rem] flex-1`}
          type="text"
          value={name}
          placeholder="cdn.example.com"
          aria-label={t("share.name", { node: node.name })}
          aria-invalid={typed !== null && !!typed.trim() && !ok}
          autoCapitalize="off"
          spellCheck={false}
          onChange={(e) => setTyped(e.target.value.trim())}
          onKeyDown={(e) => e.key === "Enter" && ok && onName(shareName(name))}
        />
        <Button size="md" variant="primary" disabled={!ok} onClick={() => onName(shareName(name))}>
          {t("share.put", { port: offer.port })}
        </Button>
        {take}
      </div>
      {typed !== null && !!typed.trim() && !ok && <small className="text-xs text-danger-text">{t("share.nameBad")}</small>}
      {ok && <DnsHint name={shareName(name)} nodeAddress={node.address} />}
    </div>
  );
}

/** What a passing check says of the shared port under the node's row: who restarts, which site a stranger sees, and the name's DNS record. */
export function RowSharedNote({ shared, node }: { shared: RowShared; node: { address: string } }) {
  return (
    <div data-testid="row-shared" className="flex min-w-0 flex-1 flex-col gap-2.5">
      <small className="text-xs leading-snug text-pretty text-sec">{shared.text}</small>
      {shared.name && <DnsHint name={shared.name} nodeAddress={node.address} />}
    </div>
  );
}
