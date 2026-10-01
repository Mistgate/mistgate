import { h, type Kid } from "./dom";

// One modal over the page: a native <dialog> shown with showModal(), so Esc closes it, the rest of the page is inert (focus
// cannot leave the dialog) and the backdrop is the browser's. The page rebuilds its whole tree on every change, and the
// opener is part of that tree, so the controller remembers the opener by its data-k and focuses the new copy on close.
//
// The dialog element itself lives outside the page tree; `sync` is called after every draw with whether it should be open
// and what it should hold.

export type Modal = {
  el: HTMLDialogElement;
  /** Opens or closes the dialog to match `open` and puts `content` in it (the page's state decides, never the dialog). */
  sync(open: boolean, content: Kid[]): void;
};

export type ModalDeps = {
  /** The user closed it (Esc, the backdrop, a button that calls dialog.close()): the page's state must follow. */
  onClose: () => void;
  /** Focuses the control with this data-k in the page, after the dialog is gone. */
  refocus: (key: string) => void;
  /** The accessible name. */
  label: () => string;
  /** The data-k of the control that was last used (a tap does not focus a button in every browser): the fallback opener. */
  lastUsed?: () => string;
};

const focusable = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function createModal({ onClose, refocus, label, lastUsed }: ModalDeps): Modal {
  const el = h("dialog", { class: "modal" });
  let opener = "";
  let downOnBackdrop = false;

  // showModal is missing in very old WebViews: the dialog then opens non-modally, which still works
  const show = () => (typeof el.showModal === "function" ? el.showModal() : el.setAttribute("open", ""));
  const hide = () => (typeof el.close === "function" ? el.close() : el.removeAttribute("open"));

  // a drag that starts in the box and ends on the backdrop is a text selection, not a click on the backdrop
  el.addEventListener("pointerdown", (e) => (downOnBackdrop = e.target === el));
  el.addEventListener("click", (e) => {
    if (e.target === el && downOnBackdrop) hide();
    downOnBackdrop = false;
  });
  // Esc and close() both end in "close"
  el.addEventListener("close", () => {
    onClose();
    if (opener) refocus(opener);
    opener = "";
  });

  return {
    el,
    sync(open, content) {
      if (!open) {
        if (el.open) hide();
        return;
      }
      el.setAttribute("aria-label", label());
      const had = el.open;
      const key = had && el.contains(document.activeElement) ? ((document.activeElement as HTMLElement).dataset?.k ?? "") : "";
      el.replaceChildren(h("div", { class: "mbox" }, ...content));
      if (!el.isConnected) document.body.append(el);
      if (!had) {
        opener = (document.activeElement as HTMLElement | null)?.dataset?.k || lastUsed?.() || "";
        show();
      }
      // keep the keyboard where it was; when that control is gone (the form became the key), the one marked autofocus,
      // else the first control
      const keep = key ? el.querySelector<HTMLElement>(`[data-k="${key}"]`) : null;
      if (keep) keep.focus({ preventScroll: true });
      else if (!had || !el.contains(document.activeElement)) {
        (el.querySelector<HTMLElement>("[data-autofocus]") ?? el.querySelector<HTMLElement>(focusable))?.focus({ preventScroll: true });
      }
    },
  };
}
