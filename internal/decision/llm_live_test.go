// 中文实测（TESTPLAN-D1-jev E 组）：真实 GLM API 走 llm-as-jev 链路的 top-1
// 准确率测量。守卫：仅 SWARM_DECISION_LIVE=1 且 key 三级解析命中时执行（常规
// `go test ./...` 自动 skip，不进全量回归、不烧额度）。key 走生产解析链
//（GLM_API_KEY 环境变量 → swarm-prime-home），测试自身不持有任何密钥。
package decision

import (
	"os"
	"testing"
	"time"
)

// liveCatalog 实测网目录（场景共用底座；描述即 LLM 所见的能力目录快照）。
func liveCatalog() []Candidate {
	return []Candidate{
		{NodeID: "mac-mini", Agent: "zcode-dev", Description: "通用编码智能体：接收开发任务，写代码、改 bug、排查构建失败、生成小工具脚本", Kind: "cli", Inflight: 0},
		{NodeID: "mac-mini", Agent: "ops-check", Description: "运维巡检脚本：采集本机磁盘、内存、进程健康指标并输出巡检摘要（只读巡检，不改动文件）", Kind: "cli", Inflight: 0},
		{NodeID: "mac-mini", Agent: "backup-tool", Description: "备份工具：对指定目录做增量备份、恢复与完整性校验", Kind: "cli", Inflight: 0},
		{NodeID: "win-box", Agent: "diskcheck", Description: "Windows 磁盘巡检：分析各盘剩余空间与大目录占用，给清理建议", Kind: "cli", Inflight: 0},
		{NodeID: "win-box", Agent: "report-gen", Description: "报表生成器：从 CSV 数据统计并生成带图表的分析报告", Kind: "cli", Inflight: 0},
		{NodeID: "baidu-node", Agent: "coder-pro", Description: "代码评审与重构助手：阅读代码给评审意见、指出缺陷与重构思路（不直接改代码）", Kind: "cli", Inflight: 0},
		{NodeID: "baidu-node", Agent: "translator", Description: "翻译工具：中英互译与多语言文本润色", Kind: "cli", Inflight: 0},
	}
}

// liveScenario 单个实测场景。
type liveScenario struct {
	name     string
	goal     string
	cands    []Candidate
	wantKey  string // 期望 agent@node
}

// liveScenarios 22 个真实中文场景：编码/巡检/备份/评审/翻译/报表六类 +
// 同名能力负载分派两例 + 干扰描述（提到备份但实为写脚本）。
func liveScenarios() []liveScenario {
	catalog := liveCatalog()
	pick := func(keys ...string) []Candidate {
		var out []Candidate
		for _, k := range keys {
			for _, c := range catalog {
				if c.Key() == k {
					out = append(out, c)
				}
			}
		}
		return out
	}
	healthMini := Candidate{NodeID: "mac-mini", Agent: "health-check", Description: "系统健康巡检：采集磁盘、内存、进程指标输出摘要", Kind: "cli", Inflight: 4}
	healthBaidu := Candidate{NodeID: "baidu-node", Agent: "health-check", Description: "系统健康巡检：采集磁盘、内存、进程指标输出摘要", Kind: "cli", Inflight: 0}
	healthMiniIdle := healthMini
	healthMiniIdle.Inflight = 0
	healthBaiduBusy := healthBaidu
	healthBaiduBusy.Inflight = 2
	return []liveScenario{
		{"写脚本", "在 demo 目录写一个 Python 脚本，输出当前时间", pick("zcode-dev@mac-mini", "ops-check@mac-mini", "translator@baidu-node"), "zcode-dev@mac-mini"},
		{"C盘清理", "C 盘快满了，帮我分析各目录占用并给清理建议", pick("diskcheck@win-box", "ops-check@mac-mini", "backup-tool@mac-mini"), "diskcheck@win-box"},
		{"健康巡检", "采集本机磁盘、内存、进程健康指标，输出一份巡检摘要", pick("ops-check@mac-mini", "zcode-dev@mac-mini", "diskcheck@win-box"), "ops-check@mac-mini"},
		{"增量备份", "把 /data/projects 目录做一次增量备份到备份盘", pick("backup-tool@mac-mini", "ops-check@mac-mini", "zcode-dev@mac-mini"), "backup-tool@mac-mini"},
		{"代码评审", "评审 main.go 的错误处理逻辑，给出重构建议", pick("coder-pro@baidu-node", "zcode-dev@mac-mini", "translator@baidu-node"), "coder-pro@baidu-node"},
		{"翻译", "把这段中文产品介绍翻译成英文", pick("translator@baidu-node", "coder-pro@baidu-node", "report-gen@win-box"), "translator@baidu-node"},
		{"报表", "用这份销售 CSV 生成月度对比图表报告", pick("report-gen@win-box", "zcode-dev@mac-mini", "translator@baidu-node"), "report-gen@win-box"},
		{"构建排查", "go build 报 undefined: NewClient，帮我排查并修复", pick("zcode-dev@mac-mini", "coder-pro@baidu-node", "ops-check@mac-mini"), "zcode-dev@mac-mini"},
		{"同名负载低", "执行一次全机健康巡检，输出摘要", append(append([]Candidate{}, healthMini), healthBaidu), "health-check@baidu-node"},
		{"README", "给 README 补一节安装说明并修正格式", pick("zcode-dev@mac-mini", "translator@baidu-node", "coder-pro@baidu-node"), "zcode-dev@mac-mini"},
		{"备份校验", "备份完成后校验备份完整性并输出校验报告", pick("backup-tool@mac-mini", "ops-check@mac-mini", "report-gen@win-box"), "backup-tool@mac-mini"},
		{"并发评审", "评审这个 PR 的并发安全性，指出潜在缺陷", pick("coder-pro@baidu-node", "zcode-dev@mac-mini", "ops-check@mac-mini"), "coder-pro@baidu-node"},
		{"文档润色", "把英文技术文档润色成通顺的中文", pick("translator@baidu-node", "coder-pro@baidu-node", "zcode-dev@mac-mini"), "translator@baidu-node"},
		{"趋势图", "从 CSV 统计各区销量并生成季度趋势图", pick("report-gen@win-box", "ops-check@mac-mini", "translator@baidu-node"), "report-gen@win-box"},
		{"D盘空间", "看看 D 盘还剩多少空间", pick("diskcheck@win-box", "ops-check@mac-mini", "backup-tool@mac-mini"), "diskcheck@win-box"},
		{"小工具", "写一个批量重命名图片的小工具", pick("zcode-dev@mac-mini", "report-gen@win-box", "backup-tool@mac-mini"), "zcode-dev@mac-mini"},
		{"僵尸进程", "巡检各节点进程是否存在僵尸进程", pick("ops-check@mac-mini", "zcode-dev@mac-mini", "coder-pro@baidu-node"), "ops-check@mac-mini"},
		{"同名负载低二", "执行一次全机健康巡检，输出摘要", append(append([]Candidate{}, healthMiniIdle), healthBaiduBusy), "health-check@mac-mini"},
		{"锁评审", "review 这段代码里互斥锁的使用是否正确", pick("coder-pro@baidu-node", "zcode-dev@mac-mini", "translator@baidu-node"), "coder-pro@baidu-node"},
		{"改bug", "登录接口偶发 500，定位原因并修复这个 bug", pick("zcode-dev@mac-mini", "coder-pro@baidu-node", "ops-check@mac-mini"), "zcode-dev@mac-mini"},
		{"备份恢复", "恢复昨天的备份到临时目录并验证可读", pick("backup-tool@mac-mini", "ops-check@mac-mini", "report-gen@win-box"), "backup-tool@mac-mini"},
		{"干扰写脚本", "我想每天自动备份，帮我写一个定时备份脚本", pick("zcode-dev@mac-mini", "backup-tool@mac-mini", "ops-check@mac-mini"), "zcode-dev@mac-mini"},
	}
}

