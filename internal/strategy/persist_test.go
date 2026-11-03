package strategy

// W09 收尾加固 A（Engine.RestoreLoop 快照持久化接线）DoD 单测：
//   - OnPersist 触发计数：变更即触发、读方法零触发、幂等命中零触发、校验拒绝零
//     触发、RestoreLoop 例外零触发（有意不回写——见 RestoreLoop 注记）；
//   - 持久化文件往返：SaveLoops 写 → 新引擎 LoadLoops/RestoreLoop → 候选/版本/
//     问题/trail 逐字段等价（JSON 字节级对照）+ 判定语义一致；
//   - 损坏 JSON / 缺 version / 未知 version / 单条目非法 / 重复条目 → 拒绝；
//   - OnPersist nil 零行为变化（显式锚；全仓既有包绿为广锚）；
//   - 死锁红线：回调内取读锁（模拟持久化 worker 组包）——触发点若回归写锁内同
//     步回调即自死锁，矩阵测试与超时探针双重暴露。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// persistCounter OnPersist 触发计数（回调内先取读锁组包——模拟 NewPersistedEngine
// 的 SaveLoops 路径 LoopIDs/Snapshot；若通知回归写锁内同步触发则自死锁，测试挂起
// 即暴露）。
type persistCounter struct {
	mu    sync.Mutex
	eng   *Engine
	count int
}

func (c *persistCounter) onTap() {
	if c.eng != nil {
		c.eng.LoopIDs()
		c.eng.Snapshot("sp-a")
	}
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
}

func (c *persistCounter) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// newCountingEngine 带计数通知的引擎（固定 UTC 时钟）。
func newCountingEngine(t *testing.T) (*Engine, *persistCounter) {
	t.Helper()
	cnt := &persistCounter{}
	cfg := DefaultConfig()
	cfg.Now = newFakeClock().Now
	cfg.OnPersist = cnt.onTap
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	cnt.eng = eng
	return eng, cnt
}

func wantCount(t *testing.T, cnt *persistCounter, want int, ctx string) {
	t.Helper()
	if got := cnt.n(); got != want {
		t.Fatalf("%s：OnPersist 触发计数应 %d，得 %d", ctx, want, got)
	}
}

