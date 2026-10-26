// 任务指令 args 长文本形态 md 渲染（docs/TASK-prompt-args-md-20261026.md）DoD 验证
// harness：起真实 hub + nodeA（网络化，echo-demo agent），playwright 断言——
//   走查一：args 长文本任务（含 md 语法）→ 任务指令 md 渲染（标题/粗体/列表）+ 原文切换
//           + 切换态跨 5s 轮询保持（D22 分区 diff 不回退）；
//   走查二：args 短参数任务 → code 胶囊逐参（现状不回归，无原文切换）；
//   走查三：stdin+args 并存任务 → 双小节标题（任务说明（stdin）/命令行参数（args · 2 段））
//           + 共享原文切换（raw 单 pre 双段行前缀）；
//   双端对比：node 控制台同组件截图（长文本 md + 双源小节）。
// 截图 docs/prototypes/shots-prompt-args-md/。控制台 pageerror 一律计 FAIL。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-prompt-args-md');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-prompt-args-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';
const ADMIN_USER = 'admin', ADMIN_PASS = 'walk-admin-pass-123';

const LONG_MD = '# 部署提示词\n\n**目标**：验证任务指令分区对长文本 args 的 Markdown 渲染。\n\n## 步骤\n1. 检查渲染器版本\n2. 核对样式分级\n\n> 注意：本提示词整篇走 input.args。\n\n- 兼容旧版短参数\n- 双端一致同文';
const STDIN_MD = '# 任务说明\n\n请按以下参数执行巡检，参数经命令行下达。';

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
const t2 = await submit({ input: { stdin: '', args: ['--model', 'opus', '--dry-run'] } });
const t3 = await submit({ input: { stdin: STDIN_MD, args: ['--flag', 'v'] } });
check('三任务提交 202（长文本 md / 短参数 / stdin+args 并存）',
  t1.status === 202 && t2.status === 202 && t3.status === 202,
  `t1=${t1.status} t2=${t2.status} t3=${t3.status}`);

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
};
const zoneState = () => page.evaluate(() => {
  const z = document.querySelector('[data-zone="prompt"]');
  if (!z) return null;
  return {
    h3: z.querySelector('h3')?.textContent || '',
    mdBodies: z.querySelectorAll('.md-body').length,
    h1: z.querySelector('h1.md-h1')?.textContent || '',
    strong: z.querySelector('strong')?.textContent || '',
    lis: z.querySelectorAll('li').length,
    cmdArgs: z.querySelectorAll('.cmd-args code').length,
    labels: [...z.querySelectorAll('.md-log-label')].map(b => b.textContent),
    rawBtn: z.querySelector('[data-act="prompt-raw"]')?.textContent || '',
    note: z.querySelector('.md-log-head .muted')?.textContent || '',
    pre: z.querySelectorAll('pre').length,
    preText: z.querySelector('pre')?.textContent || '',
    mutedIntro: z.querySelector('p.muted.small')?.textContent || '',
  };
});

/* ---------- 走查一：args 长文本（md 语法）→ md 渲染 + 原文切换 + 跨轮询保持 ---------- */
await openTask(t1.data.task_id);
let z = await zoneState();
check('长文本：分区标题「任务指令」', z?.h3 === '任务指令');
check('长文本：md 渲染 h1 标题', z.h1 === '部署提示词', z.h1);
check('长文本：md 渲染粗体', z.strong === '目标', z.strong);
check('长文本：md 渲染列表（有序 2 + 无序 2）', z.lis === 4, 'li=' + z.lis);
check('长文本：不回退逐参胶囊', z.cmdArgs === 0, 'code=' + z.cmdArgs);
check('长文本：默认渲染态文案+查看原文按钮', z.note.includes('默认按 Markdown 渲染') && z.rawBtn === '查看原文');
check('长文本：无小节标题（单源）', z.labels.length === 0, JSON.stringify(z.labels));
await shot(page, 'hub-01-args-md-rendered');
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
z = await zoneState();
check('长文本：切原文后单 pre + 文案「当前为原始输入。」', z.pre >= 1 && z.note === '当前为原始输入。' && z.mdBodies === 0, `pre=${z.pre} note=${z.note}`);
check('长文本：原文含原始 md 源码', z.preText.includes('# 部署提示词') && z.preText.includes('**目标**'), '');
await shot(page, 'hub-02-args-md-raw');
await sleep(5600);   // 跨一个轮询周期（5s）：原文切换态必须保持（D22 分区 diff 不回退）
z = await zoneState();
check('长文本：原文切换态跨 5s 轮询保持', z.note === '当前为原始输入。' && z.mdBodies === 0, `note=${z.note} md=${z.mdBodies}`);
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
z = await zoneState();
check('长文本：切回渲染态 md 复现', z.mdBodies === 1 && z.h1 === '部署提示词');
// hub 独有白名单：底部「任务输入（原样 JSON）」details 保留
const inputDetails = await page.evaluate(() => {
  const d = [...document.querySelectorAll('#layer details, [data-zone="input"] details')];
  return d.some(x => x.querySelector('summary')?.textContent.includes('任务输入（原样 JSON）'));
});
check('hub：底部「任务输入（原样 JSON）」details 保留', inputDetails);
await page.keyboard.press('Escape');
await sleep(400);