// TestLLMLiveAccuracy 真实 GLM API 准确率实测（守卫见文件头）。逐场景记录
// PASS/FAIL 与端到端延迟（-v 输出即报告素材，逐行落 TESTPLAN E 节），末尾
// 输出 top-1 准确率汇总。硬断言两条（codex 审 D1 ③）：API 成功调用 <18 视为
// 链路故障；top-1 正确 <18/22（验收线 80%）直接 FAIL——选错只记日志的假绿不再可能。
func TestLLMLiveAccuracy(t *testing.T) {
	if os.Getenv("SWARM_DECISION_LIVE") == "" {
		t.Skip("SWARM_DECISION_LIVE 未设置：跳过真实 API 实测（不烧额度）")
	}
	homeDir, _ := os.UserHomeDir()
	svc := New(Settings{Provider: "llm-as-jev"}, os.Getenv, homeDir, func(string, ...any) {})
	if !svc.Enabled() {
		t.Skip("key 三级解析未命中（GLM_API_KEY / prime-home 皆空）：跳过实测")
	}
	scenarios := liveScenarios()
	pass, apiErr := 0, 0
	var totalLat time.Duration
	t.Logf("%-14s %-8s %-24s %-6s %s", "场景", "结果", "建议", "延迟", "期望")
	for _, sc := range scenarios {
		start := time.Now()
		res := svc.Suggest(Request{Goal: sc.goal, Candidates: sc.cands})
		lat := time.Since(start)
		totalLat += lat
		got := "—"
		if res.Suggestion != nil {
			got = res.Suggestion.TargetAgent + "@" + res.Suggestion.TargetNode
		}
		if res.Suggestion == nil {
			apiErr++
			t.Logf("%-14s %-8s %-24s %-6s %s（note: %s）", sc.name, "无建议", "—", lat.Truncate(time.Millisecond), sc.wantKey, res.Note)
			continue
		}
		if got == sc.wantKey {
			pass++
			t.Logf("%-14s %-8s %-24s %-6s %s", sc.name, "PASS", got, lat.Truncate(time.Millisecond), sc.wantKey)
		} else {
			t.Logf("%-14s %-8s %-24s %-6s %s（conf %.2f，理由 %s）", sc.name, "FAIL", got, lat.Truncate(time.Millisecond), sc.wantKey, res.Suggestion.Confidence, res.Suggestion.Reason)
		}
	}
	n := len(scenarios)
	t.Logf("实测汇总：%d 场景，top-1 正确 %d（%.0f%%），无建议/链路错误 %d，答错 %d，平均端到端延迟 %s",
		n, pass, float64(pass)/float64(n)*100, apiErr, n-pass-apiErr, (totalLat / time.Duration(n)).Truncate(time.Millisecond))
	if n-apiErr < 18 {
		t.Fatalf("API 成功调用仅 %d/%d：链路故障，报告无效", n-apiErr, n)
	}
	if pass < 18 {
		t.Fatalf("top-1 准确率 %d/%d 低于验收线 ≥18/22（80%%）：逐场景结果见上，不得出具合格报告", pass, n)
	}
}
