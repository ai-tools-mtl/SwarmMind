// 任务指令长文本预览折叠（docs/TASK-prompt-preview-20261026.md）DoD 验证
// harness：起真实 hub + nodeA（网络化，echo-demo agent），playwright 断言——
//   走查一：长文本（>1200 字符）默认折叠（max-height 180px+overflow hidden+60px 渐隐
//           遮罩+展开全部按钮，md 渲染形态不变）→ 点展开全显（无遮罩+收起按钮）→
//           点收起回折叠 → 跨 5s 轮询状态保持（D22 展开态从状态算 HTML）；
//   走查二：短内容（<1200）无折叠无按钮（现状形态：md 路径 + 短参数胶囊路径）；
//   走查三：原文态同样折叠/展开联动（共享 promptExpanded，切回渲染态仍展开）；
//   双端对比：node 控制台同组件折叠/展开同形态。
// 截图 docs/prototypes/shots-prompt-preview/。控制台 pageerror 一律计 FAIL。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-prompt-preview');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-prompt-preview-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';
const ADMIN_USER = 'admin', ADMIN_PASS = 'walk-admin-pass-123';

const PARA = '本段为填充内容，用于把任务指令总长度推过折叠阈值，验证默认折叠预览形态与展开交互。';
const LONG_MD = '# 周报整理提示词\n\n**目标**：验证任务指令长文本的预览折叠。\n\n## 步骤\n\n'
  + Array.from({ length: 20 }, (_, i) => '**' + (i + 1) + '** ' + PARA + PARA).join('\n\n')
  + '\n\n> 引用：不要编造未提供的数据。\n';
const SHORT_MD = '# 巡检说明\n\n请检查磁盘与进程状态，输出简报。';

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort(), LA_PORT = await freePort();
const HUB = `http://127.0.0.1:${API_PORT}`;
const NODE = `http://127.0.0.1:${LA_PORT}`;

const sleep = ms => new Promise(r => setTimeout(r, ms));
const kids = [];
const cleanup = () => { for (const p of kids) { try { p.kill('SIGKILL'); } catch { } } };
process.on('exit', cleanup);
process.on('SIGINT', () => { cleanup(); process.exit(130); });

console.log('── 构建当前源码二进制（embed 坑：改资产必须重建再实测）');
mkdirSync(join(TMP, 'bin'), { recursive: true });
execSync('go build -o ' + JSON.stringify(join(TMP, 'bin')) + ' ./cmd/hub ./cmd/node', { cwd: ROOT, stdio: 'inherit' });

const spawnBin = (bin, cfg, log) => {
  const fd = openSync(join(TMP, log + '.log'), 'a');
  const p = spawn(join(TMP, 'bin', bin), ['-config', join(TMP, cfg)], { stdio: ['ignore', fd, fd] });
  kids.push(p); return p;
};
writeFileSync(join(TMP, 'hub.yaml'), `node_listen: 127.0.0.1:${HUB_NODE_PORT}
api_listen: 127.0.0.1:${API_PORT}
tls:
  insecure_loopback: true
nodes:
  nodeA: ${TOK_A}
admin_token: ${ADMIN}
data_dir: ${TMP}/data-hub
`);
writeFileSync(join(TMP, 'nodeA.yaml'), `node_id: nodeA
hub_url: ws://127.0.0.1:${HUB_NODE_PORT}/ws/node
register_token: ${TOK_A}
hub_pubkey_fp: ""
local_listen: 127.0.0.1:${LA_PORT}
local_token: ${LOCAL_A}
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
`);
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');

let hubUp = false, nodeUp = false;
for (let i = 0; i < 100 && !(hubUp && nodeUp); i++) {
  if (!hubUp) { try { const r = await fetch(HUB + '/api/v1/nodes'); if (r.status > 0) hubUp = true; } catch { } }
  if (!nodeUp) { try { const r = await fetch(NODE + '/api/v1/local/status'); if (r.status > 0) nodeUp = true; } catch { } }
  if (!(hubUp && nodeUp)) await sleep(150);
}
if (!hubUp || !nodeUp) { console.error('FAIL 服务未就绪'); process.exit(1); }
console.log('── hub/node 已就绪');

