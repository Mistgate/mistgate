import { Select as BaseSelect } from "@base-ui/react/select";
import { isLang, langNames, langShort, langs, localeOf, setLang, useLang, useT } from "@/i18n";
import { Icon } from "@/components/ui/icons";
import { SelectCheck, selectItemClass, selectPopupClass } from "@/components/ui/select";
import { cx } from "@/lib/cx";
import { toggleTheme, useTheme } from "@/lib/theme";

/**
 * The language menu of the top bar, the More sheet and the auth screens: a globe, the short code of the current language and
 * a chevron; the list under it names every language in its own script (and sets `lang` on the row so the right font draws it).
 * One row per entry of `langs`, so a new language adds a row and nothing else.
 */
export function LangToggle({ className }: { className?: string }) {
  const t = useT();
  const lang = useLang();
  return (
    <BaseSelect.Root value={lang} onValueChange={(v) => isLang(v) && setLang(v)}>
      <BaseSelect.Trigger
        aria-label={t("common.labeled", { label: t("header.language"), value: langNames[lang] })}
        className={cx(
          // 40px where a finger taps, the top-bar height (h-bar, 36px) from md up
          "inline-flex h-10 shrink-0 cursor-pointer items-center gap-1.5 rounded-ctl border border-line bg-surface pr-2 pl-2.5 text-xs font-bold text-muted outline-none transition-colors duration-200 hover:bg-surface-2 hover:text-fg focus-visible:border-accent data-popup-open:border-accent-line data-popup-open:text-fg md:h-bar",
          className,
        )}
      >
        <Icon name="globe" size={16} />
        <span lang={localeOf(lang)}>{langShort[lang]}</span>
        <svg aria-hidden width="10" height="10" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M3 4.5l3 3 3-3" />
        </svg>
      </BaseSelect.Trigger>
      <BaseSelect.Portal>
        <BaseSelect.Positioner side="bottom" align="end" sideOffset={6} alignItemWithTrigger={false} className="z-50 outline-none">
          <BaseSelect.Popup className={cx(selectPopupClass, "min-w-44")}>
            <BaseSelect.List>
              {langs.map((l) => (
                <BaseSelect.Item key={l} value={l} lang={localeOf(l)} className={selectItemClass}>
                  <BaseSelect.ItemText className="flex-1">{langNames[l]}</BaseSelect.ItemText>
                  <span aria-hidden className="font-mono text-[11px] font-bold text-muted">
                    {langShort[l]}
                  </span>
                  <span aria-hidden className="grid w-3 place-items-center text-accent-text">
                    <BaseSelect.ItemIndicator>
                      <SelectCheck />
                    </BaseSelect.ItemIndicator>
                  </span>
                </BaseSelect.Item>
              ))}
            </BaseSelect.List>
          </BaseSelect.Popup>
        </BaseSelect.Positioner>
      </BaseSelect.Portal>
    </BaseSelect.Root>
  );
}

/**
 * The theme switch: a track as tall as the language menu beside it (40px where a finger taps, h-bar from md up) with the
 * radius of the other controls, and a knob whose sun turns into a moon in the dark theme. Hover and focus change colours
 * only, never the size.
 */
export function ThemeToggle() {
  const t = useT();
  const dark = useTheme() === "dark";
  return (
    <button
      type="button"
      onClick={toggleTheme}
      aria-label={t(dark ? "header.theme.toLight" : "header.theme.toDark")}
      className="relative h-10 w-[66px] flex-none cursor-pointer rounded-ctl border border-line bg-surface p-0 outline-none transition-colors duration-500 hover:bg-surface-2 focus-visible:border-accent md:h-bar md:w-[60px]"
    >
      <span
        aria-hidden
        className={`absolute top-[3px] left-[3px] grid size-8 place-items-center rounded-[8px] bg-accent transition-[transform,background-color] duration-[550ms] ease-spring md:size-7 ${
          dark ? "translate-x-[26px] rotate-0 md:translate-x-6" : "rotate-180"
        }`}
      >
        <span
          className={`size-3 rounded-full transition-all duration-[450ms] ${
            dark
              ? "bg-transparent shadow-[inset_-3px_-2px_0_0_#0c0c0e]"
              : "bg-[#0c0c0e] shadow-[0_0_0_2px_color-mix(in_oklch,#0c0c0e_25%,transparent)]"
          }`}
        />
      </span>
    </button>
  );
}