// TestOnPersistTriggerMatrix 触发矩阵：写入方法变更即触发（含去重重申、重报等
// 隐性变更）；幂等命中/校验拒绝/未命中零触发；读方法零触发；RestoreLoop 零触发
// （有意例外，锚定其注记声明）。
func TestOnPersistTriggerMatrix(t *testing.T) {
	eng, cnt := newCountingEngine(t)
	const sp = "sp-a"

	// ---- 写入方法：变更即触发 ----
	hash := mustRegisterRevision(t, eng, sp, "v1", 1) // 新版本登记
	wantCount(t, cnt, 1, "RegisterRevision 新建")
	if _, _, err := eng.RegisterRevision(sp, RevisionInput{
		Baseline: "baseline-v1", ContentSHA256: hash, AuthorRound: 1, Author: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 1, "RegisterRevision 幂等命中零触发")

	mustEvidence(t, eng, sp, hash) // 四层证据各一次变更
	wantCount(t, cnt, 5, "RecordEvidence ×4")

	id := mustIssue(t, eng, sp, hash, SeverityMust) // 问题登记
	wantCount(t, cnt, 6, "RecordIssue 新建")
	if _, err := eng.RecordIssue(sp, IssueInput{
		Reviewer: "bob", Severity: SeverityMust, File: "internal/foo/bar.go", Line: 42,
		Title: "边界检查缺失", FoundInHash: hash, FoundInRound: 1,
	}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 7, "RecordIssue 同 key 重报（计数推进+留痕=变更）")

	if err := eng.ReplyIssue(sp, id, "本轮处理"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 8, "ReplyIssue")
	if err := eng.ClaimFix(sp, id, hash, 1, "已修"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 9, "ClaimFix")
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewConfirmed}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 10, "RecordReReview")

	cand, created, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 采用方案一", Round: 1})
	if err != nil || !created {
		t.Fatalf("候选登记: created=%v err=%v", created, err)
	}
	wantCount(t, cnt, 11, "RegisterCandidate 新建")
	if _, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 采用方案一", Round: 2}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 12, "RegisterCandidate 去重重申（SeenCount++ + 留痕=变更）")
	if _, err := eng.FinalizeCandidate(sp, cand.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 13, "FinalizeCandidate 迁移")
	if _, err := eng.FinalizeCandidate(sp, cand.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 13, "FinalizeCandidate 幂等命中零触发")
	if _, err := eng.DismissCandidate(sp, cand.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 14, "DismissCandidate finalized→dismissed 迁移")
	if _, err := eng.DismissCandidate(sp, cand.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 14, "DismissCandidate 幂等命中零触发")

	eng.SetAcceptanceDispute(sp, true, "验收口径分歧")
	wantCount(t, cnt, 15, "SetAcceptanceDispute 登记")
	eng.SetAcceptanceDispute(sp, true, "口径修订")
	wantCount(t, cnt, 16, "SetAcceptanceDispute 重登记（覆盖+留痕=变更）")
	eng.SetAcceptanceDispute(sp, false, "")
	wantCount(t, cnt, 17, "SetAcceptanceDispute 解除既有争议")
	eng.SetAcceptanceDispute(sp, false, "")
	wantCount(t, cnt, 17, "SetAcceptanceDispute 无争议解除幂等零触发")

	if _, err := eng.BootstrapCycle(fixtureSpace("sp-b", "", 0)); err != nil { // 未初始化 → 引导
		t.Fatalf("BootstrapCycle 引导: %v", err)
	}
	wantCount(t, cnt, 18, "BootstrapCycle 引导")
	if _, err := eng.BootstrapCycle(fixtureSpace("sp-b", string(PhaseDrafting), 0)); err != nil {
		t.Fatalf("BootstrapCycle 幂等: %v", err)
	}
	wantCount(t, cnt, 18, "BootstrapCycle 已初始化幂等零触发")

	if err := eng.SetPhase(sp, PhaseDrafting, PhaseFrozen); err != nil {
		t.Fatal(err)
	}
	wantCount(t, cnt, 19, "SetPhase 迁移")

	// ---- 校验拒绝/未命中：零触发 ----
	base := cnt.n()
	if err := eng.RecordEvidence(sp, mkhash("nope"), LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "x")); err == nil {
		t.Fatal("未登记版本登记证据应拒绝")
	}
	if _, err := eng.RecordIssue(sp, IssueInput{Reviewer: "bob", Severity: SeverityMust, File: "f.go", Line: 1, Title: "t", FoundInHash: mkhash("nope")}); err == nil {
		t.Fatal("未登记版本登记问题应拒绝")
	}
	if err := eng.ReplyIssue(sp, "iss-none", "n"); err == nil {
		t.Fatal("未知问题回应应拒绝")
	}
	if err := eng.ClaimFix(sp, "iss-none", hash, 1, "n"); err == nil {
		t.Fatal("未知问题修复声明应拒绝")
	}
	if err := eng.RecordReReview(sp, "iss-none", ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewConfirmed}); err == nil {
		t.Fatal("未知问题复审应拒绝")
	}
	if err := eng.RecordCleanReview(sp, "bob", mkhash("nope"), ""); err == nil {
		t.Fatal("未登记版本零发现复审应拒绝")
	}
	if err := eng.SetPhase(sp, PhaseCompleted, PhaseDrafting); err == nil {
		t.Fatal("表外阶段转换应拒绝")
	}
	wantCount(t, cnt, base, "校验拒绝/未命中路径零触发")

	// ---- 读方法：零触发 ----
	base = cnt.n()
	if _, ok := eng.Snapshot(sp); !ok {
		t.Fatal("快照缺失")
	}
	_ = eng.PendingCandidates(sp)
	_ = eng.CurrentRevisionHash(sp)
	_ = eng.LoopIDs()
	_, _ = eng.RecordsSince(sp, 0)
	spView := fixtureSpace(sp, string(PhaseFrozen), 0)
	_ = eng.CompletionGaps(spView)
	_ = eng.EvaluateCompletion(spView)
	_ = eng.DecidePhaseAdvance(spView)
	_ = eng.EvaluateStop(spView, newFakeClock().Now())
	_ = eng.Projection(sp, spView, newFakeClock().Now())
	_ = eng.ValidatePhaseTransition(sp, PhaseDrafting, PhaseFrozen)
	_ = eng.NextSpeaker(spView, newFakeClock().Now(), map[string]int64{}, DiscussionPolicy{}, nil)
	_, _ = eng.PickSpeakers(spView, map[string]int64{}, DiscussionPolicy{}, []string{"alice"})
	_ = eng.ValidateSpeaker(spView, map[string]int64{}, DiscussionPolicy{}, "alice")
	if err := eng.RestoreLoop("sp-r", SpaceLoop{SpaceID: "sp-r"}); err != nil {
		t.Fatalf("空快照恢复: %v", err)
	}
	wantCount(t, cnt, base, "读方法与 RestoreLoop 零触发（RestoreLoop 有意不回写，见其注记）")
}

