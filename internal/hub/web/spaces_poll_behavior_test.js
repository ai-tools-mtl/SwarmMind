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
const __sp_calls = [];   // 请求留痕 {method, path}（消息游标等请求形断言用）
globalThis.fetch = (path, opts = {}) => {
  const rec = { method: opts.method || 'GET', path: String(path), body: opts.body || null };
  __sp_calls.push(rec);
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
/* __sp_spacesQueueHook 列表端点手动队列（logout-pending 场景：GET /api/v1/spaces
 * 挂起等手动放行——跨登出挂起旧请求的最小构造）；其余路径即时 ok。 */
function __sp_spacesQueueHook() {
  __sp_hook = rec => {
    if (rec.method === 'GET' && rec.path === '/api/v1/spaces') {
      let resolve;
      const p = new Promise(r => { resolve = r; });
      __sp_waiters.push({ path: rec.path, resolve });
      return p.then(body => __sp_ok(body));
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
function __sp_respond_path(path, nth, body) {
  const hits = [];
  for (let i = 0; i < __sp_waiters.length; i++) {
    if (__sp_waiters[i].path === path) hits.push(i);
  }
  if (nth < 0 || nth >= hits.length) throw new Error('路径应答位越界 ' + nth + '（' + path + ' 在队=' + hits.length + '）');
  return __sp_respond_at(hits[nth], body);
}
function __sp_respond_at(i, body) {
  if (i < 0 || i >= __sp_waiters.length) throw new Error('应答位越界 ' + i + '（在队=' + __sp_waiters.length + '）');
  const [w] = __sp_waiters.splice(i, 1);   // splice 返回被移除元素的单元素数组
  w.resolve(body);
}
const __sp_detail = (id, version) => ({ space: __sp_sp(id, '目标 ' + id, { version }), scope: 'hub:' + id });
/* __sp_mountSpaces 列表页基线：登录态 + 进协作空间页 + 真实 render + 槽位首绘。
 * resetTransport=true 清传输态表（seq/resSnap——模拟全新启动基线）；false 保留
 * 现状（重登场景用：退出清理是否真实生效，不得被测试侧重置掩盖）。 */
function __sp_mountSpaces(resetTransport = true) {
  S.authed = true; S.page = 'spaces'; S.spaceId = ''; S.groupId = ''; S.sessId = '';
  S.spaces = []; S.spacesLoaded = false; S.spacesUnsupported = false; S.spaceFilter = '';
  S.spacePage = null; S.spacesError = ''; S.fresh = {};
  if (resetTransport) {
    S.seq = { nodes: 0, tasks: 0, directory: 0, prime: 0, works: 0, sessions: 0, spaces: 0 };
    S.resSnap = { nodes: '', tasks: '', directory: '', prime: '', works: '', sessions: '', spaces: '' };
  }
  __sp_getEl('#app').innerHTML = spacesPage();
  paintSpacesView();
}
/* __sp_clickLogout 走真实登出路径（bindEvents 委托：closest('button') 命中
 * id=logout 分支）——清理逻辑本体被驱动，不复制清理代码。 */
function __sp_clickLogout() {
  const btn = { id: 'logout', dataset: {}, disabled: false, closest(sel) { return sel === 'button' ? btn : { id: '' }; } };
  __sp_handlers.click.forEach(fn => fn({ target: btn }));
}
async function __hs_run(scenario) {
  if (scenario === 'poll-picks-up-remote-change') {
    // 建议修B：经真实 pollTick 驱动（含 spaces 资源轮询接线与槽位重绘全程），
    // 不再手动调 loadAll/paintSpacesView。
    __sp_mountSpaces();
    __sp_spacesHook();
    __sp_payloads = [__sp_sp('sp1', '他端可见目标一')];
    await pollTick();
    if (!__sp_list('space-list-zone').includes('他端可见目标一')) throw new Error('首轮轮询后列表未呈现空间');
    if (!__sp_list('space-head-zone').includes('新建空间')) throw new Error('装配在位且加载成功后页头缺创建入口');

    // 另一客户端归档 sp1（本端只被动轮询，不调任何刷新动作）：
    __sp_payloads = [__sp_sp('sp1', '他端可见目标一', { archived: true })];
    await pollTick();
    if (!__sp_list('space-chips-zone').includes('已归档 (1)')) throw new Error('他端归档后 chips 计数未更新');
    if (!__sp_list('space-list-zone').includes('没有符合筛选的空间')) throw new Error('他端归档后活跃列表未更新');

    // 另一客户端新建 sp2：
    __sp_payloads = [
      __sp_sp('sp1', '他端可见目标一', { archived: true }),
      __sp_sp('sp2', '他端新建目标二'),
    ];
    await pollTick();
    if (!__sp_list('space-list-zone').includes('他端新建目标二')) throw new Error('他端新建后列表未更新');
    return 'PASS poll-picks-up-remote-change';
  }
  if (scenario === 'non200-same-shape-not-committed') {
    // 必修1 复审回归：非 200 同形响应一律不得提交（七资源全数）——500 携同形
    // JSON 载荷，validate 形面全过，唯独 200 门拦截。
    __sp_mountSpaces();
    __sp_hook = rec => {
      if (rec.method !== 'GET') return __sp_ok({});
      const sameShape = {
        '/api/v1/nodes': { nodes: [{ node_id: 'x', online: true, epoch: 1, inflight: 0, mailbox_used: 0 }] },
        '/api/v1/tasks': { tasks: [{ id: 't-x', created_at: '2026-11-03T00:00:00Z' }] },
        '/api/v1/directory': { directory: [{ node_id: 'x', card: { name: 'x' } }] },
        '/api/v1/prime': { designation: null },
        '/api/v1/works': { works: [{ id: 'w-x', created_at: '2026-11-03T00:00:00Z' }] },
        '/api/v1/sessions': { sessions: [{ id: 's-x' }] },
        '/api/v1/spaces': { spaces: [__sp_sp('sp-x', '假绿空间')] },
      };
      const body = sameShape[rec.path];
      if (body) return { status: 500, ok: false, json: async () => body };
      return __sp_ok({});
    };
    await pollTick();
    const notOk = Object.entries(S.fresh).filter(([, v]) => v && v.ok === true);
    if (notOk.length) throw new Error('非 200 同形响应被提交为成功: ' + notOk.map(([k]) => k).join(','));
    if (S.tasks.length || S.works.length || S.sessions.length || S.nodes.length || S.directory.length || S.spaces.length) {
      throw new Error('非 200 同形响应落进了资源状态');
    }
    if (S.spacesLoaded) throw new Error('spaces 不得在非 200 应答上置已加载');
    if (__sp_list('space-head-zone').includes('新建空间')) throw new Error('失败态页头不得出现创建入口（假绿）');
    if (!__sp_list('space-list-zone').includes('加载失败')) throw new Error('失败态列表未呈现失败框');
    return 'PASS non200-same-shape-not-committed';
  }
  if (scenario === 'relogin-rebuilds-state') {
    // 必修2 复审回归：会话清理必须同步失效指纹/seq——重登后相同应答可重建状态
    // 与入口；503→重登→503 不得假亮入口。
    __sp_mountSpaces();
    __sp_spacesHook();
    __sp_payloads = [__sp_sp('sp1', '重登可见目标')];
    await pollTick();
    if (!__sp_list('space-list-zone').includes('重登可见目标')) throw new Error('基线列表未加载');
    // 真实登出（走委托处理器 logout 分支本体）：
    __sp_clickLogout();
    await __sp_settle();
    if (S.authed !== false) throw new Error('登出未生效');
    if (S.spaces.length || S.resSnap.spaces !== '') {
      throw new Error('登出未同步清理空间状态/指纹');
    }
    if (S.seq.spaces === 0) {
      throw new Error('seq 被归零=序号复用窗（codex 终审必修：旧会话挂起请求 seq 与新会话匹配 → OLD 覆盖 NEW）');
    }
    // 重登（真实传输表残留原样保留——mountSpaces 不重置）：相同列表应答必须重建。
    S.authed = true;
    __sp_mountSpaces(false);
    await pollTick();
    if (!__sp_list('space-list-zone').includes('重登可见目标')) throw new Error('重登后相同应答未重建列表（指纹残留门控假死）');
    if (!__sp_list('space-head-zone').includes('新建空间')) throw new Error('重登后页头创建入口未点亮');
    // 503 → 真实登出 → 重登 → 503：专态重建、入口不假亮。
    __sp_clickLogout();
    await __sp_settle();
    S.authed = true;
    __sp_mountSpaces(false);
    __sp_hook = rec => {
      if (rec.method === 'GET' && rec.path === '/api/v1/spaces') return { status: 503, ok: false, json: async () => ({ error: 'spaces_unavailable', message: 'x' }) };
      return __sp_ok({});
    };
    await pollTick();
    if (!S.spacesUnsupported) throw new Error('重登后 503 专态未重建');
    if (__sp_list('space-head-zone').includes('新建空间')) throw new Error('未装配态页头假亮创建入口');
    if (!__sp_list('space-list-zone').includes(SPACE_HUB_OFF)) throw new Error('未装配态列表未呈现专态空态');
    return 'PASS relogin-rebuilds-state';
  }
  if (scenario === 'message-cursor-advances') {
    // 消息游标：since=已见最大 seq 只取增量；滚动窗口与去重随 fetchSpacePage 本体。
    S.authed = true; S.page = 'spaces';
    __sp_calls.length = 0;
    const msgPages = [];
    __sp_hook = rec => {
      if (rec.method === 'GET' && rec.path.startsWith('/api/v1/spaces/spA/messages')) return __sp_ok(msgPages.shift() || { messages: [], pruned_before_seq: 0 });
      if (rec.method === 'GET' && rec.path === '/api/v1/spaces/spA') return __sp_ok(__sp_detail('spA', 1));
      if (rec.path.includes('/strategy')) return __sp_ok({ strategy: { has_loop: false } });
      return __sp_ok({});
    };
    S.spaceId = 'spA';
    S.spacePage = { id: 'spA', space: null, scope: '', zones: {}, msgs: [], lastSeq: 0, prunedSeq: 0, msgReqId: '' };
    await fetchSpacePage(); await __sp_settle();
    if (S.spacePage.lastSeq !== 0 || S.spacePage.msgs.length !== 0) throw new Error('空页基线不干净');
    // since 游标语义=请求时已见最大 seq：第二轮请求携 since=0（首轮后游标仍 0），
    // 并入 seq1 后游标=1——第三轮请求必须携 since=1（游标推进的断言点）。
    msgPages.push({ messages: [{ seq: 1, member: 'fake-coder', by: 'admin', text: '第一条', created_at: '2026-11-03T00:00:00Z' }], pruned_before_seq: 0 });
    await fetchSpacePage(); await __sp_settle();
    if (S.spacePage.msgs.length !== 1 || S.spacePage.lastSeq !== 1) throw new Error('增量消息未并入');
    const msgCalls = () => __sp_calls.filter(c => c.path.includes('/messages'));
    if (!msgCalls()[1].path.includes('since=0')) {
      throw new Error('第二轮游标不符（应 since=0）: ' + msgCalls()[1].path);
    }
    msgPages.push({ messages: [{ seq: 2, member: '', by: 'admin', text: '第二条', created_at: '2026-11-03T00:00:01Z' }], pruned_before_seq: 0 });
    await fetchSpacePage(); await __sp_settle();
    if (!msgCalls()[2].path.includes('since=1')) {
      throw new Error('第三轮取数未携 since=1 游标: ' + msgCalls()[2].path);
    }
    if (S.spacePage.msgs.length !== 2 || S.spacePage.lastSeq !== 2) throw new Error('第二条增量未并入');
    if (S.spacePage.msgs[0].seq !== 1 || S.spacePage.msgs[1].seq !== 2) throw new Error('消息序失真');
    return 'PASS message-cursor-advances';
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

  if (scenario === 'logout-pending-old-request') {
    // codex 终审必修全程：旧会话列表请求挂起（手动队列不应答）→ 真实登出
    //（状态/指纹清、seq 保持单调不归零）→ 重登 → 新请求先提交 NEW → 旧请求
    // 应答后到 → seq 单调不匹配 → NEW 不被 OLD 覆盖。
    __sp_mountSpaces();
    __sp_spacesQueueHook();            // 列表请求挂起队列（手动放行）
    const rOld = loadAll();            // 旧会话请求挂起（队列位 0）
    await __sp_settle();
    __sp_clickLogout();                // 真实登出：状态/指纹清、seq 单调保留
    await __sp_settle();
    if (S.authed !== false) throw new Error('登出未生效');
    if (S.resSnap.spaces !== '') throw new Error('指纹未清');
    if (S.seq.spaces === 0) throw new Error('seq 被归零=序号复用窗');
    // 重登：真实传输表残留（旧请求仍挂起），新请求队列位 1。
    S.authed = true;
    __sp_mountSpaces(false);
    const rNew = loadAll();            // 新会话请求挂起（队列位 1）
    await __sp_settle();
    __sp_respond_path('/api/v1/spaces', 1, { spaces: [__sp_sp('sp1', '新会话目标')], total: 1 });  // 新请求先返
    await rNew; await __sp_settle();    paintSpacesView();   // 本场景不经 pollTick，重绘手动触发（数据断言与重绘分离）
    if (!__sp_list('space-list-zone').includes('新会话目标')) throw new Error('新会话 NEW 未提交');
    __sp_respond_path('/api/v1/spaces', 0, { spaces: [__sp_sp('sp1', '旧会话目标')], total: 1 });  // 旧请求后到
    await rOld; await __sp_settle();
    paintSpacesView();
    if (!__sp_list('space-list-zone').includes('新会话目标')) throw new Error('旧请求覆盖了新会话列表（OLD 覆盖 NEW——序号复用窗未闭）');
    if (__sp_list('space-list-zone').includes('旧会话目标')) throw new Error('列表含旧会话数据');
    return 'PASS logout-pending-old-request';
  }
  throw new Error('未知场景 ' + scenario);
}
