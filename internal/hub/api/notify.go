package api

// 通知渠道管理面（P4③ 控制台化升级）：渠道 CRUD、发测试、站内通知中心。
//
// 全部端点走 auth 中间件（会话/PAT 二选一，与其他管理面端点同一体系）。
// 读响应经 Channel.Masked 脱敏（口令字段/自定义头的值/内嵌登录凭证的地址 →
// ****** 占位）；更新传回占位保留原值（语义在 notify.ChannelStore.Update，
// 此处只透传）。本组端点为人工专属管理面配置（凭据授予类），按 SPEC 六.六
// 豁免不设 MCP 工具（理由见 SPEC 六.六规则 3⑦）。

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/mtl802/swarmmind/internal/notify"
)

// channelWriteReq 渠道创建/更新请求体。enabled/events 用指针区分「未提供
// （更新时保留原值）」与「提供」；events 空数组=全订阅。
type channelWriteReq struct {
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Enabled *bool             `json:"enabled"`
	Events  []string          `json:"events"`
	Secrets map[string]string `json:"secrets"`
}

// notifyReady 通知面可用性守卫：Dispatcher 未接线（旧构造方）时统一 503 降级，
// 与群聊端点 nil-Groups 同口径。
func (s *Server) notifyReady(w http.ResponseWriter) bool {
	if s.cfg.Notify == nil {
		writeErr(w, http.StatusServiceUnavailable, "notify_unavailable", "通知功能未启用")
		return false
	}
	return true
}

// handleNotifyChannelList GET /api/v1/notify/channels：渠道列表（凭证脱敏）。
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

// handleNotifyChannelCreate POST /api/v1/notify/channels：创建（body 含明文
// 凭证；请求体不落日志）。创建即启用，首次停用走更新。201 返回脱敏后的渠道。
func (s *Server) handleNotifyChannelCreate(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	var req channelWriteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为渠道配置 JSON")
		return
	}
	ch, err := s.cfg.Notify.Store().Create(req.Type, req.Name, req.Events, req.Secrets, time.Now())
	if err != nil {
		writeChannelErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch.Masked())
}

// handleNotifyChannelUpdate PUT /api/v1/notify/channels/{id}：更新（部分更新
// 合法：只传 enabled 的启用开关等）；secrets ****** 占位保留原值。
// 200 返回脱敏后的渠道 / 404 / 400。
func (s *Server) handleNotifyChannelUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	var req channelWriteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为渠道配置 JSON")
		return
	}
	ch, err := s.cfg.Notify.Store().Update(r.PathValue("id"), req.Type, req.Name, req.Enabled, req.Events, req.Events != nil, req.Secrets)
	if err != nil {
		writeChannelErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch.Masked())
}

// handleNotifyChannelDelete DELETE /api/v1/notify/channels/{id}：删除。
// 200 / 404。
func (s *Server) handleNotifyChannelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	deleted, err := s.cfg.Notify.Store().Delete(r.PathValue("id"))
	if err != nil {
		log.Printf("[hub] 渠道删除落盘失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "删除失败")
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNotifyChannelTest POST /api/v1/notify/channels/{id}/test：发一条测试
// 消息（单次尝试，不重试——管理面不等 27s 重试链）。200 {ok, error}
// （投递失败也是 200：测试结果本身即应答）/ 404 渠道不存在。
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

// inboxReadReq 站内通知标已读请求体：id 缺省=全部标已读。
type inboxReadReq struct {
	ID string `json:"id"`
}

// handleNotifyInbox GET /api/v1/notify/inbox：站内通知最近 20 条 + 未读数。
// 轻实现如实标注：内存态不持久化，hub 重启清空（persistent=false 供前端展示）。
func (s *Server) handleNotifyInbox(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	items, unread := s.cfg.Notify.Inbox().Recent(20)
	writeJSON(w, http.StatusOK, map[string]any{
		"notifications": items,
		"unread":        unread,
		"persistent":    false,
	})
}

// handleNotifyInboxRead POST /api/v1/notify/inbox/read：标记已读（{id} 单条，
// 空体全部）。200 {marked} / 404 单条未命中。
func (s *Server) handleNotifyInboxRead(w http.ResponseWriter, r *http.Request) {
	if !s.notifyReady(w) {
		return
	}
	var req inboxReadReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		// 空请求体合法（全部标已读）；其余解码失败拒绝
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {id?}")
		return
	}
	if req.ID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"marked": s.cfg.Notify.Inbox().MarkAllRead()})
		return
	}
	if !s.cfg.Notify.Inbox().MarkRead(req.ID) {
		writeErr(w, http.StatusNotFound, "notification_not_found", "通知不存在（可能已随重启清空）")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked": 1})
}

// writeChannelErr 渠道错误 → HTTP 状态与错误码。
func writeChannelErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notify.ErrNotFound):
		writeErr(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
	case errors.Is(err, notify.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		log.Printf("[hub] 渠道配置落盘失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "保存失败")
	}
}
