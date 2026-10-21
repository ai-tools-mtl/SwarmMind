'use strict';
/* SwarmMind Hub 中心端控制台（真实 API 版，Loop 6）。
 * 数据面：/api/v1/*（SPEC 六/六.五）；会话 cookie（swarmmind_session）鉴权，
 * 任何 401 自动回登录页。无框架原生 JS，hash 路由 SPA，与 dashboard-journey
 * 原型同视觉（亮色 #f7f6f3 / #4a6cf7）与同交互闭环（INTERACTION 四：300ms
 * 反馈、终态带下一步、结果未知锁表单、技术态配人话双语）。 */

const $ = s => document.querySelector(s);
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

/* ---------- 展示词典：技术态永远配人话（INTERACTION 四.5） ---------- */
const STATES = {
  submitted: '已提交', offered: '已投递', acknowledged: '已接收',
  running: '执行中', completed: '已完成', failed: '执行失败', needs_review: '待人工核对',
};
/* 技术码仅作为 title 提示保留，不再进入主文案（产品化：见名知意）。 */
const ERRORS = {
  timeout: '执行超过本机配置的超时上限，进程组已整组终止',
  policy_denied: '参数或环境变量超出本机白名单，未执行',
  expired: '离线任务超过 7 天保留期，已判失败',
  agent_not_found: '执行节点配置已变化，找不到该 agent',
  agent_not_exposed: '目标未暴露该能力，任务未准入',
  node_restarted_midway: '执行到一半机器重启，结果不确定',
  truncated: '结果超过编码预算，答复或诊断日志已截断',
  mailbox_full: '目标信箱已满（条数或字节预算）',
  storage_full: 'hub 存储全局预算已满',
  frame_too_large: '提交体超过单消息 1MiB 上限',
};
const SKIPS = {
  offline: '离线或未注册',
  not_exposed: '在线但该 agent 未暴露 · not_exposed',
  interrupted: 'hub 创建中断，未及提交 · interrupted',
};
const BATCH_STATES = { creating: '收尾中 · creating', complete: '已完成 · complete' };
const REVIEW_ACTIONS = { ack_review: '确认结果有效', mark_failed: '标记失败' };

/* ---------- 全局状态 ---------- */
const S = {
  authed: false,
  expAt: 0,                    // 登录应答 expires_in 推算；服务端 401 为权威兜底
  page: 'overview',            // hash 路由当前页
  nodes: [], tasks: [], directory: [],
  snap: '',                    // 轮询数据指纹：未变化不重绘
  f: { status: '', trace: '', by: '', error: '', from: '', to: '' },   // 任务筛选
  drawer: null,                // 打开的任务详情 {id, task}
  C: freshControl(),           // 运维控制页状态
  tokens: [],                  // 访问令牌列表
  newToken: null,              // 刚创建的令牌明文（仅展示一次）
};
function freshControl() {
  return { view: 'form', stage: 'idle', agent: '', targets: {}, stdin: '', idem: newIdem(), message: '', unknownPending: false, batch: null };
}
function newIdem() {
  return 'ctrl-' + (globalThis.crypto?.randomUUID?.() || Date.now() + '-' + Math.random().toString(36).slice(2));
}

/* ---------- API 访问 ---------- */
/* 返回 {code, data}；网络层失败抛 {network:true}（结果未知），401 抛前先跳登录。 */
async function api(method, path, body, opt = {}) {
  let resp;
  try {
    resp = await fetch(path, {
      method,
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
      body: body !== undefined ? JSON.stringify(body) : undefined,
      credentials: 'same-origin',
    });
  } catch (e) {
    throw { network: true, cause: e };
  }
  const data = await resp.json().catch(() => null);
  if (resp.status === 401 && !opt.isLogin) {
    sessionExpired();
    throw { unauthorized: true, data };
  }
  return { code: resp.status, data };
}
function errLine(data) {
  if (!data) return '无应答体';
  return esc(data.error || 'error') + ' · ' + esc(data.message || '');
}
function sessionExpired() {
  if (!S.authed) { S.authed = false; route(); return; }
  S.authed = false; S.loaded = false; S.drawer = null; S.C = freshControl();
  render();
  location.hash = '#/login';
  toast('登录会话已失效 · 请重新登录');
}

/* ---------- 数据加载与轮询（页面可见时 5s） ---------- */
async function loadAll() {
  const [n, t, d] = await Promise.all([
    api('GET', '/api/v1/nodes'),
    api('GET', '/api/v1/tasks'),
    api('GET', '/api/v1/directory'),
  ]);
  const nodes = n.data?.nodes || [];
  const tasks = (t.data?.tasks || []).slice().sort(byCreatedDesc);
  const directory = d.data?.directory || [];
  const snap = JSON.stringify([nodes, tasks, directory]);
  const changed = snap !== S.snap;
  S.nodes = nodes; S.tasks = tasks; S.directory = directory; S.snap = snap;
  return changed;
}
function byCreatedDesc(a, b) {
  const x = a.created_at || '', y = b.created_at || '';
  return x < y ? 1 : x > y ? -1 : (a.id < b.id ? -1 : 1);
}
async function pollTick() {
  if (!S.authed || document.visibilityState !== 'visible') return;
  try {
    let changed = await loadAll();
    if (S.C.view === 'result' && S.C.batch) {
      const r = await api('GET', '/api/v1/control-operations/' + encodeURIComponent(S.C.batch.batch_id));
      if (r.code === 200 && r.data) { S.C.batch = r.data; changed = true; }
    }
    /* 表单填写视图不整页重渲染（2026-10-21 验收：5s 轮询重建 DOM 打断
     * 下拉选择与输入——「选择能力时经常刷新」根因）。数据静默更新。 */
    if (changed) {
      if (S.page === 'control' && S.C.view === 'form') return;
      render();
    }
  } catch (e) { /* 401 已由 api() 处理；其余静默，下轮再试 */ }
}

