// Development only: the made-up library of the Decoy sites scenes (screens/dev-kit-decoys.tsx), the board's own (PnDecoys, PnDecoy):
// a bakery, a photo studio, a joinery, a boat rental, a bike blog, a nursery, a backup with no home page; the nodes de1, nl1, fi1,
// msk1 on documentation addresses. The pages are drawn here as small documents, as the panel's preview would send them.

type Look = {
  brand: string;
  lang: string;
  font: string;
  bg: string;
  fg: string;
  ac: string;
  c: [string, string, string];
  ln: string;
  r: string;
  w: number;
  nav: string[];
  h1: string;
  p: string;
  cta: string;
  p1: string;
  li: [string, string][];
  p2: string;
  nf: string;
};

export const looks: Record<string, Look> = {
  bk: { brand: "Holzofen", lang: "de", font: "Georgia,serif", bg: "#f6efe4", fg: "#2a1d12", ac: "#b4552d", c: ["#e2c9a6", "#c98f5a", "#8c5a3c"], ln: "#2a1d1222", r: "4px", w: 700, nav: ["Brot", "Laden", "Kontakt"], h1: "Brot aus dem <i>Holzofen</i>", p: "Jeden Morgen frisch gebacken. Sauerteig, Roggen und Dinkel aus der Region", cta: "Sortiment ansehen", p1: "Unser Sortiment wechselt mit der Jahreszeit", li: [["Roggenbrot", "750 g"], ["Dinkelkruste", "500 g"], ["Bauernlaib", "1 kg"], ["Laugenbrezel", "Stück"]], p2: "Musterstraße 12, 10115 Berlin. Di–Sa, 7–18 Uhr", nf: "Diese Seite gibt es nicht" },
  ph: { brand: "NORDLICHT", lang: "en", font: "Helvetica,Arial,sans-serif", bg: "#101012", fg: "#f2f2f0", ac: "#e9e3d2", c: ["#2a2a30", "#3b3a44", "#55525f"], ln: "#ffffff22", r: "0", w: 300, nav: ["Work", "Studio", "Contact"], h1: "Portraits in <i>natural light</i>", p: "A small daylight studio for portraits, families and quiet product work", cta: "See the work", p1: "Selected sessions from the last two years", li: [["Portrait session", "60 min"], ["Family session", "90 min"], ["Product day", "full day"]], p2: "Studio visits by appointment. Write to hello@example.com", nf: "This page does not exist" },
  wd: { brand: "Werkplaats Eik", lang: "nl", font: '"Trebuchet MS",Verdana,sans-serif', bg: "#eef0e8", fg: "#1d2a22", ac: "#2f6b4a", c: ["#c9d3bd", "#9db39a", "#6f8f78"], ln: "#1d2a2222", r: "14px", w: 700, nav: ["Meubels", "Werkwijze", "Contact"], h1: "Meubels van <i>massief eiken</i>", p: "Tafels, kasten en banken op maat, gemaakt in onze eigen werkplaats", cta: "Bekijk het werk", p1: "Elk stuk maken we op bestelling", li: [["Eettafel", "op maat"], ["Boekenkast", "op maat"], ["Tuinbank", "180 cm"]], p2: "Voorbeeldstraat 8, Utrecht. Op afspraak", nf: "Deze pagina bestaat niet" },
  bike: { brand: "alpine·loop", lang: "en", font: '"Courier New",monospace', bg: "#fbfbf7", fg: "#1b1b1b", ac: "#d2452f", c: ["#e7e5da", "#cfd8d2", "#b9c7d6"], ln: "#1b1b1b22", r: "2px", w: 700, nav: ["Routes", "Gear", "About"], h1: "Slow rides across <i>the Alps</i>", p: "Notes, maps and packing lists from a few summers of bikepacking", cta: "Read the routes", p1: "Routes I have ridden and would ride again", li: [["Lake loop", "3 days"], ["High pass", "5 days"], ["Valley floor", "2 days"]], p2: "A personal blog. No comments, no tracking", nf: "Wrong turn. Nothing here" },
  gd: { brand: "Gärtnerei Lindenhof", lang: "de", font: "Georgia,serif", bg: "#f3f6ee", fg: "#22301c", ac: "#5c7d2e", c: ["#d6e2c2", "#b4cc97", "#8cab6a"], ln: "#22301c22", r: "999px", w: 400, nav: ["Pflanzen", "Anfahrt"], h1: "Stauden und <i>Gehölze</i>", p: "Eine kleine Gärtnerei mit eigener Anzucht", cta: "Zum Sortiment", p1: "Aus eigener Anzucht", li: [["Stauden", "ab April"], ["Obstgehölze", "ab Oktober"]], p2: "Lindenweg 3. Mo–Sa", nf: "Seite nicht gefunden" },
  boat: { brand: "Järvi Vene", lang: "fi", font: "Verdana,Arial,sans-serif", bg: "#eaf2f6", fg: "#12303f", ac: "#1f6f93", c: ["#bcd7e3", "#8fbdd0", "#5d9cb8"], ln: "#12303f22", r: "10px", w: 700, nav: ["Veneet", "Hinnat", "Yhteys"], h1: "Soutuveneet ja <i>kanootit</i>", p: "Vuokraa vene päiväksi tai viikoksi suoraan laiturilta", cta: "Katso veneet", p1: "Kaikki veneet sisältävät airot ja liivit", li: [["Soutuvene", "päivä"], ["Kanootti", "päivä"], ["Kajakki", "päivä"]], p2: "Esimerkkitie 4, Tampere. Touko–syyskuu", nf: "Sivua ei löytynyt" },
};
// a generic look for the sites a scene makes up to fill the library
const filler = (n: number): Look => ({ ...looks.bike!, brand: `Site ${n}`, bg: `hsl(${(n * 47) % 360} 30% 94%)`, ac: `hsl(${(n * 47) % 360} 45% 38%)`, h1: `Page <i>${n}</i>`, nav: ["One", "Two", "Three"] });

