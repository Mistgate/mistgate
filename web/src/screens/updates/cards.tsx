import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { bundleErrorText, heroOf, type Bundle, type UpdateActions, type Updates } from "@/lib/updates";

const bundleStates: Record<BundleStatus, { kind: StatusKind; key: MessageKey }> = {
  [BundleStatus.UNSPECIFIED]: { kind: "off", key: "up.bundle.status.missing" },
  [BundleStatus.MISSING]: { kind: "off", key: "up.bundle.status.missing" },
  [BundleStatus.TRUSTED]: { kind: "ok", key: "up.bundle.status.trusted" },
  [BundleStatus.UNTRUSTED]: { kind: "bad", key: "up.bundle.status.untrusted" },
  [BundleStatus.NO_KEY]: { kind: "warn", key: "up.bundle.status.noKey" },
};

const card = "flex min-w-0 flex-col gap-3 rounded-card-lg border border-line bg-surface p-4 md:p-[18px]";

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline gap-3 text-[13px]">
      <dt className="w-28 flex-none text-xs text-muted">{label}</dt>
      <dd className="min-w-0 flex-1 break-words">{children}</dd>
    </div>
  );
}

function Code({ children }: { children: string }) {
  return (
    <pre className="overflow-x-auto rounded-field border border-line bg-canvas p-3 font-mono text-xs leading-relaxed whitespace-pre text-fg select-all">{children}</pre>
  );
}

const shortHash = (h: string) => h.slice(0, 12);

/** A quiet "Details ⌄" toggle: the fold of a card that keeps its reference facts out of the way. */
function Fold({ open, onToggle, label, controls }: { open: boolean; onToggle: () => void; label: string; controls: string }) {
  return (
    <button
      type="button"
      aria-expanded={open}
      aria-controls={controls}
      onClick={onToggle}
      className="-mr-1 flex h-7 flex-none items-center gap-1 rounded-ctl px-2 text-xs font-bold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
    >
      {label}
      <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
    </button>
  );
}

