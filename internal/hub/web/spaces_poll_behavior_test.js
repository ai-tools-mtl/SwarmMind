'use strict';
/* hub 控制台 app.js 协作空间轮询/详情守卫行为断言 harness（W07-03 必修1/必修2
 * 反例回归，codex 审查指令）：执行方 spaces_poll_test.go 经 node 子进程调
 * dispatch_test.go 的 consoleHarnessRunner——本文件按「桩段 + APP.JS 插入标记 +
 * 驱动段」被 runner 切分拼接（桩段结构沿用 dispatch_suggest_behavior_test.js
 * 同款；两段顶级标识符一律 __sp_ 前缀防撞名）。
 *
 * 场景判据：
 *   poll-picks-up-remote-change —— 他端建/归档空间后，本端仅经 loadAll 轮询
 *     （不手动重载）列表槽位即更新（必修1：他端变更可见）；
 *   first-load-failure-self-heals —— 首载失败（500）进失败态（与文案一致），
 *     轮询恢复后列表自动就绪（必修1：自恢复，不依赖手动重试）；
 *   detail-no-rollback —— 详情页乱序应答不回退（必修2）：R2(v2) 先落、R1(v1)
 *     后到不回退；A→B→A 中旧 A 应答放行也不回退（请求序号守卫）。
 */

/* ---------- 桩段（先于 app.js 求值；与 dispatch_suggest_behavior_test.js 同款） ---------- */
const __sp_handlers = {};
const __sp_els = new Map();

