// Progressive enhancement only: the site reads and navigates fine without this file.
(() => {
  const d = document, root = d.documentElement;
  const T = JSON.parse(d.body.dataset.t || "{}");
  const $ = (s, el = d) => el.querySelector(s);
  const $$ = (s, el = d) => [...el.querySelectorAll(s)];

  // theme: no saved choice = follow the system; the toggle pins light or dark
  const dark = () => (root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches);
  $$("[data-theme-toggle]").forEach((b) => b.addEventListener("click", () => {
    const next = dark() ? "light" : "dark";
    root.dataset.theme = next;
    try { localStorage.setItem("mg-theme", next); } catch {}
  }));

  // language menu (<details class="lang">): works without JS; here it also closes on a click elsewhere, on Esc (focus returns
  // to its button) and when another <details> opens
  const menus = $$("details.lang");
  d.addEventListener("click", (e) => menus.forEach((m) => { if (m.open && !m.contains(e.target)) m.open = false; }));
  d.addEventListener("keydown", (e) => { if (e.key === "Escape") menus.forEach((m) => { if (m.open) { m.open = false; $("summary", m).focus(); } }); });
  d.addEventListener("toggle", (e) => { if (e.target.open) menus.forEach((m) => { if (m !== e.target) m.open = false; }); }, true);

  // drawer (below 900 px): without JS the same links work through :target
  const nav = $("#nav");
  const setNav = (open) => {
    root.classList.toggle("nav-open", open);
    if (!open && location.hash === "#nav") history.replaceState(null, "", location.pathname + location.search);
    if (open) $("a, button", nav)?.focus(); else $("[data-menu]")?.focus({ preventScroll: true });
  };
  $$("[data-menu]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); setNav(true); }));
  $$("[data-menu-close]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); setNav(false); }));
  d.addEventListener("keydown", (e) => { if (e.key === "Escape" && root.classList.contains("nav-open")) setNav(false); });
  $$("a", nav || d.body).forEach((a) => a.addEventListener("click", () => root.classList.remove("nav-open")));
  matchMedia("(min-width: 900px)").addEventListener("change", (e) => e.matches && root.classList.remove("nav-open"));

  // copy buttons on code blocks
  $$(".code").forEach((box) => {
    const b = d.createElement("button");
    b.type = "button"; b.className = "copy"; b.textContent = T.copy;
    b.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText($("pre", box).innerText.replace(/\n$/, "")); } catch { return; }
      b.textContent = T.copied; b.classList.add("is-done");
      setTimeout(() => { b.textContent = T.copy; b.classList.remove("is-done"); }, 1600);
    });
    $(".code__bar", box).append(b);
  });

  // table of contents: highlight the section being read
  const links = $$(".toc a");
  if (links.length && "IntersectionObserver" in window) {
    const byId = new Map(links.map((a) => [decodeURIComponent(a.hash.slice(1)), a]));
    const io = new IntersectionObserver((es) => es.forEach((e) => {
      if (!e.isIntersecting) return;
      links.forEach((a) => a.classList.remove("is-active"));
      byId.get(e.target.id)?.classList.add("is-active");
    }), { rootMargin: "-80px 0px -70% 0px" });
    byId.forEach((_, id) => { const h = d.getElementById(id); if (h) io.observe(h); });
  }

  // search: titles, descriptions, headings and names in code (flags, variables, tools), index fetched on first use
  const dlg = $("#search");
  if (!dlg || !dlg.showModal) return;
  const q = $("#search-q"), list = $("#search-list"), none = $(".search__empty", dlg);
  let index = null, shown = [], sel = -1;
  const load = () => index ||= fetch(dlg.dataset.index).then((r) => r.json()).catch(() => []);
  /*@find*/
  const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
  const mark = (i) => {
    sel = i;
    $$("li", list).forEach((li, n) => li.setAttribute("aria-selected", n === i));
    q.setAttribute("aria-activedescendant", i >= 0 ? `sr-${i}` : "");
    $(`#sr-${i}`)?.scrollIntoView({ block: "nearest" });
  };
  const draw = async () => {
    shown = find(await load(), q.value);
    list.innerHTML = shown.map((h, n) => `<li role="option" id="sr-${n}" aria-selected="false"><a href="${h.url}"><span class="t">${h.code ? `<code>${esc(h.label)}</code>` : esc(h.label)}${h.sub ? `<small>${esc(T.inn)} ${esc(h.sub)}</small>` : ""}</span>${h.group ? `<span class="chip">${esc(h.group)}</span>` : ""}</a></li>`).join("");
    none.hidden = shown.length > 0 || !q.value.trim();
    q.setAttribute("aria-expanded", shown.length > 0);
    mark(shown.length ? 0 : -1);
  };
  const open = () => { dlg.showModal(); q.select(); load(); };
  $$("[data-search-open]").forEach((b) => b.addEventListener("click", open));
  $$("[data-search-close]").forEach((b) => b.addEventListener("click", () => dlg.close()));
  dlg.addEventListener("click", (e) => { if (e.target === dlg) dlg.close(); });
  dlg.addEventListener("click", (e) => { if (e.target.closest("a")) dlg.close(); });
  // while an input method composes (pinyin, kana), the half-typed letters are not a query and Enter picks a candidate
  q.addEventListener("input", (e) => { if (!e.isComposing) draw(); });
  q.addEventListener("compositionend", draw);
  q.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); if (shown.length) mark((sel + (e.key === "ArrowDown" ? 1 : -1) + shown.length) % shown.length); }
    else if (e.key === "Enter" && shown[sel]) { e.preventDefault(); location.href = shown[sel].url; }
  });
  if (/Mac|iPhone|iPad/.test(navigator.platform)) $$("[data-mod]").forEach((k) => (k.textContent = "⌘"));
  d.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") { e.preventDefault(); if (dlg.open) dlg.close(); else open(); return; }
    if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey && !dlg.open && !/^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName) && !e.target.isContentEditable) { e.preventDefault(); open(); }
  });
})();
