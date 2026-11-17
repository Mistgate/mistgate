import { createContext, useCallback, useContext, useLayoutEffect, useState, type FormEvent, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import type { StatusKind } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { minutesText } from "@/lib/format";
import { Dot, Gl, P, Pill, type DotKind } from "@/screens/health/page-parts";
import type { SourceId } from "./search";

// The parts every source card is made of, in the board's marks (PnReach + pn.css: .rc-sd, .rc-pb, .pp-row, .rc-set, .rc-go, .rc-sf; the
// rules are in ./reach.css, all under .hlp): the open source's frame with its head, its last line and its problem; a row of a setting; the
// write-only secret; the row of buttons at the foot; the windows that ask first. The state of a source reaches the page by `useLook`: the
// card says it once, and the frame's pill, the row of the list, the tab's count and the notes over the nodes all show the same word.

/** Each source in the list: its name, what it is in a few words, and whether it looks from Russia only. */
export const meta: Record<SourceId, { title: MessageKey; what: MessageKey; ruOnly: boolean }> = {
  globalping: { title: "gp.title", what: "src.what.globalping", ruOnly: false },
  lists: { title: "bl.title", what: "src.what.lists", ruOnly: true },
  tspu: { title: "tp.title", what: "src.what.tspu", ruOnly: true },
  cheburcheck: { title: "cc.title", what: "src.what.cheburcheck", ruOnly: true },
  bsbord: { title: "bb.title", what: "src.what.bsbord", ruOnly: true },
  klick: { title: "kc.title", what: "src.what.klick", ruOnly: false },
  probes: { title: "pr.title", what: "src.what.probes", ruOnly: false },
};

/** What a source's state says. `tag` names the cases a problem box has its own words for. */
export type Look = { state: string; kind: StatusKind; word: string; detail: string; tag?: string };

/** The card reports its look up to the list (SourceCards) and to its frame. */
export const ReportLook = createContext<((look: Look) => void) | null>(null);
const CurrentLook = createContext<Look | null>(null);
/** Inside the page: the shared rules are on the page's root; a card drawn alone brings its own. */
export const InPage = createContext(false);

/** The root of the page's rules around a part drawn without the page (a card in the dev kit). */
export function Scope({ children }: { children: ReactNode }) {
  return useContext(InPage) ? <>{children}</> : <div className="hlp rcp">{children}</div>;
}

const same = (a: Look | null, b: Look) => !!a && a.state === b.state && a.kind === b.kind && a.word === b.word && a.detail === b.detail && a.tag === b.tag;

/** Says the card's state once it changes (before the paint: the row, the pill and the line change in the same frame). */
export function useLook(look: Look | null) {
  const report = useContext(ReportLook);
  const { state, kind, word, detail, tag } = look ?? ({} as Partial<Look>);
  useLayoutEffect(() => {
    if (state !== undefined && kind !== undefined && word !== undefined && detail !== undefined) report?.({ state, kind, word, detail, ...(tag && { tag }) });
  }, [report, state, kind, word, detail, tag]);
}

/** The list's word for a state, short enough for a row: works, off, an error, needs setup; and a few words on why or when. */
export function chipOf(t: T, look: Look): { kind: StatusKind; word: string; note: string } {
  const head = look.detail.split(" · ")[0] ?? "";
  // a state line says "Does not work · why" or names its status itself ("The site asked to slow down · …"): the row takes the why
  const why = [t("cr.state.works"), t("cr.state.fails"), t("cr.state.on"), t("cr.testing")].includes(look.word) ? head : look.word;
  switch (look.kind) {
    case "ok":
      return { kind: "ok", word: t("cr.state.works"), note: head };
    case "off":
      return { kind: "off", word: look.word, note: "" };
    case "bad":
      return { kind: "bad", word: t("src.chip.error"), note: why };
    case "warn":
      return { kind: "warn", word: t("status.warn"), note: why };
    case "busy":
      return { kind: "busy", word: t("src.chip.busy"), note: why };
    case "blip":
      return look.state === "noNode" ? { kind: "blip", word: t("src.chip.setup"), note: head } : { kind: "blip", word: t("cr.state.on"), note: why };
  }
}

/** A state as the board's pill and dot. */
export const pillOf: Record<StatusKind, { kind: "ok" | "part" | "bad" | "busy" | ""; glyph: DotKind }> = {
  ok: { kind: "ok", glyph: "ok" },
  off: { kind: "", glyph: "none" },
  bad: { kind: "bad", glyph: "bad" },
  warn: { kind: "part", glyph: "part" },
  busy: { kind: "busy", glyph: "busy" },
  blip: { kind: "busy", glyph: "busy" },
};
export const dotOf = (k: StatusKind): DotKind => pillOf[k].glyph;

export function SrcPill({ look, className }: { look: Look; className?: string }) {
  const t = useT();
  const chip = chipOf(t, look);
  const p = pillOf[chip.kind];
  return (
    <Pill kind={p.kind} glyph={p.glyph} className={className}>
      {chip.word}
    </Pill>
  );
}

/** "Russia only" or "any country": the sources that need Russia say so before anyone opens them. */
export function Coverage({ ruOnly }: { ruOnly: boolean }) {
  const t = useT();
  return <span className="rc-scope">{t(ruOnly ? "src.cover.ru" : "src.cover.any")}</span>;
}

/**
 * The open source: a card with its head (the globe, the name, where it looks from, the state's pill), what it does, its last line (the
 * state's own words), then what the card puts in it. Whoever draws it says the state with `useLook`.
 */
export function Detail({ id, sub, children }: { id: SourceId; sub: string; children: ReactNode }) {
  const t = useT();
  const m = meta[id];
  const up = useContext(ReportLook);
  const [look, setLook] = useState<Look | null>(null);
  const report = useCallback(
    (l: Look) => {
      setLook((was) => (same(was, l) ? was : l));
      up?.(l);
    },
    [up],
  );
  return (
    <Scope>
      <section className="card pp-sec sb-sec rc-sd" data-source={id}>
        <div className="pp-sh">
          <span className="pp-ic sky" aria-hidden="true">
            <Gl d={P.globe} />
          </span>
          <h3>{t(m.title)}</h3>
          <Coverage ruOnly={m.ruOnly} />
          {look && <SrcPill look={look} />}
        </div>
        <p className="sb-lead rc-desc">{sub}</p>
        {look && (
          <p className="sb-lead rc-last" role="status" data-state={look.state} data-word={look.word}>
            {look.detail}
          </p>
        )}
        <ReportLook.Provider value={report}>
          <CurrentLook.Provider value={look}>{children}</CurrentLook.Provider>
        </ReportLook.Provider>
      </section>
    </Scope>
  );
}

/** The source is loading, or could not be read: in the place of its rows. */
export function Loading({ q }: { q: { isPending: boolean; isError: boolean; data: unknown; error: Error | null; refetch: () => unknown } }) {
  if (q.isPending) return <Pending compact className="rc-wait" />;
  if (q.isError && !q.data) return <QueryError compact error={q.error} onRetry={() => void q.refetch()} className="rc-wait" />;
  return null;
}

/** What the state's problem says, when it has one: a title, what it means for the checks, and the way out when there is one. */
export type Problem = { tone: "bad" | "part"; title: string; body: string };

/** The words of a problem: the state's own word or reason, and what it does to the checks (a few cases have words of their own). */
export function problemOf(t: T, look: Look | null): Problem | null {
  if (!look || (look.kind !== "bad" && look.kind !== "warn")) return null;
  const head = look.detail.split(" · ")[0] ?? "";
  const generic = look.word === t("cr.state.fails");
  const special: Record<string, MessageKey> = {
    "gp.token": "rp.pb.gpToken",
    "gp.limit": "rp.pb.gpLimit",
    "bb.cap": "rp.pb.bbCap",
    "tp.node": "rp.pb.tpNode",
  };
  return {
    tone: look.kind === "bad" ? "bad" : "part",
    title: generic ? head || look.word : look.word,
    body: look.tag && special[look.tag] ? t(special[look.tag]!) : t(look.kind === "bad" ? "rp.pb.bad" : "rp.pb.warn"),
  };
}

/** The box of a problem in the open source (the board's `.rc-pb`): the dot, the title, what it means, and the button that mends it. */
export function ProblemBox({ action }: { action?: ReactNode }) {
  const t = useT();
  const p = problemOf(t, useContext(CurrentLook));
  if (!p) return null;
  return (
    <div className={cx("rc-pb", p.tone)} role="alert" data-testid="source-problem">
      <Dot kind={p.tone === "bad" ? "bad" : "part"} />
      <div>
        <b>{p.title}</b>
        <span>{p.body}</span>
      </div>
      {action}
    </div>
  );
}

/** The rows of the settings: a label and what it means on the left, the control on the right (the board's `.pp-rows`). */
export function Rows({ children }: { children: ReactNode }) {
  return <div className="pp-rows">{children}</div>;
}

export function PRow({ label, hint, wide, children }: { label: string; hint?: string; wide?: boolean; children: ReactNode }) {
  return (
    <div className={cx("pp-row", wide && "wide")}>
      <div className="pp-lb">
        <b>{label}</b>
        {hint && <span>{hint}</span>}
      </div>
      <div className="pp-ct">{children}</div>
    </div>
  );
}

/** "What and where it goes": what leaves the panel with the source on, in every state. */
export function Privacy({ children }: { children: string }) {
  const t = useT();
  return (
    <div className="rc-go">
      <span className="rc-dt">{t("rp.go")}</span>
      <p>{children}</p>
    </div>
  );
}

/**
 * Write-only: with a secret stored the row says "set" and offers to replace (or remove) it; the secret itself never comes back.
 * `signal` (a counter) opens the replacing from outside: the button of a problem box that says "Replace the token".
 */
export function SecretField(p: {
  label: string;
  hint: string;
  has: boolean;
  testId: string;
  busy: boolean;
  valid: (value: string) => boolean;
  invalid: string;
  saveLabel: string;
  onSave: (value: string, done: () => void) => void;
  onRemove?: () => void;
  signal?: number;
}) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [tried, setTried] = useState(false);
  // a new signal opens the replacing (set while drawing, not in an effect: the field is there in the same frame)
  const [seen, setSeen] = useState(p.signal ?? 0);
  if ((p.signal ?? 0) !== seen) {
    setSeen(p.signal ?? 0);
    setEditing(true);
  }

  function stop() {
    setEditing(false);
    setValue("");
    setTried(false);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (p.busy || !p.valid(value)) return;
    p.onSave(value.trim(), stop);
  }

  if (p.has && !editing)
    return (
      <PRow label={p.label}>
        <div className="rc-set">
          <span className="mono" data-testid={p.testId}>
            {t("gp.token.set")}
          </span>
          <Button size="md" onClick={() => setEditing(true)}>
            {t("gp.token.replace")}
          </Button>
          {p.onRemove && (
            <Button variant="ghostDanger" size="md" disabled={p.busy} onClick={p.onRemove}>
              {t("cc.token.remove")}
            </Button>
          )}
        </div>
      </PRow>
    );

  return (
    <PRow label={p.label} hint={p.hint}>
      <form onSubmit={submit} noValidate className="rc-set edit">
        <TextField
          aria-label={p.label}
          error={tried && !p.valid(value) ? p.invalid : undefined}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          type="password"
          autoComplete="off"
          spellCheck={false}
          mono
          className="rc-fld"
        />
        <span className="rc-btns">
          <Button type="submit" size="md" disabled={p.busy}>
            {p.busy && value ? t("gp.token.saving") : p.saveLabel}
          </Button>
          {p.has && (
            <Button variant="ghost" size="md" disabled={p.busy} onClick={stop}>
              {t("common.cancel")}
            </Button>
          )}
        </span>
      </form>
    </PRow>
  );
}

