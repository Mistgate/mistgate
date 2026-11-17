import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { useToast } from "@/components/ui/toast";
import { profiles as profilesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { agentLinked } from "@/lib/node-status";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { networkOf } from "@/lib/ways-graph";
import { useT } from "@/i18n";
import { alsoRemovesText } from "@/screens/node/relays";
import { useTx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { DlgFrame } from "./pf-frame";
import { Gl } from "./pf-bits";
import type { TabCtx } from "./page-types";
import type { ServerRow } from "./profiles-model";
import { useTakeOff } from "./take-off";
import "./profile-tabs.css";

const TICK = "M5 12.5l4.5 4.5L19 7.5";
const gOf = (k: ServerRow["kind"]) => (k === "ok" || k === "part" ? "ok" : k === "bad" ? "bad" : k === "busy" ? "busy" : "none");
const spaced = (s: string) => s.trim().replace(/\s+/g, " ");

/** Why the server refused, sorted by what the window can say: the profile got a node meanwhile, the name did not match, the data moved, it is gone. */
function errKeyOf(e: unknown): "deployed" | "name" | "stale" | "gone" | "" {
  const c = ConnectError.from(e);
  const msg = c.rawMessage;
  if (c.code === Code.NotFound) return "gone";
  if (c.code === Code.FailedPrecondition) return /name|confirm/i.test(msg) ? "name" : /version|stale|changed/i.test(msg) ? "stale" : "deployed";
  if (c.code === Code.InvalidArgument && /name|confirm/i.test(msg)) return "name";
  if (c.code === Code.Aborted) return "stale";
  return "";
}

/**
 * "Take off nodes" and "Delete" (the board's pp-dlg pp-del): the profile's nodes as boxes to tick, what the click costs said line by line,
 * and one red button. With every node ticked a switch offers to delete the profile too (the name typed, as for a person); a profile that is
 * on no node is deleted at once. One RemoveProfileFromNodes call does it all in one transaction; a node that is not connected drops the
 * profile when it connects again. `mode` is where it was opened from: "take" ticks nothing, "del" ticks all and deletes.
 */
export function TakeDialog({ ctx, mode, onClose, onGone }: { ctx: TabCtx; mode: "take" | "del"; onClose: () => void; onGone: () => void }) {
  const t = useTx();
  const tt = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const { profile } = ctx.data;
  const v = ctx.variant.split(".");
  const rows = [...ctx.servers].sort((a, b) => a.nodeName.localeCompare(b.nodeName));
  const nodes = useQuery(nodesQuery);
  const relays = useQuery(relaysQuery("")).data ?? [];
  const nodeOf = new Map((nodes.data?.nodes ?? []).map((n) => [n.id, n]));
  const takeOff = useTakeOff(profile.id);
  const [touched, setTouched] = useState<Record<string, boolean> | null>(mode === "take" && !v.includes("all") ? {} : null);
  const sel = (r: ServerRow) => (touched ? !!touched[r.nodeId] : true);
  const chosen = rows.filter(sel);
  const all = rows.length > 0 && chosen.length === rows.length;
  const [delOn, setDelOn] = useState(mode === "del");
  const del = rows.length === 0 || (all && delOn);
  const [typed, setTyped] = useState(v.includes("typed") ? profile.name : "");
  const [busy, setBusy] = useState(v.includes("run"));
  const [errK, setErrK] = useState(v.find((x) => x.startsWith("err-"))?.slice(4) ?? "");
  const [errText, setErrText] = useState("");
  const need = del && rows.length > 0;
  const block = (rows.length > 0 && chosen.length === 0) || (need && spaced(typed) !== spaced(profile.name)) || errK === "gone";
  const linked = (id: string) => {
    const n = nodeOf.get(id);
    return n ? agentLinked(n.status) : true;
  };
  const away = chosen.filter((r) => !linked(r.nodeId)).length;
  const alsoRelays = alsoRemovesText(tt, relays.filter((r) => chosen.some((c) => c.inbound.id === r.inboundId)));
  const awg = profile.protocol === "awg";

  const toggle = (id: string) => {
    setErrK("");
    setTouched((x) => ({ ...(x ?? Object.fromEntries(rows.map((r) => [r.nodeId, true]))), [id]: !(x ? x[id] : true) }));
  };
  const setAll = () => {
    setErrK("");
    setTouched(Object.fromEntries(rows.map((r) => [r.nodeId, !all])));
  };

  const lines: { g: "ok" | "part" | "bad" | "none"; t: string }[] = [];
  if (rows.length === 0) {
    lines.push({ g: "none", t: t("ps.dl.noNode") });
    if (ctx.inGroups.length > 0) lines.push({ g: "part", t: t.n("ps.dl.leaveN", ctx.inGroups.length) });
  } else if (chosen.length > 0) {
    lines.push({ g: "bad", t: t.n("ps.dl.takes", chosen.length, { nodes: chosen.map((r) => r.nodeName).join(", ") }) });
    lines.push({ g: "bad", t: t("ps.dl.cut") });
    if (alsoRelays) lines.push({ g: "bad", t: alsoRelays });
    if (away > 0) lines.push({ g: "part", t: tt.n("where.offOffline", away) });
    if (del) lines.push({ g: "bad", t: t(awg ? "ps.dl.leaveAwg" : "ps.dl.leave") });
    else if (awg) lines.push({ g: "ok", t: t("ps.dl.awgKeep") });
  }

  const err = errK ? { title: t(`ps.dl.err.${errK}.t` as "ps.dl.err.gone.t"), body: errK === "partial" ? t("ps.dl.err.partial.b", { node: chosen[0]?.nodeName ?? "" }) : errK === "other" ? errText : t(`ps.dl.err.${errK}.b` as "ps.dl.err.gone.b") } : null;

  async function go() {
    if (block || busy) return;
    setBusy(true);
    setErrK("");
    try {
      if (rows.length === 0) {
        await profilesApi.deleteProfile({ profileId: profile.id });
        await qc.invalidateQueries({ queryKey: ["profiles"] });
        toast(tt("profiles.deleted"));
        onGone();
        return;
      }
      const res = await takeOff(chosen.map((r) => r.nodeId), del ? { name: profile.name } : undefined);
      if (res.profileDeleted) {
        toast(tt("profiles.deleted"));
        onGone();
      } else if (res.removed < chosen.length) {
        setErrK("partial");
        setTouched(null);
        setDelOn(false);
      } else {
        toast(tt.n("where.offDone", res.removed));
        onClose();
      }
    } catch (e) {
      const k = errKeyOf(e);
      setErrK(k || "other");
      setErrText(errorText(e, tt));
      if (k === "deployed" || k === "stale") await qc.invalidateQueries({ queryKey: ["profiles"] });
    } finally {
      setBusy(false);
    }
  }

  const head = del ? t("ps.dl.delT", { name: profile.name }) : t("ps.dl.takeT", { name: profile.name });
  const btn = busy ? t(del ? "ps.dl.deleting" : "ps.dl.taking") : rows.length === 0 ? t("users.delete") : chosen.length === 0 ? t("ps.dl.pickNodes") : del ? t("ps.dl.takeDel") : t.n("ps.dl.takeN", chosen.length);
  return (
    <DlgFrame
      open
      onClose={onClose}
      title={head}
      className="pp-del"
      busy={busy}
      shot="take-dialog"
      footer={
        <>
          <span className="pp-xnf">{t("ps.dl.irrev")}</span>
          <Button size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button size="lg" className="rw-dng" aria-disabled={block || busy} onClick={() => void go()}>
            {btn}
          </Button>
        </>
      }
    >
      <div className="us-db pp-db">
        {err && (
          <div className="pp-xne" role="alert">
            <Gl k="bad" />
            <div>
              <b>{err.title}</b>
              <span>{err.body}</span>
            </div>
          </div>
        )}
        {rows.length > 0 && !busy && (
          <>
            <div className="pp-xnh">
              <span>{chosen.length > 0 ? t("ps.dl.chosen", { k: chosen.length, n: rows.length }) : t("ps.dl.pickHint")}</span>
              <button type="button" className="an-lk" onClick={setAll}>
                {all ? t("ps.dl.clear") : t("ps.dl.all")}
              </button>
            </div>
            <div className="pp-xnl" role="group" aria-label={t("ps.dl.nodes")}>
              {rows.map((r) => {
                const on = sel(r);
                return (
                  <button key={r.nodeId} type="button" className={cx("pp-xnr", on && "on")} role="checkbox" aria-checked={on} onClick={() => toggle(r.nodeId)}>
                    <span className="pp-xnc" aria-hidden>
                      <G d={TICK} size={14} stroke={2.5} />
                    </span>
                    <Flag code={r.countryCode} size={14} />
                    <b className="mono">{r.nodeName}</b>
                    <span className="mono pp-xnp">
                      {networkOf(r.inbound.protocol) ?? "udp"}/{r.inbound.port}
                    </span>
                    <span className="pp-xns">
                      <Gl k={gOf(r.kind)} />
                      {t(`ps.dl.st.${r.kind}` as "ps.dl.st.ok")}
                    </span>
                    {!linked(r.nodeId) && <span className="tag warn">{t("ps.dl.offline")}</span>}
                  </button>
                );
              })}
            </div>
          </>
        )}
        {busy
          ? chosen.map((r) => (
              <div key={r.nodeId} className="pp-apr">
                <Gl k={linked(r.nodeId) ? "busy" : "part"} />
                <span className="mono">{r.nodeName}</span>
                <span>{linked(r.nodeId) ? t(del ? "ps.dl.run.del" : "ps.dl.run.take") : t("ps.dl.run.later")}</span>
              </div>
            ))
          : lines.map((l, i) => (
              <div key={i} className={cx("pp-imp", l.g)}>
                <Gl k={l.g} />
                <div>
                  <span>{l.t}</span>
                </div>
              </div>
            ))}
        {all && !busy && (
          <div className="pp-xnd">
            <button type="button" className="fz-sw" role="switch" aria-checked={delOn} aria-label={t("ps.dl.delToo")} onClick={() => (setDelOn(!delOn), setTyped(""), setErrK(""))}>
              <i />
            </button>
            <div>
              <b>{t("ps.dl.delToo")}</b>
              <span>{t("ps.dl.delTooBody")}</span>
            </div>
          </div>
        )}
        {need && !busy && (
          <label className="an-f">
            <span>{t("ps.dl.type")}</span>
            <input className="an-i" type="text" placeholder={profile.name} value={typed} autoComplete="off" spellCheck={false} onChange={(e) => (setTyped(e.target.value), errK === "name" && setErrK(""))} />
          </label>
        )}
      </div>
    </DlgFrame>
  );
}
