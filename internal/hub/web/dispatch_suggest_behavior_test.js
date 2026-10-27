'use strict';
/* hub 控制台 app.js 派发建议区行为断言 harness（codex 审 D1 ②复审：源码锚点检查
 * 升级为真代码行为验证）。执行方：TestConsoleDispatchSuggestFingerprint（同目录
 * dispatch_test.go）经 node 子进程调 runner——本文件在裸 vm 上下文内先装桩 DOM，
 * runner 把 app.js 全文插入下方标记处一并求值，随后逐场景驱动并要求逐条 PASS。
 *
 * 断言驱动的是真实代码路径：全局 click/input 委托处理器（bindEvents 捕获）、
 * ACT.ctrl-suggest / ACT.ctrl-adopt 动作本体、suggestLineHtml/paintSuggestLine
 * 渲染本体、api() 真形（fetch 桩留痕）。app.js 顶层词法绑定（S/ACT/…）与驱动
 * 同脚本求值，直接可见。场景判据随 docs/TESTPLAN-D1-jev.md 复审闭合节维护。
 *
 * 注意：本文件按「桩段 + APP.JS 插入标记 + 驱动段」被 runner 切分拼接，两段
 * 顶级标识符共用同一脚本作用域，一律 __hs_ 前缀防与 app.js 撞名。 */

/* ---------- 桩段（先于 app.js 求值） ---------- */
const __hs_handlers = {};   // 事件类型 → 委托处理器
const __hs_els = new Map(); // 选择器 → 桩元素（缺失自动建）
const __hs_calls = [];      // fetch 留痕 {method, path, body}
const __hs_pending = [];    // 场景手动放行的挂起 fetch（__hs_defer 登记）{resolve}
let __hs_hook = null;       // 场景注入的 fetch 路由：(rec) => Response 形；空则默认 200 {}

