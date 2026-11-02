// Injected before the page's own scripts: the page's clock (Date, performance.now, timers, requestAnimationFrame) only
// moves when the recorder calls __vclock.advance(ms), and the CSS/WAAPI animations (frozen by the recorder through the
// Animation domain) are stepped by the same amount. So each filmed frame is exactly 1/fps of page time after the last.
(() => {
  const RealDate = Date;
  const realSetTimeout = setTimeout.bind(window);
  const realRaf = requestAnimationFrame.bind(window);
  const START = __START__;
  let now = START; // ms since the epoch
  const perf0 = performance.now();
  let seq = 0;
  const timers = new Map(); // id → { due, fn, args, every }
  let rafs = new Map();

  class VDate extends RealDate {
    constructor(...a) {
      if (a.length === 0) super(now);
      else super(...a);
    }
    static now() {
      return now;
    }
  }
  window.Date = VDate;
  performance.now = () => perf0 + (now - START);
  window.setTimeout = (fn, ms = 0, ...args) => (timers.set(++seq, { due: now + Math.max(0, +ms || 0), fn, args, every: 0 }), seq);
  window.setInterval = (fn, ms = 0, ...args) => (timers.set(++seq, { due: now + Math.max(1, +ms || 0), fn, args, every: Math.max(1, +ms || 0) }), seq);
  window.clearTimeout = window.clearInterval = (id) => timers.delete(id);
  window.requestAnimationFrame = (fn) => (rafs.set(++seq, fn), seq);
  window.cancelAnimationFrame = (id) => rafs.delete(id);
  window.requestIdleCallback = (fn) => window.setTimeout(() => fn({ didTimeout: false, timeRemaining: () => 10 }), 1);
  window.cancelIdleCallback = (id) => timers.delete(id);

  const macrotask = () => new Promise((r) => realSetTimeout(r, 0));
  window.__vclock = {
    now: () => now,
    realRaf: (fn) => realRaf(fn),
    /** Moves the clock by ms: the timers fall due in order (each in a task of its own, so promises settle between them), then one animation frame. */
    async advance(ms) {
      const target = now + ms;
      for (let guard = 0; guard < 5000; guard++) {
        let next = null;
        for (const [id, t] of timers) if (t.due <= target && (!next || t.due < next[1].due || (t.due === next[1].due && id < next[0]))) next = [id, t];
        if (!next) break;
        const [id, t] = next;
        now = Math.max(now, t.due);
        if (t.every) t.due = now + t.every;
        else timers.delete(id);
        try {
          typeof t.fn === "function" ? t.fn(...t.args) : (0, eval)(String(t.fn));
        } catch (e) {
          console.error(e);
        }
        await macrotask();
      }
      now = target;
      for (const a of document.getAnimations()) {
        if (a.playState === "running" || a.pending) a.currentTime = (a.currentTime ?? 0) + ms;
      }
      const due = rafs;
      rafs = new Map();
      const stamp = performance.now();
      for (const fn of due.values()) {
        try {
          fn(stamp);
        } catch (e) {
          console.error(e);
        }
      }
      await macrotask();
    },
  };
})();
