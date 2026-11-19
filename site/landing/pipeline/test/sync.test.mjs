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
