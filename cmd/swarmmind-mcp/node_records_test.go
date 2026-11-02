package main

// node_records_test.go node 策略面三工具实测（W05-04，SPEC 六.六同步）：进程内
// backend 打真 localapi（Collab + Strategy 同实例全装配，与 cmd/node 装配同源）——
// swarmmind_space_record（四族登记）/records（增量读）/strategy（投影只读）。
// AC12 口径（非法候选/越权登记拒绝且零派发）与 AC13 口径（agent 自报证据永不
// 证实）在工具面同断言；数据写入不等于执行（零派发零意图）。绝不真实模型/网络。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	localapi "github.com/mtl802/swarmmind/internal/node/localapi"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/strategy"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newStrategySpaceNode 起真 localapi（Collab+Strategy 同实例全装配）：策略面工具链
// 的进程内实测面（记录/投影/set_phase 引导链与 cmd/node 同源）。
func newStrategySpaceNode(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	led, _, err := ledger.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	bus := transport.NewBus()
	eng, err := strategy.NewEngine(strategy.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	store, err := collab.Open(dataDir, collab.Options{Hooks: eng})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:        led,
		Runners:       map[string]svcpkg.Runner{"echo": okRunner{}},
		ArtifactRoots: map[string]string{"echo": workdir},
		NodeID:        "nodeA",
		Collab:        store,
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	srv := httptest.NewServer(localapi.New(localapi.Config{
		NodeID:     "nodeA",
		Mode:       localapi.ModeStandalone,
		Service:    svc,
		Ledger:     led,
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  localapi.NewMcpAccessMemory(true),
		Collab:     store,
		Strategy:   eng,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// recFixture 已启动的写审空间（编排主体全链：创建→领取→配置→就绪→启动；start 后
// 循环引导 drafting 落账）+ 内容 hash fixture。
func recFixture(t *testing.T, bd backend) (string, string) {
	t.Helper()
	text, isErr := callText(t, bd, "swarmmind_space_create", map[string]any{
		"goal": "写审循环", "mode": "external", "request_id": "rec-c1",
	})
	if isErr {
		t.Fatalf("创建 external 应成功: %s", text)
	}
	spID := extractSpaceID(t, text)
	ctrl := func(action string, ver int, extra map[string]any) {
		args := map[string]any{"space_id": spID, "action": action, "term": 1, "expected_version": ver}
		for k, v := range extra {
			args[k] = v
		}
		if text, isErr = callText(t, bd, "swarmmind_space_control", args); isErr {
			t.Fatalf("control %s 应成功: %s", action, text)
		}
	}
	ctrl("takeover", 1, map[string]any{"expected_term": 0})
	ctrl("configure_members", 2, map[string]any{
		"members": []any{map[string]any{"name": "echo", "role": "writer"}, map[string]any{"name": "rev", "role": "reviewer"}},
	})
	ctrl("ready", 3, nil)
	ctrl("start", 4, nil)
	sum := sha256.Sum256([]byte("rec-v1-content"))
	return spID, hex.EncodeToString(sum[:])
}

// TestNodeRecordToolsEndToEnd 策略面三工具全链（进程内）：登记四族 → 增量读游标 →
// 投影五组；AC12/AC13 工具面拒绝 + 零派发。
func TestNodeRecordToolsEndToEnd(t *testing.T) {
	srv := newStrategySpaceNode(t)
	bd := newNodeBackendFor(srv.URL)
	spID, hash := recFixture(t, bd)

	// start 后循环引导：strategy 投影阶段=rafting（如实可查）。
	text, isErr := callText(t, bd, "swarmmind_space_strategy", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "阶段 drafting") {
		t.Fatalf("start 后投影应显示 drafting: err=%v text=%s", isErr, text)
	}

	// 候选登记（FINAL 前缀，提出者=在册成员）→ 待定版；输出渲染提出者真实值
	//（终审建议3：从应答体取权威提出者，不经入参回显——缺省/规整不渲染空值）。
	text, isErr = callText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "candidate", "member": "echo", "body": "FINAL: 采用方案一", "round": 1,
	})
	if isErr || !strings.Contains(text, "候选已登记") || !strings.Contains(text, "待定版") {
		t.Fatalf("候选登记应成功: err=%v text=%s", isErr, text)
	}
	if !strings.Contains(text, "提出者：echo") {
		t.Fatalf("候选回执应含服务端权威提出者 echo: %s", text)
	}
	// 同文重放：幂等命中（去重计数）。
	text, isErr = callText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "candidate", "member": "echo", "body": "FINAL: 采用方案一", "round": 2,
	})
	if isErr || !strings.Contains(text, "幂等命中") {
		t.Fatalf("候选同文重放应幂等命中: err=%v text=%s", isErr, text)
	}

	// AC12（工具面）：越权提出者拒绝——非法候选零登记零派发。
	got := errText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "candidate", "member": "ghost", "body": "FINAL: 越权候选",
	})
	if !strings.Contains(got, "不在空间成员内") {
		t.Fatalf("越权候选应拒绝并点明成员资格: %s", got)
	}
	// 非法词形（缺 FINAL 前缀）拒绝。
	got = errText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "candidate", "member": "echo", "body": "普通发言不是候选",
	})
	if !strings.Contains(got, "FINAL") {
		t.Fatalf("非 FINAL 候选应拒绝: %s", got)
	}

	// 版本登记 → 问题登记（发现版本锚定）。
	text, isErr = callText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "review", "op": "revision",
		"baseline": "main", "content_sha256": hash, "author": "echo", "author_round": 1,
	})
	if isErr || !strings.Contains(text, "版本已登记") {
		t.Fatalf("版本登记应成功: err=%v text=%s", isErr, text)
	}
	text, isErr = callText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "issue", "reviewer": "rev", "severity": "must",
		"file": "internal/foo.go", "line": 7, "title": "边界检查缺失", "found_in_version": hash,
	})
	if isErr || !strings.Contains(text, "问题已登记") {
		t.Fatalf("问题登记应成功: err=%v text=%s", isErr, text)
	}
	// 发现版本未登记 → 404 如实拒绝。
	got = errText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "issue", "reviewer": "rev", "severity": "must",
		"file": "x.go", "line": 1, "title": "悬空锚", "found_in_version": strings.Repeat("a", 64),
	})
	if !strings.Contains(got, "revision_not_found") {
		t.Fatalf("未登记发现版本应拒绝: %s", got)
	}

	// AC13（工具面，终审必修1边界）：编排面 verified=true 一律 403——自报来源
	//（永不可证实）与非自报来源（伪造 human_confirmation 同样拒——来源声明不能
	// 自证自身）同拒；已证实标记只能由本机主人登记（人工核对信任锚）。
	got = errText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "evidence", "version": hash, "layer": "cli_exit",
		"source": "agent_self_report", "verified": true, "ref": "agent-said-so",
	})
	if !strings.Contains(got, "不得自证") {
		t.Fatalf("编排面自报标证实应 403 拒绝: %s", got)
	}
	got = errText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "evidence", "version": hash, "layer": "work_accept",
		"source": "human_confirmation", "verified": true, "ref": "prime-said-so",
	})
	if !strings.Contains(got, "不得自证") {
		t.Fatalf("编排面伪造人工核对证据应 403 拒绝: %s", got)
	}
	// 编排面未证实证据照常可登记（verified 缺省 false——回执事实登记不越权自证）。
	if text, isErr = callText(t, bd, "swarmmind_space_record", map[string]any{
		"space_id": spID, "kind": "evidence", "version": hash, "layer": "cli_exit",
		"source": "executor_receipt", "ref": "cmd-run-1",
	}); isErr {
		t.Fatalf("回执证据（未证实标记）应登记成功: %s", text)
	}

	// 投影：候选定版态/问题 open/缺口点名证据。
	text, isErr = callText(t, bd, "swarmmind_space_strategy", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "候选 1 条") || !strings.Contains(text, "[pending]") ||
		!strings.Contains(text, "问题 1 条") || !strings.Contains(text, "待复审") ||
		!strings.Contains(text, "完成判据缺口") {
		t.Fatalf("投影应含候选/问题/缺口: %s", text)
	}
	// 零派发断言：登记面零意图（在途 0——数据写入不等于执行）。
	text, isErr = callText(t, bd, "swarmmind_space_status", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "在途 0") {
		t.Fatalf("登记后应零在途（数据写入≠执行）: err=%v text=%s", isErr, text)
	}

	// 记录增量读：全量 → since 游标只回未见。
	text, isErr = callText(t, bd, "swarmmind_space_records", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "candidate/candidate_registered") || !strings.Contains(text, "issue/issue_recorded") {
		t.Fatalf("记录增量读应含候选与问题登记: %s", text)
	}
	var lastSeq int64
	for _, line := range strings.Split(text, "\n") {
		var seq int64
		if n, _ := fmt.Sscanf(line, "#%d", &seq); n == 1 && seq > lastSeq {
			lastSeq = seq
		}
	}
	text, isErr = callText(t, bd, "swarmmind_space_records", map[string]any{"space_id": spID, "since": lastSeq})
	if isErr || !strings.Contains(text, "无新记录") {
		t.Fatalf("since=高水位应无新记录: err=%v text=%s", isErr, text)
	}
}
