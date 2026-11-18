import { Toast } from "@base-ui/react/toast";
import { createContext, use, useMemo, type ReactNode } from "react";
import { useT } from "@/i18n";
import { useEffectsEnabled } from "@/lib/effects";
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
        {/* bottom centre of the page beside the menu; on the phone it sits above the floating dock, on a desktop above the Nodes page's bulk bar (index.css) */}
        <Toast.Viewport data-toast-viewport className="pointer-events-none fixed inset-x-0 bottom-[104px] z-40 flex flex-col items-center gap-2 px-4 md:bottom-6 md:ps-[calc(var(--menu-w)+16px)]">
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
  const halo = useEffectsEnabled();
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
        className="pointer-events-auto relative isolate flex min-h-11 max-w-full items-center rounded-xl bg-fg py-2 ps-4 pe-2 text-sm leading-5 font-medium text-canvas shadow-(--shadow-toast) animate-[mg-up_0.3s_var(--e)_both] md:max-w-[480px]"
      >
        {/* a halo of accent dots under the toast: part of the effects, off with them */}
        {halo && <span aria-hidden className="toast-halo" />}
        <Toast.Content className="flex items-center gap-4">
          {/* a note is plain words, as drawn; only an error carries a red mark in front, so it is not mistaken for a note */}
          {error && (
            <span aria-hidden className="tone-bad grid size-5 flex-none place-items-center rounded-full bg-(--c) text-[13px] leading-none font-medium text-(--c-ink)">
              !
            </span>
          )}
          <Toast.Description className="min-w-0" />
          <Toast.Action className="flex h-7 flex-none items-center rounded-ctl bg-[color-mix(in_srgb,var(--bg)_8%,transparent)] px-2.5 text-[13px] font-medium transition-colors duration-150 hover:bg-[color-mix(in_srgb,var(--bg)_14%,transparent)] [:root[data-theme=light]_&]:bg-white/14 [:root[data-theme=light]_&]:hover:bg-white/14" />
          {error && (
            <Toast.Close aria-label={t("common.close")} className="-me-0.5 grid size-7 flex-none place-items-center rounded-ctl opacity-60 transition-opacity hover:opacity-100">
              <Icon name="x" size={14} />
            </Toast.Close>
          )}
        </Toast.Content>
      </Toast.Root>
    );
  });
}
