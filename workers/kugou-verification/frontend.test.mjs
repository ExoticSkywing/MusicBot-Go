import { test } from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { readFile } from 'node:fs/promises';

const source = await readFile(new URL('public/client.js', import.meta.url), 'utf8');
const token = 'a'.repeat(64);
const path = `/v/${token}`;
const pending = { captcha_app_id: '197787253', status: 'pending', expires_at: 4102444800 };

function response(status, value) {
  return new Response(value === undefined ? undefined : JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function flush() {
  for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve));
}

function setup({ states, submissions = [], autoLoadScripts = true }) {
  const button = { textContent: '', disabled: true, onclick: undefined };
  const state = { textContent: '' };
  const expiry = { textContent: '' };
  const modeLabel = { textContent: '酷狗音乐 · 安全验证' };
  const headline = { textContent: '完成验证，继续听歌。' };
  const intro = { textContent: '请打开官方验证码，手动完成拼图。验证通过后，返回 Bot 重新发送刚才的音乐请求。' };
  const bodyClasses = new Set();
  const scripts = [];
  const timers = new Map();
  const pageHandlers = new Map();
  const captchaCallbacks = [];
  const submittedBodies = [];
  let timerID = 0;
  let freeCount = 0;
  let captchaShows = 0;

  const take = (queue, kind) => {
    assert.ok(queue.length > 0, `unexpected ${kind} request`);
    const next = queue.shift();
    if (next instanceof Error) return Promise.reject(next);
    if (typeof next === 'function') return Promise.resolve().then(next);
    return Promise.resolve(next);
  };
  const fetch = (url, options = {}) => {
    if (url.endsWith('/state')) return take(states, 'state');
    if (url.endsWith('/submit')) {
      submittedBodies.push(JSON.parse(options.body));
      return take(submissions, 'submission');
    }
    throw new Error(`unexpected fetch: ${url}`);
  };
  const document = {
    querySelector(selector) {
      return {
        '#start': button,
        '#state': state,
        '#expiry': expiry,
        '#mode-label': modeLabel,
        '#headline': headline,
        '#intro': intro,
      }[selector];
    },
    createElement(tag) {
      assert.equal(tag, 'script');
      return {};
    },
    head: {
      append(element) {
        scripts.push(element);
        if (autoLoadScripts) queueMicrotask(() => element.onload());
      },
    },
    body: { classList: { add: value => bodyClasses.add(value) } },
    title: '酷狗验证 · MusicBot',
  };
  const wasm_bindgen = async () => {};
  wasm_bindgen.run = () => {};
  wasm_bindgen.EData = class {
    free() { freeCount++; }
    get_sid() { return 'browser-sid'; }
    get_edt() { return 'browser-evidence'; }
  };
  function TencentCaptcha(_appid, callback) {
    captchaCallbacks.push(callback);
    this.show = () => { captchaShows++; };
  }
  const context = {
    console,
    document,
    location: { pathname: path },
    window: { addEventListener: (name, handler) => pageHandlers.set(name, handler) },
    fetch,
    Response,
    wasm_bindgen,
    TencentCaptcha,
    queueMicrotask,
    setTimeout(fn, delay) {
      const id = ++timerID;
      timers.set(id, { fn, delay });
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
  };
  vm.runInNewContext(source, context, { filename: 'client.js' });

  return {
    button,
    state,
    expiry,
    modeLabel,
    headline,
    intro,
    bodyClasses,
    get title() { return document.title; },
    captchaCallbacks,
    submittedBodies,
    get freeCount() { return freeCount; },
    get captchaShows() { return captchaShows; },
    async runNextTimer() {
      const item = timers.entries().next();
      assert.equal(item.done, false, 'expected a scheduled poll');
      const [id, timer] = item.value;
      timers.delete(id);
      timer.fn();
      await flush();
    },
    async resolveScripts() {
      for (const element of scripts.splice(0)) element.onload();
      await flush();
    },
    pagehide() { pageHandlers.get('pagehide')?.(); },
  };
}

test('a transient first state failure recovers and initializes components on a later poll', async () => {
  const page = setup({
    states: [new Error('offline'), response(200, pending)],
  });
  await flush();
  assert.equal(page.state.textContent, '暂时无法连接，正在重试…');
  assert.equal(page.button.disabled, true);

  await page.runNextTimer();
  assert.equal(page.state.textContent, '等待你完成官方验证码');
  assert.equal(page.button.textContent, '打开官方验证码 ↗');
  assert.equal(page.button.disabled, false);
});

test('polling cannot re-enable the button while CAPTCHA is open and cancellation frees evidence once', async () => {
  const latePending = deferred();
  const page = setup({
    states: [response(200, pending), latePending.promise],
  });
  await flush();
  assert.equal(page.button.disabled, false);

  await page.runNextTimer();
  page.button.onclick();
  assert.equal(page.captchaShows, 1);
  assert.equal(page.button.disabled, true);
  assert.equal(page.state.textContent, '请在官方验证码窗口中完成验证。');

  latePending.resolve(response(200, pending));
  await flush();
  assert.equal(page.button.disabled, true);
  assert.equal(page.state.textContent, '请在官方验证码窗口中完成验证。');

  page.captchaCallbacks[0]({ ret: 2 });
  page.captchaCallbacks[0]({ ret: 2 });
  await flush();
  assert.equal(page.freeCount, 1);
  assert.equal(page.button.disabled, false);
});

test('component completion and a stale pending poll cannot overwrite submitted state', async () => {
  const loadingPage = setup({
    states: [response(200, pending), response(200, { ...pending, status: 'submitted' })],
    autoLoadScripts: false,
  });
  await flush();
  assert.equal(loadingPage.button.textContent, '正在加载验证组件…');
  await loadingPage.runNextTimer();
  await loadingPage.resolveScripts();
  assert.equal(loadingPage.state.textContent, '结果已提交，正在确认音乐能否恢复…');
  assert.equal(loadingPage.button.disabled, true);

  const latePending = deferred();
  const submittedPage = setup({
    states: [response(200, pending), latePending.promise],
    submissions: [response(202, { status: 'submitted' })],
  });
  await flush();
  await submittedPage.runNextTimer();
  submittedPage.button.onclick();
  submittedPage.captchaCallbacks[0]({ ret: 0, ticket: 'ticket', randstr: 'randstr' });
  await flush();
  assert.equal(submittedPage.title, '酷狗验证 · MusicBot');
  assert.equal(submittedPage.bodyClasses.has('test-mode'), false);
  assert.equal(submittedPage.modeLabel.textContent, '酷狗音乐 · 安全验证');
  assert.equal(submittedPage.headline.textContent, '完成验证，继续听歌。');
  assert.equal(submittedPage.intro.textContent, '请打开官方验证码，手动完成拼图。验证通过后，返回 Bot 重新发送刚才的音乐请求。');
  assert.equal(submittedPage.state.textContent, '结果已提交，正在确认音乐能否恢复…');

  latePending.resolve(response(200, pending));
  await flush();
  assert.equal(submittedPage.state.textContent, '结果已提交，正在确认音乐能否恢复…');
  assert.equal(submittedPage.button.disabled, true);
});

test('test mode is clearly labelled and a verified submit response uses test-only completion copy', async () => {
  const page = setup({
    states: [response(200, { ...pending, test_mode: true })],
    submissions: [response(200, { status: 'verified' })],
  });
  await flush();

  assert.equal(page.title, '验证码测试 · MusicBot');
  assert.equal(page.bodyClasses.has('test-mode'), true);
  assert.equal(page.modeLabel.textContent, '验证码测试');
  assert.equal(page.headline.textContent, '测试安全验证流程');
  assert.equal(page.intro.textContent, '这是一次测试，只检查验证页面和回调是否正常，不会操作酷狗账号。');
  assert.equal(page.state.textContent, '等待你完成官方验证码测试');
  assert.equal(page.button.textContent, '开始验证码测试 ↗');

  page.button.onclick();
  page.captchaCallbacks[0]({ ret: 0, ticket: 'test-ticket', randstr: 'test-randstr' });
  await flush();

  assert.equal(page.submittedBodies.length, 1);
  assert.equal(page.state.textContent, '测试完成，验证码结果已收到。未操作酷狗账号。');
  assert.equal(page.button.textContent, '测试已完成 ✓');
  assert.equal(page.button.disabled, true);
});

test('a transient submission failure keeps proof for a human-triggered retry', async () => {
  const page = setup({
    states: [response(200, pending)],
    submissions: [new Error('offline'), response(202, { status: 'submitted' })],
  });
  await flush();
  page.button.onclick();
  page.captchaCallbacks[0]({ ret: 0, ticket: 'ticket', randstr: 'randstr' });
  await flush();

  assert.equal(page.freeCount, 1);
  assert.equal(page.state.textContent, '结果未能送达，请点击重试。');
  assert.equal(page.button.textContent, '重新提交验证结果');
  assert.equal(page.button.disabled, false);
  assert.deepEqual(page.submittedBodies[0], {
    ticket: 'ticket',
    randstr: 'randstr',
    sid: 'browser-sid',
    edt: 'browser-evidence',
  });

  page.button.onclick();
  await flush();
  assert.equal(page.submittedBodies.length, 2);
  assert.deepEqual(page.submittedBodies[1], page.submittedBodies[0]);
  assert.equal(page.state.textContent, '结果已提交，正在确认音乐能否恢复…');
  assert.equal(page.button.disabled, true);
});
