import { useState } from "react";
import { Button } from "@/components/ui/button";
import { parseAwgConf, type ConfImport } from "@/lib/awg";
import { useTx } from "@/screens/users/t";

/**
 * "Paste .conf": an AmneziaWG client config is read in the browser (lib/awg.ts parseAwgConf) and handed to `onApply`,
 * which lays it over the form. The text is dropped as soon as it is read, so its keys are not kept; the report lists
 * key names only, never values.
 */
export function ConfImportBox({ busy, onApply }: { busy: boolean; onApply: (imp: ConfImport) => Promise<boolean> }) {
  const t = useTx();
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [report, setReport] = useState<ConfImport | null>(null);

  async function apply() {
    const imp = parseAwgConf(text);
    if (await onApply(imp)) {
      setReport(imp);
      setText("");
    }
  }

  const list = (names: string[]) => names.join(", ");
  return (
    <div className="flex flex-col gap-2 border-t border-line pt-3">
      <div className="flex items-center gap-3">
        <span className="flex-1 text-[13px] font-bold">{t("awg.import.title")}</span>
        <Button size="sm" aria-expanded={open} onClick={() => setOpen(!open)}>
          {t("awg.import.btn")}
        </Button>
      </div>
      {open && (
        <>
          <span className="text-[11px] leading-snug text-muted">{t("awg.import.hint")}</span>
          <textarea
            aria-label={t("awg.import.title")}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={t("awg.import.ph")}
            rows={6}
            spellCheck={false}
            autoComplete="off"
            className="w-full rounded-xl border border-line bg-canvas p-3 font-mono text-[11px] leading-[1.6] text-fg outline-none transition-colors duration-200 focus:border-accent"
          />
          <div>
            <Button variant="primary" size="sm" disabled={busy || text.trim() === ""} onClick={() => void apply()}>
              {t("awg.import.do")}
            </Button>
          </div>
        </>
      )}
      {report && (
        <div role="status" className="flex flex-col gap-1 text-xs leading-snug">
          <b>{t.opt(`awg.import.kind.${report.kind}`)}</b>
          {report.filled.length > 0 && <span>{t("awg.import.filled", { list: list(report.filled) })}</span>}
          {report.notes.map((n) => (
            <span key={n}>{t.opt(`awg.import.note.${n}`)}</span>
          ))}
          {report.ignored.length > 0 && <span className="text-muted">{t("awg.import.ignored", { list: list(report.ignored) })}</span>}
          {report.unknown.length > 0 && <span className="text-warn-text">{t("awg.import.unknown", { list: list(report.unknown) })}</span>}
        </div>
      )}
    </div>
  );
}
