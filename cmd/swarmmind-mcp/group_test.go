// group_test.go 群工具 MCP 层实测（SPEC 六.六 同步开发规则③）：真实 api
// server + 群存储全链（MCP 工具函数 → HTTP → group.Store），覆盖建群/发言/
// 增量拉取/列表/关闭与预算护栏的人话错误。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/api"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

// realHub 起一个真 hub 管理面（含群存储），按真实路径铸一枚 PAT 并把全局
// hubURL/hubToken 指向它（MCP 与 REST 同一令牌体系：Bearer PAT）。
func realHub(t *testing.T) {
	t.Helper()
	saveDecideEnv(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := group.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开群 store: %v", err)
	}
	t.Cleanup(func() { _ = gs.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	dataDir := t.TempDir()
	srv := httptest.NewServer(api.New(api.Config{Core: c, AdminToken: "tok", DataDir: dataDir, Groups: gs}).Handler())
	t.Cleanup(srv.Close)

	// 登录 → 铸 PAT（MCP 实际部署即此路径：管理台签发 smsw_ 令牌注入 env）
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"tok"}`))
	if err != nil {
		t.Fatalf("登录: %v", err)
	}
	resp.Body.Close()
	resp, err = cl.Post(srv.URL+"/api/v1/auth/tokens", "application/json", strings.NewReader(`{"name":"mcp-test"}`))
	if err != nil {
		t.Fatalf("铸 PAT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("铸 PAT 应 201, got %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("PAT 响应解析失败: %v", err)
	}
	hubURL, hubToken = srv.URL, out.Token
}

// mustCall 执行工具并断言成功，返回人话结果文本。
func mustCall(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	res, rpcErr := callTool(name, args)
	if rpcErr != nil {
		t.Fatalf("%s 不应报 RPC 错误: %v", name, rpcErr)
	}
	if res["isError"] == true {
		t.Fatalf("%s 不应失败: %v", name, res["content"])
	}
	tc, ok := res["content"].([]textContent)
	if !ok || len(tc) == 0 {
		t.Fatalf("%s 应返回文本内容", name)
	}
	return tc[0].Text
}

// groupToolsFullFlow 全链：建群 → 发言（缺省 kind=user）→ agent 发言 →
// 增量拉取 → 列表 → 关闭 → 幂等关闭 → 终态拒收 → 预算用尽提示。
func TestGroupToolsFullFlow(t *testing.T) {
	realHub(t)

	// 建群
	out := mustCall(t, "swarmmind_group_create", map[string]any{
		"goal": "对比双机磁盘报告", "members": []any{"diskcheck@mac-mini", "diskcheck@baidu-node"},
	})
	if !strings.Contains(out, "协作群已创建") || !strings.Contains(out, "g-") {
		t.Fatalf("建群输出应含群 ID, got: %s", out)
	}
	gid := out[strings.Index(out, "g-"):]
	if i := strings.IndexAny(gid, "\n"); i >= 0 {
		gid = gid[:i]
	}
	gid = strings.TrimSpace(strings.Split(gid, "\n")[0])

	// 发言：缺省 kind=user → seq=1
	out = mustCall(t, "swarmmind_group_say", map[string]any{"group_id": gid, "from": "admin", "body": "开始讨论"})
	if !strings.Contains(out, "seq=1") {
		t.Fatalf("首条发言应 seq=1, got: %s", out)
	}
	// agent 发言 → seq=2（计入预算）
	out = mustCall(t, "swarmmind_group_say", map[string]any{"group_id": gid, "from": "diskcheck", "kind": "agent", "body": "mac-mini 磁盘正常"})
	if !strings.Contains(out, "seq=2") {
		t.Fatalf("agent 发言应 seq=2, got: %s", out)
	}

	// 增量拉取：since=1 只见 seq=2
	out = mustCall(t, "swarmmind_group_messages", map[string]any{"group_id": gid, "since": float64(1)})
	if !strings.Contains(out, "新增 1 条") || !strings.Contains(out, "[#2]") {
		t.Fatalf("since=1 应只见 [#2], got: %s", out)
	}
	// 空消息路径（沿用已拉取游标）
	out = mustCall(t, "swarmmind_group_messages", map[string]any{"group_id": gid, "since": float64(2)})
	if !strings.Contains(out, "没有新消息") {
		t.Fatalf("since=2 应无新消息, got: %s", out)
	}

	// 列表
	out = mustCall(t, "swarmmind_group_list", map[string]any{})
	if !strings.Contains(out, gid) || !strings.Contains(out, "讨论中") {
		t.Fatalf("列表应含该群且讨论中, got: %s", out)
	}

	// 预算护栏：再发 agent 到满 20 条（首条 seq=2 + 19 条），第 21 条报预算用尽（人话可行动）
	for i := 3; i <= 21; i++ {
		mustCall(t, "swarmmind_group_say", map[string]any{"group_id": gid, "from": "diskcheck", "kind": "agent", "body": "r"})
	}
	res, rpcErr := callTool("swarmmind_group_say", map[string]any{"group_id": gid, "from": "diskcheck", "kind": "agent", "body": "over"})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(textOf(res), "预算") {
		t.Fatalf("超预算应返回预算用尽提示, got res=%v rpcErr=%v", res, rpcErr)
	}

	// 关闭 → 幂等 → 终态拒收
	out = mustCall(t, "swarmmind_group_close", map[string]any{"group_id": gid})
	if !strings.Contains(out, "concluded") {
		t.Fatalf("关闭应确认终态, got: %s", out)
	}
	out = mustCall(t, "swarmmind_group_close", map[string]any{"group_id": gid})
	if !strings.Contains(out, "concluded") {
		t.Fatalf("重复关闭应幂等, got: %s", out)
	}
	res, rpcErr = callTool("swarmmind_group_say", map[string]any{"group_id": gid, "from": "admin", "body": "late"})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(textOf(res), "已结束") {
		t.Fatalf("终态发言应报群已结束, got res=%v rpcErr=%v", res, rpcErr)
	}

	// 列表显示已结束
	out = mustCall(t, "swarmmind_group_list", map[string]any{})
	if !strings.Contains(out, "已结束") {
		t.Fatalf("列表应显示已结束, got: %s", out)
	}
}

// TestGroupCloseByAudit 覆盖 close 审计粒度：缺省 by 审计记 mcp；显式传 by
// 落自定义署名。经 REST 列表端点核对 closed_by（MCP 与 REST 同一存储）。
func TestGroupCloseByAudit(t *testing.T) {
	realHub(t)

	out := mustCall(t, "swarmmind_group_create", map[string]any{"goal": "默认署名群"})
	gidDefault := gidFrom(t, out)
	mustCall(t, "swarmmind_group_close", map[string]any{"group_id": gidDefault})

	out = mustCall(t, "swarmmind_group_create", map[string]any{"goal": "自定义署名群"})
	gidCustom := gidFrom(t, out)
	mustCall(t, "swarmmind_group_close", map[string]any{"group_id": gidCustom, "by": " tester-甲 "})

	code, data, err := hubAPI("GET", "/api/v1/groups", nil)
	if err != nil || code != 200 {
		t.Fatalf("拉群列表: code=%d err=%v", code, err)
	}
	var listing struct {
		Groups []struct {
			ID       string `json:"group_id"`
			ClosedBy string `json:"closed_by"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		t.Fatalf("列表解析: %v", err)
	}
	closedBy := map[string]string{}
	for _, g := range listing.Groups {
		closedBy[g.ID] = g.ClosedBy
	}
	if closedBy[gidDefault] != "mcp" {
		t.Fatalf("缺省 by 审计应记 mcp, got %q", closedBy[gidDefault])
	}
	if closedBy[gidCustom] != "tester-甲" {
		t.Fatalf("自定义 by 应去空白落库为 tester-甲, got %q", closedBy[gidCustom])
	}
}

