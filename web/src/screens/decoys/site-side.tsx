import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { deliveryHint, type Delivery, type Node, type Site, type SiteCheck, type SiteVersion } from "@/lib/decoysites";
import { useFmt, type Fmt } from "@/lib/format";
import { asSource, CardHead, DeliveryWord, DzCard, Gl, TargetMark, usageText, whenText } from "./parts";

// The right-hand column of a site's page: where it stands, what its checks say, its versions, and a few facts.

export type Place = { key: string; panel?: boolean; node?: Node; name: string; delivery: Delivery };

/** Where the site stands: the nodes with the delivery of each, the panel, and "Take off" on a row of a site that can be taken off. */
export function PlacementCard({ places, draft, onOff }: { places: readonly Place[]; draft: boolean; onOff?: (p: Place) => void }) {
  const t = useT();
  const nodes = places.filter((p) => !p.panel).length;
  const sum = places.length ? usageText(t, nodes, places.some((p) => p.panel)) : "";
  return (
    <DzCard className="dz-pl" data-shot="decoy-placement">
      <CardHead title={t("dz.pl")}>
        <span className="dz-lc">{sum}</span>
      </CardHead>
      {places.length === 0 && <p className="dz-emp">{draft ? t("dz.pl.noneDraft") : t("dz.pl.none")}</p>}
      {places.map((p) => {
        const hint = deliveryHint(t, p.delivery);
        return (
          <div key={p.key} className="dz-plr">
            <TargetMark node={p.node} panel={p.panel} />
            <div>
              <b className={p.panel ? "" : "dz-m"}>{p.panel ? t("dz.pl.panel") : p.name}</b>
              {p.node?.address && (
                <span className="dz-m" dir="ltr">
                  {p.node.address}
                </span>
              )}
            </div>
            <DeliveryWord d={p.delivery} />
            {onOff && (
              <Button variant="secondary" size="md" onClick={() => onOff(p)}>
                {t("dz.pl.off")}
              </Button>
            )}
            {hint && p.delivery.kind !== "ok" && <p>{hint}</p>}
          </div>
        );
      })}
    </DzCard>
  );
}

// ---- the checks

const warningCodes = ["no_404", "external_urls", "telltale_words", "scripts_or_forms", "duplicate_title", "few_pages", "shared_domains"] as const;
type WarningCode = (typeof warningCodes)[number];
const isWarningCode = (c: string): c is WarningCode => (warningCodes as readonly string[]).includes(c);

export type CheckLine = { kind: "ok" | "part" | "bad"; title: string; text?: string; mono?: string };
export type CheckLines = { required: CheckLine[]; warnings: CheckLine[]; passed: CheckLine[]; failed: boolean };

/**
 * What the checks of a version say, as lines. The required ones are the bundle rules (the panel keeps no version that breaks
 * them, so they read as passed, unless a page is shown one that does); the warnings are the ones that fired, with the files that
 * fired them; the passed warnings are folded away. "Stands on nodes of different domains" is the page's own reading of the
 * addresses of the nodes (the panel decides it for sure when the site is assigned).
 */
export function checkLines(t: T, fmt: Fmt, site: Pick<Site, "checks" | "source" | "sizeBytes" | "fileCount">, domains: readonly string[]): CheckLines {
  const find = (code: string): SiteCheck | undefined => site.checks.find((c) => c.level === "required" && c.code === code);
  const ok = (code: string) => find(code)?.ok ?? true;
  const sizeOk = ok("size") && ok("file_count");
  const required: CheckLine[] = [
    ok("index_html") ? { kind: "ok", title: t("dz.ck.indexOk") } : { kind: "bad", title: t("dz.ck.indexBad") },
    sizeOk ? { kind: "ok", title: t("dz.ck.sizeOk"), text: t("dz.ck.sizeOkText", { size: fmt.bytes(site.sizeBytes, 1024), n: site.fileCount }) } : { kind: "bad", title: t("dz.ck.sizeBad") },
    ok("safe_paths") ? { kind: "ok", title: t("dz.ck.pathsOk") } : { kind: "bad", title: t("dz.ck.pathsBad") },
  ];
  const fired = site.checks.filter((c) => c.level === "warning");
  const shared = domains.length > 1 && !fired.some((c) => c.code === "shared_domains");
  const warnings: CheckLine[] = fired.map((c) => {
    if (!isWarningCode(c.code)) return { kind: "part", title: t("dz.ck.unknown", { code: c.code }), mono: c.paths.join(", ") };
    return {
      kind: "part",
      title: t(`dz.ck.${c.code}`),
      text: c.code === "few_pages" ? t("dz.ck.few_pages.s") : t(`dz.ck.${c.code}.s` as MessageKey),
      mono: c.paths.join(", "),
    };
  });
  if (shared) warnings.push({ kind: "part", title: t("dz.ck.shared_domains"), text: t("dz.ck.shared_domains.s"), mono: domains.join(", ") });
  const firedCodes = new Set([...fired.map((c) => c.code), ...(shared ? ["shared_domains"] : [])]);
  // "fewer than three pages" is asked of the assistant's sites only; the others are asked of every site
  const asked = warningCodes.filter((c) => c !== "few_pages" || asSource(site.source) === "assistant");
  const failed = required.some((l) => l.kind === "bad");
  const passed: CheckLine[] = failed ? [] : asked.filter((c) => !firedCodes.has(c)).map((c) => ({ kind: "ok" as const, title: t(`dz.ck.${c}.ok` as MessageKey) }));
  return { required, warnings, passed, failed };
}

