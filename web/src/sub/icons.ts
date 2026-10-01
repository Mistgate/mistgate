// The few line icons of the page, drawn as inline SVG (24x24, stroke = currentColor, no fill), so they follow the
// button's colour in both themes and add no request. Built with createElementNS from fixed path data: no markup parsing.

const ns = "http://www.w3.org/2000/svg";

const paths = {
  download: ["M12 4v11", "m7 11 5 5 5-5", "M5 20h14"],
  copy: ["M9 9h10v11H9z", "M5 15V4h10"],
  check: ["m5 12.5 4.5 4.5L19 7.5"],
  plus: ["M12 5v14", "M5 12h14"],
  key: ["M14.5 9.5a4.5 4.5 0 1 1-1.3-3.2", "m10.5 13.5-6.5 6.5", "m6.5 17.5 2 2", "m8.5 15.5 2 2"],
  external: ["M14 5h5v5", "M19 5l-8 8", "M17 14v4a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V8a1 1 0 0 1 1-1h4"],
  lock: ["M6 11h12v9H6z", "M8.5 11V8a3.5 3.5 0 0 1 7 0v3"],
  shield: ["M12 4 5 7v5c0 4 3 7 7 8 4-1 7-4 7-8V7z"],
  close: ["m6 6 12 12", "m18 6-12 12"],
  link: ["M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1", "M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"],
  phone: ["M8 3h8a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z", "M11 18h2"],
  laptop: ["M5 6a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v9H5z", "M3 18h18"],
  device: ["M6 4h12v16H6z", "M10 17h4"],
  qr: ["M4 4h6v6H4z", "M14 4h6v6h-6z", "M4 14h6v6H4z", "M14 14h2v2h-2z", "M18 18h2v2h-2z", "M18 14h2", "M14 18h2"],
} as const;

export type IconName = keyof typeof paths;

export function icon(name: IconName): SVGSVGElement {
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", "18");
  svg.setAttribute("height", "18");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "2");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("class", "ico");
  for (const d of paths[name]) {
    const p = document.createElementNS(ns, "path");
    p.setAttribute("d", d);
    svg.append(p);
  }
  return svg;
}
