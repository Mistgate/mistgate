import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, IconChip, type IconName } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import { cx } from "@/lib/cx";
import type { Tx } from "@/screens/users/t";
import { lookOf } from "./protocol-meta";

type Proto = Pick<ProtocolInfo, "id" | "displayName" | "apps">;

// What each protocol's card says beyond its words (profiles.pick.<id>.tagline|t1..t3|best, from docs/en/guide): the icons of
// its traits, and the apps people use with it (client-apps.md). Brand names, not translated.
const traitIcons: Record<string, IconName[]> = {
  hysteria2: ["bolt", "traffic", "link"],
  vless: ["lock", "cloud", "traffic"],
  awg: ["key", "mask", "traffic"],
  mtproto: ["plane", "link", "mask"],
};
const appsOf: Record<string, string[]> = {
  hysteria2: ["Happ", "kl!ck", "Clash Verge Rev", "sing-box"],
  vless: ["Happ", "kl!ck", "v2rayNG", "Clash Verge Rev"],
  awg: ["AmneziaVPN", "AmneziaWG", "kl!ck"],
  mtproto: ["Telegram"],
};
// a protocol without its own list names the apps ProtocolInfo.apps declares
const appNames: Partial<Record<App, string>> = { [App.HAPP]: "Happ", [App.AMNEZIA]: "AmneziaVPN", [App.TELEGRAM]: "Telegram" };
const monograms: Record<string, string> = { Happ: "H", "kl!ck": "K", "Clash Verge Rev": "C", "sing-box": "S", v2rayNG: "V", AmneziaVPN: "A", AmneziaWG: "W", Telegram: "T" };

/** A protocol's card copy: one tagline, a few traits (icon and a word or two), the apps, and the fuller line for step 2. */
export function copyOf(p: Proto, t: Tx) {
  const k = (part: string) => t.opt(`profiles.pick.${p.id}.${part}`);
  const icons = traitIcons[p.id] ?? [];
  return {
    tagline: k("tagline") ?? "",
    best: k("best") ?? "",
    traits: ["t1", "t2", "t3"].flatMap((part, i) => {
      const text = k(part);
      return text ? [{ icon: icons[i] ?? "check", text }] : [];
    }),
    apps: appsOf[p.id] ?? p.apps.map((a) => appNames[a]).filter((x): x is string => !!x),
  };
}

/** "Start here" (the accent's text) or "Experimental" (a warning dot and its text): the one quiet word a protocol's card may carry. */
function Badge({ id, t, className }: { id: string; t: Tx; className?: string }) {
  const badge = lookOf(id).badge;
  if (!badge) return null;
  return (
    <span className={cx("inline-flex flex-none items-center gap-1.5 text-xs font-medium whitespace-nowrap", badge === "start" ? "text-accent-text" : "tone-warn tone-text", className)}>
      {badge === "experimental" && <span aria-hidden className="size-1.5 rounded-full bg-(--c)" />}
      {t(badge === "start" ? "profiles.pick.start" : "profiles.pick.experimental")}
    </span>
  );
}

/**
 * Step 1 of a new profile: the protocols as flat cards in one radio group: the protocol's mark (the only colour of the card),
 * the name, one tagline, a few traits and the apps (their names in a tooltip). Arrows choose, Enter, a double click or
 * "Continue" go on; the chosen card steps up a surface, takes the accent's hairline and fills the mark in its corner. Four in
 * a row where there is room, two by two in the middle, a list on the phone. `drafts`: the protocols whose form was filled in and left.
 */
