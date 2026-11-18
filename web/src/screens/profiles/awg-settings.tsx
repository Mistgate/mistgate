import { useEffect, useState, type ReactNode } from "react";
import type { ObfuscationScore } from "@/gen/mistgate/admin/v1/profile_pb";
import { parseAwgConf, randomPort, versions, type ConfImport } from "@/lib/awg";
import { cx } from "@/lib/cx";
import { useTx, type Tx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { useAwgBlock } from "./awg-panel";
import { FieldRow } from "./page-fields";
import { problemText } from "./problem-text";
import { D, Gl } from "./pf-bits";
import { errorsOf, getAt, isChanged, MASK, setAt, type Field, type Settings } from "./schema";
import type { Problem } from "./schema-form";
import "./awg-settings.css";

// AmneziaWG's settings as the board draws them (PnProfile, the state "AmneziaWG"): three cards, "Основное" (the name, the version as two cards,
// the port), "Маскировка и обход" (the look of the first packets, the domain, a signature of its own for every device, and a fold with everything
// that is the block of keys) and "Сеть" (the exit, the MTU, and a fold with the keepalive and the client networks). The cards are made of the
// schema's fields: what the schema gives that the board has no row for goes into the card it fits best (a field of the block into the fold of the
// look, any other into "Основное"), so nothing the server offers is lost.

/** What a card is about, as the board's icons: the basics sand, the disguise sage, the network sky. */
const marks = {
  basics: { tone: "sand", d: "M4 7h10M18 7h2M4 12h4M12 12h8M4 17h12M14 5v4M8 10v4M16 15v4" },
  look: { tone: "sage", d: "M4 6c5-2 11-2 16 0v5c0 5-3.5 8-8 9-4.5-1-8-4-8-9zM8.5 11h2M13.5 11h2M9.5 15c1.5 1 3.5 1 5 0" },
  net: { tone: "sky", d: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18" },
} as const;

/** The block's fields in the board's five groups of its fold (what the board calls junk, sizes, headers, the signature and the 3.1 timers). */
const blockGroups = [
  { id: "junk", title: "awg.g.junk", ids: ["obfuscation.jc", "obfuscation.jmin", "obfuscation.jmax"] },
  { id: "sizes", title: "awg.g.sizes", ids: ["obfuscation.s1", "obfuscation.s2", "obfuscation.s3", "obfuscation.s4", "obfuscation.content_padding_addition", "obfuscation.random_trailers"] },
  { id: "headers", title: "awg.g.headers", ids: ["obfuscation.h1", "obfuscation.h2", "obfuscation.h3", "obfuscation.h4", "obfuscation.header_protection_key", "obfuscation.disable_cookies"] },
  { id: "signature", title: "awg.g.signature", ids: ["obfuscation.i1", "obfuscation.i2", "obfuscation.i3", "obfuscation.i4", "obfuscation.i5", "obfuscation.signature_seed"] },
  { id: "timers", title: "awg.g.timers", ids: ["obfuscation.rekey_after_time", "obfuscation.rekey_timeout", "obfuscation.reject_after_time", "obfuscation.keepalive_timeout", "obfuscation.max_handshake_attempts"] },
] as const;

/** The fields the three cards draw by name (the rest are put by the rule above). */
const named = new Set(["version", "port", "mtu", "egress", "subnet4", "subnet6", "obfuscation.preset", "obfuscation.domain", "obfuscation.per_device_signature", "obfuscation.persistent_keepalive", ...blockGroups.flatMap((g) => g.ids)]);

/** The board's tones of the score: from 85 well hidden, from 60 a remark or two, below it a plain tell. */
const toneOf = (n: number) => (n >= 85 ? "ok" : n >= 60 ? "part" : "bad");

function Head({ mark, title, children }: { mark: keyof typeof marks; title: string; children?: ReactNode }) {
  return (
    <div className="pp-sh">
      <span className={cx("pp-ic", marks[mark].tone)} aria-hidden>
        <G d={marks[mark].d} size={15} stroke={1.5} />
      </span>
      <h3>{title}</h3>
      {children}
    </div>
  );
}

/** A row of the board that the schema's field does not draw itself: its label (with the dot of a changed value), hint and control. */
function Row({ id, label, hint, crit, dirty, wide, errors, children }: { id: string; label: ReactNode; hint?: ReactNode; crit?: { dirty: boolean; tip: string; word: string }; dirty?: boolean; wide?: boolean; errors?: ReactNode; children: ReactNode }) {
  return (
    <div className={cx("pp-row", wide && "wide", !!errors && "err")} data-k={id}>
      <div className="pp-lb">
        <b>
          {label}
          <i className={cx("pp-dot", dirty && "on")} aria-hidden />
        </b>
        {crit && (
          <i className={cx("pp-crit", crit.dirty && "on")} title={crit.tip}>
            {crit.word}
          </i>
        )}
        {hint && <span>{hint}</span>}
      </div>
      <div className="pp-ct">
        {children}
        {errors}
      </div>
    </div>
  );
}

/** The fold of a card (the board's .pp-adv): closed until opened, open while one of its fields has a problem. */
function Fold({ title, tag, open, onToggle, changed, children }: { title: string; tag: string; open: boolean; onToggle: () => void; changed: boolean; children: ReactNode }) {
  return (
    <div className={cx("pp-adv", open && "open", changed && "ch", tag && "has")}>
      <button type="button" className="pp-advh" aria-expanded={open} onClick={onToggle}>
        <G d={D.chev} size={16} stroke={1.5} />
        <span className="t">{title}</span>
        <em>{tag}</em>
      </button>
      {open && children}
    </div>
  );
}

/** "Fill from a client .conf" (the board's .pf-conf): a button that opens a box, the box takes the text, reads it here and drops it; what it did is told under it. */
function ConfFill({ busy, onApply, initialOpen }: { busy: boolean; onApply: (imp: ConfImport) => Promise<boolean>; initialOpen?: boolean }) {
  const t = useTx();
  const [open, setOpen] = useState(!!initialOpen);
  const [text, setText] = useState("");
  const [report, setReport] = useState<ConfImport | null>(null);
  const apply = async () => {
    const imp = parseAwgConf(text);
    if (await onApply(imp)) {
      setReport(imp);
      setText("");
      setOpen(false);
    }
  };
  const list = (names: string[]) => names.join(", ");
  return (
    <>
      <button type="button" className="btn sm" aria-expanded={open} onClick={() => setOpen(!open)}>
        {t("awg.conf.btn")}
      </button>
      {open && (
        <div className="pf-conf" style={{ flexBasis: "100%" }}>
          <textarea className="an-i mono" rows={4} aria-label={t("awg.conf.btn")} placeholder={t("awg.import.ph")} spellCheck={false} autoComplete="off" value={text} onChange={(e) => setText(e.target.value)} />
          <div>
            <button type="button" className="btn sm" onClick={() => setOpen(false)}>
              {t("common.cancel")}
            </button>
            <button type="button" className="btn sm pri" aria-disabled={busy || text.trim() === "" || undefined} onClick={() => !busy && text.trim() !== "" && void apply()}>
              {t("awg.conf.do")}
            </button>
          </div>
          <small>{t("awg.conf.noKeys")}</small>
        </div>
      )}
      {report && (
        <div role="status" className="pf-conf-r" style={{ flexBasis: "100%" }}>
          <b>{t.opt(`awg.import.kind.${report.kind}`)}</b>
          {report.filled.length > 0 && <span>{t("awg.import.filled", { list: list(report.filled) })}</span>}
          {report.notes.map((n) => (
            <span key={n}>{t.opt(`awg.import.note.${n}`)}</span>
          ))}
          {report.ignored.length > 0 && <span className="text-muted">{t("awg.import.ignored", { list: list(report.ignored) })}</span>}
          {report.unknown.length > 0 && <span style={{ color: "var(--warning-text)" }}>{t("awg.import.unknown", { list: list(report.unknown) })}</span>}
        </div>
      )}
    </>
  );
}

/** What the score is made of (the board's card over the form): the starting point with its reason, and every step with its sign and the way to its field. */
export function ScoreCard({ score, t, onClose, onGo }: { score: ObfuscationScore; t: Tx; onClose: () => void; onGo: (pointer: string) => void }) {
  const tier = t.opt(`awg.score.tierB.${score.tier}`) ?? t.opt(`awg.score.tier.${score.tier}`) ?? score.tier;
  return (
    <section className="card pp-sec pp-scb">
      <div className="pp-sh">
        <h3>{t("awg.score.title")}</h3>
        <button type="button" className="ic" aria-label={t("common.close")} onClick={onClose}>
          <G d="M6 6l12 12M18 6L6 18" size={16} stroke={1.5} />
        </button>
      </div>
      <div className="pp-sci">
        <b className="mono">{score.base}</b>
        <span>{t("awg.score.baseRow", { tier })}</span>
        <button type="button" className="an-lk" onClick={() => onGo("/version")}>
          {t("pf.st.toField")}
        </button>
      </div>
      {score.items.map((i) => (
        <div key={i.code + i.pointer} className="pp-sci">
          <b className={cx("mono", i.delta > 0 ? "ok" : "part")}>{i.delta > 0 ? `+${i.delta}` : `−${-i.delta}`}</b>
          <span>{t.opt(`awg.score.${i.code}`, { ...i.params, ...(i.params.preset && { preset: t.opt(`awg.preset.${i.params.preset}`) ?? i.params.preset }) }) ?? i.code}</span>
          {i.pointer && (
            <button type="button" className="an-lk" onClick={() => onGo(i.pointer)}>
              {t("pf.st.toField")}
            </button>
          )}
        </div>
      ))}
    </section>
  );
}

export type AwgSettingsProps = {
  fields: Field[];
  settings: Settings;
  base: Settings;
  problems: readonly Problem[];
  /** The form's own change (a look picked, a block generated). */
  onChange: (next: Settings) => void;
  /** A change from a row (typing into I1-I5 chooses the custom look). */
  onRow: (next: Settings) => void;
  /** The profile's name row, which leads "Основное". */
  lead: ReactNode;
  /** The profile is on a node: the client networks are kept. */
  locked: boolean;
  /** The warning under "WARP" (which nodes have none). */
  egressNote: (f: Field) => ReactNode;
  /** The window of a copy through WARP (or without it); off while there are unsaved changes. */
  twin: { word: string; dirty: boolean; open: () => void } | null;
  score?: ObfuscationScore;
  /** "To the field" of the score. */
  onGo: (pointer: string) => void;
  /** The card hands over what opens the fold a field is in (a remark's "To the field" needs it before it scrolls; it tells whether it had to). */
  register?: (reveal: ((pointer: string) => boolean) | null) => void;
  /** What a dev scene opens on its own: the score ("score"), the folds ("adv", "net"), the values ("show"), the .conf box ("conf"). */
  scene?: ReadonlySet<string>;
};

export function AwgSettings({ fields, settings, base, problems, onChange, onRow, lead, locked, egressNote, twin, score, onGo, scene, register }: AwgSettingsProps) {
  const t = useTx();
  const awg = useAwgBlock(settings, onChange);
  const [scoreOpen, setScoreOpen] = useState(!!scene?.has("score"));
  const [fold, setFold] = useState({ look: !!scene?.has("adv"), net: !!scene?.has("net") });
  const [show, setShow] = useState(!!scene?.has("show"));
  const byId = new Map(fields.map((f) => [f.id, f]));
  const dirty = (f?: Field) => !!f && isChanged(f, settings, base);
  const errs = (f?: Field) => (f ? errorsOf(f, problems) : []);
  const row = (id: string, opts: { after?: ReactNode; below?: ReactNode; masked?: boolean } = {}) => {
    const f = byId.get(id);
    return f ? <FieldRow key={f.key} protocol="awg" f={f} t={t} settings={settings} locked={locked && (f.id === "subnet4" || f.id === "subnet6")} below={opts.below} dirty={dirty(f)} problems={errs(f)} onChange={(path, value) => onRow(setAt(settings, path, value))} after={opts.after} masked={opts.masked} /> : null;
  };
  const errorLines = (f?: Field) =>
    errs(f).length > 0 && (
      <span className="pp-fes">
        {errs(f).map((p, i) => (
          <p key={i} className="pp-ferr" role="alert">
            <Gl k="bad" />
            {problemText(t, "awg", p) || p.message}
          </p>
        ))}
      </span>
    );

  const versionField = byId.get("version");
  const rest = fields.filter((f) => !named.has(f.id));
  const strays = rest.filter((f) => !f.id.startsWith("obfuscation."));
  const strayBlock = rest.filter((f) => f.id.startsWith("obfuscation."));
  const blockFields = [...blockGroups.flatMap((g) => g.ids.map((id) => byId.get(id)).filter((f): f is Field => !!f)), ...strayBlock];
  const netFields = ["obfuscation.persistent_keepalive", "subnet4", "subnet6"].map((id) => byId.get(id)).filter((f): f is Field => !!f);
  // the tag of a fold: what is wrong in it, what was changed, else that it holds what breaks issued keys (kept values do not: nobody can change them)
  const foldTag = (list: Field[], kept = false) => {
    const n = list.filter((f) => dirty(f)).length;
    const bad = list.filter((f) => errs(f).length > 0).length;
    return bad > 0 ? t.n("profiles.errors", bad) : n > 0 ? t("pf.st.changed", { n }) : !kept && list.some((f) => f.critical) ? t("pf.st.crit") : "";
  };
  const reveal = (pointer: string) => {
    for (let p = pointer; p; p = p.slice(0, Math.max(0, p.lastIndexOf("/")))) {
      const f = fields.find((x) => x.key === p);
      if (!f) continue;
      const inBlock = blockFields.includes(f);
      const inNet = netFields.includes(f);
      if ((inBlock && !fold.look) || (inNet && !fold.net)) setFold({ ...fold, look: fold.look || inBlock, net: fold.net || inNet });
      return (inBlock && !fold.look) || (inNet && !fold.net);
    }
    return false;
  };
  useEffect(() => {
    register?.(reveal);
    return () => register?.(null);
  });
  const lookOpen = fold.look || blockFields.some((f) => errs(f).length > 0);
  const netOpen = fold.net || netFields.some((f) => errs(f).length > 0);

  const cardProps = { settings, masked: !show, t, dirty, errs, onRow };
  const preset = awg.preset;
  const cur = awg.shown.find((p) => p.id === preset);
  const notePorts = cur && cur.naturalPorts.length > 0 ? t("awg.preset.note.port", { ports: cur.naturalPorts.join(", ") }) : "";
  const presetNote = preset === "quic" || preset === "curl_quic" ? t("awg.preset.note.quic") : preset === "custom" ? t("awg.preset.note.custom") : notePorts;
  const critTip = t("pf.st.critTip");
  const crit = (f?: Field) => (f?.critical ? { dirty: dirty(f), tip: critTip, word: t("pf.st.crit") } : undefined);
  const perDeviceHint = t(preset === "custom" ? "awg.perDevice.custom" : !awg.varies ? "awg.perDevice.same" : awg.perDevice ? "awg.perDevice.on" : "awg.perDevice.off");
  const domainField = byId.get("obfuscation.domain");
  const needsDomain = cur?.usesDomain ?? false;
  const keepalive = byId.get("obfuscation.persistent_keepalive");
  const nets = [byId.get("subnet4"), byId.get("subnet6")].filter((f): f is Field => !!f);

  return (
    <>
      {score && scoreOpen && <ScoreCard score={score} t={t} onClose={() => setScoreOpen(false)} onGo={onGo} />}

      <section className="card pp-sec" data-group="basics">
        <Head mark="basics" title={t("profiles.group.basics")} />
        <div className="pp-rows">
          {lead}
          {versionField && (
            <Row id="version" label={t("profiles.f.awg.version.title")} crit={crit(versionField)} dirty={dirty(versionField)} wide errors={errorLines(versionField)}>
              <div className="pf-opt pp-cards" role="radiogroup" aria-label={t("profiles.f.awg.version.title")}>
                {versions.map((v) => (
                  <button key={v} type="button" role="radio" aria-checked={awg.version === v} disabled={awg.busy} onClick={() => awg.pickVersion(v)}>
                    <span className="nx-rad" aria-hidden aria-checked={awg.version === v}>
                      <i />
                    </span>
                    <div>
                      <b>{t(v === "3.1" ? "awg.version.31" : "awg.version.20")}</b>
                      <span>{t(v === "3.1" ? "awg.version.31Hint" : "awg.version.20Hint")}</span>
                    </div>
                  </button>
                ))}
              </div>
              {awg.version === "2.0" && (
                <p className="pp-rmk part">
                  <Gl k="part" />
                  <span>{t("awg.version.oldWarn")}</span>
                </p>
              )}
              {awg.note && (
                <p role="status" className="pp-q1 ok">
                  {awg.note.n > 0 ? t.n("awg.generated", awg.note.n) : t("awg.generatedNone")}
                </p>
              )}
            </Row>
          )}
          {row("port", {
            after: (
              <button type="button" className="btn" onClick={() => onRow(setAt(settings, ["port"], randomPort()))}>
                {t("awg.port.random")}
              </button>
            ),
          })}
          {strays.map((f) => row(f.id))}
        </div>
      </section>

      <section className="card pp-sec" data-group="obfuscation">
        <Head mark="look" title={t("awg.sec.look")}>
          {score && (
            <button type="button" className={cx("pp-score", toneOf(score.value))} aria-expanded={scoreOpen} onClick={() => setScoreOpen(!scoreOpen)}>
              {t("awg.score.chip", { n: "\u0000" })
                .split("\u0000")
                .flatMap((part, i) => (i === 0 ? [part] : [<b key="n" className="mono">{score.value}</b>, part]))}
            </button>
          )}
        </Head>
        <div className="pp-rows">
          <Row id="obfuscation.preset" label={t("profiles.f.awg.obfuscation.preset.title")} hint={t("awg.mimicry.hint")} dirty={dirty(byId.get("obfuscation.preset"))} wide errors={errorLines(byId.get("obfuscation.preset"))}>
            <div className="pp-pc" role="radiogroup" aria-label={t("profiles.f.awg.obfuscation.preset.title")}>
              {awg.shown.map((p) => (
                <button key={p.id} type="button" role="radio" aria-checked={preset === p.id} disabled={awg.busy} onClick={() => void awg.signature(p.id, awg.domain)}>
                  {t.opt(`awg.preset.${p.id}`) ?? p.name}
                </button>
              ))}
            </div>
            {presetNote && <p className="pp-q1 pp-pcn">{presetNote}</p>}
          </Row>
          {needsDomain && (
            <DomainRow key={awg.domain} f={domainField} value={awg.domain} pool={awg.meta.data?.domains ?? []} busy={awg.busy} dirty={dirty(domainField)} t={t} errors={errorLines(domainField)} onCommit={(d) => void awg.signature(preset, d)} />
          )}
          <Row id="obfuscation.per_device_signature" label={t("awg.perDevice.title")} hint={perDeviceHint} dirty={dirty(byId.get("obfuscation.per_device_signature"))} errors={errorLines(byId.get("obfuscation.per_device_signature"))}>
            <button
              type="button"
              className="fz-sw"
              role="switch"
              aria-checked={awg.perDevice}
              aria-label={t("awg.perDevice.title")}
              disabled={preset === "custom" || !awg.varies}
              onClick={() => onChange({ ...settings, obfuscation: { ...awg.ob, per_device_signature: !awg.perDevice } })}
            >
              <i />
            </button>
          </Row>
          {blockFields.length > 0 && (
            <Fold title={t("awg.adv.look", { n: blockFields.length })} tag={foldTag(blockFields)} open={lookOpen} onToggle={() => setFold({ ...fold, look: !lookOpen })} changed={blockFields.some((f) => dirty(f))}>
              <div className="pp-advb">
                <div className="pp-advt">
                  <span>{t("awg.adv.hidden")}</span>
                  <button type="button" className="an-lk pp-show" onClick={() => setShow(!show)}>
                    {t(show ? "awg.adv.hide" : "awg.adv.show")}
                  </button>
                </div>
                {blockGroups.map((g) => {
                  const list = g.ids.map((id) => byId.get(id)).filter((f): f is Field => !!f);
                  return list.length > 0 && <BlockGroup key={g.id} title={t(g.title)} fields={list} {...cardProps} />;
                })}
                {strayBlock.length > 0 && <BlockGroup title={t("awg.g.other")} fields={strayBlock} {...cardProps} />}
                <div className="pp-advf">
                  <button type="button" className={cx("btn sm", awg.busy && "busy")} onClick={() => void awg.generate(awg.version)}>
                    {awg.busy ? t("awg.generating") : t("awg.generate")}
                    <i className="pp-crit">{t("pf.st.crit")}</i>
                  </button>
                  <ConfFill busy={awg.busy} onApply={awg.importConf} initialOpen={scene?.has("conf")} />
                </div>
                <p className="pp-q1">{t("awg.generateWarn")}</p>
              </div>
            </Fold>
          )}
        </div>
      </section>

      <section className="card pp-sec" data-group="net">
        <Head mark="net" title={t("awg.sec.net")} />
        <div className="pp-rows">
          {row("egress", {
            below: (
              <>
                {twin && (
                  <button type="button" className="an-lk pp-twin" aria-disabled={twin.dirty || undefined} title={twin.dirty ? t("pf.pg.twin.dirty") : undefined} onClick={() => !twin.dirty && twin.open()}>
                    {twin.word}
                  </button>
                )}
                {byId.get("egress") && egressNote(byId.get("egress")!)}
              </>
            ),
          })}
          {row("mtu")}
          {netFields.length > 0 && (
            <Fold title={t(locked ? "awg.adv.netLocked" : "awg.adv.net")} tag={foldTag(netFields, locked)} open={netOpen} onToggle={() => setFold({ ...fold, net: !netOpen })} changed={netFields.some((f) => dirty(f))}>
              <div className="pp-advb">
                {keepalive && row(keepalive.id)}
                {nets.length > 0 && (
                  <Row id="subnets" label={t("awg.nets.title")} hint={t(locked ? "awg.nets.locked" : "awg.subnet.auto")} dirty={nets.some((f) => dirty(f))} errors={nets.some((f) => errs(f).length > 0) && <>{nets.map((f) => errorLines(f))}</>}>
                    <div className="pf-nets">
                      {nets.map((f) =>
                        locked ? (
                          <div key={f.id} className="pf-lock mono" data-k={f.id}>
                            <G d="M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z" size={14} stroke={1.5} />
                            {String(getAt(settings, f.path) ?? "") || "—"}
                          </div>
                        ) : (
                          <input
                            key={f.id}
                            data-k={f.id}
                            className="an-i mono"
                            aria-label={t.opt(`profiles.f.awg.${f.id}.title`) ?? f.title}
                            aria-invalid={errs(f).length > 0 || undefined}
                            autoComplete="off"
                            spellCheck={false}
                            placeholder={f.id === "subnet4" ? "10.66.4.0/22" : "fd66:66:0:1::/64"}
                            value={String(getAt(settings, f.path) ?? "")}
                            onChange={(e) => onRow(setAt(settings, f.path, e.target.value))}
                          />
                        ),
                      )}
                    </div>
                  </Row>
                )}
              </div>
            </Fold>
          )}
        </div>
      </section>
    </>
  );
}

/** A group of the block as the board draws it: a small title over cards of three in a row, each a label over its value (here a box the value is typed in). */
function BlockGroup({ title, fields, settings, masked, t, dirty, errs, onRow }: { title: string; fields: Field[]; settings: Settings; masked: boolean; t: Tx; dirty: (f?: Field) => boolean; errs: (f?: Field) => Problem[]; onRow: (next: Settings) => void }) {
  const bad = fields.flatMap((f) => errs(f).map((p) => ({ f, p })));
  return (
    <div className="pp-hgr">
      <span className="rw-sl">{title}</span>
      <div>
        {fields.map((f) => (
          <BlockCard key={f.key} f={f} settings={settings} masked={masked} t={t} dirty={dirty(f)} invalid={errs(f).length > 0} onRow={onRow} />
        ))}
      </div>
      {bad.map(({ f, p }, i) => (
        <p key={i} className="pp-ferr" role="alert">
          <Gl k="bad" />
          {(t.opt(`profiles.f.awg.${f.id}.title`) ?? f.title) + ": " + (problemText(t, "awg", p) || p.message)}
        </p>
      ))}
    </div>
  );
}

/** One value of the block: the board's card (label, the "critical" tag, the dots until "Show values", then the value in a box); a flag is a switch that says on or off, a key the panel keeps says so. */
function BlockCard({ f, settings, masked, t, dirty, invalid, onRow }: { f: Field; settings: Settings; masked: boolean; t: Tx; dirty: boolean; invalid: boolean; onRow: (next: Settings) => void }) {
  const label = t.opt(`profiles.f.awg.${f.id}.title`) ?? f.title;
  const hint = t.opt(`profiles.f.awg.${f.id}.hint`) ?? f.hint;
  const value = getAt(settings, f.path);
  const set = (v: unknown) => onRow(setAt(settings, f.path, v));
  const secret = f.kind === "secret" || f.kind === "generate";
  const stored = secret && value === MASK;
  let control: ReactNode;
  if (masked) {
    // the board shows the dots, not the value, until "Show values"
    control = <b className="mono">{stored ? t("awg.val.keySet") : "••••"}</b>;
  } else if (f.kind === "toggle") {
    control = (
      <button type="button" className="pp-sw" role="switch" aria-checked={!!value} aria-label={label} onClick={() => set(!value)}>
        {t(value ? "awg.val.on" : "awg.val.off")}
      </button>
    );
  } else {
    const raw = f.kind === "number" ? (value === undefined || value === null ? "" : String(value)) : stored ? "" : String(value ?? "");
    control = (
      <input
        className="mono"
        aria-label={label}
        aria-invalid={invalid || undefined}
        autoComplete="off"
        spellCheck={false}
        inputMode={f.kind === "number" ? "numeric" : undefined}
        placeholder={stored ? t("awg.val.keySet") : ""}
        value={raw}
        onChange={(e) => {
          const v = e.target.value;
          if (f.kind === "number") {
            const d = v.replace(/\D/g, "");
            set(d === "" || Number.isNaN(Number(d)) ? d : Number(d));
          } else set(secret && v === "" ? MASK : v);
        }}
      />
    );
  }
  return (
    <label className={cx(dirty && "ch", invalid && "err")} title={hint || undefined} data-k={f.id}>
      <span>
        {label}
        {f.critical && <i className="pp-crit">{t("pf.st.crit")}</i>}
      </span>
      {control}
    </label>
  );
}

/**
 * The domain the packets carry: typed and applied on Enter or when the box is left, drawn from the built-in list with "Random" (the list itself is the box's
 * suggestions), empty = every device draws its own. It is the board's input with its button; the look is fetched again with the new name.
 */
function DomainRow({ f, value, pool, busy, dirty, t, errors, onCommit }: { f?: Field; value: string; pool: readonly string[]; busy: boolean; dirty: boolean; t: Tx; errors: ReactNode; onCommit: (d: string) => void }) {
  const [draft, setDraft] = useState(value);
  const commit = (d: string) => {
    if (d.trim() !== value) onCommit(d.trim());
  };
  const others = pool.filter((d) => d !== value);
  const list = "awg-domains";
  return (
    <Row id="obfuscation.domain" label={t("awg.domain.title")} hint={t("awg.domain.hint")} dirty={dirty} errors={errors}>
      <div className="pf-ib">
        <input
          className="an-i mono"
          aria-label={f ? t("awg.domain.title") : undefined}
          list={list}
          value={draft}
          disabled={busy}
          placeholder={t("awg.domain.ph")}
          autoComplete="off"
          spellCheck={false}
          maxLength={100}
          onChange={(e) => {
            setDraft(e.target.value);
            if (pool.includes(e.target.value)) commit(e.target.value); // picked from the list
          }}
          onBlur={() => commit(draft)}
          onKeyDown={(e) => e.key === "Enter" && commit(draft)}
        />
        <datalist id={list}>
          {pool.map((d) => (
            <option key={d} value={d} />
          ))}
        </datalist>
        <button type="button" className="btn" disabled={busy || others.length === 0} onClick={() => commit(others[Math.floor(Math.random() * others.length)]!)}>
          {t("awg.domain.random")}
        </button>
      </div>
    </Row>
  );
}