/** Whole hours from an hour on ("every 2 h", "every 24 h"), minutes below: a day reads as 24 hours, not as "1 d". */
export const everyText = (t: T, m: number) => (m >= 60 && m % 60 === 0 ? `${m / 60} ${t("unit.h")}` : minutesText(t, m));

/** Minutes between rounds or runs. Globalping may space its rounds out further when the hourly limit needs it. */
export function IntervalField({ current, options, onPick, label, hint }: { current: number; options: readonly number[]; onPick: (minutes: number) => void; label?: string; hint?: string }) {
  const t = useT();
  const minutes = options.includes(current) ? options : [...options, current].sort((a, b) => a - b);
  const name = label ?? t("cr.interval");
  return (
    <PRow label={name} hint={hint}>
      <div className="rc-sel">
        <Select
          compact
          aria-label={name}
          value={String(current)}
          onValueChange={(v) => Number(v) !== current && onPick(Number(v))}
          options={minutes.map((m) => ({ value: String(m), label: t("cr.interval.every", { time: everyText(t, m) }) }))}
        />
      </div>
    </PRow>
  );
}

/**
 * The foot of the card: "Test" on the left (a spinner while it runs), on the right "Pause" (stops the checks, keeps the settings: the
 * toast says so and takes it back), then "Disconnect" (forgets the secret and the settings, asks first) or "Switch on".
 */
