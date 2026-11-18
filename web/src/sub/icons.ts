// The line icons of the page, drawn as inline SVG (24x24 grid, stroke = currentColor, no fill), so they follow the
// button's colour in both themes and add no request. Built with createElementNS from fixed shapes: no markup parsing.

const ns = "http://www.w3.org/2000/svg";

type Shape = string | [tag: "rect" | "circle", attrs: Record<string, number>];
const rect = (x: number, y: number, width: number, height: number, rx: number): Shape => ["rect", { x, y, width, height, rx }];
const circle = (cx: number, cy: number, r: number): Shape => ["circle", { cx, cy, r }];

const paths = {
  download: ["M12 4v11", "m7 10 5 5 5-5", "M5 20h14"],
  external: ["M7 17 17 7M9 7h8v8"],
  plus: ["M12 5v14M5 12h14"],
  check: ["m5 12.5 4.5 4.5L19 7.5"],
  close: ["M6 6l12 12M18 6 6 18"],
  link: ["M10 14a4.5 4.5 0 0 0 6.4 0l3.2-3.2a4.5 4.5 0 0 0-6.4-6.4l-1.1 1.1", "M14 10a4.5 4.5 0 0 0-6.4 0l-3.2 3.2a4.5 4.5 0 0 0 6.4 6.4l1.1-1.1"],
  key: [circle(7.5, 15.5, 4), "m10.4 12.6 9.1-9.1", "m16 6 3 3", "m13.8 8.2 2 2"],
  copy: [rect(8.5, 8.5, 11, 11, 2.6), "M15.5 8.5V7A2.5 2.5 0 0 0 13 4.5H7A2.5 2.5 0 0 0 4.5 7v6A2.5 2.5 0 0 0 7 15.5h1.5"],
  phone: [rect(6.5, 2.5, 11, 19, 3.2), "M10.4 5.6h3.2"],
  android: [rect(6.5, 2.5, 11, 19, 2), "M10 18.6h4"],
  monitor: [rect(3, 4, 18, 12, 2), "M9 20h6M12 16v4"],
  laptop: ["M5 15.5v-9A1.5 1.5 0 0 1 6.5 5h11A1.5 1.5 0 0 1 19 6.5v9", "M2.5 15.5h19l-.9 2.1a1.5 1.5 0 0 1-1.4.9H4.8a1.5 1.5 0 0 1-1.4-.9z"],
  terminal: [rect(3, 4.5, 18, 15, 2.5), "m7.5 10 2.5 2.5-2.5 2.5M12.5 15.5h4"],
  other: [circle(12, 12, 9), "M8 12h.01M12 12h.01M16 12h.01"],
  globe: [circle(12, 12, 9), "M3 12h18", "M12 3c2.4 2.5 3.6 5.5 3.6 9s-1.2 6.5-3.6 9c-2.4-2.5-3.6-5.5-3.6-9S9.6 5.5 12 3z"],
  exit: ["M14 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4", "m10 16 4-4-4-4M14 12H4"],
  refresh: ["M20 11a8 8 0 0 0-14.3-4.3L4 8.5", "M4 4v4.5h4.5", "M4 13a8 8 0 0 0 14.3 4.3L20 15.5", "M20 20v-4.5h-4.5"],
  pencil: ["M4.5 19.5h3.6L19 8.6a2.2 2.2 0 0 0-3.1-3.1L5 16.4z", "m14.5 7 3 3"],
  trash: ["M4 7h16", "M10 11v6M14 11v6", "M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12", "M9 7V4.5h6V7"],
  file: ["M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z", "M14 3v5h5", "M12 11.5v5.5M9.5 14.5 12 17l2.5-2.5"],
  eye: ["M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z", circle(12, 12, 3)],
  power: ["M12 3.5v8", "M6.6 7.2a7.5 7.5 0 1 0 10.8 0"],
  send: ["M20.5 4 3.5 10.6l5.6 2 2 5.9 3-3.6 4.3 3.1z", "m9.1 12.6 6.4-4.4"],
  shield: ["M12 3 5 6v5.5c0 4.4 3 8.2 7 9.5 4-1.3 7-5.1 7-9.5V6z"],
  qr: [rect(4, 4, 6, 6, 1.2), rect(14, 4, 6, 6, 1.2), rect(4, 14, 6, 6, 1.2), "M14 14h2.5v2.5H14zM20 14v.01M14 20h.01M17.5 20H20v-2.5"],
  info: [circle(12, 12, 9), "M12 11v5.5M12 7.6v.01"],
  warn: ["M10.3 4.2 2.8 17.5A2 2 0 0 0 4.5 20.5h15a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0z", "M12 9.5v4M12 17v.01"],
  lock: [rect(5, 10.5, 14, 10, 2.5), "M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"],
  dots: ["M5.5 12h.01M12 12h.01M18.5 12h.01"],
  chev: ["m6 9 6 6 6-6"],
  next: ["m9 6 6 6-6 6"],
  back: ["m15 6-6 6 6 6"],
  clock: [circle(12, 12, 9), "M12 7.5V12l3 2"],
  tunnel: ["M4 20v-9a8 8 0 0 1 16 0v9", "M8.5 20v-8.5a3.5 3.5 0 0 1 7 0V20", "M2.5 20h19"],
  /** the device list of the boards: a computer, a router; the page's announcement; the theme switch (system, light, dark) */
  pc: ["M3.5 6.7A2.2 2.2 0 0 1 5.7 4.5h12.6a2.2 2.2 0 0 1 2.2 2.2v7.1a2.2 2.2 0 0 1-2.2 2.2H5.7a2.2 2.2 0 0 1-2.2-2.2zM9 20h6M12 16v4"],
  router: ["M5 13h14a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2zM7 16.5h.01M11 16.5h.01M12 13V8M8.5 6.5a5 5 0 0 1 7 0"],
  megaphone: ["M4 10v4a1 1 0 0 0 1 1h2l5 4V5L7 9H5a1 1 0 0 0-1 1z", "M16 9a4.2 4.2 0 0 1 0 6M18.6 6.6a8 8 0 0 1 0 10.8"],
  system: [rect(3.5, 4.5, 17, 11.5, 2.2), "M9 20h6M12 16v4"],
  sun: [circle(12, 12, 4), "M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4"],
  moon: ["M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z"],
  chat: ["M4.5 19.5V7a2.5 2.5 0 0 1 2.5-2.5h10A2.5 2.5 0 0 1 19.5 7v7a2.5 2.5 0 0 1-2.5 2.5H8z"],
  /** the device rows of the boards (.u-dv .tl): a phone */
  ph: ["M9.3 2.5h5.4a2.8 2.8 0 0 1 2.8 2.8v13.4a2.8 2.8 0 0 1-2.8 2.8H9.3a2.8 2.8 0 0 1-2.8-2.8V5.3a2.8 2.8 0 0 1 2.8-2.8zM10.5 18h3"],
  // E14-C, the windows (V2DlgMenu, V2DlgDelete): the board's own bin (a lid and a plain bucket), not the older .trash
  bin: ["M4.5 7h15M9.5 7V4.5h5V7M6.5 7l.8 12a1.5 1.5 0 0 0 1.5 1.4h6.4a1.5 1.5 0 0 0 1.5-1.4l.8-12"],
} as const satisfies Record<string, readonly Shape[]>;

export type IconName = keyof typeof paths;

/** The icons that point along the line: mirrored on a right-to-left page (sub.css .flip). */
const flips: ReadonlySet<IconName> = new Set(["next", "back", "external", "send", "exit", "chat"]);

/** The icon at `size` px (the grid is 24; the board draws it at 16 with a 1.5 stroke, sub.css .i). */
export function icon(name: IconName, size = 16): SVGSVGElement {
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.5");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("class", flips.has(name) ? "i flip" : "i");
  for (const s of paths[name] as readonly Shape[]) {
    const el = document.createElementNS(ns, typeof s === "string" ? "path" : s[0]);
    if (typeof s === "string") el.setAttribute("d", s);
    else for (const [k, v] of Object.entries(s[1])) el.setAttribute(k, String(v));
    svg.append(el);
  }
  return svg;
}
