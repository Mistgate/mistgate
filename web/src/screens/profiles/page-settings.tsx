import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import type { FieldError, ProfileImpact } from "@/gen/mistgate/admin/v1/profile_pb";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { reloadPage, useStaleVersion } from "@/lib/stale";
import { randomPort } from "@/lib/awg";
import { VlessHy2Docs } from "@/screens/node/inbound-check";
import { LossyNotice } from "@/screens/node/port-lossy";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { wayOf } from "@/screens/users/groups";
import { useTx, type Tx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { AwgPanel, awgHidden, handTyped, warnText } from "./awg-panel";
import { ConfLine } from "./conf-preview";
import { EgressNote } from "./egress-note";
import { dropsOf, dropWords, nameOf } from "./formats";
import { mtprotoHidden, mtprotoOn443 } from "./mtproto-form";
import type { ProfileForm } from "./page-form";
import { PageFields } from "./page-fields";
import { DlgFrame } from "./pf-frame";
import type { TabCtx } from "./page-types";
import { Gl } from "./pf-bits";
import { ownLinkOnly, trustTunnelOnly, telegramOnly } from "./protocol-meta";
import { MASK, getAt, setAt, type Field, type Settings } from "./schema";
import type { Problem } from "./schema-form";
import { TrustTunnelLink } from "./tt-link";
import { vlessEdited, vlessHidden, vlessOffers } from "./vless-form";
import "./profiles.css";
import "./profile-page.css";
import "./profile-settings.css";

type Remark = { id: string; g: "bad" | "part"; text: string; pointer: string };

/** Where a remark's "To the field" goes: the row of the field whose pointer it is (or the closest one above it). */
function goField(fields: readonly Field[], pointer: string) {
  let p = pointer;
  while (p) {
    const f = fields.find((x) => x.key === p);
    if (f) {
      const el = document.querySelector<HTMLElement>(`.pp-row[data-k="${CSS.escape(f.id)}"]`);
      if (el) {
        el.scrollIntoView({ block: "center", behavior: "smooth" });
        el.classList.remove("flash");
        void el.offsetWidth;
        el.classList.add("flash");
      }
      return;
    }
    p = p.slice(0, p.lastIndexOf("/"));
  }
}

/**
 * The Settings tab (the board's PnProfile, "Настройки"): the remarks over the form, the form's cards, the bar that holds the changes and
 * the danger zone on the left; "How the client sees it" and "Apps" on the right. The form and its save live in the page (page-form.ts)
 * so what was typed survives a look at the other tabs. AmneziaWG keeps its own cards for the version and the look.
 */
export function SettingsTab({ ctx, form, readOnly, saved }: { ctx: TabCtx; form: ProfileForm; readOnly: boolean; saved: boolean }) {
  const t = useTx();
  const stale = useStaleVersion() !== ""; // the schema this form was drawn from is the old panel's: saving it could be refused or wrong
  const { info } = ctx.data;
  const { settings, groups, fields, problems, preview, count } = form;

  const isAwg = info.id === "awg";
  const locked = isAwg && ctx.data.inbounds.length > 0;
  const readOnlyIds = useMemo<ReadonlySet<string>>(() => (locked ? new Set(["subnet4", "subnet6"]) : new Set()), [locked]);
  const isVless = info.id === "vless";
  const vlessHiddenIds = useMemo<ReadonlySet<string>>(() => (isVless ? vlessHidden(fields, settings) : new Set()), [isVless, fields, settings]);
  const isMtproto = info.id === "mtproto";
  const mtprotoHiddenIds = useMemo<ReadonlySet<string>>(() => (isMtproto ? mtprotoHidden(fields, settings) : new Set()), [isMtproto, fields, settings]);
  const hidden = isAwg ? awgHidden : isVless && vlessHiddenIds.size > 0 ? vlessHiddenIds : isMtproto && mtprotoHiddenIds.size > 0 ? mtprotoHiddenIds : undefined;
  const nodeIds = [...new Set(ctx.data.inbounds.map((i) => i.nodeId))];
  const extra = (f: Field): ReactNode => {
    const note = (text: ReactNode, tone?: "warn") => <span className={cx("pp-q1", tone === "warn" && "part")}>{text}</span>;
    if (isMtproto && f.id === "fake_tls.domain") {
      return (
        <>
          {note(t("mtproto.siteWarn"))}
          {mtprotoOn443(settings) && note(t("mtproto.site443Warn"))}
        </>
      );
    }
    if (isAwg && f.id === "port") {
      return (
        <button type="button" className="an-lk pp-more" onClick={() => form.edit(setAt(settings, ["port"], randomPort()))}>
          {t("awg.port.random")}
        </button>
      );
    }
    if (locked && (f.id === "subnet4" || f.id === "subnet6")) return note(t("awg.subnet.locked"), "warn");
    if (f.id === "egress" && getAt(settings, f.path) === "warp") return <EgressNote nodeIds={nodeIds} />;
    // behind a CDN the mode "auto" is sent to clients as packet-up (the panel does it too)
    if (isVless && f.id === "xhttp.mode" && getAt(settings, ["security"]) === "tls" && String(getAt(settings, ["cdn", "host"]) ?? "").trim() !== "" && getAt(settings, f.path) === "auto") return note(t("profiles.vless.mode.cdn"));
    // TCP 443 is also where a Hysteria2 profile on the same node keeps its site (design/vless.md §7.2)
    if (isVless && f.id === "port" && Number(getAt(settings, f.path)) === 443) {
      const tls = getAt(settings, ["security"]) === "tls";
      const self = !tls && getAt(settings, ["reality", "target_kind"]) === "self";
      return (
        <>
          {note(t(tls ? "profiles.vless.port443.tls" : self ? "profiles.vless.port443.self" : "profiles.vless.port443.site"), self ? undefined : "warn")}
          <VlessHy2Docs />
        </>
      );
    }
    if (info.id === "hysteria2" && f.id === "masquerade.type") return <VlessHy2Docs />;
    return null;
  };
  const onChange = isAwg ? (n: typeof settings) => form.edit(handTyped(settings, n)) : isVless ? (n: typeof settings) => form.edit(vlessEdited(settings, n)) : form.edit;

  // the remarks: what the form cannot be saved with, then what the server only warns about
  const warns: Remark[] = (preview.data?.warnings ?? []).map((w: FieldError, i) => ({ id: `w${i}`, g: "part", text: warnText(t, w), pointer: w.pointer }));
  const errs: Remark[] = problems.map((p: Problem, i) => ({ id: `e${i}`, g: "bad", text: ((p.code && (t.opt(`field.${info.id}${p.pointer.replaceAll("/", ".")}.${p.code}`) ?? undefined)) || p.message), pointer: p.pointer }));
  const remarks = [...errs, ...warns];

  const nameRow = (
    <div className={cx("pp-row", form.nameError && form.nameProblem && "err")} data-k="name">
      <div className="pp-lb">
        <b>
          {t("profiles.name")}
          <i className={cx("pp-dot", form.name !== ctx.data.initial.name && "on")} aria-hidden />
        </b>
        <span>{t("pf.st.nameHint")}</span>
      </div>
      <div className="pp-ct">
        <input
          className="an-i"
          type="text"
          aria-label={t("profiles.name")}
          value={form.name}
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => form.rename(e.target.value)}
        />
        {form.nameError && form.nameProblem && (
          <p className="pp-ferr" role="alert">
            <Gl k="bad" />
            {t("profiles.nameRequired")}
          </p>
        )}
      </div>
    </div>
  );

  const critical = form.changed.filter((f) => f.critical).length;
  const bar = stale ? t("pf.st.barOld") : problems.length > 0 ? t("pf.st.barFix") : t("pf.st.barChanged", { n: count }) + (critical ? ` · ${t("pf.st.barCrit", { n: critical })}` : "");
  const barOn = !readOnly && (count > 0 || stale || saved);
  const onNodes = ctx.servers.length > 0;
  const view = preview.data;
  const fmts = view?.formats ?? [];
  const drops = dropsOf(fmts);
  const noteLines: { c: string; text: string }[] = [];
  if (trustTunnelOnly(info.apps)) noteLines.push({ c: "", text: t("profiles.tt.previewHint") });
  else if (telegramOnly(info.apps)) noteLines.push({ c: "", text: t("profiles.previewHint") });
  else noteLines.push({ c: "", text: t("profiles.previewHint") });

  return (
    <div className={cx("pf-g pp-grid", readOnly && "ro")}>
      <div className="col">
        {form.lossy && (
          <LossyNotice
            vars={form.lossy}
            busy={form.busy}
            anywayLabel={t("ports.anyway.save")}
            onTake={(p) => form.edit(setAt(settings, ["port"], p))}
            onAnyway={() => {
              form.setAnyway(true);
              void form.save(false, true);
            }}
          />
        )}
        {remarks.length > 0 && (
          <section className="card pp-sec pp-rmc">
            <div className="pp-sh">
              <span className="pp-ic sand" aria-hidden>
                <G d="M12 4l9 16H3zM12 10v4M12 17v.5" size={15} stroke={1.5} />
              </span>
              <h3>
                {t("pf.st.remarks")} <span className="mono">{remarks.length}</span>
              </h3>
            </div>
            {errs.length > 0 && <p className="pp-errh">{t.n("pf.st.cantSave", errs.length)}</p>}
            {remarks.map((r) => (
              <div key={r.id} className={cx("pp-rm", r.g)}>
                <Gl k={r.g} />
                <div>
                  <span>{r.text}</span>
                </div>
                <button type="button" className="an-lk" onClick={() => goField(fields, r.pointer)}>
                  {t("pf.st.toField")}
                </button>
              </div>
            ))}
          </section>
        )}
        <div className={cx("pp-form", readOnly && "ro")} inert={readOnly}>
          {isAwg && (
            <AwgPanel settings={settings} onChange={form.edit} advice={view && view.errors.length === 0 ? { warnings: view.warnings, score: view.obfuscationScore } : undefined} />
          )}
          <PageFields protocol={info.id} groups={groups} settings={settings} base={form.base} problems={problems} onChange={onChange} hidden={hidden} readOnly={readOnlyIds} offers={isVless ? vlessOffers : undefined} extra={extra} lead={nameRow} />
        </div>
        {!readOnly && (
          <div className={cx("pf-save pp-save", barOn && "on", saved && count === 0 && !stale && "ok")} role="status">
            <span>{saved && count === 0 && !stale ? t("pf.st.saved") : bar}</span>
            {count > 0 && (
              <Button size="md" disabled={form.busy} onClick={form.discard}>
                {t("pf.st.undo")}
              </Button>
            )}
            <Button
              size="md"
              variant="primary"
              aria-disabled={stale ? undefined : problems.length > 0 || form.busy}
              disabled={!stale && (problems.length > 0 || form.busy)}
              onClick={stale ? reloadPage : () => void form.save()}
            >
              {form.busy && <span className="an-sp" aria-hidden />}
              {stale ? t("stale.reload") : onNodes ? t("pf.st.saveAsk") : t("pf.st.save")}
            </Button>
          </div>
        )}
        {ctx.owner && (
          <section className="card pp-sec up-dz pp-dz">
            <div className="pp-sh">
              <h3>{t("pf.st.danger")}</h3>
            </div>
            <div className="pf-dz">
              <div>
                <b>{t("profiles.delete")}</b>
                <span>{onNodes ? t.n("pf.st.delSum", new Set(ctx.servers.map((s) => s.nodeId)).size) : t("pf.st.delSumNone")}</span>
              </div>
              <Button variant="danger" size="lg" className="up-del" onClick={() => ctx.openDlg("del")}>
                {t("users.delete")}
              </Button>
            </div>
          </section>
        )}
      </div>
      <aside className="col pp-as">
        <section className="card pp-sec">
          <div className="pp-sh">
            <span className="pp-ic mint" aria-hidden>
              <G d="M9 8l-4 4 4 4M15 8l4 4-4 4" size={15} stroke={1.5} />
            </span>
            <h3>{t("profiles.preview")}</h3>
          </div>
          <div className="pp-asb">
            <div className="pp-ash">
              <span>{view?.clientLabel}</span>
            </div>
            {preview.isError ? (
              <p className="pp-q1 bad">{errorText(preview.error, t)}</p>
            ) : (view?.errors.length ?? 0) > 0 ? (
              <small className="pf-dn">{t("pf.st.previewErrors")}</small>
            ) : (
              <pre className="pp-pre mono">{view ? view.clientPreview.split("\n").map((line, i) => <ConfLine key={i} text={line} />) : <span className="pp-q1">{t("profiles.previewBusy")}</span>}</pre>
            )}
            {trustTunnelOnly(info.apps) && !preview.isError && (view?.errors.length ?? 0) === 0 && <TrustTunnelLink settings={settings} />}
          </div>
        </section>
        <section className="card pp-sec">
          <div className="pp-sh">
            <span className="pp-ic lav" aria-hidden>
              <G d="M8 3h8v18H8zM11 18h2" size={15} stroke={1.5} />
            </span>
            <h3>{t("pf.st.apps")}</h3>
          </div>
          <div className="pp-asb">
            {!preview.isError && !ownLinkOnly(info.apps) && fmts.length > 0 && (
              <div className="pr-fm" role="group" aria-label={t("profiles.fmt.title")}>
                {fmts.map((f) => {
                  const d = drops.find((x) => x.formats.includes(f.format));
                  const w = d ? dropWords(d, t) : undefined;
                  return (
                    <span key={f.format} className={cx(!f.ok ? "no" : f.note ? "part" : "")} title={w ? `${w.text}. ${w.full}` : undefined}>
                      <G d={f.ok ? "M5 12.5l4.5 4.5L19 7.5" : "M6 6l12 12M18 6L6 18"} size={14} stroke={2} />
                      {nameOf(f.format, t)}
                    </span>
                  );
                })}
              </div>
            )}
            {noteLines.map((l, i) => (
              <p key={i} className={cx("pp-q1", l.c)}>
                {l.text}
              </p>
            ))}
            {isAwg && <p className="pp-q1">{t("pf.st.awgOld")}</p>}
          </div>
        </section>
      </aside>
      <ImpactDialog ctx={ctx} form={form} t={t} />
    </div>
  );
}

