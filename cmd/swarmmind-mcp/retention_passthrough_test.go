package main

// terminal_retention_sec 经 MCP 状态类工具透传（maturity-retention-config R2 必修④，
// SPEC 六.六规则 15）：hub 模式 swarmmind_nodes 读 /api/v1/nodes 顶层字段（hub
// 终态任务保留），node 模式 swarmmind_nodes 读 /api/v1/local/status 同名字段
// （node 账本终态保留）——两后端各至少一条实测锁，防止字段扩面后输出断链。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHubNodesRetentionPassthrough hub 模式：假 hub 按真实应答形态（nodes 列表 +
// 顶层 terminal_retention_sec）回包，断言工具输出附保留人话行；0=「不自动清理」，
// 86400=「24 小时」，5400=「90 分钟」，90=「90 秒」（retentionHuman 退阶）。
func TestHubNodesRetentionPassthrough(t *testing.T) {
	cases := []struct {
		sec    int
		column string // 输出必须包含的保留形态
	}{
		{86400, "24 小时"},
		{0, "不自动清理"},
		{5400, "90 分钟"},
		{90, "90 秒"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// 形态对齐 internal/hub/api handleNodes：nodes 数组 + 顶层保留秒数
			_ = json.NewEncoder(w).Encode(map[string]any{
				"nodes": []map[string]any{{
					"node_id": "mac-mini", "online": true, "epoch": 7, "inflight": 2,
				}},
				"terminal_retention_sec": tc.sec,
			})
		}))
		oldURL, oldTok := hubURL, hubToken
		hubURL, hubToken = srv.URL, "tok"
		srvClose := srv.Close
		t.Cleanup(func() {
			hubURL, hubToken = oldURL, oldTok
			srvClose()
		})
		text, isErr := callText(t, hubBackend{}, "swarmmind_nodes", nil)
		if isErr {
			t.Fatalf("sec=%d：nodes 不应 isError", tc.sec)
		}
		for _, want := range []string{"mac-mini：在线（进行中 2）", "hub 终态任务保留：" + tc.column, "terminal_retention_sec"} {
			if !strings.Contains(text, want) {
				t.Errorf("sec=%d：输出缺 %q，got:\n%s", tc.sec, want, text)
			}
		}
	}
}

// TestNodeNodesRetentionPassthrough node 模式：真 localapi（newLocalNode 全装配，
// Service 未注入 TerminalRetention → protocol.TerminalRetention 默认 24h），
// status 应答自带 terminal_retention_sec=86400，断言工具输出附「账本终态保留」行。
func TestNodeNodesRetentionPassthrough(t *testing.T) {
	srv, _, _ := newLocalNode(t, "echo")
	bd := newNodeBackendFor(srv.URL)
	text, isErr := callText(t, bd, "swarmmind_nodes", nil)
	if isErr {
		t.Fatal("nodes 不应 isError")
	}
	for _, want := range []string{"账本终态保留：24 小时", "terminal_retention_sec=86400", "0=永不自动清理"} {
		if !strings.Contains(text, want) {
			t.Errorf("输出缺 %q，got:\n%s", want, text)
		}
	}
}
