// The wave across the whole window, from the control that was pressed: it plays when the theme or the language changes and
// when the menu folds or opens (the redesign board's shell). A shader draws only a smooth colour field at a third of the
// resolution (about 0.15 Mpx); the dots are a CSS mask, which is the compositor's work, not the shader's. The GL context
// is made on the first wave and frames run only while it lasts. Where WebGL is missing a CSS ring (.mg-wave-css) plays instead.
// Off with the effects switch (Settings -> Interface) and with reduced motion; it is decoration only, nothing waits for it.
import { effectsEnabled, prefersReducedMotion } from "@/lib/effects";

export type WaveOrigin = { x: number; y: number };

const VS = "attribute vec2 p;void main(){gl_Position=vec4(p,0.,1.);}";
const FS = [
  "precision mediump float;",
  "uniform vec2 uO;uniform float uT,uR,uW,uL,uC;",
  "vec3 pal(float t){return vec3(.40,.82,.68)+vec3(.24,.16,.26)*cos(6.28318*(t+vec3(.04,.18,.50)));}",
  "void main(){",
  " vec2 q=gl_FragCoord.xy-uO;float d=length(q);float a=atan(q.y,q.x);",
  " float e=1.-pow(1.-uT,3.);float R=uR*e;float w=uW*(.6+1.2*e);",
  " float n=sin(a*3.+uT*5.)*.5+sin(a*7.-uT*4.)*.27;",
  " float x=(d-R+n*w*.14)/w;float x2=(d-R*.78+n*w*.09)/(w*.6);float x3=(d-R*.58)/(w*.44);",
  " float I=exp(-x*x*2.4)+exp(-x2*x2*3.2)*.42+exp(-x3*x3*3.2)*.18;",
  " float rim=exp(-pow((x-.72)*7.,2.));",
  " float fade=smoothstep(0.,.06,uT)*(1.-smoothstep(.45,1.,uT));",
  " vec3 col=pal(x*.28+a*.10+uT*.30);",
  " float v=clamp(I*.72+rim*.42,0.,1.)*fade;",
  " if(uL<.5){gl_FragColor=vec4(mix(col,vec3(1.),rim*.4)*v,0.);}",
  " else{gl_FragColor=vec4(col*uC*v,v);}",
  "}",
].join("\n");

type Gl = {
  cv: HTMLCanvasElement;
  gl: WebGLRenderingContext;
  u: Record<"O" | "T" | "R" | "W" | "L" | "C", WebGLUniformLocation | null>;
  raf: number;
};
let state: Gl | null = null;
let fallback: { el: HTMLElement; timer: number } | null = null;

// where the person last pressed, for a change made from the keyboard or a menu that is already closed
let pointer: WaveOrigin = { x: 0, y: 0 };
if (typeof addEventListener === "function") {
  addEventListener("pointerdown", (e) => void (pointer = { x: e.clientX, y: e.clientY }), { capture: true, passive: true });
}

/** The middle of an element, or (no element) where the person last pressed. */
export function originOf(el?: Element | null): WaveOrigin {
  const r = el?.getBoundingClientRect();
  return r && (r.width || r.height) ? { x: r.left + r.width / 2, y: r.top + r.height / 2 } : pointer;
}

function canvas(): HTMLCanvasElement {
  let cv = document.querySelector<HTMLCanvasElement>("canvas.mg-wave");
  if (!cv) {
    cv = document.createElement("canvas");
    cv.className = "mg-wave";
    cv.setAttribute("aria-hidden", "true");
    document.body.append(cv);
  }
  return cv;
}

