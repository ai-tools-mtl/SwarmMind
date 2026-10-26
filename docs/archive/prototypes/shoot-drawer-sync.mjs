// 双端任务抽屉差异盘点（docs/TASK-drawer-sync.md 阶段 A）走查 harness：
// 起真实 hub + nodeA(组网) + nodeB(组网)，播种抽屉全状态任务
//（运行中/完成/失败×3因/取消/等你确认×未核对+已核对，双端各一套），
// playwright 驱动 hub 管理台 + node 控制台任务抽屉逐状态截图。
// 截图输出 docs/prototypes/shots-drawer-sync/（png 按 .gitignore 留本地）。
// 惯例沿用 shoot-copy-sweep.mjs（chromium + fullPage + 1440）。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync, closeSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-drawer-sync');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-drawer-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const TOK_B = 'walk-token-node-b-bbbbbbbbbbbb';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort(), LA_PORT = await freePort(), LB_PORT = await freePort();

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

// 巡检演示脚本：stdout 输出 markdown 报告（final_answer 源）+ 往 workdir/outputs 写产物
const INSPECT = join(TMP, 'inspect.sh');
writeFileSync(INSPECT, `#!/bin/sh
mkdir -p "\$SWARM_OUTPUTS_DIR" || exit 1
printf '巡检原始记录（本机自检采集）\\n来源：inspect-demo 脚本\\n' > "\$SWARM_OUTPUTS_DIR/巡检记录.txt"
printf '%s\\n' \\
  '# 磁盘巡检报告' \\
  '' \\
  '**结论**：两台机器磁盘均在安全水位。' \\
  '' \\
  '## 明细' \\
  '' \\
  '- nodeA：已用 68%%，剩余 210GiB' \\
  '- nodeB：已用 45%%，剩余 330GiB' \\
  '' \
  '\`\`\`text' \\
  'Filesystem      Size  Used Avail Capacity' \\
  '/dev/disk1s1    926Gi 630Gi 296Gi    68%%' \\
  '\`\`\`' \\
  '' \\
  '> 建议：nodeA 一周内清理旧日志。'
`);
execSync('chmod +x ' + JSON.stringify(INSPECT));

const agentInspect = `  - name: inspect-demo
    kind: cli
    command: ${INSPECT}
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 巡检演示：输出 markdown 报告并登记产物
    permission_mode: no-shell
`;

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
prime_agent: inspect-demo
concurrency: 4
data_dir: ${TMP}/data-a
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
${agentInspect}  - name: sleeper
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
  - name: stat-missing
    kind: cli
    command: /usr/bin/stat
    workdir: /tmp
    timeout_sec: 5
    allow_args: true
    allow_env: []
    description: 失败演示：stat 不存在文件（stderr 输出）
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
local_token: ${LOCAL_A}-b
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
writeFileSync(join(TMP, 'hub.yaml'), hubYaml);
writeFileSync(join(TMP, 'nodeA.yaml'), nodeA);
writeFileSync(join(TMP, 'nodeB.yaml'), nodeB);

