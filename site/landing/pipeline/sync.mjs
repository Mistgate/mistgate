// sync.mjs: copy the part of a design export the landing and the app page need into ../design, after a privacy scan.
//   node sync.mjs <design export folder>          (or MG_DESIGN_SRC=<folder>)
//   node sync.mjs <folder> --check                scan only, copy nothing
//   node sync.mjs <folder> --allow                copy even if the scan found something (the findings are still printed)
// The subset is: Main and AppPage, every board they import (recursively), the six stylesheets and the dictionary.
// The repository is public: the scan refuses to copy files that name the private instance (the pattern is read from the
// repository's own guard hook, it is not written down here), real-looking addresses, e-mails or local paths.
import { readFileSync, writeFileSync, mkdirSync, copyFileSync, existsSync, readdirSync, rmSync } from 'node:fs';
import { join, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';

export const HERE = dirname(fileURLToPath(import.meta.url));
export const DESIGN = join(HERE, '..', 'design');
export const ROOTS = ['Main', 'AppPage'];
export const STATIC = ['land.css', 'pn.css', 'sub-s.css', 'app-s.css', 'app-zh-s.css', 'app-fa-s.css', 'land.i18n.js'];

/** boards imported (recursively) from the roots, by `<dc-import name="X">` */
export function closure(readBoard, roots = ROOTS) {
  const seen = new Set(), todo = [...roots];
  while (todo.length) {
    const n = todo.pop();
    if (seen.has(n)) continue;
    seen.add(n);
    for (const m of readBoard(n).matchAll(/<dc-import\s+name="([A-Za-z0-9_]+)"/g)) todo.push(m[1]);
  }
  return [...seen].sort();
}

/** the guard hook's pattern, if this checkout has the hook (it lives in .git/hooks, never in the tree) */
function guardPattern() {
  if (process.env.MG_GUARD_PATTERN) return new RegExp(process.env.MG_GUARD_PATTERN, 'i');
  try {
    const gd = execFileSync('git', ['rev-parse', '--git-common-dir'], { cwd: HERE, encoding: 'utf8' }).trim();
    const hook = readFileSync(join(gd.startsWith('/') || /^[A-Za-z]:/.test(gd) ? gd : join(HERE, gd), 'hooks', 'pre-commit'), 'utf8');
    const m = /^pat='(.*)'$/m.exec(hook);
    if (m) return new RegExp(m[1], 'i');
  } catch { /* no hook here */ }
  return null;
}

const DOC_NETS = [/^203\.0\.113\./, /^198\.51\.100\./, /^192\.0\.2\./, /^10\./, /^127\./, /^0\.0\.0\.0$/, /^192\.168\./, /^172\.(1[6-9]|2\d|3[01])\./, /^255\./, /^169\.254\./];
const OK_HOST = /(^|\.)(example\.(com|org|net)|mistgate\.app|github\.com|w3\.org|fonts\.googleapis\.com|fonts\.gstatic\.com|localhost)$/;
// not addresses: a client version (AmneziaVPN 5.0.1.5) and well-known public resolvers
const PUBLIC_TOOLS = /^(5\.0\.1\.5|1\.1\.1\.1|8\.8\.8\.8)$/;
const OK_HOST_EXTRA = /^t\.me$/; // the public Telegram link of the user page

/** findings: {file, kind, text}. "review" findings need a human look but do not stop the copy */
export function scan(files, readFile = (f) => readFileSync(f, 'utf8')) {
  const guard = guardPattern(), out = [];
  for (const f of files) {
    const name = basename(f), s = readFile(f), isMarkup = /\.dc\.html$/.test(name);
    if (guard) for (const m of s.matchAll(new RegExp(guard.source, 'gi'))) out.push({ file: name, kind: 'private-name', text: s.slice(Math.max(0, m.index - 30), m.index + m[0].length + 30).replace(/\s+/g, ' ') });
    for (const m of s.matchAll(/\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b/g)) {
      const ip = m[1];
      if (DOC_NETS.some((r) => r.test(ip)) || PUBLIC_TOOLS.test(ip) || ip.split('.').some((x) => +x > 255)) continue;
      if (/\d\.\d+\.\d+\.\d+\.\d/.test(s.slice(m.index - 1, m.index + ip.length + 2))) continue; // part of a longer dotted number
      if (/^[0-9]\.[0-9]+\.[0-9]+\.[0-9]+$/.test(ip) && /(version|v|ver)[ :]*$/i.test(s.slice(Math.max(0, m.index - 10), m.index))) continue;
      out.push({ file: name, kind: 'ip', text: ip });
    }
    for (const m of s.matchAll(/[\w.+-]+@([\w-]+\.)+[a-z]{2,}/gi)) if (!OK_HOST.test(m[0].split('@')[1].toLowerCase())) out.push({ file: name, kind: 'email', text: m[0] });
    for (const m of s.matchAll(/(?:[A-Za-z]:[\\/](?:Users|projects|Documents)[^\s"'<]*|\/(?:home|Users)\/[A-Za-z0-9_.-]+[^\s"'<]*)/g)) out.push({ file: name, kind: 'path', text: m[0] });
    if (isMarkup) for (const m of s.matchAll(/https?:\/\/([a-z0-9.-]+\.[a-z]{2,})/gi)) {
      const h = m[1].toLowerCase();
      if (!OK_HOST.test(h) && !OK_HOST_EXTRA.test(h)) out.push({ file: name, kind: 'review-host', text: h });
    }
  }
  const seen = new Set();
  return out.filter((x) => { const k = x.file + x.kind + x.text; if (seen.has(k)) return false; seen.add(k); return true; });
}

export function sync(src, { check = false, allow = false, log = console.log } = {}) {
  if (!src || !existsSync(src)) throw new Error('design export folder not found: ' + src);
  const board = (n) => { const p = join(src, n + '.dc.html'); if (!existsSync(p)) throw new Error('a board is imported but missing in the export: ' + n); return readFileSync(p, 'utf8'); };
  const boards = closure(board), files = [...boards.map((b) => b + '.dc.html'), ...STATIC];
  for (const f of STATIC) if (!existsSync(join(src, f))) throw new Error('missing in the export: ' + f);
  const findings = scan(files.map((f) => join(src, f)));
  const hard = findings.filter((x) => !x.kind.startsWith('review'));
  for (const x of findings) log(`${x.kind.padEnd(12)} ${x.file}: ${x.text}`);
  log(`${boards.length} boards, ${files.length} files, ${findings.length} findings (${hard.length} blocking)`);
  if (check) return { boards, files, findings };
  if (hard.length && !allow) throw new Error('privacy scan failed, nothing copied (fix the design, or pass --allow after a human review)');
  mkdirSync(DESIGN, { recursive: true });
  for (const f of readdirSync(DESIGN)) if (/\.(dc\.html|css|js)$/.test(f) && !files.includes(f)) rmSync(join(DESIGN, f)); // boards that dropped out
  for (const f of files) copyFileSync(join(src, f), join(DESIGN, f));
  // a file the guard pattern hits stays out of git until the design renames the name: git is told so here
  const held = [...new Set(findings.filter((x) => x.kind === 'private-name').map((x) => x.file))].sort();
  writeFileSync(join(DESIGN, '.gitignore'), held.length ? '# held back by the privacy scan (sync.mjs): the design has to change a demo name first\n' + held.join('\n') + '\n' : '');
  log('copied to ' + DESIGN + (held.length ? ' (held back from git: ' + held.join(', ') + ')' : ''));
  return { boards, files, findings };
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const a = process.argv.slice(2), src = a.find((x) => !x.startsWith('--')) || process.env.MG_DESIGN_SRC;
  if (!src) { console.error('usage: node sync.mjs <design export folder> [--check] [--allow]   (or MG_DESIGN_SRC)'); process.exit(1); }
  try { sync(src, { check: a.includes('--check'), allow: a.includes('--allow') }); } catch (e) { console.error(e.message); process.exit(2); }
}