export function Foot(p: {
  test?: { pending: boolean; onClick: () => void };
  /** a button of the card's own at the left, where "Test" stands (the own probes' "Add a probe") */
  lead?: ReactNode;
  /** the source is on */
  on: boolean;
  onSwitchOn?: () => void;
  switchBusy?: boolean;
  /** something is stored that "Disconnect" would forget (the secret, the node, the units) or the source is on */
  forget?: () => void;
  pause?: () => void;
  /** a source that cannot be switched on yet says why in its state line */
  blocked?: boolean;
}) {
  const t = useT();
  return (
    <div className="rc-sf">
      {p.test && (
        <Button size="md" data-act="test" className={cx(p.test.pending && "busy")} disabled={p.test.pending} onClick={p.test.onClick}>
          {p.test.pending && <span className="an-sp" aria-hidden="true" />}
          {p.test.pending ? t("cr.testing") : t("cr.test")}
        </Button>
      )}
      {p.lead}
      <div className="rc-off">
        {p.on && p.pause && (
          <Button variant="ghost" size="md" data-act="pause" title={t("rp.pause.hint")} onClick={p.pause}>
            {t("rp.pause")}
          </Button>
        )}
        {!p.on && p.onSwitchOn && (
          <Button size="md" data-act="enable" disabled={p.switchBusy || p.blocked} onClick={p.onSwitchOn}>
            {t("rp.switchOn")}
          </Button>
        )}
        {p.forget && (
          <Button size="md" data-act="disconnect" onClick={p.forget}>
            {t("cr.disconnect")}
          </Button>
        )}
      </div>
    </div>
  );
}

/** Disconnect asks first: it forgets more than a pause does. */
export function ConfirmDisconnect({ title, body, busy, onClose, onConfirm }: { title: string; body: string; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={title}
      description={body}
      className="md:max-w-[480px]!"
      footer={
        <>
          <Button variant="ghost" size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={busy} onClick={onConfirm}>
            {t("cr.disconnect")}
          </Button>
        </>
      }
    />
  );
}

/** Switching a paid or third-party source on asks first: what leaves the panel and to whom. The step-up comes after. */
export function ConfirmOn(p: { title: string; body: string; busy: boolean; onClose: () => void; onConfirm?: () => void; confirm?: ReactNode; children?: ReactNode }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !p.busy && p.onClose()}
      title={p.title}
      description={p.body}
      className="md:max-w-[480px]!"
      footer={
        <>
          <Button variant="ghost" size="lg" disabled={p.busy} onClick={p.onClose}>
            {t("common.cancel")}
          </Button>
          {p.confirm ?? (
            <Button variant="primary" size="lg" disabled={p.busy} onClick={p.onConfirm}>
              {t("cr.switchOn")}
            </Button>
          )}
        </>
      }
    >
      {p.children}
    </Modal>
  );
}
