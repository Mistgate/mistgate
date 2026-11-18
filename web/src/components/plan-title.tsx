import { useT } from "@/i18n";
import type { Piece } from "@/lib/integrations";

/** A title made of the panel's words and names from data (`approvalTitle`, `planTitleWords`): a name is plain in the title, with the panel's note on hover that it is untrusted. */
export function PlanTitle({ pieces }: { pieces: readonly Piece[] }) {
  const t = useT();
  return (
    <>
      {pieces.map((p, i) =>
        typeof p === "string" ? (
          p
        ) : (
          <span key={i} title={t("int.ap.untrusted")}>
            {p.data}
          </span>
        ),
      )}
    </>
  );
}
