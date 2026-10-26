// svc-card 走查截图：竖线废除后 on/off 两态 + 窄视口
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const OUT = '/Users/tl.m/coding/personal/sandbox/SwarmMind/docs/prototypes/shots-svc-card';
mkdirSync(OUT, { recursive: true });
const URL = 'http://127.0.0.1:18923/svc-card-walkthrough.html';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.goto(URL, { waitUntil: 'networkidle' });
await page.screenshot({ path: `${OUT}/01-svc-cards.png`, fullPage: true });
await page.setViewportSize({ width: 1276, height: 900 });
await page.reload({ waitUntil: 'networkidle' });
await page.screenshot({ path: `${OUT}/02-narrow-1276.png`, fullPage: true });
await browser.close();
console.log('截图完成 →', OUT);
