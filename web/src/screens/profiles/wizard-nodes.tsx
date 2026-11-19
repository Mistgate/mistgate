import { useState } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { cx } from "@/lib/cx";
import { AwgBackendFields, type AwgMode } from "@/screens/node/awg-backend";
import { AppLink } from "@/screens/users/nav";
import type { Tx } from "@/screens/users/t";
import type { Fix, NodeRow } from "./wizard-check";
import { Cb, Chip, State } from "./wizard-bits";
import { RowSharedNote, ShareRowFix } from "./share-row-fix";

/** One node as the step draws it: the node's own facts, what the check said about it, and what has been typed under it. */
export type NodeVM = {
  id: string;
  name: string;
  countryCode: string;
  linked: boolean;
  entry: boolean;
  reach: { kind: "ok" | "part" | "bad" | "none"; label: string };
  row: NodeRow;
  on: boolean;
  fix: Fix;
  /** AmneziaWG's first profile on this node: its backend can be changed here (the node can build the kernel module itself). */
  awg?: { mode: AwgMode; auto: boolean };
  /** The node's address, for the DNS record a name beside another service needs. */
  address?: string;
  /** The words of the way out the profile has: "or a self-signed certificate", "or another site". */
  alt: "" | "self" | "site";
};

/** The domain asked for by a node whose address is an IP (the board's `.pp-pnf`): the field, its button, and the other way out. */
function DomainFix({ n, t, onFix, onAlt }: { n: NodeVM; t: Tx; onFix: (p: Fix) => void; onAlt: () => void }) {
  const [draft, setDraft] = useState("");
  const ok = /^[a-z0-9-]+(\.[a-z0-9-]+)+$/i.test(draft.trim());
  return (
    <>
      <input className="pw-i-field mono" type="text" placeholder="vpn.example.com" aria-label={t("wiz.dom.aria")} value={draft} autoCapitalize="off" spellCheck={false} onChange={(e) => setDraft(e.target.value.trim())} onKeyDown={(e) => e.key === "Enter" && ok && onFix({ sni: draft.trim() })} />
      <Button size="md" disabled={!ok} onClick={() => onFix({ sni: draft.trim() })}>
        {t("wiz.dom.go")}
      </Button>
      {n.alt && (
        <button type="button" className="pw-lk" onClick={onAlt}>
          {t(n.alt === "self" ? "wiz.alt.self" : "wiz.alt.site")}
        </button>
      )}
    </>
  );
}

/** Step 3: the nodes, one row each, with the check's word and what the node still needs (the board's `.pw-nodes`). */
export function NodesStep({
  t,
  name,
  nodes,
  allOn,
  onAll,
  onPick,
  onFix,
  onAlt,
  onAdd,
}: {
  t: Tx;
  name: string;
  nodes: NodeVM[];
  allOn: boolean;
  onAll: () => void;
  onPick: (id: string) => void;
  onFix: (id: string, p: Fix) => void;
  onAlt: () => void;
  onAdd: () => void;
}) {
  return (
    <>
      <h2 className="pw-q">{t("wiz.q3", { name })}</h2>
      {nodes.length === 0 ? (
        <section className="pw-card pw-sec pw-empty">
          <p className="pw-quiet">{t("wiz.noNodes")}</p>
          <div className="pw-pad">
            <Button size="lg" onClick={onAdd}>
              {t("wiz.addNode")}
            </Button>
          </div>
        </section>
      ) : (
        <>
          <div className="pw-all2">
            <button type="button" className="pw-pnh" role="checkbox" aria-checked={allOn} onClick={onAll}>
              <Cb on={allOn} />
              <b>{t("wiz.allOk")}</b>
            </button>
            <span>{t("wiz.allOkHint")}</span>
          </div>
          <section className="pw-card pw-nodes">
            {nodes.map((n) => {
              const { row } = n;
              const port = row.port && (row.verdict !== "ok" || n.fix.port !== undefined);
              const backend = !!n.awg && row.backend && !row.need;
              const showFix = row.dom || !!port || row.upd || backend || !!row.share || !!row.shared;
              return (
                <div key={n.id} className={cx("pw-pn", row.need && "need")}>
                  <button type="button" className="pw-pnh" role="checkbox" aria-checked={n.on} aria-disabled={row.need} onClick={() => onPick(n.id)}>
                    <Cb on={n.on} />
                    <State kind={n.linked ? "ok" : "none"} title={t(n.linked ? "wiz.agent.on" : "wiz.agent.off")} />
                    {n.countryCode ? <Flag code={n.countryCode} size={14} /> : <i className="pw-fl" aria-hidden="true" />}
                    <b className="mono">{n.name}</b>
                    {n.entry && <i className="pw-role">{t("wiz.entry")}</i>}
                    <Chip kind={n.reach.kind}>{n.reach.label}</Chip>
                    <em className={row.verdict}>{row.text}</em>
                  </button>
                  {showFix && (
                    <div className="pw-pnf">
                      {row.share && <ShareRowFix share={row.share} node={{ name: n.name, address: n.address ?? "" }} inputClass="pw-i-field" onName={(sni) => onFix(n.id, { sni })} onPort={(p) => onFix(n.id, { port: p, anyway: false })} />}
                      {row.shared && <RowSharedNote shared={row.shared} node={{ address: n.address ?? "" }} />}
                      {row.dom && <DomainFix n={n} t={t} onFix={(p) => onFix(n.id, p)} onAlt={onAlt} />}
                      {port && (
                        <>
                          <span className="pw-q1">{t("wiz.pt.label", { id: n.name })}</span>
                          <input
                            className="pw-i-field mono pw-pt"
                            type="text"
                            inputMode="numeric"
                            value={n.fix.port ?? row.port!.free}
                            aria-label={t("wiz.pt.aria")}
                            onChange={(e) => onFix(n.id, { port: e.target.value.replace(/\D/g, "").slice(0, 5), anyway: false })}
                          />
                          {row.port!.lossy && !(n.fix.anyway && n.fix.port === row.port!.lossy) && (
                            <Button size="md" variant="ghost" onClick={() => onFix(n.id, { port: row.port!.lossy, anyway: true })}>
                              {t("ports.anyway.put")} · {row.port!.lossy}
                            </Button>
                          )}
                        </>
                      )}
                      {row.upd && (
                        <AppLink to="/updates" className={buttonClass("secondary", "md")}>
                          {t("wiz.upd")}
                        </AppLink>
                      )}
                      {backend && n.awg && (
                        <div className="pw-bk">
                          <AwgBackendFields mode={n.fix.backend ?? n.awg.mode} onChange={(m) => onFix(n.id, { backend: m })} label={t("awg.backend.title")} auto={n.awg.auto} />
                        </div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </section>
        </>
      )}
    </>
  );
}