function BundleFiles({ bundle }: { bundle: Bundle }) {
  const t = useT();
  const fmt = useFmt();
  if (bundle.files.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-xs text-muted">{t("up.bundle.files")}</span>
      <ul className="flex flex-col divide-y divide-line rounded-field border border-line bg-surface">
        {bundle.files.map((f) => (
          <li key={f.name} className="flex flex-wrap items-center gap-x-3 gap-y-0.5 px-3 py-2 text-xs">
            <span className="min-w-0 flex-1 truncate font-mono font-bold">{f.name}</span>
            <span className="font-mono text-muted">{fmt.bytes(f.size, 1024)}</span>
            <span className="font-mono text-faint" title={f.sha256}>
              {shortHash(f.sha256)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** What the panel found in <data-dir>/dist and why it trusts it or not; the steps to make a bundle when there is none. */
function BundleDetails({ data, hideError }: { data: Updates; hideError?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const b = data.bundle;
  const status = b?.status ?? BundleStatus.MISSING;
  const missing = status === BundleStatus.MISSING || status === BundleStatus.UNSPECIFIED;
  const expired = !!b && b.expiresUnix > 0 && b.expiresUnix <= data.nowUnix;
  const err = b?.errorKey && !missing ? bundleErrorText(t, b.errorKey, b.params) : "";
  return (
    <div className="flex flex-col gap-3">
      {missing ? (
        <div className="flex flex-col gap-2.5">
          <p className="text-[13px] leading-normal text-pretty text-muted">
            <b className="text-fg">{t("up.bundle.emptyTitle")}.</b> {t("up.bundle.emptyBody")}
          </p>
          <Code>{t("up.bundle.steps")}</Code>
        </div>
      ) : (
        <>
          {err && !hideError && <Notice tone="danger">{err}</Notice>}
          {status === BundleStatus.NO_KEY && <Notice>{t("up.bundle.noKeyNote")}</Notice>}
          {b && (
            <>
              <dl className="flex flex-col gap-2">
                <Row label={t("up.bundle.version")}>
                  <span className="font-mono text-xs">{b.version || "—"}</span>
                </Row>
                <Row label={t("up.bundle.built")}>{b.built ? fmt.dateTime(b.built) : "—"}</Row>
                <Row label={t("up.bundle.expires")}>
                  {b.expiresUnix ? <span className={expired ? "font-semibold text-danger-text" : undefined}>{expired ? t("up.bundle.expired", { date: fmt.date(b.expiresUnix) }) : fmt.date(b.expiresUnix)}</span> : "—"}
                </Row>
                {b.scannedUnix > 0 && <Row label={t("up.bundle.checked")}>{fmt.ago(b.scannedUnix)}</Row>}
              </dl>
              <BundleFiles bundle={b} />
            </>
          )}
        </>
      )}
      <p className="text-xs leading-normal text-pretty text-muted">{t("up.bundle.githubNote")}</p>
    </div>
  );
}

/**
 * The foot of the top card: whether the panel trusts the release bundle (the signature check) with its version and dates
 * in one line, "Read the folder again" for the owner, and the details folded away. A bundle that is missing or not trusted
 * opens by itself: that is where its reason and the steps to fix it are.
 */
export function BundleStrip({ data, owner, actions }: { data: Updates; owner: boolean; actions: UpdateActions }) {
  const t = useT();
  const fmt = useFmt();
  const [touched, setTouched] = useState<boolean | null>(null);
  const b = data.bundle;
  const status = b?.status ?? BundleStatus.MISSING;
  const s = bundleStates[status];
  const missing = status === BundleStatus.MISSING || status === BundleStatus.UNSPECIFIED;
  const open = touched ?? status !== BundleStatus.TRUSTED;
  const expired = !!b && b.expiresUnix > 0 && b.expiresUnix <= data.nowUnix;
  // the pill names what is wrong when it is the signature; the details say the rest
  const pill = status === BundleStatus.UNTRUSTED && b?.errorKey.endsWith(".bad_signature") ? t("up.bundle.status.badSignature") : t(s.key);
  const facts = b && !missing ? [b.version, b.built ? t("up.built", { date: fmt.date(b.built) }) : "", b.expiresUnix ? (expired ? t("up.bundle.expired", { date: fmt.date(b.expiresUnix) }) : t("up.bundle.validUntil", { date: fmt.date(b.expiresUnix) })) : ""].filter(Boolean) : [];

  return (
    <div className="border-t border-line">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5 md:px-5">
        <SectionLabel as="h3" icon="shield" tone="sage" className="flex-none">
          {t("up.bundle.title")}
        </SectionLabel>
        <StatusPill kind={s.kind} label={pill} sm />
        {facts.length > 0 && (
          <span className={cx("min-w-0 basis-full font-mono text-[11px] md:flex-1 md:basis-auto md:truncate", expired ? "text-danger-text" : "text-muted")}>{facts.join(" · ")}</span>
        )}
        {facts.length === 0 && <span className="hidden flex-1 md:block" />}
        {owner && (
          <Button variant="ghost" size="sm" disabled={actions.busy} onClick={() => void actions.rescan()}>
            <Icon name="refresh" size={12} />
            {t("up.bundle.rescan")}
          </Button>
        )}
        <Fold open={open} onToggle={() => setTouched(!open)} label={t("up.bundle.details")} controls="up-bundle-details" />
      </div>
      {open && (
        <div id="up-bundle-details" className="px-4 pb-4 md:px-5">
          <BundleDetails data={data} hideError={heroOf(data).id === "untrusted"} />
        </div>
      )}
    </div>
  );
}

const errorKinds = new Set(["check_failed", "install_failed"]);

/** What the panel says about its own update, as a word, and how it should look. Shared by both cards of the panel. */
function panelUpdate(t: ReturnType<typeof useT>, data: Updates) {
  const p = data.panel;
  const update = p?.update;
  const errorLabels: Record<string, string> = {
    no_release: t("up.panel.noRelease"),
    asset_missing: t("up.panel.noAsset"),
    check_failed: t("up.panel.checkFailed"),
    unsupported: t("up.panel.unsupported"),
    unsigned: t("up.panel.unsigned", { version: update?.version ?? "" }),
    expired: t("up.panel.expired", { version: update?.version ?? "" }),
    no_key: t("up.panel.noKeyUpdate"),
    install_failed: t("up.panel.installFailed"),
  };
  const label = update?.installing
    ? t("up.panel.installing")
    : (update?.errorKey && errorLabels[update.errorKey]) ||
      (update?.available && !update.supported
        ? t("up.panel.unsupported")
        : update?.available
          ? t("up.panel.available", { version: update.version })
          : update?.checkedUnix
            ? t("up.panel.current")
            : t("up.panel.checking"));
  const kind: StatusKind = update?.installing
    ? "busy"
    : update?.errorKey && errorKinds.has(update.errorKey)
      ? "bad"
      : update?.available || (update?.errorKey && update.errorKey !== "no_release")
        ? "warn"
        : "ok";
  return { p, update, label, kind };
}

/** The steps for a panel that cannot update itself from here (no systemd service). */
function PanelHow() {
  const t = useT();
  return (
    <div className="flex flex-col gap-2.5">
      <h3 className="text-[13px] font-bold">{t("up.panel.how")}</h3>
      <p className="text-[13px] leading-normal text-pretty text-muted">{t("up.panel.howBody")}</p>
      <Code>{t("up.panel.steps")}</Code>
    </div>
  );
}

/** This panel's build, its release key and GitHub self-update controls (Settings → System). */
export function PanelCard({ data, owner, actions }: { data: Updates; owner: boolean; actions: UpdateActions }) {
  const t = useT();
  const fmt = useFmt();
  const { p, update, label, kind } = panelUpdate(t, data);
  return (
    <section className={card}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <SectionLabel as="h2" icon="server" tone="lavender">
          {t("up.panel.title")}
        </SectionLabel>
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill kind={kind} label={label} sm />
          {update?.checkedUnix ? <span className="text-xs text-faint">{t("up.panel.checkedAt", { ago: fmt.ago(update.checkedUnix) })}</span> : null}
        </div>
      </div>

      <dl className="grid gap-2 sm:grid-cols-2">
        <div className="min-w-0 rounded-field border border-line bg-surface-2 p-3.5">
          <dt className="text-xs text-muted">{t("up.panel.version")}</dt>
          <dd className="mt-1 break-words font-mono text-base font-bold text-fg">{p?.version || "—"}</dd>
          <p className="mt-1 text-xs text-muted">{t("up.panel.built")}: {p?.built ? fmt.dateTime(p.built) : "—"}</p>
        </div>
        <div className="min-w-0 rounded-field border border-line bg-surface-2 p-3.5">
          <dt className="text-xs text-muted">{t("up.panel.latest")}</dt>
          <dd className="mt-1 break-words font-mono text-base font-bold text-fg">
            {update?.version && update.url ? (
              <a href={update.url} target="_blank" rel="noreferrer" className="cursor-pointer text-accent underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent">
                {update.version}
              </a>
            ) : label}
          </dd>
          {update?.built ? (
            <p className="mt-1 text-xs text-muted">
              {t("up.panel.built")}: {fmt.dateTime(update.built)}
              {update.sha256 && (
                <>
                  {" · "}
                  {t("up.panel.sha")}{" "}
                  <span className="font-mono text-fg" title={update.sha256}>
                    {shortHash(update.sha256)}
                  </span>
                </>
              )}
            </p>
          ) : null}
          <p className="mt-1 text-xs text-muted">
            {t("up.panel.key")}: {p?.hasReleaseKey ? <span className="font-mono text-fg">{p.releaseKeyFingerprint}</span> : <span className="font-semibold text-warn-text">{t("up.panel.noKey")}</span>}
          </p>
        </div>
      </dl>

      <div className="flex flex-col gap-2.5 border-t border-line pt-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" disabled={actions.busy} onClick={() => void actions.checkPanel()}>
            <Icon name="refresh" size={12} />
            {t("up.panel.check")}
          </Button>
          {owner && update?.available && update.installable && (
            <Button variant="primary" size="sm" disabled={actions.busy || update.installing} onClick={() => void actions.installPanel({ version: update.version, sha256: update.sha256 })}>
              {t("up.panel.install")}
            </Button>
          )}
        </div>
        <p className="text-[13px] leading-normal text-pretty text-muted">{t("up.panel.updateBody")}</p>
      </div>
      {!update?.supported && (
        <div className="border-t border-line pt-3">
          <PanelHow />
        </div>
      )}
    </section>
  );
}

/**
 * This panel on the Updates page: one line (version, state, Check GitHub), the install button when a signed release is out
 * (the card takes the accent then), and the key, the SHA-256 and the long explanation folded away. A panel that cannot
 * update itself opens its steps by itself.
 */
export function PanelBar({ data, owner, actions }: { data: Updates; owner: boolean; actions: UpdateActions }) {
  const t = useT();
  const fmt = useFmt();
  const { p, update, label, kind } = panelUpdate(t, data);
  const [touched, setTouched] = useState<boolean | null>(null);
  const open = touched ?? !update?.supported;
  const offer = !!update?.available && !update.installing;
  return (
    <section className={cx("flex min-w-0 flex-col gap-3 rounded-card-lg border p-4", offer ? "border-accent-line bg-accent-soft" : "border-line bg-surface")}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <SectionLabel as="h2" icon="server" tone="lavender" className="flex-none">
          {t("up.panel.title")}
        </SectionLabel>
        <span className="font-mono text-xs font-bold">{p?.version || "—"}</span>
        {p?.built ? <span className="text-xs text-muted">{t("up.built", { date: fmt.date(p.built) })}</span> : null}
        <span className="flex-1" />
        <StatusPill kind={kind} label={label} sm />
        <Button variant="ghost" size="sm" disabled={actions.busy} onClick={() => void actions.checkPanel()}>
          <Icon name="refresh" size={12} />
          {t("up.panel.check")}
        </Button>
        {owner && update?.available && update.installable && (
          <Button variant="primary" size="md" disabled={actions.busy || update.installing} onClick={() => void actions.installPanel({ version: update.version, sha256: update.sha256 })}>
            {t("up.panel.install")}
          </Button>
        )}
        <Fold open={open} onToggle={() => setTouched(!open)} label={t("up.panel.details")} controls="up-panel-details" />
      </div>
      {open && (
        <div id="up-panel-details" className="flex flex-col gap-3 border-t border-line pt-3">
          <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-2">
            <Row label={t("up.panel.latest")}>
              {update?.version && update.url ? (
                <a href={update.url} target="_blank" rel="noreferrer" className="font-mono text-xs font-bold text-accent-text underline underline-offset-2">
                  {update.version}
                </a>
              ) : (
                <span className="text-muted">{label}</span>
              )}
              {update?.checkedUnix ? <span className="ml-2 text-xs text-faint">{t("up.panel.checkedAt", { ago: fmt.ago(update.checkedUnix) })}</span> : null}
            </Row>
            {update?.built ? (
              <Row label={t("up.panel.sha")}>
                <span className="font-mono text-xs" title={update.sha256}>
                  {shortHash(update.sha256)}
                </span>
                <span className="ml-2 text-xs text-muted">{fmt.dateTime(update.built)}</span>
              </Row>
            ) : null}
            <Row label={t("up.panel.key")}>
              {p?.hasReleaseKey ? <span className="font-mono text-xs">{p.releaseKeyFingerprint}</span> : <span className="font-semibold text-warn-text">{t("up.panel.noKey")}</span>}
            </Row>
          </dl>
          <p className="text-xs leading-normal text-pretty text-muted">{t("up.panel.updateBody")}</p>
          {!update?.supported && <PanelHow />}
        </div>
      )}
    </section>
  );
}
