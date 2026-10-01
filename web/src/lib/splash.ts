// The splash is plain markup in index.html (#mg-splash, styled and filled by public/splash.js): it is on screen before this
// bundle runs. Once the first real screen is ready the app takes it down with hideSplash().

const id = "mg-splash";
const fadeMs = 200;

/**
 * Fades the splash out (200 ms) and removes it. No splash (a test, a second call, a splash already gone) is a no-op.
 * A splash that has not faded in yet (a fast load: it waits ~200 ms before it shows) is removed at once, without a flash.
 */
export function hideSplash() {
  const el = document.getElementById(id);
  if (!el || "leaving" in el.dataset) return;
  el.dataset.leaving = "";
  const shown = parseFloat(getComputedStyle(el).opacity);
  if (!(shown > 0.02)) return el.remove();
  // freeze the fade-in where it is, then run the fade-out from there
  el.style.pointerEvents = "none";
  el.style.opacity = String(shown);
  el.style.animation = "none";
  void el.offsetWidth;
  el.style.transition = `opacity ${fadeMs}ms ease-out`;
  el.style.opacity = "0";
  setTimeout(() => el.remove(), fadeMs + 40);
}
