import { langs, setLang, useLang, useT } from "@/i18n";
import { Segmented } from "@/components/ui/segmented";
import { toggleTheme, useTheme } from "@/lib/theme";

/** RU/EN pill of the top bar, the More sheet and the auth screens. */
export function LangToggle() {
  const t = useT();
  const lang = useLang();
  return (
    <Segmented
      variant="lang"
      aria-label={t("header.language")}
      value={lang}
      onValueChange={setLang}
      options={langs.map((l) => ({ value: l, label: l.toUpperCase() }))}
    />
  );
}

/** 50x28 switch: a round knob that turns into a moon in the dark theme. */
export function ThemeToggle() {
  const t = useT();
  const dark = useTheme() === "dark";
  return (
    <button
      type="button"
      onClick={toggleTheme}
      aria-label={t(dark ? "header.theme.toLight" : "header.theme.toDark")}
      className="relative h-7 w-[50px] flex-none rounded-[14px] border border-line bg-surface p-0 transition-colors duration-500"
    >
      <span
        aria-hidden
        className={`absolute top-0.5 left-0.5 grid size-[22px] place-items-center rounded-full bg-accent transition-[transform,background-color] duration-[550ms] ease-spring ${
          dark ? "translate-x-[22px] rotate-0" : "rotate-180"
        }`}
      >
        <span
          className={`size-2.5 rounded-full transition-all duration-[450ms] ${
            dark
              ? "bg-transparent shadow-[inset_-3px_-2px_0_0_#0c0c0e]"
              : "bg-[#0c0c0e] shadow-[0_0_0_2px_color-mix(in_oklch,#0c0c0e_25%,transparent)]"
          }`}
        />
      </span>
    </button>
  );
}
