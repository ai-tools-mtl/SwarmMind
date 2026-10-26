// 界面文案黑话清扫（docs/TASK-ui-copy-sweep.md）三层走查之 2/3 层：
// 起真实 hub + nodeA(组网) + nodeB(组网) + nodeC(standalone) 环境，播种各终态任务/
// 通知渠道/令牌/群聊/管家指定，playwright 驱动双端控制台逐页 + 弹层/抽屉深层交互截图。
// 截图输出 docs/prototypes/shots-copy-sweep/（png 按 .gitignore 留本地）。
// 惯例沿用 shoot-svc-card.mjs / shoot-md-walkthrough.mjs（chromium + fullPage + 1440/1276）。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync, closeSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-copy-sweep');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-walk-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const TOK_B = 'walk-token-node-b-bbbbbbbbbbbb';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';
const LOCAL_B = 'walk-local-token-b-bbbbbbbbbb';
const LOCAL_C = 'walk-local-token-c-cccccccccc';

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort();
const LA_PORT = await freePort(), LB_PORT = await freePort(), LC_PORT = await freePort();

const sleep = ms => new Promise(r => setTimeout(r, ms));
const kids = [];
const spawnBin = (bin, cfg, log) => {
  const fd = openSync(join(TMP, log + '.log'), 'a');
  const p = spawn(join(TMP, 'bin', bin), ['-config', join(TMP, cfg)], { stdio: ['ignore', fd, fd] });
  p._log = log; kids.push(p); return p;
};
const cleanup = () => { for (const p of kids) { try { p.kill('SIGKILL'); } catch { } } };
process.on('exit', cleanup);
process.on('SIGINT', () => { cleanup(); process.exit(130); });

