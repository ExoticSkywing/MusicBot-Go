import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DatabaseSync } from 'node:sqlite';
import { readFile, readdir } from 'node:fs/promises';
import worker from './dist/worker.mjs';

const migrations = new URL('migrations/', import.meta.url);
const schema = (await Promise.all((await readdir(migrations)).filter(name => name.endsWith('.sql')).sort()
  .map(name => readFile(new URL(name, migrations), 'utf8')))).join('\n');
const origin = 'https://verify.example.com';
const secret = 'test-management-secret-at-least-32-bytes';
const proof = { ticket: 'human-ticket', randstr: 'human-randstr', sid: 'browser-sid', edt: 'browser-evidence' };
function setup(t) {
  const sqlite = new DatabaseSync(':memory:');
  sqlite.exec(schema);
  t.after(() => sqlite.close());
  const env = { BOT_API_SECRET: secret, DB: {
    prepare(sql) {
      const stmt = sqlite.prepare(sql);
      let values = [];
      return {
        bind(...v) { values = v; return this; },
        async first() { return stmt.get(...values) ?? null; },
        async run() { return { success: true, meta: stmt.run(...values) }; },
      };
    },
  } };
  const call = (path, method = 'GET', input, auth = false, headers = {}) => worker.fetch(new Request(new URL(path, origin), {
    method, headers: { ...(input ? { 'Content-Type': 'application/json' } : {}), ...(auth ? { Authorization: `Bearer ${secret}` } : {}), ...headers },
    ...(input ? { body: JSON.stringify(input) } : {}),
  }), env);
  const create = async (extra = {}) => {
    const response = await call('/api/challenges', 'POST', { captcha_app_id: '197787253', expires_in: 600, ...extra }, true);
    assert.equal(response.status, 201);
    return response.json();
  };
  return { sqlite, env, call, create };
}

test('public lifecycle has no account fields, management id, token hash, or submitted proof', async t => {
  const { sqlite, call, create } = setup(t);
  const job = await create();
  const path = new URL(job.url).pathname;
  const row = sqlite.prepare('SELECT * FROM challenges').get();
  assert.notEqual(row.token_hash, path.split('/').at(-1));
  assert.equal(Object.values(row).includes(path.split('/').at(-1)), false);
  assert.deepEqual(Object.keys(row).sort(), ['captcha_app_id', 'expires_at', 'id', 'proof', 'status', 'test_mode', 'token_hash']);
  const pending = await (await call(`${path}/state`)).json();
  assert.deepEqual(Object.keys(pending).sort(), ['captcha_app_id', 'expires_at', 'status']);
  assert.equal(pending.status, 'pending');
  const html = await call(path);
  assert.equal(html.headers.get('Referrer-Policy'), 'no-referrer');
  assert.equal(html.headers.get('Cache-Control'), 'no-store');
  assert.equal((await html.text()).includes(job.id), false);
  assert.equal((await call(`${path}/submit`, 'POST', proof, false, { Origin: origin })).status, 202);
  const publicResult = await (await call(`${path}/state`)).json();
  assert.equal(publicResult.status, 'submitted');
  assert.deepEqual(Object.keys(publicResult).sort(), ['captcha_app_id', 'expires_at', 'status']);
  const privateResult = await (await call(`/api/challenges/${job.id}`, 'GET', undefined, true)).json();
  assert.deepEqual(privateResult, { status: 'submitted', proof });
  assert.equal((await call(`/api/challenges/${job.id}/complete`, 'POST', { status: 'verified' }, true)).status, 200);
  assert.equal(sqlite.prepare('SELECT proof FROM challenges').get().proof, null);
  assert.equal((await (await call(`${path}/state`)).json()).status, 'verified');
  assert.equal((await call(`${path}/submit`, 'POST', proof, false, { Origin: origin })).status, 409);
  assert.equal((await call(`/api/challenges/${job.id}/complete`, 'POST', { status: 'verified' }, true)).status, 200);
  assert.equal((await call(`/api/challenges/${job.id}/complete`, 'POST', { status: 'failed' }, true)).status, 409);
});

test('management authorization and strict allowlists reject account data', async t => {
  const { call, create, sqlite } = setup(t);
  const payload = { captcha_app_id: '197787253' };
  assert.equal((await call('/api/challenges', 'POST', payload)).status, 401);
  assert.equal((await call('/api/challenges', 'POST', payload, false, { Authorization: 'Bearer wrong' })).status, 401);
  for (const field of ['userid', 'cookie', 'token', 'dfid', 'eventid', 'nickname']) {
    const response = await call('/api/challenges', 'POST', { ...payload, [field]: 'PRIVATE' }, true);
    assert.equal(response.status, 400);
    assert.equal((await response.text()).includes('PRIVATE'), false);
  }
  assert.equal(sqlite.prepare('SELECT count(*) AS n FROM challenges').get().n, 0);
  const job = await create();
  const path = new URL(job.url).pathname;
  assert.equal((await call(`/api/challenges/${job.id}`)).status, 401);
  assert.equal((await call(`/api/challenges/${path.split('/').at(-1)}`, 'GET', undefined, true)).status, 404);
  assert.equal((await call(`${path}/submit`, 'POST', proof)).status, 403);
  assert.equal((await call(`${path}/submit`, 'POST', proof, false, { Origin: 'https://evil.example' })).status, 403);
  assert.equal((await call(`${path}/submit`, 'POST', { ...proof, cookie: 'PRIVATE' }, false, { Origin: origin })).status, 400);
  assert.equal((await call(`${path}/submit`, 'POST', { ...proof, edt: 'a'.repeat(70000) }, false, { Origin: origin })).status, 413);
  assert.equal((await call(`/api/challenges/${job.id}/complete`, 'POST', { status: 'verified' }, true)).status, 409);
});

