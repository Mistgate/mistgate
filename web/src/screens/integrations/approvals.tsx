import { useState } from "react";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { StatusPill } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { ApprovalState } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT } from "@/i18n";
import { useFmt } from "@/lib/format";
import {
  approvalStateInfo,
  clockText,
  dangerText,
  factLabel,
  factWords,
  outcomeText,
  secondsLeft,
  toolTitle,
  useApprovalActions,
  useTick,
  type Approval,
  type Approvals,
  type Fact,
} from "@/lib/integrations";
import { card, ProfileChip } from "./parts";

/** One fact. A value that came from data (a name, a node's own text) is quoted and apart from the panel's wording, so it cannot pass for it. */
function FactRow({ fact }: { fact: Fact }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <div className="flex flex-col gap-0.5 text-[13px] sm:flex-row sm:items-baseline sm:gap-3">
      <dt className="text-xs text-muted sm:w-36 sm:flex-none">{factLabel(t, fact.key)}</dt>
      {/* a host key fingerprint or an id has no spaces: it breaks anywhere instead of running off a phone's screen */}
      <dd className="min-w-0 flex-1 text-pretty wrap-anywhere">
        {factWords(t, fmt, fact).map((p, i) =>
          typeof p === "string" ? (
            <span key={i}>{p}</span>
          ) : (
            <span key={i} title={t("int.ap.untrusted")} className="inline-block max-w-full rounded-ctl border border-line bg-canvas px-2 py-0.5 break-words whitespace-pre-wrap">
              “{p.data}”
            </span>
          ),
        )}
      </dd>
    </div>
  );
}

/**
 * node_install: the owner compares the host key the panel read (its fact, never the agent's word) and types the server
 * password here. The agent never sees the password; the panel seals it to this plan for its apply.
 */
function NodeInstallFields({ a, password, setPassword, confirmed, setConfirmed }: {
  a: Approval;
  password: string;
  setPassword: (v: string) => void;
  confirmed: boolean;
  setConfirmed: (v: boolean) => void;
}) {
  const t = useT();
  const fact = (key: string) => a.facts.find((f) => f.key === key)?.value ?? "";
  return (
    <div className="flex flex-col gap-2.5 rounded-field border border-line bg-canvas p-3">
      <p className="text-xs leading-snug text-pretty text-muted">{t("int.ap.ssh.body")}</p>
      <div className="flex flex-col gap-1">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("int.ap.ssh.fingerprint", { algorithm: fact("host_key_algorithm") || "?" })}</span>
        <code data-testid="host-key" className="block font-mono text-xs break-all wrap-anywhere select-all">{fact("host_key")}</code>
      </div>
      <label className="flex cursor-pointer items-start gap-2 text-[13px] leading-snug">
        <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} className="mt-0.5 size-4 flex-none accent-accent" />
        <span>{t("int.ap.ssh.confirmKey")}</span>
      </label>
      <TextField label={t("int.ap.ssh.password")} type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" maxLength={1024} />
    </div>
  );
}

