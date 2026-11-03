'use strict';
/* node 本机控制台 app.js 旧锚点/深链兼容行为断言 harness（W08-03 工作块2/5：旧
 * 页面/深链兼容回归锚）：执行方 web_route_compat_test.go 经 node 子进程调本文件
 * 专用 runner（与 hub web 包 consoleHarnessRunner 同构的「桩段 + APP.JS 插入标记 +
 * 驱动段」裸 vm 拼接求值；顶级标识符一律 __rc_ 前缀防撞名）。
 *
 * 场景判据（深链语义 = route() 按真实 hashchange 事件驱动的行为）：
 *   deep-link-session-detail —— #/sessions/{id} 直达会话详情页（W01-05 P02）；
 *   deep-link-space-detail —— #/spaces/{id} 直达空间详情页（W04-03 P04）；
 *   deep-link-mcp-subpage —— #/mcp 直达 MCP 接入子页（W03-04 P07，资源域）；
 *   unknown-hash-falls-back —— 未知 hash 回落总览（不 404 不白屏）；
 *   setup-mode-converges-wizard —— setup 形态深链收敛向导页（不越 setup 门）；
 *   legacy-task-page-migrate-card-jump —— T3「node 会话在任务页」迁移说明卡经
 *     真实点击委托跳转「会话」页（W08 任务书「旧锚点可跳转」字面兑现）；
 *   legacy-go-session-detail —— goSessionDetail 旧入口兼容跳转（旧锚点/动作全走
 *     详情页，W01-05 交付面回归锚）。
 */

/* ---------- 桩段（先于 app.js 求值；与 hub web 包行为 harness 同款骨架） ---------- */
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
globalThis.localStorage = { getItem() { return null; }, setItem() {}, removeItem() {} };
globalThis.navigator = { clipboard: { writeText: async () => {} } };
globalThis.location = { hash: '#/overview', host: 'node.test', protocol: 'https:' };
globalThis.fetch = (path, opts = {}) => {
  if (__rc_hook) return __rc_hook({ method: opts.method || 'GET', path: String(path), body: opts.body || null });
  return Promise.resolve({ status: 200, ok: true, json: async () => ({}) });
};
let __rc_hook = null;

/* ==== APP.JS INSERTED BELOW BY RUNNER ==== */