const HUB = `http://127.0.0.1:${API_PORT}`;
let cookie = '';
async function hub(method, path, body) {
  const r = await fetch(HUB + path, {
    method, headers: { 'Content-Type': 'application/json', ...(cookie ? { cookie } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const sc = r.headers.get('set-cookie'); if (sc) cookie = sc.split(';')[0];
  const data = await r.json().catch(() => null);
  return { code: r.status, data };
}
async function local(port, token, method, path, body) {
  const r = await fetch(`http://127.0.0.1:${port}${path}`, {
    method, headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { code: r.status, data: await r.json().catch(() => null) };
}
async function waitHubStatus(id, want, limitMs) {
  const t0 = Date.now();
  while (Date.now() - t0 < limitMs) {
    const { code, data } = await hub('GET', '/api/v1/tasks/' + id);
    if (code === 200 && new RegExp(want).test(data.status)) return data;
    await sleep(150);
  }
  throw new Error(`任务 ${id} 未到达 /${want}/`);
}
async function waitLocalStatus(port, token, id, want, limitMs) {
  const t0 = Date.now();
  while (Date.now() - t0 < limitMs) {
    try {
      const { code, data } = await local(port, token, 'GET', '/api/v1/tasks/' + id);
      if (code === 200 && new RegExp(want).test(data.status)) return data;
    } catch { /* node 重启窗口期端口未监听，重试 */ }
    await sleep(150);
  }
  throw new Error(`本地任务 ${id} 未到达 /${want}/`);
}
async function waitNodeOnline(id, limitMs) {
  const t0 = Date.now();
  while (Date.now() - t0 < limitMs) {
    try {
      const { data } = await hub('GET', '/api/v1/nodes');
      const n = (data?.nodes || []).find(x => x.node_id === id);
      if (n?.online) return;
    } catch { /* hub 未就绪，重试 */ }
    await sleep(200);
  }
  throw new Error(`节点 ${id} 未上线`);
}

console.log('── 构建 hub / node');
mkdirSync(join(TMP, 'bin'), { recursive: true });
execSync('go build -o ' + JSON.stringify(join(TMP, 'bin')) + ' ./cmd/hub ./cmd/node', { cwd: ROOT, stdio: 'inherit' });

const hubYaml = `node_listen: 127.0.0.1:${HUB_NODE_PORT}
api_listen: 127.0.0.1:${API_PORT}
tls:
  insecure_loopback: true
nodes:
  nodeA: ${TOK_A}
  nodeB: ${TOK_B}
admin_token: ${ADMIN}
data_dir: ${TMP}/data-hub
`;
const nodeA = `node_id: nodeA
hub_url: ws://127.0.0.1:${HUB_NODE_PORT}/ws/node
register_token: ${TOK_A}
hub_pubkey_fp: ""
local_listen: 127.0.0.1:${LA_PORT}
local_token: ${LOCAL_A}
prime_agent: echo-demo
concurrency: 2
data_dir: ${TMP}/data-a
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: echo-demo
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 回声演示：原样输出任务参数
    permission_mode: no-shell
  - name: sleeper
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    timeout_sec: 1
    allow_args: true
    allow_env: []
    description: 超时演示
    permission_mode: no-shell
  - name: sleeper-long
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    timeout_sec: 20
    allow_args: true
    allow_env: []
    description: 长任务演示（取消/中断场景）
    permission_mode: no-shell
  - name: policy-demo
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 5
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 白名单演示
    permission_mode: no-shell
`;
const nodeB = `node_id: nodeB
hub_url: ws://127.0.0.1:${HUB_NODE_PORT}/ws/node
register_token: ${TOK_B}
hub_pubkey_fp: ""
local_listen: 127.0.0.1:${LB_PORT}
local_token: ${LOCAL_B}
concurrency: 2
data_dir: ${TMP}/data-b
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: sleeper-long
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    timeout_sec: 20
    allow_args: true
    allow_env: []
    description: 长任务演示（nodeB）
    permission_mode: no-shell
`;
const nodeC = `node_id: nodeC
local_listen: 127.0.0.1:${LC_PORT}
local_token: ${LOCAL_C}
concurrency: 2
data_dir: ${TMP}/data-c
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: echo-local
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 本机回声（standalone）
    permission_mode: no-shell
`;
writeFileSync(join(TMP, 'hub.yaml'), hubYaml);
writeFileSync(join(TMP, 'nodeA.yaml'), nodeA);
writeFileSync(join(TMP, 'nodeB.yaml'), nodeB);
writeFileSync(join(TMP, 'nodeC.yaml'), nodeC);

console.log('── 启动 hub + nodeA + nodeB + nodeC(standalone)');
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');
spawnBin('node', 'nodeB.yaml', 'nodeB');
spawnBin('node', 'nodeC.yaml', 'nodeC');

// 登录就绪
{
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try {
      const r = await hub('POST', '/api/v1/login', { token: ADMIN });
      if (r.code === 200) break;
    } catch { /* hub 未绑定端口，稍候重试 */ }
    await sleep(200);
  }
  if (!cookie) throw new Error('hub 管理面未就绪');
}
async function tryHub(method, path, body) {
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try { return await hub(method, path, body); } catch { await sleep(200); }
  }
  throw new Error('hub 请求一直失败：' + path);
}
await waitNodeOnline('nodeA', 15000);
await waitNodeOnline('nodeB', 15000);
console.log('  全部在线');

// ---------- 播种 ----------
console.log('── 播种任务/渠道/令牌/群/管家指定');
// 完成（含 final_answer）
{
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'echo-demo', input: { args: ['hello', 'swarmmind'] }, submitted_by: 'demo' });
  await waitHubStatus(r.data.task_id, 'completed', 10000);
}
// 超时失败
{
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper', input: { args: ['41'] }, submitted_by: 'demo', idem_key: 'walk-timeout' });
  await waitHubStatus(r.data.task_id, 'failed', 15000);
}
// 白名单拒绝
{
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'policy-demo', input: { args: ['hi'], env: { PATH: '/nonexistent' } }, submitted_by: 'demo', idem_key: 'walk-policy' });
  await waitHubStatus(r.data.task_id, 'failed', 15000);
}
// needs_review（nodeB started 后 kill -9 → 重启恢复）
let NR_TASK;
{
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeB', target_agent: 'sleeper-long', input: { args: ['44'] }, submitted_by: 'demo', idem_key: 'walk-review' });
  NR_TASK = r.data.task_id;
  await waitHubStatus(NR_TASK, '^running$', 15000);
  const nodeBProc = kids.find(p => p._log === 'nodeB');
  nodeBProc.kill('SIGKILL');
  await sleep(300);
  spawnBin('node', 'nodeB.yaml', 'nodeB');
  await waitNodeOnline('nodeB', 20000);
  await waitHubStatus(NR_TASK, 'needs_review', 20000);
}
// PAT（撤销弹层用）
await hub('POST', '/api/v1/auth/tokens', { name: '运维台电脑 · 只读巡检' });
// 通知渠道：站内（铃铛）+ 飞书（卡片展示）
await hub('POST', '/api/v1/notify/channels', { type: 'inbox', name: '站内提醒', events: ['task_final'], secrets: {} });
await hub('POST', '/api/v1/notify/channels', { type: 'feishu', name: '伦哥的飞书群', events: ['task_final'], secrets: { webhook_url: 'https://open.feishu.cn/open-apis/bot/v2/hook/walkthrough-demo' } });
// 触发一条终态 → 站内通知
{
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'echo-demo', input: { args: ['notify-me'] }, submitted_by: 'demo' });
  await waitHubStatus(r.data.task_id, 'completed', 10000);
}
// 管家指定（控制页「已指定」形态）
{
  const r = await hub('PUT', '/api/v1/prime', { node_id: 'nodeA', agent_name: 'echo-demo' });
  if (r.code !== 200) throw new Error('prime 指定失败 ' + r.code);
}
// 群聊 + 一条发言
let GROUP_ID;
{
  const r = await hub('POST', '/api/v1/groups', { goal: '对比两台机器的磁盘占用并汇总', members: [] });
  if (r.code === 201) {
    GROUP_ID = r.data.group_id;
    await hub('POST', `/api/v1/groups/${GROUP_ID}/messages`, { from: 'admin', kind: 'user', body: '先各跑一轮 df -h，把两台机器的占用发我，我来汇总对比。' });
  } else console.log('  群聊创建失败（' + r.code + '），群页按空态截图');
}
// nodeB 保持离线出镜（总览离线名单 + 机器表离线徽标）
{
  const nodeBProc = kids.find(p => p._log === 'nodeB');
  nodeBProc.kill('SIGKILL');
  await sleep(400);
}
// nodeA 本机任务（node 控制台「我的任务」出镜）
let LOCAL_DONE;
{
  const r = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'echo-demo', input: { args: ['from-node-console'] }, submitted_by: 'console' });
  if (r.code !== 202 && r.code !== 200) throw new Error('nodeA 本机提交失败 ' + r.code + ' ' + JSON.stringify(r.data));
  LOCAL_DONE = r.data.task_id;
  await waitLocalStatus(LA_PORT, LOCAL_A, LOCAL_DONE, 'completed', 10000);
}
// nodeC 本机任务（standalone 需先钦定管家激活编排接口——走控制台同一 declare 协议）
{
  const d = await local(LC_PORT, LOCAL_C, 'POST', '/api/v1/local/prime/declare', { prime_agent: 'echo-local' });
  if (d.code !== 200) throw new Error('nodeC 钦定失败 ' + d.code + ' ' + JSON.stringify(d.data));
  const r = await local(LC_PORT, LOCAL_C, 'POST', '/api/v1/tasks', { target_node: 'nodeC', target_agent: 'echo-local', input: { args: ['standalone-hello'] }, submitted_by: 'console' });
  if (r.code !== 202 && r.code !== 200) throw new Error('nodeC 本机提交失败 ' + r.code + ' ' + JSON.stringify(r.data));
  await waitLocalStatus(LC_PORT, LOCAL_C, r.data.task_id, 'completed', 10000);
}
console.log('  播种完成');

