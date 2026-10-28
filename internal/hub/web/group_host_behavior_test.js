'use strict';
/* hub 控制台 app.js 群主持状态快照行为断言 harness（DESIGN-butler v4 审查修复③：
 * 停轮快照必须清掉前端旧在途状态）。执行方：TestConsoleGroupHostSnapshotReplace
 * （同目录 group_host_test.go）经 node 子进程调 dispatch_test.go 的共享 runner——
 * 本文件在裸 vm 上下文内先装桩 DOM，runner 把 app.js 全文插入下方标记处一并求值，
 * 随后逐场景驱动并要求逐条 PASS。
 *
 * 断言驱动的是真实代码路径：pullGroupMessages 的快照落地与分区 diff（paintGroupHost /
 * hostSig / hostStatusHtml 本体），桩 api 按场景返回群消息轮询应答（平级附群快照，
 * 生产 3s 轮询同形）。场景判据随 docs/TESTPLAN.md「DESIGN-butler v4 主持人接力棒」
 * 增补批次维护。
 *
 * 注意：本文件按「桩段 + APP.JS 插入标记 + 驱动段」被 runner 切分拼接，两段顶级
 * 标识符共用同一脚本作用域，一律 __gh_ 前缀防与 app.js / dispatch harness 撞名。 */

/* ---------- 桩段（先于 app.js 求值） ---------- */
const __gh_handlers = {};   // 事件类型 → 委托处理器
const __gh_els = new Map(); // 选择器 → 桩元素（缺失自动建）

