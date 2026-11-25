// boot.js: the loader of the page. The head holds only what the first paint needs (land.css, the font); everything else the page runs on is
// named in <script type="application/json" id="mg-boot"> and started here, once the first frame is out: the stylesheet of the panel mock-ups
// (pn.css, 330 KB of rules for a window below the text), the modules, the language bundle. So the first bytes the browser is waiting for are
// the ones that draw the words. The hero window, hidden by land.css until then, is let appear two frames after its stylesheet has loaded, when
// the sheet has been applied with the transitions of the mock-up held still (nothing fades from the unstyled look): html[data-pn].
// No imports: a plain script, deferred.
(() => {
  const d = document, boot = JSON.parse(d.getElementById('mg-boot').textContent);
  const add = (tag, attrs) => { const e = d.createElement(tag); for (const k in attrs) e.setAttribute(k, attrs[k]); d.head.appendChild(e); return e; };
  const shown = () => requestAnimationFrame(() => requestAnimationFrame(() => d.documentElement.setAttribute('data-pn', '')));
  const go = () => {
    for (const href of boot.css || []) { const l = add('link', { rel: 'stylesheet', href }); l.addEventListener('load', shown, { once: true }); l.addEventListener('error', shown, { once: true }); }
    for (const href of boot.preload || []) add('link', { rel: 'modulepreload', href });
    for (const href of boot.fetch || []) add('link', { rel: 'preload', as: 'fetch', href, crossorigin: '' });
    for (const src of boot.modules || []) add('script', { type: 'module', src });
  };
  requestAnimationFrame(() => setTimeout(go, 0)); // after the frame that carries the first paint
})();