/* ---------- 路由（hash SPA） ---------- */
const PAGE_IDS = ['overview', 'nodes', 'tasks', 'control', 'policy', 'tokens'];
function route() {
  const h = location.hash.replace(/^#\/?/, '');
  const [p, arg] = h.split('/');
  if (!S.authed) { S.page = 'login'; render(); return; }
  if (p === 'tasks' && arg && STATES[arg]) { S.f = { status: arg, trace: '', by: '', error: '', from: '', to: '' }; }
  S.page = PAGE_IDS.includes(p) ? p : 'overview';
  render();
  if (S.authed && S.page === 'tokens') loadTokens().then(() => { if (S.page === 'tokens') render(); });
  window.scrollTo(0, 0);
}
function go(p, status = '') {
  S.f = { status, trace: '', by: '', error: '', from: '', to: '' };
  closeLayer();
  const h = '#/' + p + (status ? '/' + status : '');
  if (location.hash === h) route(); else location.hash = h;
}

/* ---------- 渲染骨架 ---------- */
function render() {
  renderChrome();
  const app = $('#app');
  if (!S.authed) {
    if (loginPageRendered === null) {
      loginPageRendered = 'loading';
      loadAccountStatus().then(init => { loginPageRendered = init; render(); });
    }
    app.innerHTML = loginPage(loginPageRendered === true);
    $('#layer').innerHTML = '';
    return;
  }
  app.innerHTML = shell(PAGES[S.page]());
  syncDrawer();
}
function renderChrome() {
  $('#logout').hidden = !S.authed;
  $('#session-left').textContent = S.authed ? '会话剩余 ' + humanLeft() : '';
}
function humanLeft() {
  if (!S.expAt) return '有效（24h 内）';   // reload 后登录时刻未知；服务端 401 为权威兜底
  const ms = S.expAt - Date.now();
  if (ms <= 0) return '即将过期';
  const h = Math.floor(ms / 3.6e6), m = Math.floor((ms % 3.6e6) / 6e4);
  return h > 0 ? `${h} 小时 ${m} 分` : `${m} 分钟`;
}
function shell(inner) {
  const menus = [['overview', '总览'], ['nodes', '节点接入'], ['tasks', '任务监管'], ['control', '运维控制'], ['policy', '策略容量'], ['tokens', '设置']];
  const nr = S.tasks.filter(t => t.status === 'needs_review').length;
  return `<div class="shell"><aside>
  <div class="workspace"><small>协作网 · 管理员</small><strong>◈ SwarmMind 中心端</strong></div>
  <nav aria-label="旅程导航">${menus.map(([id, name], i) =>
    `<button data-act="go" data-page="${id}" class="${S.page === id ? 'active' : ''}" ${S.page === id ? 'aria-current="page"' : ''}><span class="navnum">0${i + 1}</span>${name}${id === 'tasks' && nr ? ` <small>(${nr})</small>` : ''}</button>`).join('')}</nav>
  <div class="aside-bottom">中心端 · 登记与监管<br>任务执行在各个节点本机</div>
  </aside><main id="main">${inner}</main></div>`;
}

/* ---------- 通用小件 ---------- */
function button(label, act, cls = '', extra = '') { return `<button data-act="${act}" class="${cls}" ${extra}>${label}</button>`; }
function head(title, desc, action = '') {
  return `<div class="page-head"><div><h1>${title}</h1><p class="muted">${desc}</p></div><div class="actions">${action}</div></div>`;
}
function metric(label, value, desc, cls = '') { return `<div class="card${cls ? ' ' + cls : ''}"><small>${label}</small><div class="metric">${value}</div><small>${desc}</small></div>`; }
function badge(s) { return `<span class="badge ${esc(s)}" title="${esc(s)}">${esc(STATES[s] || s)}</span>`; }
function onlineB(n) { return `<span class="badge ${n.online ? 'online' : 'offline'}">${n.online ? '在线' : '离线'}</span>`; }
function warnNotice(title, body) { return `<div class="notice warn"><b>${title}</b><br>${body}</div>`; }
const fmtTime = s => (s && !String(s).startsWith('0001-01-01')) ? String(s).replace('T', ' ').slice(0, 16) : '';
const fmtFull = s => s ? String(s).replace('T', ' ').slice(0, 19) + ' UTC' : '';
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.hidden = false;
  clearTimeout(toast.timer); toast.timer = setTimeout(() => { t.hidden = true; }, 3400);
}
function terminal(st) { return ['completed', 'failed', 'needs_review'].includes(st); }
function stateCounts() { const c = {}; for (const t of S.tasks) c[t.status] = (c[t.status] || 0) + 1; return c; }
function preserveFocus(fn) {
  const a = document.activeElement;
  const id = a?.id, pos = a && 'selectionStart' in a ? a.selectionStart : 0;
  fn();
  if (id) {
    const el = document.getElementById(id);
    if (el) { el.focus(); try { if ('setSelectionRange' in el) el.setSelectionRange(pos, pos); } catch { } }
  }
}

