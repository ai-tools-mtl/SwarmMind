// 总理凭证轮换（方案 3.2.1，2026-10-22）：控制台一键轮换 prime_token——重生成
// → 原子写盘（data_dir/prime_token，0600）→ 即时重载（旧凭证下一请求即失效）。
// 新凭证仅在应答中返回一次（与 hub 令牌创建同约定），持有人自行配给总理的
// MCP 连接。换总理三步（文档口径）：改 node.yaml prime_agent 声明 → 轮换 →
// 新总理配新凭证；运行中旧总理 MCP 连接下一请求 401/403。
package localapi

import "net/http"

// handlePrimeRotate POST /api/v1/local/prime/rotate：重生成总理凭证（仅
// local_token 可调——轮换是本机主人操作，不随编排凭证下发）。
func (s *Server) handlePrimeRotate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RotatePrime == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "总理凭证轮换未装配（RotatePrime 回调缺失）")
		return
	}
	tok, err := s.cfg.RotatePrime()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "轮换失败（旧凭证仍有效，可重试）: "+err.Error())
		return
	}
	s.SetPrimeToken(tok) // 即时重载：旧凭证下一请求即失效
	writeJSON(w, http.StatusOK, map[string]any{
		"prime_token": tok,
		"prime_agent": s.cfg.PrimeAgent,
		"note":        "新凭证仅此一次展示，请立即配给总理的 MCP 连接（SWARM_NODE_PRIME_TOKEN）；旧凭证已即时失效",
	})
}
