// 三层走查截图脚本：md 容器化改动（t20261024-md-emphasis）视觉层证据采集。
// 截四态：默认整页 / 最终答复特写 / 日志原文切换态 / 窄视口 1276。
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const OUT = '/Users/tl.m/coding/personal/sandbox/SwarmMind/docs/prototypes/shots-md-emphasis';
mkdirSync(OUT, { recursive: true });
const URL = 'http://127.0.0.1:18923/md-emphasis-walkthrough.html';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.goto(URL, { waitUntil: 'networkidle' });

// 1. 默认整页（运行日志 details 已 open）
await page.screenshot({ path: `${OUT}/01-full-default.png`, fullPage: true });

// 2. 最终答复特写
const answer = page.locator('.md-answer');
await answer.screenshot({ path: `${OUT}/02-answer-closeup.png` });

// 3. 日志容器特写（渲染态）
const logBody = page.locator('.md-body.md-log');
await logBody.screenshot({ path: `${OUT}/03-log-render-closeup.png` });

// 4. stdout/stderr pill 标签特写
const label = page.locator('.md-log-label');
await label.screenshot({ path: `${OUT}/04-log-label-pill.png` });

// 5. 切原文态：手工把日志容器换成 pre 形态（模拟「查看原文」后的真实 DOM）
await page.evaluate(() => {
  const wrap = document.querySelector('details[open]');
  const body = wrap.querySelector('.md-body.md-log');
  const pre = document.createElement('pre');
  pre.textContent = 'stdout:\n- **14:02:11** register ok (hub 1.13.158.180)\n- **14:02:12** agent 池就绪：zcode × 2\n\nstderr:（空）';
  body.replaceWith(pre);
});
await page.screenshot({ path: `${OUT}/05-log-raw-toggle.png`, fullPage: true });

// 6. 窄视口 1276（走查制度口径）
await page.setViewportSize({ width: 1276, height: 900 });
await page.reload({ waitUntil: 'networkidle' });
await page.screenshot({ path: `${OUT}/06-narrow-1276.png`, fullPage: true });

await browser.close();
console.log('截图完成 →', OUT);