console.log('── 启动 hub + nodeA + nodeB');
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');
spawnBin('node', 'nodeB.yaml', 'nodeB');

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
console.log('── 播种：hub 侧四态 + 本机 needs_review');
// hub 完成（markdown 答复 + 产物 + 运行日志）
let HUB_MD;
{
  const r = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '请汇总本机磁盘巡检结果', env: {} }, submitted_by: 'demo' });
  HUB_MD = r.data.task_id;
  await waitHubStatus(HUB_MD, 'completed', 15000);
}
// hub 失败（stderr：stat 不存在文件 → 失败原因区出镜）
let HUB_STAT;
{
  const r = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'stat-missing', input: { args: ['/definitely-missing-file'] }, submitted_by: 'demo' });
  HUB_STAT = r.data.task_id;
  await waitHubStatus(HUB_STAT, 'failed', 15000);
}
// hub 失败（超时：无 stderr → 仅错误通知条）
let HUB_TIMEOUT;
{
  const r = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper', input: { args: ['41'] }, submitted_by: 'demo', idem_key: 'drawer-timeout' });
  HUB_TIMEOUT = r.data.task_id;
  await waitHubStatus(HUB_TIMEOUT, 'failed', 15000);
}
// hub 失败（白名单拒绝：无 stderr）
let HUB_POLICY;
{
  const r = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'policy-demo', input: { args: ['hi'], env: { PATH: '/nonexistent' } }, submitted_by: 'demo', idem_key: 'drawer-policy' });
  HUB_POLICY = r.data.task_id;
  await waitHubStatus(HUB_POLICY, 'failed', 15000);
}
// 本机 needs_review：started 后 kill -9 nodeA → 重启恢复（先做，后续播种不再动 nodeA 进程）
let LOCAL_NR;
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
await waitNodeOnline('nodeA', 20000);
// hub 等你确认（nodeB started 后 kill -9 → 重启恢复）
let HUB_NR;
{
  const r = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeB', target_agent: 'sleeper-long', input: { args: ['44'] }, submitted_by: 'demo', idem_key: 'drawer-review' });
  HUB_NR = r.data.task_id;
  await waitHubStatus(HUB_NR, '^running$', 15000);
  const nodeBProc = kids.find(p => p._log === 'nodeB');
  nodeBProc.kill('SIGKILL');
  await sleep(300);
  spawnBin('node', 'nodeB.yaml', 'nodeB');
  await waitNodeOnline('nodeB', 20000);
  await waitHubStatus(HUB_NR, 'needs_review', 20000);
}
// 本机完成/失败（nodeA 重启后播种，走本地直执行）
let LOCAL_MD, LOCAL_STAT, LOCAL_TIMEOUT;
{
  const r = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '请汇总本机磁盘巡检结果', env: {} }, submitted_by: 'console' });
  LOCAL_MD = r.data.task_id;
  await waitLocalStatus(LA_PORT, LOCAL_A, LOCAL_MD, 'completed', 15000);
  const s = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'stat-missing', input: { args: ['/definitely-missing-file'] }, submitted_by: 'console' });
  LOCAL_STAT = s.data.task_id;
  await waitLocalStatus(LA_PORT, LOCAL_A, LOCAL_STAT, 'failed', 15000);
  const t = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper', input: { args: ['41'] }, submitted_by: 'console' });
  LOCAL_TIMEOUT = t.data.task_id;
  await waitLocalStatus(LA_PORT, LOCAL_A, LOCAL_TIMEOUT, 'failed', 15000);
}
// hub 取消：nodeA sleeper-long 拉起后立即取消
let HUB_CANCEL;
{
  const r = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['30'] }, submitted_by: 'demo' });
  HUB_CANCEL = r.data.task_id;
  await waitHubStatus(HUB_CANCEL, '^running$', 15000);
  const c = await tryHub('POST', `/api/v1/tasks/${HUB_CANCEL}/cancel`, {});
  if (c.code !== 200 && c.code !== 202) throw new Error('取消失败 ' + c.code);
  await waitHubStatus(HUB_CANCEL, 'cancelled', 15000);
}
console.log('  播种完成（终态齐）');

// ---------- 浏览器走查 ----------
const browser = await chromium.launch();
const dialogLog = [];
async function newPage(width = 1440, height = 2400) {
  const page = await browser.newPage({ viewport: { width, height } });
  page.on('dialog', async d => { dialogLog.push(`dialog[${d.type()}] ${d.message().slice(0, 80)}`); await d.dismiss(); });
  return page;
}
const shot = (page, name) => page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true });