// gidFrom 从建群人话输出中截取群 ID（「协作群已创建：g-…」行）。
func gidFrom(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "g-")
	if i < 0 {
		t.Fatalf("建群输出应含群 ID, got: %s", out)
	}
	gid := out[i:]
	if j := strings.IndexAny(gid, "\n"); j >= 0 {
		gid = gid[:j]
	}
	return strings.TrimSpace(gid)
}

// TestGroupToolsErrors 参数缺失与未知群的人话错误。
func TestGroupToolsErrors(t *testing.T) {
	realHub(t)
	res, rpcErr := callTool("swarmmind_group_say", map[string]any{"group_id": "g-x", "body": "b"})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(textOf(res), "from 必填") {
		t.Fatalf("缺 from 应人话报错, got res=%v rpcErr=%v", res, rpcErr)
	}
	res, rpcErr = callTool("swarmmind_group_messages", map[string]any{"group_id": "g-none"})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(textOf(res), "群不存在") {
		t.Fatalf("未知群应人话报错, got res=%v rpcErr=%v", res, rpcErr)
	}
	res, rpcErr = callTool("swarmmind_group_create", map[string]any{"goal": " "})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(textOf(res), "goal 必填") {
		t.Fatalf("空 goal 应人话报错, got res=%v rpcErr=%v", res, rpcErr)
	}
}