/** What an agent asked for, in the panel's words, with Approve and Reject. The countdown is the panel's 10 minutes from the plan. */
function ApprovalCard({ a, data, nowMs }: { a: Approval; data: Approvals; nowMs: number }) {
  const t = useT();
  const { approve, reject, busy } = useApprovalActions();
  const left = secondsLeft(a, data, nowMs);
  const expired = left === 0;
  const tool = toolTitle(t, a.tool);
  const install = a.tool === "node_install";
  const hostKey = a.facts.find((f) => f.key === "host_key")?.value ?? "";
  const [password, setPassword] = useState("");
  const [keyConfirmed, setKeyConfirmed] = useState(false);
  const installReady = !install || (password !== "" && keyConfirmed && hostKey !== "");
  const onApprove = () =>
    approve.mutate(install ? { id: a.id, sshPassword: password, confirmedFingerprint: hostKey } : { id: a.id }, { onSettled: () => setPassword("") });

  return (
    <article className="flex min-w-0 flex-col gap-3.5 rounded-card-lg border border-warn-line bg-warn-soft p-4 md:p-[18px]">
      <div className="flex flex-wrap items-start gap-x-3 gap-y-1.5">
        <div className="flex min-w-[200px] flex-1 flex-col gap-1.5">
          <h3 className="text-[17px] leading-tight font-extrabold tracking-[-0.02em]">{tool}</h3>
          <span className="flex flex-wrap items-center gap-2 text-xs text-muted">
            {t("int.ap.by", { token: a.tokenName })}
            <ProfileChip profile={a.tokenProfile} />
          </span>
        </div>
        <span className="inline-flex h-[22px] items-center gap-1.5 rounded-ctl bg-canvas px-2 font-mono text-[11px] font-bold text-warn-text">
          <Icon name="clock" size={12} />
          {expired ? t("int.ap.expired") : t("int.ap.left", { time: clockText(left) })}
        </span>
      </div>
      {a.facts.length > 0 && (
        <dl className="flex flex-col gap-1.5">
          {a.facts.map((f, i) => (
            <FactRow key={`${f.key}-${i}`} fact={f} />
          ))}
        </dl>
      )}
      {a.danger.length > 0 && (
        <ul className="flex flex-col gap-1">
          {a.danger.map((code) => (
            <li key={code} className="flex items-start gap-2 text-xs leading-snug font-semibold text-warn-text">
              <Icon name="info" size={14} className="mt-px block flex-none" />
              {dangerText(t, code)}
            </li>
          ))}
        </ul>
      )}
      {a.reason && (
        <figure className="flex flex-col gap-1">
          <figcaption className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("int.ap.reason")}</figcaption>
          <blockquote className="border-l-2 border-line pl-3 text-[13px] leading-normal break-words whitespace-pre-wrap text-muted italic">“{a.reason}”</blockquote>
          <span className="text-[11px] text-faint">{t("int.ap.reason.note")}</span>
        </figure>
      )}
      {install && <NodeInstallFields a={a} password={password} setPassword={setPassword} confirmed={keyConfirmed} setConfirmed={setKeyConfirmed} />}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="primary" size="md" disabled={busy || expired || !installReady} aria-label={t("int.ap.approve.aria", { tool })} onClick={onApprove}>
          {t("int.ap.approve")}
        </Button>
        <Button variant="secondary" size="md" disabled={busy || expired} aria-label={t("int.ap.reject.aria", { tool })} onClick={() => reject.mutate(a.id)}>
          {t("int.ap.reject")}
        </Button>
        <span className="text-xs text-muted">{installReady ? t("int.ap.approve.hint") : t("int.ap.ssh.needed")}</span>
      </div>
    </article>
  );
}

/** The queue: changes an agent planned that wait for the owner. It sits at the top of the screen and is only there while something waits. */
export function ApprovalQueue({ data }: { data: Approvals }) {
  const t = useT();
  const waiting = data.approvals.filter((a) => a.state === ApprovalState.AWAITING);
  const nowMs = useTick(waiting.length > 0);
  if (waiting.length === 0) return null;
  return (
    <section aria-labelledby="int-queue" className="flex flex-col gap-2.5">
      <div className="flex flex-col gap-0.5">
        <SectionLabel as="h2" icon="shield" tone="lavender">
          <span id="int-queue">{t("int.ap.title")}</span>
        </SectionLabel>
        <span className="text-xs text-muted">{t("int.ap.sub")}</span>
      </div>
      {waiting.map((a) => (
        <ApprovalCard key={a.id} a={a} data={data} nowMs={nowMs} />
      ))}
    </section>
  );
}

const visibleRows = 8;

/** What was decided and what came of it, newest first. */
export function ApprovalHistory({ data }: { data: Approvals }) {
  const t = useT();
  const fmt = useFmt();
  const [all, setAll] = useState(false);
  const rows = data.approvals.filter((a) => a.state !== ApprovalState.AWAITING);
  if (rows.length === 0) return null;
  const shown = all ? rows : rows.slice(0, visibleRows);
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="list" tone="sand">
        {t("int.ap.history")}
      </SectionLabel>
      <ul className="flex flex-col">
        {shown.map((a) => {
          const s = approvalStateInfo(a.state);
          const at = a.appliedUnix || a.decidedUnix || a.createdUnix;
          // our words for the outcome where the panel's code has them, else the panel's own line
          const said = outcomeText(t, fmt, a);
          return (
            <li key={a.id} className="flex flex-wrap items-start gap-x-3 gap-y-1.5 border-t border-line py-2.5">
              <div className="flex min-w-[200px] flex-1 flex-col gap-0.5">
                <b className="text-[13px]">{toolTitle(t, a.tool)}</b>
                <span className="text-[11px] leading-snug text-pretty text-muted">
                  {[a.tokenName, a.decidedByName ? t("int.ap.decidedBy", { name: a.decidedByName }) : "", fmt.ago(at)].filter(Boolean).join(" · ")}
                </span>
                {a.error && <span className="text-[11px] leading-snug text-pretty text-danger-text">{said ?? a.error}</span>}
                {!a.error && (said ?? a.result) && <span className="text-[11px] leading-snug text-pretty text-muted">{said ?? a.result}</span>}
              </div>
              <StatusPill kind={s.kind} label={t(s.key)} sm />
            </li>
          );
        })}
      </ul>
      {rows.length > visibleRows && (
        <button type="button" aria-expanded={all} onClick={() => setAll((v) => !v)} className="self-start text-xs font-bold text-muted underline-offset-2 hover:underline">
          {all ? t("int.ap.less") : t("common.showMore")}
        </button>
      )}
    </section>
  );
}