/* ---------- 驱动段（app.js 之后求值：S/route/tasksPage/goSessionDetail 直接可见） ---------- */
const __rc_settle = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)); };
/* __rc_auth 以组网形态置登录态（boot 时 localStorage 无令牌走令牌门，场景自置）。 */
function __rc_auth(mode) {
  S.authed = true;
  S.status = { mode: mode || 'networked', online: true, node_id: 'nodeA' };
}
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
    __rc_auth();
    await __rc_nav('#/sessions/sess-9f');
    __rc_assert(S.page === 'sessions', '深链 #/sessions/{id} 应落会话页，got ' + S.page);
    __rc_assert(S.sessId === 'sess-9f', '深链应携带会话 ID，got ' + S.sessId);
    return 'PASS deep-link-session-detail';
  }
  if (scenario === 'deep-link-space-detail') {
    __rc_auth();
    await __rc_nav('#/spaces/sp9');
    __rc_assert(S.page === 'spaces', '深链 #/spaces/{id} 应落协作空间页，got ' + S.page);
    __rc_assert(S.spaceId === 'sp9', '深链应携带空间 ID，got ' + S.spaceId);
    return 'PASS deep-link-space-detail';
  }
  if (scenario === 'deep-link-mcp-subpage') {
    __rc_auth();
    await __rc_nav('#/mcp');
    __rc_assert(S.page === 'mcp', '深链 #/mcp 应落 MCP 接入子页（资源域），got ' + S.page);
    return 'PASS deep-link-mcp-subpage';
  }
  if (scenario === 'unknown-hash-falls-back') {
    __rc_auth();
    await __rc_nav('#/gone-page');
    __rc_assert(S.page === 'overview', '未知 hash 应回落总览（不 404 不白屏），got ' + S.page);
    const appHtml = __rc_getEl('#app').innerHTML;
    __rc_assert(typeof appHtml === 'string' && appHtml.length > 0, '回落后 #app 应有内容（白屏=回归）');
    return 'PASS unknown-hash-falls-back';
  }
  if (scenario === 'setup-mode-converges-wizard') {
    __rc_auth('setup');
    await __rc_nav('#/tasks');
    __rc_assert(S.page === 'wizard', 'setup 形态深链应收敛向导页（不越 setup 门），got ' + S.page);
    return 'PASS setup-mode-converges-wizard';
  }
  if (scenario === 'legacy-task-page-migrate-card-jump') {
    // T3 旧「node 会话在任务页」：W01-05 迁出后任务页留迁移说明卡（旧锚点可跳转）。
    __rc_auth();
    const html = tasksPage();
    __rc_assert(html.includes('id="sess-card"'), '任务页应保留会话迁移说明卡（sess-card）');
    __rc_assert(html.includes('data-go="sessions"'), '迁移卡应带会话页跳转入口（data-go=sessions）');
    __rc_assert(html.includes('迁至独立'), '迁移卡应说明会话已迁至独立页');
    // 经真实点击委托驱动（document click 委托 data-go 分支本体，不复制跳转代码）：
    const fakeBtn = { dataset: { go: 'sessions' } };
    const ev = { target: { classList: { contains: () => false }, closest(sel) { return sel === 'button' ? fakeBtn : null; } } };
    for (const fn of (__rc_handlers.click || [])) fn(ev);
    __rc_assert(globalThis.location.hash === '#/sessions',
      '迁移卡点击应经委托置 #/sessions hash，got ' + globalThis.location.hash);
    return 'PASS legacy-task-page-migrate-card-jump';
  }
  if (scenario === 'legacy-go-session-detail') {
    // W01-05 旧入口兼容跳转：旧锚点/旧动作全部经 goSessionDetail 走详情页。
    __rc_auth();
    goSessionDetail('sess-old');
    __rc_assert(globalThis.location.hash === '#/sessions/sess-old',
      'goSessionDetail 应置旧入口兼容 hash，got ' + globalThis.location.hash);
    for (const fn of (__rc_handlers['window:hashchange'] || [])) fn();
    await __rc_settle();
    __rc_assert(S.page === 'sessions' && S.sessId === 'sess-old',
      '旧入口 hash 经 hashchange 应落详情页，got page=' + S.page + ' id=' + S.sessId);
    return 'PASS legacy-go-session-detail';
  }
  if (scenario === 'nav-renders-resource-entry') {
    // W08-03 导航词迁移：「服务」→「资源」（集成者裁决最小改名口径）——导航呈现
    // 新词、旧词退役；路由 data-go=services 与 #/services 深链存活（只改显示词）。
    __rc_auth();
    await __rc_nav('#/overview');
    const html = __rc_getEl('#app').innerHTML;
    __rc_assert(html.includes('<button data-go="services"'), '导航应保留 services 路由入口（data-go 不变）');
    __rc_assert(html.includes('<span class="navnum">02</span>资源</button>'),
      '导航第 02 位应呈现「资源」（W08-03 迁移后）');
    __rc_assert(!html.includes('<span class="navnum">02</span>服务</button>'),
      '导航不应再呈现旧词「服务」');
      // 全导航按钮级扫描（codex 复审建议：额外插入 03 位旧词按钮也须 FAIL——
      // 逐按钮绑定路由/序号/文字，整导航区旧词零呈现，非仅 02 位精确 HTML）。
      // 全导航按钮级扫描（codex 复审建议：额外插入 03 位旧词按钮也须 FAIL——
      // 任意 navnum 位旧词零呈现——正则扫 #app 全区按钮形，非仅 02 位精确 HTML）。
      const navBtnRe = new RegExp('<span class="navnum">\\d+</span>(服务)</button>');
      __rc_assert(!navBtnRe.test(html), '导航按钮残留旧词「服务」（任意位次）');
      const resBtnRe = new RegExp("<span class=\"navnum\">0?2</span>" + "资源</button>");
      __rc_assert(resBtnRe.test(html) && html.includes('data-go="services"'), '资源项应呈现序号 02 文字「资源」且绑定路由 services');
    await __rc_nav('#/services');
    __rc_assert(S.page === 'services', '#/services 深链应存活（路由词不迁移），got ' + S.page);
    return 'PASS nav-renders-resource-entry';
  }
  throw new Error('未知场景 ' + scenario);
}
