'use strict';
/* hub 控制台 app.js 旧锚点/深链兼容行为断言 harness（W08-03 工作块2/5：旧页面/
 * 深链兼容回归锚）：执行方 route_compat_test.go 经 node 子进程调 dispatch_test.go
 * 的 consoleHarnessRunner——本文件按「桩段 + APP.JS 插入标记 + 驱动段」被 runner
 * 切分拼接（桩段结构沿用 spaces_poll_behavior_test.js 同款；顶级标识符一律 __rc_
 * 前缀防撞名）。
 *
 * 场景判据（深链语义 = route() 按真实 hashchange 事件驱动的行为，非手工赋值）：
 *   deep-link-session-detail —— #/sessions/{id} 直达会话详情页（W01-05 P02）；
 *   deep-link-space-detail —— #/spaces/{id} 直达空间详情页（W07-03 P04）；
 *   deep-link-task-status-filter —— #/tasks/{status} 深链携带状态筛选（既有语义）；
 *   deep-link-group —— #/groups/{id} 直达群视图（既有语义）；
 *   unknown-hash-falls-back —— 未知 hash 回落总览（不 404 不白屏，layout 恒渲染）；
 *   legacy-session-entry-jumps —— goSessionDetail 旧入口兼容跳转（W01-05：旧锚点/
 *     动作全走详情页——700px 会话抽屉退役后的兼容面）；
 *   unauthed-deep-link-hits-login —— 未登录态任意深链收敛登录页（不白屏）。
 */

/* ---------- 桩段（先于 app.js 求值；与 spaces_poll_behavior_test.js 同款） ---------- */
const __rc_handlers = {};
const __rc_els = new Map();

function __rc_el(sel) {
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
    removeAttribute(k) { delete this[k]; },
    addEventListener() {}, removeEventListener() {},
    querySelector() { return null; }, querySelectorAll() { return []; },
    get selectionStart() { return this.value.length; },
    setSelectionRange() {},
  };
}
function __rc_getEl(sel) { if (!__rc_els.has(sel)) __rc_els.set(sel, __rc_el(sel)); return __rc_els.get(sel); }

