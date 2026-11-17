import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import type { MessageKey } from "@/i18n/en";
import { useT } from "@/i18n";

// The first state of the People tab, when there is nobody yet (the board's fz-first): what a person gets, one button, a slot
// where the first person will appear over three ghost rows, and the three steps. The dotted glow is nodes.css's .fz-first.

const steps: { title: MessageKey; hint: MessageKey }[] = [
  { title: "ul.first.s1", hint: "ul.first.s1h" },
  { title: "ul.first.s2", hint: "ul.first.s2h" },
  { title: "ul.first.s3", hint: "ul.first.s3h" },
];

const ghost = "bg-[color-mix(in_srgb,var(--text)_14%,transparent)]";

export function UsersFirst({ onAdd }: { onAdd?: () => void }) {
  const t = useT();
  return (
    <section className="fz-first relative grid grid-cols-[minmax(0,1fr)_minmax(0,440px)] items-center overflow-hidden rounded-card border border-line bg-surface max-mid:grid-cols-1">
      <div className="relative flex min-w-0 flex-col items-start py-10 ps-10 pe-8 max-md:p-5">
        <span className="flex items-center gap-2 text-[13px] text-sec">
          <i aria-hidden className="size-2 flex-none rounded-full bg-ok" />
          <b className="font-medium text-fg">{t("ul.first.title")}</b>
        </span>
        <h2 className="mt-3 text-[32px] leading-9 font-medium tracking-[-0.02em] max-md:text-2xl max-md:leading-7">
          {t("ul.first.lead")} <span className="text-[color-mix(in_srgb,var(--text)_62%,transparent)]">{t("ul.first.lead2")}</span>
        </h2>
        <p className="mt-3 max-w-[520px] text-[color-mix(in_srgb,var(--text)_78%,transparent)]">{t("ul.first.text")}</p>
        {onAdd && (
          <div className="mt-8 flex flex-wrap gap-2 max-md:w-full">
            <Button variant="primary" size="lg" className="max-md:flex-[1_1_100%]" onClick={onAdd}>
              <Icon name="plus" size={16} />
              {t("ul.new")}
            </Button>
          </div>
        )}
      </div>
      <div aria-hidden className="fz-ghosts relative flex flex-col gap-2 py-8 ps-0 pe-10 max-mid:max-h-[150px] max-mid:overflow-hidden max-mid:ps-10 max-mid:pe-8 max-mid:pt-0 max-mid:pb-2 max-md:px-5 max-md:pb-2">
        <button type="button" tabIndex={-1} onClick={onAdd} className="grid h-[60px] grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 rounded-[12px] border border-dashed border-accent-mark bg-[color-mix(in_srgb,var(--accent-mark)_9%,var(--surface))] px-4 text-start [transition:background-color_.15s_var(--e),border-color_.15s_var(--e),transform_.2s_var(--ease-spring)] hover:-translate-y-px hover:bg-[color-mix(in_srgb,var(--accent-mark)_10%,var(--surface))]">
          <span className="grid size-8 place-items-center rounded-ctl bg-tone-mint-bg text-accent-text">
            <Icon name="plus" size={16} />
          </span>
          <span className="flex min-w-0 flex-col">
            <b className="font-medium">{t("ul.first.slot")}</b>
            <span className="text-[13px] leading-4 text-sec">{t("ul.first.slotHint")}</span>
          </span>
          <i className="fz-slot-dot size-2 rounded-full bg-ok" />
        </button>
        {[0, 1, 2].map((i) => (
          <div key={i} className="grid h-[60px] grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 rounded-[12px] border border-[color-mix(in_srgb,var(--text)_14%,transparent)] bg-surface px-4">
            <i className={`block size-8 rounded-full ${ghost}`} />
            <span className="flex flex-col gap-1.5">
              <i className={`block h-2.5 w-14 rounded-[4px] ${ghost}`} />
              <i className={`block h-2 rounded-[4px] ${ghost} ${["w-[120px]", "w-24", "w-[136px]"][i]}`} />
            </span>
            <i className={`block size-2 rounded-full ${ghost}`} />
          </div>
        ))}
      </div>
      <ol className="col-span-full m-0 grid list-none grid-cols-3 border-t border-[color-mix(in_srgb,var(--text)_12%,transparent)] p-0 max-mid:grid-cols-1 max-mid:bg-surface">
        {steps.map((s, i) => (
          <li key={s.title} className={["grid grid-cols-[auto_minmax(0,1fr)] items-start gap-3 px-5 py-4 max-md:py-3", i === 0 ? "ps-10 max-md:ps-5" : "border-s border-line max-mid:border-s-0 max-mid:border-t max-mid:ps-10 max-md:ps-5"].join(" ")}>
            <span className="grid size-6 place-items-center rounded-full border border-[color-mix(in_srgb,var(--text)_28%,transparent)] font-mono text-xs font-medium text-fg">{i + 1}</span>
            <div className="min-w-0">
              <b className="block leading-6 font-medium">{t(s.title)}</b>
              <span className="block text-[13px] leading-[18px] text-sec">{t(s.hint)}</span>
            </div>
          </li>
        ))}
      </ol>
    </section>
  );
}