// TestOnPersistNilZeroBehavior OnPersist nil = 零操作：写入照常、判定照常（既有
// 28 包全绿为广锚，本锚显式声明该契约）。
func TestOnPersistNilZeroBehavior(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-nil"
	hash := mustRegisterRevision(t, eng, sp, "v1", 1)
	mustEvidence(t, eng, sp, hash)
	id := mustIssueFixed(t, eng, sp, hash, SeverityMust)
	_ = id
	mustCandidateFinalized(t, eng, sp, "FINAL: 采用方案一")
	snap, ok := eng.Snapshot(sp)
	if !ok || len(snap.Revisions) != 1 || len(snap.Candidates) != 1 {
		t.Fatalf("nil OnPersist 行为应与既有逐字节一致: %+v", snap)
	}
}

// TestOnPersistFiresAfterWriteUnlock 死锁红线探针：OnPersist 回调内取读锁
// （Snapshot）——触发点若在写锁内同步回调则 RWMutex 自死锁，5s 超时即判失败。
// 回调必须在 NewEngine **构造前**装入 Config（引擎按值拷贝配置——构造后改 cfg
// 不生效，探针会假绿：W09-A codex 复审必修2）；并显式断言回调确已触发（通知被
// 整体摘除时同样判负——探针必须双向有效）。
func TestOnPersistFiresAfterWriteUnlock(t *testing.T) {
	var eng *Engine
	fired := make(chan struct{}, 8)
	cfg := DefaultConfig()
	cfg.OnPersist = func() {
		eng.Snapshot("sp-dl") // 读锁组包：写锁内同步回调 = 死锁面
		eng.LoopIDs()
		fired <- struct{}{}
	}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eng = e
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := eng.SetPhase("sp-dl", PhaseDrafting, PhaseFrozen); err != nil {
			t.Errorf("SetPhase: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OnPersist 在写锁内同步触发 → 回调取读锁自死锁（红线：触发点必须在 e.mu 写锁释放之后）")
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("OnPersist 回调未触发（探针未安装或通知被摘除——探针假绿防线）")
	}
}