/** The checks of the current version: the required ones, the notes that do not stop assigning, and "N more passed" that opens on a click. */
export function ChecksCard({ lines, open: demoOpen }: { lines: CheckLines; open?: boolean }) {
  const t = useT();
  const [open, setOpen] = useState(!!demoOpen);
  const head = lines.failed ? t("dz.ck.fail") : lines.warnings.length ? t.n("dz.ck.warns", lines.warnings.length) : t("dz.ck.good");
  const shown: ({ hd: string } & Partial<CheckLine> | CheckLine)[] = [
    { hd: t("dz.ck.req") },
    ...lines.required,
    ...(lines.warnings.length ? [{ hd: t("dz.ck.warn") }, ...lines.warnings] : []),
    ...(open ? lines.passed : []),
  ];
  return (
    <DzCard className="dz-ck" data-shot="decoy-checks">
      <CardHead title={t("dz.ck")}>
        <span className="dz-lc">{head}</span>
      </CardHead>
      <ul className="dz-ckl">
        {shown.map((l, i) =>
          "hd" in l ? (
            <li key={`h${i}`} className="hd">
              {l.hd}
            </li>
          ) : (
            <li key={`${i}${l.title}`} className={l.kind}>
              <Gl kind={l.kind} />
              <div>
                <b>{l.title}</b>
                {l.text && <span>{l.text}</span>}
                {l.mono && (
                  <span className="dz-m" dir="ltr">
                    {l.mono}
                  </span>
                )}
              </div>
            </li>
          ),
        )}
      </ul>
      {lines.passed.length > 0 && (
        <button type="button" className="dz-lk dz-ckm" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? t("dz.ck.hide") : t.n("dz.ck.more", lines.passed.length)}
        </button>
      )}
    </DzCard>
  );
}

// ---- the versions and the facts

/** The kept versions, newest first; "Revert" on every one but the current. */
export function VersionsCard({ versions, max, onBack }: { versions: readonly SiteVersion[]; max: number; onBack: (v: SiteVersion) => void }) {
  const t = useT();
  const fmt = useFmt();
  const list = [...versions].sort((a, b) => b.seq - a.seq);
  return (
    <DzCard className="dz-vr" data-shot="decoy-versions">
      <CardHead title={t("dz.vr")}>
        <span className="dz-lc">{t("dz.vr.of", { n: list.length, max })}</span>
      </CardHead>
      {list.map((v) => (
        <div key={v.seq} className={cx("dz-vrr", v.current && "now")}>
          <i aria-hidden />
          <div>
            <b>{whenText(fmt, t, v.createdUnix)}</b>
            <span>
              {v.createdBy} · <span className="dz-m">{fmt.bytes(v.sizeBytes, 1024)}</span>
            </span>
          </div>
          {v.current ? (
            <span className="inline-flex h-6 items-center rounded-chip bg-step px-2 text-xs text-sec">{t("dz.vr.cur")}</span>
          ) : (
            <Button variant="secondary" size="md" onClick={() => onBack(v)}>
              {t("dz.vr.back")}
            </Button>
          )}
        </div>
      ))}
    </DzCard>
  );
}

/** "In short": where the site came from, when it was made and changed, and its hash. */
export function FactsCard({ site }: { site: Site }) {
  const t = useT();
  const fmt = useFmt();
  const src = asSource(site.source);
  const rows: { l: string; v: string; mono?: boolean }[] = [
    { l: t("dz.fact.source"), v: src === "upload" ? t("dz.fact.upload") : src === "assistant" ? t("dz.fact.assistant") : t("dz.fact.mcp", { token: site.sourceLabel }) },
    { l: t("dz.fact.created"), v: t("dz.fact.by", { when: whenText(fmt, t, site.createdUnix), who: site.createdBy }) },
    { l: t("dz.fact.changed"), v: t("dz.fact.by", { when: whenText(fmt, t, site.updatedUnix), who: site.updatedBy }) },
    { l: t("dz.fact.sha"), v: `${site.bundleSha256.slice(0, 8)}…`, mono: true },
  ];
  return (
    <DzCard>
      <CardHead title={t("dz.facts")} />
      <dl className="dz-facts">
        {rows.map((r) => (
          <div key={r.l}>
            <dt>{r.l}</dt>
            <dd className={r.mono ? "dz-m" : ""} dir={r.mono ? "ltr" : undefined}>
              {r.v}
            </dd>
          </div>
        ))}
      </dl>
    </DzCard>
  );
}

