import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { useFmt } from "@/lib/format";
import { bundleErrorText, type Bundle, type UpdateActions, type Updates } from "@/lib/updates";

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

function BundleFiles({ bundle }: { bundle: Bundle }) {
  const t = useT();
  const fmt = useFmt();
  if (bundle.files.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-xs text-muted">{t("up.bundle.files")}</span>
      <ul className="flex flex-col divide-y divide-line rounded-field border border-line">
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

/** What the panel found in <data-dir>/dist and whether it trusts it; the steps to make a bundle when there is none. */
export function BundleCard({ data, owner, actions }: { data: Updates; owner: boolean; actions: UpdateActions }) {
  const t = useT();
  const fmt = useFmt();
  const b = data.bundle;
  const status = b?.status ?? BundleStatus.MISSING;
  const s = bundleStates[status];
  const missing = status === BundleStatus.MISSING || status === BundleStatus.UNSPECIFIED;
  const expired = !!b && b.expiresUnix > 0 && b.expiresUnix <= data.nowUnix;
  const err = b?.errorKey && !missing ? bundleErrorText(t, b.errorKey, b.params) : "";
  // the pill names what is wrong when it is the signature; the notice under it says the rest
  const pill = status === BundleStatus.UNTRUSTED && b?.errorKey.endsWith(".bad_signature") ? t("up.bundle.status.badSignature") : t(s.key);

  return (
    <section className={card}>
      <div className="flex flex-wrap items-center gap-2.5">
        <SectionLabel as="h2" icon="upload" tone="sage" className="min-w-32 flex-1">
          {t("up.bundle.title")}
        </SectionLabel>
        <StatusPill kind={s.kind} label={pill} sm />
        {owner && (
          <Button variant="secondary" size="sm" disabled={actions.busy} onClick={() => void actions.rescan()}>
            <Icon name="refresh" size={12} />
            {t("up.bundle.rescan")}
          </Button>
        )}
      </div>
      {missing ? (
        <div className="flex flex-col gap-2.5">
          <p className="text-[13px] leading-normal text-pretty text-muted">
            <b className="text-fg">{t("up.bundle.emptyTitle")}.</b> {t("up.bundle.emptyBody")}
          </p>
          <Code>{t("up.bundle.steps")}</Code>
        </div>
      ) : (
        <>
          {err && <Notice tone="danger">{err}</Notice>}
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
    </section>
  );
}

/** This panel's build, its release key and GitHub self-update controls. */
export function PanelCard({ data, owner, actions }: { data: Updates; owner: boolean; actions: UpdateActions }) {
  const t = useT();
  const fmt = useFmt();
  const p = data.panel;
  const update = p?.update;
  const updateLabel = update?.installing
    ? t("up.panel.installing")
    : update?.errorKey === "no_release"
      ? t("up.panel.noRelease")
      : update?.errorKey === "asset_missing"
        ? t("up.panel.noAsset")
      : update?.errorKey === "check_failed"
        ? t("up.panel.checkFailed")
        : update?.errorKey === "unsupported"
          ? t("up.panel.unsupported")
        : update?.available && !update.supported
            ? t("up.panel.unsupported")
            : update?.available
              ? t("up.panel.available", { version: update.version })
              : update?.checkedUnix
                ? t("up.panel.current")
                : t("up.panel.checking");
  const updateKind: StatusKind = update?.installing ? "busy" : update?.errorKey === "check_failed" ? "bad" : update?.available || update?.errorKey === "unsupported" || update?.errorKey === "asset_missing" ? "warn" : "ok";
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="server" tone="lavender">
        {t("up.panel.title")}
      </SectionLabel>
      <dl className="flex flex-col gap-2">
        <Row label={t("up.panel.version")}>
          <span className="font-mono text-xs">{p?.version || "—"}</span>
        </Row>
        <Row label={t("up.panel.built")}>{p?.built ? fmt.dateTime(p.built) : "—"}</Row>
        <Row label={t("up.panel.key")}>
          {p?.hasReleaseKey ? <span className="font-mono text-xs">{p.releaseKeyFingerprint}</span> : <span className="text-warn-text">{t("up.panel.noKey")}</span>}
        </Row>
        {update?.version && <Row label={t("up.panel.latest")}>
          <a href={update.url} target="_blank" rel="noreferrer" className="font-mono text-xs text-accent underline underline-offset-2">
            {update.version}
          </a>
        </Row>}
      </dl>
      <div className="flex flex-col gap-2.5 border-t border-line pt-3">
        <div className="flex flex-wrap items-center gap-2.5">
          <StatusPill kind={updateKind} label={updateLabel} sm />
          {update?.checkedUnix ? <span className="text-xs text-faint">{t("up.panel.checkedAt", { ago: fmt.ago(update.checkedUnix) })}</span> : null}
          <span className="flex-1" />
          <Button variant="secondary" size="sm" disabled={actions.busy} onClick={() => void actions.checkPanel()}>
            <Icon name="refresh" size={12} />
            {t("up.panel.check")}
          </Button>
          {owner && update?.available && update.installable && (
            <Button variant="primary" size="sm" disabled={actions.busy || update.installing} onClick={() => void actions.installPanel()}>
              {t("up.panel.install")}
            </Button>
          )}
        </div>
        <p className="text-[13px] leading-normal text-pretty text-muted">{t("up.panel.updateBody")}</p>
      </div>
      {!update?.supported && (
      <div className="flex flex-col gap-2.5 border-t border-line pt-3">
        <h3 className="text-[13px] font-bold">{t("up.panel.how")}</h3>
        <p className="text-[13px] leading-normal text-pretty text-muted">{t("up.panel.howBody")}</p>
        <Code>{t("up.panel.steps")}</Code>
      </div>
      )}
    </section>
  );
}