const results = [];
const check = (name, ok, detail = '') => {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  · ' + detail : ''}`);
};
const shot = (page, name) => page.screenshot({ path: join(OUT, name + '.png'), fullPage: false });

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await ctx.newPage();
const pageErrors = [];
page.on('pageerror', e => pageErrors.push(String(e)));

/* ---------- hub：登录（首次设置管理员账户） ---------- */
await page.goto(HUB);
await page.waitForSelector('#setup-form', { timeout: 15000 });
await sleep(900);   // 启动轮询尘埃落定（SPA 首屏时序坑）
await page.fill('#su-user', ADMIN_USER);
await page.fill('#su-pass', ADMIN_PASS);
await page.fill('#su-pass2', ADMIN_PASS);
await page.click('#setup-form button[type=submit]');
await page.waitForSelector('#logout', { timeout: 15000 });
await sleep(800);
check('hub 登录成功（首次设置账户→#logout 可见）', true);

/* ---------- 经会话 API 造三形态任务 ---------- */
const submit = async input => page.evaluate(async body => {
  const r = await fetch('/api/v1/tasks', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ target_node: 'nodeA', target_agent: 'echo-demo', ...body }),
  });
  return { status: r.status, data: await r.json() };
}, input);
const t1 = await submit({ input: { stdin: '', args: [LONG_MD] } });
const t2 = await submit({ input: { stdin: SHORT_MD, args: [] } });
const t3 = await submit({ input: { stdin: '', args: ['--model', 'opus', '--dry-run'] } });
check('三任务提交 202（长文本 / 短 stdin / 短参数）',
  t1.status === 202 && t2.status === 202 && t3.status === 202,
  `t1=${t1.status} t2=${t2.status} t3=${t3.status}`);
check('长文本任务 stdin+args join 口径 >1200 字符（折叠判据）', LONG_MD.length > 1200, 'len=' + LONG_MD.length);

// 等执行回落（echo 秒回；轮询 hub 列表直至三任务全 completed）
let done = false;
for (let i = 0; i < 40 && !done; i++) {
  await sleep(500);
  const st = await page.evaluate(async () => {
    const r = await fetch('/api/v1/tasks'); return r.json();
  });
  const mine = [t1, t2, t3].map(x => x.data?.task_id);
  const rows = (st.tasks || []).filter(t => mine.includes(t.id));
  done = rows.length === 3 && rows.every(t => t.status === 'completed');
}
check('三任务全部执行到 completed（echo-demo 实跑）', done);

/* ---------- 抽屉断言助手 ---------- */
const openTask = async id => {
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector(`tr[data-task="${id}"]`, { timeout: 15000 });
  await page.click(`tr[data-task="${id}"]`);
  await page.waitForSelector('[data-zone="prompt"] h3', { timeout: 15000 });
  await sleep(700);   // 抽屉入场动画落定（modal 400ms spring 中途态陷阱）
  await page.evaluate(() => document.querySelector('[data-zone="prompt"]')?.scrollIntoView({ block: 'start' }));
  await sleep(150);
};
const zoneState = () => page.evaluate(() => {
  const z = document.querySelector('[data-zone="prompt"]');
  if (!z) return null;
  const pv = z.querySelector('.prompt-preview');
  const cs = pv ? getComputedStyle(pv) : null;
  return {
    h3: z.querySelector('h3')?.textContent || '',
    preview: !!pv,
    open: !!(pv && pv.classList.contains('prompt-open')),
    fade: !!z.querySelector('.prompt-fade'),
    fadeH: z.querySelector('.prompt-fade') ? Math.round(z.querySelector('.prompt-fade').getBoundingClientRect().height) : 0,
    maxH: cs ? cs.maxHeight : '',
    overflow: cs ? cs.overflow : '',
    clipped: !!(pv && pv.scrollHeight > pv.clientHeight + 1),
    btn: z.querySelector('[data-act="prompt-expand"]')?.textContent || '',
    rawBtn: z.querySelector('[data-act="prompt-raw"]')?.textContent || '',
    h1: z.querySelector('h1.md-h1')?.textContent || '',
    strongN: z.querySelectorAll('strong').length,
    quote: !!z.querySelector('.md-quote, blockquote'),
    mdBodies: z.querySelectorAll('.md-body').length,
    pre: z.querySelectorAll('pre').length,
    cmdArgs: z.querySelectorAll('.cmd-args code').length,
    pvText: pv?.textContent || '',
  };
});

