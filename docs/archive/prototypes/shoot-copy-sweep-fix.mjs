// 文案清扫走查补拍（shoot-copy-sweep.mjs 的补帧）：弹层进场动画 200ms 内截图
// 会得到 opacity 白帧——本脚本对 6 张 hub 弹层帧 + 1 张 node 开关态帧带 450ms
// 等待重拍，覆盖 shots-copy-sweep/ 同名文件。只需 hub + nodeA 两个进程。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-copy-sweep');
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-walkfix-'));

const ADMIN = 'walkfix-admin-token-0123456789abcdef';
const TOK_A = 'walkfix-token-node-a';
const LOCAL_A = 'walkfix-local-token-a';

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort(), LA_PORT = await freePort();
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
  return { code: r.status, data: await r.json().catch(() => null) };
}

console.log('── 构建 + 启动 hub + nodeA');
mkdirSync(join(TMP, 'bin'), { recursive: true });
execSync(`go build -o ${JSON.stringify(join(TMP, 'bin'))} ./cmd/hub ./cmd/node`, { cwd: ROOT, stdio: 'inherit' });
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
`);
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');
{
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try { if ((await hub('POST', '/api/v1/login', { token: ADMIN })).code === 200) break; } catch { }
    await sleep(200);
  }
  if (!cookie) throw new Error('hub 未就绪');
}
// PAT + 飞书渠道（撤销弹层与删除弹层的靶子）
await hub('POST', '/api/v1/auth/tokens', { name: '运维台电脑 · 只读巡检' });
await hub('POST', '/api/v1/notify/channels', { type: 'feishu', name: '伦哥的飞书群', events: ['task_final'], secrets: { webhook_url: 'https://open.feishu.cn/open-apis/bot/v2/hook/walkthrough-demo' } });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.on('dialog', d => d.dismiss().catch(() => { }));
const shot = name => page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true });
const settle = () => page.waitForTimeout(500);   // 弹层/pane 进场动画（200ms）完成后截

await page.goto(HUB + '/#/', { waitUntil: 'networkidle' });
await page.click('[data-act="switch-login"]');
await page.fill('#token', ADMIN);
await page.click('#login-form button[type=submit]');
await page.waitForSelector('.shell', { timeout: 10000 });
await page.waitForTimeout(700);

// hub-06 / hub-07：向导 step2（令牌）与 step3（一键部署包）
await page.evaluate(() => { location.hash = '#/nodes'; });
await page.waitForSelector('[data-act="enroll"]');
await page.click('[data-act="enroll"]');
await page.waitForSelector('#enroll-id');
await page.fill('#enroll-id', 'walk-demo');
await page.click('[data-act="enroll-next1"]');
await page.waitForSelector('.enroll-token-row');
await settle();
await shot('hub-06-enroll-step2');
await page.click('[data-act="enroll-pasted"]');
await page.waitForSelector('.enroll-modes');
await settle();
await shot('hub-07-enroll-step3-bundle');
await page.click('[data-act="close"]');
await page.waitForTimeout(400);

// hub-18：取消任务确认弹层（长任务保 running 窗口）
{
  let t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try {
      const n = (await hub('GET', '/api/v1/nodes')).data?.nodes?.find(x => x.node_id === 'nodeA');
      if (n?.online) break;
    } catch { }
    await sleep(200);
  }
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['15'] }, submitted_by: 'demo' });
  let rt0 = Date.now();
  while (Date.now() - rt0 < 15000) {
    const d = await hub('GET', '/api/v1/tasks/' + r.data.task_id).catch(() => null);
    if (d?.data?.status === 'running') break;
    await sleep(200);
  }
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector(`tr[data-task="${r.data.task_id}"]`);
  await page.click(`tr[data-task="${r.data.task_id}"]`);
  await page.waitForSelector('[data-act="task-cancel"]', { timeout: 10000 });
  await page.click('[data-act="task-cancel"]');
  await page.waitForSelector('[data-act="task-cancel-confirm"]');
  await settle();
  await shot('hub-18-cancel-modal');
  await page.keyboard.press('Escape');   // 不确认，仅留帧
  await page.waitForTimeout(500);
}

// hub-26 / hub-27 / hub-29：设置页三个弹层
await page.evaluate(() => { location.hash = '#/tokens'; });
await page.waitForSelector('#token-list table');
await page.waitForTimeout(600);
await page.click('[data-act="token-revoke"]');
await page.waitForSelector('[data-act="token-revoke-confirm"]');
await settle();
await shot('hub-26-token-revoke-modal');
await page.click('.modal [data-act="close"]');
await page.waitForTimeout(400);
await page.click('[data-act="notify-add"]');
await page.waitForSelector('#nf-type');
await settle();
await shot('hub-27-notify-add-feishu');
await page.click('.modal [data-act="close"]');
await page.waitForTimeout(400);
await page.click('[data-act="notify-del"]');
await page.waitForSelector('[data-act="notify-del-confirm"]');
await settle();
await shot('hub-29-notify-del-modal');
await page.click('.modal [data-act="close"]');
await page.waitForTimeout(400);

// node-07：我的服务两态（点「停止开放」走 UI 路径 → off 卡 + 同步条 + toast）
await page.goto(`http://127.0.0.1:${LA_PORT}/#/services`, { waitUntil: 'networkidle' });
await page.waitForSelector('#gate-token');
await page.fill('#gate-token', LOCAL_A);
await page.click('[data-act="gate-enter"]');
await page.waitForSelector('.svc-card', { timeout: 15000 }).catch(async () => {
  // 无令牌首载不走 route()（S.page 停在初始 overview）：登录后显式补一次路由
  await page.evaluate(() => route());
  await page.waitForSelector('.svc-card', { timeout: 10000 });
});
await page.waitForTimeout(700);
await page.waitForTimeout(700);
const offBtn = await page.$('.svc-card button[data-act^="expose-off"]');
if (offBtn) {
  await offBtn.click();
  await page.waitForTimeout(1200);   // PATCH 往返 + 局部重绘 + toast 呈现
}
await shot('node-07-services-mixed');

await browser.close();
console.log('补拍完成 → ' + OUT);
process.exit(0);
