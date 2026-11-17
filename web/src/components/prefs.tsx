import type { MouseEvent } from "react";
import { currentLang, isLang, langNames, langShort, langs, localeOf, setLang, useLang, useT, type Lang } from "@/i18n";
import { Icon } from "@/components/ui/icons";
import { Menu, MenuRadioGroup, MenuRadioItem } from "@/components/ui/menu";
import { playWave, originOf } from "@/components/fx/wave";
import { cx } from "@/lib/cx";
import { setTheme, themeNow, useTheme } from "@/lib/theme";

/**
 * The language menu of the top bar, the phone's menu and the auth screens: a 40px button with a globe, the short code of the
 * current language and a chevron; the list under it names every language in its own script (and sets `lang` on the row so the
 * right font draws it). One row per entry of `langs`, so a new language adds a row and nothing else.
 */
export function LangToggle({ className, defaultOpen }: { className?: string; defaultOpen?: boolean }) {
  const t = useT();
  const lang = useLang();
  return (
    <Menu
      narrow
      defaultOpen={defaultOpen}
      aria-label={t("shell.lang.list")}
      trigger={
        <button
          type="button"
          aria-label={t("common.labeled", { label: t("header.language"), value: langNames[lang] })}
          className={cx(
            "inline-flex h-10 shrink-0 cursor-pointer items-center gap-1.5 rounded-field border border-line bg-step ps-3 pe-2 font-mono text-xs font-medium text-fg outline-none transition-[background-color,transform] duration-150 ease-out hover:bg-(--step-hover) focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-mark active:scale-[0.98] data-popup-open:bg-(--step-hover)",
            className,
          )}
        >
          <Icon name="globe" size={16} className="text-sec" />
          <span lang={localeOf(lang)}>{langShort[lang]}</span>
          <Icon name="chevronDown" size={16} className="text-muted" />
        </button>
      }
    >
      <MenuRadioGroup
        value={lang}
        aria-label={t("shell.lang.list")}
        onValueChange={(v, row) => isLang(v) && chooseLanguage(v, row)}
      >
        {langs.map((l) => (
          <MenuRadioItem key={l} value={l} code={langShort[l]} lang={localeOf(l)}>
            {langNames[l]}
          </MenuRadioItem>
        ))}
      </MenuRadioGroup>
    </Menu>
  );
}

/** Picks a language: the wave runs from `from` (the row or button that was pressed), then the page changes under it. */
export function chooseLanguage(next: Lang, from?: Element | null) {
  if (next === currentLang()) return;
  playWave(originOf(from), { light: themeNow() === "light" });
  setLang(next);
}

const themeBtn =
  "grid size-[34px] cursor-pointer place-items-center rounded-ctl text-sec outline-none transition-colors duration-150 ease-out hover:text-fg focus-visible:outline-2 focus-visible:outline-accent-mark aria-pressed:bg-surface-2 aria-pressed:text-fg";

/**
 * The theme switch: two 34px buttons, the sun and the moon, in a 40px frame; the one that is on is a step lighter. A pressed
 * button is the theme in use (aria-pressed), so a screen reader hears the state and not an action.
 */
export function ThemeToggle({ className }: { className?: string }) {
  const t = useT();
  const dark = useTheme() === "dark";
  // the wave starts at the button; the theme changes under it
  const pick = (next: "light" | "dark") => (e: MouseEvent<HTMLElement>) => {
    if ((next === "dark") === dark) return;
    playWave(originOf(e.currentTarget), { light: next === "light", theme: true });
    setTheme(next);
  };
  return (
    <div role="group" aria-label={t("header.theme")} className={cx("inline-flex h-10 flex-none items-center gap-0.5 rounded-field border border-line bg-surface p-0.5", className)}>
      <button type="button" aria-pressed={!dark} aria-label={t("shell.theme.light")} onClick={pick("light")} className={themeBtn}>
        <Icon name="sun" size={16} />
      </button>
      <button type="button" aria-pressed={dark} aria-label={t("shell.theme.dark")} onClick={pick("dark")} className={themeBtn}>
        <Icon name="moon" size={16} />
      </button>
    </div>
  );
}
