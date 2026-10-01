// The first thing the admin page runs, before the bundle: makes the splash in index.html (#mg-splash) look like this device
// and this instance. A classic script on purpose: the panel's CSP allows no inline scripts. Everything is best-effort;
// without it the splash is still there, dark and in English, with the built-in badge.
(function () {
  var doc = document;
  var root = doc.documentElement;
  function pref(key) {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  }

  // The same choices lib/theme.ts, lib/accent.ts and i18n make later; made now so the first paint is already right.
  root.dataset.theme = pref("theme") === "light" ? "light" : "dark";
  var accent = pref("accent") || pref("instance-accent"); // the device's own pick wins over the instance default
  if (accent && /^#[0-9a-f]{6}$/i.test(accent)) root.style.setProperty("--accent", accent);
  var saved = pref("lang");
  var lang = saved === "ru" || saved === "en" ? saved : /^ru/i.test(navigator.language || "") ? "ru" : "en";
  root.lang = lang;

  var text = {
    ru: { load: "Загрузка…", fail: "Не удалось загрузить панель. Обновите страницу.", reload: "Обновить" },
    en: { load: "Loading…", fail: "Cannot load the panel. Reload the page.", reload: "Reload" },
  }[lang];

  function fill() {
    var el = doc.getElementById("mg-splash");
    if (!el) return;
    el.querySelector(".sp-sr").textContent = text.load;
    el.querySelector(".sp-fail p").textContent = text.fail;
    var button = el.querySelector(".sp-fail button");
    button.textContent = text.reload;
    button.addEventListener("click", function () {
      location.reload();
    });

    // the brand lib/brand.ts cached on the last visit: {"w": [head, tail], "l": sanitized svg | null}
    try {
      var cached = JSON.parse(pref("brand") || "null");
      if (cached && cached.w && typeof cached.w[0] === "string" && typeof cached.w[1] === "string") {
        el.querySelector(".sp-a").textContent = cached.w[0].slice(0, 24);
        el.querySelector(".sp-b").textContent = cached.w[1].slice(0, 24);
      }
      var logo = cached && cached.l;
      // sanitized when it was cached; checked again because it goes in as markup
      if (typeof logo === "string" && /^<svg[\s>]/.test(logo) && !/<script|<foreignObject|\son\w+\s*=|javascript:/i.test(logo)) {
        var mark = el.querySelector(".sp-mark");
        mark.innerHTML = logo;
        mark.setAttribute("data-kind", "custom"); // an uploaded logo only breathes as a whole
      }
    } catch {}

    // the bundle never started (a stale chunk after an update, a dropped connection): say so, with a way out
    setTimeout(function () {
      var still = doc.getElementById("mg-splash");
      if (still) still.setAttribute("data-state", "fail");
    }, 10000);
  }
  if (doc.readyState === "loading") doc.addEventListener("DOMContentLoaded", fill);
  else fill();
})();
