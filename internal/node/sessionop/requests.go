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
	"time"
)

// requestsFileName 请求登记文件名（相对 data_dir）。
const requestsFileName = "session_requests.json"

// requestsVersion 文件结构版本（未来迁移依据）。
const requestsVersion = 1

// RequestRecord 单条受理登记。
type RequestRecord struct {
	// RequestID 调用方提供的稳定创建请求 ID。
	RequestID string `json:"request_id"`
	// SessionID 原受理分配的平台会话 ID。
	SessionID string `json:"session_id"`
	// TurnSeq 原受理的平台轮序号。
	TurnSeq int64 `json:"turn_seq"`
	// PayloadHash 受理内容指纹（调用方定义的完整内容摘要；同键同文判定的第三元）。
	PayloadHash string `json:"payload_hash"`
	// AcceptedAt 首次受理时刻（UTC）。
	AcceptedAt time.Time `json:"accepted_at"`
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
	}
	s.reqs[requestID] = rec
	if err := s.saveRequestsLocked(); err != nil {
		delete(s.reqs, requestID) // 盘为真相：不保留内存登记，不虚报成功
		return RequestRecord{}, false, fmt.Errorf("sessionop: 请求登记落盘失败（未受理）: %w", err)
	}
	return rec, false, nil
}