function init(cv: HTMLCanvasElement): Gl | null {
  if (state?.cv === cv) return state;
  const gl = cv.getContext("webgl", { alpha: true, premultipliedAlpha: true, antialias: false, depth: false, stencil: false, desynchronized: true });
  if (!gl) return null;
  const mk = (type: number, src: string) => {
    const s = gl.createShader(type)!;
    gl.shaderSource(s, src);
    gl.compileShader(s);
    return s;
  };
  const prog = gl.createProgram()!;
  gl.attachShader(prog, mk(gl.VERTEX_SHADER, VS));
  gl.attachShader(prog, mk(gl.FRAGMENT_SHADER, FS));
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return null;
  gl.useProgram(prog);
  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  gl.enable(gl.BLEND);
  gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
  gl.clearColor(0, 0, 0, 0);
  const u = (n: string) => gl.getUniformLocation(prog, n);
  state = { cv, gl, u: { O: u("uO"), T: u("uT"), R: u("uR"), W: u("uW"), L: u("uL"), C: u("uC") }, raf: 0 };
  return state;
}

function drawGl(origin: WaveOrigin, light: boolean, bright: boolean): boolean {
  try {
    const cv = canvas();
    const st = init(cv);
    if (!st) return false;
    const { gl, u } = st;
    const cw = window.innerWidth;
    const ch = window.innerHeight;
    const k = Math.min(1 / 3, Math.sqrt(160000 / Math.max(1, cw * ch)));
    const W = Math.max(2, Math.round(cw * k));
    const H = Math.max(2, Math.round(ch * k));
    if (cv.width !== W || cv.height !== H) {
      cv.width = W;
      cv.height = H;
    }
    gl.viewport(0, 0, W, H);
    const ox = origin.x * k;
    const oy = H - origin.y * k;
    const bw = 170 * k;
    gl.uniform2f(u.O, ox, oy);
    gl.uniform1f(u.W, bw);
    gl.uniform1f(u.L, light ? 1 : 0);
    gl.uniform1f(u.C, bright ? 1.05 : 0.62);
    gl.uniform1f(u.R, Math.hypot(Math.max(ox, W - ox), Math.max(oy, H - oy)) + bw * 2);
    cancelAnimationFrame(st.raf);
    const D = Math.max(1300, Math.min(1900, Math.max(cw, ch) * 1.05));
    cv.style.opacity = "1";
    // starts two frames late: the restyle caused by the action itself (a theme, the menu) is done before the first frame
    let t1 = 0;
    const tick = (now: number) => {
      if (!t1) t1 = now;
      const t = Math.min(1, (now - t1) / D);
      gl.uniform1f(u.T, t);
      gl.clear(gl.COLOR_BUFFER_BIT);
      if (t < 1) {
        gl.drawArrays(gl.TRIANGLES, 0, 3);
        st.raf = requestAnimationFrame(tick);
      } else cv.style.opacity = "0";
    };
    st.raf = requestAnimationFrame(() => (st.raf = requestAnimationFrame(tick)));
    return true;
  } catch {
    return false;
  }
}

function drawCss(origin: WaveOrigin, light: boolean) {
  if (fallback) {
    clearTimeout(fallback.timer);
    fallback.el.remove();
  }
  const el = document.createElement("div");
  el.className = "mg-wave-css";
  el.setAttribute("aria-hidden", "true");
  el.dataset.light = light ? "1" : "";
  el.style.setProperty("--ox", `${Math.round(origin.x)}px`);
  el.style.setProperty("--oy", `${Math.round(origin.y)}px`);
  el.append(document.createElement("i"));
  document.body.append(el);
  fallback = { el, timer: window.setTimeout(() => (el.remove(), (fallback = null)), 2100) };
}

/**
 * Plays the wave from `origin` (originOf(button)). `light` is the theme the page is in after the change; `theme` is true for a
 * theme change, which draws the field as a veil, brighter when the page goes dark (as the board does). Nothing plays with
 * the effects off or with reduced motion.
 */
export function playWave(origin: WaveOrigin, opts: { light: boolean; theme?: boolean }): void {
  if (!effectsEnabled() || prefersReducedMotion() || typeof document === "undefined") return;
  const light = opts.theme ? true : opts.light;
  const bright = !!opts.theme && !opts.light;
  if (!drawGl(origin, light, bright)) drawCss(origin, light);
}
