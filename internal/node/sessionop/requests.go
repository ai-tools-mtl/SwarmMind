// session_requests.json 持久登记（W01-02 RFC §1）：request_id → 受理身份与内容
// 指纹。同键同文返回原受理（幂等重放查回原回执的依据）、同键异文显式冲突；落盘
// 失败不保留内存登记（盘为真相，重试重新登记），绝不虚报登记成功。原子写纪律同
// sessions.json：独占创建 tmp + fsync + rename（0600），时间一律 UTC。
package sessionop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// requestsFileName 请求登记文件名（相对 data_dir）。
const requestsFileName = "session_requests.json"

// requestsVersion 文件结构版本（未来迁移依据）。
const requestsVersion = 1

// 受理登记种类（RequestRecord.Kind；W01-03 加法）。空值按 turn 语义（W01-02
// 时代的既有登记全部为轮受理）。
const (
	RequestKindSession = "session" // 会话创建登记（TurnSeq 恒 0；pending 投影源）
	RequestKindTurn    = "turn"    // 轮受理登记（重放回执源）
)

// RequestRecord 单条受理登记。
type RequestRecord struct {
	// RequestID 调用方提供的稳定创建请求 ID。
	RequestID string `json:"request_id"`
	// SessionID 原受理分配的平台会话 ID。
	SessionID string `json:"session_id"`
	// TurnSeq 原受理的平台轮序号（创建登记恒 0——管理登记非对话轮）。
	TurnSeq int64 `json:"turn_seq"`
	// PayloadHash 受理内容指纹（调用方定义的完整内容摘要；同键同文判定的第三元）。
	PayloadHash string `json:"payload_hash"`
	// AcceptedAt 首次受理时刻（UTC）。
	AcceptedAt time.Time `json:"accepted_at"`

	// ---- W01-03 加法元数据（旧文件缺省零值，加载零迁移；W01-03 RFC §2）----

	// Kind 登记种类：RequestKindSession | RequestKindTurn（空按 turn 语义）。
	Kind string `json:"kind,omitempty"`
	// Op 轮受理的操作类型（kind=turn；重放回执权威，受理时定案不随后续改写，
	// 同 hub rowTurnOp 口径）。
	Op string `json:"op,omitempty"`
	// Agent 会话创建元数据（kind=session；pending 投影源；轮登记为空）。
	Agent string `json:"agent,omitempty"`
	// Cwd 会话创建元数据（kind=session）。
	Cwd string `json:"cwd,omitempty"`
	// Title 会话标题（kind=session，可选；内核无 title 字段，仅受理登记期可见）。
	Title string `json:"title,omitempty"`
	// TaskID 轮受理回执重放源（kind=turn；受理任务 ID，不依赖账本保留窗口）。
	TaskID string `json:"task_id,omitempty"`
	// TraceID 轮受理回执重放源（kind=turn）。
	TraceID string `json:"trace_id,omitempty"`
}

// requestsFile 磁盘形态。
type requestsFile struct {
	Version  int             `json:"version"`
	Requests []RequestRecord `json:"requests"`
}

// loadRequests 装载登记文件（仅 New 启动期调用，无锁要求）。缺失 = 首次运行；
// 损坏按空表起步并如实记日志（同 sessions.json 纪律：不阻断启动，下次落盘整体
// 重写）。
func (s *Service) loadRequests() {
	b, err := os.ReadFile(s.reqPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.reqLog.Printf("sessionop: %s 读取失败（%v），按空表起步", requestsFileName, err)
		}
		return
	}
	var p requestsFile
	if err := json.Unmarshal(b, &p); err != nil || p.Version < 1 {
		s.reqLog.Printf("sessionop: %s 损坏（%v），按空表起步，下次落盘整体重写", requestsFileName, err)
		return
	}
	for _, r := range p.Requests {
		if r.RequestID == "" {
			continue // 空条目防御
		}
		if _, dup := s.reqs[r.RequestID]; dup {
			continue // 重复 ID 防御：保留先到
		}
		s.reqs[r.RequestID] = r
	}
}

// saveRequestsLocked 全量原子落盘（调用方持 reqMu）。任一步失败：清理 tmp、
// 返回错误，磁盘旧正本原样保留；rename 成功即提交。reqInject 为包内测试故障
// 注入（生产恒 nil）。
func (s *Service) saveRequestsLocked() error {
	if s.reqInject != nil {
		if err := s.reqInject("persist"); err != nil {
			return err
		}
	}
	list := make([]RequestRecord, 0, len(s.reqs))
	for _, r := range s.reqs {
		list = append(list, r)
	}
	// 排序稳定（按受理时刻 + request_id），字节序确定。
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && recordLess(list[j], list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	b, err := json.MarshalIndent(requestsFile{Version: requestsVersion, Requests: list}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.reqPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// 上次落盘崩溃残留的 tmp：清掉重试一次（磁盘旧正本未动，安全）。
		if rmErr := os.Remove(tmp); rmErr != nil {
			return fmt.Errorf("清理残留临时文件失败: %w", rmErr)
		}
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.reqPath); err != nil {
		os.Remove(tmp)
		return err
	}
	// 目录 fsync 尽力而为（RFC §1 声明的崩溃收敛语义）：rename 已完成不可回滚，
	// fsync 失败只降级为「尽力而为」并如实注记，不虚报持久化失败；目录打不开
	// 同样注记（codex 终审顺手项：失败分支不静默）。
	if d, derr := os.Open(filepath.Dir(s.reqPath)); derr == nil {
		if serr := d.Sync(); serr != nil {
			s.reqLog.Printf("sessionop: 请求登记目录 fsync 失败（尽力而为，rename 已完成）: %v", serr)
		}
		_ = d.Close()
	} else {
		s.reqLog.Printf("sessionop: 请求登记目录打开失败（跳过目录 fsync，rename 已完成）: %v", derr)
	}
	return nil
}

// recordLess 登记排序键：(accepted_at, request_id)。
func recordLess(a, b RequestRecord) bool {
	if !a.AcceptedAt.Equal(b.AcceptedAt) {
		return a.AcceptedAt.Before(b.AcceptedAt)
	}
	return a.RequestID < b.RequestID
}

// LookupRequest 查询原受理登记（不存在 ok=false）。
func (s *Service) LookupRequest(requestID string) (RequestRecord, bool) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	r, ok := s.reqs[requestID]
	return r, ok
}