const NOT_FOUND = '<!doctype html><html><meta charset="utf-8"><title>404</title><body style="margin:0;display:grid;place-content:center;min-height:100vh;font:16px/1.5 system-ui,sans-serif;color:#444;background:#fff;text-align:center"><b style="font-size:48px;line-height:1">404</b><span>Not Found</span></body></html>';

/** One page of a made-up site as a single document: 0 the home page, 1 the first inner page, 2 the last, 404 the site's own 404.html. */
export function pageDoc(id: string, page: number | "nf", n = 0): string {
  if (page === "nf") return NOT_FOUND;
  const o = looks[id] ?? filler(n);
  const css = `*{box-sizing:border-box}body{margin:0;font:400 16px/1.5 ${o.font};background:${o.bg};color:${o.fg}}a{color:inherit;text-decoration:none}header{display:flex;flex-wrap:wrap;gap:12px 28px;justify-content:space-between;align-items:center;padding:24px 6vw}header b{font-size:20px;font-weight:${o.w}}nav{display:flex;flex-wrap:wrap;gap:8px 24px;font-size:15px;opacity:.75}main{padding:4vw 6vw 8vw}h1{margin:0 0 20px;font-size:clamp(34px,6.4vw,88px);line-height:1.02;letter-spacing:-.02em;font-weight:${o.w};max-width:13ch}h1 i{font-style:normal;color:${o.ac}}p{margin:0;max-width:46ch;font-size:clamp(16px,1.5vw,20px);opacity:.78}.b{display:inline-block;margin-top:28px;padding:14px 22px;border-radius:${o.r};background:${o.ac};color:${o.bg};font-weight:600}.g{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:16px;margin-top:6vw}.g div{aspect-ratio:4/3;border-radius:${o.r};background:${o.c[0]}}.g div:nth-child(2){background:${o.c[1]}}.g div:nth-child(3){background:${o.c[2]}}ul{list-style:none;margin:5vw 0 0;padding:0;max-width:640px}li{display:flex;justify-content:space-between;gap:16px;padding:14px 0;border-top:1px solid ${o.ln}}.e{min-height:60vh;display:grid;place-content:center;justify-items:center;text-align:center;gap:8px}.e b{font-size:clamp(72px,16vw,200px);line-height:1;color:${o.ac}}`;
  const head = `<header><b>${o.brand}</b><nav>${o.nav.map((x) => `<a href="#">${x}</a>`).join("")}</nav></header>`;
  const body =
    page === 404
      ? `<div class="e"><b>404</b><p>${o.nf}</p></div>`
      : page === 1
        ? `<h1>${o.nav[0]}</h1><p>${o.p1}</p><ul>${o.li.map((x) => `<li><span>${x[0]}</span><span>${x[1]}</span></li>`).join("")}</ul>`
        : page === 2
          ? `<h1>${o.nav[o.nav.length - 1]}</h1><p>${o.p2}</p>`
          : `<h1>${o.h1}</h1><p>${o.p}</p><a class="b" href="#">${o.cta}</a><div class="g"><div></div><div></div><div></div></div>`;
  return `<!doctype html><html lang="${o.lang}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${o.brand}</title><style>${css}</style>${head}<main>${body}</main></html>`;
}

export type DemoNode = { id: string; name: string; cc: string; address: string };
export const demoNodes: DemoNode[] = [
  { id: "nod_de1", name: "de1", cc: "DE", address: "de1.example.com" },
  { id: "nod_nl1", name: "nl1", cc: "NL", address: "nl1.example.net" },
  { id: "nod_fi1", name: "fi1", cc: "FI", address: "fi1.example.org" },
  { id: "nod_msk1", name: "msk1", cc: "RU", address: "msk1.example.com" },
];

