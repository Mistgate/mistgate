import "./sub.css";
import { h } from "./dom";
import { addModal, label, newAmzState } from "./amnezia";
import { amzActions } from "./amz-actions";
import * as api from "./api";
import { dict } from "./i18n";
import { lockActions, lockView, type LockState } from "./lock";
import { addPlatforms, detectPlatform, isHex, mainProfile, normalize, pickPlatform } from "./logic";
import { createModal } from "./modal";
import type { Lang } from "./types";
import { annKey, brandName, logoSrc, view, type Actions, type State } from "./view";

// Entry of the public user page. All state is here: the language (remembered on this device), the picked platform,
// whether the QR code is open and whether the announcement was closed. The page data is embedded by the server, there is
// no network I/O except the self-service calls (and the password form's one). A page whose password was not entered is
// the form and nothing else.

function readData() {
  try {
    return normalize(JSON.parse(document.getElementById("mg-data")?.textContent ?? ""));
  } catch {
    return normalize(null);
  }
}

// localStorage can throw or be empty (private windows, blocked site data): what it keeps is a convenience only
function stored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function store(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // ignore
  }
}
const langKey = "lang";
function savedLang(): Lang | null {
  const v = stored(langKey);
  return v === "ru" || v === "en" ? v : null;
}
const saveLang = (l: Lang) => store(langKey, l);

const data = readData();
// `vite -c vite.sub.config.ts` only: the sample cases answer the self-service calls themselves (dev-api.ts); a build drops this
const devApi = import.meta.env.DEV ? (await import("./dev-api")).devApi : (undefined as never);
const want = detectPlatform(navigator.userAgent, navigator.maxTouchPoints);
if (isHex(data.brand.accent)) document.documentElement.style.setProperty("--accent", data.brand.accent);

const root = document.getElementById("root") as HTMLElement;
const toast = h("div", { class: "toast", role: "status", "aria-live": "polite" });
document.body.append(toast);
let toastTimer = 0;
function flash(message: string) {
  toast.textContent = message;
  toast.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => toast.classList.remove("show"), 1800);
}

// Clipboard API needs a secure context and a user gesture; the textarea path covers plain http and old WebViews.
function copy(text: string, message: string, done?: () => void) {
  const ok = () => {
    flash(message);
    done?.();
  };
  const fallback = () => {
    const ta = h("textarea", { readonly: true, style: { position: "fixed", opacity: "0" } });
    ta.value = text;
    document.body.append(ta);
    ta.select();
    try {
      if (document.execCommand("copy")) ok();
    } finally {
      ta.remove();
    }
  };
  if (navigator.clipboard) navigator.clipboard.writeText(text).then(ok, fallback);
  else fallback();
}

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const link = h("a", { href: url, download: filename });
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function setDocument(lang: Lang) {
  document.documentElement.lang = lang;
  document.title = data.title.trim() || brandName(data);
}

if (data.locked) {
  // ---- the password form ----
  const st: LockState = { lang: savedLang() ?? data.lang, pw: "", busy: false, error: "", note: "" };
  const actions = lockActions({
    st,
    endpoint: data.unlock_url,
    unlock: import.meta.env.DEV && data.unlock_url === "dev:" ? (await import("./dev-api")).devUnlock : api.unlock,
    render: () => render(),
    reload: () => (import.meta.env.DEV && data.unlock_url === "dev:" ? location.assign("?case=multi") : location.reload()),
    save: saveLang,
  });
  const render = (animate = false) => {
    const tree = lockView(data, st, actions);
    if (animate) tree.classList.add("enter");
    const key = (document.activeElement as HTMLElement | null)?.dataset?.k;
    root.replaceChildren(tree);
    setDocument(st.lang);
    root.querySelector<HTMLElement>(key ? `[data-k="${key}"]` : "[data-autofocus]")?.focus({ preventScroll: true });
  };
  render(true);
} else {
  // ---- the page ----
  const ann = annKey(data.announcement);
  const here = want && (addPlatforms as readonly string[]).includes(want) ? want : "";
  const st: State = {
    lang: savedLang() ?? data.lang,
    platform: pickPlatform(data, want),
    qrOpen: false,
    annClosed: stored(ann) === "1",
    amz: newAmzState(here || "other", mainProfile(data.amnezia?.profiles ?? []), here),
  };
  const focusKey = (key: string) => requestAnimationFrame(() => root.querySelector<HTMLElement>(`[data-k="${key}"]`)?.focus({ preventScroll: true }));

  const actions: Actions = {
    lang(l) {
      st.lang = l;
      saveLang(l);
      render();
    },
    platform(p) {
      st.platform = p;
      render();
    },
    qrOpen(open) {
      st.qrOpen = open; // no re-render: <details> already shows it
    },
    closeAnn() {
      st.annClosed = true;
      store(ann, "1");
      render();
    },
    copy,
    download,
    amz: amzActions({
      data,
      st,
      render: () => render(),
      api: import.meta.env.DEV && data.amnezia?.endpoints === "dev:" ? devApi : api,
      reveal(id) {
        requestAnimationFrame(() => {
          const el = document.getElementById(id);
          el?.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
          el?.querySelector<HTMLElement>("button, select, input")?.focus({ preventScroll: true });
        });
      },
      focus: focusKey,
      locked: () => location.reload(), // the cookie is gone or the link was renewed: the reload shows the password form
    }),
  };

  // the control used last (a tap does not focus a button in Safari): where the dialog gives the focus back
  let lastKey = "";
  document.addEventListener("click", (e) => (lastKey = (e.target as Element).closest?.("[data-k]")?.getAttribute("data-k") ?? ""), true);

  // the dialog lives outside the tree the page rebuilds
  const modal = createModal({
    label: () => {
      const t = dict[st.lang];
      const x = st.amz.renew ? data.amnezia?.devices.find((d) => d.id === st.amz.renew) : undefined;
      return x ? t.renewT(label(x, t)) : t.awgAddT;
    },
    lastUsed: () => lastKey,
    onClose: () => {
      if (st.amz.adding || st.amz.renew) actions.amz.add(false); // Esc or the backdrop: the state follows the dialog
    },
    refocus: (key) => root.querySelector<HTMLElement>(`[data-k="${key}"]`)?.focus({ preventScroll: true }),
  });

  const render = (animate = false) => {
    // the tree is rebuilt on every change; keep keyboard focus on the control that was used
    const key = modal.el.contains(document.activeElement) ? undefined : (document.activeElement as HTMLElement | null)?.dataset?.k;
    const tree = view(data, st, actions);
    if (animate) tree.classList.add("enter");
    root.replaceChildren(tree);
    setDocument(st.lang);
    if (key) root.querySelector<HTMLElement>(`[data-k="${key}"]`)?.focus({ preventScroll: true });
    const open = (st.amz.adding || st.amz.renew !== null) && data.amnezia !== null;
    modal.sync(open, open ? addModal({ d: data, s: st, a: actions, t: dict[st.lang] }) : []);
  };

  render(true);
}
// the static favicon in sub.html is a placeholder (so the browser never asks for /favicon.ico); show the brand
document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.setAttribute("href", logoSrc(data));
