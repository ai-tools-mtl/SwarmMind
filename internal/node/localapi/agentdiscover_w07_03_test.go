// agentdiscover_w07_03_test.go 检测摘要能力卡投影契约测试（W07-03，hub P05 目录
// 「检测与开放状态」数据源；W06-04 登记的「hub 侧 detect 摘要投影归 W07」兑现的
// node 侧落面）：单卡装配点盖章（AgentState.Cards 经 SetDetectProvider）、脱敏
// 边界（drift 明细与 config fail 引用路径不出本机）、发现后随卡补上报（Loop 7
// 同通道）。全 fake 探测目标 + t.TempDir，绝不调用真实 codex/zcode、绝无网络。
package localapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/agentdetect"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCardDetectSummaryStamp 检测摘要随卡盖章（单卡装配点）：卡组装配 provider 后
// Cards() 按能力名盖章 AgentCard.Detect——缓存命中的能力带四层状态/检查时刻/逐层
// 明细；未发现过的能力 Detect 保持 nil（hub 如实「未上报」，不冒认已检测）。
// 同卡两枚加法章并存实证（codex 审查建议3）：真实调用 SetSpacesCapability("v1")
// + SetDetectProvider 后 Spaces 与 Detect 并存；provider 撤除只摘 detect 章、
// spaces 章（W07-01）不受影响。
func TestCardDetectSummaryStamp(t *testing.T) {
	dir := t.TempDir()
	cmd := writeFakeCLI(t, dir, "fake-cli", `echo "v1"`)
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	profiles := []protocol.ExecProfile{
		{Name: "hit", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir, TimeoutSec: 60, Exposed: true},
		{Name: "miss", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir, TimeoutSec: 60, Exposed: true},
	}
	state := newState(t, t.TempDir(), profiles)
	state.SetSpacesCapability(protocol.SpacesCapV1)   // 建议修3：spaces 章（W07-01）真实注入，与 detect 章并存断言
	srv := New(Config{NodeID: "nodeA", Agents: state, LocalToken: "tok"})
	srv.recordDetect(agentdetect.Report{
		Name:             "hit",
		Installed:        agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
		ConfigRecognized: agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
		ExecutionReady:   agentdetect.LayerCheck{Status: agentdetect.StatusFail, CheckedAt: protocol.NowUTC(), Reason: "未发现登录态标记：auth.json 不存在"},
		CallAllowed:      agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
	}, cmd, "")
	state.SetDetectProvider(srv.CardDetectSummaries)
	cards := state.Cards()
	byName := map[string]protocol.AgentCard{}
	for _, c := range cards {
		byName[c.Name] = c
	}
	hit := byName["hit"]
	if hit.Detect == nil {
		t.Fatal("缓存命中的能力卡应盖章检测摘要")
	}
	if hit.Spaces != protocol.SpacesCapV1 {
		t.Fatalf("spaces 章应并存（W07-01 盖章不受 provider 注入干扰）: %q", hit.Spaces)
	}
	if byName["miss"].Spaces != protocol.SpacesCapV1 {
		t.Fatalf("未发现过的能力卡 spaces 章应在位: %q", byName["miss"].Spaces)
	}
	if hit.Detect.Installed != "pass" || hit.Detect.ExecutionReady != "fail" || hit.Detect.CallAllowed != "pass" {
		t.Fatalf("四层状态失真: %+v", hit.Detect)
	}
	if hit.Detect.Layers == nil || hit.Detect.Layers.ExecutionReady.Status != "fail" || hit.Detect.Layers.ExecutionReady.Reason == "" {
		t.Fatalf("逐层明细应携带 fail 原因: %+v", hit.Detect.Layers)
	}
	if hit.Detect.CheckedAt == nil || hit.Detect.CheckedAt.IsZero() {
		t.Fatal("checked_at 应在位")
	}
	if byName["miss"].Detect != nil {
		t.Fatal("未发现过的能力卡不得盖章（nil=hub 如实「未上报」）")
	}
	// 撤除 provider：Detect 回落 nil（装配边界可逆），spaces 章保持（装配事实不随
	// provider 撤除消失）。
	state.SetDetectProvider(nil)
	for _, c := range state.Cards() {
		if c.Detect != nil {
			t.Fatal("provider 撤除后卡片不得再携带 detect")
		}
		if c.Spaces != protocol.SpacesCapV1 {
			t.Fatalf("provider 撤除不得影响 spaces 章: %s %q", c.Name, c.Spaces)
		}
	}
}