function __hs_el(sel) {
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
function __hs_getEl(sel) { if (!__hs_els.has(sel)) __hs_els.set(sel, __hs_el(sel)); return __hs_els.get(sel); }

globalThis.document = {
  title: '', visibilityState: 'visible', activeElement: null, body: __hs_getEl('body'),
  querySelector(sel) { return __hs_getEl(sel); },
  querySelectorAll() { return []; },
  getElementById(id) { return __hs_getEl('#' + id); },
  createElement() { return __hs_el(''); },
  addEventListener(type, fn) { (__hs_handlers[type] = __hs_handlers[type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.window = {
  scrollTo() {}, scrollY: 0,
  addEventListener(type, fn) { (__hs_handlers['window:' + type] = __hs_handlers['window:' + type] || []).push(fn); },
  removeEventListener() {},
};
globalThis.location = { hash: '#/control', host: 'hub.test', protocol: 'https:' };
globalThis.fetch = (path, opts = {}) => {
  const rec = { method: opts.method || 'GET', path: String(path), body: opts.body || null };
  __hs_calls.push(rec);
  if (__hs_hook) return __hs_hook(rec);
  return Promise.resolve({ status: 200, ok: true, json: async () => ({}) });
};

/* ==== APP.JS INSERTED BELOW BY RUNNER ==== */

/* ---------- 驱动段（app.js 之后求值：S/ACT/paintSuggestLine 直接可见） ---------- */
/* __hs_click(act)：合成 [data-act] 按钮点击事件，走真实全局 click 委托。 */
function __hs_click(act) {
  const btn = { dataset: { act }, disabled: false, closest(sel) { return sel === '[data-act]' ? btn : { id: '' }; } };
  __hs_handlers.click.forEach(fn => fn({ target: btn }));
}
/* __hs_type(value)：模拟用户输入——先写 DOM 再派发真实 input 委托（#ctrl-stdin）。 */
function __hs_type(value) {
  const ta = __hs_getEl('#ctrl-stdin');
  ta.value = value;
  __hs_handlers.input.forEach(fn => fn({ target: ta }));
}
const __hs_line = () => __hs_getEl('#ctrl-suggest-line').innerHTML;
const __hs_toast = () => __hs_getEl('#toast').textContent;
/* __hs_settle：让在途 api() 微观任务链（fetch 桩应答 → 应答门 → 渲染）落地。 */
const __hs_settle = async () => { for (let i = 0; i < 4; i++) await new Promise(r => setTimeout(r, 0)); };
/* __hs_defer：建议请求挂起、场景手动放行应答体。 */
function __hs_defer() {
  let resolve;
  const p = new Promise(r => { resolve = r; });
  __hs_pending.push({ resolve });
  return p.then(body => ({ status: 200, ok: true, json: async () => body }));
}
const __hs_ok = json => ({ status: 200, ok: true, json: async () => json });
const __hs_sugg = (agent, node, reason) => ({ status: 200, ok: true, json: async () => ({
  suggestion: { target_agent: agent, target_node: node }, confidence: 0.9,
  probabilities: {}, provider: 'llm-as-jev', degraded: false, note: '', reason: reason || '描述命中能力',
}) });
/* __hs_reset：回到「已登录 · 派单页 · 空表单 · 双候选目录」基线。 */
function __hs_reset() {
  S.authed = true; S.page = 'control'; S.drawer = null;
  S.directory = [
    { node_id: 'n1', online: true, inflight: 0, card: { name: 'translator', description: '中英互译', kind: 'agent', exposed: true } },
    { node_id: 'n1', online: true, inflight: 0, card: { name: 'coder', description: '编码修复', kind: 'agent', exposed: true } },
  ];
  S.C = freshControl();
  __hs_getEl('#ctrl-stdin').value = '';
  __hs_getEl('#ctrl-args').value = '';
  __hs_getEl('#ctrl-suggest-line').innerHTML = '';
  __hs_calls.length = 0; __hs_pending.length = 0; __hs_hook = null;
}
/* __hs_reset_args 参数输入区场景基线（maturity-dispatch-args-ux）：目录含三类能力卡
 * ——echo（allow_args=true）、report（无 allow_args 字段=旧卡 false 语义，stdin_to_arg=
 * true 验证转换提示独立于授权）、tool（allow_args=true+stdin_to_arg=true）、
 * zcode-dev（统筹载体，名字约定回退判定）。 */
function __hs_reset_args() {
  __hs_reset();
  S.directory = [
    { node_id: 'n1', online: true, inflight: 0, card: { name: 'echo', description: '回声测试', kind: 'cli', exposed: true, allow_args: true } },
    { node_id: 'n1', online: true, inflight: 0, card: { name: 'report', description: '报告生成', kind: 'cli', exposed: true, stdin_to_arg: true } },
    { node_id: 'n2', online: true, inflight: 0, card: { name: 'tool', description: '工具型', kind: 'cli', exposed: true, allow_args: true, stdin_to_arg: true } },
    { node_id: 'n1', online: true, inflight: 0, card: { name: 'zcode-dev', description: '统筹载体', kind: 'cli', exposed: true, allow_args: true } },
  ];
}
/* __hs_pickAgent 派发 change 事件选执行者：target 同时携带 id 与 data-picker 两形态
 *（真实 select 两者的并有），一次派发同时驱动 ctrl-agent 专属分支与 bindAgentPicker
 * 联动（后者经 onChange 持久化 S.C.node/agent）。 */
function __hs_pickAgent(node, agent) {
  __hs_getEl('#ctrl-node').value = node;
  const t = __hs_getEl('#ctrl-agent');
  t.value = agent;
  t.dataset = { picker: 'agent', pickerPrefix: 'ctrl' };
  __hs_handlers.change.forEach(fn => fn({ target: t }));
}
/* __hs_typeArgs 参数区输入：写 DOM 后派发真实 input 委托。 */
function __hs_typeArgs(value) {
  const ta = __hs_getEl('#ctrl-args');
  ta.value = value;
  __hs_handlers.input.forEach(fn => fn({ target: ta }));
}
/* __hs_submitForm 派发表单 submit 委托（submitControl 读两级下拉/描述/参数区真形）。 */
function __hs_submitForm() {
  __hs_handlers.submit.forEach(fn => fn({ target: { id: 'control-form' }, preventDefault() {} }));
}
function __hs_assert(cond, msg) { if (!cond) throw new Error('断言失败：' + msg + '；建议区现状：' + __hs_line().slice(0, 200)); }
/* __hs_noTypelessButton：渲染产物里零缺 type 属性的 <button>——表单内缺省 submit，
 * 「点击建议连带整表提交」实 bug（D 组走查 2026-10-26）的行为级回归判据。 */
function __hs_noTypelessButton(html, where) {
  const m = html.match(/<button\b(?![^>]*\btype=)[^>]*>/);
  __hs_assert(!m, where + ' 出现缺 type 属性的按钮：' + m);
}

/* 场景一 stale-input 输入失效（复现序列：翻译→改编译失败→旧采纳按钮不残留）：
 * 有建议在场时描述一改，采纳按钮必须当场消失、就地转失效态；建议销毁不复活。 */
async function __hs_scn_stale_input() {
  __hs_reset();
  __hs_type('把 docs 目录下的 README.md 翻译成英文');
  __hs_hook = rec => rec.path === '/api/v1/dispatch-suggest' ? __hs_defer() : Promise.resolve(__hs_ok({}));
  __hs_click('ctrl-suggest');
  __hs_assert(__hs_line().includes('决策模型思考中'), '请求期应显示思考中');
  __hs_assert(!__hs_line().includes('ctrl-adopt'), '思考中不得出现采纳按钮');
  __hs_pending[0].resolve({ suggestion: { target_agent: 'translator', target_node: 'n1' }, confidence: 0.9, probabilities: {}, provider: 'llm-as-jev', degraded: false, note: '', reason: '描述命中翻译能力' });
  await __hs_settle();
  __hs_assert(__hs_line().includes('data-act="ctrl-adopt"') && __hs_line().includes('建议派给 translator @ n1'), '有建议应出现采纳按钮');
  __hs_type('编译失败：修复 module.go 的空指针，跑通 go test');   // 改编译失败描述
  __hs_assert(!__hs_line().includes('ctrl-adopt'), '描述变化后旧采纳按钮必须立即消失（不等下一次整页渲染）');
  __hs_assert(__hs_line().includes('任务描述已修改'), '应就地显示失效态文案');
  __hs_assert(S.C.suggest.sugg === null && S.C.suggest.goal === '', '失效即销毁建议（改回原文不复活）');
  paintSuggestLine();
  __hs_assert(!__hs_line().includes('ctrl-adopt'), '再次局部重绘旧按钮不残留');
}

/* 场景二 late-response 迟到响应：思考中改描述 → 应答后到必须整轮作废
 *（建议与降级说明都不落地），采纳点击亦不得回填。 */
async function __hs_scn_late_response() {
  __hs_reset();
  __hs_type('翻译任务：README.md 中译英');
  __hs_hook = rec => rec.path === '/api/v1/dispatch-suggest' ? __hs_defer() : Promise.resolve(__hs_ok({}));
  __hs_click('ctrl-suggest');
  __hs_assert(__hs_pending.length === 1, '建议请求应在途');
  __hs_type('需求已变：改为巡检各机磁盘占用');
  __hs_assert(__hs_line().includes('决策模型思考中'), '思考中改描述不误切失效态（失效判定在应答门）');
  __hs_pending[0].resolve({ suggestion: { target_agent: 'translator', target_node: 'n1' }, confidence: 0.9, probabilities: {}, provider: 'llm-as-jev', degraded: false, note: '', reason: 'x' });
  await __hs_settle();
  __hs_assert(S.C.suggest.sugg === null && S.C.suggest.note === SUGGEST_STALE_NOTE, '迟到应答应整轮作废并转失效说明');
  __hs_assert(!__hs_line().includes('ctrl-adopt'), '迟到建议不得出现采纳按钮');
  __hs_click('ctrl-adopt');   // 防御纵深：即便被点也不得回填
  __hs_assert(S.C.node === '' && S.C.agent === '', '迟到建议不得回填两级下拉');
  __hs_assert(S.C.suggest.note === SUGGEST_STALE_NOTE, '采纳点击后仍维持失效说明');
}

/* 场景三 adopt-reject 点击拒绝：绕过 input 委托直改 DOM（程序化赋值，C.stdin
 * 镜像不知情）——采纳前重验以 DOM 真相为准，指纹不符拒绝回填并转失效。 */
async function __hs_scn_adopt_reject() {
  __hs_reset();
  __hs_type('翻译任务：README.md 中译英');
  __hs_hook = rec => rec.path === '/api/v1/dispatch-suggest' ? Promise.resolve(__hs_sugg('translator', 'n1', '翻译能力直配')) : Promise.resolve(__hs_ok({}));
  __hs_click('ctrl-suggest');
  await __hs_settle();
  __hs_assert(__hs_line().includes('ctrl-adopt'), '建议应在场');
  __hs_getEl('#ctrl-stdin').value = '换成巡检任务';   // 不派发 input 事件
  __hs_click('ctrl-adopt');
  __hs_assert(S.C.node === '' && S.C.agent === '', '指纹不符的采纳点击不得回填');
  __hs_assert(S.C.suggest.sugg === null && S.C.suggest.note === SUGGEST_STALE_NOTE, '拒绝后建议销毁转失效说明');
  __hs_assert(__hs_toast().includes('任务描述已修改'), '拒绝时应有人话 toast');
  __hs_assert(!__hs_line().includes('ctrl-adopt'), '拒绝后采纳按钮消失');
  paintSuggestLine();
  __hs_assert(!__hs_line().includes('ctrl-adopt'), '建议已销毁不因重绘复活');
}

/* 场景四 button-no-submit 按钮不触发表单提交：五态（入口/思考中/有建议/已失效/
 * 降级说明）渲染产物逐态检查零缺 type 按钮，且全程 fetch 留痕无 control-operations
 * 调用（表单提交端点）——建议动作零表单提交副作用。 */
async function __hs_scn_button_no_submit() {
  __hs_reset();
  paintSuggestLine();
  const snaps = [['入口', __hs_line(), true]];
  __hs_click('ctrl-suggest');   // 空描述：只 toast 指引，零网络调用
  await __hs_settle();
  __hs_assert(__hs_calls.length === 0, '空描述点击建议不得发起任何请求（含表单提交）');
  __hs_assert(__hs_toast().includes('请先填写任务描述'), '空描述点击应 toast 指引');
  snaps.push(['入口(空描述点击后)', __hs_line(), true]);
  __hs_type('翻译任务');
  __hs_hook = rec => rec.path === '/api/v1/dispatch-suggest' ? __hs_defer() : Promise.resolve(__hs_ok({}));
  __hs_click('ctrl-suggest');
  snaps.push(['思考中', __hs_line(), false]);
  __hs_pending[0].resolve({ suggestion: { target_agent: 'translator', target_node: 'n1' }, confidence: 0.9, probabilities: {}, provider: 'llm-as-jev', degraded: false, note: '', reason: 'x' });
  await __hs_settle();
  snaps.push(['有建议', __hs_line(), true]);
  __hs_type('改为编译任务');
  snaps.push(['已失效', __hs_line(), true]);
  __hs_hook = rec => rec.path === '/api/v1/dispatch-suggest'
    ? Promise.resolve(__hs_ok({ suggestion: null, degraded: true, note: '决策模型暂不可用：可直接手动派单' }))
    : Promise.resolve(__hs_ok({}));
  __hs_click('ctrl-suggest');
  await __hs_settle();
  __hs_assert(__hs_line().includes('决策模型暂不可用'), '降级应人话转述');
  snaps.push(['降级', __hs_line(), true]);
  for (const [name, html, expectBtn] of snaps) {
    if (expectBtn) __hs_assert(html.includes('<button'), name + ' 态应有按钮在场');
    __hs_noTypelessButton(html, name);
  }
  __hs_assert(snaps[3][1].includes('<button type="button" data-act="ctrl-adopt"'), '采纳按钮须 type="button"');
  for (const c of __hs_calls) {
    __hs_assert(c.path !== '/api/v1/control-operations', '建议动作不得触发表单提交端点：' + c.method + ' ' + c.path);
    if (c.path === '/api/v1/dispatch-suggest') __hs_assert(c.method === 'POST', '建议调用须 POST');
  }
}

/* 场景五 args-zone-modes 参数输入区三态呈现与局部更新（maturity-dispatch-args-ux）：
 * allow_args=true 出参数输入区；false/旧卡无字段出说明文案；stdin_to_arg=true 出
 * 转换提示（独立于参数授权）；切换执行者只重写 #ctrl-args-zone（任务描述占位同步
 * 两态切换），参数草稿跨切换保留；能力卡不可得回退空区不阻塞；统筹载体不出现参数区。 */
async function __hs_scn_args_zone_modes() {
  __hs_reset_args();
  const zone = () => __hs_getEl('#ctrl-args-zone').innerHTML;
  __hs_pickAgent('n1', 'echo');
  __hs_assert(zone().includes('命令行参数') && zone().includes('<textarea id="ctrl-args"'), 'allow_args=true 应出现命令行参数输入区');
  __hs_assert(zone().includes('字面量'), '参数区应声明字面量语义（不做 shell 解析）');
  __hs_typeArgs('hello world\n\na b\n   ');
  __hs_assert(S.C.args === 'hello world\n\na b\n   ', '参数草稿应镜像到 S.C.args（整页重绘数据源）');
  __hs_pickAgent('n1', 'report');   // 无 allow_args 字段（旧卡）+stdin_to_arg=true
  __hs_assert(!zone().includes('<textarea'), 'allow_args=false/旧卡不得出现参数输入框');
  __hs_assert(zone().includes('该能力未声明接受任务参数，内容将经 stdin 传递（如能力实际支持，请经 CLI/REST 以 args 传递或升级节点）'), '应显示说明文案（未声明≠不接受，混跑降级口径）');
  __hs_assert(zone().includes('描述将作为命令行参数传递给该能力'), 'stdin_to_arg=true 应附加转换提示（独立于参数授权）');
  __hs_assert(S.C.args === 'hello world\n\na b\n   ', '切到无参数能力草稿不丢（切回参数型能力可恢复）');
  __hs_assert(__hs_getEl('#ctrl-stdin').placeholder === STDIN_PH, '普通能力占位=普通文案');
  __hs_pickAgent('n2', 'tool');   // allow_args=true+stdin_to_arg=true：输入区+转换提示并存
  __hs_assert(zone().includes('<textarea id="ctrl-args"') && zone().includes('hello world'), '切回参数型能力：输入区重建且草稿回填');
  __hs_assert(zone().includes('描述将作为命令行参数传递给该能力'), '双开能力应同时出现参数输入区与转换提示');
  __hs_pickAgent('n1', 'zcode-dev');   // 统筹载体（名字约定回退判定）
  __hs_assert(zone() === '', '统筹载体不出现参数区（描述经 primeArgs 走 args，参数由统筹机制管理）');
  __hs_assert(__hs_getEl('#ctrl-stdin').placeholder === STDIN_PH_PRIME, '选中统筹载体占位切复合目标文案（两态切换既有语义保持）');
  __hs_pickAgent('n1', 'echo');
  __hs_assert(__hs_getEl('#ctrl-stdin').placeholder === STDIN_PH, '切回普通能力占位恢复普通文案（两态切换）');
  __hs_assert(zone().includes('命令行参数'), '切回参数型能力输入区恢复');
  __hs_pickAgent('n1', 'ghost-agent');   // 能力卡不可得（目录无此条目）
  __hs_assert(zone() === '', '能力卡不可得回退现状（不渲染参数区、不阻塞派发）');
  __hs_pickAgent('n1', 'echo');
  __hs_assert(zone().includes('<textarea id="ctrl-args"'), '从未选状态切回参数型能力输入区恢复');
}

/* 场景六 args-submit-mapping 提交映射行为锁：逐行映射+空行/纯空白行忽略（仅空白
 * 判断，非空行保留原文含首尾空白——codex 审查必修①边界：trim 会改写用户参数）
 * +行内空格保留=单参数（字面量）；allow_args=false 能力即使 C.args 有残留也发空
 * 数组（不预发注定 policy_denied 的请求）；统筹载体提交语义不回归（args=[提示词+
 * 目标]、stdin 空）。 */
async function __hs_scn_args_submit_mapping() {
  __hs_reset_args();
  __hs_pickAgent('n1', 'echo');
  __hs_type('echo 任务的描述正文（走 stdin）');
  __hs_typeArgs('hello world\n\n  a b  \n\t\n--verbose');
  __hs_hook = rec => rec.path === '/api/v1/control-operations'
    ? Promise.resolve(__hs_ok({ batch_id: 'b-args', status: 'complete', items: [{ target: 'n1', task_id: 't-args' }] }))
    : Promise.resolve(__hs_ok({}));
  __hs_submitForm();
  await __hs_settle();
  const sent = __hs_calls.find(c => c.path === '/api/v1/control-operations' && c.method === 'POST');
  __hs_assert(!!sent, '表单提交应 POST control-operations');
  const body = JSON.parse(sent.body);
  __hs_assert(JSON.stringify(body.input.args) === JSON.stringify(['hello world', '  a b  ', '--verbose']),
    '参数应逐行映射：空行/纯空白行忽略，非空行保留原文含首尾空白（  a b  原样到达）、行内空格保留=单参数；实得：' + JSON.stringify(body.input.args));
  __hs_assert(body.input.stdin === 'echo 任务的描述正文（走 stdin）', '描述应走 stdin 语义');
  __hs_assert(body.agent === 'echo' && JSON.stringify(body.targets) === JSON.stringify(['n1']), '目标选择不变');

  __hs_reset_args();
  __hs_pickAgent('n1', 'echo');
  __hs_typeArgs('hello world');   // 先在参数型能力上留下草稿
  __hs_pickAgent('n1', 'report'); // 切到无参数能力：C.args 保留残留（切回不丢的代价）
  __hs_assert(S.C.args === 'hello world', '前置：切走后 C.args 有残留（真实序列构造）');
  __hs_type('report 描述');
  __hs_submitForm();
  await __hs_settle();
  const sent2 = __hs_calls.filter(c => c.path === '/api/v1/control-operations').pop();
  const body2 = JSON.parse(sent2.body);
  __hs_assert(JSON.stringify(body2.input.args) === JSON.stringify([]), 'allow_args=false 能力提交 args 必为空数组（不预发注定 policy_denied 的请求，状态残留不得带出）');

  __hs_reset_args();
  __hs_pickAgent('n1', 'zcode-dev');
  __hs_type('检查两台机器磁盘占用并汇总');
  __hs_submitForm();
  await __hs_settle();
  const sent3 = __hs_calls.filter(c => c.path === '/api/v1/control-operations').pop();
  const body3 = JSON.parse(sent3.body);
  __hs_assert(Array.isArray(body3.input.args) && body3.input.args.length === 1
    && body3.input.args[0].includes('检查两台机器磁盘占用并汇总'), '统筹载体提交 args=[提示词+目标] 既有语义不回归');
  __hs_assert(body3.input.stdin === '', '统筹载体提交 stdin 置空既有语义不回归');
}

/* 场景注册：Go 侧逐场景调用并要求逐条 PASS（断言抛错 → node 非零退出）。 */
globalThis.__hs_run = async name => {
  const scn = {
    'stale-input': __hs_scn_stale_input,
    'late-response': __hs_scn_late_response,
    'adopt-reject': __hs_scn_adopt_reject,
    'button-no-submit': __hs_scn_button_no_submit,
    'args-zone-modes': __hs_scn_args_zone_modes,
    'args-submit-mapping': __hs_scn_args_submit_mapping,
  }[name];
  if (!scn) throw new Error('未知场景 ' + name);
  await scn();
  return 'PASS ' + name;
};