// ---------- 浏览器走查 ----------
const browser = await chromium.launch();
const dialogLog = [];
async function newPage(width = 1440, height = 900) {
  const page = await browser.newPage({ viewport: { width, height } });
  page.on('dialog', async d => { dialogLog.push(`dialog[${d.type()}] ${d.message().slice(0, 80)}`); await d.dismiss(); });
  return page;
}
const shot = (page, name) => page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true });

/* ===== hub 管理台 ===== */
{
  const page = await newPage();
  await page.goto(HUB + '/#/', { waitUntil: 'networkidle' });
  await page.click('[data-act="switch-login"]');
  await page.waitForSelector('#token');
  await shot(page, 'hub-01-login-token');
  await page.fill('#token', ADMIN);
  await page.click('#login-form button[type=submit]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.waitForTimeout(700);
  await shot(page, 'hub-02-overview');

  // 铃铛面板
  await page.click('#notify-bell-btn');
  await page.waitForTimeout(400);
  await shot(page, 'hub-03-bell-panel');
  await page.keyboard.press('Escape');
  await page.click('.topbar', { position: { x: 8, y: 8 } }).catch(() => { });
  await page.evaluate(() => document.body.click());
  await page.mouse.click(720, 60);

  // 机器页 + 添加机器向导（四步全走）
  await page.evaluate(() => { location.hash = '#/nodes'; });
  await page.waitForSelector('text=已登记机器');
  await page.waitForTimeout(400);
  await shot(page, 'hub-04-nodes');
  await page.click('[data-act="enroll"]');
  await page.waitForSelector('#enroll-id');
  await page.fill('#enroll-id', 'walk-demo');
  await page.waitForTimeout(300);
  await shot(page, 'hub-05-enroll-step1');
  await page.click('[data-act="enroll-next1"]');
  await page.waitForSelector('.enroll-token-row');
  await shot(page, 'hub-06-enroll-step2');
  await page.click('[data-act="enroll-pasted"]');
  await page.waitForSelector('.enroll-modes');
  await shot(page, 'hub-07-enroll-step3-bundle');
  await page.click('[data-mode="manual"]');
  await page.waitForTimeout(400);
  await page.click('.enroll-manual summary');
  await page.waitForTimeout(300);
  await shot(page, 'hub-08-enroll-step3-manual');
  await page.click('[data-mode="docker"]');
  await page.waitForTimeout(400);
  await shot(page, 'hub-09-enroll-step3-docker');
  await page.click('[data-act="enroll-next3"]');
  await page.waitForSelector('#enroll-wait');
  await page.waitForTimeout(4500);
  await shot(page, 'hub-10-enroll-step4-wait');
  await page.click('[data-act="close"]');
  await page.waitForTimeout(400);

  // 任务监管 + 高级筛选
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector('.filters');
  await page.waitForTimeout(400);
  await shot(page, 'hub-11-tasks');
  await page.click('.adv-filter summary');
  await page.waitForTimeout(300);
  await shot(page, 'hub-12-tasks-advfilter');

  // 任务抽屉：完成（含运行日志原文切换）/ 失败 / 等你确认（核对表单）
  const list = (await hub('GET', '/api/v1/tasks')).data.tasks;
  const byStatus = s => list.find(t => t.status === s);
  const done = byStatus('completed'), fail = list.find(t => t.status === 'failed' && t.result?.error?.includes('timeout')), pol = byStatus('needs_review');
  await page.click(`tr[data-task="${done.id}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(400);
  await shot(page, 'hub-13-drawer-completed');
  // 运行日志（stdout 有内容）：默认渲染 → 查看原文切换
  await page.click('.drawer details summary');
  await page.waitForTimeout(300);
  await shot(page, 'hub-14-drawer-runlog');
  await page.click('[data-act="log-raw"]');
  await page.waitForTimeout(300);
  await shot(page, 'hub-15-drawer-runlog-raw');
  await page.click('[data-act="close"]');
  await page.waitForTimeout(400);
  await page.click(`tr[data-task="${fail.id}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(300);
  await shot(page, 'hub-16-drawer-failed');
  await page.click('[data-act="close"]');
  await page.waitForTimeout(400);
  await page.click(`tr[data-task="${pol.id}"]`);
  await page.waitForSelector('#review-note');
  await page.fill('#review-note', '对比了执行前后的 df 快照，输出文件时间戳与内容一致，确认执行已实际完成。');
  await page.waitForTimeout(300);
  await shot(page, 'hub-17-drawer-needsreview');
  await page.click('[data-act="close"]');
  await page.waitForTimeout(400);

  // 运行中任务 → 取消确认弹层 → 取消后抽屉（确认成功路径自动关层，重开截「已取消」态）
  const runR = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['15'] }, submitted_by: 'demo' });
  const RUN = runR.data.task_id;
  await waitHubStatus(RUN, '^running$', 15000);
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector(`tr[data-task="${RUN}"]`);
  await page.click(`tr[data-task="${RUN}"]`);
  await page.waitForSelector('[data-act="task-cancel"]');
  await page.click('[data-act="task-cancel"]');
  await page.waitForSelector('[data-act="task-cancel-confirm"]');
  await shot(page, 'hub-18-cancel-modal');
  await page.click('[data-act="task-cancel-confirm"]');
  await page.waitForTimeout(1200);
  await waitHubStatus(RUN, 'cancelled', 15000);
  await page.click(`tr[data-task="${RUN}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(400);
  await shot(page, 'hub-19-cancelled-drawer');
  await page.click('.drawer .close');
  await page.waitForTimeout(300);

  // 任务派发（已指定智行管家形态 + 指定弹层 + 选择联动 + 批次报告）
  await page.evaluate(() => { location.hash = '#/control'; });
  await page.waitForSelector('.orch-line');
  await page.waitForTimeout(400);
  await shot(page, 'hub-20-control');
  await page.click('[data-act="prime-manage"]');
  await page.waitForSelector('#pm-node');
  await page.waitForTimeout(400);
  await shot(page, 'hub-21-prime-modal');
  await page.click('[data-act="close"]');
  await page.waitForTimeout(300);
  await page.selectOption('#ctrl-node', 'nodeA');
  await page.waitForTimeout(200);
  await page.selectOption('#ctrl-agent', 'echo-demo');
  await page.waitForTimeout(300);
  await shot(page, 'hub-22-control-picked');
  await page.fill('#ctrl-stdin', '在 /tmp 写一个输出当前时间的脚本并跑一遍，把输出贴回来');
  await page.click('#control-form button[type=submit]');
  await page.waitForSelector('text=批次报告', { timeout: 10000 });
  await page.waitForTimeout(600);
  await shot(page, 'hub-23-batch-report');

  // 策略容量 / 设置（令牌 + 通知 + 三个弹层）
  await page.evaluate(() => { location.hash = '#/policy'; });
  await page.waitForSelector('text=离线暂存条数');
  await page.waitForTimeout(300);
  await shot(page, 'hub-24-policy');
  await page.evaluate(() => { location.hash = '#/tokens'; });
  await page.waitForSelector('#token-list table');
  await page.waitForTimeout(500);
  await shot(page, 'hub-25-settings');
  await page.click('[data-act="token-revoke"]');
  await page.waitForSelector('[data-act="token-revoke-confirm"]');
  await shot(page, 'hub-26-token-revoke-modal');
  await page.click('.modal [data-act="close"]');
  await page.waitForTimeout(300);
  await page.click('[data-act="notify-add"]');
  await page.waitForSelector('#nf-type');
  await shot(page, 'hub-27-notify-add-feishu');
  await page.selectOption('#nf-type', 'email');
  await page.waitForTimeout(300);
  await shot(page, 'hub-28-notify-add-email');
  await page.click('.modal [data-act="close"]');
  await page.waitForTimeout(300);
  await page.click('[data-act="notify-del"]');
  await page.waitForSelector('[data-act="notify-del-confirm"]');
  await shot(page, 'hub-29-notify-del-modal');
  await page.click('.modal [data-act="close"]');
  await page.waitForTimeout(300);

  // 群聊列表 + 详情
  await page.evaluate(() => { location.hash = '#/groups'; });
  await page.waitForTimeout(800);
  await shot(page, 'hub-30-groups');
  if (GROUP_ID) {
    await page.click('.group-card');
    await page.waitForSelector('#group-msgline li');
    await page.waitForTimeout(500);
    await shot(page, 'hub-31-group-detail');
  }

  // 窄视口 1276 全页
  await page.setViewportSize({ width: 1276, height: 900 });
  for (const [hash, name] of [['#/overview', 'hub-40-narrow-overview'], ['#/nodes', 'hub-41-narrow-nodes'], ['#/tasks', 'hub-42-narrow-tasks'], ['#/control', 'hub-43-narrow-control'], ['#/policy', 'hub-44-narrow-policy'], ['#/tokens', 'hub-45-narrow-settings'], ['#/groups', 'hub-46-narrow-groups']]) {
    await page.evaluate(h => { location.hash = h; }, hash);
    await page.waitForTimeout(700);
    await shot(page, name);
  }
  await page.close();
}

/* ===== nodeA 控制台（组网模式） ===== */
let LOCAL_NR;
{
  const page = await newPage();
  await page.goto(`http://127.0.0.1:${LA_PORT}/#/overview`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#gate-token');
  await shot(page, 'node-01-gate');
  await page.fill('#gate-token', LOCAL_A);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.waitForTimeout(700);
  await shot(page, 'node-02-overview');

  // 派任务：选择联动 + 提交 → 已确认
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector('#f-target');
  await page.waitForTimeout(400);
  await page.selectOption('#f-target', { index: 1 });
  await page.waitForTimeout(400);
  await shot(page, 'node-03-tasks-picked');
  await page.fill('#f-stdin', '审查 docs/PRODUCT-DIRECTION-v2.md 的术语表是否还有遗漏的黑话');
  await page.click('[data-act="form-submit"]');
  await page.waitForSelector('.statusline .notice', { timeout: 10000 });
  await page.waitForTimeout(600);
  await shot(page, 'node-04-tasks-acked');
  // 我的任务抽屉（本机完成态）
  await page.click(`tr[data-task="${LOCAL_DONE}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(500);
  await shot(page, 'node-05-drawer-completed');
  await page.click('[data-act="close-drawer"]');
  await page.waitForTimeout(300);

  // 我的服务：先截开关全开 → PATCH echo-demo 关闭 → 截两态 + 配置编辑抽屉
  await page.evaluate(() => { location.hash = '#/services'; });
  await page.waitForSelector('.svc-card');
  await page.waitForTimeout(400);
  await shot(page, 'node-06-services-all-on');
  await local(LA_PORT, LOCAL_A, 'PATCH', '/api/v1/local/agents/echo-demo/exposed', { exposed: false });
  await page.waitForTimeout(900);
  await shot(page, 'node-07-services-mixed');
  await page.click('.cfg-table button[data-act^="cfg-open"]');
  await page.waitForSelector('#cfg-drawer');
  await page.waitForTimeout(400);
  await shot(page, 'node-08-cfg-drawer');
  await page.setViewportSize({ width: 1276, height: 900 });
  await page.waitForTimeout(400);
  await shot(page, 'node-09-cfg-drawer-narrow');
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.click('[data-act="cfg-close"]');
  await page.waitForTimeout(300);

  // 本机 needs_review：started 后 kill -9 nodeA → 重启恢复
  {
    const r = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['44'] }, submitted_by: 'console' });
    LOCAL_NR = r.data.task_id;
    await waitLocalStatus(LA_PORT, LOCAL_A, LOCAL_NR, '^running$', 15000);
    const nodeAProc = kids.find(p => p._log === 'nodeA');
    nodeAProc.kill('SIGKILL');
    await sleep(300);
    spawnBin('node', 'nodeA.yaml', 'nodeA');
  }
  await waitLocalStatus(LA_PORT, LOCAL_A, LOCAL_NR, 'needs_review', 25000);
  await page.reload({ waitUntil: 'networkidle' });
  // 重启后回到门（令牌还在 localStorage，自动进入）
  await page.waitForSelector('.shell', { timeout: 15000 });
  await page.waitForTimeout(900);
  await shot(page, 'node-10-overview-after-restart');
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector(`tr[data-task="${LOCAL_NR}"]`, { timeout: 10000 });
  await page.click(`tr[data-task="${LOCAL_NR}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(500);
  await shot(page, 'node-11-drawer-needsreview');
  await page.click('[data-act="close-drawer"]');

  // 窄视口 1276 全页
  await page.setViewportSize({ width: 1276, height: 900 });
  for (const [hash, name] of [['#/overview', 'node-40-narrow-overview'], ['#/services', 'node-41-narrow-services'], ['#/tasks', 'node-42-narrow-tasks']]) {
    await page.evaluate(h => { location.hash = h; }, hash);
    await page.waitForTimeout(700);
    await shot(page, name);
  }
  await page.close();
}

/* ===== nodeC 控制台（standalone 模式：通知区块 + 连接向导） ===== */
{
  const page = await newPage();
  await page.goto(`http://127.0.0.1:${LC_PORT}/#/overview`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#gate-token');
  await page.fill('#gate-token', LOCAL_C);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.waitForTimeout(700);
  await shot(page, 'nodec-01-overview-standalone');
  await page.evaluate(() => { location.hash = '#/services'; });
  await page.waitForSelector('.svc-card');
  await page.waitForTimeout(400);
  await shot(page, 'nodec-02-services');
  await page.click('[data-act="notify-add"]');
  await page.waitForSelector('#notify-drawer');
  await page.selectOption('#nf-type', 'email');
  await page.waitForTimeout(400);
  await shot(page, 'nodec-03-notify-drawer');
  await page.click('[data-act="nf-close"]');
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForTimeout(500);
  await shot(page, 'nodec-04-tasks-standalone');
  await page.evaluate(() => { location.hash = '#/wizard'; });
  await page.waitForSelector('#wiz-join');
  await shot(page, 'nodec-05-wizard');
  await page.setViewportSize({ width: 1276, height: 900 });
  for (const [hash, name] of [['#/overview', 'nodec-40-narrow-overview'], ['#/services', 'nodec-41-narrow-services'], ['#/wizard', 'nodec-42-narrow-wizard']]) {
    await page.evaluate(h => { location.hash = h; }, hash);
    await page.waitForTimeout(700);
    await shot(page, name);
  }
  await page.close();
}

await browser.close();
console.log('dialog 汇总：'); for (const d of dialogLog) console.log('  ' + d);
console.log('截图完成 → ' + OUT);

process.exit(0);   // 子进程持住事件循环：截图完成后显式退出（exit 钩子清场）