/* ===== hub 管理台：任务抽屉全状态 ===== */
{
  const page = await newPage();
  await page.goto(HUB + '/#/', { waitUntil: 'networkidle' });
  await page.click('[data-act="switch-login"]');
  await page.waitForSelector('#token');
  await page.fill('#token', ADMIN);
  await page.click('#login-form button[type=submit]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector('.filters');
  await page.waitForTimeout(500);

  const openDrawer = async id => {
    await page.click(`tr[data-task="${id}"]`);
    await page.waitForSelector('.drawer');
    await page.waitForTimeout(400);
  };
  const closeDrawer = async () => {
    await page.click('.drawer .close');
    await page.waitForTimeout(400);
  };
  // hub 抽屉 5s 轮询全量重绘子树，playwright actionability 重试会反复撞上 detach：
  // 抽屉内交互一律 evaluate 直接触发 click
  const drawerClick = async sel => {
    await page.evaluate(s => document.querySelector(`.drawer ${s}`).click(), sel);
    await page.waitForTimeout(300);
  };

  // 运行中（无结果）：介绍条 + 任务输入 + 空态兜底
  const runR = await tryHub('POST', '/api/v1/tasks', { target_node: 'nodeB', target_agent: 'sleeper-long', input: { args: ['40'] }, submitted_by: 'demo' });
  await waitHubStatus(runR.data.task_id, '^running$', 15000);
  await page.waitForTimeout(500);
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector(`tr[data-task="${runR.data.task_id}"]`);
  await openDrawer(runR.data.task_id);
  await shot(page, 'hub-drawer-1-running');
  await closeDrawer();

  // 完成（markdown 答复 + 产物 + 运行日志默认渲染 → 展开 → 原文）
  await openDrawer(HUB_MD);
  await shot(page, 'hub-drawer-2-completed');
  await drawerClick('details summary');
  await shot(page, 'hub-drawer-3-completed-runlog-open');
  await drawerClick('[data-act="log-raw"]');
  await shot(page, 'hub-drawer-4-completed-runlog-raw');
  await closeDrawer();

  // 失败·stderr（失败原因区默认渲染 → 原文）
  await openDrawer(HUB_STAT);
  await shot(page, 'hub-drawer-5-failed-stderr');
  await drawerClick('[data-act="fail-raw"]');
  await shot(page, 'hub-drawer-6-failed-stderr-raw');
  await closeDrawer();

  // 失败·超时 / 失败·白名单（仅错误通知条）
  await openDrawer(HUB_TIMEOUT);
  await shot(page, 'hub-drawer-7-failed-timeout');
  await closeDrawer();
  await openDrawer(HUB_POLICY);
  await shot(page, 'hub-drawer-8-failed-policy');
  await closeDrawer();

  // 已取消
  await openDrawer(HUB_CANCEL);
  await shot(page, 'hub-drawer-9-cancelled');
  await closeDrawer();

  // 等你确认（核对表单空 → 填写后）→ API 核对 → 已核对记录
  await openDrawer(HUB_NR);
  await shot(page, 'hub-drawer-10-needsreview-form');
  // 直设 value 不触发 input 事件的话草稿不落 S.drafts，5s 重绘即清空：补发 input
  await page.evaluate(() => {
    const el = document.querySelector('.drawer #review-note');
    el.value = '重启后登录 nodeB 核对：目标目录时间戳已更新，写入行数与预期一致，确认执行已实际完成。';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await page.waitForTimeout(300);
  await shot(page, 'hub-drawer-11-needsreview-filled');
  await closeDrawer();
  const rv = await tryHub('POST', `/api/v1/tasks/${HUB_NR}/review`, { action: 'ack_review', note: '重启后登录 nodeB 核对：目标目录时间戳已更新，写入行数与预期一致，确认执行已实际完成。' });
  if (rv.code !== 200) throw new Error('核对失败 ' + rv.code);
  // S.tasks 是 5s 轮询快照：核对后强刷页面再重开，否则抽屉渲染的还是核对前缓存
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector(`tr[data-task="${HUB_NR}"]`, { timeout: 10000 });
  await page.waitForTimeout(500);
  await openDrawer(HUB_NR);
  await shot(page, 'hub-drawer-12-reviewed');
  await closeDrawer();
  await page.close();
}

/* ===== nodeA 控制台：任务抽屉全状态（本机派单视角） ===== */
{
  const page = await newPage();
  await page.goto(`http://127.0.0.1:${LA_PORT}/#/overview`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#gate-token');
  await page.fill('#gate-token', LOCAL_A);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector('#f-target');
  await page.waitForTimeout(400);

  const openDrawer = async id => {
    await page.click(`[data-act="open-task:${id}"]`);
    await page.waitForSelector('.drawer');
    await page.waitForTimeout(400);
  };
  const closeDrawer = async () => {
    await page.click('[data-act="close-drawer"]');
    await page.waitForTimeout(400);
  };

  // 运行中（尚未到终态整段替代结果区）
  const runR = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['25'] }, submitted_by: 'console' });
  await waitLocalStatus(LA_PORT, LOCAL_A, runR.data.task_id, '^running$', 15000);
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector(`tr[data-task="${runR.data.task_id}"]`);
  await openDrawer(runR.data.task_id);
  await shot(page, 'node-drawer-1-running');
  await closeDrawer();

  // 完成（markdown 答复 + 产物本机路径 + 诊断日志默认 → 展开 → 原文）
  await openDrawer(LOCAL_MD);
  await shot(page, 'node-drawer-2-completed');
  await page.click('.drawer details summary');
  await page.waitForTimeout(300);
  await shot(page, 'node-drawer-3-completed-log-open');
  await page.click('.drawer [data-act="log-raw"]');
  await page.waitForTimeout(300);
  await shot(page, 'node-drawer-4-completed-log-raw');
  await closeDrawer();

  // 失败·stderr（错误通知条 + 诊断日志，无独立失败原因区）默认 → 展开 → 原文
  await openDrawer(LOCAL_STAT);
  await shot(page, 'node-drawer-5-failed-stderr');
  await page.click('.drawer details summary');
  await page.waitForTimeout(300);
  await page.click('.drawer [data-act="log-raw"]');
  await page.waitForTimeout(300);
  await shot(page, 'node-drawer-6-failed-stderr-raw');
  await closeDrawer();

  // 失败·超时（stdout/stderr 双空：日志区条件式渲染下整区消失——D10 统一到 hub
  // 条件式，2026-10-25 抽屉同步；旧「空 details+（空）×2」形态退役）
  await openDrawer(LOCAL_TIMEOUT);
  await shot(page, 'node-drawer-7-failed-timeout-log-empty');
  await closeDrawer();

  // 等你确认（本机双路径文案；无核对表单）
  await openDrawer(LOCAL_NR);
  await shot(page, 'node-drawer-8-needsreview');
  await closeDrawer();
  await page.close();
}

await browser.close();
console.log('dialog 汇总：'); for (const d of dialogLog) console.log('  ' + d);
console.log('截图完成 → ' + OUT);

process.exit(0);   // 子进程持住事件循环：截图完成后显式退出（exit 钩子清场）
