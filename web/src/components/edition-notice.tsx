import type { ReactNode } from "react";
import { useT } from "@/i18n";
import type { EditionFeature } from "@/i18n/edition";
import { useEdition } from "@/lib/edition";
import { Notice } from "./ui/notice";

/**
 * "Not available in the Cloudflare edition yet: <feature>", in the quiet tone, for a feature the edition lacks; nothing at all
 * where the feature exists. The children say what to do instead, when there is something.
 */
export function EditionNotice({ feature, children, className }: { feature: EditionFeature; children?: ReactNode; className?: string }) {
  const t = useT();
  if (!useEdition().off(feature)) return null;
  return (
    <Notice tone="quiet" title={t("err.unavailable_in_edition", { feature: t(`edition.feature.${feature}`) })} className={className}>
      {children}
    </Notice>
  );
}
