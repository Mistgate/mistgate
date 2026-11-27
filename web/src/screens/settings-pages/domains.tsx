import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { SubscriptionHostState, type CheckSubscriptionHostDomainResponse, type SubscriptionHost } from "@/gen/mistgate/subhost/v1/subhost_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { subHost } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { errorCode, errorText } from "@/lib/errors";
import { agoOf } from "@/lib/format";
import { instanceQuery } from "@/lib/instance";
import { nodesQuery, subHostQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { siteNameOf } from "@/lib/site-name";
import { useNow } from "@/lib/time";
import { BusyButton, CmdRow, Foot, ICONS, Mono, Pb, Row, Rows, Sec, slots, SxGate } from "./sx-bits";

/**
 * Settings -> Domains (the board's sxIs_dom): where the admin and the subscription links live, and the subscription host that can carry
 * the latter (design/adr/0018-panel-behind-nat.md). `mistgate setup` sets both addresses; a subscription host moves the subscription
 * address onto a node's subdomain once it is ready. The server fills the addresses for the owner only.
 */
export function DomainsPage() {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const hostable = !useEdition().off("subscription_host");
  const q = useQuery({ ...instanceQuery, enabled: owner });
  const sh = useQuery({ ...subHostQuery, enabled: owner && hostable });
  const viaHost = !!sh.data?.ready && sh.data.state !== SubscriptionHostState.OFF;
  const i = q.data?.instance;
  return (
    <SxGate pending={owner && q.isPending} error={owner && q.isError && !i} onRetry={() => void q.refetch()}>
      <div className="sx-one">
        <Sec tone="sky" d={ICONS.globe} title={t("sx.dom")} foot={i ? slots(t("sx.dom.foot"), { cmd: <Mono>mistgate setup</Mono> }) : undefined}>
          {!owner && <p className="sb-emp">{t("set.domains.ownerOnly")}</p>}
          {i && (
            <>
              <Rows>
                <Row label={t("sx.dom.admin")}>
                  <CmdRow value={i.adminUrl} />
                </Row>
                <Row label={t("sx.dom.sub")} hint={viaHost ? t("set.domains.subHost.via") : undefined}>
                  {i.subscriptionBase ? <CmdRow value={i.subscriptionBase} /> : <p className="sx-none">{t("set.domains.subNone")}</p>}
                </Row>
              </Rows>
              <Pb tone="part" className="sx-pbm">
                {t("sx.dom.warn")}
              </Pb>
            </>
          )}
        </Sec>
        {owner && hostable && (
          <Sec tone="mint" d={ICONS.cloud} title={t("set.domains.subHost.title")} data-card="subhost">
            {sh.isError && !sh.data && (
              <Pb tone="bad" action={<BusyButton onClick={() => void sh.refetch()}>{t("sx.err.retry")}</BusyButton>}>
                {errorText(sh.error, t)}
              </Pb>
            )}
            {sh.data && <SubHostCard host={sh.data} />}
          </Sec>
        )}
      </div>
    </SxGate>
  );
}

/** The steps of readiness, in the order they come true (SubscriptionHost.not_ready). */
const steps = ["host_offline", "host_error", "site_cert", "bridge", "panel_cert", "caa"] as const;
const bridgeReasons = ["unreachable", "tls", "pin_mismatch", "refused", "no_verification"];

/** The addresses the names resolve to and the ones they should. */
function RecordCheck({ r }: { r: CheckSubscriptionHostDomainResponse }) {
  const t = useT();
  return (
    <Pb tone={r.matches ? "ok" : "part"} title={r.matches ? t("set.domains.subHost.matches") : t("set.domains.subHost.mismatch")}>
      {t("set.domains.subHost.found", { found: r.found.join(", ") || "—" })} {t("set.domains.subHost.expected", { expected: r.expected.join(", ") || "—" })}
    </Pb>
  );
}

/** What the subscription host is, and what the owner can do with it in that state. Every change asks for a fresh sign-in. */
export function SubHostCard({ host }: { host: SubscriptionHost }) {
  const t = useT();
  const now = Math.floor(useNow(30_000) / 1000);
  const guard = useStepUp();
  const qc = useQueryClient();
  const nodeList = useQuery(nodesQuery).data?.nodes ?? [];
  const nameOf = (id: string) => nodeList.find((n) => n.id === id)?.name ?? id;
  const [node, setNode] = useState("");
  const [domain, setDomain] = useState("");
  const [siteName, setSiteNameRaw] = useState("");
  // the node's own name goes in until the owner types one (#55)
  const [siteTyped, setSiteTyped] = useState(false);
  const setSiteName = (v: string) => {
    setSiteTyped(true);
    setSiteNameRaw(v);
  };
  const pickNode = (id: string, forDomain: string) => {
    setNode(id);
    if (!siteTyped) setSiteNameRaw(siteNameOf(nodeList.find((n) => n.id === id)?.address ?? "", forDomain));
  };
  const [publicUrl, setPublicUrl] = useState("");
  const [check, setCheck] = useState<CheckSubscriptionHostDomainResponse | null>(null);
  const [failure, setFailure] = useState<{ code: string; text: string } | null>(null);
  const [askNoAddress, setAskNoAddress] = useState(false);
  const [askNoCAA, setAskNoCAA] = useState(false);
  const [busy, setBusy] = useState("");

  // during a move: do the names already point at the next node
  const moving = host.state === SubscriptionHostState.MOVING;
  const record = useQuery({
    queryKey: ["subhost", "record", host.nextNodeId, host.domain],
    queryFn: async () => subHost.checkSubscriptionHostDomain({ nodeId: host.nextNodeId, domain: host.domain }),
    enabled: moving && !!host.nextNodeId,
    refetchInterval: 15_000,
  });

  async function run(what: string, call: () => Promise<{ host?: SubscriptionHost }>) {
    setBusy(what);
    setFailure(null);
    try {
      const r = await guard(call);
      if (r.host) qc.setQueryData(subHostQuery.queryKey, r.host);
      void qc.invalidateQueries({ queryKey: ["instance"] });
      setCheck(null);
      setAskNoAddress(false);
    } catch (e) {
      if (!isStepUpCancelled(e)) setFailure({ code: errorCode(ConnectError.from(e).rawMessage), text: errorText(e, t) });
    } finally {
      setBusy("");
    }
  }

  async function checkRecords() {
    setBusy("check");
    setFailure(null);
    try {
      setCheck(await subHost.checkSubscriptionHostDomain({ nodeId: node, domain: domain.trim(), siteName: siteName.trim() }));
    } catch (e) {
      setFailure({ code: errorCode(ConnectError.from(e).rawMessage), text: errorText(e, t) });
    } finally {
      setBusy("");
    }
  }

  const options = (except: string[]) => [
    { value: "", label: t("set.domains.subHost.pickNode") },
    ...nodeList.filter((n) => !except.includes(n.id)).map((n) => ({ value: n.id, label: n.name, hint: n.address })),
  ];
  const bridge = (id: string) => {
    const b = host.bridges.find((x) => x.nodeId === id);
    if (!b) return null;
    const ago = b.sinceUnix ? agoOf(t, Number(b.sinceUnix), now) : "";
    const reason = bridgeReasons.includes(b.downReason)
      ? t(`set.domains.subHost.bridge.${b.downReason}` as MessageKey)
      : t("set.domains.subHost.bridge.other", { reason: b.downReason || "—" });
    return (
      <Row key={id} label={nameOf(id)}>
        <span className={b.up ? "text-ok-text" : "text-danger-text"}>{b.up ? t("set.domains.subHost.bridgeUp", { ago }) : t("set.domains.subHost.bridgeDown", { ago, reason })}</span>
      </Row>
    );
  };
  const error = failure && (
    <Pb tone="bad">
      {failure.text}
      {failure.code === "sub_host_nodes_lack_link" && host.lackingLink.length > 0 && (
        <ul className="list-disc ps-5">
          {host.lackingLink.map((n) => (
            <li key={n.nodeId}>{t("set.domains.subHost.updateAgent", { node: n.name || n.nodeId })}</li>
          ))}
        </ul>
      )}
    </Pb>
  );

  if (host.state === SubscriptionHostState.OFF || host.state === SubscriptionHostState.UNSPECIFIED) {
    const filled = !!node && !!domain.trim();
    const set = (skipDnsCheck: boolean) => run("set", () => subHost.setSubscriptionHost({ nodeId: node, domain: domain.trim(), siteName: siteName.trim(), skipDnsCheck }));
    return (
      <>
        <p className="sb-lead">{t("set.domains.subHost.intro")}</p>
        <Rows>
          <Row label={t("set.domains.subHost.node")}>
            <Select value={node} onValueChange={(v) => pickNode(v, domain)} options={options([])} aria-label={t("set.domains.subHost.node")} />
          </Row>
        </Rows>
        <TextField label={t("set.domains.subHost.domain")} hint={t("set.domains.subHost.domainHint")} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="sub.example.com" mono maxLength={253} autoComplete="off" />
        <TextField label={t("set.domains.subHost.siteName")} hint={t("set.domains.subHost.siteNameHint")} value={siteName} onChange={(e) => setSiteName(e.target.value)} placeholder="www.example.com" mono maxLength={253} autoComplete="off" />
        {check && <RecordCheck r={check} />}
        {error}
        <Foot>
          <BusyButton busy={busy === "check"} disabled={!filled || !!busy} onClick={() => void checkRecords()}>
            {t("set.domains.subHost.check")}
          </BusyButton>
          <BusyButton variant="primary" busy={busy === "set"} disabled={!filled || !!busy} onClick={() => void set(false)}>
            {t("set.domains.subHost.enable")}
          </BusyButton>
          {failure?.code === "sub_host_dns_mismatch" && (
            <BusyButton variant="outline" disabled={!filled || !!busy} onClick={() => void set(true)}>
              {t("set.domains.subHost.saveAnyway")}
            </BusyButton>
          )}
        </Foot>
      </>
    );
  }

  const head = (
    <Rows>
      <Row label={t("set.domains.subHost.domain")}>
        <CmdRow value={host.domain} />
      </Row>
      {host.siteName && (
        <Row label={t("set.domains.subHost.siteName")}>
          <Mono>{host.siteName}</Mono>
        </Row>
      )}
      {bridge(host.nodeId)}
      {moving && host.nextNodeId && bridge(host.nextNodeId)}
    </Rows>
  );

  if (host.state === SubscriptionHostState.ON) {
    const disable = (force: boolean) => run("off", () => subHost.disableSubscriptionHost({ publicUrl: publicUrl.trim(), force }));
    const caaAction = !host.ready && host.notReady.includes("caa") && host.caaStatus !== "account_pending";
    const caaStatus = host.caaStatus === "present" || host.caaStatus === "absent" || host.caaStatus === "account_pending" ? host.caaStatus : "unknown";
    const caaValue = host.caaRecord.match(/ CAA 0 issue "([^"]+)"$/)?.[1];
    return (
      <>
        <p className="sb-lead">{t("set.domains.subHost.onNode", { node: nameOf(host.nodeId) })}</p>
        {head}
        {host.ready ? (
          <Pb tone="ok" className="sx-pbm">{t("set.domains.subHost.readyYes")}</Pb>
        ) : (
          <Pb tone="part" className="sx-pbm" title={t("set.domains.subHost.readyNo")}>
            <ul className="sx-steps" data-testid="subhost-steps">
              {steps.map((s) => (
                <li key={s} data-done={!host.notReady.includes(s) || undefined}>
                  {host.notReady.includes(s) ? "○" : "✓"} {t(`set.domains.subHost.step.${s}`)}
                </li>
              ))}
            </ul>
          </Pb>
        )}
        {host.caaWaived && (
          <Pb tone="part" className="sx-pbm">
            {t("set.domains.subHost.caaWaived")}
          </Pb>
        )}
        {caaAction && (
          <Pb
            tone="part"
            className="sx-pbm"
            action={
              askNoCAA ? (
                <BusyButton variant="danger" busy={busy === "caa"} disabled={!!busy} onClick={() => void run("caa", () => subHost.waiveSubscriptionHostCAA({}))}>
                  {t("set.domains.subHost.caaWaive")}
                </BusyButton>
              ) : (
                <BusyButton disabled={!!busy} onClick={() => setAskNoCAA(true)}>
                  {t("set.domains.subHost.caaWaive")}
                </BusyButton>
              )
            }
          >
            {askNoCAA ? t("set.domains.subHost.caaWaiveAsk") : t(`set.domains.subHost.caa.${caaStatus}`)}
          </Pb>
        )}
        {host.caaRecord && (
          <Rows>
            <Row label={t("set.domains.subHost.caa")} hint={caaValue ? t("set.domains.subHost.caaFields", { name: host.domain }) : undefined}>
              <CmdRow value={caaValue ?? host.caaRecord} wrap />
              {!caaAction && <p className="text-xs text-muted">{t(`set.domains.subHost.caa.${caaStatus}`)}</p>}
            </Row>
          </Rows>
        )}
        {error}
        <Rows>
          <Row label={t("set.domains.subHost.moveTo")}>
            <Select value={node === host.nodeId ? "" : node} onValueChange={(v) => pickNode(v, host.domain)} options={options([host.nodeId])} aria-label={t("set.domains.subHost.moveTo")} />
          </Row>
        </Rows>
        <TextField label={t("set.domains.subHost.siteName")} hint={t("set.domains.subHost.siteNameHint")} value={siteName} onChange={(e) => setSiteName(e.target.value)} placeholder="www.example.com" mono maxLength={253} autoComplete="off" />
        <Foot>
          <BusyButton busy={busy === "move"} disabled={!node || node === host.nodeId || !!busy} onClick={() => void run("move", () => subHost.startSubscriptionHostMove({ nodeId: node, siteName: siteName.trim() }))}>
            {t("set.domains.subHost.move")}
          </BusyButton>
        </Foot>
        <TextField label={t("set.domains.subHost.publicUrl")} hint={t("set.domains.subHost.publicUrlHint")} value={publicUrl} onChange={(e) => setPublicUrl(e.target.value)} placeholder="https://panel.example.com" mono maxLength={300} autoComplete="off" />
        {askNoAddress && (
          <Pb
            tone="bad"
            action={
              <BusyButton variant="danger" busy={busy === "off"} disabled={!!busy} onClick={() => void disable(true)}>
                {t("set.domains.subHost.noAddressYes")}
              </BusyButton>
            }
          >
            {t("set.domains.subHost.noAddressAsk")}
          </Pb>
        )}
        <Foot>
          <BusyButton variant="danger" busy={busy === "off" && !askNoAddress} disabled={!!busy} onClick={() => (publicUrl.trim() ? void disable(false) : setAskNoAddress(true))}>
            {t("set.domains.subHost.turnOff")}
          </BusyButton>
        </Foot>
      </>
    );
  }

  const finish = (force: boolean) => run("finish", () => subHost.finishSubscriptionHostChange({ force }));
  const actions = (
    <Foot>
      <BusyButton variant="primary" busy={busy === "finish"} disabled={!!busy} onClick={() => void finish(false)}>
        {t("set.domains.subHost.finish")}
      </BusyButton>
      <BusyButton busy={busy === "cancel"} disabled={!!busy} onClick={() => void run("cancel", () => subHost.cancelSubscriptionHostChange({}))}>
        {t("set.domains.subHost.cancel")}
      </BusyButton>
    </Foot>
  );
  const force = (
    <Pb
      tone="part"
      action={
        <BusyButton variant="danger" disabled={!!busy} onClick={() => void finish(true)}>
          {t("set.domains.subHost.forceYes")}
        </BusyButton>
      }
    >
      {t("set.domains.subHost.forceAsk")}
    </Pb>
  );

  if (moving) {
    return (
      <>
        <p className="sb-lead">{t("set.domains.subHost.moving", { from: nameOf(host.nodeId), to: nameOf(host.nextNodeId) })}</p>
        {head}
        <p className="sb-lead">{t("set.domains.subHost.changeRecord", { address: nodeList.find((n) => n.id === host.nextNodeId)?.address ?? "" })}</p>
        {record.data && <RecordCheck r={record.data} />}
        {error}
        {(failure?.code === "sub_host_dns_mismatch" || failure?.code === "sub_host_next_not_ready") && force}
        {actions}
      </>
    );
  }

  // leaving: the panel is back on its own address and waits for the nodes to follow
  return (
    <>
      <p className="sb-lead">{t("set.domains.subHost.leaving", { url: host.publicUrl || "—" })}</p>
      {head}
      <Rows>
        <Row label={t("set.domains.subHost.pending")}>
          {host.pending.length === 0 ? (
            <p className="sx-none">{t("set.domains.subHost.pendingNone")}</p>
          ) : (
            <ul>
              {host.pending.map((p) => (
                <li key={p.nodeId}>{p.name || p.nodeId}</li>
              ))}
            </ul>
          )}
        </Row>
      </Rows>
      {error}
      {failure?.code === "sub_host_nodes_pending" && force}
      {actions}
    </>
  );
}
