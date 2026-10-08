import { Toast } from "@base-ui/react/toast";
import { createContext, use, useMemo, type ReactNode } from "react";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { Icon } from "./icons";

type Notify = ((message: string, options?: { undo?: () => void; ms?: number }) => void) & { error: (message: string) => void };
const NotifyContext = createContext<Notify>(Object.assign(() => {}, { error: () => {} }));

/**
 * toast("Saved"), toast("User removed", { undo }) or toast.error(errorText(e, t)). A note goes in 3.4 s (8 s with Undo, or the `ms` it asks for, for a long text); an
 * error is red, read out at once and stays 8 s or until closed. One of each at a time: a new note replaces the note, a new
 * error the error, and a note never pushes an error out (the error sits above it).
 */
export const useToast = () => use(NotifyContext);

export function ToastProvider({ children }: { children: ReactNode }) {
  return (
    <Toast.Provider limit={2} timeout={3400}>
      <Bridge>{children}</Bridge>
      <Toast.Portal>
        {/* bottom centre; on the phone it sits above the floating dock */}
        <Toast.Viewport className="pointer-events-none fixed inset-x-0 bottom-[88px] z-40 flex flex-col items-center gap-2 px-4 md:bottom-5">
          <Current />
        </Toast.Viewport>
      </Toast.Portal>
    </Toast.Provider>
  );
}

function Bridge({ children }: { children: ReactNode }) {
  const t = useT();
  const { add } = Toast.useToastManager();
  const undoLabel = t("common.undo");
  // the id is the slot: adding to a taken one replaces what is in it and restarts its timer
  const notify = useMemo<Notify>(
    () =>
      Object.assign(
        (message: string, options?: { undo?: () => void; ms?: number }) =>
          void add({
            id: "note",
            description: message,
            timeout: options?.ms ?? (options?.undo ? 8000 : 3400),
            actionProps: options?.undo ? { children: undoLabel, onClick: options.undo } : undefined,
          }),
        { error: (message: string) => void add({ id: "error", type: "error", priority: "high", timeout: 8000, description: message }) },
      ),
    [add, undoLabel],
  );
  return <NotifyContext value={notify}>{children}</NotifyContext>;
}

function Current() {
  const t = useT();
  const { toasts } = Toast.useToastManager();
  return ["error", "note"].map((slot) => {
    const toast = toasts.find((x) => x.id === slot);
    if (!toast) return null;
    const error = slot === "error";
    return (
      <Toast.Root
        // a refilled slot is a new element, so the new message pops in again
        key={`${slot}${toast.updateKey}`}
        toast={toast}
        className="pointer-events-auto flex max-w-full items-center rounded-field bg-fg px-3.5 py-[11px] text-xs leading-snug text-canvas shadow-(--shadow-toast) animate-[mg-ui-in_0.35s_cubic-bezier(0.2,0.9,0.3,1.3)_both] md:max-w-[480px]"
      >
        <Toast.Content className="flex items-center gap-2.5">
          <span aria-hidden className={cx("grid size-5 flex-none place-items-center rounded-full", error ? "bg-danger" : "bg-accent")}>
            {error ? <span className="text-[13px] leading-none font-extrabold text-[#0c0c0e]">!</span> : <span className="-mt-0.5 h-2 w-1 rotate-45 border-r-2 border-b-2 border-[#0c0c0e]" />}
          </span>
          <Toast.Description className="min-w-0" />
          <Toast.Action className="ml-1 flex-none font-extrabold underline" />
          {error && (
            <Toast.Close aria-label={t("common.close")} className="-mr-1.5 grid size-6 flex-none place-items-center rounded-full opacity-60 transition-opacity hover:opacity-100">
              <Icon name="x" size={12} />
            </Toast.Close>
          )}
        </Toast.Content>
      </Toast.Root>
    );
  });
}
