import { useQuery } from "@tanstack/react-query";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { isBad, isDraft, previewQuery, type Site } from "@/lib/decoysites";
import { Icon } from "@/components/ui/icons";
import { DefaultMark } from "@/components/brand";
import { useSeen } from "./parts";

/**
 * The live miniature of a site's home page (the board's .dz-th): the same sandboxed document as the page's frame, drawn at 300 %
 * and scaled down to a third. The document is asked for once the card is near the window, so a long list loads as it is
 * scrolled; a version never changes, so it is kept by its hash. A site with no index.html has none to draw.
 */
export function Thumb({ site, shot, className }: { site: Site; shot?: boolean; className?: string }) {
  const t = useT();
  const bad = isBad(site);
  const [ref, seen] = useSeen<HTMLSpanElement>();
  const doc = useQuery({ ...previewQuery(site.id, site.bundleSha256, "", false), enabled: seen && !bad });
  return (
    <span ref={ref} aria-hidden className={cx("dz-th", className, !doc.data && !bad && "bg-step!")} data-shot={shot ? "thumb" : undefined}>
      {doc.data && (
        // sandbox="" : no scripts, no forms, no same-origin; the preview has its scripts cut and its links made inert by the panel
        <iframe sandbox="" loading="lazy" tabIndex={-1} scrolling="no" srcDoc={doc.data} title={t("dz.card.thumb", { name: site.name })} />
      )}
      {bad && (
        <span className="dz-nd">
          <Icon name="file" size={24} />
          {t("dz.card.noIndex")}
        </span>
      )}
      {isDraft(site) && <span className="dz-tg">{t("dz.card.draft")}</span>}
    </span>
  );
}

/** The mark of the built-in entry: the Mistgate badge, the page it stands for. */
export function BuiltinLogo({ size = 22 }: { size?: number }) {
  return (
    <span className="block opacity-85">
      <DefaultMark size={size} />
    </span>
  );
}