globalThis.document = {
  title: '', visibilityState: 'visible', activeElement: null, body: __rc_getEl('body'),
  querySelector(sel) { return __rc_getEl(sel); },
  querySelectorAll() { return []; },
  getElementById(id) { return __rc_getEl('#' + id); },
  createElement() { return __rc_el(''); },
  addEventListener(type, fn) { (__rc_handlers[type] = __rc_handlers[type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.window = {
  scrollTo() {}, scrollY: 0,
  addEventListener(type, fn) { (__rc_handlers['window:' + type] = __rc_handlers['window:' + type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.location = { hash: '#/overview', host: 'hub.test', protocol: 'https:' };
globalThis.fetch = (path, opts = {}) => {
  if (__rc_hook) return __rc_hook({ method: opts.method || 'GET', path: String(path), body: opts.body || null });
  return Promise.resolve({ status: 200, ok: true, json: async () => ({}) });
};
let __rc_hook = null;

/* ==== APP.JS INSERTED BELOW BY RUNNER ==== */

/* ---------- 驱动段（app.js 之后求值：S/route/goSessionDetail 直接可见） ---------- */
const __rc_settle = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)); };
/* __rc_nav 真实事件路径导航：写 hash 后触发 window 上注册的 hashchange 监听
 *（app.js 顶层注册 route——不手工调 route，接线本身即被测面）。 */
async function __rc_nav(hash) {
  globalThis.location.hash = hash;
  for (const fn of (__rc_handlers['window:hashchange'] || [])) fn();
  await __rc_settle();
}
const __rc_assert = (cond, msg) => { if (!cond) throw new Error(msg); };

async function __hs_run(scenario) {
  if (scenario === 'deep-link-session-detail') {
    S.authed = true;
    await __rc_nav('#/sessions/sess-9f');
    __rc_assert(S.page === 'sessions', '深链 #/sessions/{id} 应落会话页，got ' + S.page);
    __rc_assert(S.sessId === 'sess-9f', '深链应携带会话 ID，got ' + S.sessId);
    return 'PASS deep-link-session-detail';
  }
  if (scenario === 'deep-link-space-detail') {
    S.authed = true;
    await __rc_nav('#/spaces/spA');
    __rc_assert(S.page === 'spaces', '深链 #/spaces/{id} 应落协作空间页，got ' + S.page);
    __rc_assert(S.spaceId === 'spA', '深链应携带空间 ID，got ' + S.spaceId);
    return 'PASS deep-link-space-detail';
  }
  if (scenario === 'deep-link-task-status-filter') {
    S.authed = true;
    await __rc_nav('#/tasks/running');
    __rc_assert(S.page === 'tasks', '深链 #/tasks/{status} 应落任务页，got ' + S.page);
    __rc_assert(S.f.status === 'running', '深链应携带状态筛选，got ' + S.f.status);
    return 'PASS deep-link-task-status-filter';
  }
  if (scenario === 'deep-link-group') {
    S.authed = true;
    await __rc_nav('#/groups/g1');
    __rc_assert(S.page === 'groups', '深链 #/groups/{id} 应落群聊页，got ' + S.page);
    __rc_assert(S.groupId === 'g1', '深链应携带群 ID，got ' + S.groupId);
    return 'PASS deep-link-group';
  }
  if (scenario === 'unknown-hash-falls-back') {
    S.authed = true;
    await __rc_nav('#/gone-page');
    __rc_assert(S.page === 'overview', '未知 hash 应回落总览（不 404 不白屏），got ' + S.page);
    const appHtml = __rc_getEl('#app').innerHTML;
    __rc_assert(typeof appHtml === 'string' && appHtml.length > 0, '回落后 #app 应有内容（白屏=回归）');
    return 'PASS unknown-hash-falls-back';
  }
  if (scenario === 'legacy-session-entry-jumps') {
    // W01-05 旧入口兼容跳转：700px 会话抽屉退役后，旧锚点/旧动作全部经
    // goSessionDetail 走 #/sessions/{id} 详情页（W08-03 字面兑现回归锚）。
    S.authed = true;
    goSessionDetail('sess-old');
    __rc_assert(globalThis.location.hash === '#/sessions/sess-old',
      'goSessionDetail 应置旧入口兼容 hash，got ' + globalThis.location.hash);
    await __rc_settle();
    for (const fn of (__rc_handlers['window:hashchange'] || [])) fn();
    await __rc_settle();
    __rc_assert(S.page === 'sessions' && S.sessId === 'sess-old',
      '旧入口 hash 经 hashchange 应落详情页，got page=' + S.page + ' id=' + S.sessId);
    return 'PASS legacy-session-entry-jumps';
  }
  if (scenario === 'unauthed-deep-link-hits-login') {
    S.authed = false;
    await __rc_nav('#/sessions/sess-9f');
    __rc_assert(S.page === 'login', '未登录深链应收敛登录页（不白屏），got ' + S.page);
    return 'PASS unauthed-deep-link-hits-login';
  }
  if (scenario === 'nav-renders-resource-entry') {
    // W08-03 导航词迁移：「机器」→「资源」（集成者裁决最小改名口径）——导航呈现
    // 新词、旧词退役；路由 data-page=nodes 与 #/nodes 深链存活（只改显示词）。
    S.authed = true;
    await __rc_nav('#/overview');
    const html = __rc_getEl('#app').innerHTML;
    __rc_assert(html.includes('data-page="nodes"'), '导航应保留 nodes 路由入口（data-page 不变）');
    __rc_assert(html.includes('<span class="navnum">02</span>资源</button>'),
      '导航第 02 位应呈现「资源」（W08-03 迁移后）');
    __rc_assert(!html.includes('<span class="navnum">02</span>机器</button>'),
      '导航不应再呈现旧词「机器」');
      // 全导航按钮级扫描（codex 复审建议：额外插入 03 位旧词按钮也须 FAIL——
      // 逐按钮绑定路由/序号/文字，整导航区旧词零呈现，非仅 02 位精确 HTML）。
      // 全导航按钮级扫描（codex 复审建议：额外插入 03 位旧词按钮也须 FAIL——
      // 任意 navnum 位旧词零呈现——正则扫 #app 全区按钮形，非仅 02 位精确 HTML）。
      const navBtnRe = new RegExp('<span class="navnum">\\d+</span>(机器)</button>');
      __rc_assert(!navBtnRe.test(html), '导航按钮残留旧词「机器」（任意位次）');
      const resBtnRe = new RegExp("<span class=\"navnum\">0?2</span>" + "资源</button>");
      __rc_assert(resBtnRe.test(html) && html.includes('data-page="nodes"'), '资源项应呈现序号 02 文字「资源」且绑定路由 nodes');
    await __rc_nav('#/nodes');
    __rc_assert(S.page === 'nodes', '#/nodes 深链应存活（路由词不迁移），got ' + S.page);
    return 'PASS nav-renders-resource-entry';
  }
  throw new Error('未知场景 ' + scenario);
}