/* ---------- 走查一：长文本（>1200）默认折叠 → 展开 → 收起 → 跨轮询保持 ---------- */
await openTask(t1.data.task_id);
let z = await zoneState();
check('长文本：分区标题「任务指令」', z?.h3 === '任务指令');
check('长文本：默认折叠容器在场（无 prompt-open）', z.preview && !z.open, `preview=${z.preview} open=${z.open}`);
check('长文本：限高 180px + overflow hidden + 实际被裁切', z.maxH === '180px' && z.overflow === 'hidden' && z.clipped, `maxH=${z.maxH} ov=${z.overflow} clipped=${z.clipped}`);
check('长文本：底部 60px 渐隐遮罩在场', z.fade && z.fadeH === 60, `fadeH=${z.fadeH}`);
check('长文本：「展开全部」按钮在场（无「收起」）', z.btn === '展开全部', z.btn);
check('长文本：折叠内仍 md 渲染（h1+strong+引用，形式不变只加折叠层）', z.h1 === '周报整理提示词' && z.strongN > 5 && z.quote, `h1=${z.h1} strong=${z.strongN} quote=${z.quote}`);
check('长文本：原文切换保留', z.rawBtn === '查看原文');
await shot(page, 'hub-01-long-folded');

await page.click('[data-zone="prompt"] [data-act="prompt-expand"]');
await sleep(300);
z = await zoneState();
check('长文本：展开后 prompt-open + 遮罩移除 + 不再裁切', z.open && !z.fade && !z.clipped, `open=${z.open} fade=${z.fade} clipped=${z.clipped}`);
check('长文本：展开后 max-height 解除（none）', z.maxH === 'none', z.maxH);
check('长文本：按钮变「收起」', z.btn === '收起', z.btn);
await shot(page, 'hub-02-long-expanded');

await sleep(5600);   // 跨一个轮询周期（5s）：展开态必须保持（D22 从状态算 HTML）
z = await zoneState();
check('长文本：展开态跨 5s 轮询保持（无 JS 恢复反模式）', z.open && z.btn === '收起' && !z.fade, `open=${z.open} btn=${z.btn} fade=${z.fade}`);

await page.click('[data-zone="prompt"] [data-act="prompt-expand"]');
await sleep(300);
z = await zoneState();
check('长文本：点收起回折叠态（限高+遮罩+展开全部）', !z.open && z.fade && z.maxH === '180px' && z.btn === '展开全部' && z.clipped, `open=${z.open} maxH=${z.maxH} btn=${z.btn}`);

/* ---------- 走查三：原文态同样折叠/展开联动 ---------- */
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
z = await zoneState();
check('长文本：切原文后仍默认折叠（pre 在折叠容器内+遮罩+展开按钮）', z.preview && !z.open && z.pre >= 1 && z.fade && z.btn === '展开全部', `pre=${z.pre} btn=${z.btn}`);
await shot(page, 'hub-03-long-raw-folded');
await page.click('[data-zone="prompt"] [data-act="prompt-expand"]');
await sleep(300);
z = await zoneState();
check('长文本：原文态展开全显', z.open && !z.fade && z.pre >= 1, `open=${z.open} pre=${z.pre}`);
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
z = await zoneState();
check('长文本：切回渲染态展开态共享（同一 promptExpanded）', z.open && z.btn === '收起' && z.mdBodies >= 1, `open=${z.open} md=${z.mdBodies}`);
await page.click('[data-zone="prompt"] [data-act="prompt-expand"]');
await sleep(300);
z = await zoneState();
check('长文本：收起复位回折叠', !z.open && z.fade, `open=${z.open} fade=${z.fade}`);
await page.keyboard.press('Escape');
await sleep(400);

