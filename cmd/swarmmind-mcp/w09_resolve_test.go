// w09_resolve_test.go 留待核对人工裁决工具实测（W09 工作块 B，docs/tasks/v3/
// W09-hardening.md 工作块 B「MCP 同步」；AGENTS.md 管理面 REST↔MCP 同步硬规）：
// 进程内真 hub api（真 collab 内核 + 真 spaces 服务 + 策略引擎）+ hubBackend
// 工具链——
//   - tools/list：swarmmind_space_intent_resolve 在 hub 清单（hub 模式专属追加件，
//     不入共享九件词典面）；
//   - 描述诚实性：accept/reject 双裁决词与主人专属边界在册（「能做什么」面）；
//   - 实调（真调用）：参数缺失/词形工具面拒绝；PAT 编排主体真调 hub REST → 服务端
//     403 主人专属人话原话透传（工具面零第二套权限——裁决归主人管理会话/控制台）；
//     服务端 accept 收束全链（admin 会话侧）由 internal/hub/api spaces_resolve_test
//     契约测试与本包服务端同核承担（PAT 无主人面凭据——既有 hub 工具同口径）。
package main

import (
	"strings"
	"testing"
)

// TestHubSpaceIntentResolveToolEndToEnd 裁决工具清单 + 实调（PAT 403 透明透传）。
func TestHubSpaceIntentResolveToolEndToEnd(t *testing.T) {
	ts, pat := newHubSpacesServer(t)
	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = ts.URL, pat
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })

	// tools/list：裁决工具在 hub 清单（描述含双裁决词与主人专属边界）。
	found := false
	for _, td := range (hubBackend{}).tools() {
		if td.Name == "swarmmind_space_intent_resolve" {
			found = true
			for _, want := range []string{"accept", "reject", "留待核对", "主人专属"} {
				if !strings.Contains(td.Description, want) {
					t.Fatalf("裁决工具描述应含 %q: %s", want, td.Description)
				}
			}
			if td.InputSchema == nil {
				t.Fatal("裁决工具应声明 InputSchema")
			}
		}
	}
	if !found {
		t.Fatal("hub 清单缺 swarmmind_space_intent_resolve")
	}

	bd := hubBackend{}
	// 参数缺失工具面拒绝（必填三件）。
	if _, isErr := callText(t, bd, "swarmmind_space_intent_resolve", map[string]any{"space_id": "sp-x"}); !isErr {
		t.Fatal("缺 request_id/verdict 应工具面拒绝")
	}
	// 非法词形工具面拒绝。
	if _, isErr := callText(t, bd, "swarmmind_space_intent_resolve",
		map[string]any{"space_id": "sp-x", "request_id": "rid", "verdict": "maybe"}); !isErr {
		t.Fatal("verdict=maybe 应工具面拒绝")
	}
	// 真调用（PAT）：服务端 403 主人专属人话原话透传（零第二套权限）。
	got := errText(t, bd, "swarmmind_space_intent_resolve",
		map[string]any{"space_id": "sp-x", "request_id": "rid-x", "verdict": "accept"})
	for _, want := range []string{"403", "主人专属"} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAT 真调用应透传服务端 403 主人专属（得 %q）", got)
		}
	}
}
