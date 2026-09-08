(() => {
  'use strict';
  const button = document.querySelector('#start');
  const state = document.querySelector('#state');
  const expiry = document.querySelector('#expiry');
  const modeLabel = document.querySelector('#mode-label');
  const headline = document.querySelector('#headline');
  const intro = document.querySelector('#intro');
  const path = location.pathname;
  const terminalStatuses = new Set(['verified', 'failed', 'expired']);
  const statusRank = { unknown: -1, pending: 0, submitted: 1, verified: 2, failed: 2, expired: 2 };

  let appid;
  let expiresAt;
  let serverStatus = 'unknown';
  let components = 'idle';
  let activity = 'idle';
  let stopped = false;
  let connectionFailed = false;
  let timer;
  let pollPromise;
  let componentPromise;
  let proof;
  let cancelActiveCaptcha;
  let testMode = false;
  let testModeKnown = false;

  function copy(normal, test) {
    return testMode ? test : normal;
  }

  function applyTestMode(next) {
    if (testModeKnown && testMode !== next) throw new Error('test mode changed');
    if (testModeKnown) return;
    testModeKnown = true;
    testMode = next;
    if (!testMode) return;
    document.title = '验证码测试 · MusicBot';
    document.body.classList.add('test-mode');
    modeLabel.textContent = '验证码测试';
    headline.textContent = '测试安全验证流程';
    intro.textContent = '这是一次测试，只检查验证页面和回调是否正常，不会操作酷狗账号。';
  }

  function label(text, enabled = false) {
    button.textContent = text;
    button.disabled = !enabled;
  }

  function render() {
    if (serverStatus === 'verified') {
      state.textContent = copy('验证通过！请返回 Bot，重新发送音乐请求。', '测试完成，验证码结果已收到。未操作酷狗账号。');
      label(copy('验证已完成 ✓', '测试已完成 ✓'));
      return;
    }
    if (serverStatus === 'failed') {
      state.textContent = copy('验证未通过，请返回 Bot 重新获取验证链接。', '测试未完成，请重新获取测试链接。');
      label(copy('请重新发起请求', '请重新发起测试'));
      return;
    }
    if (serverStatus === 'expired') {
      state.textContent = copy('链接已失效，请返回 Bot 重新获取验证链接。', '测试链接已失效，请重新获取测试链接。');
      label(copy('链接已失效', '测试链接已失效'));
      return;
    }
    if (serverStatus === 'submitted') {
      state.textContent = copy('结果已提交，正在确认音乐能否恢复…', '测试结果已提交，正在确认回调状态…');
      label(copy('正在确认…', '正在确认测试…'));
      return;
    }
    if (serverStatus !== 'pending') {
      state.textContent = connectionFailed ? '暂时无法连接，正在重试…' : '正在检查验证链接…';
      label('正在准备…');
      return;
    }
    if (activity === 'captcha') {
      state.textContent = copy('请在官方验证码窗口中完成验证。', '请在官方验证码窗口中完成测试。');
      label(copy('请完成验证码…', '请完成验证码测试…'));
      return;
    }
    if (activity === 'submitting') {
      state.textContent = copy('正在安全提交验证结果…', '正在提交测试结果…');
      label(copy('正在提交…', '正在提交测试…'));
      return;
    }
    if (activity === 'retry' && proof) {
      state.textContent = copy('结果未能送达，请点击重试。', '测试结果未能送达，请点击重试。');
      label(copy('重新提交验证结果', '重新提交测试结果'), true);
      return;
    }
    if (components === 'failed') {
      state.textContent = '验证组件加载失败，请检查网络后刷新页面。';
      label('请刷新页面');
      return;
    }
    if (activity === 'captcha-error') {
      state.textContent = '验证码未能打开，请重试。';
      label('重新打开官方验证码 ↗', components === 'ready');
      return;
    }
    state.textContent = copy('等待你完成官方验证码', '等待你完成官方验证码测试');
    label(components === 'ready' ? copy('打开官方验证码 ↗', '开始验证码测试 ↗') : '正在加载验证组件…', components === 'ready');
  }

  function stop(status) {
    stopped = true;
    clearTimeout(timer);
    timer = undefined;
    proof = null;
    activity = 'idle';
    if (cancelActiveCaptcha) cancelActiveCaptcha();
    expiry.textContent = '';
    serverStatus = status;
    render();
  }

  function applyServerStatus(status) {
    if (!Object.hasOwn(statusRank, status)) return;
    if (terminalStatuses.has(serverStatus) && status !== serverStatus) return;
    if (statusRank[status] < statusRank[serverStatus]) return;
    connectionFailed = false;
    if (terminalStatuses.has(status)) {
      stop(status);
      return;
    }
    serverStatus = status;
    if (status === 'submitted') {
      proof = null;
      activity = 'idle';
      if (cancelActiveCaptcha) cancelActiveCaptcha();
    }
    render();
    if (status === 'pending') void ensureComponents();
  }

  function script(src) {
    return new Promise((resolve, reject) => {
      const element = document.createElement('script');
      element.src = src;
      element.referrerPolicy = 'no-referrer';
      element.onload = resolve;
      element.onerror = reject;
      document.head.append(element);
    });
  }

  function ensureComponents() {
    if (components === 'ready' || components === 'failed') return componentPromise || Promise.resolve();
    if (componentPromise) return componentPromise;
    components = 'loading';
    render();
    componentPromise = (async () => {
      await Promise.all([script('/verifycode.js'), script('https://turing.captcha.qcloud.com/TCaptcha.js')]);
      if (typeof wasm_bindgen !== 'function') throw new Error('missing wasm loader');
      await wasm_bindgen('/verifycode_bg_ios.wasm');
      wasm_bindgen.run();
      if (typeof wasm_bindgen.EData !== 'function' || typeof TencentCaptcha !== 'function') throw new Error('missing verification component');
      components = 'ready';
    })().catch(() => {
      components = 'failed';
    }).finally(() => {
      componentPromise = undefined;
      render();
    });
    return componentPromise;
  }

  async function pollOnce() {
    if (expiresAt && Date.now() >= expiresAt * 1000) {
      applyServerStatus('expired');
      return serverStatus;
    }
    try {
      const response = await fetch(`${path}/state`, { cache: 'no-store', credentials: 'omit', referrerPolicy: 'no-referrer' });
      if (response.status === 410 || response.status === 404) {
        applyServerStatus('expired');
        return serverStatus;
      }
      if (!response.ok) throw new Error('state unavailable');
      const data = await response.json();
      if (typeof data.captcha_app_id !== 'string' || !/^\d{6,12}$/.test(data.captcha_app_id) ||
          !Number.isInteger(data.expires_at) || !Object.hasOwn(statusRank, data.status) ||
          (Object.hasOwn(data, 'test_mode') && data.test_mode !== true)) {
        throw new Error('invalid state');
      }
      applyTestMode(data.test_mode === true);
      appid = data.captcha_app_id;
      expiresAt = data.expires_at;
      expiry.textContent = `链接有效至 ${new Date(expiresAt * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`;
      applyServerStatus(data.status);
      return serverStatus;
    } catch {
      if (serverStatus === 'unknown') {
        connectionFailed = true;
        render();
      }
      return undefined;
    }
  }

  function poll() {
    if (stopped) return Promise.resolve(serverStatus);
    if (pollPromise) return pollPromise;
    clearTimeout(timer);
    timer = undefined;
    pollPromise = pollOnce().finally(() => {
      pollPromise = undefined;
      if (!stopped) timer = setTimeout(() => void poll(), 3000);
    });
    return pollPromise;
  }

  async function submit() {
    if (!proof || stopped || serverStatus !== 'pending' || activity === 'submitting') return;
    activity = 'submitting';
    render();
    try {
      const response = await fetch(`${path}/submit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'omit',
        referrerPolicy: 'no-referrer',
        body: JSON.stringify(proof),
      });
      if (response.status === 410) {
        applyServerStatus('expired');
        return;
      }
      if (response.status === 409) {
        if (!stopped && serverStatus === 'pending') {
          activity = 'retry';
          render();
          void poll();
        }
        return;
      }
      if (!response.ok) throw new Error('submission unavailable');
      const result = await response.json();
      if (!result || typeof result !== 'object' || !['submitted', 'verified'].includes(result.status)) {
        throw new Error('invalid submission result');
      }
      applyServerStatus(result.status);
    } catch {
      if (!stopped && serverStatus === 'pending') {
        activity = 'retry';
        render();
      }
    }
  }

  function openCaptcha() {
    if (proof) {
      void submit();
      return;
    }
    if (stopped || serverStatus !== 'pending') return;
    if (!expiresAt || Date.now() >= expiresAt * 1000) {
      applyServerStatus('expired');
      return;
    }
    if (components !== 'ready' || activity === 'captcha' || activity === 'submitting') return;

    activity = 'captcha';
    render();
    let evidence;
    let settled = false;
    let freed = false;
    const release = () => {
      if (freed || !evidence) return;
      freed = true;
      evidence.free();
    };
    const cancel = () => {
      if (settled) return;
      settled = true;
      release();
      cancelActiveCaptcha = undefined;
    };
    cancelActiveCaptcha = cancel;

    try {
      evidence = new wasm_bindgen.EData();
      const captcha = new TencentCaptcha(appid, result => {
        if (settled) return;
        settled = true;
        cancelActiveCaptcha = undefined;
        let validProof = false;
        try {
          if (!stopped && serverStatus === 'pending' && result && result.ret === 0 &&
              typeof result.ticket === 'string' && result.ticket && typeof result.randstr === 'string' && result.randstr) {
            proof = { ticket: result.ticket, randstr: result.randstr, sid: evidence.get_sid(), edt: evidence.get_edt() };
            validProof = true;
          }
        } catch {
          if (!stopped && serverStatus === 'pending') activity = 'captcha-error';
        } finally {
          release();
        }
        if (stopped || serverStatus !== 'pending') {
          proof = null;
          render();
        } else if (validProof) {
          activity = 'idle';
          void submit();
        } else if (activity !== 'captcha-error') {
          activity = 'idle';
          render();
        } else {
          render();
        }
      }, { showHeader: true });
      captcha.show();
    } catch {
      cancel();
      if (!stopped && serverStatus === 'pending') {
        activity = 'captcha-error';
        render();
      }
    }
  }

  button.onclick = openCaptcha;
  if (!/^\/v\/[a-f0-9]{64}$/.test(path)) {
    state.textContent = '请使用 Bot 发来的验证链接打开此页面。';
    label('等待验证链接');
    return;
  }
  render();
  void poll();
  window.addEventListener('pagehide', () => {
    stopped = true;
    proof = null;
    clearTimeout(timer);
    if (cancelActiveCaptcha) cancelActiveCaptcha();
  });
})();