// TestCardDetectSummarySanitized 脱敏边界：登记面输入漂移的明细注记含本机路径
// （上次检测 X → 现配置 Y），卡面投影只表「drifted + 固定指路」——路径不出本机；
// config 识别层 fail 的 cfgErr 原因（含引用路径）换固定脱敏人话；无漂移时 Drifted=false。
func TestCardDetectSummarySanitized(t *testing.T) {
	dir := t.TempDir()
	cmd := writeFakeCLI(t, dir, "fake-cli", `echo "v1"`)
	otherCmd := writeFakeCLI(t, dir, "fake-cli-2", `echo "v2"`)
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := newState(t, t.TempDir(), []protocol.ExecProfile{
		{Name: "drifted", Kind: protocol.AgentKindCLI, Command: otherCmd, Workdir: workdir, TimeoutSec: 60, Exposed: true},
		{Name: "cfgfail", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir, TimeoutSec: 60, Exposed: true},
	})
	srv := New(Config{NodeID: "nodeA", Agents: state, LocalToken: "tok"})
	// drifted：检测时输入 cmd，现配置已换 otherCmd（登记面输入漂移）。
	srv.recordDetect(agentdetect.Report{
		Name:             "drifted",
		Installed:        agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
		ConfigRecognized: agentdetect.LayerCheck{Status: agentdetect.StatusNotChecked},
		ExecutionReady:   agentdetect.LayerCheck{Status: agentdetect.StatusNotChecked},
		CallAllowed:      agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
	}, cmd, "")
	// cfgfail：config 识别层 fail（cfgErr 原因含引用路径）+ 执行就绪 fail（聚合
	// 前层失败文案，同样内含路径——layoutChainIssue 词表）。
	srv.recordDetect(agentdetect.Report{
		Name:             "cfgfail",
		Installed:        agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
		ConfigRecognized: agentdetect.LayerCheck{Status: agentdetect.StatusFail, CheckedAt: protocol.NowUTC(), Reason: "配置路径不存在（" + cmd + ": 引用路径事实）"},
		ExecutionReady:   agentdetect.LayerCheck{Status: agentdetect.StatusFail, CheckedAt: protocol.NowUTC(), Reason: "配置结构未识别（配置路径中间目录不存在：" + cmd + "）；配置可识别不代表可执行"},
		CallAllowed:      agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
	}, cmd, "")
	sums := srv.CardDetectSummaries()
	dr := sums["drifted"]
	if dr.Drift == nil || !dr.Drift.Drifted {
		t.Fatalf("登记面输入漂移应投影 drifted=true: %+v", dr.Drift)
	}
	if strings.Contains(dr.Drift.Note, cmd) || strings.Contains(dr.Drift.Note, otherCmd) {
		t.Fatalf("卡面漂移注记不得携带本机路径: %s", dr.Drift.Note)
	}
	if !strings.Contains(dr.Drift.Note, "重新检测") {
		t.Fatalf("漂移注记应含「需重新检测」指路: %s", dr.Drift.Note)
	}
	cf := sums["cfgfail"]
	if cf.Layers == nil || cf.Layers.ConfigRecognized.Status != "fail" {
		t.Fatalf("config fail 应逐层投影: %+v", cf.Layers)
	}
	if strings.Contains(cf.Layers.ConfigRecognized.Reason, cmd) || strings.Contains(cf.Layers.ConfigRecognized.Reason, "引用路径事实") {
		t.Fatalf("config fail 原因不得携带引用路径: %s", cf.Layers.ConfigRecognized.Reason)
	}
	if !strings.Contains(cf.Layers.ConfigRecognized.Reason, "未识别") {
		t.Fatalf("config fail 脱敏原因应表事实类别: %s", cf.Layers.ConfigRecognized.Reason)
	}
	if strings.Contains(cf.Layers.ExecutionReady.Reason, cmd) || strings.Contains(cf.Layers.ExecutionReady.Reason, "中间目录") {
		t.Fatalf("执行就绪 fail 原因不得携带聚合的本机路径: %s", cf.Layers.ExecutionReady.Reason)
	}
	if !strings.Contains(cf.Layers.ExecutionReady.Reason, "执行就绪未通过") {
		t.Fatalf("执行就绪 fail 脱敏原因应表事实类别: %s", cf.Layers.ExecutionReady.Reason)
	}
	// 无漂移对照：输入一致 + 路径事实一致 → Drifted=false（不误报）。
	srv.recordDetect(agentdetect.Report{
		Name:             "drifted",
		Installed:        agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
		ConfigRecognized: agentdetect.LayerCheck{Status: agentdetect.StatusNotChecked},
		ExecutionReady:   agentdetect.LayerCheck{Status: agentdetect.StatusNotChecked},
		CallAllowed:      agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()},
	}, otherCmd, "")
	sums = srv.CardDetectSummaries()
	if sums["drifted"].Drift == nil || sums["drifted"].Drift.Drifted {
		t.Fatalf("输入一致不得误报漂移: %+v", sums["drifted"].Drift)
	}
}