/* ---------- 登录页（免鉴权；其余页 401 自动跳到这里） ---------- */
let loginMode = 'password'; // password | token
let loginPageRendered = null; // null=未探询 / true=已初始化 / false=待初始化
async function loadAccountStatus() {
  try {
    const r = await fetch('/api/v1/auth/account');
    if (r.ok) { const d = await r.json(); return !!d.initialized; }
  } catch { }
  return false;
}
function loginPage(initialized) {
  const pw = loginMode === 'password';
  return `<main><div class="card login">
  <div class="mark">⌘</div><div class="eyebrow">管理员入口</div>
  <h1>管理你的协作网</h1>
  ${pw ? (initialized
    ? `<form id="pw-login-form">
    <label>用户名<input id="login-user" autocomplete="username" required placeholder="用户名"></label>
    <label>密码<input id="login-pass" type="password" autocomplete="current-password" required placeholder="密码"></label>
    <div id="login-feedback" class="statusline" role="status"></div>
    <button class="primary" type="submit">登录</button>
  </form>`
    : `<form id="setup-form">
    <div class="notice">首次使用：设置管理员用户名与密码</div>
    <label>用户名（3-32 位字母/数字/下划线）<input id="su-user" autocomplete="username" required placeholder="例如 admin"></label>
    <label>密码（至少 8 位）<input id="su-pass" type="password" autocomplete="new-password" required placeholder="设置密码"></label>
    <label>确认密码<input id="su-pass2" type="password" autocomplete="new-password" required placeholder="再输一次"></label>
    <div id="login-feedback" class="statusline" role="status"></div>
    <button class="primary" type="submit">创建账户并登录</button>
  </form>`)
  : `<form id="login-form">
    <label>管理令牌（恢复通道）<input id="token" type="password" autocomplete="off" required placeholder="输入 hub 配置里的管理令牌"></label>
    <div id="login-feedback" class="statusline" role="status"></div>
    <button class="primary" type="submit">用令牌登录</button>
  </form>`}
  <p class="footer-note">${pw ? '忘记密码？' : ''}<button class="link" data-act="switch-login" type="button">${pw ? '用管理令牌登录（恢复通道）' : '返回密码登录'}</button></p>
  </div></main>`;
}
async function doSetup(form) {
  const u = $('#su-user')?.value.trim(), p = $('#su-pass')?.value || '', p2 = $('#su-pass2')?.value || '';
  const fb = $('#login-feedback');
  if (!u || !p) { fb.innerHTML = '<span class="warn-text">请填写用户名与密码</span>'; return; }
  if (p !== p2) { fb.innerHTML = '<span class="warn-text">两次输入的密码不一致</span>'; return; }
  try {
    const r = await fetch('/api/v1/auth/setup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: p }) });
    const d = await r.json().catch(() => ({}));
    if (r.status === 201) {
      await doPasswordLogin(u, p); return;
    }
    fb.innerHTML = warnNotice('创建失败 · ' + esc(d.error || r.status), esc(d.message || ''));
  } catch { fb.innerHTML = warnNotice('网络异常', '请稍后重试'); }
}
async function doPasswordLogin(u, p) {
  const fb = $('#login-feedback') || { innerHTML: '' };
  try {
    const r = await fetch('/api/v1/auth/login-password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: p }) });
    if (r.status === 200) {
      S.authed = true;
      S.expAt = Date.now() + 86400 * 1000;
      S.C = freshControl();
      try { await loadAll(); } catch { }
      location.hash = '#/overview';
      route();
      toast('登录成功 · 已进入总览');
      return;
    }
    const d = await r.json().catch(() => ({}));
    fb.innerHTML = warnNotice('登录失败 · ' + esc(d.error || r.status), esc(d.message || '用户名或密码错误'));
  } catch { fb.innerHTML = warnNotice('网络异常', '请稍后重试'); }
}
async function doLogin(form) {
  const token = $('#token').value.trim();
  const fb = $('#login-feedback'), btn = form.querySelector('button[type=submit]');
  if (!token) { fb.innerHTML = '<span class="warn-text">请输入访问令牌</span>'; return; }
  btn.disabled = true; btn.textContent = '验证中…';
  fb.textContent = '正在验证令牌…';
  try {
    const r = await api('POST', '/api/v1/login', { token }, { isLogin: true });
    if (r.code === 200) {
      S.authed = true;
      S.expAt = Date.now() + (r.data?.expires_in || 86400) * 1000;
      S.C = freshControl();
      S.f = { status: '', trace: '', by: '', error: '', from: '', to: '' };
      try { await loadAll(); } catch { }
      location.hash = '#/overview';
      route();
      toast('登录成功 · 已进入总览');
      return;
    }
    fb.innerHTML = r.code === 401
      ? warnNotice('管理令牌错误 · auth_failed', '请核对 hub 配置的 admin_token（或 data_dir/admin_token 文件内容）后重试。')
      : warnNotice('登录失败 · ' + esc(r.data?.error || r.code), esc(r.data?.message || ''));
  } catch (e) {
    fb.innerHTML = e.network
      ? warnNotice('结果未知 · 未收到服务器应答', '检查 hub 是否在运行、地址是否正确后重试；登录失败不会产生副作用。')
      : warnNotice('登录失败', '请稍后重试。');
  }
  btn.disabled = false; btn.textContent = '进入 Hub 中心端 →';
}

/* ---------- 总览 ---------- */
function overviewPage() {
  const c = stateCounts();
  const online = S.nodes.filter(n => n.online).length;
  const nr = c.needs_review || 0;
  const offline = S.nodes.filter(n => !n.online);
  const statChips = [''].concat(Object.keys(STATES)).map(s => s
    ? `<button data-act="fchip" data-fchip="${s}" class="${S.f.status === s ? 'on' : ''}">${badge(s)} ${c[s] || 0}</button>`
    : `<button data-act="fchip" data-fchip="" class="${!S.f.status ? 'on' : ''}">全部 (${S.tasks.length})</button>`).join('');
  return head('总览', `${online} / ${S.nodes.length} 台节点在线 · ${S.directory.length} 项网内能力`, button('＋ 接入节点', 'go', 'primary', 'data-page="nodes"'))
    + `<div class="grid cols4">
    ${metric('全网任务', S.tasks.length, '终态保留 24 小时')}
    ${metric('正在执行', c.running || 0, '节点断线不会终止已开始的任务')}
    ${metric('已完成', c.completed || 0, '含答复与产物')}
    ${metric('等待核对', nr, nr ? '点击右侧待办处理' : '无需人工处理', nr ? 'review-card' : '')}
    </div>`
    + `<div class="grid cols2 spaced">
    <section class="card"><div class="row"><h2>中心端状态</h2><span class="badge online">在线</span></div>
      <div class="stack">
        <div class="row"><span>访问地址</span><code>${esc(location.host)}</code></div>
        <div class="row"><span>登录剩余时间</span><b>${esc(humanLeft())}</b></div>
        <div class="row"><span>网内开放能力</span><b>${S.directory.length} 项</b></div>
      </div>
      <small class="muted">中心端负责登记与监管；所有任务都在各节点本机执行。</small>
    </section>
    <section class="card"><div class="row"><h2>需要你关注</h2>${nr ? badge('needs_review') : '<span class="badge completed">无待办</span>'}</div>
      ${nr ? `<div class="notice purple"><b>${nr} 条执行结果不确定</b><p>执行中断等待人工核对，请勿重新提交同一任务。</p>${button('去处理 →', 'go', 'link', 'data-page="tasks" data-status="needs_review"')}</div>`
    : `<div class="notice">当前没有待办；${c.failed || 0} 条失败任务可在「任务监管」页下钻原因。</div>`}
      ${offline.length ? `<div class="notice warn spaced"><b>${esc(offline.map(n => n.node_id).join('、'))} 离线</b>：交给它的任务会暂存，重连后自动送达；7 天未送达则过期。<br>${button('查看节点 →', 'go', 'link', 'data-page="nodes"')}</div>` : ''}
    </section>
    </div>`
    + `<section class="card spaced"><div class="row"><h2>任务状态分布（七态）</h2>${button('查看全网任务 →', 'go', 'link', 'data-page="tasks"')}</div>
    <div class="filters">${statChips}</div></section>`
    + `<section class="card spaced"><div class="row"><h2>最近任务</h2>${button('查看全网任务 →', 'go', 'link', 'data-page="tasks"')}</div>
    ${S.tasks.length ? taskTable(S.tasks.slice(0, 5)) : `<div class="empty"><h2>还没有任务记录</h2><p class="muted">任务经 hub 管理面 API 或 node 本地 API 提交后出现在这里。</p></div>`}</section>`;
}

/* ---------- 节点接入 ---------- */
function nodesPage() {
  return head('节点接入', '每台机器独立身份与令牌；执行权始终在本机主人手里。', button('生成接入配置', 'enroll', 'primary'))
    + `<section class="card"><div class="row"><h2>已登记节点</h2><small>${S.nodes.length} 台</small></div>
    ${S.nodes.length ? `<div class="table-wrap"><table><thead><tr><th>节点</th><th>连接状态</th><th>重连次数</th><th>进行中任务</th><th>管理</th></tr></thead><tbody>
    ${S.nodes.map(n => `<tr><td><b>${esc(n.node_id)}</b></td><td>${onlineB(n)}</td><td>${n.epoch}</td><td>${n.inflight} / 8</td><td class="table-action">${button('吊销', 'revoke', 'danger', `data-node="${esc(n.node_id)}"`)}</td></tr>`).join('')}
    </tbody></table></div>`
    : `<div class="empty"><h2>还没有节点接入</h2><p class="muted">在中心端配置里登记节点身份与令牌并重启，节点连接成功后出现在这里。</p>${button('生成接入配置', 'enroll', 'primary')}</div>`}
    <p class="footer-note">每台节点最多同时进行 8 笔任务，超出自动排队；离线不会终止已在执行的任务。</p></section>`;
}
function enrollModal() {
  openLayerPlain(`${modalHead('生成接入配置 · V0 手工登记')}
  <div class="notice warn">V0 无动态签发：token 不经 Web 展示或下发。请在 hub 侧生成随机令牌（如 <code>openssl rand -hex 32</code>）；下方 token 以掩码占位，实际值从 hub 配置查看。</div>
  <label>新节点 ID（node_id，管理员填写）<input id="enroll-id" placeholder="例如 build-linux（小写字母 / 数字 / 连字符）" autocomplete="off"></label>
  <label>接入令牌（hub 配置只读 · 掩码展示）<input readonly value="****************************************（存于 hub.yaml，不通过 Web 展示）"></label>
  <h3>① hub 配置片段 —— 将此片段加入 hub.yaml 的 nodes: 段并重启 hub</h3>
  <pre id="enroll-snippet"></pre>
  <h3>② 目标机器 node.yaml —— 同步登记同一身份与令牌</h3>
  <pre id="enroll-node-snippet"></pre>
  <div class="actions">${button('复制配置片段', 'copy-config')}${button('完成，等待节点上线', 'enroll-done', 'primary')}</div>
  <p class="footer-note">node 连接成功后出现在节点列表；接入成功以列表在线状态为准。控制类身份须经管理员批准，禁止节点自报提权。</p>`);
  updateEnrollSnippet();
}
function updateEnrollSnippet() {
  const id = $('#enroll-id')?.value.trim();
  const shown = id || '<node_id>';
  const sn = $('#enroll-snippet'), ns = $('#enroll-node-snippet');
  if (sn) sn.textContent = `# hub.yaml → nodes: 段新增一行，然后重启 hub\nnodes:\n  ${shown}: <hub 侧生成的随机令牌，如 openssl rand -hex 32>`;
  if (ns) ns.textContent = `node_id: ${shown}\nhub_url: ws://<hub 地址>/ws/node   # 部署模式为 wss:// + 证书指纹\nregister_token: <同上令牌>          # 与 hub.yaml 中该 node_id 的值一致\nhub_pubkey_fp: ""                   # 空则 TOFU 记录首连\napi_listen: 127.0.0.1:8445\ndata_dir: data\nconcurrency: 2`;
}

/* ---------- 任务监管 ---------- */
function filteredTasks() {
  const f = S.f;
  return S.tasks.filter(t => {
    if (f.status && t.status !== f.status) return false;
    if (f.trace && !(t.trace_id || '').includes(f.trace) && !(t.id || '').includes(f.trace)) return false;
    if (f.by && t.submitted_by !== f.by) return false;
    if (f.error && (t.result?.error || '') !== f.error) return false;
    const day = (t.created_at || '').slice(0, 16);
    if (f.from && day < f.from) return false;
    if (f.to && day > f.to) return false;
    return true;
  });
}
function tasksPage() {
  const list = filteredTasks();
  const counts = stateCounts();
  const filtered = !!(S.f.status || S.f.trace || S.f.by || S.f.error || S.f.from || S.f.to);
  const submitters = [...new Set(S.tasks.map(t => t.submitted_by).filter(Boolean))].sort();
  return head('任务监管', '全网任务只读监管；从异常下钻，以证据核对交付。', button('↓ 导出 JSON（当前筛选）', 'export'))
    + `<section class="card">
    <div class="filters">${[''].concat(Object.keys(STATES)).map(s => s
    ? `<button data-act="fchip" data-fchip="${s}" class="${S.f.status === s ? 'on' : ''}">${badge(s)} ${counts[s] || 0}</button>`
    : `<button data-act="fchip" data-fchip="" class="${!S.f.status ? 'on' : ''}">全部 (${S.tasks.length})</button>`).join('')}</div>
    <div class="fields">
      <label>搜索任务 ID<input id="f-trace" data-f="trace" value="${esc(S.f.trace)}" placeholder="输入任务 ID 或追踪号"></label>
      <label>提交方<select data-f="by"><option value="">全部提交方</option>${submitters.map(u => `<option ${S.f.by === u ? 'selected' : ''}>${esc(u)}</option>`).join('')}</select></label>
      <label>失败原因<select data-f="error"><option value="">全部</option>${Object.entries(ERRORS).map(([k, v]) => `<option value="${k}" title="${esc(v)}" ${S.f.error === k ? 'selected' : ''}>${esc(v)}</option>`).join('')}</select></label>
      <label>提交时间 · 从（UTC）<input type="datetime-local" data-f="from" value="${esc(S.f.from)}"></label>
      <label>提交时间 · 至（UTC）<input type="datetime-local" data-f="to" value="${esc(S.f.to)}"></label>
    </div>
    ${list.length ? taskTable(list) : `<div class="empty"><h2>${S.tasks.length ? '没有符合筛选的任务' : '还没有任务记录'}</h2>${filtered ? button('清除筛选', 'clear-filters') : ''}</div>`}
    <p class="footer-note">${list.length} 条记录 · 时间为 UTC · 已完成的任务保留 24 小时，重要结果请及时导出。</p></section>`;
}
function taskTable(list) {
  return `<div class="table-wrap"><table><thead><tr><th>任务 / trace_id</th><th>目标节点</th><th>状态</th><th>提交方</th><th>提交时间（UTC）</th><th>操作</th></tr></thead><tbody>
  ${list.map(t => `<tr data-task="${esc(t.id)}" tabindex="0" aria-label="查看任务 ${esc(t.id)}">
    <td class="task-title"><strong>${esc(t.target_agent)}</strong><small>${esc(t.id)}</small></td>
    <td>${esc(t.target_node)}</td>
    <td>${badge(t.status)}${t.result?.error ? `<small class="help" title="${esc(ERRORS[t.result.error] || '执行中断，需人工核对')}">ⓘ</small>` : ''}${t.review ? '<small>已核对</small>' : ''}</td>
    <td>${esc(t.submitted_by)}</td>
    <td><small>${esc(fmtTime(t.created_at))}</small></td>
    <td class="table-action">${t.status === 'needs_review' && !t.review ? button('去核对', 'detail', 'link', `data-id="${esc(t.id)}"`) : button('查看 →', 'detail', 'link', `data-id="${esc(t.id)}"`)}</td>
  </tr>`).join('')}</tbody></table></div>`;
}

/* ---------- 任务抽屉（详情 + 核对 + 产物 + 时间线） ---------- */
function openTask(id) {
  const t = S.tasks.find(x => x.id === id);
  if (!t) {
    /* 防悬空引用：批次/核对记录可能引用已归档任务；先强刷一次快照再试。 */
    toast('正在查询任务最新状态…');
    api('GET', '/api/v1/tasks/' + encodeURIComponent(id)).then(r => {
      if (r.code === 200 && r.data) {
        const i = S.tasks.findIndex(x => x.id === id);
        if (i >= 0) S.tasks[i] = r.data; else S.tasks.unshift(r.data);
        S.drawer = { id, task: r.data };
        syncDrawer(true);
      } else {
        toast('该任务已归档（终态保留 24 小时），无法查看详情');
      }
    }).catch(() => toast('查询失败：网络异常，请稍后重试'));
    return;
  }
  modalOpen = false;
  S.drawer = { id, task: t };
  syncDrawer(true);
}
function syncDrawer(force = false) {
  const layer = $('#layer');
  if (modalOpen) return;               // 普通弹层优先：不被轮询/页面重绘冲掉
  if (!S.drawer) { layer.innerHTML = ''; return; }
  const t = S.tasks.find(x => x.id === S.drawer.id);
  if (t) S.drawer.task = t;
  const a = document.activeElement;
  const typing = !force && a && layer.contains(a) && ['INPUT', 'TEXTAREA', 'SELECT'].includes(a.tagName);
  if (!layer.innerHTML || !typing) {
    layer.innerHTML = `<div class="overlay drawer-overlay" data-backdrop="true"><section class="drawer" role="dialog" aria-modal="true" aria-label="任务详情" tabindex="-1">${drawerHtml(S.drawer.task)}</section></div>`;
  }
}
function closeLayer() { S.drawer = null; modalOpen = false; $('#layer').innerHTML = ''; }
function modalHead(title) {
  return `<div class="row"><h2>${title}</h2>${button('×', 'close', 'close', 'aria-label="关闭弹层"')}</div>`;
}
function drawerHtml(t) {
  const needs = t.status === 'needs_review';
  const r = t.result;
  const intro = needs
    ? '任务执行到一半节点重启了，结果不确定——请核对后登记结论，不要重新提交同一任务。'
    : t.status === 'completed' ? '任务已完成：可阅读最终答复并下载产物文件。'
    : t.status === 'failed' ? '本次执行未完成：先看下方说明，再决定是否重新派任务。'
    : '任务仍在进行，列表每 5 秒自动刷新。';
  return `${modalHead('任务详情 · ' + esc(t.id))}
  <p class="${needs ? 'notice purple' : 'notice'}">${intro}</p>
  <div class="row"><h2>${esc(t.target_agent)} @ ${esc(t.target_node)}</h2></div>
  <div class="row">${badge(t.status)}<small>${esc(t.trace_id)}</small></div>
  <p class="muted spaced">${esc(t.submitted_by)} 提交 · 创建于 ${esc(fmtFull(t.created_at))}${t.idem ? ' · 幂等键 <code>' + esc(t.idem) + '</code>' : ''}</p>
  ${r?.error ? `<div class="notice warn"><b>${esc(ERRORS[r.error] || '执行中断')}</b><br>内部标记：${esc(r.error)} · 退出码 ${r.exit_code}${needs ? '<br>任务执行到一半节点重启，无法确认实际进度。' : ''}</div>` : ''}
  ${r && r.stderr_tail && (t.status === 'failed' || r.error) ? `<h3 class="spaced">失败原因（进程错误输出）</h3><pre style="white-space:pre-wrap">${esc(r.stderr_tail)}</pre>` : ''}
  <h3 class="spaced">任务输入</h3>
  <pre>${esc(JSON.stringify({ args: t.input?.args || [], stdin: t.input?.stdin || '', env: t.input?.env || {} }, null, 2))}</pre>
  <h3>最终答复</h3>
  ${r && r.final_answer ? `<p style="white-space:pre-wrap">${esc(r.final_answer)}</p>` : (needs ? '<p class="muted">结果未确认，待人工核对。</p>' : r && r.stdout_tail ? '<p class="muted">该任务没有单独的文本答复，输出内容见下方诊断日志与产物。</p>' : '<p class="muted">该任务没有文本答复（例如等待类任务）；如有文件输出，见下方产物列表。</p>')}
  ${r && (r.stdout_tail || r.stderr_tail) ? `<details><summary>运行日志</summary><pre>${esc(r.stdout_tail || '')}</pre>${r.stderr_tail ? `<pre>${esc(r.stderr_tail)}</pre>` : ''}<small>最多保留各 64KiB。</small></details>` : ''}
  <h3>产物文件</h3>
  ${artifactsHtml(t)}
  <p class="footer-note">产物经中心端中继从执行节点取回，SHA-256 校验一致才允许下载；自结果确认起保留 24 小时。</p>
  <h3 class="spaced">时间线</h3>
  <ol class="timeline">${timeline(t).map(([e, time]) => `<li>${esc(e)}<br><small>${esc(time)}</small></li>`).join('')}</ol>
  ${reviewSection(t)}
  ${terminal(t.status) && !needs ? `<div class="notice">任务已归档前保留 24 小时。${button('返回任务列表 →', 'go-tasks', 'link')}</div>` : ''}
  <div class="actions spaced">${button('导出此任务 JSON', 'export-one', '', `data-id="${esc(t.id)}"`)}</div>`;
}
function artifactsHtml(t) {
  const arts = t.result?.artifacts || [];
  if (!arts.length) return '<p class="muted">暂无已登记产物。中断任务不推断存在可交付产物。</p>';
  return arts.map((a, i) => `<div class="artifact row"><div><b>${esc(a.path)}</b><br><small>${a.size} 字节 · sha256 ${esc(String(a.sha256 || '').slice(0, 16))}…</small></div>${button('取回', 'artifact', '', `data-id="${esc(t.id)}" data-index="${i}"`)}</div>`).join('');
}
function timeline(t) {
  const ev = [['任务已受理', fmtFull(t.created_at)]];
  // Go time.Time 零值序列化为 0001-01-01T00:00:00Z（omitempty 对结构体不生效）：
  // 仅非零时刻才算有证据。
  const real = s => s && !String(s).startsWith('0001-01-01');
  if (real(t.result?.started_at)) ev.push(['开始执行', fmtFull(t.result.started_at)]);
  if (terminal(t.status)) {
    const fin = [t.result?.finished_at, t.updated_at].find(real);
    ev.push([`${STATES[t.status]}`, fmtFull(fin)]);
  }
  if (t.review) ev.push([`人工核对完成（${REVIEW_ACTIONS[t.review.action] || t.review.action}，${t.review.by}）`, fmtFull(t.review.at)]);
  return ev;
}
function reviewSection(t) {
  if (t.status !== 'needs_review') return '';
  if (t.review) {
    return `<section class="card spaced"><h3>人工核对记录</h3>
    <div class="notice">已核对完成<br>结论：${esc(REVIEW_ACTIONS[t.review.action] || t.review.action)}<br>依据：${esc(t.review.note || '（未填写）')}<br><small>${esc(fmtFull(t.review.at))} · ${esc(t.review.by)}</small></div>
    <div class="actions spaced">${button('下一步：回到待办列表 →', 'go-review-queue', 'link')}</div></section>`;
  }
  return `<section class="card spaced"><h3>人工核对</h3>
  <p class="muted">核对只是登记人工结论：任务保持「待人工核对」状态，不会自动重跑，也不会改写执行结果。</p>
  <div class="notice warn"><b>请勿重新提交同一任务：</b>重复提交会在节点上再次执行。</div>
  <label class="spaced">核对依据（记录检查过的证据：文件变化 / 数据状态 / 实际产物）<textarea id="review-note" rows="4" placeholder="例如：检查迁移记录已写入、数据一致，确认执行实际完成。"></textarea></label>
  <div class="actions">
    <button class="primary" data-act="review" data-review-action="ack_review" data-id="${esc(t.id)}">确认结果有效</button>
    <button class="warning" data-act="review" data-review-action="mark_failed" data-id="${esc(t.id)}">标记为失败</button>
  </div></section>`;
}

/* ---------- 运维控制 ---------- */
function controlPage() {
  const C = S.C;
  if (C.view === 'result') return controlResult();
  const locked = C.stage === 'submitting' || C.stage === 'unknown';
  const targets = Object.keys(C.targets).filter(k => C.targets[k]);
  return head('运维控制', '向网内巡检类 agent 派发正式任务：hub 不内嵌执行体，执行仍在 node 侧。')
    + `<section class="card"><form id="control-form"><fieldset ${locked ? 'disabled' : ''}>
    <div class="row"><h2>全网巡检</h2><span class="badge offered">正式任务 · 在节点本机执行</span></div>
    <label>选择能力<select id="ctrl-agent">${S.directory.length ? S.directory.map(d => {
    const v = d.node_id + '/' + d.card.name;
    return `<option value="${esc(v)}" ${C.agent === v ? 'selected' : ''}>${esc(d.card.name)}（${esc(d.node_id)}）— ${esc(d.card.description || '（无描述）')}</option>`;
  }).join('') : '<option value="">目录为空：还没有节点开放能力</option>'}</select></label>
    ${S.directory.length ? `<div class="row"><small class="muted">将在 <b id="ctrl-node-hint">${esc((S.directory[0] || {}).node_id || '')}</b> 上执行</small></div>` : '<div class="notice warn">能力目录为空：需要先在节点配置里开放能力（exposed: true）。</div>'}
    <label class="checkline spaced"><input type="checkbox" id="ctrl-broadcast"> 广播到所有拥有同名能力的节点（默认只派给能力所属节点）</label>
    <label class="spaced">巡检指令<textarea id="ctrl-stdin" rows="4" placeholder="例如：检查本机 agent 进程、磁盘余量与最近错误日志，输出健康摘要。">${esc(C.stdin)}</textarea></label>
    <label>恢复密钥（自动生成 · 用于中断后安全重试，不会重复执行）<input readonly value="${esc(C.idem)}"></label>
    </fieldset>
    <div id="ctrl-feedback" class="statusline" role="status">${ctrlFeedback()}</div>
    <div class="actions">
      <button class="primary" type="submit" ${C.stage === 'submitting' ? 'disabled' : ''}>${C.stage === 'submitting' ? '提交中… 请勿重复操作' : '运行巡检'}</button>
      ${C.stage === 'unknown' ? button('同键重试', 'ctrl-retry') + button('放弃等待', 'ctrl-abandon', 'link') : ''}
      ${C.unknownPending && C.stage !== 'unknown' ? button('同键重试上次未决提交', 'ctrl-retry') : ''}
    </div></form></section>
    ${C.message ? `<section class="card spaced">${C.message}</section>` : ''}
    <section class="card spaced"><h3>运行规则</h3>
    <div class="step"><span>1</span><p>提交时锁定目标：离线或未开放能力的节点自动跳过，不影响其余目标。</p></div>
    <div class="step"><span>2</span><p>每个目标节点上都是一笔正式任务，全程可追踪。</p></div>
    <div class="step"><span>3</span><p>逐项结果见批次报告；任务进展在「任务监管」页跟踪。</p></div></section>`;
}
function ctrlFeedback() {
  const C = S.C;
  if (C.stage === 'unknown') return warnNotice('结果未知 · 未收到服务器确认', '这次提交可能已经生效：表单与恢复密钥已冻结。「同键重试」命中原批次不会重复执行；「放弃等待」保留密钥稍后再试。');
  if (C.stage === 'submitting') return '<span class="muted">正在提交…</span>';
  return '<span class="muted">就绪 · 提交后逐个节点返回结果</span>';
}
function controlResult() {
  const b = S.C.batch;
  const items = b.items || [];
  const ok = items.filter(i => i.task_id).length;
  const skip = items.filter(i => i.skipped).length;
  const err = items.filter(i => i.error).length;
  return head('批次报告 · ' + esc(b.batch_id), '这次巡检在每个节点上的结果与去向。', button('再运行一次', 'ctrl-new', 'primary'))
    + `<section class="card">
    <div class="row"><h2>汇总</h2><span class="badge ${b.status === 'complete' ? 'completed' : 'running'}">${esc(BATCH_STATES[b.status] || b.status)}</span></div>
    <div class="mini-stat"><div><b>${ok}</b><small>已提交</small></div><div><b>${skip}</b><small>已跳过</small></div><div><b>${err}</b><small>提交失败</small></div></div>
    <div class="table-wrap spaced"><table><thead><tr><th>目标节点</th><th>结果</th><th>去向</th></tr></thead><tbody>
    ${items.map(it => `<tr><td><b>${esc(it.target)}</b></td>
      <td>${it.task_id ? badge(it.status || 'submitted') : it.skipped ? `<span class="badge offline" title="${esc(it.skipped)}">已跳过（${it.skipped === 'offline' ? '离线' : '能力未开放'}）</span>` : `<span class="badge failed">失败</span>`}</td>
      <td>${it.task_id ? `${button('查看任务 →', 'detail', 'link', `data-id="${esc(it.task_id)}"`)}<small>${esc(it.task_id)}</small>` : `<small>${it.skipped === 'offline' ? '节点离线，未提交' : it.skipped === 'not_exposed' ? '该节点未开放此能力' : esc(ERRORS[it.error] || it.error || '')}</small>`}</td>
    </tr>`).join('')}
    </tbody></table></div>
    <p class="footer-note">批次创建于 ${esc(fmtFull(b.created_at))} · 各任务状态随页面自动刷新。</p>
    <div class="actions spaced">${button('刷新状态', 'ctrl-refresh')}${button('下一步：查看任务监管 →', 'go-tasks', 'link')}${skip ? button('查看节点 →', 'go', 'link', 'data-page="nodes"') : ''}</div>
    </section>`;
}
async function submitControl() {
  const C = S.C;
  if (C.stage === 'submitting' || C.stage === 'unknown') return;
  const sel = $('#ctrl-agent')?.value;
  if (!sel) { toast('目录为空：先在节点侧开放能力，再运行巡检'); return; }
  const agentName = sel.split('/')[1];
  const broadcast = $('#ctrl-broadcast')?.checked;
  const targets = broadcast
    ? [...new Set(S.directory.filter(d => d.card.name === agentName).map(d => d.node_id))]
    : [sel.split('/')[0]];
  if (!targets.length) {
    C.stage = 'idle'; C.unknownPending = false;
    C.message = warnNotice('明确失败 · invalid_request', '没有可执行的目标节点。');
    render(); toast('明确失败：请修正后重试'); return;
  }
  C.stdin = $('#ctrl-stdin').value;
  C.agent = sel;
  C.unknownPending = false;
  C.stage = 'submitting'; C.message = '';
  render(); toast('已锁定表单：正在提交巡检批次…');
  const body = {
    agent: agentName,
    targets,
    input: { args: [], stdin: C.stdin || '全网健康巡检：输出本机 agent、连接与资源健康摘要。' },
    idem_key: C.idem,
  };
  try {
    const r = await api('POST', '/api/v1/control-operations', body);
    if (r.code === 202 || r.code === 200) {
      C.batch = r.data; C.view = 'result'; C.stage = 'done'; C.idem = newIdem();
      /* BUG 修复（2026-10-21 验收）：提交后必须立即刷新任务快照，
       * 否则批次报告页「查看任务」在下一轮 5s 轮询前点击必然 404。 */
      try { await loadAll(); } catch { }
      const n = (r.data.items || []).length, ok = (r.data.items || []).filter(i => i.task_id).length;
      toast(`批次已受理${r.code === 200 ? '（幂等命中 dup）' : ''} · ${ok}/${n} 目标已提交 —— 下一步：查看批次报告`);
      render(); return;
    }
    C.stage = 'idle';
    C.message = warnNotice('明确失败 · ' + esc(r.data?.error || r.code), esc(r.data?.message || '') + '<br>' + esc(ERRORS[r.data?.error] || ''));
    render(); toast('明确失败：请修正后重试');
  } catch (e) {
    if (e.network) {
      C.stage = 'unknown'; C.unknownPending = true;
      C.message = warnNotice('上次提交结果未知', '幂等键已保留：需要确认时点「同键重试上次未决提交」，同键同内容命中原批次，不会重复执行。');
      render(); toast('结果未知：表单与幂等键已冻结');
    } else if (!e.unauthorized) {
      C.stage = 'idle';
      C.message = warnNotice('请求失败', '请稍后重试。');
      render();
    }
  }
}

/* ---------- 策略容量（只读） ---------- */
function policyPage() {
  const rows = [
    ['信箱条数', '100 条 / node', 'submitted 计数，出队即释放'],
    ['信箱字节预算', '8 MiB / node', '超额拒收：503 mailbox_full'],
    ['Hub 任务存储', '256 MiB 全局', '含终态 24h 保留；超则 503 storage_full'],
    ['单消息', '1 MiB', '完整 JSON 信封编码后计量'],
    ['投递窗口', '8 / node', 'offered + acknowledged 计数'],
    ['任务离线保留', '7 天', '到期未终态 → failed / expired'],
    ['终态 / 产物保留', '24h', '产物自结果确认起计时'],
    ['执行并发', '默认 2 / node', '由 node 本机配置'],
    ['单产物原始大小', '≤ 640 KiB', '最多 8 条引用，SHA-256 校验'],
    ['协议版本', 'v1', '不兼容版本拒绝连接'],
  ];
  return head('策略容量', '此页只读 · 所有额度与保留策略一目了然。', '<span class="readonly">只读</span>')
    + `<div class="notice warn">高危变更走 hub 配置文件（hub.yaml）并重启；执行配置由各 node 本机主人管理，本页无写入操作。</div>
    <section class="card spaced"><table><thead><tr><th>策略</th><th>当前值</th><th>说明</th></tr></thead>
    <tbody>${rows.map(r => `<tr>${r.map(v => `<td>${esc(v)}</td>`).join('')}</tr>`).join('')}</tbody></table>
    <p class="footer-note">低负载部署目标 ≤ 5 node；此页数据取自协议常量（SPEC 4.6），非运行时快照。</p></section>`;
}

/* ---------- 访问令牌（GitHub 式 PAT） ---------- */
async function loadTokens() {
  const r = await api('GET', '/api/v1/auth/tokens').catch(() => null);
  if (r && r.code === 200) S.tokens = r.data.tokens || [];
  const a = await api('GET', '/api/v1/auth/account').catch(() => null);
  if (a && a.code === 200) S.account = a.data;
}
function tokensPage() {
  const nt = S.newToken;
  const acct = S.account || { initialized: S.tokens.length >= 0 };
  return head('设置', '账户信息与访问令牌：日常用密码登录，令牌只给脚本和其他设备用。')
  + `<section class="card"><h2>账户信息</h2>
    ${S.account?.username ? `<p class="muted">当前用户名：<b>${esc(S.account.username)}</b></p>` : ''}
    <form id="account-form" class="fields" onsubmit="return false">
      <label>当前密码（验证身份）<input id="acc-cur" type="password" autocomplete="current-password" required></label>
      <label>新用户名（不改则留空）<input id="acc-user" autocomplete="username" placeholder="留空表示不修改"></label>
      <label>新密码（不改则留空，至少 8 位）<input id="acc-new" type="password" autocomplete="new-password" placeholder="留空表示不修改"></label>
      <div class="actions" style="align-self:end">${button('保存修改', 'account-update', 'primary')}</div>
    </form>
    <p class="footer-note">忘记密码？用 hub 配置里的管理令牌登录（登录页切换到「恢复通道」），在令牌登录态下本页同样可改。</p></section>`
  + (nt ? `<section class="card spaced"><h2>令牌已创建</h2>
    <div class="notice warn"><b>请立即保存</b>：明文只显示这一次，关闭后无法再查看。</div>
    <pre id="new-token-plain" style="user-select:all">${esc(nt.token)}</pre>
    <div class="actions">${button('复制令牌', 'copy-token', 'primary')}${button('我已保存，关闭', 'token-done', 'link')}</div>
    <p class="footer-note">名称：${esc(nt.name)} · 前缀：${esc(nt.prefix)}…</p></section>` : '')
  + `<section class="card"><div class="row"><h2>已创建的令牌</h2></div>
    <form id="token-form" class="fields">
      <label>用途名称（必填）<input id="token-name" placeholder="例如：运维台电脑 · 只读巡检" required maxlength="64"></label>
      <div class="actions" style="align-self:end"><button class="primary" type="submit">生成新令牌</button></div>
    </form>
    <div class="table-wrap spaced"><table><thead><tr><th>名称</th><th>前缀</th><th>创建时间</th><th>最近使用</th><th>状态</th><th></th></tr></thead><tbody>
    ${S.tokens.length ? S.tokens.map(t => `<tr>
      <td><b>${esc(t.name)}</b></td>
      <td><code>${esc(t.prefix)}…</code></td>
      <td><small>${esc(fmtTime(t.created_at))}</small></td>
      <td><small>${t.last_used_at ? esc(fmtTime(t.last_used_at)) : '从未使用'}</small></td>
      <td>${t.revoked ? '<span class="badge offline">已撤销</span>' : '<span class="badge online">有效</span>'}</td>
      <td class="table-action">${t.revoked ? '' : button('撤销', 'token-revoke', 'danger', `data-id="${esc(t.id)}" data-name="${esc(t.name)}"`)}</td>
    </tr>`).join('') : '<tr><td colspan="6" class="muted">还没有创建过令牌。</td></tr>'}
    </tbody></table></div>
    <p class="footer-note">令牌以 <code>Authorization: Bearer</code> 方式调用 API，权限与你的登录相同；撤销立即生效，不影响已登录的浏览器会话。</p></section>`;
}
async function submitToken(form) {
  const name = $('#token-name')?.value.trim();
  if (!name) { toast('请先填写令牌用途名称'); return; }
  const r = await api('POST', '/api/v1/auth/tokens', { name }).catch(() => null);
  if (r && r.code === 201) {
    S.newToken = r.data;
    await loadTokens();
    render();
    toast('令牌已生成 · 请立即保存明文');
  } else toast('创建失败：' + (r ? errLine(r.data) : '网络异常'));
}

/* ---------- 动作表（事件委托） ---------- */
const ACT = {
  go: b => go(b.dataset.page, b.dataset.status || ''),
  fchip: b => { S.f.status = b.dataset.fchip || ''; render(); },
  'clear-filters': () => { S.f = { status: '', trace: '', by: '', error: '', from: '', to: '' }; render(); toast('已清除筛选'); },
  close: () => closeLayer(),
  detail: b => { closeLayer(); openTask(b.dataset.id); },
  'go-tasks': () => go('tasks'),
  'go-review-queue': () => go('tasks', 'needs_review'),
  enroll: () => enrollModal(),
  'enroll-done': () => { closeLayer(); render(); toast('配置已交接 · 节点上线后将出现在列表'); },
  'copy-config': () => copyText([$('#enroll-snippet')?.textContent, $('#enroll-node-snippet')?.textContent].filter(Boolean).join('\n\n'), '接入配置片段'),
  'copy-token': () => copyText($('#new-token-plain')?.textContent || '', '令牌明文'),
  'token-done': () => { S.newToken = null; render(); toast('已关闭 · 明文不再显示'); },
  'switch-login': () => { loginMode = loginMode === 'password' ? 'token' : 'password'; loginPageRendered = null; route(); },
  'account-update': async () => {
    const cur = $('#acc-cur')?.value || '', nu = $('#acc-user')?.value.trim() || '', np = $('#acc-new')?.value || '';
    if (!cur) { toast('请先输入当前密码'); return; }
    if (!nu && !np) { toast('请填写新用户名或新密码'); return; }
    const r = await api('POST', '/api/v1/auth/account/update', { current_password: cur, new_username: nu || undefined, new_password: np || undefined }).catch(() => null);
    if (r && r.code === 200) {
      S.account = r.data.account;
      render(); toast('账户信息已更新');
    } else toast('更新失败：' + (r ? errLine(r.data) : '网络异常'));
  },
  'token-revoke': b => openLayerPlain(`${modalHead('撤销令牌 · ' + esc(b.dataset.name))}<div class="notice warn">撤销后使用此令牌的脚本或设备将立即失去访问权限（不可恢复）。可随时创建新令牌替代。</div><div class="actions spaced">${button('确认撤销', 'token-revoke-confirm', 'danger', `data-id="${esc(b.dataset.id)}"`)}${button('取消', 'close', 'link')}</div>`),
  'token-revoke-confirm': async b => {
    const r = await api('DELETE', '/api/v1/auth/tokens/' + encodeURIComponent(b.dataset.id)).catch(() => null);
    if (r && r.code === 200) {
      await loadTokens(); closeLayer(); render(); toast('令牌已撤销');
    } else toast('撤销失败：' + (r ? errLine(r.data) : '网络异常'));
  },
  revoke: b => openLayerPlain(`${modalHead('吊销 · ' + esc(b.dataset.node))}<div class="notice warn">演示：V0 无撤销机制</div><p class="spaced">此按钮不改变节点或会话状态。签发 / 撤销 / 轮换自动化属后续阶段；当前请由管理员直接修改 hub.yaml 的 nodes: 段并重启 hub。</p>${button('下一步：返回节点列表 →', 'close', 'link')}`),
  'drawer-refresh': async b => {
    const r = await api('GET', '/api/v1/tasks/' + encodeURIComponent(b.dataset.id)).catch(() => null);
    if (r && r.code === 200) {
      const i = S.tasks.findIndex(t => t.id === r.data.id);
      if (i >= 0) S.tasks[i] = r.data;
      S.drawer = { id: r.data.id, task: r.data };
      syncDrawer(true); toast('详情已刷新');
    } else toast(r && r.code === 404 ? '该任务已归档（终态保留 24 小时）' : '刷新失败：' + (r ? errLine(r.data) : '网络异常'));
  },
  export: () => {
    const list = filteredTasks();
    exportJSON(list, 'swarmmind-tasks.json');
    toast(`已导出 ${list.length} 条 · 终态保留 24h，请及时归档 —— 下一步：继续监管或核对`);
  },
  'export-one': b => {
    const t = S.tasks.find(x => x.id === b.dataset.id) || S.drawer?.task;
    if (t) { exportJSON(t, 'swarmmind-' + t.id + '.json'); toast('已导出任务 ' + t.id); }
  },
  artifact: async b => {
    const id = b.dataset.id, idx = Number(b.dataset.index);
    b.disabled = true; b.textContent = '取回中…';
    try {
      const resp = await fetch(`/api/v1/tasks/${encodeURIComponent(id)}/artifacts/${idx}`, { credentials: 'same-origin' });
      if (resp.status === 401) { sessionExpired(); return; }
      if (!resp.ok) {
        const d = await resp.json().catch(() => null);
        const msg = { 404: '产物不存在：任务无终态结果、下标越界或文件已过保留期 · artifact_not_found', 503: '执行 node 不在线，暂无法取回；重连后可再试 · node_offline', 504: '中继取回超时（30s），请稍后重试 · fetch_timeout', 409: 'SHA-256 校验不一致，禁止交付 · artifact_mismatch' }[resp.status] || errLine(d);
        toast('取回失败：' + msg);
      } else {
        const blob = await resp.blob();
        const path = resp.headers.get('X-Artifact-Path') || 'artifact-' + idx;
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url; a.download = path.split('/').pop();
        document.body.append(a); a.click(); a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        toast('产物已取回 · SHA-256 经 node 校验 —— 下一步：可继续取回其余产物');
      }
    } catch (e) {
      toast('结果未知 · 网络中断：文件可能未下载完整，请重试');
    }
    b.disabled = false; b.textContent = '取回';
  },
  review: async b => {
    const id = b.dataset.id, action = b.dataset.reviewAction;
    const note = $('#review-note')?.value.trim();
    if (!note) { toast('请先填写核对依据（记录检查过的证据）'); $('#review-note')?.focus(); return; }
    const orig = b.textContent;
    document.querySelectorAll('[data-act="review"]').forEach(x => { x.disabled = true; });
    b.textContent = '登记中…';
    try {
      const r = await api('POST', `/api/v1/tasks/${encodeURIComponent(id)}/review`, { action, note });
      if (r.code === 200) {
        const i = S.tasks.findIndex(t => t.id === id);
        if (i >= 0) S.tasks[i] = r.data;
        S.drawer = { id, task: r.data };
        render(); syncDrawer(true);
        toast(`核对已登记（${REVIEW_ACTIONS[action]}）· 任务保持 needs_review —— 下一步：回到待办列表`);
      } else {
        toast('登记失败：' + errLine(r.data));
        document.querySelectorAll('[data-act="review"]').forEach(x => { x.disabled = false; });
        b.textContent = orig;
      }
    } catch (e) {
      if (!e.unauthorized) {
        toast(e.network ? '结果未知 · 网络中断：核对可能未登记，请点「刷新」确认后重试' : '请求失败，请重试');
        document.querySelectorAll('[data-act="review"]').forEach(x => { x.disabled = false; });
        b.textContent = orig;
      }
    }
  },
  'ctrl-retry': () => { S.C.stage = 'idle'; submitControl(); },
  'ctrl-abandon': () => openLayerPlain(`${modalHead('放弃等待，不代表撤销请求')}<p>原请求可能已在 hub 持久化。表单与幂等键将保留：回到本页可「同键重试」安全恢复。</p><div class="notice warn">V0 不支持取消。请勿换用新幂等键重复提交同一巡检。</div><div class="actions">${button('保留幂等键，解锁表单', 'ctrl-abandon-confirm', 'primary')}</div>`),
  'ctrl-abandon-confirm': () => {
    S.C.stage = 'idle'; closeLayer(); render();
    toast('已保留幂等键与表单 · 可稍后同键重试');
  },
  'ctrl-new': () => { S.C = freshControl(); render(); toast('新巡检表单已就绪（新幂等键）'); },
  'ctrl-refresh': async () => {
    const b = S.C.batch;
    if (!b) return;
    const r = await api('GET', '/api/v1/control-operations/' + encodeURIComponent(b.batch_id)).catch(() => null);
    if (r && r.code === 200) { S.C.batch = r.data; render(); toast('批次状态已刷新'); }
    else toast('批次查询失败：' + (r ? errLine(r.data) : '网络异常'));
  },
};
/* openLayerPlain：普通弹层（吊销/放弃等，非任务抽屉，不参与轮询同步） */
let modalOpen = false;
function openLayerPlain(content) {
  modalOpen = true;
  $('#layer').innerHTML = `<div class="overlay" data-backdrop="true"><section class="modal" role="dialog" aria-modal="true" tabindex="-1">${content}</section></div>`;
  $('#layer [role=dialog]').focus();
}

/* ---------- 文件导出 / 复制 ---------- */
function exportJSON(data, name) {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = name;
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
async function copyText(value, label) {
  try {
    await navigator.clipboard.writeText(value);
    toast(label + '已复制 · 下一步：粘贴到对应配置文件并重启');
  } catch {
    openLayerPlain(`${modalHead('手动复制' + label)}<p>浏览器未允许剪贴板访问，可选中以下内容复制。</p><textarea readonly rows="12">${esc(value)}</textarea>${button('完成，返回 →', 'close', 'link')}`);
  }
}

/* ---------- 事件绑定（委托） ---------- */
const PAGES = { overview: overviewPage, nodes: nodesPage, tasks: tasksPage, control: controlPage, policy: policyPage, tokens: tokensPage };
function bindEvents() {
  window.addEventListener('hashchange', route);
  document.addEventListener('click', e => {
    const b = e.target.closest('button');
    if (b?.id === 'logout') {
      closeLayer();
      S.authed = false; S.loaded = false; S.C = freshControl();
      location.hash = '#/login';
      route();
      toast('已退出登录 · 服务端会话仍有效至过期（V0 无撤销端点）');
      return;
    }
    const btn = e.target.closest('[data-act]');
    if (btn && !btn.disabled) { const fn = ACT[btn.dataset.act]; if (fn) { fn(btn); return; } }
    if (e.target.dataset?.backdrop) { closeLayer(); return; }
    const tr = e.target.closest('tr[data-task]');
    if (tr) openTask(tr.dataset.task);
  });
  document.addEventListener('submit', e => {
    e.preventDefault();
    if (e.target.id === 'login-form') doLogin(e.target);
    if (e.target.id === 'control-form') submitControl();
    if (e.target.id === 'token-form') submitToken(e.target);
    if (e.target.id === 'setup-form') doSetup(e.target);
    if (e.target.id === 'pw-login-form') doPasswordLogin($('#login-user')?.value.trim(), $('#login-pass')?.value || '');
  });
  document.addEventListener('input', e => {
    const k = e.target.dataset.f;
    if (k) { S.f[k] = e.target.value; preserveFocus(() => render()); return; }
    if (e.target.id === 'enroll-id') updateEnrollSnippet();
    if (e.target.id === 'ctrl-stdin') S.C.stdin = e.target.value;
  });
  document.addEventListener('change', e => {
    const k = e.target.dataset.f;
    if (k) { S.f[k] = e.target.value; render(); return; }
    if (e.target.id === 'ctrl-agent') {
      const node = e.target.value.split('/')[0];
      const hint = document.querySelector('#ctrl-node-hint');
      if (hint) hint.textContent = node;
      return;
    }
  });
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') { closeLayer(); return; }
    const tr = e.target.closest?.('tr[data-task]');
    if (tr && e.target === tr && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); openTask(tr.dataset.task); }
  });
  setInterval(pollTick, 5000);
}

/* ---------- 启动：会话探测 → 恢复控制台或落登录页 ---------- */
async function init() {
  bindEvents();
  try {
    await api('GET', '/api/v1/nodes');
    S.authed = true;
    await loadAll();
  } catch (e) { /* 未登录 / 网络故障：登录页兜底 */ }
  route();
}
init();