/** What a changed field now holds, as the window says it: secrets as dots, a flag as on/off, a range as its two ends. */
function valueOf(f: Field, settings: Settings, t: Tx): string {
  if (f.secret || f.kind === "secret" || f.kind === "generate") return "••••";
  if (f.kind === "range") return f.parts!.map((p) => String(getAt(settings, p) ?? "")).join("–");
  const v = getAt(settings, f.path);
  if (f.kind === "toggle") return v ? t("pf.st.flagOn") : t("pf.st.flagOff");
  if (Array.isArray(v)) return v.length > 4 ? t.n("pf.st.items", v.length) : v.join(", ") || "—";
  if (f.kind === "choice") return f.choices.find((c) => c.raw === v || c.value === String(v))?.label ?? String(v ?? "—");
  return v === undefined || v === null || v === "" || v === MASK ? "—" : String(v);
}

/**
 * "Save changes?" with what they really do (the board's window): a key-based profile (AmneziaWG) names the keys that stop working; a profile
 * the subscription carries (Hysteria2) breaks for everyone who has it until their app refreshes the subscription, so it says that, with the
 * ways around it; a Telegram link (MTProto) refreshes by nothing, so people are sent the new one; otherwise the people online reconnect.
 */
function ImpactDialog({ ctx, form, t }: { ctx: TabCtx; form: ProfileForm; t: Tx }) {
  const data = ctx.data;
  const { info, profile } = data;
  const i: ProfileImpact | undefined = form.impact?.impact;
  const byTg = telegramOnly(info.apps);
  const byTt = trustTunnelOnly(info.apps);
  const bySub = !byTg && !byTt && wayOf(info.id, [{ id: info.id, apps: info.apps }]) === "sub";
  const subs = useQuery({ ...settingsQuery, enabled: form.impactOpen && bySub });
  const hours = subs.data?.settings.updateIntervalHours || 12;
  const broken = !!i && (bySub || byTg || byTt) && i.criticalFields.length > 0;
  const people = Math.max(profile.userCount, i?.affectedUserNames.length ?? 0);
  const [names, setNames] = useState(false);
  const head: string = !i
    ? ""
    : i.devicesNeedReissue > 0
      ? t.n("profiles.crit.headDevices", i.devicesNeedReissue)
      : broken
        ? i.inboundsRestarted > 0
          ? byTg
            ? t.n("profiles.crit.headTg", people)
            : byTt
              ? t.n("profiles.crit.headTt", people)
              : t.n("profiles.crit.headSub", people, { h: hours })
          : t("profiles.crit.headNowhere")
        : i.usersOnline > 0
          ? t.n("profiles.crit.headOnline", i.usersOnline)
          : t("profiles.crit.headNone");
  const bad = !!i && (i.devicesNeedReissue > 0 || broken);
  const nameChanged = form.name !== data.initial.name;
  const where = ctx.servers.length > 0 ? t("pf.st.whereOn", { n: new Set(ctx.servers.map((x) => x.nodeId)).size, list: ctx.servers.map((x) => x.nodeName).join(", ") }) : t("pf.st.whereNone");
  const [busy, setBusy] = useState(false);
  const go = async () => {
    setBusy(true);
    try {
      await form.save(true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <DlgFrame
      open={form.impactOpen}
      onClose={form.closeImpact}
      busy={busy}
      title={t("profiles.crit.title")}
      shot="impact"
      footer={
        <>
          <Button onClick={form.closeImpact}>{t("common.cancel")}</Button>
          <Button variant="primary" disabled={busy} onClick={() => void go()}>
            {busy && <span className="an-sp" aria-hidden />}
            {i && i.devicesNeedReissue > 0 ? t("profiles.crit.applyReissue") : t("profiles.crit.apply")}
          </Button>
        </>
      }
    >
      {i && (
        <div className="us-db pp-db">
          <span className="rw-sl">{t("pf.st.changedL")}</span>
          {nameChanged && (
            <div className="pp-chg">
              <b>{t("profiles.name")}</b>
              <span className="mono">{form.name}</span>
            </div>
          )}
          {form.changed.map((f) => (
            <div className="pp-chg" key={f.key}>
              <b>{t.opt(`profiles.f.${info.id}.${f.id}.title`) ?? f.title}</b>
              <span className="mono">{valueOf(f, form.settings, t)}</span>
              {f.critical && <i className="pp-crit on">{t("pf.st.crit")}</i>}
            </div>
          ))}
          <span className="rw-sl">{t("pf.st.where")}</span>
          <p>{where}</p>
          <span className="rw-sl">{t("pf.st.people")}</span>
          <div className={cx("pp-imp", bad ? "bad" : "ok")}>
            <Gl k={bad ? "bad" : "ok"} />
            <div>
              <span>{head}</span>
              {names && <em>{i.affectedUserNames.join(", ")}</em>}
            </div>
            {i.affectedUserNames.length > 0 && (
              <button type="button" className="an-lk" aria-expanded={names} onClick={() => setNames(!names)}>
                {names ? t("pf.st.hideNames") : t("pf.st.showNames")}
              </button>
            )}
          </div>
          {i.inboundsRestarted > 0 && (
            <div className="pp-imp none">
              <Gl k="none" />
              <div>
                <span>{t.n("profiles.crit.inbounds", i.inboundsRestarted)}</span>
              </div>
            </div>
          )}
          {broken && i.inboundsRestarted > 0 && (
            <div className="pp-imp none">
              <Gl k="none" />
              <div>
                <span>{t(byTg ? "profiles.crit.tgAdvice" : byTt ? "profiles.crit.ttAdvice" : "profiles.crit.subAdvice")}</span>
              </div>
            </div>
          )}
        </div>
      )}
    </DlgFrame>
  );
}