// ResolveRequest 创建请求 ID 登记/解析原语（RFC §1）：互斥段内查登记——
//   - 未登记：登记并持久化，返回 dup=false（调用方据此继续受理）；
//   - 同键同文（session_id + turn_seq + payload_hash 三元组全等）：返回原受理
//     dup=true（同键同文返回原受理）；
//   - 同键异文：ErrRequestConflict。
//
// 落盘失败：内存登记不保留（盘为真相，重试重新登记），错误如实上抛——绝不虚报
// 登记成功。request_id 为空拒绝（可选语义由 W01-03 接线层裁量）。
func (s *Service) ResolveRequest(requestID, sessionID string, turnSeq int64, payloadHash string) (RequestRecord, bool, error) {
	return s.ResolveRequestWith(requestID, sessionID, turnSeq, payloadHash, RequestMeta{})
}

// RequestMeta 受理登记附加元数据（W01-03 加法，RFC §2）：不参与同键同文判定
// （三元组判定不变），仅随登记持久化供投影与回执重放（pending 会话元数据、轮
// 受理回执重放源）。
type RequestMeta struct {
	// Kind 登记种类：RequestKindSession | RequestKindTurn；空按 turn 语义。
	Kind string
	// Op 轮受理的操作类型（kind=turn）。
	Op string
	// Agent/Cwd/Title 会话创建元数据（kind=session）。
	Agent string
	Cwd   string
	Title string
	// TaskID/TraceID 轮受理回执重放源（kind=turn）。
	TaskID  string
	TraceID string
}

// ResolveRequestWith 带元数据的 ResolveRequest 变体（W01-03）：判定语义与
// ResolveRequest 逐字节一致（三元组全等回放、异文冲突、落盘失败不保留），
// 元数据随登记持久化。既有 ResolveRequest 即本函数空元数据形态（签名零变化）。
func (s *Service) ResolveRequestWith(requestID, sessionID string, turnSeq int64, payloadHash string, meta RequestMeta) (RequestRecord, bool, error) {
	if requestID == "" {
		return RequestRecord{}, false, errors.New("sessionop: request_id 为空")
	}
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	if existing, ok := s.reqs[requestID]; ok {
		if existing.SessionID == sessionID && existing.TurnSeq == turnSeq && existing.PayloadHash == payloadHash {
			return existing, true, nil
		}
		return RequestRecord{}, false, fmt.Errorf("%w: %s（已登记 session=%s seq=%d，本次 session=%s seq=%d）",
			ErrRequestConflict, requestID, existing.SessionID, existing.TurnSeq, sessionID, turnSeq)
	}
	rec := RequestRecord{
		RequestID:   requestID,
		SessionID:   sessionID,
		TurnSeq:     turnSeq,
		PayloadHash: payloadHash,
		AcceptedAt:  s.now(),
		Kind:        meta.Kind,
		Op:          meta.Op,
		Agent:       meta.Agent,
		Cwd:         meta.Cwd,
		Title:       meta.Title,
		TaskID:      meta.TaskID,
		TraceID:     meta.TraceID,
	}
	s.reqs[requestID] = rec
	if err := s.saveRequestsLocked(); err != nil {
		delete(s.reqs, requestID) // 盘为真相：不保留内存登记，不虚报成功
		return RequestRecord{}, false, fmt.Errorf("sessionop: 请求登记落盘失败（未受理）: %w", err)
	}
	return rec, false, nil
}

// SessionCreation 按平台会话 ID 查创建登记（kind=session 且 SessionID 命中；
// pending 会话续轮推导与详情合成的查询原语，W01-03）。不存在 ok=false。
func (s *Service) SessionCreation(sessionID string) (RequestRecord, bool) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	for _, r := range s.reqs {
		if r.Kind == RequestKindSession && r.SessionID == sessionID {
			return r, true
		}
	}
	return RequestRecord{}, false
}

// SessionCreations 全量创建登记快照（kind=session；agent 面会话列表 pending
// 合成源，W01-03）。按受理时刻升序（与落盘排序同键，读序确定）。
func (s *Service) SessionCreations() []RequestRecord {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	out := make([]RequestRecord, 0, len(s.reqs))
	for _, r := range s.reqs {
		if r.Kind == RequestKindSession {
			out = append(out, r)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && recordLess(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
