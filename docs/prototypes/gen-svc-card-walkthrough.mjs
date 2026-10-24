// svc-card 竖线废除走查 harness：真实 node style.css + 复刻 svcCard DOM（on/off 两态）。
import { readFileSync, writeFileSync } from 'node:fs';

const css = readFileSync('/Users/tl.m/coding/personal/sandbox/SwarmMind/internal/node/web/style.css', 'utf8');

const card = (name, kind, badge, on, desc) => `
<section class="card svc-card spaced" id="svc-${name}">
  <div class="row"><div><h2 style="margin-bottom:4px">${name} <span class="badge">${kind}</span> <span class="badge">${badge}</span></h2>
    <small class="muted">${desc} · 权限模式 <code>workspace-write</code> · 今日已执行 <b>3</b> 次</small></div>
  <div class="actions"><div class="toggle" role="group" aria-label="暴露开关">
    <button class="on-btn${on ? ' selected' : ''}">暴露</button>
    <button class="off-btn${on ? '' : ' selected'}">停止暴露</button>
  </div></div></div>
</section>`;

const html = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>走查：svc-card 竖线废除</title><style>${css}
body{padding:24px;max-width:960px}</style></head><body>
<h1 style="font-size:20px">能力配置 · svc-card 竖线废除走查</h1>
${card('doc-writer', 'zcode', '网内可调用', true, '文档整理 · 输出 Markdown')}
${card('data-cleaner', 'python', '仅本机', false, '数据清洗脚本 · 定时批处理')}
</body></html>`;

writeFileSync('/Users/tl.m/coding/personal/sandbox/SwarmMind/docs/prototypes/svc-card-walkthrough.html', html);
console.log('svc-card harness 已生成');
