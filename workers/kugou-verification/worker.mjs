const MAX_BODY = 65536;
const TOKEN = /^[a-f0-9]{64}$/;
const HEADERS = {
  'Cache-Control': 'no-store',
  'Referrer-Policy': 'no-referrer',
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'Permissions-Policy': 'camera=(), microphone=(), geolocation=()',
  'Content-Security-Policy': "default-src 'none'; script-src 'self' 'wasm-unsafe-eval' https://turing.captcha.qcloud.com https://*.captcha.qcloud.com https://captcha.gtimg.com https://*.captcha.gtimg.com; connect-src 'self' https://*.captcha.qcloud.com https://*.captcha.qq.com https://*.captcha.gtimg.com; frame-src https://*.captcha.qcloud.com https://*.captcha.qq.com https://*.captcha.gtimg.com; img-src 'self' data: https://*.captcha.qcloud.com https://*.captcha.qq.com https://captcha.gtimg.com https://*.captcha.gtimg.com; style-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
};

function json(value, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { ...HEADERS, 'Content-Type': 'application/json; charset=utf-8' } });
}

function fail(status) { return json({ error: 'request_unavailable' }, status); }
function seconds() { return Math.floor(Date.now() / 1000); }
function hex(bytes) { return Array.from(bytes, x => x.toString(16).padStart(2, '0')).join(''); }
function randomToken() { return hex(crypto.getRandomValues(new Uint8Array(32))); }
async function digest(value) { return new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))); }

async function authorized(request, secret) {
  if (typeof secret !== 'string' || secret.length < 32) return false;
  const actual = request.headers.get('Authorization') || '';
  if (actual.length > 1024) return false;
  const [a, b] = await Promise.all([digest(actual), digest(`Bearer ${secret}`)]);
  let difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a[i] ^ b[i];
  return difference === 0;
}

async function body(request, fields) {
  if (request.headers.get('Content-Type')?.split(';')[0].trim() !== 'application/json') throw 415;
  if (Number(request.headers.get('Content-Length')) > MAX_BODY) throw 413;
  if (!request.body) throw 400;
  const reader = request.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > MAX_BODY) { await reader.cancel(); throw 413; }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const data = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { data.set(chunk, offset); offset += chunk.length; }
  let result;
  try { result = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data)); } catch { throw 400; }
  if (!result || typeof result !== 'object' || Array.isArray(result) || Object.keys(result).some(key => !fields.includes(key))) throw 400;
  return result;
}

function asset(path, status = 200) {
  const entry = ASSETS[path];
  if (!entry) return fail(404);
  return new Response(Uint8Array.from(atob(entry.data), c => c.charCodeAt(0)), {
    status, headers: { ...HEADERS, 'Content-Type': entry.type },
  });
}

async function expire(db) {
  await db.prepare('DELETE FROM challenges WHERE expires_at <= ?').bind(seconds()).run();
}

