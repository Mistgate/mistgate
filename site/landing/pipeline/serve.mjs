// serve.mjs: the built pages over HTTP with the headers of the site (CSP: script-src 'self', no inline script).
//   node serve.mjs [dir] [port]      the stand of `pnpm stand`: / is the page of every language as the generator will place it
// Pages are fragments: the stand wraps them in the document the generator writes (head from manifest.json), so what is
// served is what ships. Pages: /<lang>/ and /<lang>/app/ like the real site (en has no prefix).
import http from 'node:http';
import { readFileSync, existsSync, statSync } from 'node:fs';
import { join, extname, resolve } from 'node:path';

export const CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.png': 'image/png' };
const esc = (s) => String(s ?? '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');

/** the document of a page as the generator writes it: the contract's head from the manifest around the fragment */
export function renderDocument(manifest, page, lang, fragment, { site = 'http://127.0.0.1' } = {}) {
  const m = manifest.pages[page][lang];
  const prefix = lang === 'en' ? '' : '/' + lang;
  const url = (l) => (l === 'en' ? '' : '/' + l) + (page === 'app' ? '/app/' : '/');
  const alts = manifest.langs.map((l) => `<link rel="alternate" hreflang="${l === 'zh' ? 'zh-Hans' : l}" href="${site}${url(l).replace('//', '/')}">`).join('\n');
  return `<!doctype html>
<html lang="${lang === 'zh' ? 'zh-CN' : lang}" dir="${m.dir}"${Object.entries(m.htmlAttrs || {}).map(([k, v]) => ` ${k}="${esc(v)}"`).join('')}>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(m.title)}</title>
<meta name="description" content="${esc(m.description)}">
<link rel="canonical" href="${site}${(prefix + (page === 'app' ? '/app/' : '/')).replace('//', '/')}">
${alts}
<meta name="theme-color" content="#0c0c0e">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
${(m.fonts || []).map((f) => `<link rel="preload" href="/${f}" as="font" type="font/woff2" crossorigin>`).join('\n')}
${m.css.map((c) => `<link rel="stylesheet" href="/${c}">`).join('\n')}
${(m.fetch || []).map((f) => `<link rel="preload" href="/${f}" as="fetch" crossorigin>`).join('\n')}
${(m.preload || []).map((p) => `<link rel="modulepreload" href="/${p}">`).join('\n')}
</head>
<body>
${fragment}
${m.modules.map((s) => `<script type="module" src="/${s}"></script>`).join('\n')}
</body>
</html>
`;
}

export function serve(dir, port = 0) {
  const root = resolve(dir), manifest = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'));
  const hits = [];
  const srv = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x'), path = decodeURIComponent(u.pathname);
    hits.push(path);
    const headers = { 'content-security-policy': CSP, 'x-content-type-options': 'nosniff', 'cache-control': 'no-store' };
    if (/^\/favicon\.(ico|svg)$/.test(path)) { res.writeHead(204, headers); return res.end(); } // the favicon belongs to the site, not to the pipeline
    const m = /^\/(?:(ru|zh|fa|tr)\/)?(app\/)?$/.exec(path);
    if (m) {
      const lang = m[1] || 'en', page = m[2] ? 'app' : 'main', body = manifest.pages[page]?.[lang]?.body;
      if (body && existsSync(join(root, body))) { res.writeHead(200, { ...headers, 'content-type': MIME['.html'] }); return res.end(renderDocument(manifest, page, lang, readFileSync(join(root, body), 'utf8'))); }
    }
    const p = resolve(join(root, path));
    if (!p.startsWith(root) || !existsSync(p) || !statSync(p).isFile()) { res.writeHead(404, headers); return res.end('not found'); }
    res.writeHead(200, { ...headers, 'content-type': MIME[extname(p)] || 'application/octet-stream' });
    res.end(readFileSync(p));
  });
  return new Promise((ok) => srv.listen(port, '127.0.0.1', () => ok({ srv, port: srv.address().port, hits, close: () => srv.close() })));
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].replace(/\\/g, '/').split('/').pop()) && /serve\.mjs$/.test(process.argv[1])) {
  const dir = process.argv[2] || new URL('../build', import.meta.url).pathname.replace(/^\/(\w:)/, '$1');
  const s = await serve(dir, +(process.argv[3] || 8790));
  console.log(`stand: http://127.0.0.1:${s.port}/   (/ru/  /zh/  /fa/  /tr/  /app/  /ru/app/ ...)   Ctrl+C to stop`);
}
