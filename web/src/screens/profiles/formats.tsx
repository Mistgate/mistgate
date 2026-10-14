import { Fragment } from "react";
import type { ProfileFormat } from "@/gen/mistgate/admin/v1/profile_pb";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Tip } from "@/components/ui/tooltip";
import type { Key, Tx } from "@/screens/users/t";

type Format = Pick<ProfileFormat, "format" | "ok" | "note">;

// The short name of each format. Xray JSON is the label of "Apps & formats"; the other three are shorter here than there.
const nameKey: Partial<Record<SubFormat, Key>> = {
  [SubFormat.BASE64_URIS]: "profiles.fmt.base64",
  [SubFormat.MIHOMO_YAML]: "profiles.fmt.mihomo",
  [SubFormat.XRAY_JSON]: "subs.fmt.xray",
  [SubFormat.SINGBOX_JSON]: "profiles.fmt.singbox",
};
const nameOf = (f: SubFormat, t: Tx) => {
  const key = nameKey[f];
  return key ? t(key) : SubFormat[f];
};
const why = (note: string, t: Tx) => t.opt(`profiles.fmt.why.${note}`) ?? note;
const tip = (note: string, t: Tx) => t.opt(`profiles.fmt.tip.${note}`) ?? why(note, t);

/** The formats that drop a profile, one entry per reason in the order of the list: AmneziaWG is one mark for three formats. */
export function dropsOf(formats: readonly Format[]): { note: string; formats: SubFormat[] }[] {
  const drops: { note: string; formats: SubFormat[] }[] = [];
  for (const f of formats) {
    if (f.ok) continue;
    const note = f.note || "unsupported";
    const d = drops.find((x) => x.note === note);
    if (d) d.formats.push(f.format);
    else drops.push({ note, formats: [f.format] });
  }
  return drops;
}

/**
 * A drop, as a fact rather than an error: a yellow chip with "not in" and the short reason ("not in sing-box: XHTTP"), the
 * full sentence in a tooltip and in its accessible name.
 */
function Drop({ formats, note, t }: { formats: SubFormat[]; note: string; t: Tx }) {
  const text = t("profiles.fmt.drop", { formats: formats.map((f) => nameOf(f, t)).join(t("profiles.fmt.sep")), why: why(note, t) });
  const full = tip(note, t);
  return (
    <Tip label={full}>
      <span
        tabIndex={0}
        aria-label={`${text}. ${full}`}
        className="relative inline-flex min-h-[22px] max-w-full cursor-default items-center rounded-[7px] border border-warn-line bg-warn-soft px-1.5 align-middle text-[11px] leading-tight font-bold text-warn-text"
      >
        {text}
      </span>
    </Tip>
  );
}

/** Only the drops (the user card): nothing when the profile works in every format, or when its formats are unknown. */
export function FormatDrops({ formats, t }: { formats: readonly Format[]; t: Tx }) {
  return dropsOf(formats).map((d) => <Drop key={d.note} {...d} t={t} />);
}

/**
 * In which subscription formats a profile works, in one line that wraps: the formats that carry it, quiet ("Link list ·
 * Mihomo · Xray JSON"), then the drops ("not in sing-box: XHTTP"). A format that writes a port-hopping range as its one
 * port is dotted, with the reason in a tooltip. Nothing at all when the formats are unknown (an empty list).
 */
export function FormatsLine({ formats, t, className }: { formats: readonly Format[]; t: Tx; className?: string }) {
  if (formats.length === 0) return null;
  const ok = formats.filter((f) => f.ok);
  return (
    <div role="group" aria-label={t("profiles.fmt.title")} className={className}>
      <span className="text-xs leading-[22px] text-muted">
        {ok.map((f, i) => {
          const name = nameOf(f.format, t);
          return (
            <Fragment key={f.format}>
              {i > 0 && " · "}
              {f.note ? (
                <Tip label={tip(f.note, t)}>
                  <span
                    tabIndex={0}
                    aria-label={`${t("common.labeled", { label: name, value: why(f.note, t) })}. ${tip(f.note, t)}`}
                    className="relative cursor-default rounded-sm whitespace-nowrap underline decoration-dotted underline-offset-[3px]"
                  >
                    {name}
                  </span>
                </Tip>
              ) : (
                <span className="whitespace-nowrap">{name}</span>
              )}
            </Fragment>
          );
        })}
      </span>
      {dropsOf(formats).map((d, i) => (
        <Fragment key={d.note}>
          {(ok.length > 0 || i > 0) && " "}
          <Drop {...d} t={t} />
        </Fragment>
      ))}
    </div>
  );
}
