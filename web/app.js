// otosu browser client.
//
// Everything is encrypted here, before upload. The server receives a sealed
// blob and hands back a short code; the AES key stays in this page and travels
// to your dev box only as the part of the reference after the '#'.
//
// The sealed layout must stay byte compatible with internal/wire in Go:
//
//   blob      = nonce[12] || AES-256-GCM(plaintext)
//   plaintext = headerLen[2, big endian] || headerJSON || fileBytes

import { seal, newKey, b64url } from '/seal.js';

const $ = (id) => document.getElementById(id);
const el = {
  login: $('login'), loginForm: $('login-form'), token: $('token'), loginMsg: $('login-msg'),
  app: $('app'), drop: $('drop'), file: $('file'), status: $('status'), bar: $('bar'),
  drops: $('drops'), install: $('install'), copyInstall: $('copy-install'), logout: $('logout'),
};

// ---------------------------------------------------------------- helpers ---

function humanSize(n) {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

function setStatus(msg, kind) {
  el.status.textContent = msg || '';
  el.status.className = 'note' + (kind ? ' ' + kind : '');
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

// ------------------------------------------------------------------ upload ---

let busy = false;

async function upload(file) {
  const key = newKey();
  const blob = await seal(key, file.name || 'clipboard.png', file.type, await file.arrayBuffer());

  const res = await fetch('/api/drop', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/octet-stream' },
    body: blob,
  });

  if (res.status === 401) {
    showLogin('Session expired. Log in again.');
    throw new Error('not logged in');
  }
  if (!res.ok) {
    const detail = await res.json().catch(() => ({}));
    throw new Error(detail.error || `upload failed (${res.status})`);
  }

  const { code } = await res.json();
  return { ref: `${code}#${b64url(key)}`, name: file.name || 'clipboard.png', size: file.size };
}

async function handleFiles(files) {
  const list = [...files].filter(Boolean);
  if (!list.length || busy) return;

  busy = true;
  el.bar.classList.remove('hidden');
  try {
    for (const f of list) {
      setStatus(`Encrypting and uploading ${f.name || 'clipboard image'}…`);
      const drop = await upload(f);
      addDrop(drop);
      const copied = await copy(drop.ref);
      setStatus(
        copied ? 'Copied. Paste it into your dev box.' : 'Ready. Tap the code to copy it.',
        'ok'
      );
    }
  } catch (err) {
    setStatus(err.message, 'err');
  } finally {
    busy = false;
    el.bar.classList.add('hidden');
  }
}

// -------------------------------------------------------------------- view ---

function addDrop({ ref, name, size }) {
  el.drops.classList.remove('hidden');

  const item = document.createElement('div');
  item.className = 'drop-item';

  const head = document.createElement('div');
  head.className = 'drop-head';
  const nameEl = document.createElement('span');
  nameEl.className = 'drop-name';
  nameEl.textContent = name;
  const meta = document.createElement('span');
  meta.className = 'drop-meta';
  meta.textContent = humanSize(size);
  head.append(nameEl, meta);

  const btn = document.createElement('button');
  btn.className = 'ref';
  btn.textContent = ref;
  btn.title = 'Click to copy';
  btn.addEventListener('click', async () => {
    const label = btn.textContent;
    btn.textContent = (await copy(ref)) ? 'Copied' : 'Press Ctrl+C to copy';
    setTimeout(() => { btn.textContent = label; }, 1200);
  });

  item.append(head, btn);
  el.drops.prepend(item);
}

function showLogin(msg) {
  el.login.classList.remove('hidden');
  el.app.classList.add('hidden');
  el.loginMsg.textContent = msg || '';
  el.loginMsg.className = 'note' + (msg ? ' err' : '');
  el.token.focus();
}

function showApp() {
  el.login.classList.add('hidden');
  el.app.classList.remove('hidden');
  el.install.textContent = `curl -fsSL ${location.origin}/install | sh`;
}

// ------------------------------------------------------------------ events ---

el.loginForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  const res = await fetch('/api/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: el.token.value }),
  });
  if (res.ok) {
    el.token.value = '';
    showApp();
  } else {
    showLogin('Wrong token.');
  }
});

el.logout.addEventListener('click', async () => {
  await fetch('/api/logout', { method: 'POST', credentials: 'same-origin' });
  el.drops.textContent = '';
  el.drops.classList.add('hidden');
  showLogin();
});

el.copyInstall.addEventListener('click', async () => {
  const ok = await copy(el.install.textContent);
  el.copyInstall.textContent = ok ? 'Copied' : 'Copy failed';
  setTimeout(() => { el.copyInstall.textContent = 'Copy install command'; }, 1200);
});

// Paste anywhere on the page. This is the main path: Win+Shift+S then Ctrl+V.
document.addEventListener('paste', (e) => {
  if (el.app.classList.contains('hidden')) return;
  const files = [...(e.clipboardData?.files || [])];
  if (files.length) {
    e.preventDefault();
    handleFiles(files);
  }
});

el.drop.addEventListener('click', () => el.file.click());
el.drop.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); el.file.click(); }
});
el.file.addEventListener('change', () => {
  handleFiles(el.file.files);
  el.file.value = '';
});

for (const type of ['dragenter', 'dragover']) {
  el.drop.addEventListener(type, (e) => { e.preventDefault(); el.drop.classList.add('hot'); });
}
for (const type of ['dragleave', 'drop']) {
  el.drop.addEventListener(type, (e) => { e.preventDefault(); el.drop.classList.remove('hot'); });
}
el.drop.addEventListener('drop', (e) => handleFiles(e.dataTransfer?.files || []));

// ------------------------------------------------------------------- start ---

(async () => {
  if (!crypto?.subtle) {
    document.body.innerHTML =
      '<main><p class="err">This page needs WebCrypto, which browsers only expose over HTTPS ' +
      'or on localhost. Serve otosu over TLS.</p></main>';
    return;
  }
  const res = await fetch('/api/session', { credentials: 'same-origin' });
  if (res.ok) showApp(); else showLogin();
})();