async function route(request, env) {
  const url = new URL(request.url);
  const path = url.pathname;
  if (request.method === 'GET' && path === '/health') return json({ ok: true });
  if (request.method === 'GET' && (path === '/' || Object.hasOwn(ASSETS, path))) return asset(path);

  if (path.startsWith('/api/')) {
    if (!await authorized(request, env.BOT_API_SECRET)) return fail(401);
    if (path === '/api/challenges' && request.method === 'POST') {
      const input = await body(request, ['captcha_app_id', 'expires_in', 'test_mode']);
      if (typeof input.captcha_app_id !== 'string' || !/^\d{6,12}$/.test(input.captcha_app_id)) return fail(400);
      if (input.test_mode !== undefined && typeof input.test_mode !== 'boolean') return fail(400);
      const ttl = input.expires_in ?? 600;
      if (!Number.isInteger(ttl) || ttl < 60 || ttl > 900) return fail(400);
      const id = randomToken(), token = randomToken(), expires_at = seconds() + ttl;
      await expire(env.DB);
      await env.DB.prepare('INSERT INTO challenges (id, token_hash, captcha_app_id, expires_at, test_mode) VALUES (?, ?, ?, ?, ?)')
        .bind(id, hex(await digest(token)), input.captcha_app_id, expires_at, input.test_mode === true ? 1 : 0).run();
      return json({ id, url: `${url.origin}/v/${token}`, expires_at }, 201);
    }
    const match = path.match(/^\/api\/challenges\/([a-f0-9]{64})(\/complete)?$/);
    if (!match) return fail(404);
    const row = await env.DB.prepare('SELECT status, proof, expires_at FROM challenges WHERE id = ?').bind(match[1]).first();
    if (!row) return fail(404);
    if (row.expires_at <= seconds()) {
      await env.DB.prepare('DELETE FROM challenges WHERE id = ?').bind(match[1]).run();
      return json({ status: 'expired' });
    }
    if (request.method === 'GET' && !match[2]) {
      return json({ status: row.status, ...(row.status === 'submitted' && row.proof ? { proof: JSON.parse(row.proof) } : {}) });
    }
    if (request.method === 'POST' && match[2]) {
      const input = await body(request, ['status']);
      if (!['verified', 'failed', 'expired'].includes(input.status)) return fail(400);
      // Completion is idempotent; an old retry cannot overwrite a terminal result.
      if (row.status === input.status) return json({ status: row.status });
      const result = await env.DB.prepare("UPDATE challenges SET status = ?, proof = NULL WHERE id = ? AND expires_at > ? AND (status = 'submitted' OR (status = 'pending' AND ? != 'verified'))")
        .bind(input.status, match[1], seconds(), input.status).run();
      return result.meta.changes === 1 ? json({ status: input.status }) : fail(409);
    }
    return fail(405);
  }

  const match = path.match(/^\/v\/([^/]+)(\/(state|submit))?$/);
  if (!match || !TOKEN.test(match[1])) return fail(404);
  // Do not select proof or the management ID on any public read.
  const tokenHash = hex(await digest(match[1]));
  const row = await env.DB.prepare('SELECT captcha_app_id, status, expires_at, test_mode FROM challenges WHERE token_hash = ?').bind(tokenHash).first();
  if (!row || row.expires_at <= seconds()) {
    if (row) await env.DB.prepare('DELETE FROM challenges WHERE token_hash = ?').bind(tokenHash).run();
    return request.method === 'GET' && !match[2] ? asset('/', 410) : json({ status: 'expired' }, 410);
  }
  if (request.method === 'GET' && !match[2]) return asset('/');
  if (request.method === 'GET' && match[3] === 'state') {
    return json({ captcha_app_id: row.captcha_app_id, status: row.status, expires_at: row.expires_at, ...(row.test_mode === 1 ? { test_mode: true } : {}) });
  }
  if (request.method === 'POST' && match[3] === 'submit') {
    if (request.headers.get('Origin') !== url.origin) return fail(403);
    const input = await body(request, ['ticket', 'randstr', 'sid', 'edt']);
    for (const [key, max] of Object.entries({ ticket: 8192, randstr: 256, sid: 2048, edt: 49152 })) {
      if (typeof input[key] !== 'string' || input[key].length < 1 || input[key].length > max) return fail(400);
    }
    // Only authenticated creation can select test mode. A test acknowledges
    // browser callback delivery, not provider acceptance, and never stores proof.
    const status = row.test_mode === 1 ? 'verified' : 'submitted';
    const result = await env.DB.prepare("UPDATE challenges SET status = ?, proof = ? WHERE token_hash = ? AND status = 'pending' AND expires_at > ?")
      .bind(status, row.test_mode === 1 ? null : JSON.stringify(input), tokenHash, seconds()).run();
    return result.meta.changes === 1 ? json({ status }, 202) : fail(409);
  }
  return fail(405);
}

export default {
  async fetch(request, env) {
    try { return await route(request, env); }
    // Never return upstream exception text, SQL arguments, paths, or credentials.
    catch (error) { return fail(Number.isInteger(error) && error >= 400 && error < 500 ? error : 503); }
  },
  async scheduled(_event, env) { await expire(env.DB); },
};
