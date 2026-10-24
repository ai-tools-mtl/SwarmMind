// node 本地通知渠道管理面（standalone 通知补盲区，2026-10-24；与 hub api/notify.go
// 同一家族）：渠道 CRUD、发测试。五端点全部仅 local_token 可调（本机主人管理面，
// 鉴权分级方案 3.2.1——prime_token 是编排凭证，不放行凭据配置）。
//
// 与 hub 侧的口径差异（其余语义一一对应）：
//   - inbox 类型在 node 准入即拒绝（400 inbox_unsupported）——站内通知是 hub
//     控制台铃铛专属（node 无站内动线，Dispatcher nil inbox 装配），宁可明确
//     报错，不收一条永远发不出去的渠道配置（同 sms 口径）；
//   - 网络模式下端点 503（notify_unavailable）——通知归 hub，本机渠道配置了
//     也不会发（Configure 装配即排除，见 cmd/node openNotifyDispatcher）。
//
// 安全红线同 hub：读响应经 Channel.Masked 脱敏（口令字段/自定义头的值/内嵌登录
// 凭证的地址 → ****** 占位）；更新传回占位保留原值（语义在 notify.ChannelStore，
// 此处只透传）；投递用原始完整地址、展示/错误路径脱敏（都在 internal/notify）。
// 本组端点为人工专属管理面配置（凭据授予类），按 SPEC 六.六豁免不设 MCP 工具。
package localapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/mtl802/swarmmind/internal/notify"
)

// channelWriteReq 渠道创建/更新请求体。enabled/events 用指针区分「未提供
// （更新时保留原值）」与「提供」；events 空数组=全订阅。与 hub 同形。
type channelWriteReq struct {
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Enabled *bool             `json:"enabled"`
	Events  []string          `json:"events"`
	Secrets map[string]string `json:"secrets"`
}

// notifyReady 通知面可用性守卫：Dispatcher 未装配（networked/setup）时统一
// 503 降级，与 hub api notifyReady 同口径。
func (s *Server) notifyReady(w http.ResponseWriter) bool {
	if s.cfg.Notify == nil {
		writeErr(w, http.StatusServiceUnavailable, "notify_unavailable",
			"通知功能仅 standalone 模式可用（组网模式任务通知由 hub 统一发送，本机不重复配置）")
		return false
	}
	return true
}

// rejectInboxForNode node 侧 inbox 类型准入拒绝：站内通知是 hub 控制台专属，
// node 收下也只能是永远发不出去的配置。创建与类型切换两路共用。
func rejectInboxForNode(w http.ResponseWriter, typ string) bool {
	if typ == notify.TypeInbox {
		writeErr(w, http.StatusBadRequest, "inbox_unsupported",
			"站内通知（inbox）为 hub 控制台专属渠道，node 本机不支持；请改用飞书 / 邮件 / webhook 渠道")
		return true
	}
	return false
}

// handleNotifyChannelList GET /api/v1/local/notify/channels：渠道列表（凭证脱敏）。
func (s *Server) handleNotifyChannelList(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	chs := s.cfg.Notify.Store().List()
	out := make([]notify.Channel, len(chs))
	for i, c := range chs {
		out[i] = c.Masked()
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// handleNotifyChannelCreate POST /api/v1/local/notify/channels：创建（body 含
// 明文凭证；请求体不落日志）。创建即启用，首次停用走更新。201 返回脱敏后的渠道。
func (s *Server) handleNotifyChannelCreate(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	var req channelWriteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为渠道配置 JSON")
		return
	}
	if rejectInboxForNode(w, req.Type) {
		return
	}
	ch, err := s.cfg.Notify.Store().Create(req.Type, req.Name, req.Events, req.Secrets, time.Now())
	if err != nil {
		writeChannelErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch.Masked())
}

// handleNotifyChannelUpdate PUT /api/v1/local/notify/channels/{id}：更新（部分
// 更新合法：只传 enabled 的启用开关等）；secrets ****** 占位保留原值。
// 200 返回脱敏后的渠道 / 404 / 400（含切换到 inbox 类型的拒绝）。
func (s *Server) handleNotifyChannelUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	var req channelWriteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为渠道配置 JSON")
		return
	}
	if rejectInboxForNode(w, req.Type) {
		return
	}
	ch, err := s.cfg.Notify.Store().Update(r.PathValue("id"), req.Type, req.Name, req.Enabled, req.Events, req.Events != nil, req.Secrets)
	if err != nil {
		writeChannelErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch.Masked())
}

// handleNotifyChannelDelete DELETE /api/v1/local/notify/channels/{id}：删除。
// 200 / 404。
func (s *Server) handleNotifyChannelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	deleted, err := s.cfg.Notify.Store().Delete(r.PathValue("id"))
	if err != nil {
		log.Printf("[node] 通知渠道删除落盘失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "删除失败")
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNotifyChannelTest POST /api/v1/local/notify/channels/{id}/test：发一条
// 测试消息（单次尝试，不走重试链——管理面不等 27s）。200 {ok, error}
//（投递失败也是 200：测试结果本身即应答）/ 404 渠道不存在。
func (s *Server) handleNotifyChannelTest(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	id := r.PathValue("id")
	if _, exists := s.cfg.Notify.Store().Get(id); !exists {
		writeErr(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
		return
	}
	ok, msg := s.cfg.Notify.TestChannel(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "error": msg})
}

// writeChannelErr 渠道错误 → HTTP 状态与错误码（与 hub 同表）。
func writeChannelErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notify.ErrNotFound):
		writeErr(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
	case errors.Is(err, notify.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		log.Printf("[node] 通知渠道配置落盘失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "保存失败")
	}
}
