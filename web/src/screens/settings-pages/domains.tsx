import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { useStepUp, isStepUpCancelled } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { SubscriptionHostState, type CheckSubscriptionHostDomainResponse, type SubscriptionHost } from "@/gen/mistgate/admin/v1/subscription_host_pb";
import { useT } from "@/i18n";
import { subHost } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { instanceQuery } from "@/lib/instance";
import { nodesQuery, subHostQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";

/** A value to copy (an address, a command): mono text in a field-like row, "Copy" on the right. */
export function CopyRow({ value }: { value: string }) {
  return (
    <div className="flex items-center gap-2 rounded-field border border-line bg-canvas py-1.5 pe-1.5 ps-3">
      {/* wraps at a space where it can ("reset-login" stays whole), anywhere only when it must (a long address) */}
      <span className="min-w-0 flex-1 font-mono text-xs leading-snug [overflow-wrap:anywhere] select-all">{value}</span>
      <CopyButton value={value} />
    </div>
  );
}

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{label}</span>
      {children}
    </div>
  );
}

/** Settings -> Domains: where the admin and the subscription links live, and the subscription host that can carry the latter. */
export function DomainsPage() {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const hostable = !useEdition().off("subscription_host");
  // the server fills the addresses for the owner only
  const q = useQuery({ ...instanceQuery, enabled: owner });
  const sh = useQuery({ ...subHostQuery, enabled: owner && hostable });
  const viaHost = !!sh.data && sh.data.state !== SubscriptionHostState.OFF;
  const i = q.data?.instance;
  const [before, after] = t("set.domains.change").split("{cmd}");
  return (
    <>
      <section className="flex flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4">
        <SectionLabel as="h2" icon="globe" tone="sky">
          {t("set.domains.title")}
        </SectionLabel>
        {!owner && <p className="text-[13px] text-muted">{t("set.domains.ownerOnly")}</p>}
        {owner && q.isPending && <Pending compact />}
        {owner && q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
        {i && (
          <>
            <Labeled label={t("set.domains.admin")}>
              <CopyRow value={i.adminUrl} />
            </Labeled>
            <Labeled label={viaHost ? `${t("set.domains.sub")} · ${t("set.domains.subHost.via")}` : t("set.domains.sub")}>
              {i.subscriptionBase ? <CopyRow value={i.subscriptionBase} /> : <p className="text-xs text-muted">{t("set.domains.subNone")}</p>}
            </Labeled>
            <div className="flex flex-col gap-1 border-t border-line pt-3.5 text-xs leading-normal text-pretty text-muted">
              <p>{t("set.domains.secret")}</p>
              <p>
                {before}
                <code className="font-mono font-semibold text-fg">mistgate setup</code>
                {after}
              </p>
            </div>
          </>
        )}
      </section>
      {owner && hostable && (
        <section className="flex flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4">
          <SectionLabel as="h2" icon="link" tone="mint">
            {t("set.domains.subHost.title")}
          </SectionLabel>
          {sh.isPending && <Pending compact />}
          {sh.isError && <QueryError compact error={sh.error} onRetry={() => void sh.refetch()} />}
          {sh.data && <SubHostCard host={sh.data} />}
        </section>
      )}
    </>
  );
}

const codeOf = (e: unknown) => errorCode(ConnectError.from(e).rawMessage);

/** The addresses the domain resolves to and the ones it should. */
function RecordCheck({ r }: { r: CheckSubscriptionHostDomainResponse }) {
  const t = useT();
  return (
    <Notice tone={r.matches ? "quiet" : "warn"}>
      {r.matches ? t("set.domains.subHost.matches") : t("set.domains.subHost.mismatch")}{" "}
      {t("set.domains.subHost.found", { found: r.found.join(", ") || "—" })} {t("set.domains.subHost.expected", { expected: r.expected.join(", ") || "—" })}
    </Notice>
  );
}

function SubHostCard({ host }: { host: SubscriptionHost }) {
  const t = useT();
  const fmt = useFmt();
  const guard = useStepUp();
  const qc = useQueryClient();
  const nodeList = useQuery(nodesQuery).data?.nodes ?? [];
  const nameOf = (id: string) => nodeList.find((n) => n.id === id)?.name ?? id;
  const [node, setNode] = useState("");
  const [domain, setDomain] = useState("");
  const [publicUrl, setPublicUrl] = useState("");
  const [check, setCheck] = useState<CheckSubscriptionHostDomainResponse | null>(null);
  const [failure, setFailure] = useState<{ code: string; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  // the record of a move: does the domain already point at the next node
  const moving = host.state === SubscriptionHostState.MOVING;
  const record = useQuery({
    queryKey: ["subhost", "record", host.nextNodeId, host.domain],
    queryFn: async () => subHost.checkSubscriptionHostDomain({ nodeId: host.nextNodeId, domain: host.domain }),
    enabled: moving && !!host.nextNodeId,
    refetchInterval: 15_000,
  });

  async function run(call: () => Promise<{ host?: SubscriptionHost }>) {
    setBusy(true);
    setFailure(null);
    try {
      const r = await guard(call);
      if (r.host) qc.setQueryData(subHostQuery.queryKey, r.host);
      void qc.invalidateQueries({ queryKey: ["instance"] });
      setCheck(null);
    } catch (e) {
      if (!isStepUpCancelled(e)) setFailure({ code: codeOf(e), text: errorText(e, t) });
    } finally {
      setBusy(false);
    }
  }

  async function checkRecord() {
    setBusy(true);
    setFailure(null);
    try {
      setCheck(await subHost.checkSubscriptionHostDomain({ nodeId: node, domain: domain.trim() }));
    } catch (e) {
      setFailure({ code: codeOf(e), text: errorText(e, t) });
    } finally {
      setBusy(false);
    }
  }

  const options = (except: string[]) => [
    { value: "", label: t("set.domains.subHost.pickNode") },
    ...nodeList.filter((n) => !except.includes(n.id)).map((n) => ({ value: n.id, label: n.name, hint: n.address })),
  ];
  const tunnel = (id: string) => {
    const tun = host.tunnels.find((x) => x.nodeId === id);
    if (!tun) return null;
    const ago = tun.sinceUnix ? fmt.ago(Number(tun.sinceUnix)) : "";
    const state = tun.up
      ? "set.domains.subHost.tunnelUp"
      : tun.downReason === "not_recognised"
        ? "set.domains.subHost.tunnelNotRecognised"
        : "set.domains.subHost.tunnelDown";
    return (
      <li key={id} className="flex items-center justify-between gap-2 text-[13px]">
        <span className="font-semibold">{nameOf(id)}</span>
        <span className={tun.up ? "text-ok-text" : "text-danger-text"}>{t(state, { ago })}</span>
      </li>
    );
  };
  const error = failure && (
    <div className="flex flex-col gap-2">
      <Notice tone="danger">{failure.text}</Notice>
      {failure.code === "sub_host_nodes_lack_link" && host.lackingLink.length > 0 && (
        <ul className="list-disc ps-5 text-xs text-muted">
          {host.lackingLink.map((n) => (
            <li key={n.nodeId}>{t("set.domains.subHost.updateAgent", { node: n.name || n.nodeId })}</li>
          ))}
        </ul>
      )}
    </div>
  );

  if (host.state === SubscriptionHostState.OFF || host.state === SubscriptionHostState.UNSPECIFIED) {
    const ready = !!node && !!domain.trim();
    return (
      <div className="flex flex-col gap-3">
        <p className="text-xs leading-normal text-pretty text-muted">{t("set.domains.subHost.intro")}</p>
        <Labeled label={t("set.domains.subHost.node")}>
          <Select value={node} onValueChange={setNode} options={options([])} aria-label={t("set.domains.subHost.node")} />
        </Labeled>
        <TextField label={t("set.domains.subHost.domain")} hint={t("set.domains.subHost.domainHint")} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="sub.example.com" mono maxLength={253} autoComplete="off" />
        {check && <RecordCheck r={check} />}
        {error}
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" disabled={!ready || busy} onClick={() => void checkRecord()}>
            {t("set.domains.subHost.check")}
          </Button>
          <Button variant="primary" disabled={!ready || busy} onClick={() => void run(() => subHost.setSubscriptionHost({ nodeId: node, domain: domain.trim() }))}>
            {t("set.domains.subHost.enable")}
          </Button>
          {failure?.code === "sub_host_dns_mismatch" && (
            <Button variant="outline" disabled={!ready || busy} onClick={() => void run(() => subHost.setSubscriptionHost({ nodeId: node, domain: domain.trim(), skipDnsCheck: true }))}>
              {t("set.domains.subHost.saveAnyway")}
            </Button>
          )}
        </div>
      </div>
    );
  }

  const head = (
    <div className="flex flex-col gap-2">
      <Labeled label={t("set.domains.subHost.domain")}>
        <CopyRow value={host.domain} />
      </Labeled>
      <ul className="flex flex-col gap-1">
        {tunnel(host.nodeId)}
        {moving && host.nextNodeId && tunnel(host.nextNodeId)}
      </ul>
    </div>
  );

  if (host.state === SubscriptionHostState.ON) {
    return (
      <div className="flex flex-col gap-3">
        <p className="text-[13px]">{t("set.domains.subHost.onNode", { node: nameOf(host.nodeId) })}</p>
        {head}
        {host.caaRecord && (
          <Labeled label={t("set.domains.subHost.caa")}>
            <CopyRow value={host.caaRecord} />
            <span className="text-xs text-muted">{t(`set.domains.subHost.caa.${(["present", "absent", "account_pending"].includes(host.caaStatus) ? host.caaStatus : "unknown") as "present"}`)}</span>
          </Labeled>
        )}
        {error}
        <div className="flex flex-col gap-2 border-t border-line pt-3.5">
          <Labeled label={t("set.domains.subHost.moveTo")}>
            <Select value={node === host.nodeId ? "" : node} onValueChange={setNode} options={options([host.nodeId])} aria-label={t("set.domains.subHost.moveTo")} />
          </Labeled>
          <Button variant="secondary" className="self-start" disabled={!node || node === host.nodeId || busy} onClick={() => void run(() => subHost.startSubscriptionHostMove({ nodeId: node }))}>
            {t("set.domains.subHost.move")}
          </Button>
        </div>
        <div className="flex flex-col gap-2 border-t border-line pt-3.5">
          <TextField label={t("set.domains.subHost.publicUrl")} hint={t("set.domains.subHost.publicUrlHint")} value={publicUrl} onChange={(e) => setPublicUrl(e.target.value)} placeholder="https://panel.example.com" mono maxLength={300} autoComplete="off" />
          <Button variant="danger" className="self-start" disabled={busy} onClick={() => void run(() => subHost.disableSubscriptionHost({ publicUrl: publicUrl.trim() }))}>
            {t("set.domains.subHost.turnOff")}
          </Button>
        </div>
      </div>
    );
  }

  const finish = (force: boolean) => run(() => subHost.finishSubscriptionHostChange({ force }));
  const actions = (
    <div className="flex flex-wrap gap-2">
      <Button variant="primary" disabled={busy} onClick={() => void finish(false)}>
        {t("set.domains.subHost.finish")}
      </Button>
      <Button variant="secondary" disabled={busy} onClick={() => void run(() => subHost.cancelSubscriptionHostChange({}))}>
        {t("set.domains.subHost.cancel")}
      </Button>
    </div>
  );

  if (moving) {
    return (
      <div className="flex flex-col gap-3">
        <p className="text-[13px]">{t("set.domains.subHost.moving", { from: nameOf(host.nodeId), to: nameOf(host.nextNodeId) })}</p>
        {head}
        <p className="text-xs leading-normal text-pretty text-muted">{t("set.domains.subHost.changeRecord", { address: nodeList.find((n) => n.id === host.nextNodeId)?.address ?? "" })}</p>
        {record.data && <RecordCheck r={record.data} />}
        {error}
        {actions}
      </div>
    );
  }

  // leaving: the panel is back on its own address and waits for the nodes to follow
  return (
    <div className="flex flex-col gap-3">
      <p className="text-[13px]">{t("set.domains.subHost.leaving", { url: host.publicUrl || "—" })}</p>
      {head}
      <Labeled label={t("set.domains.subHost.pending")}>
        {host.pending.length === 0 ? (
          <p className="text-xs text-muted">{t("set.domains.subHost.pendingNone")}</p>
        ) : (
          <ul className="flex flex-col gap-1 text-[13px]">
            {host.pending.map((p) => (
              <li key={p.nodeId} className="flex justify-between gap-2">
                <span className="font-semibold">{p.name || p.nodeId}</span>
                <span className="font-mono text-xs text-muted">{p.via}</span>
              </li>
            ))}
          </ul>
        )}
      </Labeled>
      {error}
      {failure?.code === "sub_host_nodes_pending" && (
        <Notice tone="warn">
          {t("set.domains.subHost.forceAsk")}{" "}
          <Button variant="danger" size="sm" disabled={busy} onClick={() => void finish(true)}>
            {t("set.domains.subHost.forceYes")}
          </Button>
        </Notice>
      )}
      {actions}
    </div>
  );
}
