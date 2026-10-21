'use strict';
/* SwarmMind Node 本地控制台（Loop 7，INTERACTION 客户端三旅程）。
 * 数据面：本机回环 /api/v1/*（Bearer local_token，令牌持于 localStorage）。
 * 页面：总览（旅程③运维）· 我的服务（旅程①服务提供者：exposed 开关三态）·
 * 派任务·我的任务（旅程②消费者：能力目录派单 + 留痕跟踪）。与 hub 控制台
 * 同交互闭环（INTERACTION 四：300ms 反馈、终态带下一步、结果未知锁表单、
 * 技术态配人话双语）。无框架原生 JS，hash 路由 SPA。 */

const $ = s => document.querySelector(s);
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

/* ---------- 展示词典：技术态永远配人话（INTERACTION 四.5） ---------- */
const STATES = {
  submitted: '已提交', offered: '已投递', acknowledged: '已接收',
  running: '执行中', completed: '已完成', failed: '执行失败', needs_review: '待人工核对',
};
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
const TERMINAL = new Set(['completed', 'failed', 'needs_review']);

/* ---------- 全局状态 ---------- */
const TOKEN_KEY = 'swarmmind_node_token';
const S = {
  token: localStorage.getItem(TOKEN_KEY) || '',
  authed: false,
  page: 'overview',
  nodeID: '',
  status: null,            // GET /api/v1/local/status
  agents: [],              // GET /api/v1/local/agents（本机 ExecProfile 视图）
  tasks: [],               // GET /api/v1/local/tasks（我的任务留痕）
  live: {},                // task_id → 最近一次实况状态（详情/列表徽章）
  catalog: [],             // 派单目录（全网 exposed 能力）
  snap: '',                // 轮询数据指纹：未变化不重绘
  drawer: null,            // 打开的任务详情 {id, timer}
  f: { stage: 'idle', target: '', stdin: '', idem: newIdem(), message: '' },   // 派单表单状态机
  focusForm: false,
};
function newIdem() {
  return 'web-' + (globalThis.crypto?.randomUUID?.() || Date.now() + '-' + Math.random().toString(36).slice(2));
}