export function ProtocolPicker({
  protocols,
  value,
  onValue,
  onContinue,
  onBack,
  drafts,
  t,
}: {
  protocols: Proto[];
  value: string;
  onValue: (id: string) => void;
  onContinue: (id: string) => void;
  onBack: () => void;
  drafts: ReadonlySet<string>;
  t: Tx;
}) {
  const chosen = protocols.find((p) => p.id === value) ?? protocols[0]!;
  return (
    <div className="@container flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <IconButton aria-label={t("profiles.back")} onClick={onBack}>
          <Icon name="back" />
        </IconButton>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <PageTitle className="truncate">{t("profiles.new")}</PageTitle>
          <span className="text-xs text-muted">{t("profiles.pick.step1")}</span>
        </div>
      </div>

      <RadioGroup
        aria-label={t("profiles.protocol")}
        value={chosen.id}
        onValueChange={(v) => onValue(v as string)}
        onKeyDown={(e) => {
          if (e.key !== "Enter") return;
          e.preventDefault();
          onContinue(chosen.id);
        }}
        className="grid gap-3 @min-[480px]:grid-cols-2 @min-[900px]:grid-cols-4"
      >
        {protocols.map((p) => {
          const look = lookOf(p.id);
          const copy = copyOf(p, t);
          const draft = drafts.has(p.id) && <span className="text-xs font-medium text-accent-text">{t("profiles.pick.draft")}</span>;
          return (
            <Radio.Root
              key={p.id}
              value={p.id}
              data-tone={look.tone}
              onDoubleClick={() => onContinue(p.id)}
              className="group relative grid cursor-pointer grid-cols-[auto_minmax(0,1fr)] items-start gap-x-4 gap-y-1 rounded-card-lg border border-line bg-surface p-4 text-left font-normal transition-colors duration-200 hover:border-accent-line data-checked:border-accent data-checked:bg-surface-2 @min-[480px]:flex @min-[480px]:flex-col @min-[480px]:items-stretch @min-[480px]:gap-0 @min-[480px]:p-5"
            >
              {/* the selection mark keeps the corner: an empty ring, filled in the accent with a tick when chosen */}
              <span
                aria-hidden
                className="absolute top-4 right-4 grid size-5 place-items-center rounded-full border-[1.5px] border-faint text-on-accent transition-colors duration-200 group-data-checked:border-accent group-data-checked:bg-accent @min-[480px]:top-5 @min-[480px]:right-5"
              >
                <Icon name="check" size={12} className="opacity-0 group-data-checked:opacity-100" />
              </span>
              <span aria-hidden className="id-chip row-span-4 size-12 self-start rounded-xl">
                <Icon name={look.icon} size={24} />
              </span>
              <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 pr-8 @min-[480px]:mt-4 @min-[480px]:pr-0">
                <span className="text-lg leading-6 font-semibold @min-[480px]:text-xl">{p.displayName}</span>
                <Badge id={p.id} t={t} className="@min-[480px]:hidden" />
              </span>
              {/* two lines kept for it in a row of cards, so the traits under it line up */}
              {copy.tagline && <span className="text-sm leading-snug text-pretty text-muted @min-[480px]:mt-1 @min-[480px]:min-h-[2lh]">{copy.tagline}</span>}
              {copy.traits.length > 0 && (
                <span className="mt-2 flex flex-wrap gap-x-4 gap-y-1 @min-[480px]:mt-4 @min-[480px]:flex-col @min-[480px]:gap-2">
                  {copy.traits.map((tr) => (
                    <span key={tr.text} className="flex items-center gap-2 text-xs @min-[480px]:text-sm">
                      <Icon name={tr.icon} size={16} className="flex-none text-muted" />
                      {tr.text}
                    </span>
                  ))}
                </span>
              )}
              {draft && <span className="mt-1 @min-[480px]:hidden">{draft}</span>}
              {/* the bottom row: the apps (their names in a tooltip and for a screen reader) and the card's badge, lined up across a row */}
              <span className="mt-auto hidden pt-5 @min-[480px]:block">
                <span className="flex min-h-7 items-center gap-2 border-t border-line pt-4">
                  {copy.apps.length > 0 && (
                    <Tip label={copy.apps.join(" · ")}>
                      <span className="flex items-center">
                        {copy.apps.map((a) => (
                          <span key={a} aria-hidden className="-ml-1.5 grid size-6 place-items-center rounded-full border-2 border-surface bg-surface-2 text-[11px] font-semibold text-muted group-data-checked:border-surface-2 group-data-checked:bg-surface first:ml-0">
                            {monograms[a] ?? a.slice(0, 1).toUpperCase()}
                          </span>
                        ))}
                        <span className="sr-only">{copy.apps.join(", ")}</span>
                      </span>
                    </Tip>
                  )}
                  <span className="flex-1" />
                  {draft}
                  <Badge id={p.id} t={t} />
                </span>
              </span>
            </Radio.Root>
          );
        })}
      </RadioGroup>

      <div className="sticky bottom-[84px] z-[3] flex items-center gap-3 rounded-card border border-accent-line bg-surface py-2 pr-2 pl-3 md:bottom-4">
        <IconChip icon={lookOf(chosen.id).icon} tone={lookOf(chosen.id).tone} size={28} className="rounded-lg" />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-sm font-semibold">{chosen.displayName}</span>
          <span className="hidden truncate text-xs text-muted md:block">{t("profiles.pick.hint")}</span>
        </span>
        <Button variant="primary" onClick={() => onContinue(chosen.id)}>
          {t("profiles.pick.continue")}
          <Icon name="chevronRight" size={14} />
        </Button>
      </div>
    </div>
  );
}

/** Step 2's header: the chosen protocol, "Change" back to the choice (a new profile only), and what it is best for under them. */
export function ChoiceStrip({ info, t, onChange }: { info: Proto; t: Tx; onChange?: () => void }) {
  const look = lookOf(info.id);
  const copy = copyOf(info, t);
  const line = copy.best || copy.tagline;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-3">
        <IconChip icon={look.icon} tone={look.tone} size={36} className="rounded-ctl" />
        <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-base leading-5 font-semibold">{info.displayName}</span>
          {look.badge === "experimental" && <Badge id={info.id} t={t} />}
        </span>
        {onChange && (
          <Button size="sm" onClick={onChange}>
            {t("profiles.pick.change")}
          </Button>
        )}
      </div>
      {line && <p className="text-xs leading-snug text-pretty text-muted">{line}</p>}
    </div>
  );
}
