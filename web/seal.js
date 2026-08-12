// Sealing for the otosu browser client.
//
// This module is the single source of truth for the wire format, shared by the
// page and by the interop test that checks Go can open what this produces.
//
// It must stay byte compatible with internal/wire in Go:
//
//   blob      = nonce[12] || AES-256-GCM(plaintext)
//   plaintext = headerLen[2, big endian] || headerJSON || fileBytes

export const NONCE_BYTES = 12;
export const KEY_BYTES = 32;
export const MAX_HEADER = 8 << 10;

/** Encode bytes as unpadded base64url, the form used for the key in a reference. */
export function b64url(bytes) {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Generate a fresh AES-256 key. */
export function newKey() {
  return crypto.getRandomValues(new Uint8Array(KEY_BYTES));
}

/**
 * Seal a file into a blob the server stores but cannot read.
 *
 * @param {Uint8Array} key   32 byte AES key
 * @param {string} name      file name, carried inside the ciphertext
 * @param {string} mime      MIME type, may be empty
 * @param {ArrayBuffer} data file contents
 * @returns {Promise<Uint8Array>}
 */
export async function seal(key, name, mime, data) {
  const header = new TextEncoder().encode(
    JSON.stringify({ n: name, m: mime || '', s: data.byteLength })
  );
  if (header.length > MAX_HEADER) throw new Error('file name too long');

  const plain = new Uint8Array(2 + header.length + data.byteLength);
  new DataView(plain.buffer).setUint16(0, header.length, false); // big endian
  plain.set(header, 2);
  plain.set(new Uint8Array(data), 2 + header.length);

  const nonce = crypto.getRandomValues(new Uint8Array(NONCE_BYTES));
  const ck = await crypto.subtle.importKey('raw', key, 'AES-GCM', false, ['encrypt']);
  const ct = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv: nonce, tagLength: 128 }, ck, plain
  );

  const blob = new Uint8Array(NONCE_BYTES + ct.byteLength);
  blob.set(nonce, 0);
  blob.set(new Uint8Array(ct), NONCE_BYTES);
  return blob;
}