export type DemoSite = {
  id: string;
  name: string;
  note: string;
  source: "upload" | "assistant" | "mcp";
  token?: string;
  state: "ready" | "draft";
  /** A required check that fails (the board's "no index.html"). */
  bad?: boolean;
  files: number;
  bytes: number;
  sha: string;
  nodes: string[];
  panel: boolean;
  updatedAgoS: number;
  createdAgoS: number;
  by: string;
  has404: boolean;
  /** The warning codes that fired, with the files that fired them. */
  warn: { code: string; paths?: string[] }[];
  tree: [string, number][];
  versions: { agoS: number; by: string; bytes: number }[];
  title: string;
};

const KB = 1024;
const H = 3600;
const D = 86_400;
export const demoSites: DemoSite[] = [
  { id: "dsi_bk", name: "Пекарня Хольцофен", note: "пекарня, немецкий", source: "upload", state: "ready", files: 14, bytes: 412 * KB, sha: "9f2c41a7d0b3", nodes: ["nod_nl1", "nod_msk1"], panel: false, updatedAgoS: 2 * D, createdAgoS: 28 * D, by: "owner", has404: true, title: "Holzofen",
    warn: [{ code: "external_urls", paths: ["css/style.css"] }],
    tree: [["index.html", 6.2], ["brot.html", 4.8], ["kontakt.html", 3.1], ["404.html", 1.2], ["robots.txt", 0.1], ["favicon.svg", 0.9], ["css/style.css", 5.4], ["css/print.css", 0.8], ["img/ofen.jpg", 118], ["img/laden.jpg", 74], ["img/brot-1.jpg", 62], ["img/brot-2.jpg", 58], ["img/team.jpg", 71], ["img/logo.svg", 6.5]],
    versions: [{ agoS: 2 * D, by: "owner", bytes: 412 * KB }, { agoS: 8 * D, by: "owner", bytes: 398 * KB }, { agoS: 28 * D, by: "owner", bytes: 371 * KB }] },
  { id: "dsi_ph", name: "Фотостудия Нордлихт", note: "фотостудия, английский", source: "assistant", state: "ready", files: 5, bytes: 18 * KB, sha: "3b80e5d1c492", nodes: ["nod_fi1"], panel: false, updatedAgoS: 1 * D, createdAgoS: 5 * D, by: "assistant", has404: false, title: "NORDLICHT",
    warn: [{ code: "no_404" }],
    tree: [["index.html", 5.1], ["work.html", 4.4], ["contact.html", 2.9], ["style.css", 4.9], ["favicon.svg", 0.7]],
    versions: [{ agoS: 1 * D, by: "assistant", bytes: 18 * KB }, { agoS: 5 * D, by: "assistant", bytes: 16 * KB }] },
  { id: "dsi_wd", name: "Столярная мастерская", note: "мебель на заказ, нидерландский", source: "upload", state: "ready", files: 22, bytes: 1340 * KB, sha: "c47a09be5512", nodes: [], panel: true, updatedAgoS: 14 * D, createdAgoS: 14 * D, by: "owner", has404: true, title: "Werkplaats Eik",
    warn: [],
    tree: [["index.html", 7.4], ["meubels.html", 6.0], ["contact.html", 2.6], ["404.html", 1.0], ["css/site.css", 9.0], ...Array.from({ length: 18 }, (_, i) => [`img/photo-${i + 1}.jpg`, 60 + i] as [string, number])],
    versions: [{ agoS: 14 * D, by: "owner", bytes: 1340 * KB }] },
  { id: "dsi_boat", name: "Прокат лодок", note: "прокат, финский", source: "mcp", token: "claude-desktop", state: "ready", files: 7, bytes: 64 * KB, sha: "71d2f6aa8e07", nodes: [], panel: false, updatedAgoS: 5 * D, createdAgoS: 6 * D, by: "claude-desktop", has404: true, title: "Järvi Vene",
    warn: [],
    tree: [["index.html", 5.6], ["veneet.html", 4.9], ["yhteys.html", 2.4], ["404.html", 1.1], ["style.css", 6.2], ["img/laituri.jpg", 41], ["img/logo.svg", 2.8]],
    versions: [{ agoS: 5 * D, by: "claude-desktop", bytes: 64 * KB }, { agoS: 6 * D, by: "claude-desktop", bytes: 58 * KB }] },
  { id: "dsi_bike", name: "Велопоходы по Альпам", note: "личный блог, английский", source: "mcp", token: "claude-desktop", state: "draft", files: 9, bytes: 46 * KB, sha: "e05c77130a9d", nodes: [], panel: false, updatedAgoS: 3 * H, createdAgoS: 3 * H + 480, by: "claude-desktop", has404: true, title: "alpine·loop",
    warn: [{ code: "scripts_or_forms", paths: ["map.js"] }],
    tree: [["index.html", 6.8], ["routes.html", 7.9], ["gear.html", 5.2], ["about.html", 2.2], ["404.html", 0.9], ["style.css", 4.6], ["map.js", 3.4], ["img/pass.jpg", 12], ["img/tent.jpg", 3]],
    versions: [{ agoS: 3 * H, by: "claude-desktop", bytes: 46 * KB }, { agoS: 3 * H + 480, by: "claude-desktop", bytes: 41 * KB }] },
  { id: "dsi_gd", name: "Садовый питомник", note: "питомник растений, немецкий", source: "assistant", state: "draft", files: 3, bytes: 11 * KB, sha: "5a9e12c0b7f4", nodes: [], panel: false, updatedAgoS: 5 * H, createdAgoS: 5 * H, by: "assistant", has404: false, title: "Gärtnerei Lindenhof",
    warn: [{ code: "no_404" }, { code: "few_pages" }],
    tree: [["index.html", 4.7], ["pflanzen.html", 3.6], ["style.css", 2.7]],
    versions: [{ agoS: 5 * H, by: "assistant", bytes: 11 * KB }] },
  { id: "dsi_arc", name: "site-backup", note: "", source: "upload", state: "draft", bad: true, files: 31, bytes: 880 * KB, sha: "0d44b9f2c611", nodes: [], panel: false, updatedAgoS: 3 * D, createdAgoS: 3 * D, by: "owner", has404: false, title: "",
    warn: [],
    tree: [["site/index.html", 8.2], ["site/about.html", 5.5], ["site/css/main.css", 12], ...Array.from({ length: 27 }, (_, i) => [`site/img/pic-${i + 1}.jpg`, 20 + i] as [string, number])],
    versions: [{ agoS: 3 * D, by: "owner", bytes: 880 * KB }] },
];

