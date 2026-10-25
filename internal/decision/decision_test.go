package decision

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

// mockProvider 可编程 provider：记录调用并按脚本应答（测试装配）。
type mockProvider struct {
	name  string
	model string
	calls int

	sug  *Suggestion
	err  error
	wait time.Duration // Suggest 内睡眠（预算收敛测试用）
}

func (m *mockProvider) Name() string {
	if m.name == "" {
		return "mock"
	}
	return m.name
}
func (m *mockProvider) Model() string { return m.model }

func (m *mockProvider) Suggest(ctx context.Context, req Request) (*Suggestion, error) {
	m.calls++
	if m.wait > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.wait):
		}
	}
	return m.sug, m.err
}

func testCandidates() []Candidate {
	return []Candidate{
		{NodeID: "mac-mini", Agent: "zcode-dev", Description: "编码智能体", Inflight: 1},
		{NodeID: "win-box", Agent: "diskcheck", Description: "磁盘巡检脚本", Inflight: 0},
	}
}

func TestServiceHappyPath(t *testing.T) {
	mp := &mockProvider{sug: &Suggestion{TargetNode: "win-box", TargetAgent: "diskcheck",
		Confidence: 0.9, Probabilities: map[string]float64{"diskcheck@win-box": 0.9, "zcode-dev@mac-mini": 0.1},
		Reason: "巡检脚本对口", Model: "glm-5.3-flash"}}
	s := NewService(mp, time.Second)
	res := s.Suggest(Request{Goal: "检查 C 盘剩余空间", Candidates: testCandidates()})
	if res.Suggestion == nil || res.Degraded {
		t.Fatalf("应有建议且非降级: %+v", res)
	}
	if res.Suggestion.TargetAgent != "diskcheck" || res.Suggestion.TargetNode != "win-box" {
		t.Fatalf("目标不符: %+v", res.Suggestion)
	}
	if res.Suggestion.Provider != "mock" {
		t.Fatalf("Service 应盖章 provider: %+v", res.Suggestion)
	}
	if res.Suggestion.Model != "glm-5.3-flash" {
		t.Fatalf("model 透传: %+v", res.Suggestion)
	}
	if mp.calls != 1 {
		t.Fatalf("provider 应被调用一次, got %d", mp.calls)
	}
}

func TestServiceNotEnabled(t *testing.T) {
	var s *Service
	res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion != nil || res.Note == "" {
		t.Fatalf("nil Service 应安全降级: %+v", res)
	}
	s2 := &Service{} // 零值 = 未启用
	res = s2.Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion != nil || res.Degraded || res.Note != NotEnabledNote {
		t.Fatalf("未启用应返回 NotEnabledNote: %+v", res)
	}
	if s2.Enabled() || s2.ProviderName() != "" {
		t.Fatalf("未启用判定不符")
	}
}

func TestServiceEmptyInputs(t *testing.T) {
	mp := &mockProvider{}
	s := NewService(mp, time.Second)
	res := s.Suggest(Request{Goal: "  ", Candidates: testCandidates()})
	if res.Suggestion != nil || res.Note == "" {
		t.Fatalf("空 goal 应无建议: %+v", res)
	}
	res = s.Suggest(Request{Goal: "x", Candidates: nil})
	if res.Suggestion != nil || res.Note == "" {
		t.Fatalf("空候选应无建议: %+v", res)
	}
	if mp.calls != 0 {
		t.Fatalf("短路路径不得调 provider, got %d", mp.calls)
	}
}

func TestServiceDegradedPaths(t *testing.T) {
	t.Run("provider 错误", func(t *testing.T) {
		s := NewService(&mockProvider{err: errors.New("boom")}, time.Second)
		res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
		if res.Suggestion != nil || !res.Degraded || res.Note == "" {
			t.Fatalf("错误应降级: %+v", res)
		}
	})
	t.Run("越权目标", func(t *testing.T) {
		s := NewService(&mockProvider{sug: &Suggestion{TargetNode: "evil", TargetAgent: "root", Confidence: 1}}, time.Second)
		res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
		if res.Suggestion != nil || !res.Degraded || res.Note == "" {
			t.Fatalf("越权目标应弃用: %+v", res)
		}
	})
	t.Run("硬超时", func(t *testing.T) {
		s := NewService(&mockProvider{wait: 300 * time.Millisecond}, 30*time.Millisecond)
		start := time.Now()
		res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("硬超时应按时返回（不等 provider）, took %v", elapsed)
		}
		if res.Suggestion != nil || !res.Degraded || res.Note == "" {
			t.Fatalf("超时应降级: %+v", res)
		}
	})
	t.Run("choice=none", func(t *testing.T) {
		s := NewService(&mockProvider{err: ErrNoFit}, time.Second)
		res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
		if res.Suggestion != nil || res.Degraded || res.Note == "" {
			t.Fatalf("choice=none 是正常无建议非降级: %+v", res)
		}
	})
}

