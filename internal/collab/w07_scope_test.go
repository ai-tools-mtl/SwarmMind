package collab

// w07_scope_test.go 空间 scope 词形校验点测试（W07-01）：中心空间 scope 固定
// "hub:<id>"（任务书红线），node 收件校验（SessionTurnRef.Space 拒绝面第一层）
// 的词形判定单源行为锁定——hub: 空 id / 任意字符串 / 带空白一律非法，本机空间
// ID（sp-）合法。

import "testing"

func TestW07HubScopeRoundTrip(t *testing.T) {
	if got := HubScope("sp-abc"); got != "hub:sp-abc" {
		t.Fatalf("HubScope 构造不符: %q", got)
	}
	if id := HubScopeID("hub:sp-abc"); id != "sp-abc" {
		t.Fatalf("HubScopeID 取段不符: %q", id)
	}
	if id := HubScopeID("sp-abc"); id != "" {
		t.Fatalf("非 hub 词形取段应空: %q", id)
	}
	if !IsHubScope("hub:x") || IsHubScope("hub:") || IsHubScope("sp-x") {
		t.Fatal("IsHubScope 判定不符")
	}
}

func TestW07ValidScopeClaim(t *testing.T) {
	cases := []struct {
		scope string
		want  bool
	}{
		{"hub:sp-center-1", true}, // 中心空间词形
		{"hub:whatever-id", true}, // id 段不做语义解释（词形层只保真）
		{"sp-local1", true},       // 本机空间 ID
		{"sp-", false},            // 本机前缀空 ID 非法
		{"hub:", false},           // hub 前缀空 id 非法
		{"hub: sp", false},        // id 段带前导空白非法
		{"hub:sp ", false},        // 尾随空白非法
		{"g-group1", false},       // 旧群前缀不是空间 scope
		{"", false},               // 空 = 普通轮（词形层不会收到空——门在空值短路）
		{"node:n1", false},        // 未定义词形一律拒绝（不猜）
		{"hub:opaque;seg", true},  // id 段不做语义解释（词形层只管形态；防冒充靠绑定比对）
	}
	for _, c := range cases {
		if got := ValidScopeClaim(c.scope); got != c.want {
			t.Fatalf("ValidScopeClaim(%q) = %v, want %v", c.scope, got, c.want)
		}
	}
}