// textOf 提取工具结果首段文本。
func textOf(res map[string]any) string {
	if tc, ok := res["content"].([]textContent); ok && len(tc) > 0 {
		return tc[0].Text
	}
	return ""
}

// TestGroupToolsTrimNotice 覆盖（双审批 C 修 2 六.六同步）：群消息滚动窗口
// （GROUP-RFC 三 200 条）触发后，swarmmind_group_messages 人话输出标注裁剪
// 位置——since 落在被裁区间时点破缺口并从现存最旧起返回；since 越过裁剪线时
// 仅尾部提示。
func TestGroupToolsTrimNotice(t *testing.T) {
	realHub(t)

	out := mustCall(t, "swarmmind_group_create", map[string]any{"goal": "滚动窗口群"})
	gid := gidFrom(t, out)

	// 5 条 agent（计入预算）+ 200 条 user = 205 条 → 裁掉 seq 1..5
	for i := 1; i <= 5; i++ {
		mustCall(t, "swarmmind_group_say", map[string]any{"group_id": gid, "from": "a", "kind": "agent", "body": "round"})
	}
	for i := 6; i <= 205; i++ {
		mustCall(t, "swarmmind_group_say", map[string]any{"group_id": gid, "from": "admin", "body": "filler"})
	}

	// since=0 落在被裁区间（pruned_before_seq=5）：从现存最旧 [#6] 起 200 条 + 点破缺口
	out = mustCall(t, "swarmmind_group_messages", map[string]any{"group_id": gid, "since": float64(0)})
	if !strings.Contains(out, "新增 200 条") || !strings.Contains(out, "[#6]") {
		t.Fatalf("滚动窗口应保留 200 条从 [#6] 起, got head: %s", head(out, 120))
	}
	if !strings.Contains(out, "已被裁剪") || !strings.Contains(out, "seq≤5") || !strings.Contains(out, "since=0") {
		t.Fatalf("应标注裁剪位置与缺口区间, got tail: %s", tail(out, 200))
	}

	// since=10 越过裁剪线：正常增量 195 条 + 尾部裁剪提示
	out = mustCall(t, "swarmmind_group_messages", map[string]any{"group_id": gid, "since": float64(10)})
	if !strings.Contains(out, "新增 195 条") || !strings.Contains(out, "[#11]") {
		t.Fatalf("since=10 应返回 seq 11..205 共 195 条, got head: %s", head(out, 120))
	}
	if !strings.Contains(out, "seq≤5 的更早消息已被裁剪") {
		t.Fatalf("应尾部提示裁剪线, got tail: %s", tail(out, 200))
	}
}

// head/tail 取文本首/尾片段（断言输出可读）。
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
