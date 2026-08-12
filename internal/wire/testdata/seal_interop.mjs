// Seals a payload using the exact module the browser page loads, and prints the
// result as JSON so the Go test can verify that wire.Open accepts it.
//
// This is the check that the two independent implementations of the format
// agree. A mismatch here would only otherwise surface in production.

import { seal, newKey, b64url } from '../../../web/seal.js';

// getRandomValues rejects requests over 65536 bytes, so fill larger buffers in
// chunks. A 200 KB case stands in for a real screenshot.
function randomBytes(n) {
  const buf = new Uint8Array(n);
  for (let off = 0; off < n; off += 65536) {
    crypto.getRandomValues(buf.subarray(off, Math.min(off + 65536, n)));
  }
  return buf;
}

const cases = [
  { name: 'shot.png', mime: 'image/png', data: new TextEncoder().encode('\x89PNG\r\n\x1a\n hello') },
  { name: 'empty.bin', mime: '', data: new Uint8Array(0) },
  { name: 'ünïcode 名前.txt', mime: 'text/plain', data: new TextEncoder().encode('日本語のテスト') },
  { name: 'big.bin', mime: 'application/octet-stream', data: randomBytes(200000) },
];

const out = [];
for (const c of cases) {
  const key = newKey();
  const blob = await seal(key, c.name, c.mime, c.data.buffer);
  out.push({
    name: c.name,
    mime: c.mime,
    key: b64url(key),
    blob: Buffer.from(blob).toString('base64'),
    data: Buffer.from(c.data).toString('base64'),
  });
}

process.stdout.write(JSON.stringify(out));
