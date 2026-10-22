// 本机产物取回端点（方案 3.4，2026-10-22）：GET /api/v1/tasks/{id}/artifacts/{index}
// ——node MCP swarmmind_artifact 与本机控制台的数据面后端。与 hub 管理面对应端点
// 同路径同应答形态（application/octet-stream + X-Artifact-* 头），但取回不经
// artifact_fetch/artifact_data 中继帧：本地账本定位终态 → ArtifactRoots 解析
// 本机路径 → SHA-256 校验 → 直读返回（离线自治：hub 挂了本机产物照常可取）。
package localapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// handleArtifact GET /api/v1/tasks/{id}/artifacts/{index}：本机直取产物内容。
// 404 任务/产物不存在（无终态结果、下标越界、文件丢失或 agent 已从本机移除）；
// 409 内容与登记不一致（SHA-256/大小，禁止交付）；400 下标非法。
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ArtifactFetch == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "产物取回未装配（装配方须传入 ArtifactFetch）")
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "产物下标须为非负整数")
		return
	}
	meta, data, err := s.cfg.ArtifactFetch(r.PathValue("id"), index)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTaskNotFound):
			writeErr(w, http.StatusNotFound, protocol.CodeTaskNotFound, "任务不存在或已过保留期（终态保留 24h，离线任务 7 天）")
		case errors.Is(err, service.ErrArtifactMismatch):
			writeErr(w, http.StatusConflict, protocol.CodeArtifactMismatch, "产物内容与登记不一致（SHA-256/大小），已禁止交付")
		default: // ErrArtifactNotFound
			writeErr(w, http.StatusNotFound, protocol.CodeArtifactNotFound, "产物不存在（任务无终态结果/下标越界/文件丢失）")
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Artifact-Path", meta.Path)
	w.Header().Set("X-Artifact-Sha256", meta.Sha256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