/* ---------- API 访问（Bearer 本机管理令牌） ---------- */
/* 返回 {code, data}；网络层失败抛 {network:true}（结果未知），401 抛前先回令牌页。 */
async function api(method, path, body, opt = {}) {
  let resp;
  try {
    resp = await fetch(path, {
      method,
      headers: {
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...(S.token ? { 'Authorization': 'Bearer ' + S.token } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw { network: true, cause: e };
  }
  const data = await resp.json().catch(() => null);
  if (resp.status === 401 && !opt.noGate) {
    S.authed = false;
    render();
    throw { unauthorized: true, data };
  }
  return { code: resp.status, data };
}
function errLine(data) {
  if (!data) return '无应答体';
  return esc(data.error || 'error') + ' · ' + esc(data.message || '');
}

/* ---------- 数据加载与轮询（页面可见时 5s） ---------- */
async function loadAll() {
  const [st, ag, tk, dir] = await Promise.all([
    api('GET', '/api/v1/local/status'),
    api('GET', '/api/v1/local/agents'),
    api('GET', '/api/v1/local/tasks'),
    api('GET', '/api/v1/agents'),
  ]);
  S.nodeID = st.data?.node_id || '';
  S.status = st.data || null;
  S.agents = ag.data?.agents || [];
  S.tasks = tk.data?.tasks || [];
  const net = dir.data?.network;
  const list = Array.isArray(net?.agents) ? net.agents : (dir.data?.local || []);
  S.catalog = list.filter(e => e.card?.exposed);
  // 列表徽章用实况：最近 8 条留痕并发补查（hub 是状态真相源，日志不缓存状态）
  const top = S.tasks.slice(0, 8);
  const live = await Promise.all(top.map(e =>
    api('GET', '/api/v1/tasks/' + encodeURIComponent(e.task_id)).then(r => r.data).catch(() => null)));
  top.forEach((e, i) => { if (live[i]) S.live[e.task_id] = live[i]; });
  const snap = JSON.stringify([S.status, S.agents, S.tasks, S.catalog, Object.keys(S.live).length]);
  const changed = snap !== S.snap;
  S.snap = snap;
  return changed;
}
async function pollTick() {
  if (!S.authed || document.visibilityState !== 'visible') return;
  try {
    if (await loadAll()) render();
  } catch (e) { /* 401 已由 api() 处理；其余静默，下轮再试 */ }
}

/* ---------- 令牌门（本机管理令牌 = 本机控制台的登录） ---------- */
async function tryToken(token) {
  S.token = token;
  try {
    const r = await api('GET', '/api/v1/local/status', undefined, { noGate: true });
    if (r.code === 200) {
      localStorage.setItem(TOKEN_KEY, token);
      S.authed = true;
      S.snap = '';
      render();
      loadAll().then(c => { if (c) render(); }).catch(() => {});
      return;
    }
    toast('令牌未被接受 · ' + errLine(r.data));
  } catch (e) {
    toast('本机 API 不可达，确认 node 已启动');
  }
  S.token = localStorage.getItem(TOKEN_KEY) || '';
}

/* ---------- 路由（hash SPA） ---------- */
const PAGES = [['overview', '总览'], ['services', '我的服务'], ['tasks', '派任务·我的任务']];
function route() {
  const h = (location.hash || '#/overview').replace(/^#\//, '');
  S.page = PAGES.some(p => p[0] === h) ? h : 'overview';
  render();
}
window.addEventListener('hashchange', route);

/* ---------- 基础组件 ---------- */
function button(label, act, cls = '', inner = '', attr = '') {
  return `<button data-act="${act}" class="${cls}" ${attr} ${inner}>${label}</button>`;
}
function badge(status) {
  const s = String(status || 'submitted');
  return `<span class="badge ${esc(s)}">${esc(STATES[s] || s)} · ${esc(s)}</span>`;
}
function timeShort(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d) ? esc(iso) : d.toLocaleTimeString('zh-CN', { hour12: false });
}
function timeFull(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d) ? esc(iso) : d.toLocaleString('zh-CN', { hour12: false });
}

/* ---------- 布局（shell + 侧栏导航） ---------- */
function layout(content) {
  const online = S.status?.online;
  return `<div class="shell"><aside><div class="workspace"><strong>${esc(S.nodeID || 'node')}</strong><small class="muted">本机控制台</small></div>
  <nav>${PAGES.map(([id, label]) => `<button data-go="${id}" class="${S.page === id ? 'active' : ''}"><span class="navnum">${['①', '②', '③'][PAGES.findIndex(p => p[0] === id)] || ''}</span>${label}</button>`).join('')}</nav>
  <div class="aside-bottom">连接 hub：${online ? '<span class="badge online">在线</span>' : '<span class="badge offline">离线</span>'}<br>
  <span style="opacity:.75">已接收 · acknowledged<br>本机回环 · 仅本机进程可达</span></div></aside>
  <main>${content}</main></div>`;
}

/* ---------- 渲染 ---------- */
function render() {
  $('#conn-chip').hidden = !S.authed;
  $('#logout').hidden = !S.authed;
  $('#who').textContent = S.authed ? 'Bearer 令牌已就绪' : '';
  if (S.authed && S.status?.online != null) {
    const chip = $('#conn-chip');
    chip.className = 'badge ' + (S.status.online ? 'online' : 'offline');
    chip.textContent = S.status.online ? 'hub 在线 · epoch ' + S.status.epoch : 'hub 离线';
  }
  $('#app').innerHTML = !S.authed ? gateView() : layout(
    S.page === 'overview' ? overviewPage() : S.page === 'services' ? servicesPage() : tasksPage());
  if (S.drawer) renderDrawer();
}
function gateView() {
  return `<main><div class="card login"><div class="mark">⌘</div><h1>SwarmMind Node 本地控制台</h1>
  <p class="muted">本机管理令牌鉴权（Bearer）。令牌来自 node.yaml 的 <code>local_token</code>；未配置时 node 首次启动已生成，见启动日志或 <code>data_dir/local_token</code> 文件。</p>
  <label>本机管理令牌<input id="gate-token" type="password" placeholder="local_token" autocomplete="off"></label>
  ${button('进入控制台', 'gate-enter', 'primary')}
  <p class="footer-note">仅本机回环可达；令牌只存本浏览器（localStorage），不出本机。</p></div></main>`;
}

/* ---------- 页面①：总览（旅程③运维 + 三步引导空态） ---------- */
function overviewPage() {
  const st = S.status;
  const led = st?.ledger || {};
  const mb = st?.mailbox || {};
  const head = `<div class="page-head"><div><div class="eyebrow">OVERVIEW</div><h1>总览</h1>
    <p class="muted">本机 node 状态与最近任务；下一步：<b>＋派单</b> 把工作交给网内能力。</p></div>
    <div class="actions">${button('复制诊断信息', 'copy-diag')}${button('＋ 派单', 'go-tasks', 'primary')}</div></div>`;
  if (!S.agents.length) {
    return head + `<section class="card"><h2>三步把本机接入协作网</h2>
      <p class="muted">本机还没有任何 agent。配置是文件化的（V0 不做网页表单编辑，明说只读）：</p>
      <div class="step"><span>1</span><div><b>编辑 node.yaml</b><div class="muted">在 <code>agents:</code> 段登记本机 agent（command / workdir / timeout / 白名单），完整字段见 <code>configs/node.example.yaml</code>。</div></div></div>
      <div class="step"><span>2</span><div><b>重启 node</b><div class="muted">配置随进程生效；重启不丢已确认的任务结果。</div></div></div>
      <div class="step"><span>3</span><div><b>回到「我的服务」打开暴露开关</b><div class="muted">exposed=true 后能力进入全网目录，别的机器就能派单给它。</div></div></div></section>`;
  }
  return head + `
  <div class="grid cols4">
    <section class="card"><h3>连接状态</h3><div class="metric"><span class="badge ${st?.online ? 'online' : 'offline'}">${st?.online ? '在线 · online' : '离线 · offline'}</span></div>
      <small class="muted">epoch ${esc(st?.epoch ?? 0)} · 掉线自动重连，重连后代次 +1</small></section>
    <section class="card"><h3>执行账本（三态）</h3><div class="mini-stat spaced"><span><b>${esc(led.accepted ?? 0)}</b><small>accepted</small></span><span><b>${esc(led.started ?? 0)}</b><small>started</small></span><span><b>${esc(led.finished ?? 0)}</b><small>finished</small></span></div>
      <small class="muted">重启恢复：started 未完 → needs_review，绝不自动重跑</small></section>
    <section class="card"><h3>信箱余量</h3><div class="metric">${mb.error ? '—' : esc(mb.remaining ?? '—')}<small> / ${esc(mb.quota ?? '?')} 条</small></div>
      <small class="muted">${mb.error ? esc(mb.error) : `hub 侧离线信箱：已占 ${esc(mb.used ?? 0)} 条，满则新提交暂拒（503）`}</small></section>
    <section class="card"><h3>最近同步</h3><div class="metric">${st?.last_sync_at ? timeShort(st.last_sync_at) : '从未'}</div>
      <small class="muted">${st?.last_sync_at ? '最近一次注册成功 · ' + esc(timeFull(st.last_sync_at)) : '注册成功后这里显示时间'}</small></section>
  </div>
  ${recentTasksCard()}`;
}
function recentTasksCard() {
  const rows = S.tasks.slice(0, 8);
  return `<section class="card"><div class="row"><h2>最近任务（本机派出的）</h2><small>${S.tasks.length} 条留痕</small></div>
  ${rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>任务</th><th>状态</th></tr></thead><tbody>
    ${rows.map(e => { const t = S.live[e.task_id]; return `<tr data-task="${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}<small>${esc(e.task_id)}</small></td><td>${esc((e.idem_key || '—')).slice(0, 24)}</td><td>${t ? badge(t.status) : '<span class="badge offline">状态待查</span>'}</td></tr>`; }).join('')}
  </tbody></table></div><p class="footer-note">点击行查看详情；终态保留 24h，任务真相在 hub（本页实时补查）。</p>`
    : `<div class="empty"><h2>还没有派过任务</h2><p class="muted">去「派任务」选一个网内能力，描述要做的事即可。</p>${button('＋ 派单', 'go-tasks', 'primary')}</div>`}</section>`;
}

/* ---------- 页面②：我的服务（旅程①服务提供者：开关三态 + 只读配置） ---------- */
function servicesPage() {
  const head = `<div class="page-head"><div><div class="eyebrow">MY SERVICES</div><h1>我的服务</h1>
    <p class="muted">本机 agent 即服务：暴露开关主权在本机；关闭后<b>新任务</b>不再准入（404），已在执行/排队的任务照常跑到终态。</p></div></div>`;
  if (!S.agents.length) {
    return head + `<section class="card"><div class="empty"><h2>还没有可暴露的服务</h2>
      <p class="muted">在 node.yaml 的 <code>agents:</code> 段登记后重启 node，这里就会出现开关。</p>${button('查看总览引导', 'go-overview', 'primary')}</div></section>`;
  }
  return head + S.agents.map(a => svcCard(a)).join('') +
    `<p class="footer-note">完整配置（command / workdir / 白名单 / agent_args 等）请编辑 node YAML 并重启 node 生效；本页只读展示，不做表单编辑（V0 明确不做动态能力下发）。开关变更即时持久化到 <code>data/agents-state.json</code>，重启保持。</p>`;
}
function svcCard(a) {
  const on = !!a.exposed;
  const syncNote = a._pending
    ? `<div class="notice warn spaced" id="sync-${esc(a.name)}">同步中…</div>`
    : a._synced === false
      ? `<div class="notice warn spaced" id="sync-${esc(a.name)}"><b>本机已生效 · 待同步</b>：hub 暂不可达，重连注册后自动生效（已排队任务不受影响）。</div>`
      : a._synced === true
        ? `<div class="notice ok spaced" id="sync-${esc(a.name)}"><b>已同步 hub · 网内生效</b>：${on ? '能力已进入全网目录。' : '新任务将不再准入；注册表保留一条下线登记。'}</div>`
        : '';
  return `<section class="card svc-card ${on ? '' : 'off'}" id="svc-${esc(a.name)}">
    <div class="row"><div><h2 style="margin-bottom:4px">${esc(a.name)} <span class="badge">${esc(a.kind)}</span> <span class="badge">${on ? 'exposed · 网内可调用' : 'not exposed · 仅本机'}</span></h2>
      <small class="muted">${esc(a.description || '（无描述）')} · 权限模式 <code>${esc(a.permission_mode || '—')}</code> · 今日已执行 <b>${esc(a.today_tasks ?? 0)}</b> 次</small></div>
    <div class="actions"><div class="toggle" role="group" aria-label="暴露开关">
      ${button('暴露', `expose-on:${esc(a.name)}`, 'on-btn' + (on ? ' selected' : ''), '', `data-agent="${esc(a.name)}"`)}
      ${button('停止暴露', `expose-off:${esc(a.name)}`, 'off-btn' + (on ? '' : ' selected'), '', `data-agent="${esc(a.name)}"`)}
    </div></div></div>${syncNote}
    <details><summary>配置详情（只读 · 来自本机 ExecProfile）</summary>
      <div class="table-wrap"><table><tbody>
        <tr><th>command</th><td><code>${esc(a.command)}</code> ${a.allow_args ? '' : '<span class="readonly">allow_args=false：任务参数将被拒绝</span>'}</td></tr>
        <tr><th>workdir</th><td><code>${esc(a.workdir)}</code></td></tr>
        <tr><th>timeout_sec</th><td>${esc(a.timeout_sec)}s<small>唯一超时源；超时整组终止进程组</small></td></tr>
        <tr><th>allow_args</th><td>${a.allow_args ? 'true · 接受任务参数（解释器类=全参数授权，选用即知情）' : 'false · 非空参数 → policy_denied'}</td></tr>
        <tr><th>allow_env</th><td>${(a.allow_env || []).length ? '<code>' + a.allow_env.map(esc).join('</code> <code>') + '</code>' : '（空 · 任务不可覆盖环境变量）'}</td></tr>
        <tr><th>config_home</th><td><code>${esc(a.config_home || '（空 = data_dir/home）')}</code><small>进程 HOME 指此，与 workdir 分离；凭证不出本机</small></td></tr>
        <tr><th>agent_args</th><td>${(a.agent_args || []).length ? '<code>' + a.agent_args.map(esc).join(' ') + '</code>' : '（无固定附加参数）'}<small>固定拼在任务参数前，任务不可改</small></td></tr>
        <tr><th>permission_mode</th><td>${esc(a.permission_mode || '—')}<small>映射 agent 自带权限机制，调用方以此为准</small></td></tr>
      </tbody></table></div>
      <p class="footer-note">这些字段永不离开本机；对外只可见能力卡（名称/描述/权限模式/开关）。</p></details></section>`;
}
async function setExposed(name, exposed, btn) {
  const card = $('#svc-' + CSS.escape(name));
  const note = card?.querySelector(`#sync-${CSS.escape(name)}`);
  if (btn) btn.disabled = true;                     // 300ms 反馈：立即锁按钮
  if (note) note.outerHTML = `<div class="notice spaced" id="sync-${esc(name)}">同步中…</div>`;
  try {
    const r = await api('PATCH', `/api/v1/local/agents/${encodeURIComponent(name)}/exposed`, { exposed });
    if (r.code === 200) {
      const a = S.agents.find(x => x.name === name);
      if (a) { a.exposed = r.data.exposed; a._synced = !!r.data.hub_synced; }
      toast(exposed ? '已暴露 · 即时生效' : '已停止暴露 · 新任务不再准入');
    } else {
      toast('开关失败 · ' + errLine(r.data));
    }
  } catch (e) {
    toast(e.network ? '本机 API 不可达' : '开关失败');
  }
  render();
  pollTick();
}

/* ---------- 页面③：派任务·我的任务（旅程②消费者） ---------- */
function tasksPage() {
  const c = S.f;
  const head = `<div class="page-head"><div><div class="eyebrow">TASKS</div><h1>派任务 · 我的任务</h1>
    <p class="muted">从能力目录选目标 → 一段话描述要做的事 → 提交；结果在这里跟踪与取回。</p></div></div>`;
  const opts = S.catalog.map(e => {
    const v = e.node_id + '|' + e.card.name;
    return `<option value="${esc(v)}" ${c.target === v ? 'selected' : ''}>${esc(e.card.name)} @ ${esc(e.node_id)} — ${esc(e.card.description || '（无描述）')} · ${esc(e.card.permission_mode || '—')}${e.online ? '' : ' · 离线（任务进信箱，重连后执行）'}</option>`;
  }).join('');
  const target = S.catalog.find(e => e.node_id + '|' + e.card.name === c.target);
  const stageView = {
    idle: '',
    submitting: `<div class="statusline"><span class="badge offered">提交中…</span></div>`,
    acked: `<div class="notice ok"><b>已确认 · acknowledged</b>：任务已持久化，任务号 <code>${esc(c.taskId || '')}</code>。${button('查看任务', 'open-submitted', 'link')}</div>`,
    failed: `<div class="notice warn"><b>提交被拒绝</b>：${c.message}${button('修正后重试', 'form-retry', 'link')}</div>`,
    unknown: `<div class="notice warn"><b>结果未知 · 表单已锁定</b>：${esc(c.message)}<br>同一幂等键（<code>${esc(c.idem)}</code>）重试不会重复执行：${button('同键重试', 'form-resend', 'link')} · ${button('放弃这单', 'form-abandon', 'link')}</div>`,
  }[c.stage] || '';
  const formLocked = c.stage === 'submitting' || c.stage === 'unknown' || c.stage === 'acked';
  return head + `
  <section class="card" id="task-form"><h2>派任务</h2>
    ${S.catalog.length ? `
    <label>能力目录（网内已暴露的服务）<select id="f-target" ${formLocked ? 'disabled' : ''}><option value="" ${!c.target ? 'selected' : ''} disabled>选择一个能力…</option>${opts}</select></label>
    <div class="notice" ${target ? '' : 'hidden'}>${target ? `<b>${esc(target.card.name)} @ ${esc(target.node_id)}</b> · ${esc(target.card.description || '（无描述）')} · 权限模式 <code>${esc(target.card.permission_mode || '—')}</code> · ${target.online ? '<span class="badge online">在线</span>' : '<span class="badge offline">离线 · 排队等它回来</span>'}` : ''}</div>
    <label>任务描述（一段话讲清要做什么；以 prompt_text 交付给对方 agent）<textarea id="f-stdin" placeholder="例：审查这段代码的并发安全性，指出竞态并给出修复建议…" ${formLocked ? 'disabled' : ''}>${esc(c.stdin)}</textarea></label>
    <div class="row"><small class="muted">幂等键 <code>${esc(c.idem)}</code> · 结果未知时可同键重试，不会重复执行</small>
    ${c.stage === 'acked' ? button('再派一单', 'form-retry', 'primary') : button(c.stage === 'submitting' ? '提交中…' : '确认并提交', 'form-submit', 'primary', c.stage === 'submitting' ? 'disabled' : '')}</div>
    <div class="statusline">${stageView}</div>`
      : `<div class="empty"><h2>能力目录为空</h2><p class="muted">网内还没有已暴露的服务（或 hub 目录代理未配置）。可以先在「我的服务」暴露本机能力。</p>${button('去看我的服务', 'go-services', 'primary')}</div>`}
  </section>
  ${myTasksCard()}`;
}
function myTasksCard() {
  const rows = S.tasks;
  return `<section class="card"><div class="row"><h2>我的任务</h2><small>${rows.length} 条留痕 · 本机派出的全部任务</small></div>
  ${rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>任务号</th><th>状态</th><th></th></tr></thead><tbody>
    ${rows.map(e => { const t = S.live[e.task_id]; return `<tr data-task="${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}</td><td><code>${esc(e.task_id)}</code></td><td>${t ? badge(t.status) : '<span class="badge offline">点击查询</span>'}</td><td class="table-action">${button('详情', 'open-task:' + esc(e.task_id), 'link')}</td></tr>`; }).join('')}
  </tbody></table></div><p class="footer-note">状态实时补查自 hub；终态保留 24h，过期任务号查询即 404。</p>`
    : `<div class="empty"><h2>还没有任务记录</h2><p class="muted">上方表单派第一单。</p>${button('＋ 派单', 'focus-form', 'primary')}</div>`}</section>`;
}

/* ---------- 派单表单动作 ---------- */
async function submitForm() {
  const [targetNode, targetAgent] = (S.f.target || '').split('|');
  if (!targetNode || !S.f.stdin.trim()) { toast('先选目标并填写任务描述'); return; }
  S.f.stage = 'submitting'; render();
  try {
    const r = await api('POST', '/api/v1/tasks', {
      target_node: targetNode, target_agent: targetAgent,
      input: { stdin: S.f.stdin }, submitted_by: 'console',
      idem_key: S.f.idem,
    });
    if (r.code === 202 || r.code === 200) {
      S.f.stage = 'acked'; S.f.taskId = r.data?.task_id || '';
      toast(r.data?.dup ? '已确认（幂等命中原任务）' : '已确认 · acknowledged');
      try { S.snap = ''; await loadAll(); } catch (e) { /* 刷新失败交给轮询 */ }
      render(); return;
    }
    S.f.stage = 'failed';
    S.f.message = errLine(r.data);
    if (r.data?.error === 'hub_unreachable') { S.f.stage = 'unknown'; S.f.message = 'hub 暂不可达，提交是否生效不确定。'; }
  } catch (e) {
    if (e.unauthorized) return; // 401 已回令牌门
    S.f.stage = 'unknown';
    S.f.message = '网络层失败，提交是否生效不确定。';
  }
  render();
}

/* ---------- 任务详情抽屉 ---------- */
async function openDrawer(taskID) {
  closeDrawer();
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><div class="drawer" id="drawer"><p class="muted">加载中…</p></div></div>`;
  $('#layer').addEventListener('click', e => { if (e.target.classList.contains('overlay')) closeDrawer(); });
  S.drawer = { id: taskID, timer: null };
  await refreshDrawer();
  if (!S.drawer) return; // 首刷前已被关闭
  S.drawer.timer = setInterval(() => {
    const t = S.drawer?.task;
    if (S.drawer && (!t || !TERMINAL.has(t.status))) refreshDrawer();
  }, 5000);
}
function closeDrawer() {
  if (S.drawer?.timer) clearInterval(S.drawer.timer);
  S.drawer = null;
  $('#layer').innerHTML = '';
}
async function refreshDrawer() {
  if (!S.drawer) return;
  const r = await api('GET', '/api/v1/tasks/' + encodeURIComponent(S.drawer.id)).catch(() => null);
  if (!r || r.code !== 200) {
    $('#drawer').innerHTML = `<div class="row"><h2>${esc(S.drawer.id)}</h2>${button('×', 'close-drawer', 'close')}</div>
      <div class="notice warn">任务查询失败${r ? ' · ' + errLine(r.data) : '（网络）'}。可能已过保留期（终态 24h / 离线 7 天）。</div>
      <p class="footer-note">下一步：回到${button('我的任务', 'close-drawer', 'link')}列表。</p>`;
    return;
  }
  const t = r.data;
  S.drawer.task = t;
  S.live[t.id] = t;
  const res = t.result;
  const errTxt = res?.error ? ERRORS[res.error.split(';')[0]] || res.error : '';
  $('#drawer').innerHTML = `<div class="row"><h2>${esc(t.target_agent)} @ ${esc(t.target_node)} ${badge(t.status)}</h2>${button('×', 'close-drawer', 'close')}</div>
    <p class="muted small">任务号 <code>${esc(t.id)}</code> · 追踪 <code>${esc(t.trace_id || '—')}</code> · 提交方 ${esc(t.submitted_by || '—')} · 幂等键 ${esc(t.idem || '—')}</p>
    <ul class="timeline" style="list-style:none;padding:0;margin:14px 0">
      <li>创建 ${esc(timeFull(t.created_at))} · 更新 ${esc(timeFull(t.updated_at))}</li>
      ${res ? `<li>开始 ${esc(timeFull(res.started_at))} · 结束 ${esc(timeFull(res.finished_at))} · 退出码 ${esc(res.exit_code)}</li>` : ''}
    </ul>
    ${t.status === 'needs_review' ? `<div class="notice warn"><b>执行到一半机器重启了，结果不确定——等待本机主人核对，请勿重新提交</b>（needs_review 是独立终态，不是失败；核对记录在 hub 控制台登记）。</div>` : ''}
    ${errTxt || res?.error ? `<div class="notice warn"><b>${esc(errTxt || res.error)}</b> · <code>${esc(res.error)}</code></div>` : ''}
    ${!res ? `<p class="muted">尚未到终态（${esc(STATES[t.status] || t.status)}）。抽屉每 5s 自动刷新，可先去忙别的。</p>`
      : `<h3>最终答复 · FinalAnswer</h3><pre>${esc(res.final_answer || '（空）')}</pre>
         ${res.artifacts?.length ? `<h3>产物（${res.artifacts.length} 个，保留至确认后 24h）</h3>${res.artifacts.map(a => `<div class="artifact"><code>${esc(a.path)}</code> <small>${a.size} 字节 · sha256 ${esc(a.sha256.slice(0, 16))}…</small></div>`).join('')}<p class="muted small">产物内容保留在执行机本机；经 hub 管理面 <code>GET /api/v1/tasks/{id}/artifacts/{index}</code> 取回。</p>` : ''}
         <details><summary>诊断日志（stdout / stderr 尾部）</summary><pre>stdout:\n${esc(res.stdout_tail || '（空）')}\n\nstderr:\n${esc(res.stderr_tail || '（空）')}</pre></details>`}
    <div class="row spaced"><small class="muted">终态自带下一步：</small>
      <span class="actions">${t.status === 'completed' ? button('查看全部任务', 'close-drawer', 'link') : t.status === 'failed' ? button('回表单重新派单', 'reopen-form', 'link') + button('查看全部任务', 'close-drawer', 'link') : button('查看全部任务', 'close-drawer', 'link')}</span></div>`;
}

/* ---------- 复制诊断信息 ---------- */
async function copyDiagnostics() {
  const text = JSON.stringify({ node_id: S.nodeID, copied_at: new Date().toISOString(), ...S.status }, null, 2);
  try {
    await navigator.clipboard.writeText(text);
    toast('诊断信息已复制');
  } catch (e) {
    toast('复制失败——浏览器未授权剪贴板');
  }
}

/* ---------- toast ---------- */
let toastTimer = null;
function toast(msg) {
  const el = $('#toast');
  el.textContent = msg;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 3200);
}

/* ---------- 事件委托 ---------- */
document.addEventListener('click', e => {
  const btn = e.target.closest('button');
  const tr = e.target.closest('tr[data-task]');
  if (btn) {
    const [act, arg] = (btn.dataset.act || '').split(':');
    switch (act) {
      case 'gate-enter': tryToken($('#gate-token').value.trim()); return;
      case 'go': location.hash = '#/' + arg; return;
      case 'go-tasks': S.focusForm = true; location.hash = '#/tasks'; route(); if (S.page === 'tasks') focusForm(); return;
      case 'go-overview': location.hash = '#/overview'; return;
      case 'go-services': location.hash = '#/services'; return;
      case 'focus-form': focusForm(); return;
      case 'copy-diag': copyDiagnostics(); return;
      case 'logout':
        localStorage.removeItem(TOKEN_KEY); S.token = ''; S.authed = false; S.snap = ''; render(); return;
      case 'expose-on': case 'expose-off': setExposed(arg, act === 'expose-on', btn); return;
      case 'form-submit': submitForm(); return;
      case 'form-retry': // 新幂等键开新单
        S.f = { stage: 'idle', target: S.f.target, stdin: S.f.stdin, idem: newIdem(), message: '' };
        render(); focusForm(); return;
      case 'form-resend': submitForm(); return; // 同键重试
      case 'form-abandon':
        S.f = { stage: 'idle', target: S.f.target, stdin: S.f.stdin, idem: newIdem(), message: '' };
        toast('已放弃 · 幂等键作废，不会再提交这一单'); render(); return;
      case 'open-submitted': {
        const id = S.f.taskId; if (id) openDrawer(id); return;
      }
      case 'open-task': openDrawer(arg); return;
      case 'reopen-form': closeDrawer(); route(); focusForm(); return;
      case 'close-drawer': closeDrawer(); return;
    }
  }
  if (tr && !e.target.closest('button')) openDrawer(tr.dataset.task);
});
document.addEventListener('input', e => {
  if (e.target.id === 'f-target') { S.f.target = e.target.value; render(); }
  if (e.target.id === 'f-stdin') S.f.stdin = e.target.value;
});
document.addEventListener('keydown', e => {
  if (e.key === 'Enter' && e.target.id === 'gate-token') tryToken(e.target.value.trim());
});
$('#logout').addEventListener('click', () => {
  localStorage.removeItem(TOKEN_KEY); S.token = ''; S.authed = false; S.snap = ''; render();
});
function focusForm() {
  requestAnimationFrame(() => $('#f-stdin')?.focus());
}

/* ---------- 启动 ---------- */
async function boot() {
  if (!S.token) { render(); return; }
  try {
    await api('GET', '/api/v1/local/status', undefined, { noGate: true });
    S.authed = true;
    await loadAll();
  } catch (e) { /* 401 → 门；网络异常 → 空数据页，轮询会补 */ 
    if (!e.unauthorized) S.authed = true;
  }
  route();
  setInterval(pollTick, 5000);
}
boot();
