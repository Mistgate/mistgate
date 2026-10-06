import "./sub.css";
import { deviceModal, deviceModalLabel } from "./add";
import { amzActions } from "./amz-actions";
import * as api from "./api";
import { dnsActions } from "./dns-actions";
import { h } from "./dom";
import { dict } from "./i18n";
import { icon } from "./icons";
import { lockActions, lockView, type LockState } from "./lock";
import { addPlatforms, asPlatform, asTheme, detectPlatform, isHex, isReturning, mainProfile, normalize, pickPlatform, safeUrl } from "./logic";
import { createModal } from "./modal";
import { dnsLabel, dnsModal } from "./servers";
import { newAmzState, newDnsState, type Actions, type Ctx, type State } from "./state";
import type { Lang, Theme } from "./types";
import { annKey, brandName, logoSrc, view } from "./view";

// Entry of the public user page. All state is here: the language, the theme and the picked device (remembered on this
// device), the chosen app, whether the QR code is open and whether the announcement was closed. The page data is embedded by
// the server, there is no network I/O except the self-service calls (and the password form's one). A page whose password was
// not entered is the form and nothing else.

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

/** "auto" follows the system; a chosen theme is written on <html> where sub.css reads it. */
function applyTheme(theme: Theme) {
  if (theme === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
}
// `vite -c vite.sub.config.ts` only: &as=<platform> pretends the page is open on that device, &theme=light|dark picks the theme
const devQuery = import.meta.env.DEV ? new URLSearchParams(location.search) : new URLSearchParams();
const startTheme = asTheme(devQuery.get("theme") ?? stored("theme"));
applyTheme(startTheme);

const data = readData();
// `vite -c vite.sub.config.ts` only: the sample cases answer the self-service calls themselves (dev-api.ts); a build drops this
const devApi = import.meta.env.DEV ? (await import("./dev-api")).devApi : (undefined as never);
const want = asPlatform(devQuery.get("as")) ?? detectPlatform(navigator.userAgent, navigator.maxTouchPoints);
if (isHex(data.brand.accent)) document.documentElement.style.setProperty("--accent", data.brand.accent);

const root = document.getElementById("root") as HTMLElement;
const toast = h("div", { class: "toast", role: "status", "aria-live": "polite" });
document.body.append(toast);
let toastTimer = 0;
function flash(message: string) {
  toast.replaceChildren(icon("check", 16), message);
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
    platform: pickPlatform(data, asPlatform(stored("platform")), want),
    detected: want,
    app: "",
    way: "link",
    theme: startTheme,
    qrOpen: false,
    stepsAgain: false,
    annClosed: stored(ann) === "1",
    returning: isReturning(data, stored("setup") === "1"),
    marked: stored("setup") === "1",
    amz: newAmzState(here || "other", mainProfile(data.amnezia?.profiles ?? []), here),
    dns: newDnsState(),
  };
  // the first of the controls (a|b) that is on screen: a row has one for a phone and one for a computer
  const focusKey = (keys: string) =>
    requestAnimationFrame(() => {
      for (const key of keys.split("|")) {
        const el = [...root.querySelectorAll<HTMLElement>(`[data-k="${key}"]`)].find((x) => x.offsetParent !== null || x.getClientRects().length > 0);
        if (el) return el.focus({ preventScroll: true });
      }
    });
  const lockedPage = () => location.reload(); // the cookie is gone or the link was renewed: the reload shows the password form

  const actions: Actions = {
    lang(l) {
      st.lang = l;
      saveLang(l);
      render();
    },
    platform(p) {
      st.platform = p;
      st.app = "";
      store("platform", p);
      render();
    },
    app(key) {
      st.app = key;
      render();
    },
    way(w) {
      st.way = w;
      render();
    },
    theme(t) {
      st.theme = t;
      store("theme", t);
      applyTheme(t);
      render();
    },
    qrOpen(open) {
      st.qrOpen = open;
      render();
    },
    stepsAgain() {
      st.stepsAgain = true;
      render();
    },
    closeAnn() {
      st.annClosed = true;
      store(ann, "1");
      render();
    },
    mark() {
      st.marked = true;
      store("setup", "1");
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
        });
      },
      focus: focusKey,
      locked: lockedPage,
      marked: () => actions.mark(),
    }),
    dns: dnsActions({ data, st, render: () => render(), api: import.meta.env.DEV && data.dns?.endpoint === "dev:" ? devApi : api, focus: focusKey, locked: lockedPage }),
  };

  // the control used last (a tap does not focus a button in Safari): where the dialog gives the focus back
  let lastKey = "";
  document.addEventListener(
    "click",
    (e) => {
      const el = e.target as Element;
      lastKey = el.closest?.("[data-k]")?.getAttribute("data-k") ?? "";
      // a click anywhere but the open menu (or the button that opens it) closes the menu
      if (st.amz.menu && !el.closest?.(".menu") && !el.closest?.('[data-k^="amz-more-"]')) actions.amz.menu(null);
    },
    true,
  );
  document.addEventListener("keydown", (e) => {
    if (!st.amz.menu) return;
    if (e.key === "Escape") {
      const id = st.amz.menu;
      actions.amz.menu(null);
      focusKey(`amz-more-${id}`);
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Home" || e.key === "End") {
      // a menu is walked with the arrows
      const items = [...root.querySelectorAll<HTMLElement>(".menu [role=menuitem]")];
      if (items.length === 0) return;
      e.preventDefault();
      const i = items.indexOf(document.activeElement as HTMLElement);
      const next = e.key === "Home" ? 0 : e.key === "End" ? items.length - 1 : e.key === "ArrowDown" ? (i + 1) % items.length : (i - 1 + items.length) % items.length;
      items[next]!.focus();
    }
  });

  const ctx = (): Ctx => ({ d: data, s: st, a: actions, t: dict[st.lang], support: data.options.show_support ? safeUrl(data.support_url) : "" });
  // the "add a device" dialog needs no Amnezia data for its link branch; a key sheet does
  const dialogOpen = () => st.dns.open !== null || st.amz.adding || (data.amnezia !== null && (st.amz.renew !== null || st.amz.open !== null));

  // the dialog lives outside the tree the page rebuilds
  const modal = createModal({
    label: () => (st.dns.open ? dnsLabel(ctx()) : deviceModalLabel(ctx())),
    lastUsed: () => lastKey,
    onClose: () => {
      // Esc or the backdrop: the state follows the dialog
      if (st.dns.open) actions.dns.open(null);
      else if (st.amz.adding || st.amz.renew || st.amz.open) actions.amz.add(false);
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
    const open = dialogOpen();
    modal.sync(open, open ? (st.dns.open ? dnsModal(ctx()) : deviceModal(ctx())) : []);
  };

  // `vite -c vite.sub.config.ts` only: &way=key picks the key way of the first-visit steps, &sheet=pick|link|key opens the "add a device" sheet, &menu=<device id> a row's menu, &ask=remove:<id>|rotate:<id> its question
  if (import.meta.env.DEV) {
    const sheet = devQuery.get("sheet");
    if (sheet === "pick" || sheet === "link" || sheet === "key") {
      st.amz.adding = true;
      st.amz.pane = sheet;
    }
    st.amz.menu = devQuery.get("menu");
    if (devQuery.get("way") === "key") st.way = "key";
    const [kind, id] = (devQuery.get("ask") ?? "").split(":");
    if ((kind === "remove" || kind === "rotate") && id) st.amz.confirm = { id, kind };
  }
  render(true);
}
// the static favicon in sub.html is a placeholder (so the browser never asks for /favicon.ico); show the brand
document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.setAttribute("href", logoSrc(data));