function __sp_el(sel) {
  return {
    id: String(sel).replace(/^#/, ''), value: '', textContent: '', innerHTML: '',
    hidden: false, disabled: false, checked: false, placeholder: '', className: '',
    tagName: 'DIV', dataset: {}, style: {}, children: [], isConnected: true,
    classList: {
      _s: new Set(),
      add(...c) { c.forEach(x => this._s.add(x)); },
      remove(...c) { c.forEach(x => this._s.delete(x)); },
      toggle(c, f) { if (f === undefined) f = !this._s.has(c); if (f) this._s.add(c); else this._s.delete(c); return f; },
      contains(c) { return this._s.has(c); },
    },
    appendChild(ch) { this.children.push(ch); return ch; },
    prepend(ch) { this.children.unshift(ch); return ch; },
    contains() { return false; },
    closest() { return null; },
    focus() {}, blur() {}, remove() {},
    setAttribute(k, v) { this[k] = v; },
    getAttribute(k) { return this[k]; },
    addEventListener() {}, removeEventListener() {},
    querySelector() { return null; }, querySelectorAll() { return []; },
    get selectionStart() { return this.value.length; },
    setSelectionRange() {},
  };
}
function __sp_getEl(sel) { if (!__sp_els.has(sel)) __sp_els.set(sel, __sp_el(sel)); return __sp_els.get(sel); }

globalThis.document = {
  title: '', visibilityState: 'visible', activeElement: null, body: __sp_getEl('body'),
  querySelector(sel) { return __sp_getEl(sel); },
  querySelectorAll() { return []; },
  getElementById(id) { return __sp_getEl('#' + id); },
  createElement() { return __sp_el(''); },
  addEventListener(type, fn) { (__sp_handlers[type] = __sp_handlers[type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.window = {
  scrollTo() {}, scrollY: 0,
  addEventListener(type, fn) { (__sp_handlers['window:' + type] = __sp_handlers['window:' + type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.location = { hash: '#/spaces', host: 'hub.test', protocol: 'https:' };
globalThis.fetch = (path, opts = {}) => {
  const rec = { method: opts.method || 'GET', path: String(path), body: opts.body || null };
  if (__sp_hook) return __sp_hook(rec);
  return Promise.resolve({ status: 200, ok: true, json: async () => ({}) });
};
let __sp_hook = null;

/* ==== APP.JS INSERTED BELOW BY RUNNER ==== */

/* ---------- 驱动段（app.js 之后求值：S/loadAll/paintSpacesView/fetchSpacePage 直接可见） ---------- */
const __sp_settle = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)); };
const __sp_ok = json => ({ status: 200, ok: true, json: async () => json });
const __sp_err = code => ({ status: code, ok: false, json: async () => ({ error: 'x', message: 'boom' }) });
const __sp_list = zone => __sp_getEl('#' + zone).innerHTML;
const __sp_sp = (id, goal, extra = {}) => ({
  id, goal, status: 'pending_config', mode: 'manual', version: 1, archived: false,
  updated_at: '2026-11-03T00:00:00Z', created_at: '2026-11-03T00:00:00Z',
  leader: {}, policy: { finish_auth: 'human_only' }, budget: {}, used: {}, ...extra,
});
/* __sp_spacesHook 列表端点路由（场景逐段改 payloads 模拟他端变更）。 */
let __sp_payloads = null;
function __sp_spacesHook() {
  __sp_hook = rec => {
    if (rec.method === 'GET' && rec.path === '/api/v1/spaces') {
      if (__sp_payloads === 'fail') return __sp_err(500);
      return __sp_ok({ spaces: __sp_payloads, scopes: {}, total: __sp_payloads.length });
    }
    return __sp_ok({});
  };
}
/* __sp_detailHook 详情三源路由：sr（详情）按路径挂手动放行队列（乱序控制），
 * mr/tr 即时应答。 */
const __sp_waiters = [];
function __sp_detailHook() {
  __sp_hook = rec => {
    if (rec.method === 'GET' && (rec.path === '/api/v1/spaces/spA' || rec.path === '/api/v1/spaces/spB')) {
      let resolve;
      const p = new Promise(r => { resolve = r; });
      __sp_waiters.push({ path: rec.path, resolve });
      return p.then(body => __sp_ok(body));
    }
    if (rec.path.includes('/messages')) return __sp_ok({ messages: [], pruned_before_seq: 0 });
    if (rec.path.includes('/strategy')) return __sp_ok({ strategy: { has_loop: false } });
    return __sp_ok({});
  };
}
/* __sp_respond_at 按队列位放行应答（同路径多请求在途时以位次区分先后——乱序
 * 控制的最小必要精度）；__sp_waiters 保持挂起序（即请求发起序）。 */
function __sp_respond_at(i, body) {
  if (i < 0 || i >= __sp_waiters.length) throw new Error('应答位越界 ' + i + '（在队=' + __sp_waiters.length + '）');
  const [w] = __sp_waiters.splice(i, 1);   // splice 返回被移除元素的单元素数组
  w.resolve(body);
}
const __sp_detail = (id, version) => ({ space: __sp_sp(id, '目标 ' + id, { version }), scope: 'hub:' + id });
/* __sp_mountSpaces 列表页基线：登录态 + 进协作空间页 + 真实 render + 槽位首绘。 */
function __sp_mountSpaces() {
  S.authed = true; S.page = 'spaces'; S.spaceId = ''; S.groupId = ''; S.sessId = '';
  S.spaces = []; S.spacesLoaded = false; S.spacesUnsupported = false; S.spaceFilter = '';
  S.spacePage = null; S.spacesError = ''; S.fresh = {};
  S.seq = { nodes: 0, tasks: 0, directory: 0, prime: 0, works: 0, sessions: 0, spaces: 0 };
  S.resSnap = { nodes: '', tasks: '', directory: '', prime: '', works: '', sessions: '', spaces: '' };
  __sp_getEl('#app').innerHTML = spacesPage();
  paintSpacesView();
}
async function __hs_run(scenario) {
  if (scenario === 'poll-picks-up-remote-change') {
    __sp_mountSpaces();
    __sp_spacesHook();
    __sp_payloads = [__sp_sp('sp1', '他端可见目标一')];
    await loadAll();
    paintSpacesView();
    if (!__sp_list('space-list-zone').includes('他端可见目标一')) throw new Error('首载后列表未呈现空间');
    if (!__sp_list('space-head-zone').includes('新建空间')) throw new Error('装配在位且加载成功后页头缺创建入口');

    // 另一客户端归档 sp1（本端只被动轮询，不调任何刷新动作）：
    __sp_payloads = [__sp_sp('sp1', '他端可见目标一', { archived: true })];
    await loadAll();
    paintSpacesView();
    if (!__sp_list('space-chips-zone').includes('已归档 (1)')) throw new Error('他端归档后 chips 计数未更新');
    if (!__sp_list('space-list-zone').includes('没有符合筛选的空间')) throw new Error('他端归档后活跃列表未更新');

    // 另一客户端新建 sp2：
    __sp_payloads = [
      __sp_sp('sp1', '他端可见目标一', { archived: true }),
      __sp_sp('sp2', '他端新建目标二'),
    ];
    await loadAll();
    paintSpacesView();
    if (!__sp_list('space-list-zone').includes('他端新建目标二')) throw new Error('他端新建后列表未更新');
    return 'PASS poll-picks-up-remote-change';
  }
  if (scenario === 'first-load-failure-self-heals') {
    __sp_mountSpaces();
    __sp_spacesHook();
    __sp_payloads = 'fail';
    await loadAll();
    paintSpacesView();
    if (!__sp_list('space-list-zone').includes('加载失败')) throw new Error('首载失败未进失败态');
    if (__sp_list('space-head-zone').includes('新建空间')) throw new Error('失败态页头不应有创建入口');
    // 轮询恢复（同 loadAll 链，无手动重试）：
    __sp_payloads = [__sp_sp('sp1', '自恢复目标')];
    await loadAll();
    paintSpacesView();
    if (!__sp_list('space-list-zone').includes('自恢复目标')) throw new Error('恢复后列表未自动就绪');
    if (!__sp_list('space-head-zone').includes('新建空间')) throw new Error('恢复后页头创建入口未点亮');
    return 'PASS first-load-failure-self-heals';
  }
  if (scenario === 'detail-no-rollback') {
    S.authed = true; S.page = 'spaces';
    __sp_detailHook();
    // A 首访（R1 挂起）：
    S.spaceId = 'spA';
    S.spacePage = { id: 'spA', space: null, scope: '', zones: {}, msgs: [], lastSeq: 0, prunedSeq: 0, msgReqId: '' };
    const r1 = fetchSpacePage();
    // B（R2 挂起后先应答 v2）：
    S.spaceId = 'spB';
    S.spacePage = { id: 'spB', space: null, scope: '', zones: {}, msgs: [], lastSeq: 0, prunedSeq: 0, msgReqId: '' };
    const r2 = fetchSpacePage();
    await __sp_settle();
    __sp_respond_at(1, __sp_detail('spB', 2));   // 新请求先返
    await r2; await __sp_settle();
    if (S.spacePage.id !== 'spB' || S.spacePage.space.version !== 2) throw new Error('R2 新快照未落');
    // 回 A（R3 挂起后应答 v3）：
    S.spaceId = 'spA';
    S.spacePage = { id: 'spA', space: null, scope: '', zones: {}, msgs: [], lastSeq: 0, prunedSeq: 0, msgReqId: '' };
    const r3 = fetchSpacePage();
    await __sp_settle();
    __sp_respond_at(1, __sp_detail('spA', 3));   // 新请求先返
    await r3; await __sp_settle();
    if (S.spacePage.space.version !== 3) throw new Error('R3 新快照未落');
    // 旧 A 应答（R1, v1）此时才到：序号守卫必须作废，不得 version=3→1 回退
    //（A→B→A 放行旧 A 反例）。
    __sp_respond_at(0, __sp_detail('spA', 1));   // 旧请求后返
    await r1; await __sp_settle();
    if (S.spacePage.space.version !== 3) throw new Error('乱序旧应答回退了新快照（version=' + S.spacePage.space.version + '）');
    // 同空间双在途：R5（新）先返 v5、R4（旧）后返 v4 → 保持 v5（队列位=发起序：
    // respond_at(1)=新请求先返、respond_at(0)=旧请求后返）。
    const r4 = fetchSpacePage();   // 队列 [0]=R4
    const r5 = fetchSpacePage();   // 队列 [0]=R4, [1]=R5
    await __sp_settle();
    __sp_respond_at(1, __sp_detail('spA', 5));   // 新请求先返
    await r5; await __sp_settle();
    if (S.spacePage.space.version !== 5) throw new Error('v5 快照未落');
    __sp_respond_at(0, __sp_detail('spA', 4));   // 旧请求后返
    await r4; await __sp_settle();
    if (S.spacePage.space.version !== 5) throw new Error('后到旧应答回退（version=5→' + S.spacePage.space.version + '）');
    return 'PASS detail-no-rollback';
  }
  throw new Error('未知场景 ' + scenario);
}
