import { useEffect, useRef, useState, type ReactNode } from "react";
import { Modal } from "@/components/ui/modal";
import { useT } from "@/i18n";
import { SxRoot } from "./sx-bits";
import "./settings-b.css";

/**
 * A window of the board (us-dlg hl-dlg uq-dlg ai-dlg): the title, a body of lines and fields, a footer of buttons. The window is drawn in
 * a portal outside the settings root, so the body and the buttons each wear their own (`.sx.sx-dlg`, `.sx.sx-dfoot`). `locked` takes the
 * round close button away (a check or a restore that is running is not abandoned by a click).
 */
export function SxWindow({ open, onOpenChange, title, shot, locked, footer, children }: { open: boolean; onOpenChange: (open: boolean) => void; title: ReactNode; shot?: string; locked?: boolean; footer?: ReactNode; children: ReactNode }) {
  const t = useT();
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={title} shot={shot} closeLabel={locked ? undefined : t("sx.dlg.close")} footer={footer && <SxRoot className="sx-dfoot">{footer}</SxRoot>}>
      <SxRoot className="sx-dlg">{children}</SxRoot>
    </Modal>
  );
}

/**
 * The keys that appeared since the first time the list was seen, for a moment (the board's `fresh`: a passkey or a backup glows once, then
 * settles). The first list is the page's own, not news; `initial` is for a scene that starts one glowing.
 */
export function useFreshKeys(keys: readonly string[] | undefined, initial: readonly string[] = [], ms = 1600): ReadonlySet<string> {
  const known = useRef<Set<string> | null>(null);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(() => new Set(initial));
  const gone = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => {
    if (!keys) return;
    if (!known.current) {
      known.current = new Set(keys);
      return;
    }
    const added = keys.filter((k) => !known.current!.has(k));
    for (const k of added) known.current.add(k);
    if (added.length > 0) {
      setFresh(new Set(added));
      clearTimeout(gone.current);
      gone.current = setTimeout(() => setFresh(new Set()), ms);
    }
  }, [keys, ms]);
  useEffect(() => {
    if (initial.length === 0) return;
    const id = setTimeout(() => setFresh(new Set()), ms);
    return () => clearTimeout(id);
  }, [initial.length, ms]);
  useEffect(() => () => clearTimeout(gone.current), []);
  return fresh;
}
