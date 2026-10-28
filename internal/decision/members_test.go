// members_test.go SuggestMembers 协作成员多选（DESIGN-butler 方案三，2026-10-28）：
// 多选语义与单选独立（R3' P1-6 纪律），覆盖正常/越界剔除/NoFit/降级/超时/未启用/
// 非 memberSelector provider 各路。
package decision

import (
	"context"
	"errors"
	"testing"
	"time"
)

// memberMock 可编程多选 provider：mockProvider 基座上补 memberSelector 可选接口。
type memberMock struct {
	mockProvider
	members   []string
	reason    string
	err       error
	multiWait time.Duration // SuggestMembers 内睡眠（预算收敛测试用）
}

func (m *memberMock) SuggestMembers(ctx context.Context, goal string, cands []Candidate) ([]string, string, error) {
	if m.multiWait > 0 {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(m.multiWait):
		}
	}
	return m.members, m.reason, m.err
}

func TestSuggestMembersHappy(t *testing.T) {
	mp := &memberMock{members: []string{"diskcheck@win-box", "zcode-dev@mac-mini"}, reason: "巡检加编码互补"}
	s := NewService(mp, time.Second)
	res := s.SuggestMembers(Request{Goal: "盘点两台机器磁盘并汇总", Candidates: testCandidates()})
	if res.Degraded || len(res.Members) != 2 || res.Members[0] != "diskcheck@win-box" {
		t.Fatalf("应给出成员集合: %+v", res)
	}
	if res.Reason != "巡检加编码互补" || res.Provider != "mock" {
		t.Fatalf("理由/来源不符: %+v", res)
	}
}

func TestSuggestMembersOutOfBoundsDropped(t *testing.T) {
	mp := &memberMock{members: []string{"ghost@nowhere", "diskcheck@win-box"}} // 首个越界
	s := NewService(mp, time.Second)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if res.Degraded || len(res.Members) != 1 || res.Members[0] != "diskcheck@win-box" {
		t.Fatalf("越界候选应剔除: %+v", res)
	}
}

func TestSuggestMembersNoFitNote(t *testing.T) {
	mp := &memberMock{err: ErrNoFit}
	s := NewService(mp, time.Second)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if res.Degraded || res.Members != nil {
		t.Fatalf("NoFit 应非降级且无成员: %+v", res)
	}
	if res.Note == "" {
		t.Fatal("NoFit 应带人话 Note")
	}
}

func TestSuggestMembersDegraded(t *testing.T) {
	mp := &memberMock{err: errors.New("决策模型网络请求失败")}
	s := NewService(mp, time.Second)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if !res.Degraded || res.Members != nil || res.Note == "" {
		t.Fatalf("provider 失败应降级带 Note: %+v", res)
	}
}

func TestSuggestMembersTimeout(t *testing.T) {
	mp := &memberMock{multiWait: 300 * time.Millisecond}
	s := NewService(mp, 20 * time.Millisecond) // 预算小于睡眠时长 → 超时降级
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if !res.Degraded {
		t.Fatalf("超预算应降级: %+v", res)
	}
}

func TestSuggestMembersNotEnabledAndEmpty(t *testing.T) {
	var zero *Service
	if res := zero.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()}); res.Note != NotEnabledNote {
		t.Fatalf("零值应未启用: %+v", res)
	}
	s := NewService(&memberMock{}, time.Second)
	if res := s.SuggestMembers(Request{Goal: " ", Candidates: testCandidates()}); res.Members != nil || res.Note == "" {
		t.Fatalf("空 goal 应人话拒绝: %+v", res)
	}
	if res := s.SuggestMembers(Request{Goal: "g"}); res.Members != nil || res.Note == "" {
		t.Fatalf("零候选应人话拒绝: %+v", res)
	}
}

func TestSuggestMembersNoSelector(t *testing.T) {
	// 纯 mockProvider（未实现 memberSelector）→ 判定不支持，降级语义
	s := NewService(&mockProvider{}, time.Second)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if !res.Degraded || res.Note == "" {
		t.Fatalf("无多选能力的 provider 应降级: %+v", res)
	}
}

// cancelProbeMember 配合取消的探针 provider：ctx.Done 即退出并上报取消路径；
// 预算内未取消则走 1s 长睡眠后完成（测试断言其不被走到）。
type cancelProbeMember struct {
	mockProvider
	observed chan bool
}

func (p *cancelProbeMember) SuggestMembers(ctx context.Context, goal string, cands []Candidate) ([]string, string, error) {
	select {
	case <-ctx.Done():
		p.observed <- true
		return nil, "", ctx.Err()
	case <-time.After(time.Second):
		p.observed <- false
		return nil, "", ErrNoFit
	}
}

// TestSuggestMembersCancelsUpstream 预算耗尽必须取消上游（R1-04，对齐 Suggest 的
// call 纪律）：API 按预算返回降级，同时 provider 观察到 ctx 取消——两个断言都要
// 有，只测前者不能证明上游释放（HTTP 连接/模型请求/goroutine 不占到期满）。
func TestSuggestMembersCancelsUpstream(t *testing.T) {
	p := &cancelProbeMember{observed: make(chan bool, 1)}
	s := NewService(p, 20*time.Millisecond)
	start := time.Now()
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if !res.Degraded {
		t.Fatalf("超预算应降级: %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("API 应按预算返回, 耗时 %v", elapsed)
	}
	select {
	case ok := <-p.observed:
		if !ok {
			t.Fatal("provider 走完了长睡眠路径：预算耗尽未取消上游 ctx")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("预算耗尽后 provider 未观察到取消（仍占用上游调用）")
	}
}
