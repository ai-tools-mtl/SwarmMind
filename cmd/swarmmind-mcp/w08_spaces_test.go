package main

// w08_spaces_test.go W08-02 空间工具同步实测（SPEC 六.六同步规则①②③）：
// swarmmind_space_list 归档过滤参数（include_archived 缺省隐藏/显式包含 +
// archived_hidden 如实注记「列表没有 ≠ 不存在」）；swarmmind_space_status 诊断
// 投影渲染（diag.awaiting_review=0 不冒充留待核对——未证实不绿；归档空间按 ID
// 照常可查且如实标注收纳态）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// w08Rest 主人面直调（local_token；MCP 编排凭据无归档/定版权——豁免⑯同界）。
func w08Rest(t *testing.T, srvURL, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srvURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok-local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

// TestNodeSpaceToolsW08ArchiveAndDiag 归档过滤参数与诊断投影渲染（进程内真 localapi）。
func TestNodeSpaceToolsW08ArchiveAndDiag(t *testing.T) {
	srv := newSpaceNode(t)
	bd := newNodeBackendFor(srv.URL)

	// 两空间（manual 主人面直建）：其一终态定版后归档。
	mkID := func(rid, goal string) string {
		code, out := w08Rest(t, srv.URL, "POST", "/api/v1/spaces",
			fmt.Sprintf(`{"request_id":%q,"goal":%q,"mode":"manual"}`, rid, goal))
		if code != http.StatusCreated {
			t.Fatalf("建空间: %d %s", code, out)
		}
		var resp struct {
			Space struct {
				ID string `json:"id"`
			} `json:"space"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("应答解析: %v", err)
		}
		return resp.Space.ID
	}
	archID := mkID("w08-mcp-arch", "MCP 归档夹具")
	liveID := mkID("w08-mcp-live", "MCP 活跃夹具")
	if code, out := w08Rest(t, srv.URL, "POST", "/api/v1/spaces/"+archID+"/finalize",
		`{"expected_version":1,"final_outcome":"abandoned","reason":"W08 MCP 夹具"}`); code != http.StatusOK {
		t.Fatalf("定版: %d %s", code, out)
	}
	if code, out := w08Rest(t, srv.URL, "POST", "/api/v1/spaces/"+archID+"/archive",
		`{"expected_version":2}`); code != http.StatusOK {
		t.Fatalf("归档: %d %s", code, out)
	}

	// 列表（缺省）：归档隐藏 + 如实注记「另有 N 个已归档空间未列出」。
	text, isErr := callText(t, bd, "swarmmind_space_list", map[string]any{})
	if isErr || !strings.Contains(text, "另有 1 个已归档空间未列出") || strings.Contains(text, archID) {
		t.Fatalf("缺省列表应隐藏归档并注记: err=%v text=%s", isErr, text)
	}
	if !strings.Contains(text, liveID) {
		t.Fatalf("缺省列表应含活跃空间: %s", text)
	}
	// 列表（include_archived=true）：显式包含 + 头注记。
	text, isErr = callText(t, bd, "swarmmind_space_list", map[string]any{"include_archived": true})
	if isErr || !strings.Contains(text, archID) || !strings.Contains(text, "含已归档") {
		t.Fatalf("显式包含应列出归档: err=%v text=%s", isErr, text)
	}

	// 详情：diag.awaiting_review=0 → 不渲染留待核对注记（未证实不绿——零在途
	// 空间不得出现「轮终不确定」冒充）。
	text, isErr = callText(t, bd, "swarmmind_space_status", map[string]any{"space_id": liveID})
	if isErr || strings.Contains(text, "轮终不确定") {
		t.Fatalf("零在途详情不得渲染留待核对注记: err=%v text=%s", isErr, text)
	}
	// 归档空间按 ID 照常可查（回查不摘除）且如实标注收纳态。
	text, isErr = callText(t, bd, "swarmmind_space_status", map[string]any{"space_id": archID})
	if isErr || !strings.Contains(text, "已归档") {
		t.Fatalf("归档空间详情应可查并标注收纳态: err=%v text=%s", isErr, text)
	}
}
