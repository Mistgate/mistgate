import { useMemo } from "react";
import type { ProfileFormat, ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { Notice } from "@/components/ui/notice";
import { cx } from "@/lib/cx";
import { asVersion, minClients } from "@/lib/awg";
import { useTx, type Tx } from "@/screens/users/t";
import { FormatsLine } from "./formats";
import { allFields, changedFields, parseSchema, type Settings } from "./schema";
import type { AwgAdvice } from "./awg-panel";
import type { Problem } from "./schema-form";
import { TrustTunnelLink } from "./tt-link";
import { Gl, P } from "./wizard-bits";
import { FullForm } from "./wizard-full";
import { netOf, summaryRows, type Task, type Tone } from "./wizard-model";
import { ownLinkOnly, trustTunnelOnly } from "./protocol-meta";

export type Preview = { text: string; errors: boolean; failed: string; busy: boolean; formats: readonly ProfileFormat[] };

export type SettingsProps = {
  task: Task;
  /** The mark of the heading: the task's, or the picked protocol's for "I'll pick it myself". */
  glyph: string;
  tone: Tone;
  info: ProtocolInfo;
  /** "I'll pick it myself": every protocol the panel has, to choose between. */
  choose?: ProtocolInfo[];
  name: string;
  nameError: "" | "empty" | "taken";
  portText: string;
  portErr: boolean;
  cdn: string;
  cdnErr: boolean;
  all: boolean;
  settings: Settings;
  base: Settings;
  problems: readonly Problem[];
  advice?: AwgAdvice;
  preview: Preview;
  onName: (v: string) => void;
  onPort: (v: string) => void;
  onCdn: (v: string) => void;
  onOld: (old: boolean) => void;
  onAll: (open: boolean) => void;
  onSettings: (next: Settings) => void;
  onProtocol: (id: string) => void;
  onChangeTask: () => void;
};

/** What the preview says the person's app gets, in a few words: formats for the subscription, a sentence for what has its own link. */
function AppsText({ info, settings, t, formats }: { info: ProtocolInfo; settings: Settings; t: Tx; formats: readonly ProfileFormat[] }) {
  if (info.id === "awg") {
    const v = asVersion(settings.version);
    // the oldest client of each app that reads this version (the board names AmneziaVPN, Mihomo and the routers)
    const oldest = minClients[v]
      .filter((c) => ["AmneziaVPN", "Mihomo", "Keenetic", "Keenetic stable", "GL.iNet firmware"].includes(c.app))
      .map((c) => `${c.app} ${c.min}`)
      .join(", ");
    return <p className="pw-q1">{t("wiz.apps.awg", { old: oldest })}</p>;
  }
  if (info.id === "mtproto") return <p className="pw-q1">{t("wiz.apps.tg")}</p>;
  if (info.id === "trusttunnel") return <p className="pw-q1">{t("wiz.apps.tt")}</p>;
  return (
    <>
      {formats.length > 0 && !ownLinkOnly(info.apps) ? <FormatsLine formats={formats} t={t} className="pw-fmt" /> : <p className="pw-q1">{t("profiles.previewHint")}</p>}
      {info.id === "hysteria2" && <p className="pw-q1">{t("wiz.apps.happ")}</p>}
    </>
  );
}

/** Step 2: the task's few fields, "All settings" with the protocol's whole form, and what the client will get (the board's `.pf-g.pp-grid`). */
export function SettingsStep(p: SettingsProps) {
  const t = useTx();
  const { task, info } = p;
  const own = task.id === "own";
  const fields = useMemo(() => allFields(parseSchema(info.settingsSchemaJson)), [info.settingsSchemaJson]);
  const changed = changedFields(fields, p.settings, p.base).length;
  const errors = p.problems.length;
  const rows = summaryRows(info, p.settings, t);
  const awg = info.id === "awg";
  const old = awg && asVersion(p.settings.version) === "2.0";
  const net = netOf(info.id);
  const title = `${t(`wiz.t.${task.id}.t`)}${own ? ` · ${info.displayName}` : ` · ${t(`wiz.t.${task.id}.p`)}`}`;
  const open = p.all || errors > 0;
  const quick = awg ? [] : ["443", "8443"];
  return (
    <div className="pw-grid">
      <div className="pw-col">
        <section className="pw-card pw-sec">
          <div className="pw-sh">
            <span className={cx("pw-g", p.tone)} aria-hidden="true">
              <Gl d={p.glyph} />
            </span>
            <h3>{title}</h3>
            <button type="button" className="pw-lk" onClick={p.onChangeTask}>
              {t("wiz.change")}
            </button>
          </div>
          <div className="pw-rows">
            {own && p.choose && (
              <div className="pw-row">
                <div className="pw-lb">
                  <b>{t("wiz.protocol")}</b>
                  <span>{t("wiz.protocolHint")}</span>
                </div>
                <div className="pw-ct">
                  <div className="pw-seg wrap" role="radiogroup" aria-label={t("wiz.protocol")}>
                    {p.choose.map((x) => (
                      <button key={x.id} type="button" role="radio" aria-checked={x.id === info.id} onClick={() => p.onProtocol(x.id)}>
                        {x.displayName}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            )}
            <div className="pw-row">
              <div className="pw-lb">
                <b>{t("wiz.name")}</b>
                <span>{t("wiz.nameHint")}</span>
              </div>
              <div className="pw-ct">
                <input className="pw-i-field" type="text" data-w="name" value={p.name} autoComplete="off" spellCheck={false} maxLength={64} aria-label={t("wiz.name")} aria-invalid={p.nameError !== ""} onChange={(e) => p.onName(e.target.value)} />
                {p.nameError !== "" && <p className="pw-q1 bad">{t(p.nameError === "taken" ? "wiz.nameTaken" : "profiles.nameRequired")}</p>}
              </div>
            </div>
            <div className="pw-row">
              <div className="pw-lb">
                <b>{t(net === "udp" ? "wiz.portUdp" : "wiz.portTcp")}</b>
                <span>{t("wiz.portHint")}</span>
              </div>
              <div className="pw-ct">
                <div className="pw-ib pw-short">
                  <input className="pw-i-field mono" type="text" inputMode="numeric" data-w="port" value={p.portText} autoComplete="off" aria-label={t(net === "udp" ? "wiz.portUdp" : "wiz.portTcp")} aria-invalid={p.portErr} onChange={(e) => p.onPort(e.target.value.trim())} />
                  {quick.length > 0 && (
                    <span className="pw-quick">
                      {quick.map((v) => (
                        <button key={v} type="button" className="mono" aria-pressed={p.portText === v} onClick={() => p.onPort(v)}>
                          {v}
                        </button>
                      ))}
                    </span>
                  )}
                </div>
                {p.portErr && <p className="pw-q1 bad">{t("wiz.portErr")}</p>}
              </div>
            </div>
            {task.id === "cdn" && (
              <div className="pw-row">
                <div className="pw-lb">
                  <b>{t("wiz.cdn")}</b>
                  <span>{t("wiz.cdnHint")}</span>
                </div>
                <div className="pw-ct">
                  <input className="pw-i-field mono" type="text" placeholder="cdn.example.org" value={p.cdn} autoComplete="off" spellCheck={false} aria-label={t("wiz.cdn")} aria-invalid={p.cdnErr} onChange={(e) => p.onCdn(e.target.value.trim())} />
                  {p.cdnErr && <p className="pw-q1 bad">{t("wiz.cdnErr")}</p>}
                </div>
              </div>
            )}
            {awg && (
              <div className="pw-row">
                <div className="pw-lb">
                  <b>{t("wiz.old")}</b>
                  <span>{t("wiz.oldHint")}</span>
                </div>
                <div className="pw-ct">
                  <div className="pw-seg" role="radiogroup" aria-label={t("wiz.old")}>
                    {[false, true].map((v) => (
                      <button key={String(v)} type="button" role="radio" aria-checked={old === v} onClick={() => p.onOld(v)}>
                        {t(v ? "wiz.old.yes" : "wiz.old.no")}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            )}
          </div>
          <div className={cx("pw-adv", open && "open", changed > 0 && "has ch", errors > 0 && "has err")}>
            <button type="button" className="pw-advh pw-all" aria-expanded={open} onClick={() => p.onAll(!p.all)}>
              <Gl d={P.next} />
              <span className="t">{t("wiz.all")}</span>
              <em>{errors > 0 ? t.n("profiles.errors", errors) : changed > 0 ? t("profiles.changed", { n: changed }) : ""}</em>
            </button>
            {open && (
              <div className="pw-advb pw-def">
                <p className="pw-q1">{t(own ? "wiz.allLeadOwn" : "wiz.allLead")}</p>
                {rows.map((r) => (
                  <div key={r.id} className="pw-chg">
                    <b>{r.label}</b>
                    <span>{r.value}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>
        {open && <FullForm info={info} settings={p.settings} base={p.base} problems={p.problems} advice={p.advice} onChange={p.onSettings} />}
      </div>
      <aside className="pw-col pw-as">
        <section className="pw-card pw-sec">
          <div className="pw-sh">
            <span className="pw-ic mint" aria-hidden="true">
              <Gl d={P.code} />
            </span>
            <h3>{t("wiz.preview")}</h3>
          </div>
          <div className="pw-asb">
            {p.preview.failed ? <Notice tone="danger">{p.preview.failed}</Notice> : p.preview.errors ? <Notice>{t("profiles.previewErrors")}</Notice> : <pre className="pw-pre mono">{p.preview.text || (p.preview.busy ? t("profiles.previewBusy") : "")}</pre>}
            {trustTunnelOnly(info.apps) && !p.preview.failed && !p.preview.errors && <TrustTunnelLink settings={p.settings} />}
          </div>
        </section>
        <section className="pw-card pw-sec">
          <div className="pw-sh">
            <span className="pw-ic lav" aria-hidden="true">
              <Gl d={P.phone} />
            </span>
            <h3>{t("wiz.apps")}</h3>
          </div>
          <div className="pw-asb">
            <AppsText info={info} settings={p.settings} t={t} formats={p.preview.failed ? [] : p.preview.formats} />
          </div>
        </section>
      </aside>
    </div>
  );
}
