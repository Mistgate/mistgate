import { test } from 'node:test';
import assert from 'node:assert/strict';
import { closure, scan } from '../sync.mjs';

test('the subset is the closure of the imports of the two pages', () => {
  const src = { Main: '<dc-import name="A"></dc-import><dc-import name="B">', AppPage: '<dc-import name="B">', A: '<dc-import name="C">', B: '', C: '', Unused: '<dc-import name="Z">' };
  assert.deepEqual(closure((n) => src[n]), ['A', 'AppPage', 'B', 'C', 'Main']);
});

test('the privacy scan: real-looking addresses, e-mails and local paths are findings, documentation addresses are not', () => {
  const files = {
    'a.dc.html': 'de1 203.0.113.7 198.51.100.2 10.0.0.1 192.168.1.2 AmneziaVPN 5.0.1.5 https://example.com/x mail@example.org',
    'b.dc.html': 'ssh 95.216.1.2 and anna@gmail.com in C:\\Users\\x\\code or /home/x/y and https://t.me/proxy https://some-vpn-provider.net/a',
  };
  const f = scan(Object.keys(files), (n) => files[n]);
  const kinds = f.map((x) => x.kind + ':' + x.text);
  assert.ok(kinds.includes('ip:95.216.1.2')); assert.ok(kinds.includes('email:anna@gmail.com')); assert.ok(kinds.some((k) => k.startsWith('path:C:')));
  assert.ok(kinds.includes('review-host:some-vpn-provider.net'));
  for (const ok of ['203.0.113.7', '198.51.100.2', '10.0.0.1', '192.168.1.2', '5.0.1.5', 'example.com', 'mail@example.org', 't.me']) assert.ok(!kinds.some((k) => k.endsWith(':' + ok)), ok + ' is not a finding');
});

test('images of the canvas go through the same scan, and an svg that carries a script or loads from outside is blocking', () => {
  const files = {
    'ok.svg': '<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 3 2"><defs><clipPath id="a"><path d="M0 0h3v2z"/></clipPath></defs><use xlink:href="#a"/><rect fill="url(#a)"/></svg>',
    'script.svg': '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>',
    'handler.svg': '<svg xmlns="http://www.w3.org/2000/svg" onload="x()"></svg>',
    'remote.svg': '<svg xmlns="http://www.w3.org/2000/svg"><image href="https://tracker.example/p.png"/></svg>',
    'css.svg': '<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(https://tracker.example/x)"/></svg>',
    'path.svg': '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L95.216.1.2z"/></svg>',
  };
  const f = scan(Object.keys(files), (n) => files[n]);
  const bad = new Set(f.filter((x) => x.kind === 'unsafe-svg').map((x) => x.file));
  assert.deepEqual([...bad].sort(), ['css.svg', 'handler.svg', 'remote.svg', 'script.svg']);
  assert.ok(!f.some((x) => x.file === 'ok.svg'));
});

test('a blob has a real file: the real file is used and named by its content; the stand-in only when there is none', async () => {
  const { blobAsset, loadBlobs } = await import('../blobs.mjs');
  const real = new Map([['a'.repeat(32), { body: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"/>'), ext: 'svg' }], ['b'.repeat(32), { body: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"/>'), ext: 'svg' }]]);
  const info = new Map([['a'.repeat(32), { kind: 'flag', cc: 'de' }], ['c'.repeat(32), { kind: 'flag', cc: 'de' }]]);
  const a = blobAsset('a'.repeat(32), info, '', real), b = blobAsset('b'.repeat(32), info, '', real), c = blobAsset('c'.repeat(32), info, '', real);
  assert.equal(a.standIn, false); assert.equal(a.file, b.file, 'two hashes of one picture are one file');
  assert.equal(c.standIn, true); assert.notEqual(c.file, a.file);
  assert.equal(loadBlobs('Z:/no/such/folder').size, 0);
});
