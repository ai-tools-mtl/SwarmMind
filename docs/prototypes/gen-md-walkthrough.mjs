// 视觉走查 harness 生成器：提取两端逐字节一致的 md 渲染器 shared block（真函数），
// 跑真实渲染产物，注入复刻抽屉 DOM 的 harness 页面（引真实 style.css）。
// 用途：t20261024-md-emphasis 容器化改动的三层走查（视觉层）。
import { readFileSync, writeFileSync } from 'node:fs';

const src = readFileSync('/Users/tl.m/coding/personal/sandbox/SwarmMind/internal/hub/web/app.js', 'utf8');
const begin = src.indexOf('/* ===== md 渲染器 shared block begin');
const end = src.indexOf('===== md 渲染器 shared block end ===== */');
if (begin < 0 || end < 0) throw new Error('shared block 标记未找到');
const block = src.slice(begin, end + '===== md 渲染器 shared block end ===== */'.length);

// block 内是纯函数声明，直接 eval 拿到 mdToHtml
const mdToHtml = new Function(`${block}; return mdToHtml;`)();

// 代表性 md 样本：标题/粗体/行内 code/代码块(带语言)/表格/引用/两层列表/链接/分隔线
const answerMd = `## 部署验证结论

服务已在 **3 台节点**全部拉起，健康检查 \`/healthz\` 连续 5 次通过。

### 各节点耗时

| 节点 | 冷启动 | 热更新 |
|---|---|---|
| nodeA | 1.8s | 320ms |
| nodeB | 2.1s | 298ms |

> 注意：nodeB 首次冷启动偏慢，日志见下节诊断。

剩余工作：

1. 观察窗口跑满 14 天
   - 每日导出指标快照
2. 归档旧配置

详见 [内部手册](https://example.com/manual)。

\`\`\`bash
systemctl status swarmmind-node
\`\`\``;

const logMd = `stdout 尾部（agent 结构化日志）：

- **14:02:11** register ok (hub 1.13.158.180)
- **14:02:12** agent 池就绪：zcode × 2
- **14:05:40** 任务 t20261024-demo 开始执行`;

const css = readFileSync('/Users/tl.m/coding/personal/sandbox/SwarmMind/internal/hub/web/style.css', 'utf8');
const answerHtml = mdToHtml(answerMd);
const logHtml = mdToHtml(logMd);

const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>走查：任务抽屉 md 容器化</title>
<style>${css}</style>
</head>
<body>
<div class="overlay open" style="position:relative;opacity:1;padding:28px">
  <div class="modal" style="opacity:1;transform:none">
    <div class="row"><h2 id="layer-title">任务详情 · t20261024-demo</h2><button aria-label="关闭弹层">×</button></div>
    <p class="notice">任务已完成：可阅读最终答复并下载产物文件。</p>
    <div class="row"><h2>doc-writer @ nodeA</h2></div>
    <h3 class="spaced">任务输入</h3>
    <pre>{\n  "args": ["deploy-verify"],\n  "stdin": "",\n  "env": {}\n}</pre>
    <h3>最终答复</h3>
    <div class="md-body md-answer">${answerHtml}</div>
    <details open style="margin-top:20px"><summary>运行日志</summary>
      <div class="md-log-head"><span class="muted small">默认按 Markdown 渲染；空格对齐的进程输出请切原文。</span><button class="link">查看原文</button></div>
      <b class="md-log-label">stdout</b>
      <div class="md-body md-log">${logHtml}</div>
    </details>
    <h3 class="spaced">产物文件</h3>
    <div class="artifact row"><div><b>artifacts/report.md</b><br><small>48213 字节 · sha256 9f2c11a4b8e6d3f7…</small></div><button>取回</button></div>
    <p class="footer-note">产物经中心端中继从执行节点取回，SHA-256 校验一致才允许下载；自结果确认起保留 24 小时。</p>
    <h3 class="spaced">失败原因（对照 · 原文态 pre 形态）</h3>
    <div class="md-log-head"><span class="muted small">当前为原始输出。</span><button class="link">查看渲染</button></div>
    <pre>exit status 1
panic: runtime error: index out of range [5] with length 3</pre>
  </div>
</div>
</body>
</html>`;

writeFileSync('/Users/tl.m/coding/personal/sandbox/SwarmMind/docs/prototypes/md-emphasis-walkthrough.html', html);
console.log('harness 已生成：docs/prototypes/md-emphasis-walkthrough.html');
console.log('answer html bytes:', answerHtml.length, '/ log html bytes:', logHtml.length);