/* ---------- 走查二：短内容（<1200）无折叠无按钮（现状形态） ---------- */
await openTask(t2.data.task_id);
z = await zoneState();
check('短 stdin：无折叠容器无遮罩无按钮（现状全显）', !z.preview && !z.fade && z.btn === '', `preview=${z.preview} btn=${z.btn}`);
check('短 stdin：md 渲染不回归（h1 在场）', z.mdBodies >= 1 && z.h1 === '巡检说明', `h1=${z.h1}`);
check('短 stdin：原文切换保留（现状）', z.rawBtn === '查看原文');
await shot(page, 'hub-04-short-no-fold');
await page.keyboard.press('Escape');
await sleep(400);

await openTask(t3.data.task_id);
z = await zoneState();
check('短参数：无折叠无按钮（胶囊现状不回归）', !z.preview && !z.fade && z.btn === '' && z.cmdArgs === 3, `preview=${z.preview} code=${z.cmdArgs}`);
await page.keyboard.press('Escape');
await sleep(400);

/* ---------- node 控制台双端对比（折叠/展开同形态） ---------- */
await page.goto(NODE);
await page.waitForSelector('#gate-token', { timeout: 15000 });
await sleep(900);
await page.fill('#gate-token', LOCAL_A);
await page.click('[data-act="gate-enter"]');
await page.waitForSelector('.shell', { timeout: 10000 });
await sleep(700);
await page.evaluate(() => { location.hash = '#/tasks'; });
await page.waitForSelector(`tr[data-task="${t1.data.task_id}"]`, { timeout: 15000 });
await page.click(`tr[data-task="${t1.data.task_id}"]`);
await page.waitForSelector('[data-zone="prompt"] h3', { timeout: 15000 });
await sleep(700);
await page.evaluate(() => document.querySelector('[data-zone="prompt"]')?.scrollIntoView({ block: 'start' }));
await sleep(150);
z = await zoneState();
check('node 双端对比：长文本默认折叠同形态（限高+遮罩+展开全部）', z.preview && !z.open && z.maxH === '180px' && z.fade && z.btn === '展开全部' && z.clipped, `maxH=${z.maxH} btn=${z.btn}`);
await shot(page, 'node-01-long-folded');
await page.click('[data-zone="prompt"] [data-act="prompt-expand"]');
await sleep(300);
z = await zoneState();
check('node 双端对比：展开态同形态（无遮罩+收起）', z.open && !z.fade && z.btn === '收起', `open=${z.open} btn=${z.btn}`);
await shot(page, 'node-02-long-expanded');
await sleep(5600);   // node 抽屉同为 5s 轮询：展开态跨轮询保持
z = await zoneState();
check('node 双端对比：展开态跨轮询保持', z.open && z.btn === '收起');
await page.click('[data-act="close-drawer"]');
await sleep(400);

/* ---------- 收尾 ---------- */
await browser.close();
const errs = pageErrors.filter(e => !e.includes('ResizeObserver'));
check('控制台无 pageerror', errs.length === 0, errs.join(' | ').slice(0, 200));
const failed = results.filter(r => !r.ok);
console.log(`\n==== ${results.length - failed.length}/${results.length} PASS ====`);
process.exit(failed.length ? 1 : 0);   // spawn 的 hub/node 子进程句柄会吊住事件循环，显式退出（exit 事件里统一 SIGKILL 清场）
