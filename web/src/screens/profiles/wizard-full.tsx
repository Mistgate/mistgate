import { useMemo, type ReactNode } from "react";
import type { ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { cx } from "@/lib/cx";
import { randomPort } from "@/lib/awg";
import { VlessHy2Docs } from "@/screens/node/inbound-check";
import { useTx } from "@/screens/users/t";
import { AwgPanel, awgHidden, handTyped, type AwgAdvice } from "./awg-panel";
import { EgressNote } from "./egress-note";
import { mtprotoEdited, mtprotoGroups, mtprotoHidden, mtprotoLocked, mtprotoOn443, mtprotoWeb } from "./mtproto-form";
import { allFields, getAt, parseSchema, setAt, type Field, type Settings } from "./schema";
import { SchemaForm, type Problem } from "./schema-form";
import { vlessEdited, vlessHidden, vlessOffers } from "./vless-form";

/**
 * "All settings" of the wizard: the protocol's whole form, the one the profile's page has (the schema's groups, and AmneziaWG's own
 * version and look cards), so a task never keeps a setting out of reach. What a protocol adds under a field (the random port, the notes
 * about 443 and the CDN) is the editor's.
 */
export function FullForm({ info, settings, base, problems, advice, onChange }: { info: ProtocolInfo; settings: Settings; base: Settings; problems: readonly Problem[]; advice?: AwgAdvice; onChange: (next: Settings) => void }) {
  const t = useTx();
  const groups = useMemo(() => (info.id === "mtproto" ? mtprotoGroups(parseSchema(info.settingsSchemaJson)) : parseSchema(info.settingsSchemaJson)), [info.id, info.settingsSchemaJson]);
  const fields = useMemo(() => allFields(groups), [groups]);
  const isAwg = info.id === "awg";
  const isVless = info.id === "vless";
  const isMtproto = info.id === "mtproto";
  const vlessHiddenIds = useMemo<ReadonlySet<string>>(() => (isVless ? vlessHidden(fields, settings) : new Set()), [isVless, fields, settings]);
  const mtprotoHiddenIds = useMemo<ReadonlySet<string>>(() => (isMtproto ? mtprotoHidden(fields, settings) : new Set()), [isMtproto, fields, settings]);
  const hidden = isAwg ? awgHidden : isVless && vlessHiddenIds.size > 0 ? vlessHiddenIds : isMtproto && mtprotoHiddenIds.size > 0 ? mtprotoHiddenIds : undefined;

  const extra = (f: Field): ReactNode => {
    if (isMtproto && f.id === "mode" && mtprotoWeb(settings)) return <span className="text-[11px] leading-snug text-muted">{t("mtproto.web.dns")}</span>;
    if (isMtproto && f.id === "port" && mtprotoWeb(settings)) return <span className="text-[11px] leading-snug text-muted">{t("mtproto.web.port")}</span>;
    if (isMtproto && f.id === "fake_tls.domain") {
      return (
        <span className="flex flex-col gap-1 text-[11px] leading-snug text-muted">
          <span>{t("mtproto.siteWarn")}</span>
          {mtprotoOn443(settings) && <span>{t("mtproto.site443Warn")}</span>}
        </span>
      );
    }
    if (isAwg && (f.id === "subnet4" || f.id === "subnet6") && !getAt(settings, f.path)) return <span className="text-[11px] leading-snug text-muted">{t("awg.subnet.auto")}</span>;
    if (isAwg && f.id === "port") {
      return (
        <button type="button" className="w-fit text-[11px] font-bold text-accent-text" onClick={() => onChange(setAt(settings, ["port"], randomPort()))}>
          {t("awg.port.random")}
        </button>
      );
    }
    if (f.id === "egress" && getAt(settings, f.path) === "warp") return <EgressNote nodeIds={[]} />;
    // behind a CDN the mode "auto" is sent to clients as packet-up (the panel does it too)
    if (isVless && f.id === "xhttp.mode" && getAt(settings, ["security"]) === "tls" && String(getAt(settings, ["cdn", "host"]) ?? "").trim() !== "" && getAt(settings, f.path) === "auto") {
      return <span className="text-[11px] leading-snug text-muted">{t("profiles.vless.mode.cdn")}</span>;
    }
    // TCP 443 is also where a Hysteria2 profile on the same node keeps its site (design/vless.md §7.2)
    if (isVless && f.id === "port" && Number(getAt(settings, f.path)) === 443) {
      const tls = getAt(settings, ["security"]) === "tls";
      const self = !tls && getAt(settings, ["reality", "target_kind"]) === "self";
      return (
        <span className={cx("flex flex-col items-start gap-0.5 text-[11px] leading-snug", self ? "text-muted" : "text-warn-text")}>
          {t(tls ? "profiles.vless.port443.tls" : self ? "profiles.vless.port443.self" : "profiles.vless.port443.site")}
          <VlessHy2Docs />
        </span>
      );
    }
    if (info.id === "hysteria2" && f.id === "masquerade.type") {
      return (
        <span className="text-[11px] leading-snug">
          <VlessHy2Docs />
        </span>
      );
    }
    return null;
  };

  return (
    <div className="pw-full flex flex-col gap-4">
      {isAwg && <AwgPanel settings={settings} onChange={onChange} advice={advice} />}
      <SchemaForm
        protocol={info.id}
        groups={groups}
        settings={settings}
        base={base}
        problems={problems}
        onChange={isAwg ? (n) => onChange(handTyped(settings, n)) : isVless ? (n) => onChange(vlessEdited(settings, n)) : isMtproto ? (n) => onChange(mtprotoEdited(settings, n)) : onChange}
        readOnly={isMtproto ? mtprotoLocked(settings) : undefined}
        hidden={hidden}
        offers={isVless ? vlessOffers : undefined}
        extra={extra}
      />
    </div>
  );
}