/* ---------- 走查二：args 短参数 → 逐参胶囊（现状不回归） ---------- */
await openTask(t2.data.task_id);
z = await zoneState();
check('短参数：逐参胶囊 3 枚', z.cmdArgs === 3, 'code=' + z.cmdArgs);
check('短参数：不走 md 渲染', z.mdBodies === 0);
check('短参数：引言行保留（现状文案）', z.mutedIntro.includes('本任务以命令行参数下达（逐参数展示，保留边界）：'), z.mutedIntro);
check('短参数：无原文切换（现状）', z.rawBtn === '');
check('短参数：无小节标题（单源）', z.labels.length === 0);
await shot(page, 'hub-03-args-short-pills');
await page.keyboard.press('Escape');
await sleep(400);

/* ---------- 走查三：stdin+args 并存 → 双小节 + 共享原文切换 ---------- */
await openTask(t3.data.task_id);
z = await zoneState();
check('并存：双小节标题', JSON.stringify(z.labels) === JSON.stringify(['任务说明（stdin）', '命令行参数（args · 2 段）']), JSON.stringify(z.labels));
check('并存：stdin 走 md + args 走胶囊', z.mdBodies === 1 && z.cmdArgs === 2, `md=${z.mdBodies} code=${z.cmdArgs}`);
check('并存：共享一个查看原文切换', z.rawBtn === '查看原文');
await shot(page, 'hub-04-dual-sections');
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
z = await zoneState();
check('并存：raw 单 pre 全部原文（stdin:/args: 行前缀）', z.pre === 1 && z.preText.includes('stdin:\n# 任务说明') && z.preText.includes('args:\n--flag\nv'), '');
await shot(page, 'hub-05-dual-raw');
await page.keyboard.press('Escape');
await sleep(400);

/* ---------- node 控制台双端对比（长文本 md + 双源小节） ---------- */
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
z = await zoneState();
check('node 双端对比：长文本 md 渲染（与 hub 同组件同形态）', z.h1 === '部署提示词' && z.strong === '目标' && z.lis === 4 && z.cmdArgs === 0, `h1=${z.h1} li=${z.lis}`);
await shot(page, 'node-01-args-md-rendered');
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
z = await zoneState();
check('node 双端对比：原文切换同机制', z.note === '当前为原始输入。' && z.pre >= 1);
await page.click('[data-zone="prompt"] [data-act="prompt-raw"]');
await sleep(300);
await page.click('[data-act="close-drawer"]');
await sleep(400);
await page.click(`tr[data-task="${t3.data.task_id}"]`);
await page.waitForSelector('[data-zone="prompt"] h3', { timeout: 15000 });
await sleep(700);
z = await zoneState();
check('node 双端对比：双源双小节标题', JSON.stringify(z.labels) === JSON.stringify(['任务说明（stdin）', '命令行参数（args · 2 段）']), JSON.stringify(z.labels));
await shot(page, 'node-02-dual-sections');

/* ---------- 收尾 ---------- */
await browser.close();
const errs = pageErrors.filter(e => !e.includes('ResizeObserver'));
check('控制台无 pageerror', errs.length === 0, errs.join(' | ').slice(0, 200));
const failed = results.filter(r => !r.ok);
console.log(`\n==== ${results.length - failed.length}/${results.length} PASS ====`);
if (failed.length) { process.exit(1); }