/** Sites made up to fill the library to its limit (the scene "full"). */
export const fillerSites = (from: number, to: number): DemoSite[] =>
  Array.from({ length: to - from + 1 }, (_, i) => {
    const n = from + i;
    return { id: `dsi_f${n}`, name: `Site ${n}`, note: `theme ${n}`, source: "upload" as const, state: "ready" as const, files: 4, bytes: 9 * KB, sha: `f${n}`.padEnd(12, "0"), nodes: [], panel: false, updatedAgoS: n * H, createdAgoS: n * H, by: "owner", has404: true, title: `Site ${n}`, warn: [], tree: [["index.html", 4]] as [string, number][], versions: [{ agoS: n * H, by: "owner", bytes: 9 * KB }] };
  });

/** The text of a file of a made-up site, for the viewer. */
export function fileText(id: string, path: string): string {
  const o = looks[id] ?? looks.bike!;
  const ext = path.split(".").pop() ?? "";
  const base = path.split("/").pop() ?? path;
  if (ext === "html") {
    const home = base === "index.html";
    const h = base === "404.html" ? "404" : home ? o.h1.replace(/<\/?i>/g, "") : o.nav[0];
    const p = base === "404.html" ? o.nf : home ? o.p : o.p1;
    return `<!doctype html>\n<html lang="${o.lang}">\n<head>\n  <meta charset="utf-8">\n  <meta name="viewport" content="width=device-width, initial-scale=1">\n  <title>${o.brand}</title>\n  <link rel="stylesheet" href="style.css">\n</head>\n<body>\n  <header>\n    <b>${o.brand}</b>\n    <nav>${o.nav.map((n) => `<a href="${n.toLowerCase()}.html">${n}</a>`).join(" ")}</nav>\n  </header>\n  <main>\n    <h1>${h}</h1>\n    <p>${p}</p>\n  </main>\n</body>\n</html>`;
  }
  if (ext === "css") return `${path.endsWith("style.css") && id === "bk" ? '@import url("https://fonts.googleapis.com/css2?family=Lora");\n\n' : ""}body {\n  margin: 0;\n  font: 400 16px/1.5 ${o.font};\n  background: ${o.bg};\n  color: ${o.fg};\n}\n\nheader {\n  display: flex;\n  justify-content: space-between;\n  padding: 24px 6vw;\n}\n\nh1 i {\n  font-style: normal;\n  color: ${o.ac};\n}`;
  if (ext === "txt") return "User-agent: *\nAllow: /";
  if (ext === "svg") return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">\n  <rect width="32" height="32" rx="6" fill="${o.ac}"/>\n</svg>`;
  if (ext === "js") return 'const el = document.querySelector(".map");\nel.addEventListener("click", () => {\n  el.classList.toggle("open");\n});';
  return "";
}