test('concurrent proof submissions consume a pending challenge exactly once', async t => {
  const { call, create } = setup(t);
  const job = await create();
  const path = new URL(job.url).pathname;
  const statuses = await Promise.all(Array.from({ length: 12 }, (_, n) => call(`${path}/submit`, 'POST', { ...proof, ticket: `ticket-${n}` }, false, { Origin: origin }).then(r => r.status)));
  assert.equal(statuses.filter(x => x === 202).length, 1);
  assert.equal(statuses.filter(x => x === 409).length, 11);
});

test('isolated test tasks acknowledge callbacks once without storing proof', async t => {
  const { call, create, sqlite } = setup(t);
  const job = await create({ test_mode: true });
  const path = new URL(job.url).pathname;
  const pending = await (await call(`${path}/state`)).json();
  assert.deepEqual(pending, { captcha_app_id: '197787253', expires_at: job.expires_at, status: 'pending', test_mode: true });
  const responses = await Promise.all(Array.from({ length: 8 }, () => call(`${path}/submit`, 'POST', proof, false, { Origin: origin })));
  assert.equal(responses.filter(r => r.status === 202).length, 1);
  assert.equal(responses.filter(r => r.status === 409).length, 7);
  assert.deepEqual(await responses.find(r => r.status === 202).json(), { status: 'verified' });
  assert.equal(sqlite.prepare('SELECT proof FROM challenges WHERE id = ?').get(job.id).proof, null);
  assert.deepEqual(await (await call(`/api/challenges/${job.id}`, 'GET', undefined, true)).json(), { status: 'verified' });
  assert.deepEqual(await (await call(`${path}/state`)).json(), { ...pending, status: 'verified' });
});

test('test mode requires authenticated creation and cannot alter a real task', async t => {
  const { call, create, sqlite, env } = setup(t);
  const input = { captcha_app_id: '197787253', test_mode: true };
  assert.equal((await call('/api/challenges', 'POST', input)).status, 401);
  for (const test_mode of ['true', 1, null, {}, []]) {
    assert.equal((await call('/api/challenges', 'POST', { ...input, test_mode }, true)).status, 400);
  }
  const job = await create();
  const path = new URL(job.url).pathname;
  assert.equal((await call(`${path}/submit`, 'POST', { ...proof, test_mode: true }, false, { Origin: origin })).status, 400);
  assert.equal(sqlite.prepare('SELECT status, test_mode FROM challenges WHERE id = ?').get(job.id).status, 'pending');
  assert.equal(sqlite.prepare('SELECT test_mode FROM challenges WHERE id = ?').get(job.id).test_mode, 0);
  assert.equal((await call(`${path}/submit`, 'POST', proof, false, { Origin: origin })).status, 202);
  assert.deepEqual(await (await call(`/api/challenges/${job.id}`, 'GET', undefined, true)).json(), { status: 'submitted', proof });
  const demo = await create({ test_mode: true });
  sqlite.prepare('UPDATE challenges SET expires_at = 0 WHERE id = ?').run(demo.id);
  assert.equal((await call(`${new URL(demo.url).pathname}/submit`, 'POST', proof, false, { Origin: origin })).status, 410);
  await worker.scheduled({}, env);
  assert.equal(sqlite.prepare('SELECT count(*) AS n FROM challenges WHERE id = ?').get(demo.id).n, 0);
});

test('expired links cannot submit and cleanup removes stored proof', async t => {
  const { call, create, sqlite, env } = setup(t);
  const job = await create();
  const path = new URL(job.url).pathname;
  await call(`${path}/submit`, 'POST', proof, false, { Origin: origin });
  sqlite.prepare('UPDATE challenges SET expires_at = 0').run();
  assert.equal((await call(`${path}/submit`, 'POST', proof, false, { Origin: origin })).status, 410);
  assert.equal(sqlite.prepare('SELECT count(*) AS n FROM challenges').get().n, 0);
  const next = await create();
  sqlite.prepare('UPDATE challenges SET expires_at = 0').run();
  assert.deepEqual(await (await call(`/api/challenges/${next.id}`, 'GET', undefined, true)).json(), { status: 'expired' });
  await create();
  sqlite.prepare('UPDATE challenges SET expires_at = 0, proof = ?').run(JSON.stringify(proof));
  await worker.scheduled({}, env);
  assert.equal(sqlite.prepare('SELECT count(*) AS n FROM challenges').get().n, 0);
});

test('static assets have expected MIME and database failures never echo internals', async t => {
  const { call, env } = setup(t);
  const wasm = await call('/verifycode_bg_ios.wasm');
  assert.equal(wasm.headers.get('Content-Type'), 'application/wasm');
  assert.deepEqual([...new Uint8Array(await wasm.arrayBuffer()).slice(0, 4)], [0, 97, 115, 109]);
  assert.equal((await call('/client.js')).headers.get('Content-Type'), 'application/javascript; charset=utf-8');
  env.DB.prepare = () => { throw Error('private-account-and-sql'); };
  const response = await call('/api/challenges', 'POST', { captcha_app_id: '197787253' }, true);
  assert.equal(response.status, 503);
  assert.equal(await response.text(), '{"error":"request_unavailable"}');
});