// TestPersistFileRoundTrip 持久化往返（DoD：写 → 新引擎 RestoreLoop → 候选/版本/
// 问题/trail 逐字段等价）：覆盖全部恢复字段的丰富事实，JSON 字节级对照 + 判定语
// 义一致；文件 0600、version=1、时间 UTC（RFC3339 Z）。
func TestPersistFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	cfg := DefaultConfig()
	cfg.Now = clk.Now
	eng, err := NewPersistedEngine(dir, cfg, nil)
	if err != nil {
		t.Fatalf("NewPersistedEngine: %v", err)
	}
	// 缺文件 = 零操作空态（老部署/首次启动）。
	if ids := eng.LoopIDs(); len(ids) != 0 {
		t.Fatalf("缺文件应零操作空态: %v", ids)
	}

	const sp = "sp-rt"
	hash := mustRegisterRevision(t, eng, sp, "v1", 1)
	mustEvidence(t, eng, sp, hash)
	mustIssueFixed(t, eng, sp, hash, SeverityMust)
	mustCandidateFinalized(t, eng, sp, "FINAL: 采用方案一")
	shouldID, err := eng.RecordIssue(sp, IssueInput{
		Reviewer: "bob", Severity: SeverityShould, File: "internal/foo/baz.go", Line: 7,
		Title: "命名不一致", FoundInHash: hash, FoundInRound: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReplyIssue(sp, shouldID.ID, "本轮处理"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordCleanReview(sp, "bob", hash, "补一轮零发现复审"); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetPhase(sp, PhaseReview, PhasePaused); err != nil {
		t.Fatal(err)
	}
	eng.SetAcceptanceDispute(sp, true, "验收口径分歧")
	snapA, ok := eng.Snapshot(sp)
	if !ok {
		t.Fatal("快照缺失")
	}

	// 文件形态：version=1、单 loops 数组、0600、UTC（Z 后缀 RFC3339）。
	path := filepath.Join(dir, LoopStoreFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("持久化文件缺失（变更即同步写）: %v", err)
	}
	var doc struct {
		Version int               `json:"version"`
		Loops   []json.RawMessage `json:"loops"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("文件应合法 JSON: %v", err)
	}
	if doc.Version != 1 || len(doc.Loops) != 1 {
		t.Fatalf("文件形态应 {version:1,loops:[1 条]}: version=%d loops=%d", doc.Version, len(doc.Loops))
	}
	if fi, serr := os.Stat(path); serr != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限应 0600: %v %v", fi, serr)
	}
	if !strings.Contains(string(data), `"2026-11-01T08:00:00Z"`) {
		t.Fatalf("时间序列化应 UTC RFC3339 Z 形态: %s", data)
	}

	// 往返：新引擎（独立实例）读回 → 逐字段等价（JSON 字节对照 = 序列化面逐字段）。
	eng2, err := NewPersistedEngine(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("恢复启动: %v", err)
	}
	if ids := eng2.LoopIDs(); !reflect.DeepEqual(ids, []string{sp}) {
		t.Fatalf("恢复后 LoopIDs 应 [%s]: %v", sp, ids)
	}
	snapB, ok := eng2.Snapshot(sp)
	if !ok {
		t.Fatal("恢复后快照缺失")
	}
	ja, _ := json.Marshal(snapA)
	jb, _ := json.Marshal(snapB)
	if !bytes.Equal(ja, jb) {
		t.Fatalf("往返快照须逐字段等价：\n%s\n---\n%s", ja, jb)
	}
	// 深等价以序列化形态为准（持久化契约面）：内存快照的空切片可能为非 nil
	// （如 RegisterRevision 组包 make(...,0,n)），JSON 往返 omitempty 归一为 nil
	// ——值面差异不属于文件契约；深比较取 snapA 的 JSON 往返副本对照 snapB。
	var snapA2 SpaceLoop
	if err := json.Unmarshal(ja, &snapA2); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapA2, snapB) {
		t.Fatal("往返快照须深等价（序列化形态）")
	}

	// 语义等价：完成/停止/阶段推进判定一致（对照 restore_test 同族断言）。
	spacePaused := fixtureSpace(sp, string(PhasePaused), 0)
	vA, vB := eng.EvaluateCompletion(spacePaused), eng2.EvaluateCompletion(spacePaused)
	if vA.Satisfied != vB.Satisfied || !vB.Satisfied {
		t.Fatalf("完成判定不一致: A=%v B=%v（%s / %s）", vA.Satisfied, vB.Satisfied, vA.Reason, vB.Reason)
	}
	now := clk.Now()
	evA := eng.EvaluateStop(fixtureSpace(sp, string(PhasePaused), 0), now)
	evB := eng2.EvaluateStop(fixtureSpace(sp, string(PhasePaused), 0), now)
	if evA.ShouldPause != evB.ShouldPause || !evB.ShouldPause {
		t.Fatalf("停止判定不一致（争议触发）: A=%+v B=%+v", evA, evB)
	}
	dA := eng.DecidePhaseAdvance(fixtureSpace(sp, string(PhaseReview), 0))
	dB := eng2.DecidePhaseAdvance(fixtureSpace(sp, string(PhaseReview), 0))
	if dA.Advance != dB.Advance || dA.NextPhase != dB.NextPhase {
		t.Fatalf("阶段推进判定不一致: A=%+v B=%+v", dA, dB)
	}

	// 往返后再写入：恢复态引擎续写正常（续号自 FactSeq 高水位，不撞不混序）。
	if err := eng2.SetPhase(sp, PhasePaused, PhaseReview); err != nil {
		t.Fatalf("恢复态续写: %v", err)
	}
	after, ok := eng2.Snapshot(sp)
	if !ok || after.FactSeq != snapB.FactSeq+1 {
		t.Fatalf("续写应自高水位 +1: %d vs %d", after.FactSeq, snapB.FactSeq)
	}
	if _, err := NewPersistedEngine(dir, DefaultConfig(), nil); err != nil {
		t.Fatalf("续写产物应可再恢复: %v", err)
	}
}

// TestSaveLoopsDeterministicOrder loops 按空间 ID 排序（文件内容确定性）。
func TestSaveLoopsDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	eng := newTestEngine(t)
	for _, sp := range []string{"sp-c", "sp-a", "sp-b"} {
		if _, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 方案", Round: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveLoops(dir, eng); err != nil {
		t.Fatalf("SaveLoops: %v", err)
	}
	var doc loopStoreFile
	data, err := os.ReadFile(filepath.Join(dir, LoopStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Loops) != 3 || doc.Loops[0].SpaceID != "sp-a" || doc.Loops[1].SpaceID != "sp-b" || doc.Loops[2].SpaceID != "sp-c" {
		t.Fatalf("loops 应按空间 ID 升序: %+v", doc.Loops)
	}
}

// TestPersistFileRejects 拒绝面（同 spaces.json 宁拒启口径）：损坏 JSON / 缺
// version / 未知 version / 单条目非法（错误列明空间 ID 与原因）/ 重复条目。
func TestPersistFileRejects(t *testing.T) {
	mk := func(entries ...string) string {
		return `{"version":1,"loops":[` + strings.Join(entries, ",") + `]}`
	}
	badTime := "2026-11-01T08:00:00Z"
	cases := []struct {
		name string
		data string
		sub  []string // 错误信息须逐个包含的子串
	}{
		{"损坏 JSON", "{not json", []string{"损坏"}},
		{"缺 version", `{"loops":[]}`, []string{"version"}},
		{"未知 version", `{"version":2,"loops":[]}`, []string{"version"}},
		{"条目留痕序号倒挂", mk(fmt.Sprintf(
			`{"space_id":"sp-x","fact_seq":2,"trail":[{"seq":2,"at":%[1]q,"kind":"phase_changed"},{"seq":1,"at":%[1]q,"kind":"phase_changed"}]}`, badTime)),
			[]string{"sp-x", "倒挂"}},
		{"条目候选 ID 非法", mk(fmt.Sprintf(
			`{"space_id":"sp-y","candidates":[{"id":"junk","summary":"s","proposer":"a","round":1,"final_text":"FINAL: x","registered_at":%[1]q,"status":"pending","seen_count":1}]}`, badTime)),
			[]string{"sp-y", "候选 ID 非法或重复"}},
		{"条目缺空间 ID", mk(`{"candidates":[]}`), []string{"恢复目标空间 ID 必填"}},
		{"重复空间条目", mk(`{"space_id":"sp-z"}`, `{"space_id":"sp-z"}`), []string{"sp-z", "已存在"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, LoopStoreFile), []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			eng, err := NewPersistedEngine(dir, DefaultConfig(), nil)
			if err == nil {
				t.Fatal("损坏/非法文件应拒绝启动")
			}
			if eng != nil {
				t.Fatal("拒绝启动时不得返回引擎")
			}
			for _, s := range tc.sub {
				if !strings.Contains(err.Error(), s) {
					t.Fatalf("错误信息须列明 %q: %v", s, err)
				}
			}
		})
	}
	// 文件读取失败（目录不可读形态替代：路径是目录）。
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, LoopStoreFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistedEngine(dir, DefaultConfig(), nil); err == nil {
		t.Fatal("文件不可读应拒绝启动")
	}
}

// TestSaveLoopsConcurrentConverges 并发变更各自触发同步落盘（-race 面）：落盘串
// 行化后每次写都是完整快照，终态读回与内存一致。
func TestSaveLoopsConcurrentConverges(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	var eng *Engine
	var failMu sync.Mutex
	var fails []string
	cfg.OnPersist = func() {
		if err := SaveLoops(dir, eng); err != nil {
			failMu.Lock()
			fails = append(fails, err.Error())
			failMu.Unlock()
		}
	}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eng = e

	const gs, perG = 4, 25
	var wg sync.WaitGroup
	for g := 0; g < gs; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sp := fmt.Sprintf("sp-g%d", g)
			for i := 0; i < perG; i++ {
				if _, _, err := eng.RegisterCandidate(sp, CandidateInput{
					Proposer: "alice", Body: fmt.Sprintf("FINAL: 方案 %d-%d", g, i), Round: int64(i),
				}); err != nil {
					t.Errorf("RegisterCandidate: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	failMu.Lock()
	defer failMu.Unlock()
	if len(fails) > 0 {
		t.Fatalf("并发落盘失败 %d 次: %v", len(fails), fails[0])
	}
	eng2, err := NewPersistedEngine(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("终态读回: %v", err)
	}
	ids := eng2.LoopIDs()
	want := []string{"sp-g0", "sp-g1", "sp-g2", "sp-g3"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("恢复空间应 %v: %v", want, ids)
	}
	for _, sp := range want {
		if got := len(eng2.PendingCandidates(sp)); got != perG {
			t.Fatalf("%s 恢复候选应 %d 条: %d", sp, perG, got)
		}
	}
}

// TestNoEmptyLoopOnLookupMiss 零建 loop 契约回归（W09-A codex 复审必修3）：
// 查找锚定写路径的失败/无操作分支不得创建空循环条目——「建而不通知」会让
// LoopIDs 与落盘文件失配（空引擎 LoopIDs 已含该空间、重启后条目消失）。
func TestNoEmptyLoopOnLookupMiss(t *testing.T) {
	cfg := DefaultConfig()
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 各查找拒绝路径（目标项不存在——空间未载入）。
	const h = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := eng.ReplyIssue("sp-x", "issue-1", "回应"); err == nil {
		t.Fatal("未知问题应拒绝")
	}
	if err := eng.ClaimFix("sp-x", "issue-1", h, 0, ""); err == nil {
		t.Fatal("未知问题修复声明应拒绝")
	}
	if _, err := eng.FinalizeCandidate("sp-x", "cand-1", "admin"); err == nil {
		t.Fatal("未知候选应拒绝")
	}
	if err := eng.RecordEvidence("sp-x", h, LayerCLIExit, Evidence{Verified: true, Source: "cli", Ref: "ref-1"}); err == nil {
		t.Fatal("未知版本证据登记应拒绝")
	}
	if err := eng.RecordCleanReview("sp-x", "admin", h, "n"); err == nil {
		t.Fatal("未知版本零发现复审应拒绝")
	}
	// 验收争议解除（本无争议）＝零变更零副作用。
	eng.SetAcceptanceDispute("sp-x", false, "")
	if got := eng.LoopIDs(); len(got) != 0 {
		t.Fatalf("查找拒绝/无操作路径不得创建空循环条目，LoopIDs=%v", got)
	}
	// 真实落事实路径照常建条目并通知（对照面）。
	eng.SetAcceptanceDispute("sp-y", true, "验收存疑")
	if got := eng.LoopIDs(); len(got) != 1 || got[0] != "sp-y" {
		t.Fatalf("争议登记应建条目，LoopIDs=%v", got)
	}
}

// TestSnapshotsAllSingleLockConsistency SnapshotsAll 一致快照压力（W09-A codex
// 复审必修1 结构锚）：并发写入与落盘交错下（-race 抖动），落盘文件内容必须恒为
// 内存某时点的完整合法形态——分阶段断言不可行，以压力+race 抖动 + 静默后文件与
// 引擎状态逐字节等价收口（分段取照的 A₀+B₁ 组合形态由单读锁结构性排除）。
func TestSnapshotsAllSingleLockConsistency(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	var eng *Engine
	cfg.OnPersist = func() { _ = SaveLoops(dir, eng) }
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eng = e
	const spaces = 4
	done := make(chan struct{})
	for g := 0; g < spaces; g++ {
		sp := fmt.Sprintf("sp-c%d", g)
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 30; i++ {
				eng.SetAcceptanceDispute(sp, i%2 == 0, "压测")
			}
		}()
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = SaveLoops(dir, eng)
			}
		}
	}()
	for g := 0; g < spaces; g++ {
		<-done
	}
	close(stop)
	// 静默后：文件与引擎当前状态逐字节等价（SaveLoops 幂等重写同文）。
	if err := SaveLoops(dir, eng); err != nil {
		t.Fatalf("落盘: %v", err)
	}
	b1, err := os.ReadFile(filepath.Join(dir, LoopStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	eng2, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := LoadLoops(dir, eng2); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if err := SaveLoops(dir, eng2); err != nil {
		t.Fatalf("重写: %v", err)
	}
	b2, err := os.ReadFile(filepath.Join(dir, LoopStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("落盘→恢复→再落盘应逐字节等价（一致快照契约）")
	}
}
