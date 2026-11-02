// scope.go 空间 scope 词形与校验点（W07-01，docs/tasks/v3/W07-hub-routing.md
// 「中心空间scope固定为hub:<id>」）：中心空间与本机空间的 scope 词形单源定义，
// node 收件校验（SessionTurnRef.Space 拒绝面）与 W07-02 hub 空间服务（创建/
// 加载中心空间时固定 hub: 前缀）共用。纯函数零 IO。
package collab

import "strings"

// ScopeHubPrefix 中心空间 scope 前缀：中心创建的空间 scope 固定为
// "hub:<id>"（id 为 hub 空间服务分配的空间标识）。node 本机空间不携带本前缀
// （本机空间以自身 ID sp-… 为 scope，只在执行 node 本地有意义）。
const ScopeHubPrefix = "hub:"

// HubScope 构造中心空间 scope（"hub:<id>"；W07-02 hub 空间服务创建/引用空间
// 时的唯一合法词形——scope 固定不开放其他形态）。
func HubScope(id string) string { return ScopeHubPrefix + id }

// HubScopeID 取中心空间 scope 的 id 段（"hub:<id>" → <id>；非 hub 词形或 id 段
// 带空白/为空返回空——词形清洁，防 "hub: <id>" 冒充）。
func HubScopeID(scope string) string {
	if !strings.HasPrefix(scope, ScopeHubPrefix) {
		return ""
	}
	id := strings.TrimPrefix(scope, ScopeHubPrefix)
	if id == "" || id != strings.TrimSpace(id) {
		return ""
	}
	return id
}

// IsHubScope 判定 scope 是否为中心空间词形（hub: + 非空且无空白环绕的 id）。
func IsHubScope(scope string) bool { return HubScopeID(scope) != "" }

// ValidScopeClaim 空间来源标记词形校验（SessionTurnRef.Space 收件校验第一层）：
// 合法词形 = 中心空间 "hub:<id>"（非空 id）或本机空间 ID（SpaceIDPrefix 前缀 +
// 非空 ID 段，本机派发轮自带 scope 的形态）。其余词形（任意字符串冒充/跨词形
// 拼接/带空白）一律非法——node 收到即拒绝，不做前缀外猜测。
func ValidScopeClaim(scope string) bool {
	if IsHubScope(scope) {
		return true
	}
	if !strings.HasPrefix(scope, SpaceIDPrefix) {
		return false
	}
	return strings.TrimPrefix(scope, SpaceIDPrefix) != "" && scope == strings.TrimSpace(scope)
}
