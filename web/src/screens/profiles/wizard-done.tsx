import { useState, type CSSProperties } from "react";
import { Button } from "@/components/ui/button";
import { cx } from "@/lib/cx";
import type { Tx } from "@/screens/users/t";
import type { Fix } from "./wizard-check";
import { State } from "./wizard-bits";
import { progressOf, runDone, type RunNode, type RunState } from "./wizard-run";

/** The line under a node that failed: the port or the domain to try again with, and "Repeat for de3" (the board's `.pp-apr .btn`). */
function Mend({ n, t, onRetry }: { n: RunNode; t: Tx; onRetry: (fix: Fix) => void }) {
  const [port, setPort] = useState(n.fix.port ?? n.free ?? "");
  const [sni, setSni] = useState(n.fix.sni ?? "");
  const lossy = n.lossy && port === n.lossy;
  const go = () => onRetry({ ...n.fix, ...(n.mend === "port" && port && { port, anyway: !!lossy }), ...(n.mend === "domain" && sni && { sni: sni.trim() }) });
  return (
    <div className="pw-apf">
      {n.mend === "port" && <input className="pw-i-field mono pw-pt" type="text" inputMode="numeric" value={port} aria-label={t("wiz.pt.aria")} onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))} />}
      {n.mend === "domain" && <input className="pw-i-field mono" type="text" placeholder="vpn.example.com" aria-label={t("wiz.dom.aria")} value={sni} autoCapitalize="off" spellCheck={false} onChange={(e) => setSni(e.target.value.trim())} />}
      {n.mend === "port" && n.lossy && !lossy && (
        <Button size="md" variant="ghost" onClick={() => setPort(n.lossy!)}>
          {t("ports.anyway.put")} · {n.lossy}
        </Button>
      )}
      <Button size="md" onClick={go}>
        {t("wiz.run.retry", { id: n.name })}
      </Button>
    </div>
  );
}

const glyphOf = (phase: RunNode["phase"]) => (phase === "ok" ? "ok" : phase === "bad" ? "bad" : phase === "put" || phase === "apply" ? "busy" : "none");

/** Step 5: the profile made, the groups, then each node until it runs (the board's `.pw-run`). `onFix`: back to the settings. */
export function DoneStep({ t, name, run, deliv, onRetry, onFix }: { t: Tx; name: string; run: RunState; deliv: string; onRetry: (id: string, fix: Fix) => void; onFix: () => void }) {
  const done = runDone(run);
  const ok = run.nodes.filter((n) => n.phase === "ok").length;
  const bad = run.nodes.filter((n) => n.phase === "bad").map((n) => n.name);
  const title = !done
    ? t("wiz.run.title", { name })
    : run.profile.phase === "bad"
      ? t("wiz.run.badTitle", { name })
      : run.nodes.length === 0
        ? t("wiz.run.noNodes")
        : t.n("wiz.run.on", ok, { total: run.nodes.length }) + (bad.length > 0 ? t("wiz.run.onFail", { ids: bad.join(", ") }) : "");
  const g = run.groups;
  const gNames = g.names.join(", ");
  const rowCls = (ph: string) => (ph === "ok" ? "done" : ph === "bad" ? "bad" : "");
  return (
    <>
      <h2 className="pw-q" role="status">
        {title}
      </h2>
      <section className="pw-card pw-apply pw-run" aria-busy={!done}>
        <i className="pw-pg" style={{ "--w": `${progressOf(run)}%` } as CSSProperties} aria-hidden="true" />
        <div className={cx("pw-apr", rowCls(run.profile.phase))}>
          <State kind={run.profile.phase === "busy" ? "busy" : run.profile.phase} />
          <span className="mono" />
          <span>{run.profile.phase === "busy" ? t("wiz.run.making") : run.profile.phase === "ok" ? t("wiz.run.made") : run.profile.text}</span>
          {run.profile.phase === "bad" && run.profile.fields && (
            <Button size="md" onClick={onFix}>
              {t("wiz.run.fixProfile")}
            </Button>
          )}
        </div>
        {run.profile.phase !== "bad" && (
          <div className={cx("pw-apr", rowCls(g.phase))}>
            <State kind={g.phase === "busy" ? "busy" : g.phase === "ok" ? "ok" : g.phase === "bad" ? "bad" : "none"} />
            <span className="mono" />
            <span>{g.phase === "none" ? t("wiz.run.groupsNone") : g.phase === "ok" ? t("wiz.run.groupsDone", { names: gNames }) : g.phase === "bad" ? t("wiz.run.groupsBad", { why: g.text }) : t("wiz.run.groupsDoing", { names: gNames })}</span>
          </div>
        )}
        {run.nodes.map((n) => (
          <div key={n.id}>
            <div className={cx("pw-apr", rowCls(n.phase))}>
              <State kind={glyphOf(n.phase)} />
              <span className="mono">{n.name}</span>
              <span>
                {n.phase === "wait" ? t("wiz.run.waiting") : n.phase === "put" ? t("wiz.run.putting") : n.phase === "apply" ? t("wiz.run.applying") : n.phase === "ok" ? t("wiz.run.ok") : n.text}
                {n.notes?.map((x) => (
                  <small key={x} className="pw-note">
                    {x}
                  </small>
                ))}
              </span>
              {n.phase === "bad" && !n.mend && (
                <Button size="md" onClick={() => onRetry(n.id, n.fix)}>
                  {t("wiz.run.retry", { id: n.name })}
                </Button>
              )}
            </div>
            {n.phase === "bad" && n.mend && <Mend n={n} t={t} onRetry={(fix) => onRetry(n.id, fix)} />}
          </div>
        ))}
      </section>
      {done && run.profile.phase === "ok" && (
        <section className="pw-card pw-sec pw-next">
          <div className="pw-sh">
            <h3>{t("wiz.next")}</h3>
          </div>
          <p className="pw-quiet">{deliv}</p>
        </section>
      )}
    </>
  );
}
