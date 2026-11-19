import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button, buttonClass } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import { shareRefusalText } from "@/lib/errors";
import { sharedNote } from "@/lib/port-check";
import { dnsLine, dnsRecord, shareFreePort, shareName, shareOffer, validShareName, type ShareCode } from "@/lib/tcpshare";
import type { InboundCheck } from "./inbound-check";

// What the dialogs that put a profile on a node, or change its port or name, say and offer when the node's port is held and may be shared
// (lib/tcpshare.ts): the name the profile answers to there, the DNS record that name needs, and the way out the old answer had (a free
// port). The sentence and the buttons show before the click on "Save"; the check runs again on the name that is put in.

/** The DNS record a name needs to lead to the node, in the words of a DNS panel, with a button that copies it. */
export function DnsHint({ name, nodeAddress, className }: { name: string; nodeAddress: string; className?: string }) {
  const t = useT();
  const record = dnsRecord(name, nodeAddress);
  if (!record) return null;
  return (
    <div data-testid="dns-hint" className={`screen-enter flex flex-col gap-2 rounded-field border border-line bg-step px-3 py-2.5 ${className ?? ""}`}>
      <div className="flex items-center gap-2">
        <span className="me-auto text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("share.dns.title")}</span>
        <CopyButton value={dnsLine(record)} label={t("share.dns.copy")} size="sm" />
      </div>
      <dl className="m-0 grid grid-cols-[3.5rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-[13px]">
        <dt className="text-muted">{t("share.dns.type")}</dt>
        <dd className="m-0 font-mono font-medium">{record.type}</dd>
        <dt className="text-muted">{t("share.dns.name")}</dt>
        <dd className="m-0 font-mono break-all">{record.name}</dd>
        <dt className="text-muted">{t("share.dns.value")}</dt>
        <dd className="m-0 font-mono break-all">{record.value}</dd>
      </dl>
      <p className="text-xs leading-snug text-pretty text-muted">{t(record.type === "CNAME" ? "share.dns.hintCname" : "share.dns.hint")}</p>
    </div>
  );
}

/**
 * The refusal of a shared TCP port, worded, with what the admin can do about it. A name that is held (sni_taken) of a service that may
 * be renamed offers to put it beside the other one under a name of its own: a field (the panel's suggestion in it), its DNS record, and
 * the button that puts the name into the dialog's domain field, which runs the check again. Every refusal that has a free port also
 * offers it ("Take 8443"); an agent that is too old points at the updates.
 */
export function ShareFix({
  code,
  vars,
  node,
  onPut,
  onTake,
  onLeave,
}: {
  code: ShareCode;
  vars: Record<string, string>;
  node: { name: string; address: string };
  /** The name the admin chose: the dialog sets its domain field to it, and the check goes again. */
  onPut: (name: string) => void;
  onTake: (port: number) => void;
  onLeave?: () => void;
}) {
  const t = useT();
  const offer = shareOffer(code, vars);
  const free = shareFreePort(vars);
  const [typed, setTyped] = useState<string | null>(null);
  const name = typed ?? offer?.suggest ?? "";
  const bad = typed !== null && !!typed.trim() && !validShareName(typed);
  const ok = validShareName(name);
  const take = free > 0 && (
    <Button variant="secondary" size="sm" onClick={() => onTake(free)}>
      {t("node.check.port.take", { port: free })}
    </Button>
  );
  if (!offer) {
    return (
      <Notice tone="danger" className="items-start">
        <span className="flex flex-col items-start gap-2" data-testid="share-refusal">
          {shareRefusalText(t, code, { node: node.name, ...vars })}
          {(take || code === "share_agent_old") && (
            <span className="flex flex-wrap items-center gap-2">
              {code === "share_agent_old" && (
                <Link to="/updates" onClick={onLeave} className={buttonClass("secondary", "sm")}>
                  {t("share.openUpdates")}
                </Link>
              )}
              {take}
            </span>
          )}
        </span>
      </Notice>
    );
  }
  return (
    <section data-testid="share-offer" className="screen-enter flex flex-col gap-3 rounded-field border border-warn-line bg-warn-soft px-3.5 py-3">
      <div className="flex flex-col gap-1">
        <b className="text-[13px] leading-snug">{t("share.title", { profile: offer.profile, port: offer.port })}</b>
        <p className="text-xs leading-snug text-pretty">{t(offer.suggest ? "share.body" : "share.bodyIp", { profile: offer.profile, port: offer.port, node: node.name, held: offer.held })}</p>
      </div>
      <TextField
        label={t("share.name", { node: node.name })}
        value={name}
        onChange={(e) => setTyped(e.target.value)}
        placeholder="cdn.example.com"
        mono
        autoCapitalize="off"
        spellCheck={false}
        error={bad ? t("share.nameBad") : undefined}
      />
      {ok && <p className="text-xs leading-snug text-muted">{t("share.dnsNext")}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="primary" size="sm" disabled={!ok} onClick={() => onPut(shareName(name))}>
          {t("share.put", { port: offer.port })}
        </Button>
        {take}
      </div>
    </section>
  );
}

/**
 * What a check that passed has to say about the port the profile stands on before the click: it shares the port (who restarts, the
 * name it answers to and the DNS record that name needs) or one of the services shows another site. Nothing when the port is its own.
 */
export function ShareNotes({ check, node }: { check: InboundCheck; node: { name: string; address: string } }) {
  const t = useT();
  if (check.state !== "ok") return null;
  const shared = check.warnings.find((w) => w.code === "tcp_port_shared");
  const faces = check.warnings.filter((w) => w.code === "share_two_faces");
  if (!shared && faces.length === 0) return null;
  const name = check.inbound?.tlsServerName ?? "";
  const own = !name || shareName(name) === shareName(node.address);
  return (
    <div data-testid="share-notes" className="flex flex-col gap-2">
      {shared && (
        <Notice className="items-start">
          <span className="flex flex-col items-start gap-2">
            {sharedNote(t, shared)}
            {!own && <span className="text-xs text-sec">{t("share.answersTo", { name })}</span>}
          </span>
        </Notice>
      )}
      {faces.map((w) => (
        <Notice key={w.params.profile} className="items-start">
          {sharedNote(t, w)}
        </Notice>
      ))}
      {shared && !own && <DnsHint name={name} nodeAddress={node.address} />}
    </div>
  );
}
