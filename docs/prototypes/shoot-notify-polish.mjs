// 通知配置统一与排版修正（docs/TASK-notify-polish-20261026b.md）DoD 验证 harness：
// 起真实 hub + nodeA（网络化），playwright 断言——
//   改动一：hub 铃铛几何：.bell-btn 内 svg 中心与按钮中心横纵偏差 <1px；
//   改动二：hub/node 渠道编辑表单恒定骨架——切 generic↔feishu 仅 label/disabled
//           变化，#nf-type-fields 高度差 = 0；feishu 时 headers textarea disabled；
//           hub 类型下拉含站内通知、node 不含（NOTIFY_TYPE_KEYS 如实口径）；
//   改动三：node 渠道编辑为居中 modal（overlay+.modal，非 drawer），role=dialog/
//           aria-modal/aria-labelledby/焦点移入；遮罩点击关闭；保存流程走通；
//   改动四：node 服务页通知卡位于页头后第一位（.page-head 的下一兄弟即通知卡）。
// 截图 docs/prototypes/shots-notify-polish/（双端同组件对比：表单两态×2、modal
// 形态、卡片位置）。控制台 pageerror 一律计 FAIL。断言结果逐条 stdout，FAIL 退出码 1。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-notify-polish');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-notify-polish-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';
const ADMIN_USER = 'admin', ADMIN_PASS = 'walk-admin-pass-123';

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
agents: []
`);
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');

// 等两端 HTTP 就绪
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

/* ---------- 改动一：铃铛几何 ---------- */
const bell = await page.evaluate(() => {
  const b = document.querySelector('.bell-btn');
  if (!b) return null;
  const s = b.querySelector('svg');
  if (!s) return { noSvg: true };
  const br = b.getBoundingClientRect(), sr = s.getBoundingClientRect();
  return {
    dx: Math.abs((sr.x + sr.width / 2) - (br.x + br.width / 2)),
    dy: Math.abs((sr.y + sr.height / 2) - (br.y + br.height / 2)),
    pad: getComputedStyle(b).padding,
  };
});
check('改动一 · hub 铃铛 .bell-btn padding=0', bell && bell.pad === '0px', bell ? `padding=${bell.pad}` : '无按钮');
check('改动一 · 铃铛 svg 中心与按钮中心偏差 <1px', !!bell && !bell.noSvg && bell.dx < 1 && bell.dy < 1,
  bell && !bell.noSvg ? `dx=${bell.dx.toFixed(2)}px dy=${bell.dy.toFixed(2)}px` : JSON.stringify(bell));
await page.screenshot({ path: join(OUT, 'hub-bell-geometry.png') });

/* ---------- 改动二（hub）：设置页通知渠道表单恒定骨架 ---------- */
await page.evaluate(() => { location.hash = '#/tokens'; });
await page.waitForSelector('#sec-notify', { timeout: 10000 });
await sleep(400);
await page.screenshot({ path: join(OUT, 'hub-settings-notify.png') });
await page.click('#sec-notify [data-act="notify-add"]');
await page.waitForSelector('#layer .modal #nf-type-fields', { timeout: 10000 });
await sleep(650);   // 等模态入场动画（400ms spring scale）落定再量几何，避免中途缩放假差

const hubModalMorph = await page.evaluate(() => ({
  isModal: !!document.querySelector('#layer .modal'),
  isDrawer: !!document.querySelector('#layer .drawer'),
  drawerOverlay: !!document.querySelector('#layer .drawer-overlay'),
  role: document.querySelector('#layer [role=dialog]')?.getAttribute('role'),
  labelledby: document.querySelector('#layer [role=dialog]')?.getAttribute('aria-labelledby'),
  ariaModal: document.querySelector('#layer [role=dialog]')?.getAttribute('aria-modal'),
  focusIn: document.querySelector('#layer [role=dialog]')?.contains(document.activeElement) || document.activeElement?.getAttribute('role') === 'dialog',
  title: document.getElementById('layer-title')?.textContent,
}));
check('改动三（hub 基准）· hub 渠道编辑为居中 modal（overlay+.modal）', hubModalMorph.isModal && !hubModalMorph.isDrawer && !hubModalMorph.drawerOverlay);
check('改动三（hub 基准）· role=dialog/aria-modal/aria-labelledby=layer-title/焦点移入',
  hubModalMorph.role === 'dialog' && hubModalMorph.ariaModal === 'true' && hubModalMorph.labelledby === 'layer-title' && hubModalMorph.focusIn,
  `标题=${hubModalMorph.title}`);

const hubInboxOption = await page.evaluate(() =>
  [...document.querySelectorAll('#nf-type option')].some(o => o.value === 'inbox'));
check('改动二 · hub 类型下拉含「站内通知」（hub 全集）', hubInboxOption);

const nfHeight = sel => page.evaluate(s => {
  const el = document.querySelector(s);
  el?.scrollIntoView({ block: 'center' });
  return Math.round(el.getBoundingClientRect().height * 100) / 100;
}, sel);

const h1 = await nfHeight('#nf-type-fields');
const skeleton1 = await page.evaluate(() => ({
  ids: [...document.querySelectorAll('#nf-type-fields label > [id]')].map(x => x.id),
  headersDisabled: document.querySelector('#nf-headers')?.disabled,
  addrLabel: document.querySelector('#nf-url')?.closest('label')?.childNodes[0]?.textContent,
  headersLabel: document.querySelector('#nf-headers')?.closest('label')?.childNodes[0]?.textContent,
}));
await page.locator('#layer .modal').screenshot({ path: join(OUT, 'hub-form-generic.png') });

await page.selectOption('#nf-format', 'feishu');
await sleep(650);   // 统一等稳再量几何
const h2 = await nfHeight('#nf-type-fields');
const skeleton2 = await page.evaluate(() => ({
  ids: [...document.querySelectorAll('#nf-type-fields label > [id]')].map(x => x.id),
  headersDisabled: document.querySelector('#nf-headers')?.disabled,
  addrLabel: document.querySelector('#nf-url')?.closest('label')?.childNodes[0]?.textContent,
  headersLabel: document.querySelector('#nf-headers')?.closest('label')?.childNodes[0]?.textContent,
  hintVisible: !document.getElementById('nf-hint')?.hidden && (document.getElementById('nf-hint')?.textContent || '').includes('自定义机器人'),
}));
await page.locator('#layer .modal').screenshot({ path: join(OUT, 'hub-form-feishu.png') });

check('改动二 · hub 字段区恒显三件（nf-format/nf-url/nf-headers 恒在恒位）',
  JSON.stringify(skeleton1.ids) === JSON.stringify(['nf-format', 'nf-url', 'nf-headers']) &&
  JSON.stringify(skeleton2.ids) === JSON.stringify(['nf-format', 'nf-url', 'nf-headers']),
  `ids=${skeleton1.ids.join(',')}`);
check('改动二 · hub 切格式 #nf-type-fields 高度差 = 0', Math.abs(h1 - h2) < 0.5, `generic=${h1}px feishu=${h2}px 差=${Math.abs(h1 - h2)}px`);
check('改动二 · hub feishu 态 headers disabled + 行内说明', skeleton2.headersDisabled === true && skeleton2.headersLabel.includes('飞书机器人协议不支持自定义请求头'), skeleton2.headersLabel);
check('改动二 · hub generic 态 headers 可编辑 + 地址 label 随格式切换',
  skeleton1.headersDisabled === false && skeleton1.addrLabel === '推送地址' && skeleton2.addrLabel === '飞书机器人 Webhook 地址');
check('改动二 · hub feishu 态说明槽切换为飞书路径文案（位置恒定不占字段区）', skeleton2.hintVisible === true);

/* hub 保存流程冒烟（collectNotifyForm 收编后回归） */
await page.selectOption('#nf-format', 'generic');
await sleep(200);
await page.fill('#nf-name', '走查测试渠道');
await page.fill('#nf-url', 'https://example.com/hook-walk');
await page.fill('#nf-headers', 'Authorization: Bearer walk-123');
await page.click('[data-act="notify-save"]');
await page.waitForSelector('#layer .modal', { state: 'detached', timeout: 10000 });
await sleep(400);
const hubSaved = await page.evaluate(() => {
  const card = document.querySelector('#notify-section .chan-card');
  return { has: !!card, text: card?.textContent || '' };
});
check('改动二 · hub 保存流程走通（modal 关闭+渠道卡出现）',
  hubSaved.has && hubSaved.text.includes('走查测试渠道'),
  hubSaved.text.replace(/\s+/g, ' ').slice(0, 60));

/* ---------- node：登录 + 服务页通知卡位置 + modal 形态 + 恒定骨架 ---------- */
await page.goto(NODE);
await page.waitForSelector('#gate-token', { timeout: 15000 });
await sleep(900);
await page.fill('#gate-token', LOCAL_A);
await page.keyboard.press('Enter');
await page.waitForSelector('#logout', { timeout: 15000 });
await sleep(800);
check('node 登录成功（gate→#logout 可见）', true);

await page.click('[data-go="services"]');
await page.waitForSelector('#notify-section', { timeout: 10000 });
await sleep(500);
const cardPos = await page.evaluate(() => {
  const head = document.querySelector('main .page-head');
  const next = head?.nextElementSibling;
  return {
    nextIsNotify: !!next && !!next.querySelector('#notify-section'),
    nextClass: next?.className || '',
    cfgSink: (() => {
      const cards = [...document.querySelectorAll('main > section.card, main section.card')];
      const notifyIdx = cards.findIndex(c => c.querySelector('#notify-section'));
      const cfgIdx = cards.findIndex(c => c.textContent.includes('能力配置'));
      return notifyIdx >= 0 && cfgIdx > notifyIdx;
    })(),
  };
});
check('改动四 · node 服务页通知卡位于页头后第一位', cardPos.nextIsNotify, `head 之后=${cardPos.nextClass}`);
check('改动四 · node 能力配置区沉底（通知卡在前）', cardPos.cfgSink);
await page.screenshot({ path: join(OUT, 'node-services-card.png') });

await page.click('[data-act="notify-add"]');
await page.waitForSelector('#layer .modal #nf-type-fields', { timeout: 10000 });
await sleep(650);   // 等模态入场动画落定再量几何（同 hub）

const nodeModalMorph = await page.evaluate(() => ({
  isModal: !!document.querySelector('#layer .modal'),
  modalId: document.querySelector('#layer .modal')?.id,
  isDrawer: !!document.querySelector('#layer .drawer'),
  drawerOverlay: !!document.querySelector('#layer .drawer-overlay'),
  role: document.querySelector('#layer [role=dialog]')?.getAttribute('role'),
  labelledby: document.querySelector('#layer [role=dialog]')?.getAttribute('aria-labelledby'),
  ariaModal: document.querySelector('#layer [role=dialog]')?.getAttribute('aria-modal'),
  focusIn: document.activeElement?.getAttribute('role') === 'dialog',
  headline: document.getElementById('layer-title')?.textContent,
}));
check('改动三 · node 渠道编辑为居中 modal（overlay+.modal，抽屉形态退役）',
  nodeModalMorph.isModal && nodeModalMorph.modalId === 'notify-modal' && !nodeModalMorph.isDrawer && !nodeModalMorph.drawerOverlay,
  `modal=${nodeModalMorph.isModal} drawer=${nodeModalMorph.isDrawer}`);
check('改动三 · node 无障碍对齐（role/aria-modal/aria-labelledby/焦点移入）',
  nodeModalMorph.role === 'dialog' && nodeModalMorph.ariaModal === 'true' && nodeModalMorph.labelledby === 'layer-title' && nodeModalMorph.focusIn,
  `标题=${nodeModalMorph.headline}`);

const nodeNoInbox = await page.evaluate(() =>
  [...document.querySelectorAll('#nf-type option')].map(o => o.value).join(','));
check('改动二 · node 类型下拉无「站内通知」（NOTIFY_TYPE_KEYS 如实口径）',
  nodeNoInbox === 'email,webhook,sms', `options=${nodeNoInbox}`);

const nh1 = await nfHeight('#nf-type-fields');
const ns1 = await page.evaluate(() => ({
  ids: [...document.querySelectorAll('#nf-type-fields label > [id]')].map(x => x.id),
  headersDisabled: document.querySelector('#nf-headers')?.disabled,
}));
await page.locator('#layer .modal').screenshot({ path: join(OUT, 'node-form-generic.png') });
await page.selectOption('#nf-format', 'feishu');
await sleep(650);   // 统一等稳再量几何
const nh2 = await nfHeight('#nf-type-fields');
const ns2 = await page.evaluate(() => ({
  headersDisabled: document.querySelector('#nf-headers')?.disabled,
  headersLabel: document.querySelector('#nf-headers')?.closest('label')?.childNodes[0]?.textContent,
}));
await page.locator('#layer .modal').screenshot({ path: join(OUT, 'node-form-feishu.png') });
check('改动二 · node 字段区恒显三件 + 切格式高度差 = 0',
  JSON.stringify(ns1.ids) === JSON.stringify(['nf-format', 'nf-url', 'nf-headers']) && Math.abs(nh1 - nh2) < 0.5,
  `generic=${nh1}px feishu=${nh2}px 差=${Math.abs(nh1 - nh2)}px`);
check('改动二 · node feishu 态 headers disabled + 行内说明（与 hub 同文）',
  ns2.headersDisabled === true && ns2.headersLabel.includes('飞书机器人协议不支持自定义请求头'));

/* node 保存流程冒烟 + 遮罩关闭语义 */
await page.selectOption('#nf-format', 'generic');
await sleep(200);
await page.fill('#nf-name', '走查测试渠道');
await page.fill('#nf-url', 'https://example.com/node-hook');
await page.click('[data-act="notify-save"]');
await page.waitForSelector('#layer .modal', { state: 'detached', timeout: 10000 });
await sleep(400);
const nodeSaved = await page.evaluate(() => !!document.querySelector('#notify-section .chan-card'));
check('改动三 · node modal 保存流程走通（关闭+渠道卡出现）', nodeSaved);

await page.click('[data-act="notify-add"]');
await page.waitForSelector('#layer .modal #nf-type-fields', { timeout: 10000 });
await page.mouse.click(30, 450);   // 遮罩（居中 modal 外缘）
await sleep(400);
const maskClosed = await page.evaluate(() => !document.querySelector('#notify-modal'));
check('改动三 · node 遮罩点击关闭（取消语义保留）', maskClosed);

/* ---------- 汇总 ---------- */
await browser.close();
cleanup();
const fails = results.filter(r => !r.ok);
console.log(`\n═══ 结果：${results.length - fails.length}/${results.length} PASS${pageErrors.length ? ` · pageerror ${pageErrors.length} 个：` + pageErrors.join(' | ') : ' · 无页面脚本错误'}`);
if (fails.length || pageErrors.length) process.exit(1);
