package main

// w07_capabilities_render_test.go 能力元信息渲染空间投影测试（W07-01）：
//   - hub 形态携 spaces 投影：中心装配态人话（未装配/已装配）+ 节点声明计数 +
//     隐私边界随行（支持情况不表示私有历史可读）；
//   - node 形态无 spaces 键：渲染零变化（投影行不出现，不加空壳行）；
//   - 旧行体（无 spaces 键的 hub 应答，旧构造方）：容忍缺省。

import (
	"strings"
	"testing"
)

func TestW07RenderCapabilitiesSpacesProjection(t *testing.T) {
	// hub 未装配 + 两节点声明态（一真一假）
	hubJSON := `{"server":"hub","version":"v1","scope":"hub 全网","features":{"spaces":false},` +
		`"spaces":{"hub":false,"nodes":{"n-new":true,"n-old":false},"private_history_readable":false}}`
	out, err := renderCapabilities([]byte(hubJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"空间协作：中心服务未装配（空间操作暂不可用，空间面升级后开放）",
		"已声明空间特性的节点 1/2",
		"不表示任何一端私有会话历史可读",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("渲染缺 %q: %s", want, out)
		}
	}

	// hub 已装配形态（W07-02 接线后的词形，渲染器前瞻锁定）
	hubOn := `{"server":"hub","version":"v1","scope":"hub 全网","features":{"spaces":true},` +
		`"spaces":{"hub":true,"nodes":{"n-new":true},"private_history_readable":false}}`
	out, err = renderCapabilities([]byte(hubOn))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "空间协作：中心服务已装配") {
		t.Fatalf("已装配态渲染不符: %s", out)
	}

	// node 形态（无 spaces 键）：零变化回归锚——投影行与隐私行都不出现。
	nodeJSON := `{"server":"node","version":"v1","scope":"本机","features":{"spaces":true,"mcp_access_enabled":true}}`
	out, err = renderCapabilities([]byte(nodeJSON))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "空间协作：") || strings.Contains(out, "私有会话历史可读") {
		t.Fatalf("node 形态不得出现空间投影行: %s", out)
	}
	if !strings.Contains(out, "本机 MCP 编排授权：已启用") {
		t.Fatalf("node 授权行丢失（渲染回归）: %s", out)
	}
}
