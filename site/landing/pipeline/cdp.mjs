// cdp.mjs: one headless Chrome driven over the DevTools protocol (no dependencies; Node 22 has WebSocket).
// Chrome is found in CHROME, or at the usual places. One instance at a time; it is stopped by its own PID (the tree of it),
// never by the name of the image.
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, existsSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const CANDIDATES = [process.env.CHROME, 'C:/Program Files/Google/Chrome/Application/chrome.exe', 'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe', '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'].filter(Boolean);
export const chromePath = () => { const p = CANDIDATES.find((x) => existsSync(x)); if (!p) throw new Error('Chrome not found: set CHROME'); return p; };
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function launch({ profile, port = 9440 + Math.floor(Math.random() * 50), args = [] }) {
  rmSync(profile, { recursive: true, force: true }); mkdirSync(profile, { recursive: true });
  const proc = spawn(chromePath(), ['--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, '--no-first-run', '--no-default-browser-check', '--hide-scrollbars', '--force-color-profile=srgb', '--disable-background-networking', '--disable-component-update', '--disable-sync', '--disable-gpu', '--font-render-hinting=none', ...args, 'about:blank'], { stdio: 'ignore' });
  let list = [];
  for (let i = 0; i < 150; i++) { try { list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json(); if (list.some((t) => t.type === 'page')) break; } catch { /* not up yet */ } await sleep(200); }
  const page = list.find((t) => t.type === 'page');
  if (!page) { stop(proc); throw new Error('Chrome did not start'); }
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
  let seq = 0; const pending = new Map(), handlers = new Set();
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) { const { res, rej } = pending.get(msg.id); pending.delete(msg.id); msg.error ? rej(new Error(JSON.stringify(msg.error))) : res(msg.result); }
    else if (msg.method) handlers.forEach((h) => h(msg));
  };
  const send = (method, params = {}) => new Promise((res, rej) => { const i = ++seq; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
  const ev = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error('eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 500));
    return r.result.value;
  };
  const shot = async (file) => { const r = await send('Page.captureScreenshot', { format: 'png' }); writeFileSync(file, Buffer.from(r.data, 'base64')); return file; };
  const close = () => { try { ws.close(); } catch { /* closed */ } stop(proc); };
  return { send, ev, shot, on: (h) => handlers.add(h), off: (h) => handlers.delete(h), close, pid: proc.pid };
}

function stop(proc) {
  try {
    if (process.platform === 'win32') spawnSync('taskkill', ['/PID', String(proc.pid), '/T', '/F'], { stdio: 'ignore' }); // our PID and its children only
    else proc.kill();
  } catch { /* gone */ }
}

/** console, exceptions, failed loads, CSP violations of the page, as strings */
export function watch(chrome) {
  const errors = [], reqs = [];
  chrome.on((m) => {
    const p = m.params;
    if (m.method === 'Runtime.exceptionThrown') errors.push('exception: ' + (p.exceptionDetails.exception?.description || p.exceptionDetails.text).slice(0, 300));
    else if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning', 'assert'].includes(p.type)) errors.push('console.' + p.type + ': ' + p.args.map((a) => a.value ?? a.description ?? '').join(' ').slice(0, 300));
    else if (m.method === 'Log.entryAdded' && ['error', 'warning'].includes(p.entry.level)) errors.push('log.' + p.entry.level + ': ' + p.entry.text + ' ' + (p.entry.url || ''));
    else if (m.method === 'Network.loadingFailed' && !p.canceled) errors.push('net failed: ' + p.errorText + ' ' + (p.blockedReason || ''));
    else if (m.method === 'Network.responseReceived') { reqs.push({ url: p.response.url.replace(/^http:\/\/127\.0\.0\.1:\d+/, ''), status: p.response.status, type: p.type }); if (p.response.status >= 400) errors.push('http ' + p.response.status + ' ' + p.response.url); }
    else if (m.method === 'Audits.issueAdded' && p.issue.code === 'ContentSecurityPolicyIssue') errors.push('csp: ' + JSON.stringify(p.issue.details.contentSecurityPolicyIssueDetails || {}).slice(0, 300));
  });
  return { errors, reqs };
}