// TestDiscoverRereportsCardsWithDetect 发现后随卡补上报（Loop 7 同通道）：discover
// 写入检测缓存后 CardsReporter 收到带最新摘要的能力表（旧 card_update 只在开关/
// 配置变更触发——检测缓存变化原本不上报，hub 目录会停在注册时刻快照）。
func TestDiscoverRereportsCardsWithDetect(t *testing.T) {
	dir := t.TempDir()
	cmd := writeFakeCLI(t, dir, "fake-zcode", `echo "zcode runtime 1.0"`)
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := protocol.ExecProfile{Name: "zc", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir, TimeoutSec: 60, Exposed: true}
	state := newState(t, t.TempDir(), []protocol.ExecProfile{profile})
	var mu sync.Mutex
	reported := 0
	var lastCards []protocol.AgentCard
	cfg := Config{
		NodeID: "nodeA", Agents: state, LocalToken: "tok-local", PrimeToken: "tok-prime",
		CardsReporter: func(cards []protocol.AgentCard) error {
			mu.Lock()
			reported++
			lastCards = cards
			mu.Unlock()
			return nil
		},
	}
	srv := New(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// 未发现过：卡面无 detect（不冒认）。
	state.SetDetectProvider(srv.CardDetectSummaries)
	for _, c := range state.Cards() {
		if c.Detect != nil {
			t.Fatal("发现前卡面不得带 detect")
		}
	}
	code, out := postLocal(t, ts, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"zc","host":"zcode","command":"`+cmd+`","config_home":""}]}`)
	if code != http.StatusOK {
		t.Fatalf("discover 应 200, got %d: %v", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if reported != 1 {
		t.Fatalf("discover 后应补上报一次能力表, got %d", reported)
	}
	if len(lastCards) != 1 || lastCards[0].Name != "zc" || lastCards[0].Detect == nil {
		t.Fatalf("补上报的卡应携带检测摘要: %+v", lastCards)
	}
	if lastCards[0].Detect.Installed != "pass" {
		t.Fatalf("补报摘要四层状态失真: %+v", lastCards[0].Detect)
	}
}