func TestNormalizeProbs(t *testing.T) {
	cands := testCandidates()
	key := "diskcheck@win-box"
	other := "zcode-dev@mac-mini"
	t.Run("P(choice)=conf 其余候选按原比例归一到 1-conf", func(t *testing.T) {
		got := normalizeProbs(map[string]float64{key: 2, other: 1, "ghost@x": 5, "neg": -1}, cands, key, 0.5)
		want := map[string]float64{key: 0.5, other: 0.5}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("choice 缺失回落 confidence（其余缺失均匀补齐，分布恒合法）", func(t *testing.T) {
		// codex 审 D1 ①复现形态：conf=0.6 但只给了 choice 概率 → 旧实现总和 0.6，
		// 现补齐为 {choice:0.6, other:0.4}。
		got := normalizeProbs(map[string]float64{key: 0.6}, cands, key, 0.6)
		if math.Abs(got[key]-0.6) > 1e-9 || math.Abs(got[other]-0.4) > 1e-9 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("conf 缺失回落 probabilities[choice]", func(t *testing.T) {
		got := normalizeProbs(map[string]float64{key: 0.9, other: 0.1}, cands, key, 0)
		if math.Abs(got[key]-0.9) > 1e-9 || math.Abs(got[other]-0.1) > 1e-9 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("全空按均匀分布兜底（不凭空 100%）", func(t *testing.T) {
		got := normalizeProbs(nil, cands, key, 0)
		if math.Abs(got[key]-0.5) > 1e-9 || math.Abs(got[other]-0.5) > 1e-9 {
			t.Fatalf("均匀兜底应为 1/n, got %v", got)
		}
	})
	t.Run("conf 与概率全缺三候选均匀 1/3", func(t *testing.T) {
		three := append(append([]Candidate{}, cands...), Candidate{NodeID: "n3", Agent: "c3"})
		got := normalizeProbs(map[string]float64{}, three, key, 0)
		for _, k := range []string{key, other, "c3@n3"} {
			if math.Abs(got[k]-1.0/3.0) > 1e-9 {
				t.Fatalf("应为均匀 1/3, got %v", got)
			}
		}
	})
	t.Run("conf 缺失且概率无 choice 键 → 均匀低置信兜底", func(t *testing.T) {
		// 旧实现此处会产出 0 置信建议（choice 概率 0 仍当选）；现均匀兜底。
		got := normalizeProbs(map[string]float64{other: 3}, cands, key, 0)
		if math.Abs(got[key]-0.5) > 1e-9 || math.Abs(got[other]-0.5) > 1e-9 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("部分缺失按显式均值填补", func(t *testing.T) {
		three := append(append([]Candidate{}, cands...), Candidate{NodeID: "n3", Agent: "c3"})
		// conf=0.5，显式只给 other=2 → c3 缺失按均值 2 填补 → 各占 (1-0.5)/2。
		got := normalizeProbs(map[string]float64{other: 2}, three, key, 0.5)
		if math.Abs(got[key]-0.5) > 1e-9 || math.Abs(got[other]-0.25) > 1e-9 || math.Abs(got["c3@n3"]-0.25) > 1e-9 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("单候选分布恒为 1", func(t *testing.T) {
		got := normalizeProbs(nil, cands[:1], key, 0)
		if got[key] != 1 {
			t.Fatalf("唯一候选置信应为 1（事实非造假）, got %v", got)
		}
	})
	t.Run("conf 越界钳制", func(t *testing.T) {
		got := normalizeProbs(nil, cands, key, 7)
		if got[key] != 1 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("和为 1", func(t *testing.T) {
		got := normalizeProbs(map[string]float64{key: 0.7, other: 0.9}, cands, key, 0.7)
		sum := 0.0
		for _, v := range got {
			sum += v
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Fatalf("和应为 1, got %v (sum %v)", got, sum)
		}
	})
}

func TestClamp01(t *testing.T) {
	cases := map[float64]float64{0.5: 0.5, -1: 0, 2: 1, math.NaN(): 0, 0: 0, 1: 1}
	for in, want := range cases {
		if got := clamp01(in); math.Abs(got-want) > 1e-9 {
			t.Fatalf("clamp01(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestServiceConfidenceFromNormalizedProbs(t *testing.T) {
	// confidence 与归一后 probabilities[choice] 一致（Service 单一权威）。
	mp := &mockProvider{sug: &Suggestion{TargetNode: "win-box", TargetAgent: "diskcheck",
		Confidence: 5, Probabilities: map[string]float64{"diskcheck@win-box": 1, "zcode-dev@mac-mini": 3}}}
	s := NewService(mp, time.Second)
	res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion.Confidence != res.Suggestion.Probabilities["diskcheck@win-box"] {
		t.Fatalf("confidence 应等于归一后概率: %+v", res.Suggestion)
	}
	if res.Suggestion.Confidence <= 0 || res.Suggestion.Confidence > 1 {
		t.Fatalf("confidence 应在 [0,1]: %v", res.Suggestion.Confidence)
	}
}

func TestSortCandidates(t *testing.T) {
	cands := []Candidate{
		{NodeID: "b", Agent: "y"}, {NodeID: "a", Agent: "z"}, {NodeID: "a", Agent: "x"},
	}
	SortCandidates(cands)
	if cands[0].Key() != "x@a" || cands[1].Key() != "z@a" || cands[2].Key() != "y@b" {
		t.Fatalf("稳定序不符: %v", cands)
	}
}