function __gh_el(sel) {
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
function __gh_getEl(sel) { if (!__gh_els.has(sel)) __gh_els.set(sel, __gh_el(sel)); return __gh_els.get(sel); }

globalThis.document = {
  title: '', visibilityState: 'visible', activeElement: null, body: __gh_getEl('body'),
  querySelector(sel) { return __gh_getEl(sel); },
  querySelectorAll() { return []; },
  getElementById(id) { return __gh_getEl('#' + id); },
  createElement() { return __gh_el(''); },
  addEventListener(type, fn) { (__gh_handlers[type] = __gh_handlers[type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.window = {
  scrollTo() {}, scrollY: 0,
  addEventListener(type, fn) { (__gh_handlers['window:' + type] = __gh_handlers['window:' + type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.location = { hash: '#/control', host: 'hub.test', protocol: 'https:' };
let __gh_hook = null; // 场景注入的 fetch 路由：(rec) => Promise<Response> 形；空则默认 200 {}
globalThis.fetch = (path, opts = {}) => {
  const rec = { method: opts.method || 'GET', path: String(path), body: opts.body || null };
  if (__gh_hook) return __gh_hook(rec);
  return Promise.resolve({ status: 200, ok: true, json: async () => ({}) });
};

/* ==== APP.JS INSERTED BELOW BY RUNNER ==== */

/* ---------- 驱动段（app.js 之后求值：S/pullGroupMessages/hostSig 直接可见） ---------- */
const __gh_ok = json => ({ status: 200, ok: true, json: async () => json });
/* __gh_groupPollPoll 群消息轮询应答桩：平级附群快照（生产 GET messages 同形）。 */
function __gh_groupPoll(snapshot) {
  return rec => rec.path.startsWith('/api/v1/groups/')
    ? Promise.resolve(__gh_ok({ group: snapshot, messages: [], pruned_before_seq: 0 }))
    : Promise.resolve(__gh_ok({}));
}
/* __gh_reset 回到「已登录 · 群详情 · 在途第一棒」基线：本地群对象带旧在途状态。 */
function __gh_reset() {
  S.authed = true; S.page = 'groups'; S.groupId = 'g-1'; S.drawer = null;
  S.groups = [{
    group_id: 'g-1', goal: '巡检协作', status: 'discussing', host: 'admin',
    members: ['n1/butler'], budget: { max_rounds: 20 }, created_at: '2026-10-28T00:00:00Z',
    host_active: true, host_round: 1, host_turn: 't1', host_turn_member: 'n1/butler',
  }];
  S.groupView = { id: 'g-1', lastSeq: 0, msgs: [], unread: 0 };
  __gh_getEl('#group-host').innerHTML = '';
  __gh_getEl('#group-host').dataset.hostSig = '';
  __gh_hook = null;
}
function __gh_assert(cond, msg) { if (!cond) throw new Error('断言失败：' + msg); }

/* 场景一 stopped-snapshot-clears-turn 运行→停轮（codex ui-repro 等效固化，v4 审查
 * 修复③）：服务端停轮清空 HostTurn，omitempty 省略 host_turn/host_turn_member——
 * 快照落地必须整对象替换而非合并，旧在途状态（任务 ID + 成员）不得残留，主持状态
 * 分区随签名 diff 转停轮呈现。旧 {...旧,...新} 合并行为下 host_turn 残留，本场景
 * 的两条宿主断言转红。 */
async function __gh_scn_stopped_snapshot_clears_turn() {
  __gh_reset();
  __gh_assert(hostSig(S.groups[0]) === 'run:1:n1/butler', '前置：基线为第一棒在途态');
  __gh_hook = __gh_groupPoll({   // 停轮群完整快照：无 host_turn / host_turn_member
    group_id: 'g-1', goal: '巡检协作', status: 'discussing', host: 'admin',
    members: ['n1/butler'], budget: { max_rounds: 20 }, created_at: '2026-10-28T00:00:00Z',
    host_active: true, host_round: 1,
  });
  await pullGroupMessages();
  const g = S.groups.find(x => x.group_id === 'g-1');
  __gh_assert(!g.host_turn, '停轮快照必须清掉旧 host_turn（合并会残留任务 ID）');
  __gh_assert(!g.host_turn_member, '停轮快照必须清掉旧 host_turn_member');
  __gh_assert(g.host_active === true && g.host_round === 1, '停轮态其余快照字段照常落地');
  __gh_assert(hostSig(g) === 'stop:1:', 'hostSig 应转停轮态 stop:1:，实得 ' + hostSig(g));
  const zone = __gh_getEl('#group-host');
  __gh_assert(zone.dataset.hostSig === 'stop:1:', '分区签名应同步为停轮态');
  __gh_assert(zone.innerHTML.includes('已停轮'), '主持状态分区应呈现「已停轮」，实得：' + zone.innerHTML);
  // 3s 轮询重复到达同一停轮快照：状态稳定不回弹
  await pullGroupMessages();
  __gh_assert(!S.groups.find(x => x.group_id === 'g-1').host_turn, '重复轮询不得让旧在途状态回弹');
}

/* 场景二 member-lookup-missing-degrades 有任务 ID 但成员查不到（v4 审查修复③配套
 * 状态）：HostTurn 在途但任务账本查不到目标（任务清理/派发窗口）→ 快照省略
 * host_turn_member，前端按「第 N 轮」降级呈现，不得虚构成员、不得丢失在途任务 ID。 */
async function __gh_scn_member_lookup_missing_degrades() {
  __gh_reset();
  S.groups[0].host_turn = ''; S.groups[0].host_turn_member = ''; S.groups[0].host_round = 0;
  __gh_hook = __gh_groupPoll({   // 在途第二棒：host_turn 在、host_turn_member 查不到（省略）
    group_id: 'g-1', goal: '巡检协作', status: 'discussing', host: 'admin',
    members: ['n1/butler'], budget: { max_rounds: 20 }, created_at: '2026-10-28T00:00:00Z',
    host_active: true, host_round: 2, host_turn: 't2',
  });
  await pullGroupMessages();
  const g = S.groups.find(x => x.group_id === 'g-1');
  __gh_assert(g.host_turn === 't2', '在途棒任务 ID 应落地');
  __gh_assert(!g.host_turn_member, '成员查不到时该字段应留空（omitempty 省略）');
  __gh_assert(hostSig(g) === 'run:2:', '签名应为在途降级形态 run:2:，实得 ' + hostSig(g));
  const zone = __gh_getEl('#group-host');
  __gh_assert(zone.innerHTML.includes('第 2 轮'), '应呈现「第 2 轮」，实得：' + zone.innerHTML);
  __gh_assert(!zone.innerHTML.includes('当前'), '任务账本查不到成员不得虚构「当前」成员：' + zone.innerHTML);
}

/* 场景注册：Go 侧逐场景调用并要求逐条 PASS（断言抛错 → node 非零退出）。 */
globalThis.__hs_run = async name => {
  const scn = {
    'stopped-snapshot-clears-turn': __gh_scn_stopped_snapshot_clears_turn,
    'member-lookup-missing-degrades': __gh_scn_member_lookup_missing_degrades,
  }[name];
  if (!scn) throw new Error('未知场景 ' + name);
  await scn();
  return 'PASS ' + name;
};
