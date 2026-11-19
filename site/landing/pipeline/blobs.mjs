// blobs.mjs: the design refers to images as /_blob/<hash> (flags of the server lists, the accent-coloured logos, the support
// avatar), and the export does not carry them. Each one is replaced by a stand-in drawn here, so the page has no broken
// image; the owner can drop the real files in later (the report names every stand-in).
import { createHash } from 'node:crypto';

const FLAG_W = 30, FLAG_H = 20;
const hs = (cols, w = cols.map(() => 1)) => { const t = w.reduce((a, b) => a + b, 0); let y = 0; return cols.map((c, i) => { const h = FLAG_H * w[i] / t, r = `<rect y="${y.toFixed(2)}" width="${FLAG_W}" height="${h.toFixed(2)}" fill="${c}"/>`; y += h; return r; }).join(''); };
const vs = (cols) => cols.map((c, i) => `<rect x="${i * FLAG_W / cols.length}" width="${FLAG_W / cols.length}" height="${FLAG_H}" fill="${c}"/>`).join('');
const cross = (bg, fg, x = 10) => `<rect width="${FLAG_W}" height="${FLAG_H}" fill="${bg}"/><rect x="${x - 2}" width="4" height="${FLAG_H}" fill="${fg}"/><rect y="8" width="${FLAG_W}" height="4" fill="${fg}"/>`;
export const FLAGS = {
  de: hs(['#000', '#d00', '#ffce00']), nl: hs(['#ae1c28', '#fff', '#21468b']), ru: hs(['#fff', '#0039a6', '#d52b1e']), pl: hs(['#fff', '#dc143c']),
  ro: vs(['#002b7f', '#fcd116', '#ce1126']), fr: vs(['#0055a4', '#fff', '#ef4135']), lv: hs(['#9e3039', '#fff', '#9e3039'], [2, 1, 2]),
  ee: hs(['#0072ce', '#000', '#fff']), by: hs(['#cf101a', '#007c30'], [2, 1]), ir: hs(['#239f40', '#fff', '#da0000']),
  fi: cross('#fff', '#003580'), se: cross('#006aa7', '#fecc00'),
  cn: `<rect width="${FLAG_W}" height="${FLAG_H}" fill="#de2910"/><path d="M6 3l1.2 3.6h3.8l-3 2.2 1.1 3.6L6 10.2 2.9 12.4 4 8.8 1 6.6h3.8z" fill="#ffde00"/>`,
};
const svg = (inner, vb) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${vb}">${inner}</svg>`;
export const flagSvg = (cc) => svg(FLAGS[cc] || `<rect width="${FLAG_W}" height="${FLAG_H}" fill="#6b6b73"/>`, `0 0 ${FLAG_W} ${FLAG_H}`);

/** hash -> {kind: flag|logo|brand|avatar, cc?} from the places that name them */
export function scanBlobs(sources) {
  const info = new Map(), set = (h, v) => { if (!info.has(h)) info.set(h, v); };
  for (const src of Object.values(sources)) {
    for (const m of src.matchAll(/["']([a-z]{2})["']\s*:\s*["']\/_blob\/([0-9a-f]{32})/g)) set(m[2], { kind: 'flag', cc: m[1] });
    for (const m of src.matchAll(/["']([a-z]{2})["']\s*:\s*\{[^{}]*?"flag"\s*:\s*"\/_blob\/([0-9a-f]{32})"/g)) set(m[2], { kind: 'flag', cc: m[1] });
    for (const m of src.matchAll(/\b(lavender|mint|sand|rose)\s*:\s*['"]\/_blob\/([0-9a-f]{32})/g)) set(m[2], { kind: 'logo', color: m[1] });
    for (const m of src.matchAll(/class="brand"[^>]*><img src="\/_blob\/([0-9a-f]{32})"/g)) set(m[1], { kind: 'brand' });
    for (const m of src.matchAll(/class="u-ava" src="\/_blob\/([0-9a-f]{32})"/g)) set(m[1], { kind: 'avatar' });
    for (const m of src.matchAll(/src="\/_blob\/([0-9a-f]{32})" alt="Flag of ([A-Za-z ]+)"/g)) set(m[1], { kind: 'flag', cc: ({ Finland: 'fi', Netherlands: 'nl', Germany: 'de' })[m[2]] || null });
    for (const m of src.matchAll(/<img src="\/_blob\/([0-9a-f]{32})" alt="">/g)) set(m[1], { kind: 'brand' }); // the logo in the brand links and the comparison
  }
  return info;
}

/** the stand-in file of a blob: {file, body, standIn}. logoSvg: the brand mark (the badge the Pn boards embed) */
export function blobAsset(hash, info, logoSvg) {
  const i = info.get(hash) || { kind: 'unknown' };
  let body = i.kind === 'flag' ? flagSvg(i.cc) : i.kind === 'unknown' ? svg('<rect width="20" height="20" fill="#6b6b73"/>', '0 0 20 20') : logoSvg;
  body = Buffer.from(body);
  return { file: 'img-' + createHash('sha1').update(body).digest('hex').slice(0, 10) + '.svg', body, kind: i.kind, cc: i.cc || null, standIn: i.kind !== 'brand' };
}
