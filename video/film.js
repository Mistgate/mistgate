// The film engine shared by the compositions. Everything on screen is a function of the time HyperFrames seeks to
// (the hf-seek protocol): the windows show frames of recorded takes (record.mjs), a camera moves over a "world" where
// the windows sit, a cursor is drawn from the take's track, captions and the outro fade by their own clocks.
//
//   Film.run({ duration, tracks: ["hero-admin", ...], build: (lang, tracks) => spec })
//
// spec = {
//   windows: [{ id, take, x, y, w, h, kind: "window" | "phone", segments: [{ at, from, to, speed }], cursor, opacity(t) }],
//   camera: [{ t, cx, cy, vw, d }],      // world centre and visible width; each key moves there in d seconds from t
//   captions: [{ at, end, text }],       // one line each, on a solid plate at the bottom
//   loop: 0.5, outroAt                   // the last seconds cross back to the first frame; the outro starts at outroAt
// }
(() => {
  const W = 1920;
  const H = 1080;
  const FPS = 30;
  const clamp = (x, a = 0, b = 1) => Math.min(b, Math.max(a, x));
  const inOut = (t) => (t < 0.5 ? 4 * t * t * t : 1 - (-2 * t + 2) ** 3 / 2);
  const outCubic = (t) => 1 - (1 - t) ** 3;
  const lerp = (a, b, k) => a + (b - a) * k;
  const pad5 = (n) => String(n).padStart(5, "0");

  const loadScript = (src) =>
    new Promise((res, rej) => {
      const s = document.createElement("script");
      s.src = src;
      s.onload = res;
      s.onerror = () => rej(new Error(`cannot load ${src}`));
      document.head.append(s);
    });

  /** The take frame a window shows at t: the segment playing (or the last one that ended holds its last frame). */
  function frameAt(segments, t) {
    let f = segments[0].from;
    for (const s of segments) {
      if (t < s.at) break;
      const n = s.from + Math.floor((t - s.at) * FPS * s.speed + 1e-6);
      f = Math.min(n, s.to);
    }
    return f;
  }
  /** The timeline time at which a window first shows take frame f (for placing camera keys and captions on events). */
  function timeOf(segments, f) {
    for (const s of segments) if (f >= s.from && f <= s.to) return s.at + (f - s.from) / FPS / s.speed;
    return segments.at(-1).at;
  }

  /** Camera keys: each one moves from wherever the camera is at its start to its own state in d seconds. */
  function cameraAt(keys, t) {
    const mix = (a, b, p) => {
      const k = inOut(clamp(p));
      return { cx: lerp(a.cx, b.cx, k), cy: lerp(a.cy, b.cy, k), vw: Math.exp(lerp(Math.log(a.vw), Math.log(b.vw), k)) };
    };
    const progress = (k, at) => (k.d ? (at - k.t) / k.d : 1);
    let from = keys[0];
    let key = keys[0];
    for (let i = 1; i < keys.length && keys[i].t <= t; i++) {
      from = mix(from, key, progress(key, keys[i].t));
      key = keys[i];
    }
    return mix(from, key, progress(key, t));
  }

  /**
   * Centre and width that show a rect (world units) with padding on the 16:9 frame, above the caption band at the
   * bottom (band px of the output) so a caption never covers what the cursor works on.
   */
  function fit(r, pad = 60, dx = 0, dy = 0, band = 150) {
    const vw = Math.max(r.w + 2 * pad, ((r.h + 2 * pad) * W) / (H - band));
    return { cx: r.x + r.w / 2 + dx, cy: r.y + r.h / 2 + dy + ((band / 2) * vw) / W, vw };
  }

  const cursorSvg = `<svg viewBox="0 0 24 36" width="24" height="36"><path d="M2 2 L2 28.5 L8.6 22.6 L13 33 L17.4 31.1 L13.1 21 L22 21 Z" fill="#0c0c0e" stroke="#fff" stroke-width="2" stroke-linejoin="round"/></svg>`;

  function run({ duration, tracks: trackNames, build }) {
    const stage = document.getElementById("stage");
    let spec;
    let els;
    let lang = "en";
    const ready = (async () => {
      lang = window.__hyperframes?.getVariables?.().lang || new URLSearchParams(location.search).get("lang") || "en";
      document.documentElement.lang = lang === "zh" ? "zh-CN" : lang;
      document.documentElement.dataset.lang = lang;
      await Promise.all(trackNames.map((n) => loadScript(`assets/takes/${n}-${lang}/track.js`)));
      const tracks = Object.fromEntries(trackNames.map((n) => [n, window.__tracks[`${n}-${lang}`]]));
      spec = build(lang, tracks, { fit, timeOf });
      spec.tracks = Object.fromEntries(trackNames.map((n) => [n, tracks[n]]));
      els = spec.windows.map((w) => {
        const el = document.createElement("div");
        el.className = `win ${w.kind}`;
        const screen = document.createElement("div");
        screen.className = "screen";
        const img = document.createElement("img");
        img.alt = "";
        screen.append(img);
        el.append(screen);
        stage.append(el);
        return { el, screen, img, src: "" };
      });
      els.tap = document.createElement("div");
      els.tap.className = "tap";
      stage.append(els.tap);
      els.cursor = document.createElement("div");
      els.cursor.className = "cursor";
      els.cursor.innerHTML = cursorSvg;
      stage.append(els.cursor);
      els.caps = spec.captions.map((c) => {
        const el = document.createElement("div");
        el.className = "cap";
        el.innerHTML = "<span></span>";
        el.firstChild.textContent = c.text;
        stage.append(el);
        return el;
      });
      for (const el of document.querySelectorAll("[data-text]")) el.textContent = spec.text?.[el.dataset.text] ?? el.textContent;
      await document.fonts.ready;
    })();

    function render(time) {
      const loop = spec.loop ?? 0;
      const tail = loop ? clamp((time - (duration - loop)) / loop) : 0;
      // the last `loop` seconds show the opening frame under the fading outro, so the end meets the start
      const t = tail > 0 ? 0 : time;
      const cam = cameraAt(spec.camera, t);
      const s = W / cam.vw;
      const X = (x) => (x - cam.cx) * s + W / 2;
      const Y = (y) => (y - cam.cy) * s + H / 2;
      const waits = [];
      let cursor = null;
      let tap = null;
      spec.windows.forEach((w, i) => {
        const e = els[i];
        const op = w.opacity ? w.opacity(t) : 1;
        e.el.style.opacity = op;
        e.el.style.display = op <= 0 ? "none" : "";
        const dx = w.offset ? w.offset(t) : 0;
        const dy = w.lift ? w.lift(t) : 0;
        const bez = w.kind === "phone" ? 11 : 0;
        Object.assign(e.el.style, {
          left: `${X(w.x + dx - bez)}px`, top: `${Y(w.y + dy - bez)}px`, width: `${(w.w + 2 * bez) * s}px`, height: `${(w.h + 2 * bez) * s}px`,
          borderRadius: `${(w.kind === "phone" ? 58 : 14) * s}px`, padding: `${bez * s}px`,
        });
        e.screen.style.borderRadius = `${(w.kind === "phone" ? 47 : 14) * s}px`;
        if (op <= 0) return;
        const f = frameAt(w.segments, t);
        const src = `assets/takes/${w.take}-${lang}/f${pad5(f)}.png`;
        if (e.src !== src) {
          e.src = src;
          e.img.src = src;
          waits.push(e.img.decode().catch(() => {}));
        }
        if (w.taps) {
          // a finger on a phone: a ring where it touched, for a moment after the touch
          const tr = spec.tracks[w.take];
          const c = tr.clicks.filter((k) => k <= f).at(-1);
          const age = c === undefined ? 99 : (f - c) / FPS / (w.segments[0].speed || 1);
          if (age < 0.4) {
            const [cx, cy] = tr.frames[Math.min(c, tr.frames.length - 1)];
            tap = { x: X(w.x + dx + cx), y: Y(w.y + dy + cy), k: age / 0.4, s };
          }
        }
        if (w.cursor) {
          const tr = spec.tracks[w.take];
          const [cx, cy, down] = tr.frames[Math.min(f, tr.frames.length - 1)];
          const inside = cx >= 0 && cy >= 0 && cx <= w.w && cy <= w.h;
          cursor = inside ? { x: X(w.x + dx + cx), y: Y(w.y + dy + cy), down, op: clamp((op - 0.6) / 0.4) } : null;
        }
      });
      if (tap) {
        const r = 22 * tap.s * (0.8 + 0.5 * outCubic(tap.k));
        Object.assign(els.tap.style, { display: "", left: `${tap.x - r}px`, top: `${tap.y - r}px`, width: `${2 * r}px`, height: `${2 * r}px`, opacity: String(0.9 * (1 - tap.k)) });
      } else els.tap.style.display = "none";
      const c = els.cursor;
      if (cursor && cursor.op > 0) {
        const size = 1.25 * clamp(0.75 + 0.25 * s, 0.9, 1.35) * (cursor.down ? 0.86 : 1);
        c.style.display = "";
        c.style.opacity = cursor.op;
        c.style.transform = `translate(${cursor.x - 2 * size}px, ${cursor.y - 2 * size}px) scale(${size})`;
      } else c.style.display = "none";

      // captions: a short fade and rise in, a fade out, by each caption's own window
      spec.captions.forEach((cap, i) => {
        const a = cap.at;
        const k = clamp((time - a) / 0.35) * clamp((cap.end - time) / 0.25);
        const span = els.caps[i].firstChild;
        span.style.opacity = tail > 0 ? 0 : outCubic(k);
        span.style.transform = `translateY(${(1 - outCubic(clamp((time - a) / 0.35))) * 10}px)`;
      });
      // the outro: the ground comes up, the mark rises, the word wipes in beside it, the address follows
      const o = document.getElementById("outro");
      if (o && spec.outroAt !== undefined) {
        const k = (a, d) => clamp((time - (spec.outroAt + a)) / d);
        o.style.opacity = clamp((time - spec.outroAt) / 0.4) * (1 - tail);
        const mark = o.querySelector("img");
        mark.style.opacity = outCubic(k(0.15, 0.5));
        mark.style.transform = `translateY(${(1 - outCubic(k(0.15, 0.6))) * 14}px) scale(${0.94 + 0.06 * outCubic(k(0.15, 0.6))})`;
        o.querySelector(".word").style.clipPath = `inset(-20% ${100 - 100 * outCubic(k(0.35, 0.6))}% -20% 0)`;
        o.querySelector(".url").style.opacity = outCubic(k(0.85, 0.5));
      }
      return Promise.all(waits);
    }

    window.addEventListener("hf-seek", (e) => e.detail.waitUntil(ready.then(() => render(e.detail.time))));
    window.__film = { ready, render: (t) => ready.then(() => render(t)), spec: () => spec };
  }

  window.Film = { run, fit, clamp, inOut, outCubic, W, H, FPS };
})();
